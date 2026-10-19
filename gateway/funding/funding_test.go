package funding

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/resident"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

func instrument(venueName exchange.Venue, product exchange.Product, symbol, base, quote, native, nativeID string) exchange.Instrument {
	settle := ""
	if product == exchange.ProductPerp {
		settle = quote
	}
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: venueName, Product: product, Symbol: symbol, BaseAsset: base, QuoteAsset: quote, SettleAsset: settle, NativeSymbol: native, NativeID: nativeID},
		Rules:    exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}
}

func testDataset(t *testing.T) (*Dataset, *resident.Registry) {
	t.Helper()
	catalog := market.NewStaticCatalog([]exchange.Instrument{
		instrument(exchange.VenueOKX, exchange.ProductPerp, "BTC-USDT-SWAP", "BTC", "USDT", "BTC-USDT-SWAP", "10459"),
		instrument(exchange.VenueOKX, exchange.ProductSpot, "BTC-USDT", "BTC", "USDT", "BTC-USDT", ""),
		instrument(exchange.VenueHyperliquid, exchange.ProductPerp, "BTC-USDC", "BTC", "USDC", "BTC", "0"),
		instrument(exchange.VenueHyperliquid, exchange.ProductPerp, "xyz:TSLA-USDC", "TSLA", "USDC", "xyz:TSLA", "110000"),
	})
	registry := resident.NewRegistry()
	return newDataset(catalog, registry), registry
}

var (
	okxPerp = venue.Market{Venue: exchange.VenueOKX, Product: exchange.ProductPerp, Settlement: "USDT"}
	hlPerp  = venue.Market{Venue: exchange.VenueHyperliquid, Product: exchange.ProductPerp, Settlement: "USDC"}
)

func quote(native, nativeID, rate string, hours int64, next time.Time) exchange.DerivativeFact {
	result := exchange.DerivativeFact{
		InstrumentID: nativeID + "|" + native, RouteProfileID: "test", Environment: exchange.EnvironmentLive,
		Instrument:  exchange.InstrumentIdentity{Product: exchange.ProductPerp, NativeSymbol: native, NativeID: nativeID},
		FundingRate: exchange.OptionalDecimal{Value: decimal.RequireFromString(rate), Valid: true}, NextFundingTime: next, FundingObservedAt: time.Now(),
	}
	if hours > 0 {
		result.FundingIntervalHours = exchange.OptionalInt{Value: hours, Valid: true}
	}
	return result
}

// boardOf is a complete snapshot of one market's funding.
func boardOf(served venue.Market, facts ...exchange.DerivativeFact) exchange.DerivativePatch {
	return exchange.DerivativePatch{Kind: exchange.EventSnapshot, Scope: exchange.CatalogScope{Venue: served.Venue, Product: exchange.ProductPerp, Settlement: served.Settlement},
		ObservedAt: time.Now(), Fields: exchange.DerivativeFieldAll, Facts: facts}
}

// feed runs a fake resident loop so registry freshness is real.
func feed(t *testing.T, dataset *Dataset, registry *resident.Registry, served venue.Market, result exchange.DerivativePatch) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	applied := make(chan struct{})
	var once sync.Once
	go resident.Run(ctx, registry, resident.Spec[exchange.DerivativePatch]{
		Dataset: datasetName, Market: served.String(), Interval: time.Hour,
		Load:  func(context.Context) (exchange.DerivativePatch, error) { return result, nil },
		Apply: func(value exchange.DerivativePatch) { dataset.apply(served, value); once.Do(func() { close(applied) }) },
	})
	t.Cleanup(cancel)
	select {
	case <-applied:
	case <-time.After(2 * time.Second):
		t.Fatal("board not applied")
	}
}

