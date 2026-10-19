package gwclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Topic is one gateway WebSocket subscription.
type Topic struct {
	Channel    string `json:"ch"`
	Instrument string `json:"inst,omitempty"`
	Interval   string `json:"interval,omitempty"`
	Account    string `json:"account,omitempty"`
}

// Message is one pushed message. Account topics fan out as account.orders,
// account.fills, account.positions, account.balances and account.status.
type Message struct {
	Channel    string          `json:"ch"`
	Instrument string          `json:"inst,omitempty"`
	Account    string          `json:"account,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// Stream keeps one gateway WebSocket open, re-subscribing after every
// reconnect. Retained channels (book, ticker, funding, account.positions…)
// replay their current state on (re)subscribe, so consumers replace state.
type Stream struct {
	client   *Client
	messages chan Message
	states   chan bool

	mu     sync.Mutex
	topics map[Topic]struct{}
	conn   *websocket.Conn
}

func (client *Client) Stream() *Stream {
	return &Stream{client: client, messages: make(chan Message, 4096), states: make(chan bool, 16), topics: map[Topic]struct{}{}}
}

// Messages delivers every pushed message. It is never closed.
func (stream *Stream) Messages() <-chan Message { return stream.messages }

// Connected delivers connection state changes (true after each (re)connect).
func (stream *Stream) Connected() <-chan bool { return stream.states }

func (stream *Stream) Subscribe(topic Topic) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if _, exists := stream.topics[topic]; exists {
		return
	}
	stream.topics[topic] = struct{}{}
	stream.sendLocked("sub", topic)
}

func (stream *Stream) Unsubscribe(topic Topic) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if _, exists := stream.topics[topic]; !exists {
		return
	}
	delete(stream.topics, topic)
	stream.sendLocked("unsub", topic)
}

func (stream *Stream) sendLocked(op string, topic Topic) {
	if stream.conn == nil {
		return
	}
	payload, _ := json.Marshal(struct {
		Op string `json:"op"`
		Topic
	}{op, topic})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = stream.conn.Write(ctx, websocket.MessageText, payload)
}

// Run connects and reconnects until ctx ends.
func (stream *Stream) Run(ctx context.Context) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		started := time.Now()
		err := stream.session(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("gateway stream disconnected", "error", err)
		select {
		case stream.states <- false:
		default:
		}
		if time.Since(started) > time.Minute {
			backoff = 500 * time.Millisecond
		}
		jitter := time.Duration(rand.Int64N(int64(backoff/4) + 1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func (stream *Stream) session(ctx context.Context) error {
	target := *stream.client.base
	target.Scheme = map[string]string{"https": "wss"}[target.Scheme]
	if target.Scheme == "" {
		target.Scheme = "ws"
	}
	target.Path = stream.client.base.Path + "/v1/ws"
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, target.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + stream.client.token}},
	})
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)
	stream.mu.Lock()
	stream.conn = conn
	for topic := range stream.topics {
		stream.sendLocked("sub", topic)
	}
	stream.mu.Unlock()
	defer func() {
		stream.mu.Lock()
		stream.conn = nil
		stream.mu.Unlock()
	}()
	select {
	case stream.states <- true:
	default:
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var message Message
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if message.Channel == "error" {
			slog.Warn("gateway stream error", "data", string(message.Data))
			continue
		}
		select {
		case stream.messages <- message:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
