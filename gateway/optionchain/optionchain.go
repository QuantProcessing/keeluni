// Package optionchain is the gateway's resident option-chain dataset: every
// listed option of every served options market, read whole from the venue
// on a cadence and published per series (an underlying in one premium
// currency) as a REST read and a retained WebSocket topic.
//
// Normalization, the same for every venue:
//   - Prices (bid, ask, mark, last) stay in the series' premium asset, as
//     the venue quotes and the order takes them. Inverse options (Deribit
//     BTC, ETH) quote in the coin.
//   - IVs are annualized fractions. Bid and ask IVs are implied from the
//     prices against the expiry's forward when the venue does not publish
//     them.
//   - Greeks are Black-76 on the forward with the mark IV, per one unit of
//     the underlying, in the strike currency (USD): delta in units, gamma
//     per 1.0 move, vega per vol point, theta per calendar day. They match
//     Deribit's published greeks to the fourth decimal and are computed the
//     same way for venues that publish none.
//
// Per expiry the dataset reports the forward, the ATM IV (interpolated at
// the forward in log-moneyness over out-of-the-money options) and the 25-delta
// risk reversal and butterfly. These are facts of one expiry's marks; any
// relation across positions or instruments belongs to an app.
package optionchain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/hub"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/resident"
	"github.com/QuantProcessing/keeluni/gateway/venue"
	"github.com/QuantProcessing/keeluni/optionmath"
)

// Channel is the per-series WebSocket channel: {"ch":"options","inst":<series>}.
const Channel = "options"

const datasetName = "options"

// Cadence is how often each options market's chain is read.
var Cadence = 10 * time.Second

var (
	ErrUnknownSeries = errors.New("unknown option series")
	ErrNoChain       = errors.New("option chain not loaded yet")
)

// Row is one option of a chain.
type Row struct {
	Instrument venue.InstrumentID `json:"inst"`
	Kind       string             `json:"kind"` // call | put
	Strike     string             `json:"strike"`
	Expiry     int64              `json:"expiry"`
	// Prices in the series' premium asset; empty when the venue has none.
	Bid  string `json:"bid,omitempty"`
	Ask  string `json:"ask,omitempty"`
	Mark string `json:"mark,omitempty"`
	Last string `json:"last,omitempty"`
	// IVs are fractions; 0 when unknown.
	MarkIV float64 `json:"markIv,omitempty"`
	BidIV  float64 `json:"bidIv,omitempty"`
	AskIV  float64 `json:"askIv,omitempty"`
	// Greeks per unit of the underlying in the strike currency.
	Delta        float64 `json:"delta"`
	Gamma        float64 `json:"gamma"`
	Vega         float64 `json:"vega"`
	Theta        float64 `json:"theta"`
	OpenInterest string  `json:"oi,omitempty"`
	Volume       string  `json:"volume,omitempty"`

	forward, discount float64
}

// Expiry summarizes one expiry of a series. Forward and Discount are the
// expiry's valuation model: an option is worth Discount × Black-76(Forward)
// — Discount is 1 on venues that publish a forward and e^(−rT) on venues
// that value on spot with a rate.
type Expiry struct {
	Expiry   int64   `json:"expiry"`
	Forward  float64 `json:"forward,omitempty"`
	Discount float64 `json:"discount,omitempty"`
	ATMIV    float64 `json:"atmIv,omitempty"`
	// RR25 is the 25-delta call IV minus the 25-delta put IV; BF25 their
	// average minus the ATM IV. Zero when the wings do not reach 25 delta.
	RR25    float64 `json:"rr25,omitempty"`
	BF25    float64 `json:"bf25,omitempty"`
	Strikes int     `json:"strikes"`
}

// Chain is one series: an underlying's options in one premium asset.
type Chain struct {
	Series       string         `json:"series"`
	Venue        exchange.Venue `json:"venue"`
	Underlying   string         `json:"underlying"`
	PremiumAsset string         `json:"premiumAsset"`
	// StrikeAsset is the currency strikes and greeks are in.
	StrikeAsset string   `json:"strikeAsset"`
	Inverse     bool     `json:"inverse,omitempty"`
	Index       float64  `json:"index,omitempty"`
	UpdatedAt   int64    `json:"updatedAt"`
	Stale       bool     `json:"stale"`
	Expiries    []Expiry `json:"expiries"`
	Rows        []Row    `json:"rows"`

	market venue.Market
}

