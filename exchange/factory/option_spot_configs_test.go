package factory

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

// Kraken and Deribit spot, Deribit options and CrossEx with spot: typed
// configs that build account clients without I/O and validate locally.
func TestSpotAndOptionConfigsConstructWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	options := []Option{WithEnvironment(EnvironmentLive), WithHTTPClient(&http.Client{Transport: transport})}
	for name, cfg := range map[string]Config[exchange.SpotClient]{
		"kraken spot":  KrakenSpotConfig("key", "c2VjcmV0", options...),
		"deribit spot": DeribitSpotConfig("id", "secret", options...),
	} {
		spot, err := New(cfg)
		requireConstructed(t, spot, err)
		if _, ok := spot.(exchange.ExactOrderLookup); !ok {
			t.Fatalf("%s lacks exact order lookup", name)
		}
		if _, ok := spot.WebSocket().(exchange.AccountWebSocket); !ok {
			t.Fatalf("%s websocket lacks account events", name)
		}
		if err := spot.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for name, cfg := range map[string]Config[exchange.OptionClient]{
		"deribit option": DeribitOptionConfig("id", "secret", options...),
		"okx option":     OKXOptionConfig("key", "secret", "passphrase", options...),
	} {
		option, err := New(cfg)
		requireConstructed(t, option, err)
		if _, ok := option.(exchange.ExactOrderLookup); !ok {
			t.Fatalf("%s lacks exact order lookup", name)
		}
		if _, ok := option.WebSocket().(exchange.AccountWebSocket); !ok {
			t.Fatalf("%s websocket lacks account events", name)
		}
		if _, ok := option.WebSocket().(exchange.AccountInstrumentIndexer); !ok {
			t.Fatalf("%s websocket lacks the account instrument index", name)
		}
		if err := option.Close(); err != nil {
			t.Fatal(err)
		}
	}
	natives := func(context.Context) ([]exchange.Instrument, error) { return nil, nil }
	asks := BestAskSource(func(context.Context, exchange.Instrument) (decimal.Decimal, error) { return decimal.Zero, nil })
	brokered, err := New(CrossExConfig("key", "secret", natives, asks, options...))
	requireConstructed(t, brokered, err)
	_ = brokered.Close()
	if transport.requests.Load() != 0 {
		t.Fatalf("construction performed %d requests", transport.requests.Load())
	}
	for _, err := range []error{
		func() error { _, err := New(KrakenSpotConfig("", "secret")); return err }(),
		func() error { _, err := New(DeribitSpotConfig("id", " ")); return err }(),
		func() error { _, err := New(DeribitOptionConfig("", "secret")); return err }(),
		func() error { _, err := New(OKXOptionConfig("key", "secret", "")); return err }(),
	} {
		if !errors.Is(err, exchange.ErrInvalidConfig) {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestOptionPublicConnectorServesTheChain(t *testing.T) {
	for _, venue := range []exchange.Venue{exchange.VenueDeribit, exchange.VenueOKX} {
		connector, err := NewPublicMarketConnector(venue, exchange.ProductOption, "", EnvironmentLive, &http.Client{Transport: new(countingTransport)})
		if err != nil {
			t.Fatal(venue, err)
		}
		defer connector.Close()
		if _, ok := connector.WebSocket().(exchange.OptionQuoteWebSocket); !ok {
			t.Fatalf("%s option connector socket lacks quotes", venue)
		}
	}
	spot, err := NewPublicMarketConnector(exchange.VenueKraken, exchange.ProductSpot, "", EnvironmentLive, &http.Client{Transport: new(countingTransport)})
	if err != nil {
		t.Fatal(err)
	}
	defer spot.Close()
	if _, served, _ := spot.OptionChain(context.Background(), exchange.OptionChainRequest{}); served {
		t.Fatal("a spot connector claims an option chain")
	}
}
