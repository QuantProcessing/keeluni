package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	kraken "github.com/QuantProcessing/boltertrader/sdk/kraken"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

// krakenStub serves the Kraken REST surface from the SDK's captured fixtures
// and per-test overrides, and a WebSocket that replays scripted frames.
type krakenStub struct {
	server *httptest.Server
	mu     sync.Mutex
	routes map[string]string
	seen   []string
	ws     func(conn *websocket.Conn)
}

func newKrakenStub(t *testing.T) *krakenStub {
	t.Helper()
	stub := &krakenStub{routes: map[string]string{
		"GET /derivatives/api/v3/instruments": "@instruments.json",
		"GET /derivatives/api/v3/orderbook":   "@orderbook.json",
		"GET /derivatives/api/v3/tickers":     "@tickers.json",
		"GET /derivatives/api/v3/history":     "@history.json",
	}}
	upgrader := websocket.Upgrader{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/v1" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"info","version":1}`))
			stub.mu.Lock()
			script := stub.ws
			stub.mu.Unlock()
			if script != nil {
				script(conn)
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		stub.mu.Lock()
		stub.seen = append(stub.seen, key+"?"+r.URL.RawQuery+"|"+string(body))
		payload, ok := stub.routes[key]
		stub.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/charts/") {
			payload, ok = "@candles.json", true
		}
		if strings.HasPrefix(r.URL.Path, "/derivatives/api/v3/tickers/") {
			payload, ok = `{"result":"success","ticker":{"symbol":"PF_XBTUSD","markPrice":80999.87,"relativeFundingRate":5.9655e-06,"fundingRate":0.4857}}`, true
		}
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasPrefix(payload, "@") {
			data, err := os.ReadFile("../../../sdk/kraken/testdata/" + payload[1:])
			if err != nil {
				t.Error(err)
			}
			payload = string(data)
		}
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *krakenStub) set(route, body string) {
	stub.mu.Lock()
	stub.routes[route] = body
	stub.mu.Unlock()
}

func (stub *krakenStub) last(prefix string) string {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for index := len(stub.seen) - 1; index >= 0; index-- {
		if strings.HasPrefix(stub.seen[index], prefix) {
			return stub.seen[index]
		}
	}
	return ""
}

func newTestKrakenClient(t *testing.T, stub *krakenStub) (*krakenPerpClient, exchange.InstrumentCatalog) {
	t.Helper()
	catalog := exchange.NewDefaultMemoryCatalog()
	settings := Settings{Endpoint: stub.server.URL, WebSocketEndpoint: "ws" + strings.TrimPrefix(stub.server.URL, "http") + "/ws/v1",
		HTTPClient: stub.server.Client(), InstrumentCatalog: catalog}
	settings.SourceDescriptor = exchange.SourceDescriptor{Name: "kraken-usd-perp", Endpoint: settings.Endpoint, WebSocketEndpoint: settings.WebSocketEndpoint, Transport: "test"}
	settings.CatalogScope = exchange.CatalogScope{Venue: exchange.VenueKraken, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USD",
		EndpointFingerprint: settings.SourceDescriptor.Fingerprint()}
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, NewKrakenInstrumentLoader(settings)); err != nil {
		t.Fatal(err)
	}
	client := NewKrakenPerp("key", "c2VjcmV0", settings).(*krakenPerpClient)
	t.Cleanup(func() { _ = client.Close() })
	return client, catalog
}

func krakenBTC(t *testing.T, client *krakenPerpClient) exchange.Instrument {
	t.Helper()
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USD")
	if err != nil {
		t.Fatal(err)
	}
	return instrument
}

func TestKrakenCatalogKeepsTradeableLinearPerps(t *testing.T) {
	client, _ := newTestKrakenClient(t, newKrakenStub(t))
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// PI_XBTUSD (inverse) is skipped; BTC, ETH, ADA linear perps remain.
	got := map[string]exchange.Instrument{}
	for _, instrument := range instruments {
		got[instrument.Identity.Symbol] = instrument
	}
	btc, ok := got["BTC-USD"]
	if !ok || btc.Identity.NativeSymbol != "PF_XBTUSD" || btc.Identity.SettleAsset != "USD" ||
		!btc.Rules.PriceIncrement.Equal(decimal.NewFromInt(1)) || !btc.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("BTC = %+v", btc)
	}
	for symbol := range got {
		if !strings.HasSuffix(symbol, "-USD") {
			t.Fatalf("unexpected %s", symbol)
		}
	}
	if btc.Metadata.AssetClass != exchange.AssetClassCrypto {
		t.Fatalf("asset class = %s", btc.Metadata.AssetClass)
	}
	// Negative precision trades in steps of ten.
	rows := []kraken.Instrument{{Symbol: "PF_PEPEUSD", Type: "flexible_futures", Base: "PEPE", Quote: "USD", TickSize: "0.0000001", ContractSize: "1", ContractValueTradePrecision: "-3", Tradeable: true, Category: "Meme"}}
	pepe, err := krakenCatalogInstruments(rows)
	if err != nil || !pepe[0].Rules.QuantityIncrement.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("pepe = %+v, %v", pepe, err)
	}
}