// Series is a chain's header, for the series list.
type Series struct {
	Series       string         `json:"series"`
	Venue        exchange.Venue `json:"venue"`
	Market       string         `json:"market"`
	Underlying   string         `json:"underlying"`
	PremiumAsset string         `json:"premiumAsset"`
	StrikeAsset  string         `json:"strikeAsset"`
	Inverse      bool           `json:"inverse,omitempty"`
	Index        float64        `json:"index,omitempty"`
	Expiries     int            `json:"expiries"`
	Contracts    int            `json:"contracts"`
	// FrontATMIV is the nearest expiry's ATM IV (beyond one day).
	FrontATMIV float64 `json:"frontAtmIv,omitempty"`
	Stale      bool    `json:"stale"`
}

// SeriesID is "venue:UNDERLYING:PREMIUM", e.g. "deribit:BTC:BTC".
func SeriesID(venueName exchange.Venue, underlying, premium string) string {
	return string(venueName) + ":" + underlying + ":" + premium
}

// SeriesOf names the series of an option instrument.
func SeriesOf(instrument market.Instrument) (string, bool) {
	if instrument.Option == nil {
		return "", false
	}
	return SeriesID(instrument.Venue, instrument.Option.Underlying, instrument.Option.PremiumAsset), true
}

// Source reads one options market's whole chain.
type Source interface {
	OptionChain(ctx context.Context, market venue.Market, underlying string) ([]exchange.OptionQuote, bool, error)
}

type Dataset struct {
	source   Source
	catalog  *market.Catalog
	registry *resident.Registry
	markets  []venue.Market
	now      func() time.Time

	mu       sync.RWMutex
	chains   map[string]*Chain
	byMarket map[venue.Market][]string
	watchers map[string]map[*watcher]struct{}
}

type watcher struct{ emit hub.Emit }

func New(catalog *market.Catalog, registry *resident.Registry) *Dataset {
	dataset := newDataset(catalog, catalog, registry)
	for _, served := range venue.Markets {
		if served.Product == exchange.ProductOption {
			dataset.markets = append(dataset.markets, served)
			registry.Declare(datasetName, served.String(), resident.ModePoll, Cadence)
		}
	}
	return dataset
}

func newDataset(source Source, catalog *market.Catalog, registry *resident.Registry) *Dataset {
	return &Dataset{
		source: source, catalog: catalog, registry: registry, now: time.Now,
		chains: map[string]*Chain{}, byMarket: map[venue.Market][]string{}, watchers: map[string]map[*watcher]struct{}{},
	}
}

// Run polls every options market until ctx ends.
func (dataset *Dataset) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, served := range dataset.markets {
		wg.Add(1)
		go func(served venue.Market) {
			defer wg.Done()
			resident.Run(ctx, dataset.registry, resident.Spec[[]exchange.OptionQuote]{
				Dataset: datasetName, Market: served.String(), Interval: Cadence,
				Load: func(ctx context.Context) ([]exchange.OptionQuote, error) {
					quotes, ok, err := dataset.source.OptionChain(ctx, served, "")
					if err == nil && !ok {
						err = fmt.Errorf("%s serves no option chain", served)
					}
					return quotes, err
				},
				Apply: func(quotes []exchange.OptionQuote) { dataset.apply(served, quotes) },
			})
		}(served)
	}
	wg.Wait()
}

