package optionchain

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/hub"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/resident"
	"github.com/QuantProcessing/keeluni/gateway/venue"
	"github.com/QuantProcessing/keeluni/optionmath"
	"github.com/shopspring/decimal"
)

var (
	testNow    = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	testExpiry = time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	forward    = 82000.0
)

func option(kind exchange.OptionKind, strike int64, inverse bool) exchange.Instrument {
	quote, premium, settle, symbol, native := "USDC", "USDC", "USDC", "BTC-USDC", "BTC_USDC-27NOV26-"
	if inverse {
		quote, premium, settle, symbol, native = "USD", "BTC", "BTC", "BTC-USD", "BTC-27NOV26-"
	}
	letter := "C"
	if kind == exchange.OptionPut {
		letter = "P"
	}
	strikeText := decimal.NewFromInt(strike).String()
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueDeribit, Product: exchange.ProductOption, Symbol: symbol + "-20261127-" + strikeText + "-" + letter,
			BaseAsset: "BTC", QuoteAsset: quote, SettleAsset: settle, NativeSymbol: native + strikeText + "-" + letter},
		Rules:    exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.0001"), QuantityIncrement: decimal.RequireFromString("0.1"), MinQuantity: decimal.RequireFromString("0.1")},
		Metadata: exchange.InstrumentMetadata{MarketFamily: exchange.MarketFamilyOption},
		Option: exchange.OptionTerms{Underlying: "BTC", Kind: kind, Strike: decimal.NewFromInt(strike), Expiry: testExpiry, PremiumAsset: premium,
			Inverse: inverse, European: true, CashSettled: true},
	}
}

// smile is a skewed vol surface: puts richer than calls.
func smile(strike float64) float64 {
	m := math.Log(strike / forward)
	return 0.45 - 0.20*m + 0.30*m*m
}

type chainSource struct{ quotes []exchange.OptionQuote }

func (source chainSource) OptionChain(context.Context, venue.Market, string) ([]exchange.OptionQuote, bool, error) {
	return source.quotes, true, nil
}

// newTestDataset builds a BTC inverse chain from 60k to 104k (mark only,
// IVs given) and a BTC linear chain with prices only (IVs implied).
func newTestDataset(t *testing.T) *Dataset {
	t.Helper()
	var natives []exchange.Instrument
	var quotes []exchange.OptionQuote
	years := optionmath.Years(testNow, testExpiry)
	for strike := int64(60000); strike <= 104000; strike += 2000 {
		for _, kind := range []exchange.OptionKind{exchange.OptionCall, exchange.OptionPut} {
			vol := smile(float64(strike))
			usd := optionmath.Price(kind == exchange.OptionCall, forward, float64(strike), years, vol)
			inverse := option(kind, strike, true)
			natives = append(natives, inverse)
			quotes = append(quotes, exchange.OptionQuote{Instrument: inverse,
				Mark:            exchange.OptionalDecimal{Value: decimal.NewFromFloat(usd / forward).Round(6), Valid: true},
				MarkIV:          exchange.OptionalDecimal{Value: decimal.NewFromFloat(vol), Valid: true},
				UnderlyingPrice: exchange.OptionalDecimal{Value: decimal.NewFromFloat(forward), Valid: true},
				IndexPrice:      exchange.OptionalDecimal{Value: decimal.NewFromInt(81500), Valid: true}})
			if strike%10000 == 0 {
				linear := option(kind, strike, false)
				natives = append(natives, linear)
				quotes = append(quotes, exchange.OptionQuote{Instrument: linear,
					Bid:             exchange.OptionalDecimal{Value: decimal.NewFromFloat(usd * 0.97).Round(1), Valid: true},
					Ask:             exchange.OptionalDecimal{Value: decimal.NewFromFloat(usd * 1.03).Round(1), Valid: true},
					Mark:            exchange.OptionalDecimal{Value: decimal.NewFromFloat(usd).Round(2), Valid: true},
					UnderlyingPrice: exchange.OptionalDecimal{Value: decimal.NewFromFloat(forward), Valid: true}})
			}
		}
	}
	catalog := market.NewStaticCatalog(natives)
	registry := resident.NewRegistry()
	dataset := newDataset(chainSource{quotes}, catalog, registry)
	dataset.markets = []venue.Market{{Venue: exchange.VenueDeribit, Product: exchange.ProductOption}}
	dataset.now = func() time.Time { return testNow }
	dataset.apply(dataset.markets[0], quotes)
	return dataset
}

