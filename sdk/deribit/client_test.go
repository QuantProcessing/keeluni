package deribit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stubServer struct {
	auths atomic.Int32
	calls []*http.Request
}

// Public payloads are captured from www.deribit.com on 2026-10-18; private
// payloads are the documented OpenAPI examples.
func newStub(t *testing.T, routes map[string]string) (*Client, *stubServer) {
	t.Helper()
	stub := &stubServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.calls = append(stub.calls, r)
		method := strings.TrimPrefix(r.URL.Path, "/api/v2/")
		if method == "public/auth" {
			stub.auths.Add(1)
			if r.URL.Query().Get("client_secret") != "secret" {
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","error":{"message":"invalid_credentials","code":13004},"testnet":false}`)
				return
			}
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{"access_token":"tok","expires_in":900,"refresh_token":"r","scope":"connection","token_type":"bearer"}}`)
			return
		}
		if strings.HasPrefix(method, "private/") && r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","error":{"message":"unauthorized","code":13009}}`)
			return
		}
		payload, ok := routes[method]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasPrefix(payload, "@") {
			data, err := os.ReadFile("testdata/" + payload[1:])
			if err != nil {
				t.Fatal(err)
			}
			payload = string(data)
		}
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)
	return NewClient().WithBaseURL(server.URL).WithCredentials("id", "secret"), stub
}

func TestPublicMethodsDecodeLivePayloads(t *testing.T) {
	client, stub := newStub(t, map[string]string{
		"public/get_instruments":               "@instruments.json",
		"public/ticker":                        "@ticker.json",
		"public/get_order_book":                "@book.json",
		"public/get_last_trades_by_instrument": "@trades.json",
		"public/get_tradingview_chart_data":    "@candles.json",
		"public/get_funding_rate_history":      "@funding.json",
	})
	ctx := context.Background()
	instruments, err := client.Instruments(ctx, "USDC")
	if err != nil {
		t.Fatal(err)
	}
	linear := 0
	for _, instrument := range instruments {
		if instrument.LinearPerpetual() {
			linear++
		}
		if instrument.InstrumentName == "AAPL_USDC-PERPETUAL" && (instrument.ContractSize.String() != "0.01" || instrument.BaseCurrency != "AAPL") {
			t.Fatalf("AAPL = %+v", instrument)
		}
	}
	if linear != 3 {
		t.Fatalf("linear perps = %d of %d", linear, len(instruments))
	}
	if got := stub.calls[0].URL.RawQuery; got != "currency=USDC&kind=future" {
		t.Fatalf("instruments query = %q", got)
	}
	ticker, err := client.Ticker(ctx, "BTC_USDC-PERPETUAL")
	if err != nil || ticker.MarkPrice.String() == "" || ticker.BestBidPrice.String() == "" || ticker.Stats.Volume.String() == "" {
		t.Fatalf("ticker = %+v, %v", ticker, err)
	}
	book, err := client.OrderBook(ctx, "BTC_USDC-PERPETUAL", 50)
	if err != nil || len(book.Bids) != 3 || book.ChangeID == 0 {
		t.Fatalf("book = %+v, %v", book, err)
	}
	trades, err := client.LastTrades(ctx, "BTC_USDC-PERPETUAL", 50)
	if err != nil || len(trades) == 0 || trades[0].Direction == "" || trades[0].TradeID == "" {
		t.Fatalf("trades = %+v, %v", trades, err)
	}
	chart, err := client.Chart(ctx, "BTC_USDC-PERPETUAL", "1", 1792333980000, 1792334100000)
	if err != nil || len(chart.Ticks) != 3 || len(chart.Close) != 3 {
		t.Fatalf("chart = %+v, %v", chart, err)
	}
	funding, err := client.FundingRateHistory(ctx, "BTC_USDC-PERPETUAL", 0, 1)
	if err != nil || len(funding) != 3 || funding[0].Interest1h.String() == "" {
		t.Fatalf("funding = %+v, %v", funding, err)
	}
	for _, call := range stub.calls {
		if call.Header.Get("Authorization") != "" {
			t.Fatal("public call carried a token")
		}
	}
	if stub.auths.Load() != 0 {
		t.Fatal("public calls authenticated")
	}
}