// apply rebuilds every series of one market from a whole-chain read and
// notifies each series' subscribers.
func (dataset *Dataset) apply(served venue.Market, quotes []exchange.OptionQuote) {
	now := dataset.now()
	chains := map[string]*Chain{}
	for _, quote := range quotes {
		identity := quote.Instrument.Identity
		instrument, err := dataset.catalog.Get(venue.NewInstrumentID(identity.Venue, identity.Product, identity.Symbol))
		if err != nil || instrument.Option == nil {
			continue
		}
		terms := quote.Instrument.Option
		id := SeriesID(identity.Venue, terms.Underlying, terms.PremiumAsset)
		chain := chains[id]
		if chain == nil {
			chain = &Chain{Series: id, Venue: identity.Venue, Underlying: terms.Underlying, PremiumAsset: terms.PremiumAsset,
				StrikeAsset: identity.QuoteAsset, Inverse: terms.Inverse, UpdatedAt: now.UnixMilli(), market: served}
			chains[id] = chain
		}
		if quote.IndexPrice.Valid && chain.Index == 0 {
			chain.Index = quote.IndexPrice.Value.InexactFloat64()
		}
		chain.Rows = append(chain.Rows, project(instrument, terms, quote, now))
	}
	for _, chain := range chains {
		summarize(chain, now)
	}
	ids := make([]string, 0, len(chains))
	for id := range chains {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	dataset.mu.Lock()
	for _, old := range dataset.byMarket[served] {
		if _, kept := chains[old]; !kept {
			delete(dataset.chains, old)
		}
	}
	for id, chain := range chains {
		dataset.chains[id] = chain
	}
	dataset.byMarket[served] = ids
	dataset.mu.Unlock()
	for _, id := range ids {
		dataset.publish(id)
	}
}

func decimalText(value exchange.OptionalDecimal) string {
	if !value.Valid {
		return ""
	}
	return value.Value.String()
}

func positive(value exchange.OptionalDecimal) float64 {
	if !value.Valid || !value.Value.IsPositive() {
		return 0
	}
	return value.Value.InexactFloat64()
}

// modelOf is the valuation model of one quote: on the venue's forward when
// it publishes one (Deribit, OKX), else on the index carried at the venue's
// rate (Binance).
func modelOf(quote exchange.OptionQuote, years float64) optionmath.Model {
	if forward := positive(quote.UnderlyingPrice); forward > 0 {
		return optionmath.OnForward(forward, years)
	}
	rate := 0.0
	if quote.Rate.Valid {
		rate = quote.Rate.Value.InexactFloat64()
	}
	return optionmath.OnSpot(positive(quote.IndexPrice), rate, years)
}

// project normalizes one quote.
func project(instrument market.Instrument, terms exchange.OptionTerms, quote exchange.OptionQuote, now time.Time) Row {
	call := terms.Kind == exchange.OptionCall
	row := Row{Instrument: instrument.ID, Kind: string(terms.Kind), Strike: terms.Strike.String(), Expiry: terms.Expiry.UnixMilli(),
		Bid: decimalText(quote.Bid), Ask: decimalText(quote.Ask), Mark: decimalText(quote.Mark), Last: decimalText(quote.Last),
		OpenInterest: decimalText(quote.OpenInterest), Volume: decimalText(quote.Volume24h)}
	strike := terms.Strike.InexactFloat64()
	years := optionmath.Years(now, terms.Expiry)
	model := modelOf(quote, years)
	forward := model.Forward
	row.forward, row.discount = forward, model.Discount
	// usd converts a premium into the strike currency.
	usd := func(price float64) float64 {
		if terms.Inverse {
			return price * forward
		}
		return price
	}
	implied := func(price float64) float64 {
		if price <= 0 || forward <= 0 {
			return 0
		}
		vol, ok := model.ImpliedVol(call, strike, usd(price))
		if !ok {
			return 0
		}
		return vol
	}
	row.MarkIV = positive(quote.MarkIV)
	if row.MarkIV == 0 {
		row.MarkIV = implied(positive(quote.Mark))
	}
	row.BidIV = positive(quote.BidIV)
	if row.BidIV == 0 {
		row.BidIV = implied(positive(quote.Bid))
	}
	row.AskIV = positive(quote.AskIV)
	if row.AskIV == 0 {
		row.AskIV = implied(positive(quote.Ask))
	}
	if forward > 0 {
		greeks := model.Greeks(call, strike, row.MarkIV)
		row.Delta, row.Gamma, row.Vega, row.Theta = round(greeks.Delta, 5), sig(greeks.Gamma), round(greeks.Vega, 4), round(greeks.Theta, 4)
	}
	row.MarkIV, row.BidIV, row.AskIV = round(row.MarkIV, 5), round(row.BidIV, 5), round(row.AskIV, 5)
	return row
}

// round rounds to places; a non-finite value (an option moments from
// expiry can produce one) is 0, so the chain always encodes.
func round(value float64, places int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}

// sig keeps six significant digits (gamma is tiny for high-priced coins).
func sig(value float64) float64 {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	magnitude := math.Floor(math.Log10(math.Abs(value)))
	return round(value, int(5-magnitude))
}

// expiryPoint is one strike's out-of-the-money IV and the delta it carries.
type expiryPoint struct {
	strike, moneyness, iv, callIV, putIV, callDelta, putDelta float64
}

// summarize sorts rows (expiry, strike, call before put) and computes each
// expiry's forward, ATM IV and 25-delta skew.
func summarize(chain *Chain, now time.Time) {
	sort.Slice(chain.Rows, func(i, j int) bool {
		a, b := chain.Rows[i], chain.Rows[j]
		if a.Expiry != b.Expiry {
			return a.Expiry < b.Expiry
		}
		as, bs := parseFloat(a.Strike), parseFloat(b.Strike)
		if as != bs {
			return as < bs
		}
		return a.Kind < b.Kind
	})
	var start int
	for start < len(chain.Rows) {
		end := start
		for end < len(chain.Rows) && chain.Rows[end].Expiry == chain.Rows[start].Expiry {
			end++
		}
		rows := chain.Rows[start:end]
		chain.Expiries = append(chain.Expiries, summarizeExpiry(rows))
		start = end
	}
}

func parseFloat(text string) float64 {
	var value float64
	_, _ = fmt.Sscan(text, &value)
	return value
}

// summarizeExpiry: the forward is the venue's (the median the rows were
// valued against; Deribit gives every option of an expiry the same one).
func summarizeExpiry(rows []Row) Expiry {
	summary := Expiry{Expiry: rows[0].Expiry}
	byStrike := map[float64]*expiryPoint{}
	var strikes, forwards []float64
	for _, row := range rows {
		if row.forward > 0 {
			forwards = append(forwards, row.forward)
			summary.Discount = round(row.discount, 8)
		}
		strike := parseFloat(row.Strike)
		point := byStrike[strike]
		if point == nil {
			point = &expiryPoint{strike: strike}
			byStrike[strike] = point
			strikes = append(strikes, strike)
		}
		if row.Kind == "call" {
			point.callIV, point.callDelta = row.MarkIV, row.Delta
		} else {
			point.putIV, point.putDelta = row.MarkIV, row.Delta
		}
	}
	sort.Float64s(strikes)
	summary.Strikes = len(strikes)
	if len(forwards) == 0 {
		return summary
	}
	sort.Float64s(forwards)
	forward := forwards[len(forwards)/2]
	summary.Forward = round(forward, 2)
	var points []expiryPoint
	for _, strike := range strikes {
		point := *byStrike[strike]
		point.moneyness = math.Log(strike / forward)
		// Out-of-the-money IV: puts below the forward, calls above.
		if strike < forward {
			point.iv = point.putIV
		} else {
			point.iv = point.callIV
		}
		if point.iv > 0 {
			points = append(points, point)
		}
	}
	summary.ATMIV = round(interpolate(points, func(p expiryPoint) float64 { return p.moneyness }, 0), 5)
	callWing := make([]expiryPoint, 0, len(points))
	putWing := make([]expiryPoint, 0, len(points))
	for _, point := range points {
		if point.strike >= forward && point.callDelta > 0 {
			callWing = append(callWing, point)
		}
		if point.strike <= forward && point.putDelta < 0 {
			putWing = append(putWing, point)
		}
	}
	call25 := interpolate(callWing, func(p expiryPoint) float64 { return -p.callDelta }, -0.25)
	put25 := interpolate(putWing, func(p expiryPoint) float64 { return p.putDelta }, -0.25)
	if call25 > 0 && put25 > 0 && summary.ATMIV > 0 {
		summary.RR25 = round(call25-put25, 5)
		summary.BF25 = round((call25+put25)/2-summary.ATMIV, 5)
	}
	return summary
}

// interpolate returns the IV at x = target by linear interpolation over
// points ordered by x; 0 when target is outside their span.
func interpolate(points []expiryPoint, x func(expiryPoint) float64, target float64) float64 {
	ordered := append([]expiryPoint(nil), points...)
	sort.Slice(ordered, func(i, j int) bool { return x(ordered[i]) < x(ordered[j]) })
	for index := 1; index < len(ordered); index++ {
		left, right := ordered[index-1], ordered[index]
		lx, rx := x(left), x(right)
		if target < lx || target > rx {
			continue
		}
		if rx == lx {
			return left.iv
		}
		weight := (target - lx) / (rx - lx)
		return left.iv + weight*(right.iv-left.iv)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func (dataset *Dataset) decorate(chain Chain) Chain {
	chain.Stale = !dataset.registry.Fresh(datasetName, chain.market.String())
	return chain
}

// List describes every loaded series, ordered by venue then underlying.
func (dataset *Dataset) List() []Series {
	dataset.mu.RLock()
	chains := make([]Chain, 0, len(dataset.chains))
	for _, chain := range dataset.chains {
		chains = append(chains, *chain)
	}
	dataset.mu.RUnlock()
	now := dataset.now().UnixMilli()
	out := make([]Series, 0, len(chains))
	for _, chain := range chains {
		chain = dataset.decorate(chain)
		series := Series{Series: chain.Series, Venue: chain.Venue, Market: chain.market.String(), Underlying: chain.Underlying, PremiumAsset: chain.PremiumAsset,
			StrikeAsset: chain.StrikeAsset, Inverse: chain.Inverse, Index: chain.Index, Expiries: len(chain.Expiries), Contracts: len(chain.Rows), Stale: chain.Stale}
		for _, expiry := range chain.Expiries {
			if expiry.Expiry-now > int64(24*time.Hour/time.Millisecond) && expiry.ATMIV > 0 {
				series.FrontATMIV = expiry.ATMIV
				break
			}
		}
		out = append(out, series)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Venue != out[j].Venue {
			return out[i].Venue < out[j].Venue
		}
		if out[i].Underlying != out[j].Underlying {
			return seriesRank(out[i].Underlying) < seriesRank(out[j].Underlying) || (seriesRank(out[i].Underlying) == seriesRank(out[j].Underlying) && out[i].Underlying < out[j].Underlying)
		}
		return out[i].Inverse && !out[j].Inverse
	})
	return out
}

// seriesRank puts the deepest markets first.
func seriesRank(underlying string) int {
	switch underlying {
	case "BTC":
		return 0
	case "ETH":
		return 1
	}
	return 2
}

// Get returns one series' chain, optionally one expiry's rows only.
func (dataset *Dataset) Get(series string, expiry int64) (Chain, error) {
	dataset.mu.RLock()
	chain, ok := dataset.chains[series]
	dataset.mu.RUnlock()
	if !ok {
		if dataset.known(series) {
			return Chain{}, ErrNoChain
		}
		return Chain{}, fmt.Errorf("%w: %s", ErrUnknownSeries, series)
	}
	copied := dataset.decorate(*chain)
	if expiry > 0 {
		rows := make([]Row, 0)
		for _, row := range copied.Rows {
			if row.Expiry == expiry {
				rows = append(rows, row)
			}
		}
		copied.Rows = rows
	}
	return copied, nil
}

// known reports a well-formed series id of a served options venue.
func (dataset *Dataset) known(series string) bool {
	parts := strings.Split(series, ":")
	if len(parts) != 3 {
		return false
	}
	for _, served := range dataset.markets {
		if string(served.Venue) == parts[0] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// WebSocket topic
// ---------------------------------------------------------------------------

func (dataset *Dataset) ValidKey(key hub.Key) error {
	if key.Channel != Channel || key.Account != "" || key.Interval != "" {
		return fmt.Errorf("invalid options topic")
	}
	dataset.mu.RLock()
	_, loaded := dataset.chains[key.Instrument]
	dataset.mu.RUnlock()
	if loaded || dataset.known(key.Instrument) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnknownSeries, key.Instrument)
}

// Source feeds one series' topic: the current chain, then every rebuild.
func (dataset *Dataset) Source(ctx context.Context, key hub.Key, emit hub.Emit) error {
	self := &watcher{emit: emit}
	dataset.mu.Lock()
	if dataset.watchers[key.Instrument] == nil {
		dataset.watchers[key.Instrument] = map[*watcher]struct{}{}
	}
	dataset.watchers[key.Instrument][self] = struct{}{}
	chain, ok := dataset.chains[key.Instrument]
	dataset.mu.Unlock()
	defer func() {
		dataset.mu.Lock()
		delete(dataset.watchers[key.Instrument], self)
		dataset.mu.Unlock()
	}()
	if ok {
		emit(hub.Message{Channel: Channel, Instrument: key.Instrument, Data: dataset.decorate(*chain)}, true)
	}
	<-ctx.Done()
	return nil
}

func (dataset *Dataset) publish(series string) {
	dataset.mu.RLock()
	chain, ok := dataset.chains[series]
	watchers := make([]*watcher, 0, len(dataset.watchers[series]))
	for item := range dataset.watchers[series] {
		watchers = append(watchers, item)
	}
	dataset.mu.RUnlock()
	if !ok || len(watchers) == 0 {
		return
	}
	message := hub.Message{Channel: Channel, Instrument: series, Data: dataset.decorate(*chain)}
	for _, item := range watchers {
		item.emit(message, true)
	}
}
