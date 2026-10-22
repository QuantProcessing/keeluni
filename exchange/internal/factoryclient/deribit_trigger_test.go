package factoryclient

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func TestDeribitTriggerOrders(t *testing.T) {
	stub := newDeribitStub(t)
	client := newTestDeribitClient(t, stub)
	btc := deribitInstrument(t, client, "BTC-USDC")
	ctx := context.Background()
	request := exchange.TriggerOrderRequest{Instrument: btc, ClientOrderID: "17", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.NewFromInt(80000), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.001")}
	stub.set("private/sell", `{"jsonrpc":"2.0","result":{"order":{"order_id":"USDC-T1","label":"17","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","order_type":"stop_market","order_state":"untriggered","price":"market_price","amount":0.001,"filled_amount":0,"reduce_only":true,"trigger_price":80000,"trigger":"mark_price","creation_timestamp":1720000000000},"trades":[]}}`)
	order, err := client.PlaceTriggerOrder(ctx, request)
	if err != nil || order.OrderID != "USDC-T1" || order.Kind != exchange.TriggerStopLoss || order.PriceType != exchange.TriggerPriceMark || order.Status != "open" {
		t.Fatalf("placed = %+v %v", order, err)
	}
	sent := stub.last("private/sell").URL.Query()
	for key, want := range map[string]string{"type": "stop_market", "trigger_price": "80000", "trigger": "mark_price", "reduce_only": "true", "label": "17", "amount": "0.001"} {
		if sent.Get(key) != want {
			t.Fatalf("%s = %q, want %q (%v)", key, sent.Get(key), want, sent)
		}
	}
	if sent.Get("price") != "" {
		t.Fatalf("a market trigger carried a price: %v", sent)
	}
	take := request
	take.Kind, take.Side, take.PriceType = exchange.TriggerTakeProfit, exchange.SideBuy, exchange.TriggerPriceLast
	stub.set("private/buy", `{"jsonrpc":"2.0","result":{"order":{"order_id":"USDC-T2","label":"18","instrument_name":"BTC_USDC-PERPETUAL","direction":"buy","order_type":"take_market","order_state":"untriggered","amount":0.001,"trigger_price":70000,"trigger":"last_price"},"trades":[]}}`)
	if _, err := client.PlaceTriggerOrder(ctx, take); err != nil {
		t.Fatal(err)
	}
	if q := stub.last("private/buy").URL.Query(); q.Get("type") != "take_market" || q.Get("trigger") != "last_price" {
		t.Fatalf("take sent %v", q)
	}
	stub.set("private/sell", `{"jsonrpc":"2.0","error":{"message":"not_enough_funds","code":10009}}`)
	if _, err := client.PlaceTriggerOrder(ctx, request); !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}

	stub.set("private/get_open_orders", `{"jsonrpc":"2.0","result":[
		{"order_id":"USDC-T1","label":"17","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","order_type":"stop_market","order_state":"untriggered","amount":0.001,"trigger_price":80000,"trigger":"mark_price"},
		{"order_id":"USDC-T3","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","order_type":"stop_limit","order_state":"untriggered","price":79000,"amount":0.001,"trigger_price":79500,"trigger":"last_price"},
		{"order_id":"USDC-1","label":"5","instrument_name":"BTC_USDC-PERPETUAL","direction":"buy","order_type":"limit","order_state":"open","price":60000,"amount":0.001,"filled_amount":0}]}`)
	open, err := client.OpenTriggerOrders(ctx, exchange.OpenOrdersRequest{Instrument: &btc})
	if err != nil || len(open) != 1 || open[0].OrderID != "USDC-T1" || open[0].ClientOrderID != "17" {
		t.Fatalf("open triggers = %+v %v", open, err)
	}
	page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &btc})
	if err != nil || len(page.Orders) != 1 || page.Orders[0].OrderID != "USDC-1" {
		t.Fatalf("open orders = %+v %v", page.Orders, err)
	}
	stub.set("private/get_order_state_by_label", `{"jsonrpc":"2.0","result":[{"order_id":"USDC-T1","label":"17","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","order_type":"stop_market","order_state":"untriggered","amount":0.001,"trigger_price":80000,"trigger":"mark_price"}]}`)
	if order, status, err := client.LookupTriggerOrder(ctx, btc, "17"); err != nil || status != exchange.TriggerFound || order.OrderID != "USDC-T1" {
		t.Fatalf("lookup = %+v %s %v", order, status, err)
	}
}
