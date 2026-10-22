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

func TestGateTriggerPriceOrders(t *testing.T) {
	var body map[string]any
	open := `[{"id":701,"status":"open","create_time":1720000000.5,"order_type":"plan-close-long-position",
		"initial":{"contract":"BTC_USDT","size":-10,"price":"0","tif":"ioc","text":"t-42","reduce_only":true},
		"trigger":{"strategy_type":0,"price_type":1,"price":"90000","rule":2}},
		{"id":702,"status":"open","initial":{"contract":"BTC_USDT","size":-10,"price":"89000","tif":"gtc"},"trigger":{"strategy_type":0,"price_type":0,"price":"89500","rule":2}},
		{"id":703,"status":"open","order_type":"close-long-position","initial":{"contract":"BTC_USDT","size":0,"price":"0","tif":"ioc","close":true},"trigger":{"strategy_type":0,"price_type":0,"price":"120000","rule":1}}]`
	client := NewGateUSDTPerp("key", "secret", gateTestSettings(exchange.ProductPerp, openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/futures/usdt/price_orders"):
			raw, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(raw, &body)
			return openAPIJSONResponse(`{"id":701}`), nil
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/futures/usdt/price_orders"):
			if request.URL.Query().Get("status") == "finished" {
				return openAPIJSONResponse(`[]`), nil
			}
			return openAPIJSONResponse(open), nil
		case request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, "/price_orders/701"):
			return openAPIJSONResponse(`{"id":701,"status":"finished","finish_as":"cancelled","initial":{"contract":"BTC_USDT"},"trigger":{}}`), nil
		case request.Method == http.MethodPost:
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"label":"INVALID_PARAM_VALUE","message":"bad"}`))}, nil
		}
		t.Fatalf("unexpected %s %s", request.Method, request.URL.Path)
		return nil, nil
	}))).(exchange.TriggerOrderREST)
	instrument := gateTestInstrument(exchange.ProductPerp)
	// 0.01 BTC at 0.001 BTC a contract: 10 contracts, negative to close a long.
	order, err := client.PlaceTriggerOrder(context.Background(), exchange.TriggerOrderRequest{
		Instrument: instrument, ClientOrderID: "42", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.NewFromInt(90000), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.01"),
	})
	if err != nil || order.OrderID != "701" {
		t.Fatalf("placed = %+v %v", order, err)
	}
	initial, trigger := body["initial"].(map[string]any), body["trigger"].(map[string]any)
	if initial["contract"] != "BTC_USDT" || initial["size"] != float64(-10) || initial["price"] != "0" || initial["tif"] != "ioc" || initial["reduce_only"] != true || initial["text"] != "t-42" {
		t.Fatalf("initial = %+v", initial)
	}
	if trigger["price"] != "90000" || trigger["price_type"] != float64(1) || trigger["rule"] != float64(2) {
		t.Fatalf("trigger = %+v", trigger)
	}

	listed, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil {
		t.Fatal(err)
	}
	// The stop-limit (702) is left out; 703 is a whole-position take-profit.
	if len(listed) != 2 || listed[0].ClientOrderID != "42" || listed[0].Kind != exchange.TriggerStopLoss || listed[0].PriceType != exchange.TriggerPriceMark ||
		!listed[0].Quantity.Equal(decimal.RequireFromString("0.01")) || listed[1].Kind != exchange.TriggerTakeProfit || !listed[1].ClosePosition {
		t.Fatalf("listed = %+v", listed)
	}
	if found, status, err := client.LookupTriggerOrder(context.Background(), instrument, "42"); err != nil || status != exchange.TriggerFound || found.OrderID != "701" {
		t.Fatalf("lookup = %+v %s %v", found, status, err)
	}
	if _, _, err := client.LookupTriggerOrder(context.Background(), instrument, "404"); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("absent lookup = %v", err)
	}
	if err := client.CancelTriggerOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "701"}); err != nil {
		t.Fatal(err)
	}
}