func TestKrakenMarketData(t *testing.T) {
	client, _ := newTestKrakenClient(t, newKrakenStub(t))
	btc := krakenBTC(t, client)
	ctx := context.Background()
	book, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: btc, Limit: 2})
	if err != nil || len(book.Bids) != 2 || len(book.Asks) != 2 || !book.Bids[0].Price.GreaterThan(book.Bids[1].Price) || !book.Asks[0].Price.LessThan(book.Asks[1].Price) {
		t.Fatalf("book = %+v, %v", book, err)
	}
	if !book.Bids[0].Price.LessThan(book.Asks[0].Price) {
		t.Fatal("book is crossed")
	}
	page, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: btc, Interval: "1m", Start: time.UnixMilli(1792333980000), End: time.UnixMilli(1792334100000)})
	if err != nil || len(page.Candles) != 3 || !page.Candles[0].OpenTime.Before(page.Candles[1].OpenTime) {
		t.Fatalf("candles = %+v, %v", page, err)
	}
	trades, err := client.PublicTrades(ctx, exchange.PublicTradesRequest{Instrument: btc})
	if err != nil || len(trades.Trades) != 3 {
		t.Fatalf("trades = %+v, %v", trades, err)
	}
	rate, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: btc})
	if err != nil || !rate.Rate.Equal(decimal.RequireFromString("5.9655e-06")) || !rate.MarkPrice.Valid || rate.NextFundingTime.Minute() != 0 {
		t.Fatalf("funding = %+v, %v", rate, err)
	}
	if _, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: btc, Interval: "3m"}); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("bad interval err = %v", err)
	}
}

