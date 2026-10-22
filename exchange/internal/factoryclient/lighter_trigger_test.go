package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

type lighterTriggerTransport struct {
	mu     sync.Mutex
	sent   []map[string]any
	reply  string
	status int
	active string
}

func (transport *lighterTriggerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/api/v1/orderBookDetails":
		return openAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}],"spot_order_book_details":[]}`), nil
	case "/api/v1/nextNonce":
		return openAPIJSONResponse(`{"code":200,"message":"","nonce":10}`), nil
	case "/api/v1/sendTx":
		_ = request.ParseMultipartForm(1 << 20)
		var payload map[string]any
		_ = json.Unmarshal([]byte(request.FormValue("tx_info")), &payload)
		payload["tx_type"] = request.FormValue("tx_type")
		transport.mu.Lock()
		transport.sent = append(transport.sent, payload)
		transport.mu.Unlock()
		if transport.status != 0 {
			return &http.Response{StatusCode: transport.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(transport.reply))}, nil
		}
		return openAPIJSONResponse(transport.reply), nil
	case "/api/v1/accountActiveOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"","orders":[` + transport.active + `]}`), nil
	case "/api/v1/accountInactiveOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"","orders":[]}`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":404}`))}, nil
}

const lighterStopRow = `{"order_index":55,"client_order_index":9,"market_index":2,"initial_base_amount":"0.010","remaining_base_amount":"0.010","filled_base_amount":"0","price":"74997.6","is_ask":true,"type":"stop-loss","time_in_force":"immediate-or-cancel","reduce_only":true,"trigger_price":"83330.7","status":"open","trigger_status":"mark-price","created_at":1720000000000,"updated_at":1720000000000}`

func newLighterTriggerClient(t *testing.T) (exchange.TriggerOrderREST, exchange.PerpClient, exchange.Instrument, *lighterTriggerTransport) {
	t.Helper()
	transport := &lighterTriggerTransport{reply: `{"code":200,"message":"","tx_hash":"0xabc","predicted_execution_time_ms":1}`}
	client := NewLighterPerp(openAPILighterPrivateKey, 7, 2, Settings{Endpoint: "https://trigger.invalid", HTTPClient: &http.Client{Transport: transport}})
	trigger, ok := client.(exchange.TriggerOrderREST)
	if !ok {
		t.Fatal("Lighter perp lacks trigger orders")
	}
	return trigger, client, lighterExactInstrument(exchange.ProductPerp, "BTC-USDC", "BTC", "2"), transport
}

func TestLighterTriggerSendsReduceOnlyStopWithProtectedLimit(t *testing.T) {
	client, _, instrument, transport := newLighterTriggerClient(t)
	transport.active = lighterStopRow
	order, err := client.PlaceTriggerOrder(context.Background(), exchange.TriggerOrderRequest{
		Instrument: instrument, ClientOrderID: "9", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("83330.7"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.OrderID != "55" || order.Kind != exchange.TriggerStopLoss || order.Status != "open" {
		t.Fatalf("order = %+v", order)
	}
	sent := transport.sent[0]
	// Type 2 (stop-loss), IOC once triggered, reduce-only, trigger and a
	// limit 10% below it, in ticks of 0.1 and sizes of 0.001.
	for key, want := range map[string]float64{"Type": 2, "TimeInForce": 0, "ReduceOnly": 1, "TriggerPrice": 833307, "Price": 749976, "BaseAmount": 10, "IsAsk": 1, "ClientOrderIndex": 9, "MarketIndex": 2} {
		if got, _ := sent[key].(float64); got != want {
			t.Fatalf("%s = %v, want %v (%+v)", key, sent[key], want, sent)
		}
	}
}

func TestLighterTriggersListApartFromOrdersAndRefuseLastPrice(t *testing.T) {
	client, perp, instrument, transport := newLighterTriggerClient(t)
	transport.active = lighterStopRow + `,` + lighterExactOrderJSON(2, 56, 10, "open")
	open, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil || len(open) != 1 || open[0].OrderID != "55" || !open[0].TriggerPrice.Equal(decimal.RequireFromString("83330.7")) || open[0].Quantity.String() != "0.01" {
		t.Fatalf("open triggers = %+v %v", open, err)
	}
	page, err := perp.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil || len(page.Orders) != 1 || page.Orders[0].OrderID != "56" {
		t.Fatalf("open orders = %+v %v", page.Orders, err)
	}
	last := exchange.TriggerOrderRequest{Instrument: instrument, ClientOrderID: "9", Side: exchange.SideSell, Kind: exchange.TriggerTakeProfit,
		TriggerPrice: decimal.RequireFromString("90000"), PriceType: exchange.TriggerPriceLast, Quantity: decimal.RequireFromString("0.01")}
	if _, err := client.PlaceTriggerOrder(context.Background(), last); !errors.Is(err, exchange.ErrInvalidRequest) || len(transport.sent) != 0 {
		t.Fatalf("last-price trigger = %v, sent %d", err, len(transport.sent))
	}
	// Absent from both lists is not proof of absence on an asynchronous venue.
	if _, _, err := client.LookupTriggerOrder(context.Background(), instrument, "404"); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("absent lookup = %v", err)
	}
}

// The order rules apply unchanged: an HTTP 400 is a rejection, while a
// 5-digit venue code (>= 500) stays ambiguous, as for ordinary orders.
func TestLighterTriggerRejection(t *testing.T) {
	client, _, instrument, transport := newLighterTriggerClient(t)
	transport.status, transport.reply = http.StatusBadRequest, `{"code":400,"message":"invalid order"}`
	_, err := client.PlaceTriggerOrder(context.Background(), exchange.TriggerOrderRequest{
		Instrument: instrument, ClientOrderID: "9", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("83330.7"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.01"),
	})
	if !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}
}
