package factoryclient

import (
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	bitget "github.com/QuantProcessing/keeluni/sdk/bitget"
	bybit "github.com/QuantProcessing/keeluni/sdk/bybit"
	gate "github.com/QuantProcessing/keeluni/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestBybitTickerFactKeepsAbsentDeltaFieldsUntouched(t *testing.T) {
	observedAt := time.Date(2026, time.August, 20, 1, 2, 3, 0, time.UTC)
	fact, fields := bybitTickerFact(fundingTestIdentity(), bybit.Ticker{FundingRate: "0.0001"}, observedAt)
	if fields != nativeDerivativeFieldFunding || !fact.FundingRate.Valid || fact.MarkPrice.Valid || fact.OpenInterest.Valid {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestBitgetTickerFactUsesUTAOpenInterest(t *testing.T) {
	fact, fields := bitgetTickerFact(fundingTestIdentity(), bitget.Ticker{
		OpenInterest: "12.5", MarkPrice: "200", FundingRate: "0.0002",
	}, time.Now().UTC())
	if !fields.Has(nativeDerivativeFieldOpenInterest) ||
		!fields.Has(nativeDerivativeFieldOpenInterestNotional) ||
		!fact.OpenInterest.Valid || !fact.OpenInterest.Value.Equal(decimal.RequireFromString("12.5")) ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(2500)) ||
		fact.OpenInterestNotionalAsset != "USDT" {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestGateTickerFactAppliesContractMultiplier(t *testing.T) {
	fact, fields := gateTickerFact(fundingTestIdentity(), gate.FuturesTicker{
		Contract: "BTC_USDT", MarkPrice: "200", TotalSize: "10",
	}, decimal.RequireFromString("0.001"), time.Now().UTC())
	if !fields.Has(nativeDerivativeFieldOpenInterestNotional) ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestLighterMarketStatCarriesComparableFundingPeriodAndPrices(t *testing.T) {
	identity := fundingTestIdentity()
	identity.Venue = exchange.VenueLighter
	identity.QuoteAsset = "USDC"
	identity.SettleAsset = "USDC"
	observedAt := time.Date(2026, time.August, 21, 1, 2, 3, 0, time.UTC)
	// current_funding_rate is a percentage per hourly settlement: 0.0012 %/h
	// is 0.000012 per settlement, settled on every hour.
	fact, fields := lighterMarketStatFact(identity, lighterMarketStat{
		CurrentFundingRate: "0.0012", MarkPrice: "64000", IndexPrice: "63990", OpenInterest: "12.5",
		FundingTimestamp: time.Date(2026, time.August, 21, 1, 0, 0, 2_000_000, time.UTC).UnixMilli(),
	}, observedAt)
	if !fact.FundingRate.Valid || !fact.FundingRate.Value.Equal(decimal.RequireFromString("0.000012")) ||
		!fact.NextFundingTime.Equal(time.Date(2026, time.August, 21, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("funding fact=%+v", fact)
	}
	if !fields.Has(nativeDerivativeFieldFundingInterval) ||
		!fact.FundingIntervalHours.Valid || fact.FundingIntervalHours.Value != 1 ||
		!fact.MarkPrice.Valid || !fact.IndexPrice.Valid ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(800000)) {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestDerivativeSnapshotDoesNotClaimAbsentOpenInterest(t *testing.T) {
	fields := nativeDerivativeSnapshotFields([]nativeDerivativeFact{{
		FundingRate: exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
		MarkPrice:   exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true},
		IndexPrice:  exchange.OptionalDecimal{Value: decimal.NewFromInt(99), Valid: true},
	}})
	if !fields.Has(nativeDerivativeFieldIdentity) || !fields.Has(nativeDerivativeFieldFunding) || !fields.Has(nativeDerivativeFieldMarkPrice) || !fields.Has(nativeDerivativeFieldIndexPrice) {
		t.Fatalf("snapshot fields missing available values: %b", fields)
	}
	if fields.Has(nativeDerivativeFieldOpenInterest) || fields.Has(nativeDerivativeFieldOpenInterestNotional) {
		t.Fatalf("snapshot claims absent open interest: %b", fields)
	}
}

func TestGateSubscriptionFactsAreStableAndTrackMembership(t *testing.T) {
	facts := []nativeDerivativeFact{
		{Instrument: exchange.InstrumentIdentity{NativeSymbol: "ETH_USDT"}},
		{Instrument: exchange.InstrumentIdentity{NativeSymbol: "BTC_USDT"}},
	}
	identities, _, payload := gateSubscriptionFacts(facts)
	if len(identities) != 2 || len(payload) != 2 || payload[0] != "BTC_USDT" || payload[1] != "ETH_USDT" {
		t.Fatalf("gate subscription facts = identities:%+v payload:%+v", identities, payload)
	}
	if !sameStrings(payload, []string{"BTC_USDT", "ETH_USDT"}) || sameStrings(payload, []string{"BTC_USDT"}) {
		t.Fatal("gate catalog membership comparison is unstable")
	}
}

func TestNativeDerivativeBatchesEmitOncePerFieldShape(t *testing.T) {
	scope := exchange.CatalogScope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT"}
	at := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	batches := make(map[nativeDerivativeField]*nativeDerivativeBatch)
	appendNativeDerivativeBatch(batches, at, nativeDerivativeFieldMarkPrice, nativeDerivativeFact{Instrument: exchange.InstrumentIdentity{NativeSymbol: "BTCUSDT"}})
	appendNativeDerivativeBatch(batches, at.Add(time.Second), nativeDerivativeFieldMarkPrice, nativeDerivativeFact{Instrument: exchange.InstrumentIdentity{NativeSymbol: "ETHUSDT"}})
	appendNativeDerivativeBatch(batches, at, nativeDerivativeFieldFunding|nativeDerivativeFieldMarkPrice, nativeDerivativeFact{Instrument: exchange.InstrumentIdentity{NativeSymbol: "SOLUSDT"}})

	var patches []nativeDerivativePatch
	emitNativeDerivativeBatches(scope, batches, func(patch nativeDerivativePatch) { patches = append(patches, patch) })
	if len(patches) != 2 {
		t.Fatalf("patch count = %d, want one patch per field shape", len(patches))
	}
	if patches[0].Fields != nativeDerivativeFieldMarkPrice || len(patches[0].Facts) != 2 || !patches[0].ObservedAt.Equal(at.Add(time.Second)) {
		t.Fatalf("mark batch = %+v", patches[0])
	}
	if patches[1].Fields != nativeDerivativeFieldFunding|nativeDerivativeFieldMarkPrice || len(patches[1].Facts) != 1 {
		t.Fatalf("funding batch = %+v", patches[1])
	}
}

func fundingTestIdentity() exchange.InstrumentIdentity {
	return exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC",
		QuoteAsset: "USDT", SettleAsset: "USDT",
	}
}
