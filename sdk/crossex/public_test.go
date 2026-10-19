package crossex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// publicFake plays the public endpoint. It records requests per connection
// and lets the test push frames or drop the connection.
type publicFake struct {
	mu          sync.Mutex
	connections int
	requests    []map[string]any
	frames      chan string
}

func newPublicFake(t *testing.T) (*publicFake, string) {
	fake := &publicFake{frames: make(chan string, 32)}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		fake.mu.Lock()
		fake.connections++
		fake.mu.Unlock()
		go func() {
			for {
				var request map[string]any
				if conn.ReadJSON(&request) != nil {
					return
				}
				fake.mu.Lock()
				fake.requests = append(fake.requests, request)
				fake.mu.Unlock()
				if request["channel"] == "order_book_20" {
					payload, _ := json.Marshal(request["payload"])
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"order_book_20","event":"subscribe","payload":`+string(payload)+
						`,"result":{"status":"failed"},"error":{"code":1000106,"message":"Kraken does not support snapshot orderBook levels"}}`))
				}
			}
		}()
		for frame := range fake.frames {
			if frame == "drop" {
				return
			}
			if conn.WriteMessage(websocket.TextMessage, []byte(frame)) != nil {
				return
			}
		}
	}))
	t.Cleanup(func() { close(fake.frames); server.Close() })
	return fake, "ws" + strings.TrimPrefix(server.URL, "http")
}

func (fake *publicFake) subscriptions(event, channel string) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	count := 0
	for _, request := range fake.requests {
		if request["event"] == event && request["channel"] == channel {
			count++
		}
	}
	return count
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPublicStreamSharesTopicsRoutesBySymbolAndResubscribes(t *testing.T) {
	fake, url := newPublicFake(t)
	stream := NewPublicStream().WithURL(url)
	defer stream.Close()

	var mu sync.Mutex
	got := map[string][]string{}
	record := func(name string) func(PublicUpdate) {
		return func(update PublicUpdate) {
			var trade PublicTrade
			_ = DecodePublic(update.Result, &trade)
			mu.Lock()
			got[name] = append(got[name], trade.Symbol+"@"+trade.Price)
			mu.Unlock()
		}
	}
	stopA, err := stream.Subscribe(ChannelTrade, "KRAKEN_FUTURE_BTC_USD", record("a"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stopB, _ := stream.Subscribe(ChannelTrade, "KRAKEN_FUTURE_BTC_USD", record("b"), nil)
	_, _ = stream.Subscribe(ChannelTrade, "DERIBIT_FUTURE_BTC_USDC", record("c"), nil)
	eventually(t, "two trade subscriptions", func() bool { return fake.subscriptions("subscribe", ChannelTrade) == 2 })

	fake.frames <- `{"channel":"trade","event":"update","result":{"s":"KRAKEN_FUTURE_BTC_USD","i":"1","p":"81922.0","q":"0.0047","S":"SELL","ts":1}}`
	fake.frames <- `{"channel":"trade","event":"update","result":{"s":"DERIBIT_FUTURE_BTC_USDC","i":"2","p":"82009.7","q":"0.0016","S":"BUY","ts":1}}`
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := len(got["a"]) == 1 && len(got["b"]) == 1 && len(got["c"]) == 1
		snapshot := fmtMap(got)
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("routed trades = %s", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if got["a"][0] != "KRAKEN_FUTURE_BTC_USD@81922.0" || got["c"][0] != "DERIBIT_FUTURE_BTC_USDC@82009.7" {
		t.Fatalf("routing = %v", got)
	}
	mu.Unlock()

	// The last handler of a topic unsubscribes it upstream.
	stopA()
	if fake.subscriptions("unsubscribe", ChannelTrade) != 0 {
		t.Fatal("unsubscribed while a handler remained")
	}
	stopB()
	eventually(t, "upstream unsubscribe", func() bool { return fake.subscriptions("unsubscribe", ChannelTrade) == 1 })

	// After a drop, the remaining topic is resubscribed on a new connection.
	fake.frames <- "drop"
	eventually(t, "reconnect and resubscribe", func() bool {
		fake.mu.Lock()
		connections := fake.connections
		fake.mu.Unlock()
		return connections == 2 && fake.subscriptions("subscribe", ChannelTrade) == 3
	})
}

func TestPublicStreamReportsRefusedSubscription(t *testing.T) {
	_, url := newPublicFake(t)
	stream := NewPublicStream().WithURL(url)
	defer stream.Close()
	refused := make(chan error, 1)
	_, err := stream.Subscribe(ChannelOrderBook(20), "KRAKEN_FUTURE_BTC_USD", func(PublicUpdate) {}, func(err error) { refused <- err })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-refused:
		if !strings.Contains(err.Error(), "does not support snapshot") {
			t.Fatalf("refusal = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refusal not reported")
	}
}

func TestPublicStreamRejectsUseAfterClose(t *testing.T) {
	stream := NewPublicStream().WithURL("ws://127.0.0.1:1/never")
	_ = stream.Close()
	if _, err := stream.Subscribe(ChannelTrade, "X", func(PublicUpdate) {}, nil); err == nil {
		t.Fatal("subscribe after close accepted")
	}
}

func fmtMap(values map[string][]string) string {
	data, _ := json.Marshal(values)
	return string(data)
}

func TestDecodePublicKeepsCaseCollidingKeysApart(t *testing.T) {
	var trade PublicTrade
	if err := DecodePublic(json.RawMessage(`{"s":"KRAKEN_FUTURE_BTC_USD","i":"1","p":"81922.0","q":"0.0047","S":"SELL","ts":1}`), &trade); err != nil {
		t.Fatal(err)
	}
	if trade.Symbol != "KRAKEN_FUTURE_BTC_USD" || trade.Side != "SELL" || trade.Qty != "0.0047" {
		t.Fatalf("trade = %+v", trade)
	}
	var kline PublicKline
	_ = DecodePublic(json.RawMessage(`{"s":"X","o":"1","h":"2","l":"0.5","c":"1.5","v":"10","t":1792326420000,"T":1792326479999,"x":true}`), &kline)
	if kline.Start != 1792326420000 || kline.End != 1792326479999 || !kline.Closed {
		t.Fatalf("kline = %+v", kline)
	}
	var book PublicBook
	_ = DecodePublic(json.RawMessage(`{"snapshot":true,"ts":5,"s":"X","U":7,"u":9,"a":[["1","2"]],"b":[]}`), &book)
	if book.First.String() != "7" || book.Last.String() != "9" || !book.Snapshot || len(book.Asks) != 1 {
		t.Fatalf("book = %+v", book)
	}
	var funding PublicFunding
	_ = DecodePublic(json.RawMessage(`{"s":"X","r":"0.0001","T":1792339200000}`), &funding)
	if funding.Next == nil || *funding.Next != 1792339200000 {
		t.Fatalf("funding = %+v", funding)
	}
	_ = DecodePublic(json.RawMessage(`{"s":"X","r":"0.00007943","T":null}`), &funding)
	if funding.Next != nil {
		t.Fatalf("null next funding decoded as %v", *funding.Next)
	}
}