func TestKrakenPlaceOrderOutcomes(t *testing.T) {
	stub := newKrakenStub(t)
	client, _ := newTestKrakenClient(t, stub)
	btc := krakenBTC(t, client)
	ctx := context.Background()
	request := exchange.PlaceOrderRequest{Instrument: btc, ClientOrderID: "17", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.0001"), LimitPrice: decimal.NewFromInt(60000), LimitPolicy: exchange.LimitPolicyPostOnly}

	stub.set("POST /derivatives/api/v3/sendorder", `{"result":"success","sendStatus":{"order_id":"179f9af8","status":"placed","orderEvents":[{"type":"PLACE","order":{"orderId":"179f9af8","cliOrdId":"17","type":"post","symbol":"PF_XBTUSD","side":"buy","quantity":0.0001,"filled":0,"limitPrice":60000}}]}}`)
	ack, err := client.PlaceOrder(ctx, request)
	if err != nil || ack.State != exchange.AckResting || ack.OrderID != "179f9af8" {
		t.Fatalf("resting ack = %+v, %v", ack, err)
	}
	if sent := stub.last("POST /derivatives/api/v3/sendorder"); !strings.Contains(sent, "orderType=post") || !strings.Contains(sent, "cliOrdId=17") || !strings.Contains(sent, "symbol=PF_XBTUSD") {
		t.Fatalf("sent = %s", sent)
	}

	stub.set("POST /derivatives/api/v3/sendorder", `{"result":"success","sendStatus":{"order_id":"a1","status":"placed","orderEvents":[{"type":"EXECUTION","price":60000,"amount":0.0001,"executionId":"e1"}]}}`)
	ack, err = client.PlaceOrder(ctx, request)
	if err != nil || ack.State != exchange.AckImmediatelyFilled || !ack.FilledQuantity.Equal(decimal.RequireFromString("0.0001")) || !ack.AverageFillPrice.Value.Equal(decimal.NewFromInt(60000)) {
		t.Fatalf("filled ack = %+v, %v", ack, err)
	}

	stub.set("POST /derivatives/api/v3/sendorder", `{"result":"success","sendStatus":{"status":"insufficientAvailableFunds"}}`)
	ack, err = client.PlaceOrder(ctx, request)
	if !errors.Is(err, exchange.ErrVenueRejected) || ack.State != exchange.AckRejected || ack.VenueCode != "insufficientAvailableFunds" || ack.VenueMessage != "Insufficient available funds" {
		t.Fatalf("rejected ack = %+v, %v", ack, err)
	}

	stub.set("POST /derivatives/api/v3/sendorder", `{"result":"success","sendStatus":{"order_id":"b2","status":"placed","orderEvents":[{"type":"CANCEL","reason":"POST_WOULD_EXECUTE"}]}}`)
	ack, err = client.PlaceOrder(ctx, request)
	if err != nil || ack.State != exchange.AckCanceled {
		t.Fatalf("cancelled ack = %+v, %v", ack, err)
	}

	// A server failure proves nothing about the order.
	stub.set("POST /derivatives/api/v3/sendorder", "")
	stub.mu.Lock()
	delete(stub.routes, "POST /derivatives/api/v3/sendorder")
	stub.mu.Unlock()
	ack, err = client.PlaceOrder(ctx, request)
	if ack.State == exchange.AckRejected || err == nil {
		t.Fatalf("404 ack = %+v, %v", ack, err)
	}
}

func TestKrakenAccountAndCancel(t *testing.T) {
	stub := newKrakenStub(t)
	client, _ := newTestKrakenClient(t, stub)
	btc := krakenBTC(t, client)
	ctx := context.Background()
	stub.set("GET /derivatives/api/v3/accounts", `{"result":"success","accounts":{"flex":{"type":"multiCollateralMarginAccount","currencies":{"USD":{"quantity":5000,"value":5000,"collateral":5000,"available":4000},"XBT":{"quantity":0.01,"value":810,"collateral":790,"available":0.01}},"portfolioValue":5810,"initialMarginWithOrders":1000,"marginEquity":5790,"availableMargin":4790,"totalUnrealized":12.5}}}`)
	account, err := client.PerpAccount(ctx)
	if err != nil || !account.Equity.Value.Equal(decimal.NewFromInt(5790)) || !account.Available.Value.Equal(decimal.NewFromInt(4790)) || len(account.Balances) != 2 {
		t.Fatalf("account = %+v, %v", account, err)
	}
	if account.Balances[0].Asset != "USD" || !account.Balances[0].Locked.Equal(decimal.NewFromInt(1000)) || account.Balances[1].Asset != "BTC" {
		t.Fatalf("balances = %+v", account.Balances)
	}
	stub.set("GET /derivatives/api/v3/openpositions", `{"result":"success","openPositions":[{"side":"short","symbol":"PF_XBTUSD","price":81000,"size":0.5,"unrealizedPnl":-12}]}`)
	positions, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: &btc})
	if err != nil || len(positions) != 1 || !positions[0].Quantity.Equal(decimal.RequireFromString("-0.5")) || positions[0].Side != exchange.SideSell || !positions[0].MarkPrice.IsPositive() {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	stub.set("POST /derivatives/api/v3/cancelorder", `{"result":"success","cancelStatus":{"status":"cancelled","order_id":"o1"}}`)
	if ack, err := client.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: btc, OrderID: "o1"}); err != nil || ack.State != exchange.AckCanceled {
		t.Fatalf("cancel = %+v, %v", ack, err)
	}
	stub.set("POST /derivatives/api/v3/cancelorder", `{"result":"success","cancelStatus":{"status":"notFound"}}`)
	if _, err := client.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: btc, OrderID: "o1"}); !errors.Is(err, exchange.ErrNotFound) {
		t.Fatalf("cancel missing err = %v", err)
	}
	stub.set("PUT /derivatives/api/v3/leveragepreferences", `{"result":"success"}`)
	if leverage, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: btc, Leverage: 5}); err != nil || leverage.Effective != 5 {
		t.Fatalf("leverage = %+v, %v", leverage, err)
	}
	stub.set("GET /derivatives/api/v3/accounts", `{"result":"error","error":"authenticationError"}`)
	if _, err := client.Balances(ctx); !errors.Is(err, exchange.ErrAuthentication) {
		t.Fatalf("auth err = %v", err)
	}
}

