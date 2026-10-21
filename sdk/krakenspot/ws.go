package krakenspot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	wsPingInterval = 20 * time.Second
	wsReadTimeout  = 60 * time.Second
	wsAckTimeout   = 10 * time.Second
)

var ErrSubscriptionRejected = errors.New("kraken spot ws: subscription rejected")

// Subscription is one v2 subscribe request: channel with its parameters.
// Private channels (executions, balances) take the token from Stream.
type Subscription struct {
	Channel string
	Symbols []string
	Params  map[string]any
}

// Message is one channel push. Type is snapshot or update.
type Message struct {
	Channel string
	Type    string
	Data    json.RawMessage
}

type Handler struct {
	Ready   func()
	Message func(Message)
}

// Stream is one v2 connection for a fixed set of subscriptions; Run blocks
// for its life and reconnecting is the caller's job.
type Stream struct {
	url   string
	token func(context.Context) (string, error)
}

func NewStream() *Stream { return &Stream{url: DefaultWSURL} }

func (s *Stream) WithURL(rawURL string) *Stream {
	s.url = rawURL
	return s
}

// WithToken makes the stream private: token is fetched on every connect
// (GetWebSocketsToken tokens expire 15 minutes after issue unless used).
func (s *Stream) WithToken(token func(context.Context) (string, error)) *Stream {
	s.token = token
	return s
}

