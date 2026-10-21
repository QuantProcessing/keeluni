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

func TestHyperliquidTriggerLimitPriceBoundsTheFill(t *testing.T) {
	for _, tc := range []struct {
		side     exchange.Side
		trigger  string
		decimals int
		want     string
	}{
		{exchange.SideSell, "2300", 2, "2070"},    // 2300 × 0.9
		{exchange.SideBuy, "2300", 2, "2530"},     // 2300 × 1.1
		{exchange.SideSell, "83456", 1, "75110"},  // 75110.4 → integer (5 sig figs)
		{exchange.SideBuy, "83456", 1, "91802"},   // 91801.6 rounded up
		{exchange.SideSell, "1.2345", 4, "1.111"}, // 1.11105 → 1.1110 floor
		{exchange.SideSell, "0.012345", 6, "0.01111"},
		{exchange.SideBuy, "0.0001234", 6, "0.000136"}, // capped at 6 decimals, ceil
	} {
		got, err := hlTriggerLimitPrice(tc.side, decimal.RequireFromString(tc.trigger), tc.decimals)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.side, tc.trigger, err)
		}
		if !got.Equal(decimal.RequireFromString(tc.want)) {
			t.Fatalf("%s %s: got %s, want %s", tc.side, tc.trigger, got, tc.want)
		}
		if _, err := hlValidateLimitPrice(tc.side, got, tc.decimals); err != nil {
			t.Fatalf("%s %s: bound %s violates the price rule: %v", tc.side, tc.trigger, got, err)
		}
	}
}

type hyperliquidTriggerTransport struct {
	mu      sync.Mutex
	actions []map[string]any
	place   string // the /exchange reply
}

func (transport *hyperliquidTriggerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if strings.HasSuffix(request.URL.Path, "/exchange") {
		transport.mu.Lock()
		action, _ := payload["action"].(map[string]any)
		transport.actions = append(transport.actions, action)
		transport.mu.Unlock()
		if action["type"] == "cancel" {
			return openAPIJSONResponse(`{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`), nil
		}
		return openAPIJSONResponse(transport.place), nil
	}
	switch payload["type"] {
	case "frontendOpenOrders":
		return openAPIJSONResponse(`[
			{"coin":"BTC","side":"A","limitPx":"75110","sz":"0.01","oid":77,"cloid":"0x00000000000000000000000000000009","timestamp":1720000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Stop Market","tif":null,"isTrigger":true,"triggerPx":"83456","triggerCondition":"Price below 83456","isPositionTpsl":false},
			{"coin":"BTC","side":"A","limitPx":"90000","sz":"0.0","oid":78,"timestamp":1720000000000,"origSz":"0.0","reduceOnly":true,"orderType":"Take Profit Market","isTrigger":true,"triggerPx":"95000","isPositionTpsl":true},
			{"coin":"BTC","side":"A","limitPx":"90000","sz":"0.01","oid":79,"timestamp":1720000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Stop Limit","isTrigger":true,"triggerPx":"80000"},
			{"coin":"BTC","side":"B","limitPx":"80000","sz":"0.01","oid":80,"timestamp":1720000000000,"origSz":"0.01","reduceOnly":false,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0"}
		]`), nil
	case "orderStatus":
		return openAPIJSONResponse(`{"status":"order","order":{"order":{"coin":"BTC","side":"A","limitPx":"75110","sz":"0.01","oid":77,"cloid":"0x00000000000000000000000000000009","timestamp":1720000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Stop Market","tif":null,"isTrigger":true,"triggerPx":"83456"},"status":"open","statusTimestamp":1720000000000}}`), nil
	}
	return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unexpected"}`))}, nil
}

func newHyperliquidTriggerClient(t *testing.T, place string) (exchange.TriggerOrderREST, exchange.PerpClient, exchange.Instrument, *hyperliquidTriggerTransport) {
	t.Helper()
	transport := &hyperliquidTriggerTransport{place: place}
	instrument := hyperliquidCatalogInstrument(exchange.ProductPerp, "BTC-USDC", "BTC", "0", "USDC", 5)
	instrument.Rules.PriceIncrement = decimal.RequireFromString("1")
	hlApplyScaleMetadata(&instrument, 1, 5, 6)
	scope := exchange.CatalogScope{Venue: exchange.VenueHyperliquid, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDC"}
	client := NewHyperliquidPerp(openAPITestPrivateKey, Settings{
		Endpoint:          "https://trigger.invalid",
		HTTPClient:        &http.Client{Transport: transport},
		InstrumentCatalog: exchange.NewMemoryCatalog(exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: []exchange.Instrument{instrument}}}, nil),
		CatalogScope:      scope,
	})
	trigger, ok := client.(exchange.TriggerOrderREST)
	if !ok {
		t.Fatal("Hyperliquid perp lacks trigger orders")
	}
	return trigger, client, instrument, transport
}

