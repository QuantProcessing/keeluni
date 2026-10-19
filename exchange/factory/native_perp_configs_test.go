package factory

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
)

// Binance USDC perps, Kraken Futures and Deribit: typed configs that build
// account clients without I/O, validate locally and expose account events
// and exact order lookup like every native perp client.
func TestNativePerpConfigsConstructWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	options := []Option{WithEnvironment(EnvironmentLive), WithHTTPClient(&http.Client{Transport: transport})}
	for name, cfg := range map[string]Config[exchange.PerpClient]{
		"binance usdc": BinanceUSDCPerpConfig("key", "secret", options...),
		"kraken":       KrakenPerpConfig("key", "c2VjcmV0", options...),
		"deribit":      DeribitPerpConfig("id", "secret", options...),
	} {
		perp, err := New(cfg)
		requireConstructed(t, perp, err)
		if _, ok := perp.(exchange.ExactOrderLookup); !ok {
			t.Fatalf("%s lacks exact order lookup", name)
		}
		if _, ok := perp.WebSocket().(exchange.AccountWebSocket); !ok {
			t.Fatalf("%s websocket lacks account events", name)
		}
		if _, ok := perp.WebSocket().(exchange.AccountInstrumentIndexer); !ok {
			t.Fatalf("%s websocket lacks the account instrument index", name)
		}
		if _, ok := perp.WebSocket().(exchange.DerivativeReferenceWebSocket); !ok {
			t.Fatalf("%s websocket lacks the derivative reference stream", name)
		}
		if err := perp.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if transport.requests.Load() != 0 {
		t.Fatalf("construction performed %d requests", transport.requests.Load())
	}
}

func TestNativePerpConfigsValidateLocally(t *testing.T) {
	for _, cfg := range []Config[exchange.PerpClient]{
		BinanceUSDCPerpConfig("", "secret"),
		KrakenPerpConfig("key", ""),
		DeribitPerpConfig(" ", "secret"),
		KrakenPerpConfig("key", "secret", WithAccountAddress("0x0000000000000000000000000000000000000001")),
	} {
		if _, err := New(cfg); !errors.Is(err, exchange.ErrInvalidConfig) {
			t.Fatalf("%s: err = %v", cfg, err)
		}
	}
}
