package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func newBitgetTriggerClient(t *testing.T, holdMode string, handler func(*http.Request) *http.Response) exchange.TriggerOrderREST {
	t.Helper()
	client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/api/v3/account/settings" {
				return bitgetData(`{"accountMode":"unified","holdMode":"` + holdMode + `"}`), nil
			}
			return handler(request), nil
		})},
	})
	trigger, ok := client.(exchange.TriggerOrderREST)
	if !ok {
		t.Fatal("Bitget perp lacks trigger orders")
	}
	return trigger
}

func bitgetTriggerRequest(kind exchange.TriggerKind, side exchange.Side) exchange.TriggerOrderRequest {
	return exchange.TriggerOrderRequest{
		Instrument: bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT"), ClientOrderID: "42", Side: side, Kind: kind,
		TriggerPrice: decimal.RequireFromString("120000"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.01"),
	}
}

func TestBitgetTriggerPlacesPartialTPSLStrategyOrder(t *testing.T) {
	for _, tc := range []struct {
		holdMode, posSide, reduceOnly string
	}{
		{"one_way_mode", "", "yes"},
		{"hedge_mode", "long", ""}, // a sell closing a long
	} {
		var body map[string]any
		client := newBitgetTriggerClient(t, tc.holdMode, func(request *http.Request) *http.Response {
			if request.URL.Path != "/api/v3/trade/place-strategy-order" {
				t.Fatalf("unexpected %s", request.URL.Path)
			}
			raw, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(raw, &body)
			return bitgetData(`{"orderId":"9001","clientOid":"42"}`)
		})
		order, err := client.PlaceTriggerOrder(context.Background(), bitgetTriggerRequest(exchange.TriggerTakeProfit, exchange.SideSell))
		if err != nil {
			t.Fatalf("%s: %v", tc.holdMode, err)
		}
		want := map[string]any{"category": "USDT-FUTURES", "symbol": "BTCUSDT", "type": "tpsl", "side": "sell", "qty": "0.01", "clientOid": "42",
			"tpslMode": "partial", "takeProfit": "120000", "tpTriggerBy": "mark", "tpOrderType": "market"}
		if tc.posSide != "" {
			want["posSide"] = tc.posSide
		}
		if tc.reduceOnly != "" {
			want["reduceOnly"] = tc.reduceOnly
		}
		for key, value := range want {
			if body[key] != value {
				t.Fatalf("%s: %s = %v, want %v (%+v)", tc.holdMode, key, body[key], value, body)
			}
		}
		if _, ok := body["stopLoss"]; ok {
			t.Fatalf("%s: take-profit carried a stop: %+v", tc.holdMode, body)
		}
		if order.OrderID != "9001" {
			t.Fatalf("order = %+v", order)
		}
	}
}

func TestBitgetTriggerRejectionListAndLookup(t *testing.T) {
	// A documented definitive code is a rejection; an unlisted one stays
	// ambiguous (the SDK taxonomy fails closed).
	for code, want := range map[string]error{"25002": exchange.ErrVenueRejected, "40762": exchange.ErrAmbiguousOutcome} {
		client := newBitgetTriggerClient(t, "one_way_mode", func(*http.Request) *http.Response { return bitgetDataError(code, "refused") })
		if _, err := client.PlaceTriggerOrder(context.Background(), bitgetTriggerRequest(exchange.TriggerStopLoss, exchange.SideSell)); !errors.Is(err, want) {
			t.Fatalf("code %s = %v, want %v", code, err, want)
		}
	}
	rows := `[{"orderId":"9001","clientOid":"42","category":"USDT-FUTURES","symbol":"BTCUSDT","qty":"0.01","side":"sell","status":"pending","tpslMode":"partial","tpTriggerBy":"mark","takeProfit":"120000","tpOrderType":"market","createdTime":"1720000000000"},
		{"orderId":"9002","clientOid":"","category":"USDT-FUTURES","symbol":"BTCUSDT","qty":"","side":"sell","status":"pending","tpslMode":"full","tpTriggerBy":"market","slTriggerBy":"market","takeProfit":"130000","stopLoss":"90000","tpOrderType":"market","slOrderType":"limit","createdTime":"1720000000000"}]`
	client := newBitgetTriggerClient(t, "one_way_mode", func(request *http.Request) *http.Response {
		switch request.URL.Path {
		case "/api/v3/trade/unfilled-strategy-orders":
			return bitgetData(rows)
		case "/api/v3/trade/history-strategy-orders":
			return bitgetData(`{"list":[],"cursor":""}`)
		case "/api/v3/trade/cancel-strategy-order":
			return bitgetData(`null`)
		}
		t.Fatalf("unexpected %s", request.URL.Path)
		return nil
	})
	instrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	open, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil {
		t.Fatal(err)
	}
	// The full-position order: its market take-profit shows; its limit stop does not.
	if len(open) != 2 || open[0].OrderID != "9001" || open[0].PriceType != exchange.TriggerPriceMark ||
		open[1].OrderID != "9002" || open[1].Kind != exchange.TriggerTakeProfit || !open[1].ClosePosition {
		t.Fatalf("open = %+v", open)
	}
	if order, status, err := client.LookupTriggerOrder(context.Background(), instrument, "42"); err != nil || status != exchange.TriggerFound || order.OrderID != "9001" {
		t.Fatalf("lookup = %+v %s %v", order, status, err)
	}
	if _, _, err := client.LookupTriggerOrder(context.Background(), instrument, "404"); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("absent lookup = %v", err)
	}
	if err := client.CancelTriggerOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "9001"}); err != nil {
		t.Fatal(err)
	}
}
