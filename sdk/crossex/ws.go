package crossex

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

const (
	DefaultPrivateWSURL = "wss://api.gateio.ws/ws/crossex"
	DefaultPublicWSURL  = "wss://api.gateio.ws/ws/crossex/public"

	// Private channels used by account streams. Each is subscribed with "!all".
	ChannelOrder      = "order"
	ChannelUserTrades = "usertrades"
	ChannelPosition   = "position"
	ChannelAsset      = "asset"

	wsSuccessCode = "100000"
)

// The server drops a connection that sends no protocol Ping for 30 seconds.
var (
	wsPingInterval = 15 * time.Second
	wsReadTimeout  = 60 * time.Second
	wsAckTimeout   = 10 * time.Second
)

// ErrWSLogin is returned when the server rejects the login.
var ErrWSLogin = errors.New("crossex ws: login rejected")

// PrivateStream is one authenticated CrossEx WebSocket connection. Run blocks
// for the life of the connection; reconnecting is the caller's job, so each
// call to Run is a fresh login and a fresh subscription set.
type PrivateStream struct {
	url       string
	apiKey    string
	secretKey string
	now       func() time.Time
	dialer    func(ctx context.Context, rawURL string) (*websocket.Conn, error)
}

func NewPrivateStream(apiKey, secretKey string) *PrivateStream {
	return &PrivateStream{url: DefaultPrivateWSURL, apiKey: apiKey, secretKey: secretKey, now: time.Now, dialer: dialWebSocket}
}

func (s *PrivateStream) WithURL(rawURL string) *PrivateStream {
	s.url = rawURL
	return s
}

func (s *PrivateStream) WithClock(now func() time.Time) *PrivateStream {
	if now != nil {
		s.now = now
	}
	return s
}

// Push is one data message. Payload is the channel's object (Order, Trade,
// Position or AccountAsset); decode it with the matching Decode* helper.
type Push struct {
	Channel string
	Payload json.RawMessage
	TimeMS  int64
}

// StreamHandler receives stream lifecycle and data. Ready is called once all
// channels are subscribed; pushes may arrive before Ready only for channels
// already acknowledged. Handlers run on the read goroutine and must not block.
type StreamHandler struct {
	Ready func()
	Push  func(Push)
}

