// Package funding is the gateway's resident funding dataset: the current
// funding of every served perpetual, normalized to one convention (see
// exchange.FundingQuote), kept in memory by background collection, and
// published as a table, a per-instrument WebSocket topic and a bounded
// history read.
//
// Collection: Hyperliquid pushes its whole board (every dex) over one
// WebSocket; every other market is read whole over REST every 15 s (Gate
// every 30 s: its reply is ~0.5 MB and uncompressed).
package funding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/resident"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

// Channel is the per-instrument WebSocket channel.
const Channel = "funding"

const datasetName = "funding"

var (
	ErrNotPerpetual  = errors.New("funding applies to perpetuals only")
	ErrNoQuote       = errors.New("no current funding quote for this instrument")
	ErrNoHistory     = errors.New("this venue publishes no funding history")
	ErrInvalidWindow = errors.New("window must be positive and at most 30 days")
)

// Quote is one instrument's current funding as served.
type Quote struct {
	Instrument venue.InstrumentID `json:"inst"`
	Venue      exchange.Venue     `json:"venue"`
	Base       string             `json:"base"`
	// Rate is the fraction of notional paid at NextFunding (>0: longs pay shorts).
	Rate          string `json:"rate"`
	IntervalHours int64  `json:"intervalHours,omitempty"`
	// Apr is Rate annualized (simple, 8760 h); absent when the period is unknown.
	Apr         string `json:"apr,omitempty"`
	NextFunding int64  `json:"nextFunding,omitempty"`
	RateCap     string `json:"rateCap,omitempty"`
	RateFloor   string `json:"rateFloor,omitempty"`
	ObservedAt  int64  `json:"observedAt"`
	// Stale: the market's collection has missed three cadences.
	Stale bool `json:"stale"`

	market string
}