func TestKrakenExactLookup(t *testing.T) {
	stub := newKrakenStub(t)
	client, _ := newTestKrakenClient(t, stub)
	btc := krakenBTC(t, client)
	ctx := context.Background()
	stub.set("POST /derivatives/api/v3/orders/status", `{"result":"success","orders":[{"order":{"orderId":"o1","cliOrdId":"17","type":"lmt","symbol":"PF_XBTUSD","side":"buy","quantity":1,"filled":0.4,"limitPrice":60000,"timestamp":"2026-10-18T10:00:00Z","lastUpdateTimestamp":"2026-10-18T10:00:01Z"},"status":"ENTERED_BOOK"}]}`)
	result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "17"})
	if err != nil || result.Status != exchange.ExactOrderFound || result.Order.Status != "partially_filled" || result.Order.OrderID != "o1" {
		t.Fatalf("found = %+v, %v", result, err)
	}
	stub.set("POST /derivatives/api/v3/orders/status", `{"result":"success","orders":[]}`)
	stub.set("GET /derivatives/api/v3/fills", `{"result":"success","fills":[{"fill_id":"f1","symbol":"PF_XBTUSD","side":"buy","order_id":"o2","cliOrdId":"18","size":0.5,"price":60000,"fillTime":"2026-10-18T10:00:00Z","fillType":"taker"},{"fill_id":"f2","symbol":"PF_XBTUSD","side":"buy","order_id":"o2","cliOrdId":"18","size":0.5,"price":61000,"fillTime":"2026-10-18T10:00:01Z","fillType":"taker"}]}`)
	result, err = client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "18"})
	if err != nil || result.Status != exchange.ExactOrderFound || result.Order.Status != "filled" || !result.Order.Filled.Equal(decimal.NewFromInt(1)) || !result.Order.AverageFillPrice.Value.Equal(decimal.NewFromInt(60500)) {
		t.Fatalf("from fills = %+v, %v", result, err)
	}
	result, err = client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "19"})
	if err != nil || result.Status != exchange.ExactOrderUnconfirmed {
		t.Fatalf("missing = %+v, %v", result, err)
	}
}

func scriptKrakenWS(stub *krakenStub, script func(read func() map[string]any, write func(string))) {
	stub.mu.Lock()
	stub.ws = func(conn *websocket.Conn) {
		read := func() map[string]any {
			for {
				var frame map[string]any
				if conn.ReadJSON(&frame) != nil {
					return nil
				}
				if frame["event"] != "ping" {
					return frame
				}
			}
		}
		script(read, func(frame string) { _ = conn.WriteMessage(websocket.TextMessage, []byte(frame)) })
	}
	stub.mu.Unlock()
}