func TestChainNormalizesSeriesAndGreeks(t *testing.T) {
	dataset := newTestDataset(t)
	series := dataset.List()
	if len(series) != 2 || series[0].Series != "deribit:BTC:BTC" || !series[0].Inverse || series[1].Series != "deribit:BTC:USDC" {
		t.Fatalf("series = %+v", series)
	}
	if series[0].StrikeAsset != "USD" || series[0].Contracts != 46 || series[0].Index != 81500 {
		t.Fatalf("inverse series = %+v", series[0])
	}
	chain, err := dataset.Get("deribit:BTC:BTC", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Expiries) != 1 || chain.Rows[0].Strike != "60000" || chain.Rows[0].Kind != "call" || chain.Rows[1].Kind != "put" {
		t.Fatalf("rows not ordered by strike, call first: %+v", chain.Rows[:2])
	}
	// Greeks are per one BTC in USD, the same convention for inverse and
	// linear options.
	years := optionmath.Years(testNow, testExpiry)
	want := optionmath.GreeksOf(false, forward, 74000, years, smile(74000))
	for _, row := range chain.Rows {
		if row.Strike == "74000" && row.Kind == "put" {
			if math.Abs(row.Delta-want.Delta) > 1e-4 || math.Abs(row.Vega-want.Vega) > 0.01 || row.MarkIV != round(smile(74000), 5) {
				t.Fatalf("74000 put = %+v, want %+v", row, want)
			}
		}
	}
}

func TestImpliedVolsWhenTheVenuePublishesNone(t *testing.T) {
	dataset := newTestDataset(t)
	chain, err := dataset.Get("deribit:BTC:USDC", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range chain.Rows {
		strike := parseFloat(row.Strike)
		if math.Abs(row.MarkIV-smile(strike)) > 2e-3 {
			t.Fatalf("%s %s mark IV = %v, want %v", row.Strike, row.Kind, row.MarkIV, smile(strike))
		}
		if row.BidIV == 0 || row.AskIV == 0 || row.BidIV >= row.MarkIV || row.AskIV <= row.MarkIV {
			if (row.Kind == "call" && strike < forward*0.85) || (row.Kind == "put" && strike > forward*1.15) {
				continue // deep in the money: a 3% haircut falls through intrinsic, which has no IV
			}
			t.Fatalf("%s %s IVs = %v / %v / %v", row.Strike, row.Kind, row.BidIV, row.MarkIV, row.AskIV)
		}
	}
}

func TestExpirySummaryReadsTheSmile(t *testing.T) {
	dataset := newTestDataset(t)
	chain, _ := dataset.Get("deribit:BTC:BTC", testExpiry.UnixMilli())
	summary := chain.Expiries[0]
	if summary.Forward != forward || summary.Strikes != 23 {
		t.Fatalf("summary = %+v", summary)
	}
	if math.Abs(summary.ATMIV-smile(forward)) > 1e-3 {
		t.Fatalf("ATM IV = %v, want %v", summary.ATMIV, smile(forward))
	}
	// Downside skew: the 25-delta put is richer than the call; the wings
	// are above ATM (positive butterfly).
	if summary.RR25 >= 0 || summary.BF25 <= 0 {
		t.Fatalf("RR25 = %v BF25 = %v", summary.RR25, summary.BF25)
	}
	if got := dataset.List()[0].FrontATMIV; got != summary.ATMIV {
		t.Fatalf("front ATM IV = %v", got)
	}
}

type sink struct{ messages []hub.Message }

func TestTopicReplaysAndPublishesRebuilds(t *testing.T) {
	dataset := newTestDataset(t)
	if err := dataset.ValidKey(hub.Key{Channel: Channel, Instrument: "deribit:BTC:BTC"}); err != nil {
		t.Fatal(err)
	}
	if err := dataset.ValidKey(hub.Key{Channel: Channel, Instrument: "binance:BTC:USDT"}); err == nil {
		t.Fatal("unserved venue accepted")
	}
	if err := dataset.ValidKey(hub.Key{Channel: Channel, Instrument: "deribit:SOL:USDC"}); err != nil {
		t.Fatalf("a not-yet-loaded series of a served venue: %v", err)
	}
	var received []hub.Message
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = dataset.Source(ctx, hub.Key{Channel: Channel, Instrument: "deribit:BTC:BTC"}, func(message hub.Message, retain bool) {
			if retain {
				received = append(received, message)
			}
		})
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for len(received) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	dataset.apply(dataset.markets[0], dataset.source.(chainSource).quotes)
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if len(received) != 2 || received[0].Data.(Chain).Series != "deribit:BTC:BTC" {
		t.Fatalf("received %d", len(received))
	}
	if _, err := dataset.Get("deribit:ETH:ETH", 0); err != ErrNoChain {
		t.Fatalf("missing chain err = %v", err)
	}
}

// A chain read moments after an expiry, or with a NaN from the venue, must
// still encode: one bad row once stopped the whole series from publishing.
func TestRoundNeverReturnsNonFinite(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := round(value, 4); got != 0 {
			t.Fatalf("round(%v) = %v", value, got)
		}
		if got := sig(value); got != 0 {
			t.Fatalf("sig(%v) = %v", value, got)
		}
	}
}