// Run subscribes every subscription and delivers channel pushes until ctx
// ends (nil) or the connection fails.
func (s *Stream) Run(ctx context.Context, subscriptions []Subscription, handler Handler) error {
	if len(subscriptions) == 0 {
		return fmt.Errorf("kraken spot ws: no subscriptions")
	}
	token := ""
	if s.token != nil {
		var err error
		if token, err = s.token(ctx); err != nil {
			return err
		}
	}
	conn, err := dial(ctx, s.url)
	if err != nil {
		return fmt.Errorf("kraken spot ws: dial: %w", err)
	}
	session := &session{conn: conn}
	defer session.close()
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-runCtx.Done()
		session.close()
	}()
	go session.pingLoop(runCtx)
	reads := make(chan wsRead, 256)
	go session.readLoop(reads)

	pending := map[int]bool{}
	for index, sub := range subscriptions {
		request := map[string]any{"channel": sub.Channel}
		for key, value := range sub.Params {
			request[key] = value
		}
		if len(sub.Symbols) > 0 {
			request["symbol"] = sub.Symbols
		}
		if token != "" {
			request["token"] = token
		}
		id := index + 1
		pending[id] = true
		if err := session.write(map[string]any{"method": "subscribe", "params": request, "req_id": id}); err != nil {
			return err
		}
	}
	// A multi-symbol subscribe is acknowledged once per symbol; count acks
	// per request and wait for every symbol.
	expected := map[int]int{}
	for index, sub := range subscriptions {
		expected[index+1] = max(1, len(sub.Symbols))
	}
	deadline := time.NewTimer(wsAckTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			if len(pending) > 0 {
				return fmt.Errorf("kraken spot ws: subscriptions not acknowledged")
			}
		case read := <-reads:
			if read.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return read.err
			}
			var head struct {
				Method  string          `json:"method"`
				Success *bool           `json:"success"`
				Error   string          `json:"error"`
				ReqID   int             `json:"req_id"`
				Channel string          `json:"channel"`
				Type    string          `json:"type"`
				Data    json.RawMessage `json:"data"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(read.data)))
			decoder.UseNumber()
			if decoder.Decode(&head) != nil {
				return fmt.Errorf("kraken spot ws: malformed frame")
			}
			if head.Method == "subscribe" {
				if head.Success != nil && !*head.Success {
					return fmt.Errorf("%w: %s", ErrSubscriptionRejected, head.Error)
				}
				if pending[head.ReqID] {
					expected[head.ReqID]--
					if expected[head.ReqID] <= 0 {
						delete(pending, head.ReqID)
						if len(pending) == 0 && handler.Ready != nil {
							handler.Ready()
						}
					}
				}
				continue
			}
			if head.Method != "" || head.Channel == "" || head.Channel == "heartbeat" || head.Channel == "status" {
				continue
			}
			if handler.Message != nil {
				handler.Message(Message{Channel: head.Channel, Type: head.Type, Data: head.Data})
			}
		}
	}
}

// Decode decodes a push payload preserving numbers.
func Decode(raw json.RawMessage, out any) error { return decodeNumbers(raw, out) }

// ---------------------------------------------------------------------------
// Channel payloads
// ---------------------------------------------------------------------------

type BookLevel struct {
	Price json.Number `json:"price"`
	Qty   json.Number `json:"qty"`
}

type Book struct {
	Symbol    string      `json:"symbol"`
	Bids      []BookLevel `json:"bids"`
	Asks      []BookLevel `json:"asks"`
	Checksum  uint32      `json:"checksum"`
	Timestamp string      `json:"timestamp"`
}

type Ticker struct {
	Symbol string      `json:"symbol"`
	Bid    json.Number `json:"bid"`
	BidQty json.Number `json:"bid_qty"`
	Ask    json.Number `json:"ask"`
	AskQty json.Number `json:"ask_qty"`
	Last   json.Number `json:"last"`
	Volume json.Number `json:"volume"`
	Change json.Number `json:"change_pct"`
}

type Trade struct {
	Symbol    string      `json:"symbol"`
	Side      string      `json:"side"`
	Price     json.Number `json:"price"`
	Qty       json.Number `json:"qty"`
	TradeID   json.Number `json:"trade_id"`
	Timestamp string      `json:"timestamp"`
}

type Candle struct {
	Symbol        string      `json:"symbol"`
	Open          json.Number `json:"open"`
	High          json.Number `json:"high"`
	Low           json.Number `json:"low"`
	Close         json.Number `json:"close"`
	Volume        json.Number `json:"volume"`
	IntervalBegin string      `json:"interval_begin"`
	Interval      int         `json:"interval"`
}

// Execution is one executions push: an order state change, or a trade
// (ExecType "trade") carrying the fill.
type Execution struct {
	OrderID       string      `json:"order_id"`
	ClientOrderID string      `json:"cl_ord_id"`
	ExecID        string      `json:"exec_id"`
	ExecType      string      `json:"exec_type"`
	OrderStatus   string      `json:"order_status"`
	Symbol        string      `json:"symbol"`
	Side          string      `json:"side"`
	OrderType     string      `json:"order_type"`
	OrderQty      json.Number `json:"order_qty"`
	LimitPrice    json.Number `json:"limit_price"`
	TimeInForce   string      `json:"time_in_force"`
	PostOnly      bool        `json:"post_only"`
	CumQty        json.Number `json:"cum_qty"`
	AvgPrice      json.Number `json:"avg_price"`
	LastQty       json.Number `json:"last_qty"`
	LastPrice     json.Number `json:"last_price"`
	LiquidityInd  string      `json:"liquidity_ind"` // m, t
	Fees          []struct {
		Asset string      `json:"asset"`
		Qty   json.Number `json:"qty"`
	} `json:"fees"`
	Reason    string `json:"reason"`
	Timestamp string `json:"timestamp"`
}

// BalanceRow is a balances snapshot row (Wallets) or an update (Balance is
// the new total of the wallet named by WalletType/WalletID).
type BalanceRow struct {
	Asset      string      `json:"asset"`
	Balance    json.Number `json:"balance"`
	WalletType string      `json:"wallet_type"`
	Wallets    []struct {
		Type    string      `json:"type"`
		ID      string      `json:"id"`
		Balance json.Number `json:"balance"`
	} `json:"wallets"`
}

// ParseTime parses a v2 RFC 3339 timestamp.
func ParseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// ---------------------------------------------------------------------------
// Connection
// ---------------------------------------------------------------------------

type wsRead struct {
	data []byte
	err  error
}

type session struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func (s *session) write(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(value)
}

func (s *session) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.write(map[string]string{"method": "ping"}) != nil {
				s.close()
				return
			}
		}
	}
}

func (s *session) readLoop(out chan<- wsRead) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			out <- wsRead{err: fmt.Errorf("kraken spot ws: read: %w", err)}
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		out <- wsRead{data: data}
	}
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.writeMu.Lock()
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		s.writeMu.Unlock()
		_ = s.conn.Close()
	})
}

func dial(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Hostname() == "" {
		return nil, fmt.Errorf("invalid endpoint URL")
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	if host := target.Hostname(); strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		dialer.Proxy = nil
	} else {
		dialer.Proxy = proxy
	}
	conn, _, err := dialer.DialContext(ctx, rawURL, nil)
	return conn, err
}

func proxy(request *http.Request) (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	}
	if raw == "" {
		return http.ProxyFromEnvironment(request)
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Host == "" {
		return nil, fmt.Errorf("kraken spot ws: invalid proxy URL")
	}
	return proxyURL, nil
}
