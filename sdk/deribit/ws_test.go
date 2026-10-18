package deribit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
			var frame map[string]any
			if conn.ReadJSON(&frame) != nil {
				return nil
			}
			return frame
		}
		script(read, func(frame string) { _ = conn.WriteMessage(websocket.TextMessage, []byte(frame)) })
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// Notification frames follow wss://www.deribit.com/ws/api/v2 captures from
// 2026-10-18.
func TestPrivateStreamAuthenticatesHeartbeatsAndDelivers(t *testing.T) {
	answeredTest := make(chan struct{})
	url := fakeWS(t, func(read func() map[string]any, write func(string)) {
		auth := read()
		params, _ := auth["params"].(map[string]any)
		if auth["method"] != "public/auth" || params["client_secret"] != "secret" {
			return
		}
		write(`{"jsonrpc":"2.0","id":1,"result":{"access_token":"tok","expires_in":900}}`)
		if read()["method"] != "public/set_heartbeat" {
			return
		}
		write(`{"jsonrpc":"2.0","id":2,"result":"ok"}`)
		sub := read()
		if sub["method"] != "private/subscribe" {
			return
		}
		write(`{"jsonrpc":"2.0","id":3,"result":["user.orders.future.USDC.raw","user.trades.future.USDC.raw"]}`)
		write(`{"jsonrpc":"2.0","method":"heartbeat","params":{"type":"test_request"}}`)
		if read()["method"] == "public/test" {
			close(answeredTest)
		}
		write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"user.orders.future.USDC.raw","data":{"time_in_force":"good_til_cancelled","reduce_only":false,"price":60000.0,"post_only":true,"order_type":"limit","order_state":"open","order_id":"USDC-123","label":"bt-1","instrument_name":"BTC_USDC-PERPETUAL","filled_amount":0.0,"direction":"buy","creation_timestamp":1792334590406,"last_update_timestamp":1792334590406,"average_price":0.0,"amount":0.001}}}`)
		time.Sleep(time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	notes := make(chan Notification, 2)
	done := make(chan error, 1)
	go func() {
		done <- NewStream().WithURL(url).WithCredentials("id", "secret").Run(ctx,
			[]string{"user.orders.future.USDC.raw", "user.trades.future.USDC.raw"},
			Handler{Ready: func() { close(ready) }, Notification: func(n Notification) { notes <- n }})
	}()
	<-ready
	<-answeredTest
	note := <-notes
	var order Order
	if note.Channel != "user.orders.future.USDC.raw" || Decode(note.Data, &order) != nil || order.Label != "bt-1" || order.Amount.String() != "0.001" || order.LimitPrice() != "60000.0" {
		t.Fatalf("note = %+v order=%+v", note, order)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPublicStreamDecodesBook(t *testing.T) {
	url := fakeWS(t, func(read func() map[string]any, write func(string)) {
		read()
		write(`{"jsonrpc":"2.0","id":2,"result":"ok"}`)
		if read()["method"] != "public/subscribe" {
			return
		}
		write(`{"jsonrpc":"2.0","id":3,"result":["book.BTC_USDC-PERPETUAL.100ms"]}`)
		write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"book.BTC_USDC-PERPETUAL.100ms","data":{"type":"snapshot","timestamp":1792334631847,"instrument_name":"BTC_USDC-PERPETUAL","change_id":91540452512,"bids":[["new",81056.4,0.161]],"asks":[["new",81060.3,0.484]]}}}`)
		write(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"book.BTC_USDC-PERPETUAL.100ms","data":{"type":"change","timestamp":1792334631947,"prev_change_id":91540452512,"instrument_name":"BTC_USDC-PERPETUAL","change_id":91540452530,"bids":[["delete",81056.4,0.0]],"asks":[]}}}`)
		time.Sleep(time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notes := make(chan Notification, 2)
	go func() {
		_ = NewStream().WithURL(url).Run(ctx, []string{"book.BTC_USDC-PERPETUAL.100ms"}, Handler{Notification: func(n Notification) { notes <- n }})
	}()
	var snapshot, change BookChange
	if Decode((<-notes).Data, &snapshot) != nil || snapshot.Type != "snapshot" || string(snapshot.Bids[0][0]) != `"new"` || string(snapshot.Bids[0][1]) != "81056.4" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if Decode((<-notes).Data, &change) != nil || change.PrevChangeID != snapshot.ChangeID || string(change.Bids[0][0]) != `"delete"` {
		t.Fatalf("change = %+v", change)
	}
}

func TestStreamAuthErrors(t *testing.T) {
	if err := NewStream().Run(context.Background(), []string{"user.orders.future.USDC.raw"}, Handler{}); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("err = %v", err)
	}
	url := fakeWS(t, func(read func() map[string]any, write func(string)) {
		read()
		write(`{"jsonrpc":"2.0","id":1,"error":{"message":"invalid_credentials","code":13004}}`)
		time.Sleep(time.Second)
	})
	err := NewStream().WithURL(url).WithCredentials("id", "bad").Run(context.Background(), []string{"user.orders.future.USDC.raw"}, Handler{})
	if !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("err = %v", err)
	}
}