// MarketStatus is one market's collection state inside a table response.
type MarketStatus struct {
	Market    string `json:"market"`
	Mode      string `json:"mode"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	Stale     bool   `json:"stale"`
	Error     string `json:"error,omitempty"`
}

// Table is the funding of every served perpetual.
type Table struct {
	Quotes  []Quote        `json:"quotes"`
	Markets []MarketStatus `json:"markets"`
}

type board struct {
	market venue.Market
	source *factory.PublicFundingBoard
	every  time.Duration
}

// Dataset owns the boards, their resident loops and the published state.
type Dataset struct {
	catalog  *market.Catalog
	registry *resident.Registry
	boards   []board
	history  map[venue.Market]*factory.PublicFundingHistoryConnector
	cache    *historyCache
	now      func() time.Time

	mu       sync.RWMutex
	quotes   map[venue.InstrumentID]Quote
	byMarket map[venue.Market][]venue.InstrumentID
	watchers map[venue.InstrumentID]map[*watcher]struct{}
}

type watcher struct{ emit hub.Emit }

// Collection cadences.
const (
	defaultPoll = 15 * time.Second
	gatePoll    = 30 * time.Second
	// streamCadence is how often Hyperliquid's allDexsAssetCtxs pushes.
	streamCadence = 15 * time.Second
)

func New(catalog *market.Catalog, registry *resident.Registry, httpClient *http.Client) (*Dataset, error) {
	dataset := newDataset(catalog, registry)
	for _, served := range venue.Markets {
		if served.Product != exchange.ProductPerp {
			continue
		}
		source, err := factory.NewPublicFundingBoard(served.Venue, served.Settlement, factory.EnvironmentLive, httpClient)
		if err != nil {
			return nil, fmt.Errorf("funding board %s: %w", served, err)
		}
		every := defaultPoll
		switch {
		case source.Streams():
			every = streamCadence
		case served.Venue == exchange.VenueGate:
			every = gatePoll
		}
		dataset.boards = append(dataset.boards, board{market: served, source: source, every: every})
		mode := resident.ModePoll
		if source.Streams() {
			mode = resident.ModeStream
		}
		registry.Declare(datasetName, served.String(), mode, every)
		if connector, err := factory.NewPublicFundingHistoryConnector(served.Venue, served.Product, served.Settlement, factory.EnvironmentLive, httpClient); err == nil {
			dataset.history[served] = connector
		}
	}
	return dataset, nil
}

func newDataset(catalog *market.Catalog, registry *resident.Registry) *Dataset {
	return &Dataset{
		catalog: catalog, registry: registry, now: time.Now,
		history:  map[venue.Market]*factory.PublicFundingHistoryConnector{},
		cache:    newHistoryCache(),
		quotes:   map[venue.InstrumentID]Quote{},
		byMarket: map[venue.Market][]venue.InstrumentID{},
		watchers: map[venue.InstrumentID]map[*watcher]struct{}{},
	}
}

// Run starts one resident loop per market and blocks until ctx ends.
func (dataset *Dataset) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, item := range dataset.boards {
		wg.Add(1)
		go func(item board) {
			defer wg.Done()
			spec := resident.Spec[exchange.FundingBoard]{
				Dataset: datasetName, Market: item.market.String(), Interval: item.every,
				Apply: func(result exchange.FundingBoard) { dataset.apply(item.market, result) },
			}
			if item.source.Streams() {
				spec.Stream = item.source.Stream
			} else {
				spec.Load = item.source.Load
			}
			resident.Run(ctx, dataset.registry, spec)
		}(item)
	}
	wg.Wait()
}

// apply replaces one market's quotes with a complete board and notifies the
// subscribers of every instrument whose quote changed or disappeared.
func (dataset *Dataset) apply(served venue.Market, result exchange.FundingBoard) {
	next := make(map[venue.InstrumentID]Quote, len(result.Quotes))
	for _, quote := range result.Quotes {
		instrument, ok := dataset.catalog.ResolveNative(served, quote.NativeSymbol, quote.NativeID)
		if !ok {
			continue // not tradable through the gateway
		}
		next[instrument.ID] = project(served, instrument, quote)
	}
	ids := make([]venue.InstrumentID, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	type notice struct {
		id    venue.InstrumentID
		quote Quote
		gone  bool
	}
	var notices []notice
	dataset.mu.Lock()
	for _, id := range dataset.byMarket[served] {
		if _, kept := next[id]; !kept {
			delete(dataset.quotes, id)
			notices = append(notices, notice{id: id, gone: true})
		}
	}
	for _, id := range ids {
		quote := next[id]
		if previous, existed := dataset.quotes[id]; !existed || !sameQuote(previous, quote) {
			notices = append(notices, notice{id: id, quote: quote})
		}
		dataset.quotes[id] = quote
	}
	dataset.byMarket[served] = ids
	dataset.mu.Unlock()
	for _, item := range notices {
		dataset.publish(item.id, item.quote, item.gone)
	}
}

func sameQuote(a, b Quote) bool {
	a.ObservedAt, b.ObservedAt = 0, 0
	return a == b
}

func project(served venue.Market, instrument market.Instrument, quote exchange.FundingQuote) Quote {
	result := Quote{
		Instrument: instrument.ID, Venue: instrument.Venue, Base: instrument.Base,
		Rate: quote.Rate.String(), ObservedAt: quote.ObservedAt.UnixMilli(), market: served.String(),
	}
	if quote.IntervalHours.Valid {
		result.IntervalHours = quote.IntervalHours.Value
	}
	if apr := quote.Annualized(); apr.Valid {
		result.Apr = apr.Value.Round(8).String()
	}
	if !quote.NextFundingTime.IsZero() {
		result.NextFunding = quote.NextFundingTime.UnixMilli()
	}
	if quote.RateCap.Valid {
		result.RateCap = quote.RateCap.Value.String()
	}
	if quote.RateFloor.Valid {
		result.RateFloor = quote.RateFloor.Value.String()
	}
	return result
}

// decorate fills fields derived at read time: staleness from the market's
// loop, and a settlement clock rolled forward past a settlement that already
// happened (the next board corrects the rate within one cadence).
func (dataset *Dataset) decorate(quote Quote) Quote {
	quote.Stale = !dataset.registry.Fresh(datasetName, quote.market)
	if quote.NextFunding > 0 && quote.IntervalHours > 0 {
		now := dataset.now().UnixMilli()
		period := quote.IntervalHours * int64(time.Hour/time.Millisecond)
		for quote.NextFunding <= now {
			quote.NextFunding += period
		}
	}
	return quote
}

// FundingOf serves the market feeds (ticker stream, 24h stats), so every
// consumer reads funding from this one source.
func (dataset *Dataset) FundingOf(id venue.InstrumentID) (market.FundingView, bool) {
	quote, err := dataset.Get(id)
	if err != nil {
		return market.FundingView{}, false
	}
	return market.FundingView{Rate: quote.Rate, IntervalHours: quote.IntervalHours, Apr: quote.Apr, NextFunding: quote.NextFunding}, true
}

// Get returns one instrument's current quote.
func (dataset *Dataset) Get(id venue.InstrumentID) (Quote, error) {
	instrument, err := dataset.catalog.Get(id)
	if err != nil {
		return Quote{}, err
	}
	if instrument.Product != exchange.ProductPerp {
		return Quote{}, ErrNotPerpetual
	}
	dataset.mu.RLock()
	quote, ok := dataset.quotes[id]
	dataset.mu.RUnlock()
	if !ok {
		return Quote{}, ErrNoQuote
	}
	return dataset.decorate(quote), nil
}

// Table lists current quotes, optionally filtered by venue and base asset
// (case-insensitive), with each market's collection state.
func (dataset *Dataset) Table(filterVenue exchange.Venue, base string) Table {
	base = strings.ToUpper(strings.TrimSpace(base))
	dataset.mu.RLock()
	quotes := make([]Quote, 0, len(dataset.quotes))
	for _, quote := range dataset.quotes {
		if filterVenue != "" && quote.Venue != filterVenue {
			continue
		}
		if base != "" && strings.ToUpper(quote.Base) != base {
			continue
		}
		quotes = append(quotes, quote)
	}
	dataset.mu.RUnlock()
	for index := range quotes {
		quotes[index] = dataset.decorate(quotes[index])
	}
	sort.Slice(quotes, func(i, j int) bool { return quotes[i].Instrument < quotes[j].Instrument })
	table := Table{Quotes: quotes, Markets: []MarketStatus{}}
	for _, status := range dataset.registry.Statuses() {
		if status.Dataset != datasetName {
			continue
		}
		if filterVenue != "" && !strings.HasPrefix(status.Market, string(filterVenue)+":") {
			continue
		}
		table.Markets = append(table.Markets, MarketStatus{Market: status.Market, Mode: string(status.Mode), UpdatedAt: status.UpdatedAt, Stale: status.Stale, Error: status.Error})
	}
	return table
}

// ---------------------------------------------------------------------------
// WebSocket topic (hub source)
// ---------------------------------------------------------------------------

// ValidKey accepts {"ch":"funding","inst":<perp>}.
func (dataset *Dataset) ValidKey(key hub.Key) error {
	if key.Channel != Channel || key.Interval != "" || key.Account != "" {
		return fmt.Errorf("invalid funding topic")
	}
	instrument, err := dataset.catalog.Get(venue.InstrumentID(key.Instrument))
	if err != nil {
		return err
	}
	if instrument.Product != exchange.ProductPerp {
		return ErrNotPerpetual
	}
	return nil
}

// Source feeds one instrument's topic from the resident state: the current
// quote first, then every change. It opens no venue connection.
func (dataset *Dataset) Source(ctx context.Context, key hub.Key, emit hub.Emit) error {
	id := venue.InstrumentID(key.Instrument)
	subscriber := &watcher{emit: emit}
	dataset.mu.Lock()
	if dataset.watchers[id] == nil {
		dataset.watchers[id] = map[*watcher]struct{}{}
	}
	dataset.watchers[id][subscriber] = struct{}{}
	quote, ok := dataset.quotes[id]
	dataset.mu.Unlock()
	defer func() {
		dataset.mu.Lock()
		delete(dataset.watchers[id], subscriber)
		if len(dataset.watchers[id]) == 0 {
			delete(dataset.watchers, id)
		}
		dataset.mu.Unlock()
	}()
	lastStale := false
	if ok {
		current := dataset.decorate(quote)
		lastStale = current.Stale
		emit(hub.Message{Channel: Channel, Instrument: string(id), Data: current}, true)
	}
	// Staleness changes without a new board; re-announce it when it flips.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, err := dataset.Get(id)
			if err == nil && current.Stale != lastStale {
				lastStale = current.Stale
				emit(hub.Message{Channel: Channel, Instrument: string(id), Data: current}, true)
			}
		}
	}
}

func (dataset *Dataset) publish(id venue.InstrumentID, quote Quote, gone bool) {
	dataset.mu.RLock()
	subscribers := make([]*watcher, 0, len(dataset.watchers[id]))
	for subscriber := range dataset.watchers[id] {
		subscribers = append(subscribers, subscriber)
	}
	dataset.mu.RUnlock()
	if len(subscribers) == 0 {
		return
	}
	var data any = dataset.decorate(quote)
	if gone {
		data = map[string]any{"inst": id, "gone": true}
	}
	for _, subscriber := range subscribers {
		subscriber.emit(hub.Message{Channel: Channel, Instrument: string(id), Data: data}, true)
	}
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

// Point is one settled funding event.
type Point struct {
	Time          int64  `json:"t"`
	Rate          string `json:"rate"`
	IntervalHours int64  `json:"intervalHours,omitempty"`
	Apr           string `json:"apr,omitempty"`
}

// MaxHistory is the widest window served (every venue answers it).
const MaxHistory = 30 * 24 * time.Hour

// History reads settled funding over [since, until] for one perpetual. It is
// an interactive read, not background collection; results are cached until
// the instrument's next settlement because settled points never change.
func (dataset *Dataset) History(ctx context.Context, id venue.InstrumentID, since, until time.Time) ([]Point, error) {
	instrument, err := dataset.catalog.Get(id)
	if err != nil {
		return nil, err
	}
	if instrument.Product != exchange.ProductPerp {
		return nil, ErrNotPerpetual
	}
	now := dataset.now().UTC()
	if until.IsZero() || until.After(now) {
		until = now
	}
	if since.IsZero() {
		since = until.Add(-7 * 24 * time.Hour)
	}
	if !since.Before(until) || until.Sub(since) > MaxHistory+time.Hour {
		return nil, ErrInvalidWindow
	}
	served, ok := venue.MarketOf(instrument.Native())
	if !ok {
		return nil, ErrNotPerpetual
	}
	connector := dataset.history[served]
	if connector == nil {
		return nil, ErrNoHistory
	}
	key := fmt.Sprintf("%s|%d|%d", id, since.Truncate(time.Hour).Unix(), until.Truncate(time.Hour).Unix())
	if points, hit := dataset.cache.get(key, now); hit {
		return points, nil
	}
	identity := instrument.Native().Identity
	snapshot, err := connector.Load(ctx, exchange.FundingHistoryRequest{
		Symbol: identity.Symbol, NativeSymbol: identity.NativeSymbol, NativeID: identity.NativeID,
		Since: since, Until: until,
	})
	if err != nil {
		return nil, err
	}
	points := make([]Point, 0, len(snapshot.Points))
	year := decimal.NewFromInt(exchangeHoursPerYear)
	for _, item := range snapshot.Points {
		point := Point{Time: item.FundingTime.UnixMilli(), Rate: item.Rate.String()}
		if item.IntervalHours.Valid && item.IntervalHours.Value > 0 {
			point.IntervalHours = item.IntervalHours.Value
			point.Apr = item.Rate.Mul(year).Div(decimal.NewFromInt(item.IntervalHours.Value)).Round(8).String()
		}
		points = append(points, point)
	}
	expires := now.Add(time.Hour)
	if current, err := dataset.Get(id); err == nil && current.NextFunding > 0 {
		expires = time.UnixMilli(current.NextFunding).Add(time.Minute)
	}
	dataset.cache.put(key, points, expires)
	return points, nil
}

const exchangeHoursPerYear = 24 * 365

type historyCache struct {
	mu      sync.Mutex
	entries map[string]cachedHistory
}

type cachedHistory struct {
	points  []Point
	expires time.Time
}

func newHistoryCache() *historyCache { return &historyCache{entries: map[string]cachedHistory{}} }

func (cache *historyCache) get(key string, now time.Time) ([]Point, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok || now.After(entry.expires) {
		delete(cache.entries, key)
		return nil, false
	}
	return entry.points, true
}

func (cache *historyCache) put(key string, points []Point, expires time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) >= 512 {
		for existing := range cache.entries {
			delete(cache.entries, existing)
			break
		}
	}
	cache.entries[key] = cachedHistory{points: points, expires: expires}
}