func TestBoardsMapOntoCatalogIdsAndAnnualize(t *testing.T) {
	dataset, registry := testDataset(t)
	next := time.Now().Add(2 * time.Hour).Truncate(time.Hour)
	feed(t, dataset, registry, okxPerp, boardOf(okxPerp,
		quote("BTC-USDT-SWAP", "", "0.0001", 8, next),
		quote("DELISTED-USDT-SWAP", "", "0.01", 8, next), // not in the catalog: dropped
	))
	feed(t, dataset, registry, hlPerp, boardOf(hlPerp,
		quote("BTC", "0", "0.0000125", 1, next),
		quote("xyz:TSLA", "110000", "-0.00002", 1, next),
	))
	btc, err := dataset.Get("okx:perp:BTC-USDT-SWAP")
	if err != nil {
		t.Fatal(err)
	}
	if btc.Rate != "0.0001" || btc.IntervalHours != 8 || btc.Apr != "0.1095" || btc.NextFunding != next.UnixMilli() || btc.Stale {
		t.Fatalf("okx quote = %+v", btc)
	}
	tsla, err := dataset.Get("hyperliquid:perp:xyz:TSLA-USDC")
	if err != nil || tsla.Apr != "-0.1752" || tsla.Base != "TSLA" {
		t.Fatalf("HIP-3 quote = %+v err=%v", tsla, err)
	}
	if _, err := dataset.Get("okx:spot:BTC-USDT"); !errors.Is(err, ErrNotPerpetual) {
		t.Fatalf("spot err = %v", err)
	}
	table := dataset.Table("", "btc")
	if len(table.Quotes) != 2 || len(table.Markets) != 2 {
		t.Fatalf("table = %+v", table)
	}
	if only := dataset.Table(exchange.VenueHyperliquid, ""); len(only.Quotes) != 2 || len(only.Markets) != 1 {
		t.Fatalf("venue filter = %+v", only)
	}
}

func TestUnknownPeriodHasNoAprAndClockRollsPastSettlement(t *testing.T) {
	dataset, registry := testDataset(t)
	past := time.Now().Add(-90 * time.Minute)
	feed(t, dataset, registry, okxPerp, boardOf(okxPerp, quote("BTC-USDT-SWAP", "", "0.0001", 0, time.Time{})))
	if got, _ := dataset.Get("okx:perp:BTC-USDT-SWAP"); got.Apr != "" || got.IntervalHours != 0 {
		t.Fatalf("unknown period = %+v", got)
	}
	feed(t, dataset, registry, hlPerp, boardOf(hlPerp, quote("BTC", "0", "0.00001", 1, past)))
	got, _ := dataset.Get("hyperliquid:perp:BTC-USDC")
	if got.NextFunding <= time.Now().UnixMilli() || got.NextFunding > time.Now().Add(time.Hour).UnixMilli() {
		t.Fatalf("clock not rolled forward: %d", got.NextFunding)
	}
}

// A streamed delta updates only the fields it carries and only instruments
// the snapshot listed.
func TestDeltaUpdatesTheFieldsItCarries(t *testing.T) {
	dataset, registry := testDataset(t)
	next := time.Now().Add(time.Hour).Truncate(time.Hour)
	feed(t, dataset, registry, hlPerp, boardOf(hlPerp, quote("BTC", "0", "0.00001", 1, next)))
	delta := quote("BTC", "0", "0.00003", 0, time.Time{})
	delta.FundingObservedAt = time.Now().Add(time.Second)
	dataset.apply(hlPerp, exchange.DerivativePatch{Kind: exchange.EventDelta, Fields: exchange.DerivativeFieldFunding, Facts: []exchange.DerivativeFact{delta, quote("ETH", "1", "0.5", 1, next)}})
	got, err := dataset.Get("hyperliquid:perp:BTC-USDC")
	if err != nil || got.Rate != "0.00003" || got.IntervalHours != 1 || got.Apr != "0.2628" {
		t.Fatalf("after delta = %+v, %v", got, err)
	}
	if len(dataset.Table(exchange.VenueHyperliquid, "").Quotes) != 1 {
		t.Fatal("a delta added an instrument the snapshot did not list")
	}
}

