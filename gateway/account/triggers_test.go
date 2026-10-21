package account

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/venue"
	"github.com/shopspring/decimal"
)

func TestTriggerRequestValidation(t *testing.T) {
	support := &TriggerSupport{PriceTypes: []string{"mark"}}
	valid := TriggerRequest{Side: "sell", Kind: "stop_loss", TriggerPrice: "60000.1", Quantity: "0.01", ClientOrderID: "7"}
	native, err := valid.native(testInstrument(), support)
	if err != nil {
		t.Fatal(err)
	}
	// The default price type is the venue's first.
	if native.PriceType != exchange.TriggerPriceMark || native.Kind != exchange.TriggerStopLoss || native.Side != exchange.SideSell {
		t.Fatalf("native = %+v", native)
	}
	for name, request := range map[string]TriggerRequest{
		"client id":         {Side: "sell", Kind: "stop_loss", TriggerPrice: "1", Quantity: "1", ClientOrderID: "0"},
		"kind":              {Side: "sell", Kind: "trailing", TriggerPrice: "1", Quantity: "1", ClientOrderID: "1"},
		"side":              {Side: "close", Kind: "stop_loss", TriggerPrice: "1", Quantity: "1", ClientOrderID: "1"},
		"trigger":           {Side: "sell", Kind: "stop_loss", TriggerPrice: "-1", Quantity: "1", ClientOrderID: "1"},
		"misaligned":        {Side: "sell", Kind: "stop_loss", TriggerPrice: "1.05", Quantity: "1", ClientOrderID: "1"},
		"unsupported price": {Side: "sell", Kind: "take_profit", TriggerPrice: "1", Quantity: "1", PriceType: "last", ClientOrderID: "1"},
	} {
		if _, err := request.native(testInstrument(), support); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestClassifyTriggerNeverReportsRejectedWithoutProof(t *testing.T) {
	order := exchange.TriggerOrder{OrderID: "9", Side: exchange.SideSell, Kind: exchange.TriggerTakeProfit, TriggerPrice: decimal.NewFromInt(70000), PriceType: exchange.TriggerPriceLast, Quantity: decimal.RequireFromString("0.01"), Status: "open"}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"accepted":        {nil, OutcomeAccepted},
		"ambiguous":       {exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{}), OutcomeUnknown},
		"transport":       {exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{HTTPStatus: 502}), OutcomeUnknown},
		"plain":           {errors.New("boom"), OutcomeUnknown},
		"venue rejection": {exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Code: "-2021"}), OutcomeRejected},
		"not sent":        {exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{CommandNotSent: true}), OutcomeRejected},
		"local invalid":   {exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{}), OutcomeRejected},
	} {
		got := classifyTrigger("binance:perp:BTC-USDT", order, tc.err)
		if got.Status != tc.want {
			t.Fatalf("%s: status = %s, want %s", name, got.Status, tc.want)
		}
		if tc.want == OutcomeAccepted && (got.Trigger == nil || got.Trigger.Kind != "take_profit" || got.Trigger.Quantity != "0.01" || got.Trigger.TriggerPrice != "70000") {
			t.Fatalf("trigger = %+v", got.Trigger)
		}
	}
}

// fakeTriggerPerp is a perp client with trigger orders.
type fakeTriggerPerp struct {
	fakePerp
	placed  []exchange.TriggerOrderRequest
	resting []exchange.TriggerOrder
}

func (client *fakeTriggerPerp) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (client *fakeTriggerPerp) PlaceTriggerOrder(_ context.Context, request exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	client.placed = append(client.placed, request)
	order := exchange.TriggerOrder{NativeSymbol: request.Instrument.Identity.NativeSymbol, OrderID: "501", ClientOrderID: request.ClientOrderID, Side: request.Side, Kind: request.Kind,
		TriggerPrice: request.TriggerPrice, PriceType: request.PriceType, Quantity: request.Quantity, Status: "open"}
	client.resting = append(client.resting, order)
	return order, nil
}

func (client *fakeTriggerPerp) CancelTriggerOrder(context.Context, exchange.CancelOrderRequest) error {
	client.resting = nil
	return nil
}

func (client *fakeTriggerPerp) OpenTriggerOrders(context.Context, exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	return client.resting, nil
}

func (client *fakeTriggerPerp) LookupTriggerOrder(context.Context, exchange.Instrument, string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	return exchange.TriggerOrder{}, exchange.TriggerNotFound, nil
}

func TestRuntimeTriggersFollowTheClientFacet(t *testing.T) {
	_, _, _, catalog := brokerFixture(t)
	key := venue.Market{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT"}
	client := &fakeTriggerPerp{}
	rt := newRuntime("acc_bn", exchange.VenueBinance, []routeClient{{key: key, markets: []venue.Market{key}, perp: client}}, catalog)

	routes, err := rt.Routes(context.Background())
	if err != nil || len(routes) != 1 || routes[0].Triggers == nil || len(routes[0].Triggers.PriceTypes) != 2 || routes[0].Triggers.PriceTypes[0] != "last" {
		t.Fatalf("routes = %+v %v", routes, err)
	}
	btc, _ := catalog.Get("binance:perp:BTC-USDT")
	result, err := rt.PlaceTrigger(context.Background(), btc, TriggerRequest{Instrument: btc.ID, Side: "sell", Kind: "take_profit", TriggerPrice: "90000", Quantity: "0.01", ClientOrderID: "9"})
	if err != nil || result.Status != OutcomeAccepted || result.Trigger == nil || result.Trigger.OrderID != "501" {
		t.Fatalf("place = %+v %v", result, err)
	}
	if len(client.placed) != 1 || client.placed[0].PriceType != exchange.TriggerPriceLast {
		t.Fatalf("placed = %+v", client.placed)
	}
	open, err := rt.OpenTriggers(context.Background(), nil)
	if err != nil || len(open) != 1 || open[0].Instrument != btc.ID || open[0].Kind != "take_profit" {
		t.Fatalf("open = %+v %v", open, err)
	}
	if lookup, err := rt.LookupTrigger(context.Background(), btc, "9"); err != nil || lookup.Status != "not_found" {
		t.Fatalf("lookup = %+v %v", lookup, err)
	}

	// A client without the facet offers no triggers: the route says so and
	// placement is unsupported.
	plain := newRuntime("acc_plain", exchange.VenueBinance, []routeClient{{key: key, markets: []venue.Market{key}, perp: &fakePerp{}}}, catalog)
	if routes, _ := plain.Routes(context.Background()); len(routes) != 1 || routes[0].Triggers != nil {
		t.Fatalf("plain routes = %+v", routes)
	}
	if _, err := plain.PlaceTrigger(context.Background(), btc, TriggerRequest{Instrument: btc.ID, Side: "sell", Kind: "stop_loss", TriggerPrice: "1", Quantity: "1", ClientOrderID: "1"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("plain place = %v", err)
	}
	if open, err := plain.OpenTriggers(context.Background(), nil); err != nil || len(open) != 0 {
		t.Fatalf("plain open = %+v %v", open, err)
	}
}
