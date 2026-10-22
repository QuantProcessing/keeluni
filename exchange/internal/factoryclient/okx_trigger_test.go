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

func newOKXTriggerClient(t *testing.T, handler func(*http.Request) *http.Response) (exchange.TriggerOrderREST, exchange.Instrument, *okxExactLookupRouter) {
	t.Helper()
	router := &okxExactLookupRouter{handler: handler}
	client := NewOKXUSDTPerp("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductPerp, Settings{
		Endpoint: "https://okx.invalid", HTTPClient: &http.Client{Transport: router},
	}))
	trigger, ok := client.(exchange.TriggerOrderREST)
	if !ok {
		t.Fatal("OKX perp lacks trigger orders")
	}
	return trigger, okxOpenAPIFixtureInstrument(exchange.ProductPerp), router
}

func TestOKXTriggerPlacesOneWayConditionalInContracts(t *testing.T) {
	var body map[string]any
	client, instrument, _ := newOKXTriggerClient(t, func(request *http.Request) *http.Response {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v5/trade/order-algo" {
			t.Fatalf("unexpected %s %s", request.Method, request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		return okxOpenAPIData(`[{"algoId":"881","algoClOrdId":"42","sCode":"0","sMsg":""}]`)
	})
	// The fixture swap is 0.01 BTC a contract: 0.1 BTC is 10 contracts.
	order, err := client.PlaceTriggerOrder(context.Background(), exchange.TriggerOrderRequest{
		Instrument: instrument, ClientOrderID: "42", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("90"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"instId": "BTC-USDT-SWAP", "tdMode": "cross", "side": "sell", "ordType": "conditional", "sz": "10",
		"algoClOrdId": "42", "reduceOnly": true, "slTriggerPx": "90", "slOrdPx": "-1", "slTriggerPxType": "mark"} {
		if body[key] != want {
			t.Fatalf("%s = %v, want %v (%+v)", key, body[key], want, body)
		}
	}
	if _, ok := body["tpTriggerPx"]; ok {
		t.Fatalf("stop-loss carried a take-profit leg: %+v", body)
	}
	if order.OrderID != "881" || order.Kind != exchange.TriggerStopLoss || !order.Quantity.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("order = %+v", order)
	}
}

func TestOKXTriggerOutcomes(t *testing.T) {
	rejecting, instrument, _ := newOKXTriggerClient(t, func(*http.Request) *http.Response {
		return openAPIJSONResponse(`{"code":"1","msg":"","data":[{"algoId":"","algoClOrdId":"42","sCode":"51277","sMsg":"TP trigger price cannot be higher than the last price"}]}`)
	})
	request := exchange.TriggerOrderRequest{Instrument: instrument, ClientOrderID: "42", Side: exchange.SideSell, Kind: exchange.TriggerTakeProfit,
		TriggerPrice: decimal.RequireFromString("90"), PriceType: exchange.TriggerPriceLast, Quantity: decimal.RequireFromString("0.1")}
	if _, err := rejecting.PlaceTriggerOrder(context.Background(), request); !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}
	broken, _, _ := newOKXTriggerClient(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad gateway"))}
	})
	if _, err := broken.PlaceTriggerOrder(context.Background(), request); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("502 = %v", err)
	}
}

func TestOKXTriggersListConditionalAndOCOAndLookUp(t *testing.T) {
	client, instrument, router := newOKXTriggerClient(t, func(request *http.Request) *http.Response {
		switch {
		case request.URL.Path == "/api/v5/trade/orders-algo-pending" && strings.Contains(request.URL.RawQuery, "ordType=conditional"):
			return okxOpenAPIData(`[{"algoId":"881","algoClOrdId":"42","instType":"SWAP","instId":"BTC-USDT-SWAP","side":"sell","sz":"10","ordType":"conditional","state":"live","slTriggerPx":"90","slOrdPx":"-1","slTriggerPxType":"mark","cTime":"1720000000000","uTime":"1720000000000"}]`)
		case request.URL.Path == "/api/v5/trade/orders-algo-pending":
			// An OCO set on the venue's position panel: both legs, whole position.
			return okxOpenAPIData(`[{"algoId":"882","instType":"SWAP","instId":"BTC-USDT-SWAP","side":"sell","sz":"","closeFraction":"1","ordType":"oco","state":"live","tpTriggerPx":"120","tpOrdPx":"-1","tpTriggerPxType":"last","slTriggerPx":"85","slOrdPx":"84","slTriggerPxType":"last","cTime":"1720000000000"}]`)
		case request.URL.Path == "/api/v5/trade/order-algo" && request.Method == http.MethodGet:
			if strings.Contains(request.URL.RawQuery, "algoClOrdId=404") {
				return openAPIJSONResponse(`{"code":"51603","msg":"Order does not exist","data":[]}`)
			}
			return okxOpenAPIData(`[{"algoId":"881","algoClOrdId":"42","instType":"SWAP","instId":"BTC-USDT-SWAP","side":"sell","sz":"10","ordType":"conditional","state":"live","slTriggerPx":"90","slOrdPx":"-1","slTriggerPxType":"mark"}]`)
		case request.URL.Path == "/api/v5/trade/cancel-algos":
			return okxOpenAPIData(`[{"algoId":"881","sCode":"0","sMsg":""}]`)
		}
		t.Fatalf("unexpected %s %s", request.Method, request.URL.RequestURI())
		return nil
	})
	open, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil {
		t.Fatal(err)
	}
	// The OCO's stop is a limit leg (slOrdPx 84): only its take-profit shows.
	if len(open) != 2 || open[0].OrderID != "881" || open[0].PriceType != exchange.TriggerPriceMark ||
		open[1].OrderID != "882" || open[1].Kind != exchange.TriggerTakeProfit || !open[1].ClosePosition {
		t.Fatalf("open = %+v", open)
	}
	if order, status, err := client.LookupTriggerOrder(context.Background(), instrument, "42"); err != nil || status != exchange.TriggerFound || order.OrderID != "881" {
		t.Fatalf("lookup = %+v %s %v", order, status, err)
	}
	if _, status, err := client.LookupTriggerOrder(context.Background(), instrument, "404"); err != nil || status != exchange.TriggerNotFound {
		t.Fatalf("missing lookup = %s %v", status, err)
	}
	if err := client.CancelTriggerOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "881"}); err != nil {
		t.Fatal(err)
	}
	if last := router.requestAt(router.count() - 1); !strings.HasPrefix(last, "POST /api/v5/trade/cancel-algos") {
		t.Fatalf("cancel request = %q", last)
	}
}