func TestPrivateCallsAuthenticateOnceAndDecode(t *testing.T) {
	client, stub := newStub(t, map[string]string{
		"private/buy": `{"jsonrpc":"2.0","id":6130,"result":{"trades":[{"trade_seq":1966068,"trade_id":"ETH-2696097","timestamp":1590486335742,"state":"filled","reduce_only":true,"price":202.8,"order_type":"limit","order_id":"ETH-584864807","liquidity":"T","instrument_name":"ETH-PERPETUAL","fee_currency":"ETH","fee":7.766e-05,"direction":"sell","amount":21}],
			"order":{"web":false,"time_in_force":"good_til_cancelled","replaced":false,"reduce_only":true,"price":198.75,"post_only":false,"order_type":"limit","order_state":"filled","order_id":"ETH-584864807","max_show":21,"last_update_timestamp":1590486335742,"label":"","is_liquidation":false,"instrument_name":"ETH-PERPETUAL","filled_amount":21,"direction":"sell","creation_timestamp":1590486335742,"average_price":202.8,"api":true,"amount":21}}}`,
		"private/cancel":                      `{"jsonrpc":"2.0","id":4214,"result":{"triggered":false,"time_in_force":"good_til_cancelled","reduce_only":false,"price":"market_price","post_only":false,"order_type":"stop_market","order_state":"untriggered","order_id":"ETH-SLIS-12","label":"","instrument_name":"ETH-PERPETUAL","direction":"sell","creation_timestamp":1550575961291,"api":false,"amount":5}}`,
		"private/get_positions":               `{"jsonrpc":"2.0","id":2236,"result":[{"average_price":7440.18,"direction":"buy","estimated_liquidation_price":null,"floating_profit_loss":0,"initial_margin":0.000197283,"instrument_name":"BTC-PERPETUAL","kind":"future","leverage":35.639422,"maintenance_margin":0.000143783,"mark_price":7476.65,"size":50}]}`,
		"private/get_open_orders_by_currency": `{"jsonrpc":"2.0","id":1953,"result":[{"time_in_force":"good_til_cancelled","reduce_only":false,"price":0.0028,"post_only":false,"order_type":"limit","order_state":"open","order_id":"146062","label":"fooBar","instrument_name":"BTC-15FEB19-3250-P","filled_amount":0,"direction":"buy","creation_timestamp":1550050597036,"average_price":0,"api":true,"amount":10}]}`,
		"private/get_account_summary":         `{"jsonrpc":"2.0","result":{"currency":"USDC","balance":1000.5,"equity":1001.2,"margin_balance":1001.2,"available_funds":990.1,"initial_margin":11.1,"maintenance_margin":5.5,"futures_pl":0.7,"session_upl":0.7,"total_pl":0.7}}`,
	})
	ctx := context.Background()
	placed, err := client.Place(ctx, OrderRequest{Direction: "buy", Instrument: "BTC_USDC-PERPETUAL", Amount: "0.001", Type: "limit", Price: "60000", TimeInForce: "good_til_cancelled", Label: "bt-1", PostOnly: true, ReduceOnly: true})
	if err != nil || placed.Order.OrderState != "filled" || placed.Order.LimitPrice() != "198.75" || len(placed.Trades) != 1 {
		t.Fatalf("place = %+v, %v", placed, err)
	}
	query := stub.calls[len(stub.calls)-1].URL.Query()
	want := url.Values{"instrument_name": {"BTC_USDC-PERPETUAL"}, "amount": {"0.001"}, "type": {"limit"}, "price": {"60000"}, "time_in_force": {"good_til_cancelled"}, "label": {"bt-1"}, "post_only": {"true"}, "reject_post_only": {"true"}, "reduce_only": {"true"}}
	if query.Encode() != want.Encode() || stub.calls[len(stub.calls)-1].URL.Path != "/api/v2/private/buy" {
		t.Fatalf("place request = %s %s", stub.calls[len(stub.calls)-1].URL.Path, query.Encode())
	}
	if _, err := client.Place(ctx, OrderRequest{Direction: "sell", Instrument: "x", Amount: "1", Type: "market"}); err == nil || !strings.Contains(stub.calls[len(stub.calls)-1].URL.Path, "private/sell") {
		t.Fatal("sell did not route to private/sell")
	}
	cancelled, err := client.Cancel(ctx, "ETH-SLIS-12")
	if err != nil || cancelled.LimitPrice() != "" || cancelled.OrderState != "untriggered" {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	positions, err := client.Positions(ctx, "USDC")
	if err != nil || len(positions) != 1 || positions[0].EstimatedLiquidationPrice.String() != "" {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	orders, err := client.OpenOrders(ctx, "USDC")
	if err != nil || len(orders) != 1 || orders[0].Label != "fooBar" {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
	summary, err := client.AccountSummary(ctx, "USDC")
	if err != nil || summary.AvailableFunds.String() != "990.1" {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	if stub.auths.Load() != 1 {
		t.Fatalf("authenticated %d times", stub.auths.Load())
	}
}

func TestTokenRefreshesBeforeExpiryAndAfterUnauthorized(t *testing.T) {
	client, stub := newStub(t, map[string]string{"private/get_positions": `{"jsonrpc":"2.0","result":[]}`})
	now := time.Unix(1792334590, 0)
	client.WithClock(func() time.Time { return now })
	ctx := context.Background()
	if _, err := client.Positions(ctx, "USDC"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(14 * time.Minute) // within a minute of the 15 min expiry
	if _, err := client.Positions(ctx, "USDC"); err != nil {
		t.Fatal(err)
	}
	if stub.auths.Load() != 2 {
		t.Fatalf("auths = %d, want refresh before expiry", stub.auths.Load())
	}
	client.token = "stale"
	_, err := client.Positions(ctx, "USDC")
	if !IsAuthError(err) || client.token != "" {
		t.Fatalf("unauthorized err = %v token=%q", err, client.token)
	}
	if _, err := client.Positions(ctx, "USDC"); err != nil {
		t.Fatalf("token was not re-obtained: %v", err)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	client, _ := newStub(t, map[string]string{
		"private/get_order_state": `{"jsonrpc":"2.0","error":{"message":"order_not_found","code":10004}}`,
		"private/sell":            `{"jsonrpc":"2.0","error":{"message":"not_enough_funds","code":10009,"data":{"reason":"insufficient margin"}}}`,
	})
	ctx := context.Background()
	if _, err := client.OrderState(ctx, "x"); !IsOrderNotFound(err) {
		t.Fatalf("not found err = %v", err)
	}
	_, err := client.Place(ctx, OrderRequest{Direction: "sell", Instrument: "x", Amount: "1", Type: "market"})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeNotEnoughFunds || rpcErr.Reason != "insufficient margin" {
		t.Fatalf("funds err = %v", err)
	}
	if _, err := NewClient().Positions(ctx, "USDC"); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("no credentials err = %v", err)
	}
	bad, _ := newStub(t, nil)
	bad.WithCredentials("id", "wrong")
	if _, err := bad.Positions(ctx, "USDC"); !IsAuthError(err) {
		t.Fatalf("bad secret err = %v", err)
	}
}
