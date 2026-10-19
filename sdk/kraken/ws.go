package kraken

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

const DefaultWSURL = "wss://futures.kraken.com/ws/v1"

// Feeds. Snapshot messages arrive with the "_snapshot" suffix.
const (
	FeedBook         = "book"
	FeedTrade        = "trade"
	FeedTicker       = "ticker"
	FeedOpenOrders   = "open_orders"
	FeedFills        = "fills"
	FeedOpenPosition = "open_positions"
	FeedBalances     = "balances"
)

var (
	wsPingInterval = 30 * time.Second // Kraken drops idle connections after 60s
	wsReadTimeout  = 90 * time.Second
	wsAckTimeout   = 10 * time.Second
)

var ErrChallengeRejected = errors.New("kraken ws: challenge rejected")

// Message is one feed message. Raw is the full JSON object; Feed already has
// any "_snapshot" suffix removed and Snapshot records it.
type Message struct {
	Feed     string
	Snapshot bool
	Product  string
	Raw      json.RawMessage
}

// Stream is one Kraken Futures WebSocket connection for a fixed set of
// subscriptions. Run blocks for the life of the connection; reconnecting is
// the caller's job.
type Stream struct {
	url    string
	apiKey string
	secret string
}

func NewStream() *Stream { return &Stream{url: DefaultWSURL} }

func (s *Stream) WithURL(rawURL string) *Stream {
	s.url = rawURL
	return s
}

func (s *Stream) WithCredentials(apiKey, secret string) *Stream {
	s.apiKey, s.secret = apiKey, secret
	return s
}

// Subscription is one feed, with product ids for public feeds.
type Subscription struct {
	Feed     string
	Products []string
}

// Handler receives stream lifecycle and data on the read goroutine.
type Handler struct {
	Ready   func()
	Message func(Message)
}

// Run subscribes every subscription and delivers messages until ctx ends
// (returns nil) or the connection fails. Private feeds authenticate with a
// signed challenge first.
func (s *Stream) Run(ctx context.Context, subscriptions []Subscription, handler Handler) error {
	if len(subscriptions) == 0 {
		return fmt.Errorf("kraken ws: no subscriptions")
	}
	private := false
	for _, sub := range subscriptions {
		if isPrivateFeed(sub.Feed) {
			private = true
		}
	}
	if private && (s.apiKey == "" || s.secret == "") {
		return ErrCredentialsRequired
	}
	conn, err := dial(ctx, s.url)
	if err != nil {
		return fmt.Errorf("kraken ws: dial: %w", err)
	}
	session := newSession(conn)
	defer session.close()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-runCtx.Done()
		session.close()
	}()
	go session.pingLoop(runCtx)
	reads := make(chan readResult, 256)
	go session.readLoop(reads)

	var challenge, signed string
	if private {
		if err := session.write(map[string]string{"event": "challenge", "api_key": s.apiKey}); err != nil {
			return err
		}
	} else if err := s.subscribe(session, subscriptions, "", ""); err != nil {
		return err
	}
	pending := map[string]bool{}
	for _, sub := range subscriptions {
		pending[sub.Feed] = true
	}
	deadline := time.NewTimer(wsAckTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			if len(pending) > 0 {
				return fmt.Errorf("kraken ws: subscriptions not acknowledged: %v", keys(pending))
			}
		case result := <-reads:
			if result.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return result.err
			}
			var head struct {
				Event     string `json:"event"`
				Feed      string `json:"feed"`
				Message   string `json:"message"`
				ProductID string `json:"product_id"`
			}
			if json.Unmarshal(result.data, &head) != nil {
				return fmt.Errorf("kraken ws: malformed frame")
			}
			switch head.Event {
			case "info", "pong":
				continue
			case "challenge":
				if !private || head.Message == "" {
					continue
				}
				challenge = head.Message
				if signed, err = SignChallenge(s.secret, challenge); err != nil {
					return err
				}
				if err := s.subscribe(session, subscriptions, challenge, signed); err != nil {
					return err
				}
				deadline.Reset(wsAckTimeout)
				continue
			case "subscribed":
				if pending[head.Feed] {
					delete(pending, head.Feed)
					if len(pending) == 0 && handler.Ready != nil {
						handler.Ready()
					}
				}
				continue
			case "alert", "error":
				if strings.Contains(strings.ToLower(head.Message), "auth") || strings.Contains(strings.ToLower(head.Message), "challenge") {
					return fmt.Errorf("%w: %s", ErrChallengeRejected, head.Message)
				}
				return fmt.Errorf("kraken ws: %s %s", head.Event, head.Message)
			case "subscribed_failed":
				return fmt.Errorf("kraken ws: subscribe %s failed: %s", head.Feed, head.Message)
			}
			if head.Feed == "" || handler.Message == nil {
				continue
			}
			feed, snapshot := strings.CutSuffix(head.Feed, "_snapshot")
			if feed == "open_positions" {
				snapshot = true
			}
			handler.Message(Message{Feed: feed, Snapshot: snapshot, Product: head.ProductID, Raw: result.data})
		}
	}
}

