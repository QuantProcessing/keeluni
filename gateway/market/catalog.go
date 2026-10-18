// Package market serves public market data: the instrument catalog, REST
// snapshots and shared WebSocket streams. It never touches credentials.
package market

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// ErrUnknownInstrument is returned for ids that are not in the catalog.
var ErrUnknownInstrument = fmt.Errorf("unknown instrument")

// Instrument is the public projection of one tradable instrument.
type Instrument struct {
	ID           venue.InstrumentID `json:"id"`
	Venue        exchange.Venue     `json:"venue"`
	Product      exchange.Product   `json:"product"`
	Symbol       string             `json:"symbol"`
	Base         string             `json:"base"`
	Quote        string             `json:"quote"`
	Settle       string             `json:"settle,omitempty"`
	TickSize     string             `json:"tickSize"`
	StepSize     string             `json:"stepSize"`
	MinQty       string             `json:"minQty"`
	MinNotional  string             `json:"minNotional,omitempty"`
	MarketFamily string             `json:"marketFamily,omitempty"`
	// Source names where market data comes from when it is not the venue
	// itself ("crossex"): such instruments have no market history.
	Source string `json:"source,omitempty"`

	native   exchange.Instrument
	brokered bool
}

// Native returns the exchange-layer instrument used for venue calls.
func (instrument Instrument) Native() exchange.Instrument { return instrument.native }

// Brokered reports an instrument that exists only through a broker; its
// market data is the broker's and it has no native client.
func (instrument Instrument) Brokered() bool { return instrument.brokered }

type connector struct {
	market venue.Market
	client *factory.PublicMarketConnector
}

// Catalog owns one credential-free connector per served market and the merged
// instrument directory. Instrument metadata is refreshed periodically; the
// last good snapshot of a market survives a failed refresh.
//
// Brokered markets (venue.BrokeredMarkets) share one connector: the broker's
// public data. Each of their instruments records that it is served by it.
type Catalog struct {
	connectors map[venue.Market]*connector
	brokered   *connector

	mu          sync.RWMutex
	instruments map[venue.InstrumentID]Instrument
	byMarket    map[venue.Market][]venue.InstrumentID
	loadErrors  map[venue.Market]string
	// brokeredIDs holds broker-only instruments per market. They are listed
	// with the market's native instruments but served by the broker connector.
	brokeredIDs map[venue.Market][]venue.InstrumentID
}

func NewCatalog(httpClient *http.Client) (*Catalog, error) {
	catalog := &Catalog{
		connectors:  make(map[venue.Market]*connector, len(venue.Markets)),
		instruments: make(map[venue.InstrumentID]Instrument),
		byMarket:    make(map[venue.Market][]venue.InstrumentID),
		loadErrors:  make(map[venue.Market]string),
		brokeredIDs: make(map[venue.Market][]venue.InstrumentID),
	}
	for _, market := range venue.Markets {
		client, err := factory.NewPublicMarketConnector(market.Venue, market.Product, market.Settlement, factory.EnvironmentLive, httpClient)
		if err != nil {
			return nil, fmt.Errorf("public connector %s: %w", market, err)
		}
		catalog.connectors[market] = &connector{market: market, client: client}
	}
	brokered, err := factory.NewCrossExMarketConnector(catalog.nativePerps, httpClient, factory.WithEnvironment(factory.EnvironmentLive))
	if err != nil {
		return nil, fmt.Errorf("crossex market connector: %w", err)
	}
	catalog.brokered = &connector{market: brokeredSource, client: brokered}
	return catalog, nil
}

// brokeredSource keys the broker connector's load status.
var brokeredSource = venue.Market{Venue: exchange.VenueCrossEx, Product: exchange.ProductPerp}

// nativePerps lists natively served perp instruments, so the broker source
// skips instruments a native connector already serves.
func (catalog *Catalog) nativePerps(context.Context) ([]exchange.Instrument, error) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	var out []exchange.Instrument
	for _, market := range venue.Markets {
		if market.Product != exchange.ProductPerp {
			continue
		}
		for _, id := range catalog.byMarket[market] {
			out = append(out, catalog.instruments[id].native)
		}
	}
	return out, nil
}

