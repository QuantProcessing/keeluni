package kraken

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

// Vectors produced by the official module github.com/krakenfx/api-go
// (derivatives.Sign) with this base64 secret.
const testSecret = "dGVzdF9zZWNyZXRfa2V5X2Zvcl9rcmFrZW5fZnV0dXJlc192ZWN0b3JzXzA2NA=="

func TestSignMatchesOfficialClient(t *testing.T) {
	cases := []struct{ data, nonce, path, want string }{
		{"symbol=fi_xbtusd_180615", "1415957147987", "/derivatives/api/v3/orderbook",
			"7uTMrjWrvLCza/NT+c3DMlutMoZIcf+dJUTc5Mer9HA/BtSxp6SCxStkakF3FJwKv4+YkKP4guN+xkofBrMSEw=="},
		{"orderType=lmt&symbol=PF_XBTUSD&side=buy&size=0.0001&limitPrice=60000&cliOrdId=17", "1792334590406", "/derivatives/api/v3/sendorder",
			"2+CYarQes7clu5GWcO2pD6xCMjVV3haoL760swE9QvFk8+j/A2SdLmoiHRxccrEIZTSG9IagYxaQjZEk/phRRA=="},
		{"", "1792334590407", "/derivatives/api/v3/openpositions",
			"Z85+6adq0hktSGH28fIRIRxbfr3T30S1EZacBm/YHb+/fLt/C+CBDmEH2CQyUBSI8L310XA+muZPb+fns6BRAA=="},
	}
	for _, tc := range cases {
		got, err := Sign(testSecret, tc.data, tc.nonce, tc.path)
		if err != nil || got != tc.want {
			t.Fatalf("Sign(%q) = %s, %v", tc.data, got, err)
		}
	}
	if _, err := Sign("not base64!", "", "1", "/x"); err == nil {
		t.Fatal("invalid secret accepted")
	}
}

type recorded struct {
	method, path, query, body string
	header                    http.Header
}

func stub(t *testing.T, routes map[string]string) (*Client, *[]recorded) {
	t.Helper()
	var seen []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Clone()})
		payload, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if len(payload) > 0 && payload[0] == '@' {
			data, err := os.ReadFile("testdata/" + payload[1:])
			if err != nil {
				t.Fatal(err)
			}
			payload = string(data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)
	client := NewClient().WithBaseURL(server.URL).WithCredentials("key", testSecret).WithClock(func() time.Time { return time.UnixMilli(1792334590406) })
	return client, &seen
}

func TestPublicEndpointsDecodeLivePayloads(t *testing.T) {
	client, seen := stub(t, map[string]string{
		"GET /derivatives/api/v3/instruments":   "@instruments.json",
		"GET /derivatives/api/v3/orderbook":     "@orderbook.json",
		"GET /derivatives/api/v3/history":       "@history.json",
		"GET /derivatives/api/v3/tickers":       "@tickers.json",
		"GET /api/charts/v1/trade/PF_XBTUSD/1m": "@candles.json",
	})
	ctx := context.Background()
	instruments, err := client.Instruments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var btc Instrument
	for _, instrument := range instruments {
		if instrument.Symbol == "PF_XBTUSD" {
			btc = instrument
		}
	}
	if !btc.Linear() || btc.Base != "BTC" || btc.Quote != "USD" || btc.TickSize.String() != "1" || btc.ContractValueTradePrecision.String() != "4" {
		t.Fatalf("PF_XBTUSD = %+v", btc)
	}
	book, err := client.OrderBook(ctx, "PF_XBTUSD")
	if err != nil || len(book.Bids) != 3 || len(book.Asks) != 3 || book.ServerTime == "" {
		t.Fatalf("book = %+v, %v", book, err)
	}
	if (*seen)[len(*seen)-1].query != "symbol=PF_XBTUSD" || (*seen)[len(*seen)-1].header.Get("Authent") != "" {
		t.Fatal("public request was signed or lost its query")
	}
	trades, err := client.TradeHistory(ctx, "PF_XBTUSD")
	if err != nil || len(trades) != 3 || trades[0].Side == "" || trades[0].Price.String() == "" {
		t.Fatalf("trades = %+v, %v", trades, err)
	}
	candles, err := client.Candles(ctx, "PF_XBTUSD", "1m", 1792333980, 0)
	if err != nil || len(candles) != 3 || candles[0].Open == "" {
		t.Fatalf("candles = %+v, %v", candles, err)
	}
	if (*seen)[len(*seen)-1].query != "from=1792333980" {
		t.Fatalf("candle query = %q", (*seen)[len(*seen)-1].query)
	}
	tickers, err := client.Tickers(ctx)
	if err != nil || len(tickers) != 2 || tickers[0].MarkPrice.String() == "" {
		t.Fatalf("tickers = %+v, %v", tickers, err)
	}
}

