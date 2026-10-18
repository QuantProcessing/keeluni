package factory

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestCrossExPerpConfigConstructsABrokeredClientWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	calls := 0
	var natives NativeInstrumentSource = func(context.Context) ([]exchange.Instrument, error) {
		calls++
		return nil, nil
	}
	perp, err := New(CrossExPerpConfig("key", "secret", natives,
		WithEnvironment(EnvironmentLive), WithEndpoint("http://exchange.test"), WithWebSocketEndpoint("ws://exchange.test/ws"),
		WithHTTPClient(&http.Client{Transport: transport})))
	requireConstructed(t, perp, err)
	if transport.requests.Load() != 0 || calls != 0 {
		t.Fatalf("construction performed I/O: http=%d natives=%d", transport.requests.Load(), calls)
	}
	brokered, ok := perp.(exchange.BrokeredClient)
	if !ok || brokered.Broker() != exchange.VenueCrossEx || len(brokered.Reaches()) == 0 {
		t.Fatal("crossex client is not brokered")
	}
	for _, route := range brokered.Reaches() {
		if exchange.IsBroker(route.Venue) || route.Product != exchange.ProductPerp || route.Settlement == "" {
			t.Fatalf("reachable route %+v", route)
		}
	}
	if _, ok := perp.(exchange.ExactOrderLookup); !ok {
		t.Fatal("crossex client lacks exact order lookup")
	}
	if _, ok := perp.WebSocket().(exchange.AccountWebSocket); !ok {
		t.Fatal("crossex websocket lacks account events")
	}
	if err := perp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := perp.Close(); err != nil {
		t.Fatal("close is not idempotent")
	}
}

func TestCrossExPerpConfigValidatesLocally(t *testing.T) {
	natives := NativeInstrumentSource(func(context.Context) ([]exchange.Instrument, error) { return nil, nil })
	cases := []Config[exchange.PerpClient]{
		CrossExPerpConfig("", "secret", natives),
		CrossExPerpConfig("key", " ", natives),
		CrossExPerpConfig("key", "secret", nil),
		CrossExPerpConfig("key", "secret", natives, WithAccountAddress("0x0000000000000000000000000000000000000001")),
	}
	for _, cfg := range cases {
		if _, err := New(cfg); !errors.Is(err, exchange.ErrInvalidConfig) {
			t.Fatalf("%s: err = %v", cfg, err)
		}
	}
	if got := CrossExPerpConfig("key", "secret", natives).String(); got != `exchange/factory.Config{name:"crossex-perp", credentials:redacted}` {
		t.Fatalf("config string = %s", got)
	}
}

func TestCrossExMarketConnectorIsPublicAndConstructsWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	connector, err := NewCrossExMarketConnector(func(context.Context) ([]exchange.Instrument, error) { return nil, nil },
		&http.Client{Transport: transport}, WithEnvironment(EnvironmentLive))
	if err != nil {
		t.Fatal(err)
	}
	if transport.requests.Load() != 0 {
		t.Fatal("construction performed I/O")
	}
	if _, ok := connector.WebSocket().(exchange.DerivativeReferenceWebSocket); !ok {
		t.Fatal("crossex connector lacks the reference stream")
	}
	if _, supported, _ := connector.FundingRate(context.Background(), exchange.FundingRateRequest{}); supported {
		t.Fatal("crossex connector claims REST funding")
	}
	if _, err := connector.Candles(context.Background(), exchange.CandlesRequest{}); !errors.Is(err, exchange.ErrUnsupported) {
		t.Fatalf("history err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, supported, err := connector.Ticker24h(ctx, exchange.Instrument{}); !supported || err == nil {
		t.Fatalf("ticker24h supported=%v err=%v", supported, err)
	}
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	if _, supported, _ := (*PublicMarketConnector)(nil).Ticker24h(ctx, exchange.Instrument{}); supported {
		t.Fatal("nil connector claims a live ticker")
	}
	if _, err := NewCrossExMarketConnector(nil, nil); !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("nil http client err = %v", err)
	}
	native := exchange.Instrument{Metadata: exchange.InstrumentMetadata{ClassificationSource: "crossex"}}
	if !CrossExOnly(native) || CrossExOnly(exchange.Instrument{}) {
		t.Fatal("CrossExOnly misclassifies")
	}
}
