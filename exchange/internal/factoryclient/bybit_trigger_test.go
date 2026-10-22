package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func TestTriggerDirectionFollowsKindAndClosingSide(t *testing.T) {
	for _, tc := range []struct {
		kind exchange.TriggerKind
		side exchange.Side
		want int
	}{
		{exchange.TriggerTakeProfit, exchange.SideSell, 1}, // long take-profit: price rises
		{exchange.TriggerStopLoss, exchange.SideSell, 2},   // long stop: falls
		{exchange.TriggerTakeProfit, exchange.SideBuy, 2},  // short take-profit: falls
		{exchange.TriggerStopLoss, exchange.SideBuy, 1},    // short stop: rises
	} {
		if got := triggerDirection(tc.kind, tc.side); got != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.kind, tc.side, got, tc.want)
		}
	}
}

func newBybitTriggerClient(t *testing.T, handler func(*http.Request) *http.Response) exchange.TriggerOrderREST {
	t.Helper()
	client := NewBybitLinearPerp("key", "secret", "USDT", bybitBitgetStaticCatalogSettings(t, Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return handler(request), nil
		})},
	}, exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT"))
	trigger, ok := client.(exchange.TriggerOrderREST)
	if !ok {
		t.Fatal("Bybit perp lacks trigger orders")
	}
	return trigger
}

func TestBybitTriggerPlacesConditionalMarketOrder(t *testing.T) {
	var body map[string]any
	client := newBybitTriggerClient(t, func(request *http.Request) *http.Response {
		if request.URL.Path != "/v5/order/create" {
			t.Fatalf("unexpected %s", request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		return bybitData(`{"orderId":"c-1","orderLinkId":"42"}`)
	})
	instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	order, err := client.PlaceTriggerOrder(context.Background(), exchange.TriggerOrderRequest{
		Instrument: instrument, ClientOrderID: "42", Side: exchange.SideBuy, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("95000"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"category": "linear", "symbol": "BTCUSDT", "side": "Buy", "orderType": "Market", "qty": "0.01",
		"reduceOnly": true, "orderLinkId": "42", "triggerPrice": "95000", "triggerDirection": float64(1), "triggerBy": "MarkPrice", "orderFilter": "StopOrder"} {
		if body[key] != want {
			t.Fatalf("%s = %v, want %v", key, body[key], want)
		}
	}
	if order.OrderID != "c-1" || order.Kind != exchange.TriggerStopLoss {
		t.Fatalf("order = %+v", order)
	}
}

func TestBybitTriggerRejectionAndTransport(t *testing.T) {
	instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	request := exchange.TriggerOrderRequest{Instrument: instrument, ClientOrderID: "42", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("95000"), PriceType: exchange.TriggerPriceLast, Quantity: decimal.RequireFromString("0.01")}
	rejecting := newBybitTriggerClient(t, func(*http.Request) *http.Response {
		return bybitDataError(110092, "expect Rising, but trigger_price[95000] <= current[98000]")
	})
	if _, err := rejecting.PlaceTriggerOrder(context.Background(), request); !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}
	failing := newBybitTriggerClient(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad gateway"))}
	})
	if _, err := failing.PlaceTriggerOrder(context.Background(), request); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("502 = %v", err)
	}
}

func TestBybitTriggersListApartFromOrders(t *testing.T) {
	stop := `{"category":"linear","orderId":"c-1","orderLinkId":"42","symbol":"BTCUSDT","side":"Sell","orderType":"Market","price":"0","qty":"0.01","cumExecQty":"0","orderStatus":"Untriggered","reduceOnly":true,"createdTime":"1720000000000","updatedTime":"1720000000000","triggerPrice":"90000","triggerDirection":2,"triggerBy":"LastPrice","stopOrderType":"Stop","orderFilter":"StopOrder"}`
	tpsl := `{"category":"linear","orderId":"c-2","orderLinkId":"","symbol":"BTCUSDT","side":"Sell","orderType":"Market","price":"0","qty":"0","cumExecQty":"0","orderStatus":"Untriggered","reduceOnly":true,"closeOnTrigger":true,"createdTime":"1720000000000","updatedTime":"1720000000000","triggerPrice":"120000","triggerDirection":1,"triggerBy":"MarkPrice","stopOrderType":"TakeProfit","orderFilter":"tpslOrder"}`
	plain := bybitOrderJSON("o-1", "7", "New", false)
	client := newBybitTriggerClient(t, func(request *http.Request) *http.Response {
		if strings.Contains(request.URL.RawQuery, "orderFilter=StopOrder") {
			return bybitData(`{"list":[` + stop + `,` + tpsl + `],"nextPageCursor":""}`)
		}
		return bybitData(`{"list":[` + plain + `,` + stop + `],"nextPageCursor":""}`)
	})
	instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	open, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].Kind != exchange.TriggerStopLoss || open[0].ClientOrderID != "42" ||
		open[1].Kind != exchange.TriggerTakeProfit || open[1].PriceType != exchange.TriggerPriceMark || !open[1].ClosePosition {
		t.Fatalf("open = %+v", open)
	}
	// Ordinary open orders leave the conditional row out.
	page, err := client.(exchange.PerpClient).OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil || len(page.Orders) != 1 || page.Orders[0].OrderID != "o-1" {
		t.Fatalf("open orders = %+v %v", page.Orders, err)
	}
	if order, status, err := client.LookupTriggerOrder(context.Background(), instrument, "42"); err != nil || status != exchange.TriggerFound || order.OrderID != "c-1" {
		t.Fatalf("lookup = %+v %s %v", order, status, err)
	}
}