// Private payloads below are the documented examples of the Derivatives REST
// reference (send-order, cancel-order, open positions, open orders, fills,
// wallets).
func TestPrivateEndpointsAreSignedAndDecode(t *testing.T) {
	client, seen := stub(t, map[string]string{
		"POST /derivatives/api/v3/sendorder": `{"result":"success","sendStatus":{"order_id":"179f9af8-e45e-469d-b3e9-2fd4675cb7d0","status":"placed","receivedTime":"2019-09-05T16:33:50.734Z",
			"orderEvents":[{"type":"PLACE","order":{"orderId":"179f9af8-e45e-469d-b3e9-2fd4675cb7d0","cliOrdId":"17","type":"lmt","symbol":"PF_XBTUSD","side":"buy","quantity":0.0001,"filled":0,"limitPrice":60000,"reduceOnly":false,"timestamp":"2019-09-05T16:33:50.734Z","lastUpdateTimestamp":"2019-09-05T16:33:50.734Z"},"reducedQuantity":null}]},"serverTime":"2019-09-05T16:33:50.734Z"}`,
		"POST /derivatives/api/v3/cancelorder":  `{"result":"success","cancelStatus":{"status":"cancelled","order_id":"cb4e34f6-4eb3-4d4b-9724-4c3035b99d47","receivedTime":"2020-07-22T13:26:20.806Z","orderEvents":[]},"serverTime":"2020-07-22T13:26:20.806Z"}`,
		"GET /derivatives/api/v3/openpositions": `{"result":"success","openPositions":[{"side":"long","symbol":"PF_DEFIUSD","price":570,"size":1,"unrealizedPnl":12.34,"unrealizedFunding":-0.0073428045972263895,"pnlCurrency":"BTC","maxFixedLeverage":5}],"serverTime":"2020-07-22T14:39:12.376Z"}`,
		"GET /derivatives/api/v3/openorders":    `{"result":"success","openOrders":[{"order_id":"59302619-41d2-4f0b-941f-7e7914760ad3","symbol":"PI_XBTUSD","side":"sell","orderType":"lmt","limitPrice":10640,"unfilledSize":304,"receivedTime":"2019-09-05T17:01:17.410Z","status":"untouched","filledSize":0,"reduceOnly":true,"lastUpdateTime":"2019-09-05T17:01:17.410Z"}],"serverTime":"2019-09-05T17:01:17.410Z"}`,
		"GET /derivatives/api/v3/fills":         `{"result":"success","fills":[{"fill_id":"3d57ed09-fbd6-44f1-8e8b-b10e551c5e73","symbol":"PI_XBTUSD","side":"buy","order_id":"693af756-055e-47ef-99d5-bcf4c456ebc5","size":5490,"price":9400,"fillTime":"2020-07-22T13:37:27.077Z","fillType":"maker"}],"serverTime":"2020-07-22T13:44:24.311Z"}`,
		"GET /derivatives/api/v3/accounts": `{"result":"success","serverTime":"2016-02-25T09:45:53.818Z","accounts":{"cash":{"type":"cashAccount","balances":{"xbt":"141.31756797"}},
			"flex":{"type":"multiCollateralMarginAccount","currencies":{"USD":{"quantity":5000,"value":5000,"collateral":5000,"available":5000}},"balanceValue":34995.52,"portfolioValue":34995.52,"collateralValue":34122.66,"initialMargin":0,"initialMarginWithOrders":0,"maintenanceMargin":0,"pnl":0,"unrealizedFunding":0,"totalUnrealized":0,"totalUnrealizedAsMargin":0,"marginEquity":34122.66,"availableMargin":34122.66}}}`,
		"PUT /derivatives/api/v3/leveragepreferences": `{"result":"success","serverTime":"2022-06-28T14:48:58.711Z"}`,
	})
	ctx := context.Background()
	status, err := client.SendOrder(ctx, OrderRequest{OrderType: "lmt", Symbol: "PF_XBTUSD", Side: "buy", Size: "0.0001", LimitPrice: "60000", ClientOrderID: "17"})
	if err != nil || !status.IsAccepted() || status.OrderID != "179f9af8-e45e-469d-b3e9-2fd4675cb7d0" || status.OrderEvents[0].Order.ClientOrderID != "17" {
		t.Fatalf("send = %+v, %v", status, err)
	}
	sent := (*seen)[len(*seen)-1]
	if sent.body != "cliOrdId=17&limitPrice=60000&orderType=lmt&side=buy&size=0.0001&symbol=PF_XBTUSD" || sent.header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("send body = %q", sent.body)
	}
	want, _ := Sign(testSecret, sent.body, sent.header.Get("Nonce"), "/derivatives/api/v3/sendorder")
	if sent.header.Get("APIKey") != "key" || sent.header.Get("Authent") != want {
		t.Fatal("send order signature does not cover the body as sent")
	}

	cancel, err := client.CancelOrder(ctx, "cb4e34f6-4eb3-4d4b-9724-4c3035b99d47", "")
	if err != nil || cancel.Status != "cancelled" {
		t.Fatalf("cancel = %+v, %v", cancel, err)
	}
	positions, err := client.OpenPositions(ctx)
	if err != nil || len(positions) != 1 || positions[0].Side != "long" || positions[0].Price.String() != "570" {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	get := (*seen)[len(*seen)-1]
	wantGet, _ := Sign(testSecret, "", get.header.Get("Nonce"), "/derivatives/api/v3/openpositions")
	if get.header.Get("Authent") != wantGet {
		t.Fatal("GET signature mismatch")
	}
	orders, err := client.OpenOrders(ctx)
	if err != nil || len(orders) != 1 || orders[0].UnfilledSize.String() != "304" || !orders[0].ReduceOnly {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
	fills, err := client.Fills(ctx, "")
	if err != nil || len(fills) != 1 || fills[0].FillType != "maker" {
		t.Fatalf("fills = %+v, %v", fills, err)
	}
	accounts, err := client.Accounts(ctx)
	if err != nil || accounts.Flex == nil || accounts.Flex.AvailableMargin.String() != "34122.66" || accounts.Flex.Currencies["USD"].Available.String() != "5000" {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	if err := client.SetLeveragePreference(ctx, "PF_XBTUSD", "10"); err != nil {
		t.Fatal(err)
	}
	put := (*seen)[len(*seen)-1]
	if put.method != http.MethodPut || put.body != "maxLeverage=10&symbol=PF_XBTUSD" {
		t.Fatalf("leverage request = %s %q", put.method, put.body)
	}
}

func TestNoncesStrictlyIncrease(t *testing.T) {
	client := NewClient().WithClock(func() time.Time { return time.UnixMilli(1792334590406) })
	first, second := client.nextNonce(), client.nextNonce()
	if !(first < second) || len(first) != len(second) {
		t.Fatalf("nonces %s %s", first, second)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	client, _ := stub(t, map[string]string{
		"GET /derivatives/api/v3/accounts": `{"result":"error","error":"authenticationError","serverTime":"2026-10-18T14:42:20.639Z"}`,
	})
	_, err := client.Accounts(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "authenticationError" || !IsAuthError(err) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewClient().OpenOrders(context.Background()); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("unsigned private call err = %v", err)
	}
	if q := (params{"b": "2", "a": "x y", "c": ""}).encode(); q != "a="+url.QueryEscape("x y")+"&b=2" {
		t.Fatalf("encode = %q", q)
	}
}
