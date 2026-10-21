package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

// crossExStub serves CrossEx REST and the private stream from fixtures.
type crossExStub struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	routes   map[string]crossExReply // "METHOD /path" -> reply
	requests []crossExRecorded
	pushes   chan string
	logins   int
}

type crossExReply struct {
	status int
	body   string
}

type crossExRecorded struct {
	method, path, query, body string
}

const crossExSymbolsFixture = `[
 {"symbol":"BINANCE_FUTURE_BTC_USDT","exchange_type":"BINANCE","business_type":"FUTURE","state":"live","min_size":"0.001","min_notional":"50","lot_size":"0.001","tick_size":"0.10","max_num_orders":"200","max_market_size":"120","max_limit_size":"1000","contract_size":null,"liquidation_fee":"0.0125","default_leverage":"5","delist_time":"0","support_rpi":"true","support_cross":"true","tag":""},
 {"symbol":"OKX_FUTURE_ADA_USDT","exchange_type":"OKX","business_type":"FUTURE","state":"live","min_size":"0.1","min_notional":null,"lot_size":"0.1","tick_size":"0.0001","max_num_orders":"200","max_market_size":null,"max_limit_size":null,"contract_size":null,"liquidation_fee":null,"default_leverage":"5","delist_time":"0","support_rpi":"false","support_cross":"true","tag":""},
 {"symbol":"KRAKEN_FUTURE_BTC_USD","exchange_type":"KRAKEN","business_type":"FUTURE","state":"live","min_size":"0.0001","min_notional":null,"lot_size":"0.0001","tick_size":"0.5","max_num_orders":"200","max_market_size":null,"max_limit_size":null,"contract_size":null,"liquidation_fee":null,"default_leverage":"5","delist_time":"0","support_rpi":"false","support_cross":"true","tag":""}
]`

func newCrossExStub(t *testing.T) *crossExStub {
	t.Helper()
	stub := &crossExStub{t: t, routes: map[string]crossExReply{}, pushes: make(chan string, 16)}
	stub.reply("GET /api/v4/crossex/rule/symbols", 200, crossExSymbolsFixture)
	upgrader := websocket.Upgrader{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/crossex" {
			stub.serveStream(upgrader, w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.requests = append(stub.requests, crossExRecorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body)})
		reply, ok := stub.routes[r.Method+" "+r.URL.Path]
		stub.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"label":"NOT_FOUND","message":"no route"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *crossExStub) reply(route string, status int, body string) {
	stub.mu.Lock()
	stub.routes[route] = crossExReply{status, body}
	stub.mu.Unlock()
}

func (stub *crossExStub) last(method, path string) (crossExRecorded, bool) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for i := len(stub.requests) - 1; i >= 0; i-- {
		if stub.requests[i].method == method && stub.requests[i].path == path {
			return stub.requests[i], true
		}
	}
	return crossExRecorded{}, false
}

