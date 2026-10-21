package krakenspot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The documented signature example (docs.kraken.com spot-rest-auth).
func TestSignMatchesDocumentedExample(t *testing.T) {
	got, err := Sign("kQH5HW/8p1uGOVjbgWA7FunAmGO8lsSUXNsu3eow76sz84Q18fWxnyRzBHCd3pd5nE9qa99HAZtuZuj6F1huXg==",
		"/0/private/AddOrder", "1616492376594", "nonce=1616492376594&ordertype=limit&pair=XBTUSD&price=37500&type=buy&volume=1.25")
	if err != nil || got != "4/dpxb3iT4tp/ZCVEwSnEsLxx0bqyhLpdfOpc6fn7OR8+UClSV5n9E6aSS8MPtnRfp32bAb0nmbRn6H8ndwLUQ==" {
		t.Fatalf("sign = %q, %v", got, err)
	}
}

func newStub(t *testing.T, routes map[string]string) (*Client, *[]*http.Request, *[]url.Values) {
	t.Helper()
	var seen []*http.Request
	var bodies []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		bodies = append(bodies, values)
		payload, ok := routes[r.URL.Path]
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
	return NewClient().WithBaseURL(server.URL).WithCredentials("key", "kQH5HW/8p1uGOVjbgWA7FunAmGO8lsSUXNsu3eow76sz84Q18fWxnyRzBHCd3pd5nE9qa99HAZtuZuj6F1huXg=="), &seen, &bodies
}

// Public payloads captured from api.kraken.com on 2026-10-20.
func TestPublicPayloads(t *testing.T) {
	client, _, _ := newStub(t, map[string]string{
		"/0/public/AssetPairs": "@asset_pairs.json", "/0/public/Assets": "@assets.json",
		"/0/public/Depth": "@depth.json", "/0/public/OHLC": "@ohlc.json", "/0/public/Trades": "@trades.json",
	})
	ctx := context.Background()
	pairs, err := client.AssetPairs(ctx)
	if err != nil || len(pairs) < 5 {
		t.Fatalf("pairs = %d, %v", len(pairs), err)
	}
	symbols := map[string]bool{}
	for _, pair := range pairs {
		symbols[SymbolOf(pair.WSName)] = true
	}
	if !symbols["BTC/USD"] || !symbols["DOGE/USD"] || !symbols["ETH/BTC"] || !symbols["ZORA/USD"] {
		t.Fatalf("symbols = %v", symbols)
	}
	assets, err := client.Assets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"XXBT": "BTC", "ZUSD": "USD", "XXDG": "DOGE", "ZORA": "ZORA", "USDT": "USDT"} {
		if got, ok := BalanceAsset(key, assets); !ok || got != want {
			t.Fatalf("BalanceAsset(%s) = %s, want %s", key, got, want)
		}
	}
	if _, ok := BalanceAsset("ETH.F", assets); ok {
		t.Fatal("earn balance treated as spot")
	}
	book, err := client.Depth(ctx, "BTC/USD", 3)
	if err != nil || len(book.Bids) != 3 || book.Asks[0][0].String() == "" {
		t.Fatalf("book = %+v, %v", book, err)
	}
	candles, err := client.OHLC(ctx, "BTC/USD", 60, 0)
	if err != nil || len(candles) != 3 || candles[0].Time == 0 || candles[0].Close == "" {
		t.Fatalf("candles = %+v, %v", candles, err)
	}
	trades, err := client.Trades(ctx, "BTC/USD", 3)
	if err != nil || len(trades) != 3 || trades[0].Time.IsZero() || (trades[0].Side != "b" && trades[0].Side != "s") || trades[0].TradeID == 0 {
		t.Fatalf("trades = %+v, %v", trades, err)
	}
}

func TestPrivateCallsSignAndClassifyErrors(t *testing.T) {
	client, seen, bodies := newStub(t, map[string]string{
		"/0/private/AddOrder":    `{"error":[],"result":{"txid":["OUF4EM-FRGI2-MQMWZD"],"descr":{"order":"buy 0.1 XBTUSD @ limit 100"}}}`,
		"/0/private/BalanceEx":   `{"error":[],"result":{"ZUSD":{"balance":"25435.21","hold_trade":"8249.76"},"XXBT":{"balance":"1.2435","hold_trade":"0.8423"}}}`,
		"/0/private/CancelOrder": `{"error":["EOrder:Unknown order"]}`,
	})
	client.WithClock(func() time.Time { return time.UnixMilli(1616492376594) })
	ctx := context.Background()
	result, err := client.AddOrder(ctx, OrderRequest{Pair: "BTC/USD", Side: "buy", Type: "limit", Volume: "0.1", Price: "100", Flags: "post", ClientOrderID: "123"})
	if err != nil || result.TxID[0] != "OUF4EM-FRGI2-MQMWZD" {
		t.Fatalf("add = %+v, %v", result, err)
	}
	request, body := (*seen)[0], (*bodies)[0]
	if request.Method != http.MethodPost || request.Header.Get("API-Key") != "key" || request.Header.Get("API-Sign") == "" ||
		body.Get("cl_ord_id") != "123" || body.Get("oflags") != "post" || body.Get("nonce") == "" || body.Get("pair") != "BTC/USD" {
		t.Fatalf("request = %v %v", request.Header, body)
	}
	balances, err := client.BalanceEx(ctx)
	if err != nil || balances["XXBT"].HoldTrade.String() != "0.8423" {
		t.Fatalf("balances = %+v, %v", balances, err)
	}
	if _, err := client.CancelOrder(ctx, "X", ""); !IsUnknownOrder(err) || !IsOrderRejection(err) || IsAuthError(err) {
		t.Fatalf("cancel err = %v", err)
	}
	if _, err := NewClient().BalanceEx(ctx); err != ErrCredentialsRequired {
		t.Fatalf("no credentials = %v", err)
	}
}

func TestStreamSubscribesWithTokenAndDelivers(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]any
		if conn.ReadJSON(&request) != nil {
			return
		}
		params := request["params"].(map[string]any)
		if params["token"] != "tok" || params["channel"] != "executions" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"method":"subscribe","success":false,"error":"EAccount:Invalid permissions","req_id":1}`))
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"method":"subscribe","success":true,"req_id":1,"result":{"channel":"executions"}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"heartbeat"}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"executions","type":"update","data":[{"order_id":"O1","cl_ord_id":"7","exec_type":"trade","order_status":"filled","symbol":"BTC/USD","side":"buy","last_qty":0.005,"last_price":26599.9,"liquidity_ind":"t","fees":[{"asset":"USD","qty":0.3458}],"timestamp":"2023-09-22T10:33:05.709993Z"}]}`))
		time.Sleep(time.Second)
	}))
	defer server.Close()
	stream := NewStream().WithURL("ws" + strings.TrimPrefix(server.URL, "http")).WithToken(func(context.Context) (string, error) { return "tok", nil })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := false
	var got []Execution
	_ = stream.Run(ctx, []Subscription{{Channel: "executions", Params: map[string]any{"snap_orders": true}}}, Handler{
		Ready: func() { ready = true },
		Message: func(message Message) {
			var rows []Execution
			if Decode(message.Data, &rows) == nil {
				got = append(got, rows...)
			}
			cancel()
		},
	})
	if !ready || len(got) != 1 || got[0].LastQty.String() != "0.005" || got[0].ClientOrderID != "7" || ParseTime(got[0].Timestamp).IsZero() {
		t.Fatalf("ready=%v got=%+v", ready, got)
	}
}