func TestKrakenOrderBookStreamResyncsOnGap(t *testing.T) {
	stub := newKrakenStub(t)
	client, _ := newTestKrakenClient(t, stub)
	btc := krakenBTC(t, client)
	old := supervisedStreamDelays
	supervisedStreamDelays = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { supervisedStreamDelays = old })
	var connections int
	var mu sync.Mutex
	scriptKrakenWS(stub, func(read func() map[string]any, write func(string)) {
		mu.Lock()
		connections++
		n := connections
		mu.Unlock()
		sub := read()
		write(`{"event":"subscribed","feed":"book","product_ids":["PF_XBTUSD"]}`)
		_ = sub
		if n == 1 {
			write(`{"feed":"book_snapshot","product_id":"PF_XBTUSD","timestamp":1,"seq":10,"bids":[{"price":100,"qty":1}],"asks":[{"price":101,"qty":1}]}`)
			write(`{"feed":"book","product_id":"PF_XBTUSD","side":"buy","seq":11,"price":100,"qty":0,"timestamp":2}`)
			write(`{"feed":"book","product_id":"PF_XBTUSD","side":"buy","seq":13,"price":99,"qty":2,"timestamp":3}`) // gap
		} else {
			write(`{"feed":"book_snapshot","product_id":"PF_XBTUSD","timestamp":4,"seq":20,"bids":[{"price":98,"qty":3}],"asks":[{"price":101,"qty":1}]}`)
		}
		time.Sleep(2 * time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription, err := client.WebSocket().WatchOrderBook(ctx, exchange.WatchRequest{Instrument: btc})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var kinds []exchange.EventKind
	deadline := time.After(5 * time.Second)
	for len(kinds) < 3 {
		select {
		case event := <-subscription.Events():
			kinds = append(kinds, event.Kind)
			if len(kinds) == 3 && (event.Sequence != "20" || !event.Bids[0].Price.Equal(decimal.NewFromInt(98))) {
				t.Fatalf("resynced snapshot = %+v", event)
			}
		case err := <-subscription.Errors():
			t.Fatalf("stream error: %v", err)
		case <-deadline:
			t.Fatalf("events = %v", kinds)
		}
	}
	if kinds[0] != exchange.EventSnapshot || kinds[1] != exchange.EventDelta || kinds[2] != exchange.EventSnapshot {
		t.Fatalf("kinds = %v (the gapped delta must not be delivered)", kinds)
	}
}

func TestKrakenAccountEventsTrackOrdersAndPositions(t *testing.T) {
	stub := newKrakenStub(t)
	client, _ := newTestKrakenClient(t, stub)
	btc := krakenBTC(t, client)
	if err := client.WebSocket().(exchange.AccountInstrumentIndexer).SetAccountInstruments([]exchange.AccountInstrument{{InstrumentID: "kraken:perp:BTC-USD", Instrument: btc}}); err != nil {
		t.Fatal(err)
	}
	scriptKrakenWS(stub, func(read func() map[string]any, write func(string)) {
		if read()["event"] != "challenge" {
			return
		}
		write(`{"event":"challenge","message":"c1"}`)
		for i := 0; i < 4; i++ {
			sub := read()
			if sub["signed_challenge"] == nil {
				return
			}
			write(`{"event":"subscribed","feed":"` + sub["feed"].(string) + `"}`)
		}
		write(`{"feed":"open_orders_snapshot","orders":[{"instrument":"PF_XBTUSD","time":1,"last_update_time":1,"qty":1,"filled":0,"limit_price":60000,"type":"limit","order_id":"o1","cli_ord_id":"17","direction":0,"reduce_only":false}]}`)
		write(`{"feed":"open_orders","order_id":"o1","is_cancel":true,"reason":"not_enough_margin"}`)
		write(`{"feed":"fills","fills":[{"instrument":"PF_XBTUSD","time":2,"price":60000,"seq":1,"buy":true,"qty":0.5,"order_id":"o2","fill_id":"f1","fill_type":"maker","fee_paid":-0.3,"fee_currency":"USD"}]}`)
		write(`{"feed":"open_positions","positions":[{"instrument":"PF_XBTUSD","balance":0.5,"pnl":1.5,"entry_price":60000,"mark_price":60003,"effective_leverage":2.0}]}`)
		write(`{"feed":"open_positions","positions":[]}`)
		time.Sleep(2 * time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := exchange.AccountRoute{AccountID: "a", RouteID: "kraken:perp:USD", Venue: exchange.VenueKraken, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USD", NativeFamily: exchange.NativeFamilyPerpetual}
	subscription, err := client.WebSocket().(exchange.AccountWebSocket).WatchAccountEvents(ctx, exchange.WatchAccountEventsRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var events []exchange.AccountEvent
	deadline := time.After(5 * time.Second)
	for len(events) < 5 {
		select {
		case event := <-subscription.Events():
			events = append(events, event)
		case err := <-subscription.Errors():
			t.Fatalf("stream error: %v", err)
		case <-deadline:
			t.Fatalf("events = %d", len(events))
		}
	}
	if events[0].Order.Order.ClientOrderID != "17" || events[0].Order.Order.Status != "open" {
		t.Fatalf("snapshot order = %+v", events[0].Order.Order)
	}
	rejected := events[1].Order.Order
	if rejected.OrderID != "o1" || rejected.Status != "rejected" || rejected.RejectCode != "not_enough_margin" || rejected.ClientOrderID != "17" {
		t.Fatalf("removed order = %+v", rejected)
	}
	fill := events[2].Fill.Fill
	if !fill.Fee.Equal(decimal.RequireFromString("-0.3")) || fill.Liquidity != exchange.LiquidityMaker || events[2].InstrumentID != "kraken:perp:BTC-USD" {
		t.Fatalf("fill = %+v", fill)
	}
	if len(events[3].Position.Positions) != 1 || !events[3].Position.Positions[0].Quantity.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("position = %+v", events[3].Position)
	}
	if events[4].Position.Kind != exchange.EventSnapshot || len(events[4].Position.Positions) != 0 || events[4].InstrumentID != "kraken:perp:BTC-USD" {
		t.Fatalf("closed position = %+v", events[4])
	}
}

func TestKrakenRejectionText(t *testing.T) {
	for in, want := range map[string]string{"insufficientAvailableFunds": "Insufficient available funds", "postWouldExecute": "Post would execute", "": "kraken rejected the order"} {
		if got := krakenRejectionText(in); got != want {
			t.Fatalf("%q -> %q", in, got)
		}
	}
}
