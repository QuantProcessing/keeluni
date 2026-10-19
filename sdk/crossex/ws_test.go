package crossex

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

// fakeServer plays CrossEx's private endpoint. script runs after the upgrade
// with a reader of client frames and a writer of raw server frames.
func fakeServer(t *testing.T, script func(read func() map[string]any, write func(string))) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		read := func() map[string]any {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return nil
			}
			var frame map[string]any
			_ = json.Unmarshal(data, &frame)
			return frame
		}
		write := func(frame string) { _ = conn.WriteMessage(websocket.TextMessage, []byte(frame)) }
		script(read, write)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func newTestStream(url string) *PrivateStream {
	return NewPrivateStream("key", "secret").WithURL(url).WithClock(func() time.Time { return time.Unix(1754272114, 0) })
}

const loginOK = `{"event":"login","request_id":"login","payload":{"conn_id":"c1"},"result":{"code":"100000","message":"success"},"time":1756434168,"time_ms":1756434168928}`

func ack(channel string) string {
	return `{"channel":"` + channel + `","event":"subscribe","payload":["!all"],"result":{"code":"100000","message":"success"},"time":1756434168,"time_ms":1756434168929}`
}

func TestPrivateStreamLogsInSubscribesAndDeliversPushes(t *testing.T) {
	var mu sync.Mutex
	var login map[string]any
	var subscriptions []map[string]any
	url := fakeServer(t, func(read func() map[string]any, write func(string)) {
		frame := read()
		mu.Lock()
		login = frame
		mu.Unlock()
		write(loginOK)
		for i := 0; i < 2; i++ {
			frame := read()
			mu.Lock()
			subscriptions = append(subscriptions, frame)
			mu.Unlock()
		}
		// A push may arrive between acknowledgements; both use event=subscribe.
		write(ack(ChannelOrder))
		write(`{"channel":"order","event":"subscribe","payload":{"order_id":"2072652940337152","text":"77","state":"FAIL",
			"symbol":"OKX_FUTURE_ADA_USDT","side":"BUY","type":"MARKET","attribute":"COMMON","exchange_type":"OKX",
			"business_type":"FUTURE","qty":"10","reason":"{\"label\":\"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR\",\"message\":\"Insufficient availableMargin\"}",
			"update_time":"1756434169098"},"result":{"code":"100000","message":"success"},"time":1756434168,"time_ms":1756434168968}`)
		write(ack(ChannelPosition))
		write(`{"channel":"position","event":"subscribe","payload":{"symbol":"OKX_FUTURE_ADA_USDT","position_side":"NONE",
			"position_qty":"0","margin_mode":"CROSS","update_time":"1756434169098"},"result":{"code":"100000","message":"success"},"time":1756434168,"time_ms":1756434168984}`)
		time.Sleep(time.Second)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	pushes := make(chan Push, 4)
	done := make(chan error, 1)
	go func() {
		done <- newTestStream(url).Run(ctx, []string{ChannelOrder, ChannelPosition}, StreamHandler{
			Ready: func() { close(ready) },
			Push:  func(push Push) { pushes <- push },
		})
	}()

	first := <-pushes
	if first.Channel != ChannelOrder {
		t.Fatalf("first push = %+v", first)
	}
	order, err := DecodeOrder(first.Payload)
	if err != nil || order.State != StateFail || order.Text != "77" {
		t.Fatalf("order = %+v, %v", order, err)
	}
	if reason, ok := order.FailReason(); !ok || reason.Label != "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR" {
		t.Fatalf("fail reason = %+v", reason)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("not ready after every channel was acknowledged")
	}
	second := <-pushes
	position, err := DecodePosition(second.Payload)
	if second.Channel != ChannelPosition || err != nil || position.Symbol != "OKX_FUTURE_ADA_USDT" || second.TimeMS != 1756434168984 {
		t.Fatalf("second push = %+v %+v", second, position)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run after cancel = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	payload, _ := login["payload"].(map[string]any)
	if login["event"] != "login" || payload["method"] != "api_key" || payload["api_key"] != "key" ||
		payload["sign"] != sign("secret", "channel=&event=login&time=1754272114") {
		t.Fatalf("login frame = %v", login)
	}
	if len(subscriptions) != 2 {
		t.Fatalf("subscriptions = %v", subscriptions)
	}
	for i, channel := range []string{ChannelOrder, ChannelPosition} {
		frame := subscriptions[i]
		all, _ := frame["payload"].([]any)
		if frame["event"] != "subscribe" || frame["channel"] != channel || len(all) != 1 || all[0] != "!all" {
			t.Fatalf("subscription %d = %v", i, frame)
		}
	}
}

func TestPrivateStreamLoginRejection(t *testing.T) {
	url := fakeServer(t, func(read func() map[string]any, write func(string)) {
		read()
		write(`{"event":"login","result":{"code":"100006","message":"Login failed"},"time":1756434168}`)
		time.Sleep(time.Second)
	})
	err := newTestStream(url).Run(context.Background(), []string{ChannelOrder}, StreamHandler{})
	if !errors.Is(err, ErrWSLogin) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrivateStreamSubscriptionFailure(t *testing.T) {
	url := fakeServer(t, func(read func() map[string]any, write func(string)) {
		read()
		write(loginOK)
		read()
		write(`{"channel":"order","event":"subscribe","payload":["!all"],"result":{"code":"100001","label":"COMMON_PARAM_BIND_ERROR","message":"Invalid Parameter"},"time":1756434168}`)
		time.Sleep(time.Second)
	})
	err := newTestStream(url).Run(context.Background(), []string{ChannelOrder}, StreamHandler{})
	if err == nil || !strings.Contains(err.Error(), "COMMON_PARAM_BIND_ERROR") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrivateStreamReportsDisconnect(t *testing.T) {
	url := fakeServer(t, func(read func() map[string]any, write func(string)) {
		read()
		write(loginOK)
		read()
		write(ack(ChannelOrder))
		// return: the server closes the connection
	})
	ready := make(chan struct{})
	err := newTestStream(url).Run(context.Background(), []string{ChannelOrder}, StreamHandler{Ready: func() { close(ready) }})
	select {
	case <-ready:
	default:
		t.Fatal("ready not signalled before disconnect")
	}
	if err == nil {
		t.Fatal("disconnect returned nil")
	}
}

func TestPrivateStreamLoginTimeout(t *testing.T) {
	previous := wsAckTimeout
	wsAckTimeout = 200 * time.Millisecond
	defer func() { wsAckTimeout = previous }()
	url := fakeServer(t, func(read func() map[string]any, write func(string)) {
		read()
		time.Sleep(time.Second)
	})
	err := newTestStream(url).Run(context.Background(), []string{ChannelOrder}, StreamHandler{})
	if err == nil || !strings.Contains(err.Error(), "login not acknowledged") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrivateStreamRequiresCredentials(t *testing.T) {
	if err := NewPrivateStream("", "").Run(context.Background(), []string{ChannelOrder}, StreamHandler{}); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestMessageClassification(t *testing.T) {
	cases := []struct {
		frame       string
		ack, push   bool
		failureLike string
	}{
		{ack(ChannelOrder), true, false, ""},
		{`{"channel":"order","event":"subscribe","payload":{"order_id":"1"}}`, false, true, ""},
		{`{"channel":"ticker","event":"update","result":{"s":"X"}}`, false, false, ""},
		{`{"channel":"ticker","event":"subscribe","result":{"status":"failed"},"error":{"message":"invalid symbol"}}`, false, false, "invalid symbol"},
		{`{"channel":"not_exist","event":"api","result":{"code":"100001","label":"COMMON_PARAM_BIND_ERROR","message":"Invalid"}}`, false, false, "COMMON_PARAM_BIND_ERROR"},
	}
	for _, tc := range cases {
		var message wsMessage
		if err := json.Unmarshal([]byte(tc.frame), &message); err != nil {
			t.Fatal(err)
		}
		if message.isAck() != tc.ack || message.isPush() != tc.push || !strings.Contains(message.failure(), tc.failureLike) ||
			(tc.failureLike == "" && message.failure() != "") {
			t.Fatalf("%s: ack=%v push=%v failure=%q", tc.frame, message.isAck(), message.isPush(), message.failure())
		}
	}
}