func (s *Stream) subscribe(session *session, subscriptions []Subscription, challenge, signed string) error {
	for _, sub := range subscriptions {
		request := map[string]any{"event": "subscribe", "feed": sub.Feed}
		if len(sub.Products) > 0 {
			request["product_ids"] = sub.Products
		}
		if isPrivateFeed(sub.Feed) {
			request["api_key"] = s.apiKey
			request["original_challenge"] = challenge
			request["signed_challenge"] = signed
		}
		if err := session.write(request); err != nil {
			return fmt.Errorf("kraken ws: subscribe %s: %w", sub.Feed, err)
		}
	}
	return nil
}

func isPrivateFeed(feed string) bool {
	switch feed {
	case FeedOpenOrders, FeedFills, FeedOpenPosition, FeedBalances, "open_orders_verbose", "account_log", "notifications_auth":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Feed payloads (decode Message.Raw)
// ---------------------------------------------------------------------------

type BookLevel struct {
	Price json.Number `json:"price"`
	Qty   json.Number `json:"qty"`
}

// BookMessage covers book_snapshot (Bids/Asks) and book deltas (Side, Price,
// Qty; Qty 0 deletes the level). Seq is contiguous per product.
type BookMessage struct {
	ProductID string      `json:"product_id"`
	Timestamp int64       `json:"timestamp"`
	Seq       int64       `json:"seq"`
	Bids      []BookLevel `json:"bids"`
	Asks      []BookLevel `json:"asks"`
	Side      string      `json:"side"`
	Price     json.Number `json:"price"`
	Qty       json.Number `json:"qty"`
}

type TradeMessage struct {
	ProductID string      `json:"product_id"`
	UID       string      `json:"uid"`
	Side      string      `json:"side"`
	Type      string      `json:"type"`
	Time      int64       `json:"time"`
	Qty       json.Number `json:"qty"`
	Price     json.Number `json:"price"`
	Seq       int64       `json:"seq"`
}

// TradeSnapshot is trade_snapshot: recent trades.
type TradeSnapshot struct {
	ProductID string         `json:"product_id"`
	Trades    []TradeMessage `json:"trades"`
}

// TickerMessage carries mark, index, funding and open interest.
// RelativeFundingRate is the hourly rate as a fraction of position value.
type TickerMessage struct {
	ProductID           string      `json:"product_id"`
	Time                int64       `json:"time"`
	Bid                 json.Number `json:"bid"`
	Ask                 json.Number `json:"ask"`
	BidSize             json.Number `json:"bid_size"`
	AskSize             json.Number `json:"ask_size"`
	Last                json.Number `json:"last"`
	MarkPrice           json.Number `json:"markPrice"`
	Index               json.Number `json:"index"`
	OpenInterest        json.Number `json:"openInterest"`
	RelativeFundingRate json.Number `json:"relative_funding_rate"`
	NextFundingRateTime int64       `json:"next_funding_rate_time"`
	Volume              json.Number `json:"volume"`
}

// WSOrder is one order of open_orders. Direction 0 is buy, 1 is sell.
type WSOrder struct {
	Instrument     string      `json:"instrument"`
	Time           int64       `json:"time"`
	LastUpdateTime int64       `json:"last_update_time"`
	Qty            json.Number `json:"qty"`
	Filled         json.Number `json:"filled"`
	LimitPrice     json.Number `json:"limit_price"`
	StopPrice      json.Number `json:"stop_price"`
	Type           string      `json:"type"`
	OrderID        string      `json:"order_id"`
	ClientOrderID  string      `json:"cli_ord_id"`
	Direction      int         `json:"direction"`
	ReduceOnly     bool        `json:"reduce_only"`
}

// OpenOrdersMessage is open_orders_snapshot (Orders) or a delta (Order, or
// only OrderID when IsCancel removes the order).
type OpenOrdersMessage struct {
	Orders   []WSOrder `json:"orders"`
	Order    *WSOrder  `json:"order"`
	OrderID  string    `json:"order_id"`
	IsCancel bool      `json:"is_cancel"`
	Reason   string    `json:"reason"`
}

type WSFill struct {
	Instrument        string      `json:"instrument"`
	Time              int64       `json:"time"`
	Price             json.Number `json:"price"`
	Seq               int64       `json:"seq"`
	Buy               bool        `json:"buy"`
	Qty               json.Number `json:"qty"`
	RemainingOrderQty json.Number `json:"remaining_order_qty"`
	OrderID           string      `json:"order_id"`
	ClientOrderID     string      `json:"cli_ord_id"`
	FillID            string      `json:"fill_id"`
	FillType          string      `json:"fill_type"`
	FeePaid           json.Number `json:"fee_paid"`
	FeeCurrency       string      `json:"fee_currency"`
}

type FillsMessage struct {
	Fills []WSFill `json:"fills"`
}

// WSPosition is one open_positions row. Balance is signed size.
type WSPosition struct {
	Instrument           string      `json:"instrument"`
	Balance              json.Number `json:"balance"`
	PnL                  json.Number `json:"pnl"`
	EntryPrice           json.Number `json:"entry_price"`
	MarkPrice            json.Number `json:"mark_price"`
	LiquidationThreshold json.Number `json:"liquidation_threshold"`
	EffectiveLeverage    json.Number `json:"effective_leverage"`
	InitialMargin        json.Number `json:"initial_margin"`
	MaintenanceMargin    json.Number `json:"maintenance_margin"`
}

type PositionsMessage struct {
	Positions []WSPosition `json:"positions"`
}

// BalancesMessage carries the multi-collateral account under flex_futures.
type BalancesMessage struct {
	FlexFutures *struct {
		Currencies map[string]struct {
			Quantity  json.Number `json:"quantity"`
			Value     json.Number `json:"value"`
			Available json.Number `json:"available"`
		} `json:"currencies"`
		MarginEquity    json.Number `json:"margin_equity"`
		AvailableMargin json.Number `json:"available_margin"`
		InitialMargin   json.Number `json:"initial_margin"`
		PnL             json.Number `json:"pnl"`
	} `json:"flex_futures"`
	Timestamp int64 `json:"timestamp"`
}

// Decode decodes a message payload, preserving numbers.
func Decode(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode(out)
}

// ---------------------------------------------------------------------------
// Connection
// ---------------------------------------------------------------------------

type readResult struct {
	data []byte
	err  error
}

type session struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func newSession(conn *websocket.Conn) *session {
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	return &session{conn: conn}
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
			// Kraken expects an application-level ping to keep the session.
			if s.write(map[string]string{"event": "ping"}) != nil {
				s.close()
				return
			}
		}
	}
}

func (s *session) readLoop(out chan<- readResult) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			out <- readResult{err: fmt.Errorf("kraken ws: read: %w", err)}
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		out <- readResult{data: data}
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

func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

func dial(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Hostname() == "" {
		return nil, fmt.Errorf("invalid endpoint URL")
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	if loopback(target.Hostname()) {
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
		return nil, fmt.Errorf("kraken ws: invalid proxy URL")
	}
	return proxyURL, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