func (stub *crossExStub) serveStream(upgrader websocket.Upgrader, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var login map[string]any
	if conn.ReadJSON(&login) != nil {
		return
	}
	stub.mu.Lock()
	stub.logins++
	stub.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"login","result":{"code":"100000","message":"success"},"time":1}`))
	for range crossExAccountChannels {
		var sub map[string]any
		if conn.ReadJSON(&sub) != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"`+sub["channel"].(string)+`","event":"subscribe","payload":["!all"],"result":{"code":"100000","message":"success"},"time":1}`))
	}
	for {
		select {
		case frame, ok := <-stub.pushes:
			if !ok {
				return
			}
			if frame == "drop" {
				return
			}
			if conn.WriteMessage(websocket.TextMessage, []byte(frame)) != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func crossExNatives() []exchange.Instrument {
	return []exchange.Instrument{
		nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.10", "0.001"),
		nativePerp(exchange.VenueOKX, "ADA-USDT-SWAP", "ADA", "USDT", "ADA-USDT-SWAP", "0.0001", "10"),
		nativePerp(exchange.VenueKraken, "BTC-USD", "BTC", "USD", "PF_XBTUSD", "1", "0.0001"),
	}
}

func newTestCrossExClient(t *testing.T, stub *crossExStub) *crossExPerpClient {
	t.Helper()
	natives := func(context.Context) ([]exchange.Instrument, error) { return crossExNatives(), nil }
	client := NewCrossExPerp("key", "secret", natives, Settings{
		Endpoint:          stub.server.URL + "/api/v4",
		WebSocketEndpoint: "ws" + strings.TrimPrefix(stub.server.URL, "http") + "/ws/crossex",
		HTTPClient:        stub.server.Client(),
	}).(*crossExPerpClient)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func binanceBTC(t *testing.T, client *crossExPerpClient) exchange.Instrument {
	t.Helper()
	instrument, err := client.ResolveInstrument(context.Background(), "binance:BTC-USDT")
	if err != nil {
		t.Fatal(err)
	}
	return instrument
}

func TestCrossExClientIsBrokeredAndKeepsNativeIdentity(t *testing.T) {
	client := newTestCrossExClient(t, newCrossExStub(t))
	var brokered exchange.BrokeredClient = client
	// Ten perp markets and five spot markets.
	if brokered.Broker() != exchange.VenueCrossEx || len(brokered.Reaches()) != 15 {
		t.Fatalf("broker = %s reaches = %v", brokered.Broker(), brokered.Reaches())
	}
	instruments, err := client.RefreshInstruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Every CrossEx row maps onto a native instrument, Kraken's XBT included.
	if len(instruments) != 3 {
		t.Fatalf("instruments = %v", instruments)
	}
	kraken, err := client.ResolveInstrument(context.Background(), "kraken:BTC-USD")
	if err != nil || kraken.Identity.NativeSymbol != "KRAKEN_FUTURE_BTC_USD" || kraken.Identity.SettleAsset != "USD" || !kraken.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("kraken = %+v %+v, %v", kraken.Identity, kraken.Rules, err)
	}
	btc := binanceBTC(t, client)
	if btc.Identity.Venue != exchange.VenueBinance || btc.Identity.Symbol != "BTC-USDT" || btc.Identity.NativeSymbol != "BINANCE_FUTURE_BTC_USDT" {
		t.Fatalf("identity = %+v", btc.Identity)
	}
	ada, err := client.ResolveInstrument(context.Background(), "OKX_FUTURE_ADA_USDT")
	if err != nil {
		t.Fatal(err)
	}
	// CrossEx trades ADA in 0.1 base units; the native venue in 10-ADA contracts.
	if !ada.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("ada rules = %+v", ada.Rules)
	}
}

func TestCrossExMarketDataIsUnsupported(t *testing.T) {
	client := newTestCrossExClient(t, newCrossExStub(t))
	btc := binanceBTC(t, client)
	ctx := context.Background()
	_, bookErr := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: btc})
	_, candleErr := client.Candles(ctx, exchange.CandlesRequest{Instrument: btc})
	_, fundingErr := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: btc})
	_, watchErr := client.WebSocket().WatchOrderBook(ctx, exchange.WatchRequest{Instrument: btc})
	for _, err := range []error{bookErr, candleErr, fundingErr, watchErr} {
		if !errors.Is(err, exchange.ErrUnsupported) {
			t.Fatalf("market data err = %v", err)
		}
	}
}

func TestCrossExPlaceOrderIsAcceptedPendingWithCrossExRules(t *testing.T) {
	stub := newCrossExStub(t)
	stub.reply("POST /api/v4/crossex/orders", 200, `{"order_id":"2072652940337152","text":"1700000000123"}`)
	client := newTestCrossExClient(t, stub)
	btc := binanceBTC(t, client)

	ack, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument: btc, ClientOrderID: "1700000000123", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.001"), LimitPrice: decimal.RequireFromString("60000"), LimitPolicy: exchange.LimitPolicyResting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("ack invalid: %v", err)
	}
	if ack.State != exchange.AckAcceptedPending || ack.OrderID != "2072652940337152" || ack.Venue != exchange.VenueBinance ||
		ack.NativeSymbol != "BINANCE_FUTURE_BTC_USDT" {
		t.Fatalf("ack = %+v", ack)
	}
	sent, _ := stub.last("POST", "/api/v4/crossex/orders")
	var body map[string]string
	_ = json.Unmarshal([]byte(sent.body), &body)
	if body["symbol"] != "BINANCE_FUTURE_BTC_USDT" || body["text"] != "1700000000123" || body["qty"] != "0.001" ||
		body["time_in_force"] != "GTC" || body["position_side"] != "" {
		t.Fatalf("order body = %s", sent.body)
	}

	// The native instrument resolves to CrossEx rules: 60000 x 0.001 = 60 >= 50.
	// Below CrossEx's 50 USDT minimum notional it is refused locally.
	_, err = client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument: crossExNatives()[0], ClientOrderID: "1700000000124", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.001"), LimitPrice: decimal.RequireFromString("40000"), LimitPolicy: exchange.LimitPolicyResting,
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("below min notional err = %v", err)
	}
}

func TestCrossExPlaceOrderOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		state  exchange.OrderAckState
		kind   error
		code   string
	}{
		{"risk refusal", 400, `{"label":"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR","message":"Insufficient availableMargin"}`, exchange.AckRejected, exchange.ErrVenueRejected, "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR"},
		{"duplicate text", 400, `{"label":"TRADE_ORDER_DUPLICATE_ERROR","message":"Text duplicate"}`, exchange.AckAmbiguous, exchange.ErrAmbiguousOutcome, ""},
		{"server failure", 502, `bad gateway`, exchange.AckAmbiguous, exchange.ErrAmbiguousOutcome, ""},
		{"rate limited", 429, `{"label":"TOO_MANY_REQUESTS","message":"slow"}`, exchange.AckAmbiguous, exchange.ErrAmbiguousOutcome, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newCrossExStub(t)
			stub.reply("POST /api/v4/crossex/orders", tc.status, tc.body)
			client := newTestCrossExClient(t, stub)
			ack, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
				Instrument: binanceBTC(t, client), ClientOrderID: "17", Side: exchange.SideSell, Type: exchange.OrderTypeMarket,
				Quantity: decimal.RequireFromString("0.001"),
			})
			if !errors.Is(err, tc.kind) || ack.State != tc.state {
				t.Fatalf("ack = %+v err = %v", ack, err)
			}
			if tc.code != "" && ack.VenueCode != tc.code {
				t.Fatalf("venue code = %q", ack.VenueCode)
			}
			if ack.ClientOrderID != "17" {
				t.Fatal("ambiguous ack lost its correlation id")
			}
		})
	}
}

func TestCrossExAccountRequiresCrossExchangeSingleMode(t *testing.T) {
	account := `{"user_id":"1","available_margin":"1200","margin_balance":"1500","initial_margin":"300","maintenance_margin":"100",
		"initial_margin_rate":"5","maintenance_margin_rate":"15","position_mode":"SINGLE","account_mode":"CROSS_EXCHANGE","exchange_type":"CROSSEX",
		"assets":[
		 {"coin":"USDT","exchange_type":"CROSSEX","balance":"1500","upnl":"-20","equity":"1480","available_balance":"1200"},
		 {"coin":"USDT","exchange_type":"BINANCE","balance":"900","upnl":"-25","equity":"875","available_balance":"700"},
		 {"coin":"USDT","exchange_type":"OKX","balance":"600","upnl":"5","equity":"605","available_balance":"500"}]}`
	stub := newCrossExStub(t)
	stub.reply("GET /api/v4/crossex/accounts", 200, account)
	client := newTestCrossExClient(t, stub)
	perp, err := client.PerpAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(perp.Balances) != 1 || !perp.Balances[0].Total.Equal(decimal.NewFromInt(1500)) || !perp.Balances[0].Available.Equal(decimal.NewFromInt(1200)) ||
		!perp.Balances[0].Locked.Equal(decimal.NewFromInt(300)) {
		t.Fatalf("balances = %+v (pool rows only, venue rows must not double count)", perp.Balances)
	}
	if !perp.Available.Value.Equal(decimal.NewFromInt(1200)) || !perp.UnrealizedPnL.Value.Equal(decimal.NewFromInt(-20)) {
		t.Fatalf("perp account = %+v", perp)
	}

	for _, mode := range []string{
		strings.Replace(account, `"CROSS_EXCHANGE"`, `"ISOLATED_EXCHANGE"`, 1),
		strings.Replace(account, `"SINGLE"`, `"DUAL"`, 1),
	} {
		stub.reply("GET /api/v4/crossex/accounts", 200, mode)
		if _, err := client.Balances(context.Background()); !errors.Is(err, exchange.ErrUnsupported) {
			t.Fatalf("unsupported mode accepted: %v", err)
		}
	}

	stub.reply("GET /api/v4/crossex/accounts", 401, `{"label":"INVALID_KEY","message":"Invalid key provided"}`)
	if _, err := client.Balances(context.Background()); !errors.Is(err, exchange.ErrAuthentication) {
		t.Fatalf("bad key err = %v", err)
	}
}

