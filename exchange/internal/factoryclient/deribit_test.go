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
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

// deribitStub serves JSON-RPC over HTTP from the SDK's captured fixtures and
// per-test overrides, plus a scripted WebSocket.
type deribitStub struct {
	server *httptest.Server
	mu     sync.Mutex
	routes map[string]string
	seen   []*http.Request
	ws     func(read func() map[string]any, write func(string))
}

func newDeribitStub(t *testing.T) *deribitStub {
	t.Helper()
	stub := &deribitStub{routes: map[string]string{
		"public/get_instruments":               "@instruments.json",
		"public/ticker":                        "@ticker.json",
		"public/get_order_book":                "@book.json",
		"public/get_last_trades_by_instrument": "@trades.json",
		"public/get_tradingview_chart_data":    "@candles.json",
		"public/get_funding_rate_history":      "@funding.json",
		"public/auth":                          `{"jsonrpc":"2.0","result":{"access_token":"tok","expires_in":900}}`,
	}}
	upgrader := websocket.Upgrader{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/api/v2" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			stub.mu.Lock()
			script := stub.ws
			stub.mu.Unlock()
			if script != nil {
				script(func() map[string]any {
					var frame map[string]any
					if conn.ReadJSON(&frame) != nil {
						return nil
					}
					return frame
				}, func(frame string) { _ = conn.WriteMessage(websocket.TextMessage, []byte(frame)) })
			}
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/api/v2/")
		stub.mu.Lock()
		stub.seen = append(stub.seen, r)
		payload, ok := stub.routes[method]
		stub.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasPrefix(payload, "@") {
			data, err := os.ReadFile("../../../sdk/deribit/testdata/" + payload[1:])
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

func (stub *deribitStub) set(method, body string) {
	stub.mu.Lock()
	stub.routes[method] = body
	stub.mu.Unlock()
}

func (stub *deribitStub) last(method string) *http.Request {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for index := len(stub.seen) - 1; index >= 0; index-- {
		if strings.HasSuffix(stub.seen[index].URL.Path, method) {
			return stub.seen[index]
		}
	}
	return nil
}

func newTestDeribitClient(t *testing.T, stub *deribitStub) *deribitPerpClient {
	t.Helper()
	catalog := exchange.NewDefaultMemoryCatalog()
	settings := Settings{Endpoint: stub.server.URL, WebSocketEndpoint: "ws" + strings.TrimPrefix(stub.server.URL, "http") + "/ws/api/v2",
		HTTPClient: stub.server.Client(), InstrumentCatalog: catalog}
	settings.SourceDescriptor = exchange.SourceDescriptor{Name: "deribit-usdc-perp", Endpoint: settings.Endpoint, WebSocketEndpoint: settings.WebSocketEndpoint, Transport: "test"}
	settings.CatalogScope = exchange.CatalogScope{Venue: exchange.VenueDeribit, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDC",
		EndpointFingerprint: settings.SourceDescriptor.Fingerprint()}
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, NewDeribitInstrumentLoader(settings)); err != nil {
		t.Fatal(err)
	}
	client := NewDeribitPerp("id", "secret", settings).(*deribitPerpClient)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func deribitInstrument(t *testing.T, client *deribitPerpClient, symbol string) exchange.Instrument {
	t.Helper()
	instrument, err := client.ResolveInstrument(context.Background(), symbol)
	if err != nil {
		t.Fatal(err)
	}
	return instrument
}

func TestDeribitCatalogKeepsUSDCLinearPerps(t *testing.T) {
	client := newTestDeribitClient(t, newDeribitStub(t))
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 3 {
		t.Fatalf("instruments = %d (dated futures must be skipped)", len(instruments))
	}
	aapl := deribitInstrument(t, client, "AAPL-USDC")
	if aapl.Identity.NativeSymbol != "AAPL_USDC-PERPETUAL" || !aapl.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) ||
		aapl.Metadata.AssetClass != exchange.AssetClassEquity || aapl.Identity.SettleAsset != "USDC" {
		t.Fatalf("AAPL = %+v", aapl)
	}
	btc := deribitInstrument(t, client, "BTC-USDC")
	if !btc.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.0001")) || !btc.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("BTC rules = %+v", btc.Rules)
	}
}

func TestDeribitMarketData(t *testing.T) {
	stub := newDeribitStub(t)
	client := newTestDeribitClient(t, stub)
	btc := deribitInstrument(t, client, "BTC-USDC")
	ctx := context.Background()
	book, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: btc, Limit: 20})
	if err != nil || len(book.Bids) != 3 || book.Sequence == "" {
		t.Fatalf("book = %+v, %v", book, err)
	}
	page, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: btc, Interval: "1m", Start: time.UnixMilli(1792333980000), End: time.UnixMilli(1792334100000)})
	if err != nil || len(page.Candles) != 3 {
		t.Fatalf("candles = %+v, %v", page, err)
	}
	if query := stub.last("get_tradingview_chart_data").URL.Query(); query.Get("resolution") != "1" {
		t.Fatalf("chart query = %v", query)
	}
	if _, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: btc, Interval: "4h"}); !errors.Is(err, exchange.ErrUnsupported) {
		t.Fatalf("4h err = %v", err)
	}
	rate, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: btc})
	if err != nil || !rate.Rate.Equal(decimal.RequireFromString("0.0000085")) {
		t.Fatalf("funding = %+v, %v (funding_8h 6.8e-05 / 8)", rate, err)
	}
	history, err := client.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{Instrument: btc})
	if err != nil || len(history.Rates) != 3 || !history.Rates[0].FundingTime.After(history.Rates[1].FundingTime) {
		t.Fatalf("history = %+v, %v", history, err)
	}
}