// Refresh reloads every market concurrently and returns once all attempts
// finished. Failures are recorded per market and logged, never fatal.
func (catalog *Catalog) Refresh(ctx context.Context) {
	markets := make([]venue.Market, 0, len(catalog.connectors))
	for market := range catalog.connectors {
		markets = append(markets, market)
	}
	catalog.refreshMarkets(ctx, markets)
	catalog.refreshBrokered(ctx)
}

// refreshBrokered loads the broker-only instruments. It runs after the native
// markets so an instrument a native connector serves is never served twice.
func (catalog *Catalog) refreshBrokered(ctx context.Context) {
	if catalog.brokered == nil {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	natives, err := catalog.brokered.client.Refresh(loadCtx)
	if err != nil {
		slog.Warn("instrument catalog refresh failed", "market", "crossex", "error", err)
		catalog.mu.Lock()
		catalog.loadErrors[brokeredSource] = err.Error()
		catalog.mu.Unlock()
		return
	}
	grouped := map[venue.Market][]exchange.Instrument{}
	for _, native := range natives {
		key := venue.MarketKey(native)
		grouped[key] = append(grouped[key], native)
	}
	catalog.mu.Lock()
	delete(catalog.loadErrors, brokeredSource)
	catalog.mu.Unlock()
	for _, market := range venue.BrokeredMarkets {
		catalog.publishBrokered(market, grouped[market])
	}
}

func (catalog *Catalog) refreshMarkets(ctx context.Context, markets []venue.Market) {
	var wg sync.WaitGroup
	for _, market := range markets {
		item := catalog.connectors[market]
		if item == nil {
			continue
		}
		wg.Add(1)
		go func(item *connector) {
			defer wg.Done()
			loadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			natives, err := item.client.Refresh(loadCtx)
			if err != nil {
				slog.Warn("instrument catalog refresh failed", "market", item.market.String(), "error", err)
				catalog.mu.Lock()
				catalog.loadErrors[item.market] = err.Error()
				catalog.mu.Unlock()
				return
			}
			catalog.mu.RLock()
			recovered := catalog.loadErrors[item.market] != ""
			catalog.mu.RUnlock()
			catalog.publish(item.market, natives)
			if recovered {
				slog.Info("market recovered", "market", item.market.String(), "instruments", len(natives))
			}
		}(item)
	}
	wg.Wait()
}

// Run refreshes the catalog every interval until ctx ends. While any market
// has never loaded (e.g. the venue was unreachable at startup), it retries
// that market's load every retry period instead of waiting a full interval.
func (catalog *Catalog) Run(ctx context.Context, interval, retry time.Duration) {
	next := time.Now().Add(interval)
	for {
		wait := time.Until(next)
		if missing := catalog.unloadedMarkets(); (len(missing) > 0 || catalog.brokeredMissing()) && retry < wait {
			wait = retry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if time.Now().Before(next) {
			catalog.refreshMarkets(ctx, catalog.unloadedMarkets())
			if catalog.brokeredMissing() {
				catalog.refreshBrokered(ctx)
			}
			continue
		}
		catalog.Refresh(ctx)
		next = time.Now().Add(interval)
	}
}

// brokeredMissing reports a broker source that has never loaded.
func (catalog *Catalog) brokeredMissing() bool {
	if catalog.brokered == nil {
		return false
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	return len(catalog.brokeredIDs) == 0
}

// unloadedMarkets lists markets with no instruments loaded yet.
func (catalog *Catalog) unloadedMarkets() []venue.Market {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	var missing []venue.Market
	for _, market := range venue.Markets {
		if len(catalog.byMarket[market]) == 0 {
			missing = append(missing, market)
		}
	}
	return missing
}

func (catalog *Catalog) publish(market venue.Market, natives []exchange.Instrument) {
	ids := make([]venue.InstrumentID, 0, len(natives))
	projected := make(map[venue.InstrumentID]Instrument, len(natives))
	for _, native := range natives {
		owner, ok := venue.MarketOf(native)
		if !ok || owner != market {
			continue
		}
		instrument := project(native)
		if _, duplicate := projected[instrument.ID]; duplicate {
			slog.Warn("duplicate instrument id dropped", "id", instrument.ID)
			continue
		}
		projected[instrument.ID] = instrument
		ids = append(ids, instrument.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	for _, old := range catalog.byMarket[market] {
		delete(catalog.instruments, old)
	}
	for id, instrument := range projected {
		catalog.instruments[id] = instrument
	}
	catalog.byMarket[market] = ids
	delete(catalog.loadErrors, market)
	// A venue that newly lists an instrument takes it over from the broker.
	if brokered := catalog.brokeredIDs[market]; len(brokered) > 0 {
		kept := brokered[:0]
		for _, id := range brokered {
			if _, native := projected[id]; !native {
				kept = append(kept, id)
			}
		}
		catalog.brokeredIDs[market] = kept
	}
}

// publishBrokered replaces market's broker-only instruments. Native ids win:
// an id a native connector already serves is never replaced.
func (catalog *Catalog) publishBrokered(market venue.Market, natives []exchange.Instrument) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	for _, old := range catalog.brokeredIDs[market] {
		delete(catalog.instruments, old)
	}
	ids := make([]venue.InstrumentID, 0, len(natives))
	for _, native := range natives {
		instrument := project(native)
		instrument.brokered = true
		if _, taken := catalog.instruments[instrument.ID]; taken {
			continue
		}
		catalog.instruments[instrument.ID] = instrument
		ids = append(ids, instrument.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	catalog.brokeredIDs[market] = ids
}

func project(native exchange.Instrument) Instrument {
	identity := native.Identity
	instrument := Instrument{
		ID:           venue.NewInstrumentID(identity.Venue, identity.Product, identity.Symbol),
		Venue:        identity.Venue,
		Product:      identity.Product,
		Symbol:       identity.Symbol,
		Base:         identity.BaseAsset,
		Quote:        identity.QuoteAsset,
		Settle:       identity.SettleAsset,
		TickSize:     native.Rules.PriceIncrement.String(),
		StepSize:     native.Rules.QuantityIncrement.String(),
		MinQty:       native.Rules.MinQuantity.String(),
		MarketFamily: string(native.Metadata.MarketFamily),
		native:       native,
	}
	if native.Rules.MinNotional.Valid {
		instrument.MinNotional = native.Rules.MinNotional.Value.String()
	}
	if factory.CrossExOnly(native) {
		instrument.Source = "crossex"
		instrument.brokered = true
	}
	return instrument
}

// Get resolves one instrument id.
func (catalog *Catalog) Get(id venue.InstrumentID) (Instrument, error) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	instrument, ok := catalog.instruments[id]
	if !ok {
		return Instrument{}, fmt.Errorf("%w: %s", ErrUnknownInstrument, id)
	}
	return instrument, nil
}

// List returns instruments filtered by venue and product (either may be empty).
func (catalog *Catalog) List(filterVenue exchange.Venue, filterProduct exchange.Product) []Instrument {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	var result []Instrument
	for _, market := range catalog.allMarkets() {
		if (filterVenue != "" && market.Venue != filterVenue) || (filterProduct != "" && market.Product != filterProduct) {
			continue
		}
		for _, id := range catalog.byMarket[market] {
			result = append(result, catalog.instruments[id])
		}
		for _, id := range catalog.brokeredIDs[market] {
			result = append(result, catalog.instruments[id])
		}
	}
	return result
}

// allMarkets lists native markets, then brokered markets not already listed.
func (catalog *Catalog) allMarkets() []venue.Market {
	seen := map[venue.Market]bool{}
	var markets []venue.Market
	for _, list := range [][]venue.Market{venue.Markets, venue.BrokeredMarkets} {
		for _, market := range list {
			if !seen[market] {
				seen[market] = true
				markets = append(markets, market)
			}
		}
	}
	return markets
}

// MarketInstruments returns the native instruments of one market, used to
// build private-stream instrument indexes.
func (catalog *Catalog) MarketInstruments(market venue.Market) []Instrument {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	result := make([]Instrument, 0, len(catalog.byMarket[market])+len(catalog.brokeredIDs[market]))
	for _, id := range catalog.byMarket[market] {
		result = append(result, catalog.instruments[id])
	}
	for _, id := range catalog.brokeredIDs[market] {
		result = append(result, catalog.instruments[id])
	}
	return result
}

// ResolveNative maps a venue-native symbol or id back to the gateway id.
func (catalog *Catalog) ResolveNative(market venue.Market, nativeSymbol, nativeID string) (Instrument, bool) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	for _, id := range catalog.byMarket[market] {
		instrument := catalog.instruments[id]
		identity := instrument.native.Identity
		if (nativeID != "" && identity.NativeID == nativeID) || (nativeSymbol != "" && identity.NativeSymbol == nativeSymbol) {
			return instrument, true
		}
	}
	return Instrument{}, false
}

// MarketStatus summarizes catalog health per market.
type MarketStatus struct {
	Market      string `json:"market"`
	Instruments int    `json:"instruments"`
	Error       string `json:"error,omitempty"`
}

func (catalog *Catalog) Status() []MarketStatus {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	result := make([]MarketStatus, 0, len(venue.Markets)+1)
	for _, market := range venue.Markets {
		result = append(result, MarketStatus{Market: market.String(), Instruments: len(catalog.byMarket[market]), Error: catalog.loadErrors[market]})
	}
	if catalog.brokered != nil {
		count := 0
		for _, ids := range catalog.brokeredIDs {
			count += len(ids)
		}
		result = append(result, MarketStatus{Market: "crossex", Instruments: count, Error: catalog.loadErrors[brokeredSource]})
	}
	return result
}

func (catalog *Catalog) connector(id venue.InstrumentID) (Instrument, *factory.PublicMarketConnector, error) {
	instrument, err := catalog.Get(id)
	if err != nil {
		return Instrument{}, nil, err
	}
	if instrument.brokered {
		if catalog.brokered == nil {
			return Instrument{}, nil, fmt.Errorf("%w: %s", ErrUnknownInstrument, id)
		}
		return instrument, catalog.brokered.client, nil
	}
	market, _ := venue.MarketOf(instrument.native)
	item := catalog.connectors[market]
	if item == nil {
		return Instrument{}, nil, fmt.Errorf("%w: %s", ErrUnknownInstrument, id)
	}
	return instrument, item.client, nil
}

func (catalog *Catalog) Close() {
	for _, item := range catalog.connectors {
		_ = item.client.Close()
	}
	if catalog.brokered != nil {
		_ = catalog.brokered.client.Close()
	}
}

// NewTestInstrument projects a native instrument without a catalog. It exists
// for tests in other packages.
func NewTestInstrument(native exchange.Instrument) Instrument { return project(native) }

// WithRules returns the instrument bound to another client's view of it — a
// broker's symbol and rules — keeping the gateway id and data source.
func (instrument Instrument) WithRules(native exchange.Instrument) Instrument {
	bound := project(native)
	bound.ID, bound.Source, bound.brokered = instrument.ID, instrument.Source, instrument.brokered
	return bound
}

// NewStaticCatalog builds a catalog from fixed instruments without any venue
// connector. It exists for tests in other packages.
func NewStaticCatalog(natives []exchange.Instrument) *Catalog {
	catalog := &Catalog{
		connectors:  map[venue.Market]*connector{},
		instruments: map[venue.InstrumentID]Instrument{},
		byMarket:    map[venue.Market][]venue.InstrumentID{},
		loadErrors:  map[venue.Market]string{},
		brokeredIDs: map[venue.Market][]venue.InstrumentID{},
	}
	grouped := map[venue.Market][]exchange.Instrument{}
	brokered := map[venue.Market][]exchange.Instrument{}
	for _, native := range natives {
		if factory.CrossExOnly(native) {
			key := venue.MarketKey(native)
			brokered[key] = append(brokered[key], native)
			continue
		}
		if market, ok := venue.MarketOf(native); ok {
			grouped[market] = append(grouped[market], native)
		}
	}
	for market, items := range grouped {
		catalog.publish(market, items)
	}
	for market, items := range brokered {
		catalog.publishBrokered(market, items)
	}
	return catalog
}