func TestStaleWhenTheMarketsLoopStopsUpdating(t *testing.T) {
	dataset, registry := testDataset(t)
	dataset.apply(okxPerp, boardOf(okxPerp, quote("BTC-USDT-SWAP", "", "0.0001", 8, time.Time{})))
	// No resident loop ever succeeded for this market.
	if got, _ := dataset.Get("okx:perp:BTC-USDT-SWAP"); !got.Stale {
		t.Fatalf("quote without a live loop not stale: %+v", got)
	}
	_ = registry
}

type sink struct {
	mu       sync.Mutex
	messages []hub.Message
}

func (s *sink) Send(payload []byte) bool {
	var message hub.Message
	_ = json.Unmarshal(payload, &message)
	s.mu.Lock()
	s.messages = append(s.messages, message)
	s.mu.Unlock()
	return true
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *sink) last() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(s.messages[len(s.messages)-1].Data)
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	return value
}

func TestTopicReplaysCurrentQuoteThenOnlyChanges(t *testing.T) {
	dataset, registry := testDataset(t)
	feed(t, dataset, registry, okxPerp, boardOf(okxPerp, quote("BTC-USDT-SWAP", "", "0.0001", 8, time.Time{})))
	topics := hub.New(dataset.Source, time.Second)
	defer topics.Close()
	key := hub.Key{Channel: Channel, Instrument: "okx:perp:BTC-USDT-SWAP"}
	if err := dataset.ValidKey(key); err != nil {
		t.Fatal(err)
	}
	if err := dataset.ValidKey(hub.Key{Channel: Channel, Instrument: "okx:spot:BTC-USDT"}); err == nil {
		t.Fatal("spot topic accepted")
	}
	subscriber := &sink{}
	topics.Subscribe(key, subscriber)
	deadline := time.Now().Add(2 * time.Second)
	for subscriber.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if subscriber.count() != 1 || subscriber.last()["rate"] != "0.0001" {
		t.Fatalf("replay = %d %v", subscriber.count(), subscriber.last())
	}
	// The same board again changes nothing; a new rate is pushed.
	dataset.apply(okxPerp, boardOf(okxPerp, quote("BTC-USDT-SWAP", "", "0.0001", 8, time.Time{})))
	dataset.apply(okxPerp, boardOf(okxPerp, quote("BTC-USDT-SWAP", "", "0.0002", 8, time.Time{})))
	deadline = time.Now().Add(2 * time.Second)
	for subscriber.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if subscriber.count() != 2 || subscriber.last()["rate"] != "0.0002" {
		t.Fatalf("change = %d %v", subscriber.count(), subscriber.last())
	}
	// A board without the instrument announces it gone.
	dataset.apply(okxPerp, boardOf(okxPerp))
	deadline = time.Now().Add(2 * time.Second)
	for subscriber.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if subscriber.last()["gone"] != true {
		t.Fatalf("removal = %v", subscriber.last())
	}
}

func TestHistoryRejectsBadWindowsAndSpot(t *testing.T) {
	dataset, _ := testDataset(t)
	now := time.Now()
	if _, err := dataset.History(context.Background(), "okx:spot:BTC-USDT", time.Time{}, time.Time{}); !errors.Is(err, ErrNotPerpetual) {
		t.Fatal(err)
	}
	if _, err := dataset.History(context.Background(), "okx:perp:BTC-USDT-SWAP", now.Add(-40*24*time.Hour), now); !errors.Is(err, ErrInvalidWindow) {
		t.Fatal(err)
	}
	if _, err := dataset.History(context.Background(), "okx:perp:BTC-USDT-SWAP", now, now.Add(-time.Hour)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatal(err)
	}
	// No connector configured in the test dataset.
	if _, err := dataset.History(context.Background(), "okx:perp:BTC-USDT-SWAP", time.Time{}, time.Time{}); !errors.Is(err, ErrNoHistory) {
		t.Fatal(err)
	}
}
