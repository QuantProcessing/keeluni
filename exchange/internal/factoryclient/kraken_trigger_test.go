package factoryclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func TestKrakenTriggerOrders(t *testing.T) {
	stub := newKrakenStub(t)
	client, _ := newTestKrakenClient(t, stub)
	btc := krakenBTC(t, client)
	ctx := context.Background()
	request := exchange.TriggerOrderRequest{Instrument: btc, ClientOrderID: "17", Side: exchange.SideSell, Kind: exchange.TriggerStopLoss,
		TriggerPrice: decimal.NewFromInt(60000), PriceType: exchange.TriggerPriceMark, Quantity: decimal.RequireFromString("0.0001")}

	// Kraken accepts a resting trigger with status "placed" and an id.
	stub.set("POST /derivatives/api/v3/sendorder", `{"result":"success","sendStatus":{"order_id":"t1","status":"placed","orderEvents":[{"type":"PLACE"}]}}`)
	order, err := client.PlaceTriggerOrder(ctx, request)
	if err != nil || order.OrderID != "t1" || order.Kind != exchange.TriggerStopLoss {
		t.Fatalf("placed = %+v %v", order, err)
	}
	sent := stub.last("POST /derivatives/api/v3/sendorder")
	for _, want := range []string{"orderType=stp", "stopPrice=60000", "triggerSignal=mark", "reduceOnly=true", "side=sell", "cliOrdId=17"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("sent %s lacks %s", sent, want)
		}
	}
	if strings.Contains(sent, "limitPrice=") {
		t.Fatalf("a market trigger carried a limit price: %s", sent)
	}
	take := request
	take.Kind, take.PriceType = exchange.TriggerTakeProfit, exchange.TriggerPriceLast
	if _, err := client.PlaceTriggerOrder(ctx, take); err != nil {
		t.Fatal(err)
	}
	if sent := stub.last("POST /derivatives/api/v3/sendorder"); !strings.Contains(sent, "orderType=take_profit") || !strings.Contains(sent, "triggerSignal=last") {
		t.Fatalf("take-profit sent %s", sent)
	}

	stub.set("POST /derivatives/api/v3/sendorder", `{"result":"success","sendStatus":{"status":"wouldNotReducePosition"}}`)
	if _, err := client.PlaceTriggerOrder(ctx, request); !errors.Is(err, exchange.ErrVenueRejected) {
		t.Fatalf("rejection = %v", err)
	}

	stub.set("GET /derivatives/api/v3/openorders", `{"result":"success","openOrders":[
		{"order_id":"t1","cliOrdId":"17","status":"untouched","side":"sell","orderType":"stp","symbol":"PF_XBTUSD","stopPrice":60000,"triggerSignal":"mark","filledSize":0,"unfilledSize":0.0001,"reduceOnly":true,"receivedTime":"2026-10-21T10:00:00.000Z"},
		{"order_id":"t2","status":"untouched","side":"sell","orderType":"stp","symbol":"PF_XBTUSD","stopPrice":59000,"limitPrice":58900,"triggerSignal":"last","filledSize":0,"unfilledSize":0.0001,"reduceOnly":true},
		{"order_id":"l1","status":"untouched","side":"buy","orderType":"lmt","symbol":"PF_XBTUSD","limitPrice":50000,"filledSize":0,"unfilledSize":0.0001}]}`)
	open, err := client.OpenTriggerOrders(ctx, exchange.OpenOrdersRequest{Instrument: &btc})
	if err != nil || len(open) != 1 || open[0].OrderID != "t1" || open[0].PriceType != exchange.TriggerPriceMark || open[0].ClientOrderID != "17" {
		t.Fatalf("open triggers = %+v %v", open, err)
	}
	// Ordinary open orders leave both stops out.
	page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &btc})
	if err != nil || len(page.Orders) != 1 || page.Orders[0].OrderID != "l1" {
		t.Fatalf("open orders = %+v %v", page.Orders, err)
	}

	stub.set("POST /derivatives/api/v3/orders/status", `{"result":"success","orders":[]}`)
	if _, _, err := client.LookupTriggerOrder(ctx, btc, "404"); !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("absent lookup = %v", err)
	}
}
