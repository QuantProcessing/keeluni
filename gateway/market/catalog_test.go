package market

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

func TestUnloadedMarketsListsOnlyEmptyMarkets(t *testing.T) {
	catalog := NewStaticCatalog([]exchange.Instrument{{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT"},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}})
	missing := catalog.unloadedMarkets()
	if len(missing) != len(venue.Markets)-1 {
		t.Fatalf("missing = %d, want %d", len(missing), len(venue.Markets)-1)
	}
	for _, market := range missing {
		if market.String() == "binance:perp:USDT" {
			t.Fatal("loaded market reported as missing")
		}
	}
}

func TestRunStopsWithContext(t *testing.T) {
	catalog := NewStaticCatalog(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { catalog.Run(ctx, time.Hour, 5*time.Millisecond); close(done) }()
	time.Sleep(30 * time.Millisecond) // several retry passes over connector-less markets
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}

func crossExOnlyInstrument(v exchange.Venue, symbol, base, settle, native string) exchange.Instrument {
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: v, Product: exchange.ProductPerp, Symbol: symbol, BaseAsset: base, QuoteAsset: settle, SettleAsset: settle, NativeSymbol: native},
		Rules:    exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.5"), QuantityIncrement: decimal.RequireFromString("0.0001"), MinQuantity: decimal.RequireFromString("0.0001")},
	}
	instrument.Metadata.ClassificationSource = "crossex"
	return instrument
}

func TestBrokeredInstrumentsAreListedAndNativeWins(t *testing.T) {
	nativeBTC := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT"},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}
	catalog := NewStaticCatalog([]exchange.Instrument{
		nativeBTC,
		crossExOnlyInstrument(exchange.VenueKraken, "BTC-USD", "BTC", "USD", "KRAKEN_FUTURE_BTC_USD"),
		crossExOnlyInstrument(exchange.VenueBinance, "AMZN-USDT", "AMZN", "USDT", "BINANCE_FUTURE_AMZN_USDT"),
	})
	kraken, err := catalog.Get("kraken:perp:BTC-USD")
	if err != nil || !kraken.Brokered() || kraken.Source != "crossex" {
		t.Fatalf("kraken = %+v, %v", kraken, err)
	}
	native, _ := catalog.Get("binance:perp:BTC-USDT")
	if native.Brokered() || native.Source != "" {
		t.Fatalf("native = %+v", native)
	}
	binance := catalog.List(exchange.VenueBinance, exchange.ProductPerp)
	if len(binance) != 2 {
		t.Fatalf("binance perps = %d", len(binance))
	}
	if len(catalog.List(exchange.VenueKraken, "")) != 1 {
		t.Fatal("kraken not listed")
	}

	// A broker instrument with a native id never replaces the native one.
	catalog.publishBrokered(venue.Market{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT"}, []exchange.Instrument{
		crossExOnlyInstrument(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BINANCE_FUTURE_BTC_USDT"),
	})
	if again, _ := catalog.Get("binance:perp:BTC-USDT"); again.Brokered() {
		t.Fatal("broker instrument displaced the native one")
	}
	// A venue that lists an instrument natively takes it over.
	listed := crossExOnlyInstrument(exchange.VenueBinance, "AMZN-USDT", "AMZN", "USDT", "AMZNUSDT")
	listed.Metadata.ClassificationSource = ""
	catalog.publish(venue.Market{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT"}, []exchange.Instrument{nativeBTC, listed})
	if amzn, _ := catalog.Get("binance:perp:AMZN-USDT"); amzn.Brokered() {
		t.Fatal("natively listed instrument still served by the broker")
	}
	if got := len(catalog.List(exchange.VenueBinance, exchange.ProductPerp)); got != 2 {
		t.Fatalf("binance perps after takeover = %d", got)
	}
}
