package account

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/venue"
	"github.com/shopspring/decimal"
)

// fakeBroker stands in for the CrossEx client: one perp client whose
// instruments keep the reached venue's identity, with its own symbols and
// rules.
type fakeBroker struct {
	exchange.PerpClient
	instruments []exchange.Instrument
	socket      *fakeBrokerSocket

	mu      sync.Mutex
	placed  []exchange.PlaceOrderRequest
	indexed []exchange.AccountInstrument
	route   exchange.AccountRoute
}

func (client *fakeBroker) Instruments(context.Context) ([]exchange.Instrument, error) {
	return client.instruments, nil
}

func (client *fakeBroker) ResolveInstrument(_ context.Context, symbol string) (exchange.Instrument, error) {
	for _, instrument := range client.instruments {
		if string(instrument.Identity.Venue)+":"+instrument.Identity.Symbol == symbol || instrument.Identity.NativeSymbol == symbol {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{SafeMessage: "not found"})
}

func (client *fakeBroker) PlaceOrder(_ context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.mu.Lock()
	client.placed = append(client.placed, request)
	client.mu.Unlock()
	return exchange.OrderAcknowledgement{Venue: request.Instrument.Identity.Venue, Product: exchange.ProductPerp, Operation: exchange.OrderOperationPlace,
		State: exchange.AckAcceptedPending, NativeSymbol: request.Instrument.Identity.NativeSymbol, OrderID: "9", ClientOrderID: request.ClientOrderID}, nil
}

func (client *fakeBroker) Balances(context.Context) ([]exchange.Balance, error) {
	return []exchange.Balance{{Asset: "USDT", Total: decimal.NewFromInt(1500), Available: decimal.NewFromInt(1200), Locked: decimal.NewFromInt(300)}}, nil
}

func (client *fakeBroker) Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error) {
	return []exchange.Position{
		{NativeSymbol: "BINANCE_FUTURE_BTC_USDT", PositionSlot: exchange.PositionSlotNet, Quantity: decimal.RequireFromString("0.01"), EntryPrice: decimal.NewFromInt(60000)},
		{NativeSymbol: "KRAKEN_FUTURE_BTC_USD", PositionSlot: exchange.PositionSlotNet, Quantity: decimal.RequireFromString("-0.02"), EntryPrice: decimal.NewFromInt(60100)},
	}, nil
}

func (client *fakeBroker) MarginPool(context.Context) (exchange.MarginPool, error) {
	return exchange.MarginPool{
		Equity:                exchange.OptionalDecimal{Value: decimal.NewFromInt(1480), Valid: true},
		Available:             exchange.OptionalDecimal{Value: decimal.NewFromInt(1200), Valid: true},
		MaintenanceMarginRate: exchange.OptionalDecimal{Value: decimal.RequireFromString("4.8"), Valid: true},
		Venues:                []exchange.VenueMarginShare{{Venue: exchange.VenueBinance, Asset: "USDT", Equity: decimal.NewFromInt(875), Available: decimal.NewFromInt(700)}},
	}, nil
}

func (client *fakeBroker) WebSocket() exchange.PerpWebSocket { return client.socket }
func (client *fakeBroker) Close() error                      { return nil }

type fakeBrokerSocket struct {
	exchange.PerpWebSocket
	broker       *fakeBroker
	subscription *fakeSubscription
}

func (socket *fakeBrokerSocket) SetAccountInstruments(items []exchange.AccountInstrument) error {
	socket.broker.mu.Lock()
	socket.broker.indexed = items
	socket.broker.mu.Unlock()
	return nil
}

func (socket *fakeBrokerSocket) WatchAccountEvents(_ context.Context, request exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	socket.broker.mu.Lock()
	socket.broker.route = request.Route
	socket.broker.mu.Unlock()
	return socket.subscription, nil
}