func TestDeribitPlaceCancelAndAccount(t *testing.T) {
	stub := newDeribitStub(t)
	client := newTestDeribitClient(t, stub)
	btc := deribitInstrument(t, client, "BTC-USDC")
	ctx := context.Background()
	request := exchange.PlaceOrderRequest{Instrument: btc, ClientOrderID: "17", Side: exchange.SideSell, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.001"), LimitPrice: decimal.NewFromInt(90000), LimitPolicy: exchange.LimitPolicyPostOnly}
	stub.set("private/sell", `{"jsonrpc":"2.0","result":{"order":{"order_id":"USDC-1","label":"17","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","order_type":"limit","order_state":"open","price":90000,"amount":0.001,"filled_amount":0,"post_only":true},"trades":[]}}`)
	ack, err := client.PlaceOrder(ctx, request)
	if err != nil || ack.State != exchange.AckResting || ack.OrderID != "USDC-1" {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	sent := stub.last("private/sell").URL.Query()
	if sent.Get("label") != "17" || sent.Get("post_only") != "true" || sent.Get("amount") != "0.001" || stub.last("private/sell").Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("sent = %v", sent)
	}
	stub.set("private/sell", `{"jsonrpc":"2.0","error":{"message":"not_enough_funds","code":10009}}`)
	ack, err = client.PlaceOrder(ctx, request)
	if !errors.Is(err, exchange.ErrVenueRejected) || ack.State != exchange.AckRejected || ack.VenueCode != "10009" || ack.VenueMessage != "Not enough funds" {
		t.Fatalf("rejected = %+v, %v", ack, err)
	}
	stub.set("private/sell", `{"jsonrpc":"2.0","error":{"message":"too_many_requests","code":10028}}`)
	ack, _ = client.PlaceOrder(ctx, request)
	if ack.State == exchange.AckRejected {
		t.Fatal("a rate limit was taken as proof of rejection")
	}
	stub.set("private/cancel", `{"jsonrpc":"2.0","result":{"order_id":"USDC-1","order_state":"cancelled","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","amount":0.001,"filled_amount":0}}`)
	if ack, err := client.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: btc, OrderID: "USDC-1"}); err != nil || ack.State != exchange.AckCanceled {
		t.Fatalf("cancel = %+v, %v", ack, err)
	}
	stub.set("private/get_account_summary", `{"jsonrpc":"2.0","result":{"currency":"USDC","balance":1000,"equity":1010,"available_funds":900,"initial_margin":100,"session_upl":10}}`)
	account, err := client.PerpAccount(ctx)
	if err != nil || !account.Equity.Value.Equal(decimal.NewFromInt(1010)) || !account.Balances[0].Locked.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("account = %+v, %v", account, err)
	}
	stub.set("private/get_positions", `{"jsonrpc":"2.0","result":[{"instrument_name":"BTC_USDC-PERPETUAL","kind":"future","direction":"sell","size":-0.002,"average_price":90000,"mark_price":89000,"floating_profit_loss":2,"estimated_liquidation_price":null},{"instrument_name":"BTC-PERPETUAL","direction":"buy","size":10}]}`)
	positions, err := client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil || len(positions) != 1 || !positions[0].Quantity.Equal(decimal.RequireFromString("-0.002")) || positions[0].LiquidationPrice.Valid {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	if _, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: btc, Leverage: 5}); !errors.Is(err, exchange.ErrUnsupported) {
		t.Fatalf("leverage err = %v", err)
	}
	stub.set("private/get_order_state_by_label", `{"jsonrpc":"2.0","result":[{"order_id":"USDC-1","label":"17","instrument_name":"BTC_USDC-PERPETUAL","direction":"sell","order_type":"limit","order_state":"filled","price":90000,"amount":0.001,"filled_amount":0.001,"average_price":90000}]}`)
	result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "17"})
	if err != nil || result.Status != exchange.ExactOrderFound || result.Order.Status != "filled" {
		t.Fatalf("lookup = %+v, %v", result, err)
	}
	stub.set("private/get_order_state", `{"jsonrpc":"2.0","error":{"message":"order_not_found","code":10004}}`)
	result, err = client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, VenueOrderID: "USDC-9"})
	if err != nil || result.Status != exchange.ExactOrderNotFound {
		t.Fatalf("lookup by id = %+v, %v", result, err)
	}
}

