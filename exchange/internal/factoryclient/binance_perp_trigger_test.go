package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func binancePerpTriggerClient(t *testing.T, handle func(*http.Request, url.Values) (*http.Response, error)) exchange.TriggerOrderREST {
	t.Helper()
	client := NewBinanceUSDPerp("key", "secret", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			values := request.URL.Query()
			if request.Body != nil {
				body, _ := io.ReadAll(request.Body)
				if form, err := url.ParseQuery(string(body)); err == nil {
					for key, items := range form {
						values[key] = items
					}
				}
			}
			return handle(request, values)
		})},
	})
	trigger, ok := any(client).(exchange.TriggerOrderREST)
	if !ok {
		t.Fatal("Binance perp client lacks trigger orders")
	}
	return trigger
}

func binanceTriggerRequest(kind exchange.TriggerKind) exchange.TriggerOrderRequest {
	return exchange.TriggerOrderRequest{
		Instrument: binancePerpETHUSDTInstrument(), ClientOrderID: "2001", Side: exchange.SideSell, Kind: kind,
		TriggerPrice: decimal.RequireFromString("2600"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.027"),
	}
}

func TestBinancePerpTriggerPlacesReduceOnlyConditionalAlgoOrders(t *testing.T) {
	for kind, wantType := range map[exchange.TriggerKind]string{exchange.TriggerTakeProfit: "TAKE_PROFIT_MARKET", exchange.TriggerStopLoss: "STOP_MARKET"} {
		var sent url.Values
		client := binancePerpTriggerClient(t, func(request *http.Request, values url.Values) (*http.Response, error) {
			if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/fapi/v1/algoOrder") {
				t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
			}
			sent = values
			return openAPIJSONResponse(`{"algoId":77,"clientAlgoId":"2001","algoType":"CONDITIONAL","orderType":"` + wantType + `","symbol":"ETHUSDT","side":"SELL","positionSide":"BOTH","quantity":"0.027","algoStatus":"NEW","triggerPrice":"2600","workingType":"MARK_PRICE","reduceOnly":true,"createTime":1720000000000,"updateTime":1720000000000}`), nil
		})
		order, err := client.PlaceTriggerOrder(context.Background(), binanceTriggerRequest(kind))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for key, want := range map[string]string{"symbol": "ETHUSDT", "side": "SELL", "type": wantType, "algoType": "CONDITIONAL", "quantity": "0.027", "triggerPrice": "2600", "workingType": "MARK_PRICE", "reduceOnly": "true", "clientAlgoId": "2001"} {
			if got := sent.Get(key); got != want {
				t.Fatalf("%s: %s = %q, want %q", kind, key, got, want)
			}
		}
		if order.OrderID != "77" || order.Kind != kind || order.Status != "open" || order.PriceType != exchange.TriggerPriceMark || !order.TriggerPrice.Equal(decimal.RequireFromString("2600")) {
			t.Fatalf("%s: order = %+v", kind, order)
		}
	}
}

func TestBinancePerpTriggerOutcomes(t *testing.T) {
	// A definitive 400 rejection is a venue rejection, not an unknown outcome.
	rejecting := binancePerpTriggerClient(t, func(*http.Request, url.Values) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":-2021,"msg":"Order would immediately trigger."}`))}, nil
	})
	if _, err := rejecting.PlaceTriggerOrder(context.Background(), binanceTriggerRequest(exchange.TriggerStopLoss)); !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}
	// A transport failure after a possible send is ambiguous.
	failing := binancePerpTriggerClient(t, func(*http.Request, url.Values) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})
	if _, err := failing.PlaceTriggerOrder(context.Background(), binanceTriggerRequest(exchange.TriggerStopLoss)); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("transport failure = %v", err)
	}
	// Local validation never sends.
	invalid := binanceTriggerRequest(exchange.TriggerStopLoss)
	invalid.TriggerPrice = decimal.RequireFromString("2600.001")
	if _, err := failing.PlaceTriggerOrder(context.Background(), invalid); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("misaligned trigger = %v", err)
	}
}

func TestBinancePerpTriggerListsLooksUpAndCancels(t *testing.T) {
	client := binancePerpTriggerClient(t, func(request *http.Request, values url.Values) (*http.Response, error) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/openAlgoOrders"):
			if values.Get("symbol") != "ETHUSDT" {
				t.Fatalf("open orders symbol = %q", values.Get("symbol"))
			}
			// A trailing stop is not a take-profit / stop-loss: skipped.
			return openAPIJSONResponse(`[
				{"algoId":77,"clientAlgoId":"2001","orderType":"TAKE_PROFIT_MARKET","symbol":"ETHUSDT","side":"SELL","quantity":"0.027","algoStatus":"NEW","triggerPrice":"2600","workingType":"CONTRACT_PRICE","reduceOnly":true},
				{"algoId":78,"clientAlgoId":"x","orderType":"STOP_MARKET","symbol":"ETHUSDT","side":"SELL","quantity":"","closePosition":true,"algoStatus":"NEW","triggerPrice":"2300","workingType":"MARK_PRICE"},
				{"algoId":79,"clientAlgoId":"y","orderType":"TRAILING_STOP_MARKET","symbol":"ETHUSDT","side":"SELL","quantity":"1","algoStatus":"NEW","triggerPrice":"2300"}
			]`), nil
		case strings.HasSuffix(request.URL.Path, "/algoOrder") && request.Method == http.MethodGet:
			if values.Get("clientAlgoId") == "404" {
				return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":-2013,"msg":"Order does not exist."}`))}, nil
			}
			return openAPIJSONResponse(`{"algoId":77,"clientAlgoId":"2001","orderType":"TAKE_PROFIT_MARKET","symbol":"ETHUSDT","side":"SELL","quantity":"0.027","algoStatus":"NEW","triggerPrice":"2600","workingType":"CONTRACT_PRICE","reduceOnly":true}`), nil
		case strings.HasSuffix(request.URL.Path, "/algoOrder") && request.Method == http.MethodDelete:
			if values.Get("algoId") != "77" {
				t.Fatalf("cancel algoId = %q", values.Get("algoId"))
			}
			return openAPIJSONResponse(`{"algoId":77,"clientAlgoId":"2001","code":"200","msg":"success"}`), nil
		}
		t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		return nil, nil
	})
	instrument := binancePerpETHUSDTInstrument()
	open, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].Kind != exchange.TriggerTakeProfit || open[0].PriceType != exchange.TriggerPriceLast || !open[1].ClosePosition || open[1].Kind != exchange.TriggerStopLoss {
		t.Fatalf("open = %+v", open)
	}
	order, status, err := client.LookupTriggerOrder(context.Background(), instrument, "2001")
	if err != nil || status != exchange.TriggerFound || order.OrderID != "77" {
		t.Fatalf("lookup = %+v %s %v", order, status, err)
	}
	if _, status, err := client.LookupTriggerOrder(context.Background(), instrument, "404"); err != nil || status != exchange.TriggerNotFound {
		t.Fatalf("missing lookup = %s %v", status, err)
	}
	if err := client.CancelTriggerOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "77"}); err != nil {
		t.Fatal(err)
	}
}