func perpInstrument(venueName exchange.Venue, symbol, base, settle, native, step string) exchange.Instrument {
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: venueName, Product: exchange.ProductPerp, Symbol: symbol, BaseAsset: base,
			QuoteAsset: settle, SettleAsset: settle, NativeSymbol: native},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString(step), MinQuantity: decimal.RequireFromString(step)},
	}
}

func brokerFixture(t *testing.T) (*runtime, *fakeBroker, *fakeSubscription, *market.Catalog) {
	t.Helper()
	nativeBTC := perpInstrument(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.01")
	nativeKraken := perpInstrument(exchange.VenueKraken, "BTC-USD", "BTC", "USD", "PF_XBTUSD", "0.0001")
	catalog := market.NewStaticCatalog([]exchange.Instrument{nativeBTC, nativeKraken})

	// The broker's view: the same identities, its own symbols and rules.
	brokerBTC := perpInstrument(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BINANCE_FUTURE_BTC_USDT", "0.001")
	brokerBTC.Rules.MinNotional = exchange.OptionalDecimal{Value: decimal.NewFromInt(50), Valid: true}
	brokerKraken := perpInstrument(exchange.VenueKraken, "BTC-USD", "BTC", "USD", "KRAKEN_FUTURE_BTC_USD", "0.0001")
	subscription := newFakeSubscription()
	broker := &fakeBroker{instruments: []exchange.Instrument{brokerBTC, brokerKraken}}
	broker.socket = &fakeBrokerSocket{broker: broker, subscription: subscription}

	crossex, _ := venue.BrokerOf(exchange.VenueCrossEx)
	key := venue.Market{Venue: exchange.VenueCrossEx, Product: exchange.ProductPerp}
	rt := newRuntime("acc_cx", exchange.VenueCrossEx, []routeClient{{key: key, markets: crossex.Markets, perp: broker, broker: true}}, catalog)
	return rt, broker, subscription, catalog
}

func TestBrokerAccountTradesEveryReachedVenueWithBrokerRules(t *testing.T) {
	rt, broker, _, catalog := brokerFixture(t)
	ctx := context.Background()
	btc, _ := catalog.Get("binance:perp:BTC-USDT")
	kraken, _ := catalog.Get("kraken:perp:BTC-USD")

	// 0.005 BTC is below the native step (0.01) but on CrossEx's (0.001).
	result, err := rt.Place(ctx, btc, PlaceRequest{Instrument: btc.ID, Side: "buy", Type: "limit", Quantity: "0.005", Price: "60000", ClientOrderID: "17"})
	if err != nil || result.Status != OutcomeAccepted || result.Order.Status != "pending" || result.Order.Instrument != "binance:perp:BTC-USDT" {
		t.Fatalf("place = %+v, %v", result, err)
	}
	// Below CrossEx's 50 USDT minimum notional: refused locally.
	if _, err := rt.Place(ctx, btc, PlaceRequest{Instrument: btc.ID, Side: "buy", Type: "limit", Quantity: "0.001", Price: "100", ClientOrderID: "18"}); err == nil {
		t.Fatal("order below the broker's minimum notional was sent")
	}
	if _, err := rt.Place(ctx, kraken, PlaceRequest{Instrument: kraken.ID, Side: "sell", Type: "market", Quantity: "0.0001", ClientOrderID: "19"}); err != nil {
		t.Fatalf("kraken place: %v", err)
	}
	broker.mu.Lock()
	sent := append([]exchange.PlaceOrderRequest(nil), broker.placed...)
	broker.mu.Unlock()
	if len(sent) != 2 || sent[0].Instrument.Identity.NativeSymbol != "BINANCE_FUTURE_BTC_USDT" || sent[1].Instrument.Identity.NativeSymbol != "KRAKEN_FUTURE_BTC_USD" {
		t.Fatalf("sent = %+v", sent)
	}

	routes, err := rt.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[venue.InstrumentID]Route{}
	for _, route := range routes {
		byID[route.Instrument] = route
	}
	if len(byID) != 2 || byID["binance:perp:BTC-USDT"].StepSize != "0.001" || byID["binance:perp:BTC-USDT"].MinNotional != "50" {
		t.Fatalf("routes = %+v", routes)
	}
}

func TestNativeAccountTradesOnlyItsOwnVenue(t *testing.T) {
	_, _, _, catalog := brokerFixture(t)
	key := venue.Market{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT"}
	native := newRuntime("acc_bn", exchange.VenueBinance, []routeClient{{key: key, markets: []venue.Market{key}, perp: &fakePerp{}}}, catalog)
	kraken, _ := catalog.Get("kraken:perp:BTC-USD")
	if _, err := native.clientFor(kraken); err == nil {
		t.Fatal("a Binance account accepted a Kraken instrument")
	}
	btc, _ := catalog.Get("binance:perp:BTC-USDT")
	if _, err := native.clientFor(btc); err != nil {
		t.Fatalf("native account refused its own instrument: %v", err)
	}
	routes, _ := native.Routes(context.Background())
	if len(routes) != 1 || routes[0].Instrument != "binance:perp:BTC-USDT" {
		t.Fatalf("native routes = %+v", routes)
	}
}

func TestBrokerAccountStreamsEveryVenueAndPublishesMargin(t *testing.T) {
	rt, broker, subscription, _ := brokerFixture(t)
	sink := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.stream(ctx, sink.emit) }()

	// The REST snapshot resolves CrossEx symbols to gateway ids on two venues.
	waitFor(t, func() bool {
		positions, ok := sink.lastPositions()
		return ok && len(positions) == 2
	})
	positions, _ := sink.lastPositions()
	ids := []string{string(positions[0].Instrument), string(positions[1].Instrument)}
	if strings.Join(ids, ",") != "binance:perp:BTC-USDT,kraken:perp:BTC-USD" || positions[1].Side != "short" {
		t.Fatalf("positions = %+v", positions)
	}

	// One account route names the broker; the index carries the broker's symbols.
	broker.mu.Lock()
	route, indexed := broker.route, broker.indexed
	broker.mu.Unlock()
	if route.Venue != exchange.VenueCrossEx || route.Validate() != nil {
		t.Fatalf("route = %+v", route)
	}
	natives := map[string]string{}
	for _, item := range indexed {
		natives[item.InstrumentID] = item.Instrument.Identity.NativeSymbol
	}
	if natives["binance:perp:BTC-USDT"] != "BINANCE_FUTURE_BTC_USDT" || natives["kraken:perp:BTC-USD"] != "KRAKEN_FUTURE_BTC_USD" {
		t.Fatalf("index = %v", natives)
	}

	// A rejection after acceptance carries the reason and the order's origin.
	subscription.events <- exchange.AccountEvent{Kind: exchange.AccountEventOrder, InstrumentID: "binance:perp:BTC-USDT", ObservedAt: time.Now(),
		Order: &exchange.OrderEvent{Order: exchange.Order{OrderID: "9", ClientOrderID: "17", Status: "rejected",
			RejectCode: "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR", RejectMessage: "Insufficient availableMargin"}}}
	waitFor(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, message := range sink.messages {
			if order, ok := message.Data.(Order); ok && message.Channel == ChannelOrders {
				return order.Status == "rejected" && order.RejectCode == "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR" && order.RejectMessage == "Insufficient availableMargin"
			}
		}
		return false
	})

	// The pool is published; balances are labeled with the broker, not a market.
	waitFor(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		var margin *Margin
		var balances []Balance
		for _, message := range sink.messages {
			switch message.Channel {
			case ChannelMargin:
				margin = message.Data.(*Margin)
			case ChannelBalances:
				balances = message.Data.([]Balance)
			}
		}
		return margin != nil && margin.Available == "1200" && margin.MaintenanceMarginRate == "4.8" && len(margin.ByVenue) == 1 &&
			len(balances) == 1 && balances[0].Market == "crossex"
	})
	if names := rt.marketNames(); len(names) != 10 || rt.kind() != "broker" {
		t.Fatalf("markets = %v kind = %s", names, rt.kind())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