func TestCrossExPositionsOrdersAndLeverage(t *testing.T) {
	stub := newCrossExStub(t)
	stub.reply("GET /api/v4/crossex/positions", 200, `[
		{"symbol":"OKX_FUTURE_ADA_USDT","position_side":"NONE","position_qty":"-10","entry_price":"0.5426","mark_price":"0.5795","upnl":"-0.369","liq_price":"0","leverage":"3","initial_margin":"1.93"},
		{"symbol":"BINANCE_FUTURE_BTC_USDT","position_side":"NONE","position_qty":"0"}]`)
	stub.reply("GET /api/v4/crossex/open_orders", 200, `[{"order_id":"9","client_order_id":"17","state":"NEW","symbol":"BINANCE_FUTURE_BTC_USDT","side":"BUY","type":"LIMIT","time_in_force":"GTC","qty":"0.001","price":"60000","executed_qty":"0","business_type":"FUTURE"}]`)
	stub.reply("POST /api/v4/crossex/positions/leverage", 202, `{"symbol":"BINANCE_FUTURE_BTC_USDT","leverage":"10"}`)
	client := newTestCrossExClient(t, stub)
	ctx := context.Background()

	positions, err := client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].NativeSymbol != "OKX_FUTURE_ADA_USDT" || !positions[0].Quantity.Equal(decimal.NewFromInt(-10)) {
		t.Fatalf("positions = %+v", positions)
	}

	btc := binanceBTC(t, client)
	page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &btc})
	if err != nil {
		t.Fatal(err)
	}
	if sent, _ := stub.last("GET", "/api/v4/crossex/open_orders"); sent.query != "symbol=BINANCE_FUTURE_BTC_USDT" {
		t.Fatalf("open orders query = %q", sent.query)
	}
	if len(page.Orders) != 1 || page.Orders[0].Status != "pending" || page.Orders[0].ClientOrderID != "17" {
		t.Fatalf("open orders = %+v", page.Orders)
	}

	leverage, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: btc, Leverage: 10})
	if err != nil || leverage.Effective != 10 {
		t.Fatalf("leverage = %+v, %v", leverage, err)
	}

	// An instrument CrossEx cannot reach is refused before any request.
	foreign := nativePerp(exchange.VenueBitget, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.1", "0.001")
	if _, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: foreign, Leverage: 5}); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("foreign instrument err = %v", err)
	}
}

func TestCrossExExactLookup(t *testing.T) {
	order := `{"order_id":"9","text":"17","state":"FILLED","symbol":"BINANCE_FUTURE_BTC_USDT","side":"BUY","type":"MARKET","qty":"0.001","executed_qty":"0.001","executed_avg_price":"60010","business_type":"FUTURE"}`
	notFound := `{"label":"TRADE_ORDER_NOT_FOUND_ERROR","message":"The order was not found"}`
	ctx := context.Background()

	t.Run("found by text", func(t *testing.T) {
		stub := newCrossExStub(t)
		stub.reply("GET /api/v4/crossex/orders/17", 200, order)
		client := newTestCrossExClient(t, stub)
		btc := binanceBTC(t, client)
		result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "17"})
		if err != nil || result.Status != exchange.ExactOrderFound || result.Order.Status != "filled" || result.Order.OrderID != "9" {
			t.Fatalf("result = %+v, %v", result, err)
		}
	})
	t.Run("text not found but present in history", func(t *testing.T) {
		stub := newCrossExStub(t)
		stub.reply("GET /api/v4/crossex/orders/17", 404, notFound)
		stub.reply("GET /api/v4/crossex/history_orders", 200, "["+order+"]")
		client := newTestCrossExClient(t, stub)
		btc := binanceBTC(t, client)
		result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "17"})
		if err != nil || result.Status != exchange.ExactOrderFound {
			t.Fatalf("result = %+v, %v", result, err)
		}
	})
	t.Run("text not found anywhere is unconfirmed", func(t *testing.T) {
		stub := newCrossExStub(t)
		stub.reply("GET /api/v4/crossex/orders/17", 404, notFound)
		stub.reply("GET /api/v4/crossex/history_orders", 200, "[]")
		client := newTestCrossExClient(t, stub)
		btc := binanceBTC(t, client)
		result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "17"})
		if err != nil || result.Status != exchange.ExactOrderUnconfirmed {
			t.Fatalf("result = %+v, %v (text lookup cannot prove absence)", result, err)
		}
	})
	t.Run("order id not found is authoritative", func(t *testing.T) {
		stub := newCrossExStub(t)
		stub.reply("GET /api/v4/crossex/orders/2072652940337152", 404, notFound)
		client := newTestCrossExClient(t, stub)
		btc := binanceBTC(t, client)
		result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, VenueOrderID: "2072652940337152"})
		if err != nil || result.Status != exchange.ExactOrderNotFound {
			t.Fatalf("result = %+v, %v", result, err)
		}
	})
}