func TestDeribitBookStreamFollowsChangeIDs(t *testing.T) {
	stub := newDeribitStub(t)
	client := newTestDeribitClient(t, stub)
	btc := deribitInstrument(t, client, "BTC-USDC")
	old := supervisedStreamDelays
	supervisedStreamDelays = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { supervisedStreamDelays = old })
	var mu sync.Mutex
	connections := 0
	stub.mu.Lock()
	stub.ws = func(read func() map[string]any, write func(string)) {
		mu.Lock()
		connections++
		n := connections
		mu.Unlock()
		read() // heartbeat
		write(`{"jsonrpc":"2.0","id":2,"result":"ok"}`)
		read()
		write(`{"jsonrpc":"2.0","id":3,"result":["book.BTC_USDC-PERPETUAL.100ms"]}`)
		note := func(data string) {
			write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"book.BTC_USDC-PERPETUAL.100ms","data":` + data + `}}`)
		}
		if n == 1 {
			note(`{"type":"snapshot","timestamp":1,"instrument_name":"BTC_USDC-PERPETUAL","change_id":5,"bids":[["new",100,1]],"asks":[["new",101,1]]}`)
			note(`{"type":"change","timestamp":2,"instrument_name":"BTC_USDC-PERPETUAL","prev_change_id":5,"change_id":7,"bids":[["delete",100,0]],"asks":[]}`)
			note(`{"type":"change","timestamp":3,"instrument_name":"BTC_USDC-PERPETUAL","prev_change_id":8,"change_id":9,"bids":[["new",99,1]],"asks":[]}`)
		} else {
			note(`{"type":"snapshot","timestamp":4,"instrument_name":"BTC_USDC-PERPETUAL","change_id":20,"bids":[["new",98,2]],"asks":[["new",101,1]]}`)
		}
		time.Sleep(2 * time.Second)
	}
	stub.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription, err := client.WebSocket().WatchOrderBook(ctx, exchange.WatchRequest{Instrument: btc})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var events []exchange.BookEvent
	deadline := time.After(5 * time.Second)
	for len(events) < 3 {
		select {
		case event := <-subscription.Events():
			events = append(events, event)
		case err := <-subscription.Errors():
			t.Fatalf("stream error: %v", err)
		case <-deadline:
			t.Fatalf("events = %d", len(events))
		}
	}
	if events[1].Kind != exchange.EventDelta || !events[1].Bids[0].Quantity.IsZero() || events[2].Kind != exchange.EventSnapshot || events[2].Sequence != "20" {
		t.Fatalf("events = %+v", events)
	}
}

func TestDeribitAccountEventsRouteUSDCPerpsOnly(t *testing.T) {
	stub := newDeribitStub(t)
	client := newTestDeribitClient(t, stub)
	btc := deribitInstrument(t, client, "BTC-USDC")
	if err := client.WebSocket().(exchange.AccountInstrumentIndexer).SetAccountInstruments([]exchange.AccountInstrument{{InstrumentID: "deribit:perp:BTC-USDC", Instrument: btc}}); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.ws = func(read func() map[string]any, write func(string)) {
		if read()["method"] != "public/auth" {
			return
		}
		write(`{"jsonrpc":"2.0","id":1,"result":{"access_token":"tok","expires_in":900}}`)
		read()
		write(`{"jsonrpc":"2.0","id":2,"result":"ok"}`)
		read()
		write(`{"jsonrpc":"2.0","id":3,"result":["user.changes.future.USDC.raw","user.portfolio.USDC"]}`)
		write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"user.changes.future.USDC.raw","data":{"instrument_name":"BTC_USDC-PERPETUAL",
			"orders":[{"order_id":"USDC-1","label":"17","instrument_name":"BTC_USDC-PERPETUAL","direction":"buy","order_type":"limit","order_state":"filled","price":90000,"amount":0.001,"filled_amount":0.001,"average_price":90000}],
			"trades":[{"trade_id":"t1","instrument_name":"BTC_USDC-PERPETUAL","order_id":"USDC-1","label":"17","direction":"buy","price":90000,"amount":0.001,"fee":0.02,"fee_currency":"USDC","liquidity":"M","timestamp":5}],
			"positions":[{"instrument_name":"BTC_USDC-PERPETUAL","direction":"buy","size":0.001,"average_price":90000,"mark_price":90001}]}}}`)
		write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"user.changes.future.USDC.raw","data":{"instrument_name":"ETH_USDC-30OCT26","orders":[{"order_id":"x","instrument_name":"ETH_USDC-30OCT26","order_state":"open"}],"trades":[],"positions":[]}}}`)
		write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"user.portfolio.USDC","data":{"currency":"USDC","balance":1000,"available_funds":900,"equity":1000}}}`)
		time.Sleep(2 * time.Second)
	}
	stub.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := exchange.AccountRoute{AccountID: "a", RouteID: "deribit:perp:USDC", Venue: exchange.VenueDeribit, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, Settlement: "USDC", NativeFamily: exchange.NativeFamilyPerpetual}
	subscription, err := client.WebSocket().(exchange.AccountWebSocket).WatchAccountEvents(ctx, exchange.WatchAccountEventsRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var events []exchange.AccountEvent
	deadline := time.After(5 * time.Second)
	for len(events) < 4 {
		select {
		case event := <-subscription.Events():
			events = append(events, event)
		case err := <-subscription.Errors():
			t.Fatalf("stream error: %v", err)
		case <-deadline:
			t.Fatalf("events = %d", len(events))
		}
	}
	if events[0].Kind != exchange.AccountEventOrder || events[0].Order.Order.ClientOrderID != "17" || events[0].Order.Order.Status != "filled" {
		t.Fatalf("order = %+v", events[0])
	}
	if events[1].Kind != exchange.AccountEventFill || events[1].Fill.Fill.Liquidity != exchange.LiquidityMaker || !events[1].Fill.Fill.Fee.Equal(decimal.RequireFromString("0.02")) {
		t.Fatalf("fill = %+v", events[1])
	}
	if events[2].Kind != exchange.AccountEventPosition || events[3].Kind != exchange.AccountEventBalance {
		t.Fatalf("kinds = %s %s (the dated future must be dropped)", events[2].Kind, events[3].Kind)
	}
}