// Run connects, logs in, subscribes channels with "!all", then delivers pushes
// until ctx ends (returns nil) or the connection fails (returns the cause).
func (s *PrivateStream) Run(ctx context.Context, channels []string, handler StreamHandler) error {
	if s.apiKey == "" || s.secretKey == "" {
		return ErrCredentialsRequired
	}
	if len(channels) == 0 {
		return fmt.Errorf("crossex ws: at least one channel is required")
	}
	conn, err := s.dialer(ctx, s.url)
	if err != nil {
		return fmt.Errorf("crossex ws: dial: %w", err)
	}
	session := newWSSession(conn)
	defer session.close()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-runCtx.Done()
		session.close()
	}()
	go session.pingLoop(runCtx)

	reads := make(chan wsResult, 64)
	go session.readLoop(reads)

	ts := s.now().Unix()
	login := wsRequest{Time: ts, Event: "login", RequestID: "login", Payload: wsLogin{
		Method: "api_key", APIKey: s.apiKey, Sign: sign(s.secretKey, wsSigningPayload("", "login", ts)),
	}}
	if err := session.write(login); err != nil {
		return fmt.Errorf("crossex ws: login: %w", err)
	}

	pending := map[string]bool{}
	loggedIn := false
	deadline := time.NewTimer(wsAckTimeout)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			if !loggedIn {
				return fmt.Errorf("crossex ws: login not acknowledged")
			}
			if len(pending) > 0 {
				return fmt.Errorf("crossex ws: subscriptions not acknowledged: %s", strings.Join(keys(pending), ","))
			}
		case result := <-reads:
			if result.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return result.err
			}
			message := result.message
			switch {
			case message.Event == "login":
				if message.Result.Code != wsSuccessCode {
					return fmt.Errorf("%w: %s %s", ErrWSLogin, message.Result.Code, message.Result.Message)
				}
				loggedIn = true
				for _, channel := range channels {
					pending[channel] = true
					request := wsRequest{Time: s.now().Unix(), Channel: channel, Event: "subscribe", RequestID: "sub-" + channel, Payload: []string{"!all"}}
					if err := session.write(request); err != nil {
						return fmt.Errorf("crossex ws: subscribe %s: %w", channel, err)
					}
				}
				deadline.Reset(wsAckTimeout)
			case message.isAck():
				// Acknowledgements and pushes share event="subscribe"; an
				// acknowledgement's payload is the echoed symbol array.
				if failure := message.failure(); failure != "" {
					return fmt.Errorf("crossex ws: subscribe %s failed: %s", message.Channel, failure)
				}
				if pending[message.Channel] {
					delete(pending, message.Channel)
					if len(pending) == 0 && handler.Ready != nil {
						handler.Ready()
					}
				}
			case message.isPush():
				if handler.Push != nil {
					handler.Push(Push{Channel: message.Channel, Payload: message.Payload, TimeMS: message.TimeMS})
				}
			default:
				if failure := message.failure(); failure != "" {
					return fmt.Errorf("crossex ws: %s %s: %s", message.Event, message.Channel, failure)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Wire
// ---------------------------------------------------------------------------

type wsRequest struct {
	Time      int64  `json:"time"`
	Channel   string `json:"channel,omitempty"`
	Event     string `json:"event"`
	RequestID string `json:"request_id,omitempty"`
	Payload   any    `json:"payload"`
}

type wsLogin struct {
	Method string `json:"method"`
	APIKey string `json:"api_key"`
	Sign   string `json:"sign"`
}

type wsResultBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Label   string `json:"label"`
	Status  string `json:"status"`
}

type wsMessage struct {
	Time      int64           `json:"time"`
	TimeMS    int64           `json:"time_ms"`
	Channel   string          `json:"channel"`
	Event     string          `json:"event"`
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"payload"`
	Result    wsResultBody    `json:"result"`
	Error     json.RawMessage `json:"error"`
}

func (m wsMessage) payloadKind() byte {
	for _, c := range m.Payload {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c
		}
	}
	return 0
}

func (m wsMessage) isAck() bool {
	return (m.Event == "subscribe" || m.Event == "unsubscribe") && m.payloadKind() == '['
}

func (m wsMessage) isPush() bool {
	return (m.Event == "subscribe" || m.Event == "update") && m.payloadKind() == '{'
}

// failure describes an error response, or "" for success.
func (m wsMessage) failure() string {
	if len(m.Error) > 0 && string(m.Error) != "null" && string(m.Error) != "{}" {
		var body wsResultBody
		if json.Unmarshal(m.Error, &body) == nil && (body.Message != "" || body.Code != "") {
			return strings.TrimSpace(body.Code + " " + body.Label + " " + body.Message)
		}
		return string(m.Error)
	}
	if m.Result.Status == "failed" || (m.Result.Code != "" && m.Result.Code != wsSuccessCode) {
		return strings.TrimSpace(m.Result.Code + " " + m.Result.Label + " " + m.Result.Message)
	}
	return ""
}

func DecodeOrder(raw json.RawMessage) (Order, error) {
	var out Order
	err := json.Unmarshal(raw, &out)
	return out, err
}

func DecodeTrade(raw json.RawMessage) (Trade, error) {
	var out Trade
	err := json.Unmarshal(raw, &out)
	return out, err
}

func DecodePosition(raw json.RawMessage) (Position, error) {
	var out Position
	err := json.Unmarshal(raw, &out)
	return out, err
}

func DecodeAsset(raw json.RawMessage) (AccountAsset, error) {
	var out AccountAsset
	err := json.Unmarshal(raw, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Connection
// ---------------------------------------------------------------------------

type wsResult struct {
	message wsMessage
	err     error
}

type wsSession struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func newWSSession(conn *websocket.Conn) *wsSession {
	session := &wsSession{conn: conn}
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	return session
}

func (s *wsSession) write(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(value)
}

func (s *wsSession) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.writeMu.Lock()
			err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			s.writeMu.Unlock()
			if err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *wsSession) readLoop(out chan<- wsResult) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			out <- wsResult{err: fmt.Errorf("crossex ws: read: %w", err)}
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		var message wsMessage
		if err := json.Unmarshal(data, &message); err != nil {
			out <- wsResult{err: fmt.Errorf("crossex ws: malformed frame: %w", err)}
			return
		}
		out <- wsResult{message: message}
	}
}

func (s *wsSession) close() {
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

func dialWebSocket(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Hostname() == "" {
		return nil, fmt.Errorf("invalid endpoint URL")
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	if loopbackHost(target.Hostname()) {
		dialer.Proxy = nil
	} else {
		dialer.Proxy = webSocketProxy
	}
	conn, _, err := dialer.DialContext(ctx, rawURL, nil)
	return conn, err
}

// webSocketProxy honours PROXY / ALL_PROXY before the standard HTTP(S)_PROXY
// variables, matching the other SDK websocket clients.
func webSocketProxy(request *http.Request) (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	}
	if raw == "" {
		return http.ProxyFromEnvironment(request)
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Host == "" {
		return nil, fmt.Errorf("crossex ws: invalid proxy URL")
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https", "socks5":
		return proxyURL, nil
	}
	return nil, fmt.Errorf("crossex ws: unsupported proxy scheme")
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
