package crossex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Public channels of wss://api.gateio.ws/ws/crossex/public. No login.
const (
	ChannelTicker          = "ticker"
	ChannelTrade           = "trade"
	ChannelFundingRate     = "funding_rate"
	ChannelMarkPrice       = "mark_price"
	ChannelOpenInterest    = "open_interest"
	ChannelOrderBookUpdate = "order_book_update"
)

// ChannelOrderBook is the limited-depth snapshot channel "order_book_<depth>".
// Supported depths differ per exchange (Binance 5/10/20, Deribit 1/5/10/20,
// Kraken none).
func ChannelOrderBook(depth int) string { return fmt.Sprintf("order_book_%d", depth) }

// ChannelKline is "kline_<interval>" for 1m, 5m, 15m, 30m, 1h, 4h, 1d.
func ChannelKline(interval string) string { return "kline_" + interval }

// PublicUpdate is one public push. Result is the channel's object.
type PublicUpdate struct {
	Channel string
	Result  json.RawMessage
	TimeMS  int64
}

// PublicBook is an order_book_<depth> snapshot or an order_book_update delta.
// Sizes are as the exchange quotes them.
type PublicBook struct {
	Symbol   string      `json:"s"`
	Snapshot bool        `json:"snapshot"`
	First    json.Number `json:"first"`
	Last     json.Number `json:"u"`
	Asks     [][2]string `json:"a"`
	Bids     [][2]string `json:"b"`
	Time     int64       `json:"ts"`
}

type PublicTrade struct {
	Symbol string `json:"s"`
	ID     string `json:"i"`
	Price  string `json:"p"`
	Qty    string `json:"q"`
	Side   string `json:"side"`
	Time   int64  `json:"ts"`
}

type PublicKline struct {
	Symbol string `json:"s"`
	Open   string `json:"o"`
	High   string `json:"h"`
	Low    string `json:"l"`
	Close  string `json:"c"`
	Volume string `json:"v"`
	Start  int64  `json:"t"`
	End    int64  `json:"end"`
	Closed bool   `json:"x"`
}

type PublicTicker struct {
	Symbol    string  `json:"s"`
	Last      string  `json:"lp"`
	BidPrice  string  `json:"bp"`
	BidSize   string  `json:"bs"`
	AskPrice  string  `json:"ap"`
	AskSize   string  `json:"as"`
	Open24h   *string `json:"o"`
	High24h   *string `json:"h"`
	Low24h    *string `json:"l"`
	Volume24h *string `json:"v"`
	Time      int64   `json:"ts"`
}

type PublicFunding struct {
	Symbol string `json:"s"`
	Rate   string `json:"r"`
	Next   *int64 `json:"end"`
}

type PublicMark struct {
	Symbol string `json:"s"`
	Price  string `json:"mp"`
}

type PublicOpenInterest struct {
	Symbol string `json:"s"`
	Value  string `json:"oi"`
}

// PublicStream is one public connection serving any number of (channel,
// symbol) topics. Subscribing a topic that is already open shares it.
// It reconnects on its own and resubscribes every open topic.
type PublicStream struct {
	url string

	mu       sync.Mutex
	topics   map[publicTopic]map[int]func(PublicUpdate)
	failures map[publicTopic][]func(error)
	nextID   int
	session  *wsSession
	cancel   context.CancelFunc
	running  bool
	closed   bool
}

type publicTopic struct{ channel, symbol string }

func NewPublicStream() *PublicStream {
	return &PublicStream{url: DefaultPublicWSURL, topics: map[publicTopic]map[int]func(PublicUpdate){}, failures: map[publicTopic][]func(error){}}
}

func (s *PublicStream) WithURL(rawURL string) *PublicStream {
	s.url = rawURL
	return s
}

