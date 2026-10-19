package crossex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
)

type recorded struct {
	method, path, rawQuery, body string
	header                       http.Header
}

func testServer(t *testing.T, status int, response string) (*Client, *recorded) {
	t.Helper()
	got := &recorded{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*got = recorded{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, body: string(body), header: r.Header.Clone()}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	client := NewClient().WithBaseURL(server.URL+"/api/v4").WithCredentials("key", "secret").
		WithClock(func() time.Time { return time.Unix(1541993715, 0) })
	return client, got
}

func assertSigned(t *testing.T, got *recorded) {
	t.Helper()
	if got.header.Get("KEY") != "key" || got.header.Get("Timestamp") != "1541993715" {
		t.Fatalf("auth headers = %v", got.header)
	}
	want := sign("secret", signingPayload(got.method, got.path, got.rawQuery, got.body, "1541993715"))
	if got.header.Get("SIGN") != want {
		t.Fatalf("SIGN does not cover the request as sent")
	}
}

// Response bodies below are the examples of the CrossEx v1.0.6 reference.

func TestAccountDecodesDocumentedExample(t *testing.T) {
	client, got := testServer(t, 200, `{"user_id":"123456789","available_margin":"1200","margin_balance":"1200","initial_margin":"500",
		"maintenance_margin":"250","initial_margin_rate":"2.4","maintenance_margin_rate":"4.8","position_mode":"SINGLE",
		"account_limit":"5000","create_time":"1687573845000","update_time":"1687588938000","account_mode":"CROSS_EXCHANGE",
		"exchange_type":"CROSSEX","assets":[{"user_id":"123456789","coin":"USDT","exchange_type":"BINANCE","balance":"1000",
		"upnl":"200","equity":"1200","futures_initial_margin":"400","futures_maintenance_margin":"130",
		"borrowing_initial_margin":"100","borrowing_maintenance_margin":"120","available_balance":"1000.0","liability":"0"}]}`)
	account, err := client.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodGet || got.path != "/api/v4/crossex/accounts" || got.rawQuery != "" {
		t.Fatalf("request = %s %s?%s", got.method, got.path, got.rawQuery)
	}
	assertSigned(t, got)
	if account.AccountMode != AccountModeCross || account.PositionMode != PositionModeSingle || account.AvailableMargin != "1200" ||
		len(account.Assets) != 1 || account.Assets[0].ExchangeType != "BINANCE" || account.Assets[0].Equity != "1200" {
		t.Fatalf("account = %+v", account)
	}
}

func TestPlaceOrderSendsSignedBodyAndOmitsEmptyFields(t *testing.T) {
	client, got := testServer(t, 200, `{"order_id":"123456","text":"cross-test-1"}`)
	ack, err := client.PlaceOrder(context.Background(), OrderRequest{
		Text: "1700000000123", Symbol: "BINANCE_FUTURE_BTC_USDT", Side: "BUY", Type: "LIMIT",
		TimeInForce: "GTC", Qty: "0.001", Price: "60000", ReduceOnly: "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/api/v4/crossex/orders" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	assertSigned(t, got)
	if got.header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", got.header.Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"quote_qty", "position_side"} {
		if _, ok := body[absent]; ok {
			t.Fatalf("empty %s was sent: %s", absent, got.body)
		}
	}
	if body["text"] != "1700000000123" || body["symbol"] != "BINANCE_FUTURE_BTC_USDT" || body["qty"] != "0.001" {
		t.Fatalf("body = %s", got.body)
	}
	if ack.OrderID != "123456" || ack.Text != "cross-test-1" {
		t.Fatalf("ack = %+v", ack)
	}
}

func TestOrderByTextAndCancelUseThePath(t *testing.T) {
	client, got := testServer(t, 200, `{"user_id":"10001004","order_id":"2048522992198912","text":"2048522992198912","state":"FILLED",
		"symbol":"BINANCE_SPOT_ADA_USDT","side":"BUY","type":"MARKET","attribute":"COMMON","exchange_type":"BINANCE",
		"business_type":"SPOT","qty":"0","quote_qty":"7","price":"0","time_in_force":"GTC","executed_qty":"12.9",
		"executed_amount":"6.96471","executed_avg_price":"0.5399","fee_coin":"ADA","fee":"0.0129","reduce_only":"false",
		"leverage":"1","reason":"","last_executed_qty":"12.9","last_executed_price":"0.5399","last_executed_amount":"6.96471",
		"position_side":"NONE","create_time":"1750681141933","update_time":"1750681142379"}`)
	order, err := client.Order(context.Background(), "1700000000123")
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/api/v4/crossex/orders/1700000000123" {
		t.Fatalf("path = %s", got.path)
	}
	assertSigned(t, got)
	if order.State != StateFilled || order.ExecutedQty != "12.9" || order.Text != "2048522992198912" {
		t.Fatalf("order = %+v", order)
	}
	if _, err := client.CancelOrder(context.Background(), "2048522992198912"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodDelete || got.path != "/api/v4/crossex/orders/2048522992198912" {
		t.Fatalf("cancel = %s %s", got.method, got.path)
	}
	if _, err := client.Order(context.Background(), "../accounts"); err == nil {
		t.Fatal("path traversal id accepted")
	}
}

func TestOpenOrdersAcceptsClientOrderIDAlias(t *testing.T) {
	// The open_orders example names the client id "client_order_id".
	client, got := testServer(t, 200, `[{"user_id":"10001004","order_id":"2048529119934720","client_order_id":"777",
		"state":"PARTIALLY_FILLED","symbol":"OKX_SPOT_ADA_USDT","side":"BUY","type":"MARKET","attribute":"COMMON",
		"exchange_type":"OKX","business_type":"SPOT","qty":"6","quote_qty":"6","price":"0","time_in_force":"GTC",
		"executed_qty":"11.0354","executed_amount":"5.99994698","executed_avg_price":"0.5437","fee_coin":"ADA",
		"fee":"0.0110354","reduce_only":"false","leverage":"1","reason":"","position_side":"NONE",
		"create_time":"1750682602377","update_time":"1750682602413"}]`)
	orders, err := client.OpenOrders(context.Background(), OpenOrdersQuery{BusinessType: BusinessFuture})
	if err != nil {
		t.Fatal(err)
	}
	if got.rawQuery != "business_type=FUTURE" {
		t.Fatalf("query = %q", got.rawQuery)
	}
	assertSigned(t, got)
	if len(orders) != 1 || orders[0].Text != "777" || orders[0].State != StatePartiallyFilled {
		t.Fatalf("orders = %+v", orders)
	}
}

func TestPositionsTradesAndLeverage(t *testing.T) {
	client, got := testServer(t, 200, `[{"user_id":"10001004","position_id":"20062926505289216","symbol":"OKX_FUTURE_ADA_USDT",
		"position_side":"LONG","initial_margin":"5.79934625","isolated_margin":"0","margin_mode":"CROSS",
		"maintenance_margin":"0.06229625","position_qty":"10","position_value":"5.795","upnl":"0.369",
		"upnl_rate":"0.068005897530409141","entry_price":"0.5426","liq_price":"0","mark_price":"0.5795","leverage":"1",
		"max_leverage":"18","risk_limit":"1","fee":"0.002713","funding_fee":"0","funding_time":"0",
		"create_time":"1750682334273","update_time":"1750730699867","closed_pnl":"12"}]`)
	positions, err := client.Positions(context.Background(), PositionsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].PositionQty != "10" || positions[0].MarginMode != "CROSS" {
		t.Fatalf("positions = %+v", positions)
	}

	client, got = testServer(t, 200, `[{"user_id":"3511316454450547","transaction_id":"2049614605858560","order_id":"2049614605857536",
		"text":"2049614605857536","symbol":"BINANCE_FUTURE_ADA_USDT","exchange_type":"BINANCE","business_type":"FUTURE",
		"side":"SELL","qty":"10","price":"0.5597","fee":"0.002798500000000000","fee_coin":"USDT","fee_rate":"0.0005",
		"match_role":"MAKER","rpnl":"-0.001","position_mode":"DUAL","position_side":"LONG","create_time":"1750941402661"}]`)
	trades, err := client.Trades(context.Background(), HistoryQuery{From: 1750000000000, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/api/v4/crossex/history_trades" || got.rawQuery != "from=1750000000000&limit=100" {
		t.Fatalf("trades request = %s?%s", got.path, got.rawQuery)
	}
	assertSigned(t, got)
	if len(trades) != 1 || trades[0].MatchRole != "MAKER" || trades[0].Fee != "0.002798500000000000" {
		t.Fatalf("trades = %+v", trades)
	}

	client, got = testServer(t, 202, `{"symbol":"OKX_FUTURE_ADA_USDT","leverage":"1"}`)
	leverage, err := client.SetLeverage(context.Background(), "OKX_FUTURE_ADA_USDT", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.body != `{"leverage":"1","symbol":"OKX_FUTURE_ADA_USDT"}` {
		t.Fatalf("leverage request = %s %s", got.method, got.body)
	}
	assertSigned(t, got)
	if leverage.Leverage != "1" {
		t.Fatalf("leverage = %+v", leverage)
	}

	client, _ = testServer(t, 200, `{"BINANCE_FUTURE_BTC_USDT":"3","OKX_FUTURE_BTC_USDT":"3"}`)
	leverages, err := client.Leverages(context.Background())
	if err != nil || leverages["OKX_FUTURE_BTC_USDT"] != "3" {
		t.Fatalf("leverages = %v, %v", leverages, err)
	}
}

func TestSymbolsIsPublic(t *testing.T) {
	client, got := testServer(t, 200, `[{"symbol":"HYPERLIQUID_FUTURE_kPEPE_USDC","exchange_type":"HYPERLIQUID","business_type":"FUTURE",
		"state":"live","min_size":"1","min_notional":"10","lot_size":"1","tick_size":"0.000001","max_num_orders":"100",
		"max_market_size":null,"max_limit_size":null,"contract_size":null,"liquidation_fee":"0.005","default_leverage":"5",
		"delist_time":"0","support_rpi":"false","support_cross":"true","tag":""}]`)
	client.WithCredentials("", "")
	symbols, err := client.Symbols(context.Background(), "HYPERLIQUID_FUTURE_kPEPE_USDC")
	if err != nil {
		t.Fatal(err)
	}
	if got.header.Get("SIGN") != "" || got.rawQuery != "symbols=HYPERLIQUID_FUTURE_kPEPE_USDC" {
		t.Fatalf("public request signed or wrong query: %q %v", got.rawQuery, got.header)
	}
	if len(symbols) != 1 || symbols[0].Base() != "kPEPE" || symbols[0].Quote() != "USDC" || symbols[0].MaxMarketSize != nil ||
		symbols[0].MinNotional == nil || *symbols[0].MinNotional != "10" {
		t.Fatalf("symbols = %+v", symbols)
	}
}

func TestPrivateCallsRequireCredentials(t *testing.T) {
	client := NewClient()
	if _, err := client.Account(context.Background()); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIErrorClassification(t *testing.T) {
	cases := []struct {
		status      int
		body        string
		notFound    bool
		credentials bool
		definitive  bool
	}{
		{404, `{"label":"TRADE_ORDER_NOT_FOUND_ERROR","message":"The order was not found"}`, true, false, true},
		{400, `{"label":"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR","message":"Insufficient availableMargin"}`, false, false, true},
		{401, `{"label":"INVALID_KEY","message":"Invalid key provided"}`, false, true, true},
		// A duplicate text may mean an earlier attempt was accepted.
		{400, `{"label":"TRADE_ORDER_DUPLICATE_ERROR","message":"Text duplicate"}`, false, false, false},
		{400, `{"label":"SOMETHING_NEW","message":"?"}`, false, false, false},
		{429, `{"label":"TOO_MANY_REQUESTS","message":"slow down"}`, false, false, false},
		{502, `<html>bad gateway</html>`, false, false, false},
	}
	for _, tc := range cases {
		client, _ := testServer(t, tc.status, tc.body)
		_, err := client.PlaceOrder(context.Background(), OrderRequest{Symbol: "GATE_FUTURE_BTC_USDT", Side: "BUY", Qty: "1"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
			t.Fatalf("%s: err = %v", tc.body, err)
		}
		var httpErr *venuehttp.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != apiErr.Label {
			t.Fatalf("%s: HTTP error code = %+v", tc.body, httpErr)
		}
		if IsOrderNotFound(err) != tc.notFound || IsCredentialRejection(err) != tc.credentials || IsDefinitiveCommandRejection(err) != tc.definitive {
			t.Fatalf("%s: notFound=%v credentials=%v definitive=%v", tc.body, IsOrderNotFound(err), IsCredentialRejection(err), IsDefinitiveCommandRejection(err))
		}
	}
}

func TestMalformedSuccessBodyIsMarked(t *testing.T) {
	client, _ := testServer(t, 200, `{"order_id":`)
	_, err := client.PlaceOrder(context.Background(), OrderRequest{Symbol: "GATE_FUTURE_BTC_USDT", Side: "BUY", Qty: "1"})
	if !errors.Is(err, venuehttp.ErrMalformedResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestFailReasonParsesOnlyFailOrders(t *testing.T) {
	fail := Order{State: StateFail, Reason: `{"label":"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR","message":"Insufficient availableMargin"}`}
	if reason, ok := fail.FailReason(); !ok || reason.Label != "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR" {
		t.Fatalf("fail reason = %+v %v", reason, ok)
	}
	reject := Order{State: StateReject, Reason: "Due to the order could not be executed as maker, the Post Only order will be rejected."}
	if _, ok := reject.FailReason(); ok {
		t.Fatal("exchange text parsed as a CrossEx reason")
	}
	if !strings.Contains(reject.Reason, "Post Only") {
		t.Fatal("reject reason lost")
	}
}