func hlStopRequest(instrument exchange.Instrument) exchange.TriggerOrderRequest {
	return exchange.TriggerOrderRequest{Instrument: instrument, ClientOrderID: "9", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("83456"), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.01")}
}

func TestHyperliquidTriggerPlacesReduceOnlyMarketTrigger(t *testing.T) {
	client, _, instrument, transport := newHyperliquidTriggerClient(t, `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":77,"cloid":"0x00000000000000000000000000000009"}}]}}}`)
	if got := client.TriggerPriceTypes(); len(got) != 1 || got[0] != exchange.TriggerPriceMark {
		t.Fatalf("price types = %v", got)
	}
	order, err := client.PlaceTriggerOrder(context.Background(), hlStopRequest(instrument))
	if err != nil {
		t.Fatal(err)
	}
	if order.OrderID != "77" || order.Kind != exchange.TriggerStopLoss || order.PriceType != exchange.TriggerPriceMark {
		t.Fatalf("order = %+v", order)
	}
	wire := transport.actions[0]["orders"].([]any)[0].(map[string]any)
	trigger := wire["t"].(map[string]any)["trigger"].(map[string]any)
	if wire["r"] != true || wire["b"] != false || wire["p"] != "75110" || wire["s"] != "0.01" ||
		trigger["isMarket"] != true || trigger["tpsl"] != "sl" || trigger["triggerPx"] != "83456" {
		t.Fatalf("wire order = %+v", wire)
	}
	// A last-price trigger is refused before anything is sent.
	last := hlStopRequest(instrument)
	last.PriceType = exchange.TriggerPriceLast
	if _, err := client.PlaceTriggerOrder(context.Background(), last); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("last-price trigger = %v", err)
	}
	if len(transport.actions) != 1 {
		t.Fatalf("refused request was sent: %d actions", len(transport.actions))
	}
}

func TestHyperliquidTriggerWaitingForTriggerReadsTheOrderBack(t *testing.T) {
	client, _, instrument, _ := newHyperliquidTriggerClient(t, `{"status":"ok","response":{"type":"order","data":{"statuses":["waitingForTrigger"]}}}`)
	order, err := client.PlaceTriggerOrder(context.Background(), hlStopRequest(instrument))
	if err != nil {
		t.Fatal(err)
	}
	if order.OrderID != "77" || order.ClientOrderID != "9" || !order.TriggerPrice.Equal(decimal.RequireFromString("83456")) {
		t.Fatalf("order = %+v", order)
	}
}

func TestHyperliquidTriggerRejection(t *testing.T) {
	client, _, instrument, _ := newHyperliquidTriggerClient(t, `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Reduce only order would increase position."}]}}}`)
	if _, err := client.PlaceTriggerOrder(context.Background(), hlStopRequest(instrument)); !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}
}

func TestHyperliquidTriggersListApartFromOrders(t *testing.T) {
	client, perp, instrument, transport := newHyperliquidTriggerClient(t, "")
	open, err := client.OpenTriggerOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil {
		t.Fatal(err)
	}
	// Stop market and the position take-profit; the stop-limit is skipped.
	if len(open) != 2 || open[0].OrderID != "77" || open[0].ClientOrderID != "9" || open[0].Quantity.String() != "0.01" ||
		open[1].Kind != exchange.TriggerTakeProfit || !open[1].ClosePosition {
		t.Fatalf("open triggers = %+v", open)
	}
	// Ordinary open orders no longer fail on trigger rows.
	page, err := perp.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: &instrument})
	if err != nil || len(page.Orders) != 1 || page.Orders[0].OrderID != "80" {
		t.Fatalf("open orders = %+v %v", page.Orders, err)
	}
	if err := client.CancelTriggerOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "77"}); err != nil {
		t.Fatal(err)
	}
	if last := transport.actions[len(transport.actions)-1]; last["type"] != "cancel" {
		t.Fatalf("cancel action = %+v", last)
	}
	order, status, err := client.LookupTriggerOrder(context.Background(), instrument, "9")
	if err != nil || status != exchange.TriggerFound || order.OrderID != "77" || order.Status != "open" {
		t.Fatalf("lookup = %+v %s %v", order, status, err)
	}
}
