package kraken

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fakeWS(t *testing.T, script func(read func() map[string]any, write func(string))) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		read := func() map[string]any {
			for {
				var frame map[string]any
				if conn.ReadJSON(&frame) != nil {
					return nil
				}
				if frame["event"] == "ping" {
					continue
				}
				return frame
			}
		}
		write := func(frame string) { _ = conn.WriteMessage(websocket.TextMessage, []byte(frame)) }
		write(`{"event":"info","version":1}`)
		script(read, write)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// Frames captured from wss://futures.kraken.com/ws/v1 on 2026-10-18 and the
// documented private feed examples.
func TestPrivateStreamSignsChallengeAndDelivers(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	url := fakeWS(t, func(read func() map[string]any, write func(string)) {
		challenge := read()
		mu.Lock()
		requests = append(requests, challenge)
		mu.Unlock()
		write(`{"event":"challenge","message":"226aee50-88fc-4618-a42a-34f7709570b2"}`)
		for i := 0; i < 2; i++ {
			sub := read()
			mu.Lock()
			requests = append(requests, sub)
			mu.Unlock()
			write(`{"event":"subscribed","feed":"` + sub["feed"].(string) + `"}`)
		}
		write(`{"feed":"open_orders","order":{"instrument":"PF_XBTUSD","time":1567702877410,"last_update_time":1567702877410,"qty":304.0,"filled":0.0,"limit_price":10640.0,"stop_price":0.0,"type":"limit","order_id":"59302619-41d2-4f0b-941f-7e7914760ad3","cli_ord_id":"17","direction":1,"reduce_only":true},"is_cancel":false,"reason":"new_placed_order_by_user"}`)
		write(`{"feed":"fills_snapshot","account":"DemoUser","fills":[{"instrument":"PF_XBTUSD","time":1600256910739,"price":10937.5,"seq":36,"buy":true,"qty":5000.0,"remaining_order_qty":0.0,"order_id":"9e30258b-5a98-4002-968a-5b0e149bcfbf","fill_id":"cad76f07-814e-4dc6-8478-7867407b6bff","fill_type":"maker","fee_paid":-0.00009142857,"fee_currency":"USD"}]}`)
		time.Sleep(time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	messages := make(chan Message, 4)
	done := make(chan error, 1)
	go func() {
		done <- NewStream().WithURL(url).WithCredentials("key", testSecret).Run(ctx,
			[]Subscription{{Feed: FeedOpenOrders}, {Feed: FeedFills}},
			Handler{Ready: func() { close(ready) }, Message: func(m Message) { messages <- m }})
	}()
	<-ready
	order := <-messages
	var update OpenOrdersMessage
	if order.Feed != FeedOpenOrders || order.Snapshot || Decode(order.Raw, &update) != nil || update.Order == nil ||
		update.Order.ClientOrderID != "17" || update.Order.Direction != 1 || update.Order.Qty.String() != "304.0" {
		t.Fatalf("order message = %+v %+v", order, update)
	}
	fills := <-messages
	var snapshot FillsMessage
	if fills.Feed != FeedFills || !fills.Snapshot || Decode(fills.Raw, &snapshot) != nil || len(snapshot.Fills) != 1 || snapshot.Fills[0].FeePaid.String() != "-0.00009142857" {
		t.Fatalf("fills = %+v %+v", fills, snapshot)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	signed, _ := SignChallenge(testSecret, "226aee50-88fc-4618-a42a-34f7709570b2")
	if requests[0]["event"] != "challenge" || requests[0]["api_key"] != "key" {
		t.Fatalf("challenge request = %v", requests[0])
	}
	for _, sub := range requests[1:] {
		if sub["event"] != "subscribe" || sub["original_challenge"] != "226aee50-88fc-4618-a42a-34f7709570b2" || sub["signed_challenge"] != signed {
			t.Fatalf("subscription = %v", sub)
		}
	}
}

func TestPublicStreamDecodesBookAndTrades(t *testing.T) {
	url := fakeWS(t, func(read func() map[string]any, write func(string)) {
		for i := 0; i < 2; i++ {
			sub := read()
			products, _ := json.Marshal(sub["product_ids"])
			if string(products) != `["PF_XBTUSD"]` || sub["api_key"] != nil {
				write(`{"event":"error","message":"bad request"}`)
				return
			}
			write(`{"event":"subscribed","feed":"` + sub["feed"].(string) + `","product_ids":["PF_XBTUSD"]}`)
		}
		write(`{"feed":"book_snapshot","product_id":"PF_XBTUSD","timestamp":1792334590406,"seq":161144096,"tickSize":null,"bids":[{"price":81000,"qty":1.352}],"asks":[{"price":81001,"qty":1.9948}]}`)
		write(`{"feed":"book","product_id":"PF_XBTUSD","side":"sell","seq":161144097,"price":81013,"qty":0.7,"timestamp":1792334590408}`)
		write(`{"product_id":"PF_XBTUSD","feed":"trade","uid":"7bd69fb5-e434-4e83-aac6-57fff0d95177","side":"buy","type":"fill","time":1792334591710,"qty":0.1338,"price":81001,"seq":638439}`)
		time.Sleep(time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make(chan Message, 4)
	go func() {
		_ = NewStream().WithURL(url).Run(ctx, []Subscription{{Feed: FeedBook, Products: []string{"PF_XBTUSD"}}, {Feed: FeedTrade, Products: []string{"PF_XBTUSD"}}},
			Handler{Message: func(m Message) { messages <- m }})
	}()
	var snap, delta BookMessage
	first, second, third := <-messages, <-messages, <-messages
	if !first.Snapshot || Decode(first.Raw, &snap) != nil || snap.Seq != 161144096 || snap.Bids[0].Price.String() != "81000" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if second.Snapshot || Decode(second.Raw, &delta) != nil || delta.Side != "sell" || delta.Seq != snap.Seq+1 {
		t.Fatalf("delta = %+v", delta)
	}
	var trade TradeMessage
	if third.Feed != FeedTrade || Decode(third.Raw, &trade) != nil || trade.Qty.String() != "0.1338" {
		t.Fatalf("trade = %+v", trade)
	}
}

func TestPrivateStreamRequiresCredentialsAndReportsErrors(t *testing.T) {
	if err := NewStream().Run(context.Background(), []Subscription{{Feed: FeedFills}}, Handler{}); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("err = %v", err)
	}
	url := fakeWS(t, func(read func() map[string]any, write func(string)) {
		read()
		write(`{"event":"challenge","message":"x"}`)
		read()
		write(`{"event":"alert","message":"Failed to subscribe to authenticated feed"}`)
		time.Sleep(time.Second)
	})
	err := NewStream().WithURL(url).WithCredentials("key", testSecret).Run(context.Background(), []Subscription{{Feed: FeedFills}}, Handler{})
	if !errors.Is(err, ErrChallengeRejected) {
		t.Fatalf("err = %v", err)
	}
}