// Subscribe adds handler for (channel, symbol). onError receives a
// subscription refusal (unsupported channel for the exchange) and is called
// at most once. The returned func removes the handler.
func (s *PublicStream) Subscribe(channel, symbol string, handler func(PublicUpdate), onError func(error)) (func(), error) {
	if strings.TrimSpace(channel) == "" || strings.TrimSpace(symbol) == "" || handler == nil {
		return nil, fmt.Errorf("crossex public: channel, symbol and handler are required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("crossex public: stream is closed")
	}
	topic := publicTopic{channel, symbol}
	handlers := s.topics[topic]
	first := handlers == nil
	if first {
		handlers = map[int]func(PublicUpdate){}
		s.topics[topic] = handlers
	}
	s.nextID++
	id := s.nextID
	handlers[id] = handler
	if onError != nil {
		s.failures[topic] = append(s.failures[topic], onError)
	}
	session := s.session
	if !s.running {
		s.running = true
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.run(ctx)
	}
	s.mu.Unlock()
	if first && session != nil {
		_ = session.write(publicRequest("subscribe", channel, symbol))
	}
	return func() { s.unsubscribe(topic, id) }, nil
}

func (s *PublicStream) unsubscribe(topic publicTopic, id int) {
	s.mu.Lock()
	handlers := s.topics[topic]
	delete(handlers, id)
	last := handlers != nil && len(handlers) == 0
	if last {
		delete(s.topics, topic)
		delete(s.failures, topic)
	}
	session := s.session
	s.mu.Unlock()
	if last && session != nil {
		_ = session.write(publicRequest("unsubscribe", topic.channel, topic.symbol))
	}
}

// Close ends the connection and every topic.
func (s *PublicStream) Close() error {
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func publicRequest(event, channel, symbol string) wsRequest {
	return wsRequest{Time: time.Now().Unix(), Channel: channel, Event: event, Payload: []string{symbol}}
}

func (s *PublicStream) run(ctx context.Context) {
	delays := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	attempt := 0
	for ctx.Err() == nil {
		healthy := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if healthy {
			attempt = 0
		}
		delay := delays[min(attempt, len(delays)-1)]
		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// runOnce serves one connection. It reports whether the connection carried
// data, which resets the reconnect backoff.
func (s *PublicStream) runOnce(ctx context.Context) bool {
	conn, err := dialWebSocket(ctx, s.url)
	if err != nil {
		return false
	}
	session := newWSSession(conn)
	defer session.close()
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		session.close()
	}()
	go session.pingLoop(connCtx)

	s.mu.Lock()
	s.session = session
	topics := make([]publicTopic, 0, len(s.topics))
	for topic := range s.topics {
		topics = append(topics, topic)
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.session == session {
			s.session = nil
		}
		s.mu.Unlock()
	}()
	for _, topic := range topics {
		if session.write(publicRequest("subscribe", topic.channel, topic.symbol)) != nil {
			return false
		}
	}

	reads := make(chan wsResult, 256)
	go session.readLoop(reads)
	healthy := false
	for {
		select {
		case <-ctx.Done():
			return healthy
		case result := <-reads:
			if result.err != nil {
				return healthy
			}
			message := result.message
			if message.Event == "subscribe" {
				if failure := message.failure(); failure != "" {
					s.refuse(message, failure)
				}
				continue
			}
			if message.Event != "update" || len(message.raw) == 0 {
				continue
			}
			symbol := publicSymbol(message.raw)
			if symbol == "" {
				continue
			}
			healthy = true
			s.mu.Lock()
			handlers := make([]func(PublicUpdate), 0, len(s.topics[publicTopic{message.Channel, symbol}]))
			for _, handler := range s.topics[publicTopic{message.Channel, symbol}] {
				handlers = append(handlers, handler)
			}
			s.mu.Unlock()
			update := PublicUpdate{Channel: message.Channel, Result: message.raw, TimeMS: message.TimeMS}
			for _, handler := range handlers {
				handler(update)
			}
		}
	}
}

// publicSymbol reads the "s" key exactly. CrossEx public objects use keys
// that differ only by case ("s" symbol, "S" side; "t"/"T" kline times), and
// encoding/json matches field names case-insensitively, so a struct decode
// could take the side for the symbol.
func publicSymbol(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	var symbol string
	if json.Unmarshal(fields["s"], &symbol) != nil {
		return ""
	}
	return symbol
}

// DecodePublic decodes a public result into out using exact key matching.
// Every Public* type is decoded with it; plain json.Unmarshal would let "S"
// (side) overwrite "s" (symbol) and "T" (end time) overwrite "t".
func DecodePublic(raw json.RawMessage, out any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	exact := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		exact[exactKey(key)] = value
	}
	data, err := json.Marshal(exact)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// exactKey rewrites the case-colliding keys to unambiguous names that the
// Public* struct tags use.
func exactKey(key string) string {
	switch key {
	case "S":
		return "side"
	case "T":
		return "end"
	case "U":
		return "first"
	}
	return key
}

// refuse reports a subscription refusal to the topics it names and drops
// them: CrossEx will not serve them on this connection or the next.
func (s *PublicStream) refuse(message wsMessage, failure string) {
	var symbols []string
	_ = json.Unmarshal(message.Payload, &symbols)
	err := fmt.Errorf("crossex public: %s %s", message.Channel, failure)
	s.mu.Lock()
	var notify []func(error)
	for _, symbol := range symbols {
		topic := publicTopic{message.Channel, symbol}
		notify = append(notify, s.failures[topic]...)
		delete(s.failures, topic)
		delete(s.topics, topic)
	}
	s.mu.Unlock()
	for _, fn := range notify {
		fn(err)
	}
}
