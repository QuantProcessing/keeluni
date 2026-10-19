package market

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/venue"
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

func perp(v exchange.Venue, symbol, base, settle, native string) exchange.Instrument {
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: v, Product: exchange.ProductPerp, Symbol: symbol, BaseAsset: base, QuoteAsset: settle, SettleAsset: settle, NativeSymbol: native},
		Rules:    exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.5"), QuantityIncrement: decimal.RequireFromString("0.0001"), MinQuantity: decimal.RequireFromString("0.0001")},
	}
}

// Kraken, Deribit and Binance USDC perps are native markets: each lists its
// own instruments, and the venue's asset class is published.
func TestEveryPerpMarketIsNative(t *testing.T) {
	aapl := perp(exchange.VenueDeribit, "AAPL-USDC", "AAPL", "USDC", "AAPL_USDC-PERPETUAL")
	aapl.Metadata.AssetClass = exchange.AssetClassEquity
	catalog := NewStaticCatalog([]exchange.Instrument{
		perp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT"),
		perp(exchange.VenueBinance, "BTC-USDC", "BTC", "USDC", "BTCUSDC"),
		perp(exchange.VenueKraken, "BTC-USD", "BTC", "USD", "PF_XBTUSD"),
		aapl,
	})
	for _, id := range []venue.InstrumentID{"binance:perp:BTC-USDT", "binance:perp:BTC-USDC", "kraken:perp:BTC-USD", "deribit:perp:AAPL-USDC"} {
		if _, err := catalog.Get(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if got := len(catalog.List(exchange.VenueBinance, exchange.ProductPerp)); got != 2 {
		t.Fatalf("binance perps = %d", got)
	}
	if listed, _ := catalog.Get("deribit:perp:AAPL-USDC"); listed.AssetClass != "equity" {
		t.Fatalf("asset class = %q", listed.AssetClass)
	}
	if listed, _ := catalog.Get("kraken:perp:BTC-USD"); listed.AssetClass != "" {
		t.Fatalf("unknown asset class published as %q", listed.AssetClass)
	}
	if found, ok := catalog.ResolveNative(venue.Market{Venue: exchange.VenueKraken, Product: exchange.ProductPerp, Settlement: "USD"}, "PF_XBTUSD", ""); !ok || found.ID != "kraken:perp:BTC-USD" {
		t.Fatalf("resolve native = %+v %v", found, ok)
	}
}