func TestCrossExAccountEventsRouteEveryVenueThroughOneStream(t *testing.T) {
	stub := newCrossExStub(t)
	client := newTestCrossExClient(t, stub)
	instruments, err := client.RefreshInstruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	index := make([]exchange.AccountInstrument, 0, len(instruments))
	for _, instrument := range instruments {
		index = append(index, exchange.AccountInstrument{InstrumentID: string(instrument.Identity.Venue) + ":perp:" + instrument.Identity.Symbol, Instrument: instrument})
	}
	socket := client.WebSocket().(*crossExWebSocket)
	if err := socket.SetAccountInstruments(index); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := exchange.AccountRoute{AccountID: "acc", RouteID: "crossex:perp", Venue: exchange.VenueCrossEx, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, NativeFamily: "brokered"}
	subscription, err := socket.WatchAccountEvents(ctx, exchange.WatchAccountEventsRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	awaitActive(t, subscription.Status())

	stub.pushes <- `{"channel":"order","event":"subscribe","payload":{"order_id":"1","text":"17","state":"FAIL","symbol":"OKX_FUTURE_ADA_USDT","side":"BUY","type":"MARKET","qty":"10","executed_qty":"0","business_type":"FUTURE","reason":"{\"label\":\"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR\",\"message\":\"Insufficient availableMargin\"}","update_time":"1756434169098"},"time":1}`
	stub.pushes <- `{"channel":"order","event":"subscribe","payload":{"order_id":"2","state":"OPEN","symbol":"KRAKEN_FUTURE_BTC_USD","side":"BUY","type":"LIMIT","qty":"1","business_type":"FUTURE"},"time":1}`
	// An exchange the gateway does not serve is skipped, not reported as an error.
	stub.pushes <- `{"channel":"order","event":"subscribe","payload":{"order_id":"3","state":"OPEN","symbol":"OKX_FUTURE_BTC_USDC","side":"BUY","type":"LIMIT","qty":"1","business_type":"FUTURE"},"time":1}`
	stub.pushes <- `{"channel":"usertrades","event":"subscribe","payload":{"transaction_id":"5","order_id":"3","text":"18","symbol":"BINANCE_FUTURE_BTC_USDT","business_type":"FUTURE","side":"SELL","qty":"0.001","price":"60000","fee":"0.012","fee_coin":"USDT","match_role":"TAKER","create_time":"1756465636528"},"time":1}`
	stub.pushes <- `{"channel":"position","event":"subscribe","payload":{"symbol":"BINANCE_FUTURE_BTC_USDT","position_side":"NONE","position_qty":"0","update_time":"1756434169098"},"time":1}`
	stub.pushes <- `{"channel":"asset","event":"subscribe","payload":{"coin":"USDT","exchange_type":"BINANCE","balance":"900","available_balance":"700"},"time":1}`
	stub.pushes <- `{"channel":"asset","event":"subscribe","payload":{"coin":"USDT","exchange_type":"CROSSEX","balance":"1500","available_balance":"1200"},"time":1,"time_ms":1756465636463}`

	var events []exchange.AccountEvent
	timeout := time.After(3 * time.Second)
	for len(events) < 5 {
		select {
		case event := <-subscription.Events():
			if err := event.Validate(); err != nil {
				t.Fatalf("invalid event %+v: %v", event, err)
			}
			events = append(events, event)
		case <-timeout:
			t.Fatalf("got %d events: %+v", len(events), events)
		}
	}
	failed := events[0].Order
	if events[0].Kind != exchange.AccountEventOrder || events[0].InstrumentID != "okx:perp:ADA-USDT-SWAP" || failed.Order.Status != "rejected" ||
		failed.Order.RejectCode != "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR" || failed.Order.RejectMessage != "Insufficient availableMargin" {
		t.Fatalf("order event = %+v", events[0])
	}
	if events[1].Kind != exchange.AccountEventOrder || events[1].InstrumentID != "kraken:perp:BTC-USD" {
		t.Fatalf("kraken order event = %+v", events[1])
	}
	if events[2].Kind != exchange.AccountEventFill || events[2].InstrumentID != "binance:perp:BTC-USDT" || events[2].Fill.Fill.Liquidity != exchange.LiquidityTaker {
		t.Fatalf("fill event = %+v", events[2])
	}
	if events[3].Kind != exchange.AccountEventPosition || events[3].Position.Kind != exchange.EventSnapshot || len(events[3].Position.Positions) != 0 {
		t.Fatalf("closed position event = %+v", events[3])
	}
	// The per-venue asset row is a breakdown; only the pool row is a balance.
	if events[4].Kind != exchange.AccountEventBalance || len(events[4].Balance.Balances) != 1 || !events[4].Balance.Balances[0].Total.Equal(decimal.NewFromInt(1500)) {
		t.Fatalf("balance event = %+v", events[4])
	}
	select {
	case err := <-subscription.Errors():
		t.Fatalf("unexpected stream error: %v", err)
	default:
	}
}

func TestCrossExAccountStreamReconnects(t *testing.T) {
	previous := crossExReconnectDelays
	crossExReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { crossExReconnectDelays = previous }()

	stub := newCrossExStub(t)
	client := newTestCrossExClient(t, stub)
	instruments, _ := client.RefreshInstruments(context.Background())
	socket := client.WebSocket().(*crossExWebSocket)
	_ = socket.SetAccountInstruments([]exchange.AccountInstrument{{InstrumentID: "binance:perp:BTC-USDT", Instrument: instruments[0]}})
	route := exchange.AccountRoute{AccountID: "acc", RouteID: "crossex:perp", Venue: exchange.VenueCrossEx, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, NativeFamily: "brokered"}
	subscription, err := socket.WatchAccountEvents(context.Background(), exchange.WatchAccountEventsRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	awaitActive(t, subscription.Status())
	stub.pushes <- "drop"
	awaitState(t, subscription.Status(), exchange.SubscriptionConnecting)
	awaitActive(t, subscription.Status())
	stub.mu.Lock()
	logins := stub.logins
	stub.mu.Unlock()
	if logins != 2 {
		t.Fatalf("logins = %d, want a fresh login after the drop", logins)
	}
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := socket.WatchAccountEvents(context.Background(), exchange.WatchAccountEventsRequest{Route: route}); err == nil {
		t.Fatal("watch accepted after close")
	}
}

func TestCrossExAccountIndexAcceptsEveryReachableVenue(t *testing.T) {
	socket := newTestCrossExClient(t, newCrossExStub(t)).WebSocket().(*crossExWebSocket)
	binance := nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BINANCE_FUTURE_BTC_USDT", "0.1", "0.001")
	okx := nativePerp(exchange.VenueOKX, "ADA-USDT-SWAP", "ADA", "USDT", "OKX_FUTURE_ADA_USDT", "0.0001", "0.1")
	if err := socket.SetAccountInstruments([]exchange.AccountInstrument{{InstrumentID: "a", Instrument: binance}, {InstrumentID: "b", Instrument: okx}}); err != nil {
		t.Fatal(err)
	}
	bitget := nativePerp(exchange.VenueBitget, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.1", "0.001")
	if err := socket.SetAccountInstruments([]exchange.AccountInstrument{{InstrumentID: "c", Instrument: bitget}}); err == nil {
		t.Fatal("unreachable venue accepted into the index")
	}
}

func awaitActive(t *testing.T, statuses <-chan exchange.StreamStatusEvent) {
	t.Helper()
	awaitState(t, statuses, exchange.SubscriptionActive)
}

func awaitState(t *testing.T, statuses <-chan exchange.StreamStatusEvent, want exchange.SubscriptionState) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case status := <-statuses:
			if status.State == want {
				return
			}
		case <-timeout:
			t.Fatalf("stream never reached %s", want)
		}
	}
}
