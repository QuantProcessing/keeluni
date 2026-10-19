package deribit

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
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const DefaultWSURL = "wss://www.deribit.com/ws/api/v2"

var (
	wsHeartbeatSeconds = 30
	wsReadTimeout      = 90 * time.Second
	wsAckTimeout       = 10 * time.Second
)

var ErrAuthRejected = errors.New("deribit ws: authentication rejected")

// Notification is one subscription message.
type Notification struct {
	Channel string
	Data    json.RawMessage
}

// Stream is one JSON-RPC WebSocket connection for a fixed set of channels.
// Run blocks for the life of the connection; reconnecting is the caller's
// job. Private channels ("user.*") authenticate with client_credentials.
type Stream struct {
	url          string
	clientID     string
	clientSecret string
}

func NewStream() *Stream { return &Stream{url: DefaultWSURL} }

func (s *Stream) WithURL(rawURL string) *Stream {
	s.url = rawURL
	return s
}

func (s *Stream) WithCredentials(clientID, clientSecret string) *Stream {
	s.clientID, s.clientSecret = clientID, clientSecret
	return s
}

type Handler struct {
	Ready        func()
	Notification func(Notification)
}

type rpcMessage struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Params struct {
		Channel string          `json:"channel"`
		Data    json.RawMessage `json:"data"`
		Type    string          `json:"type"`
	} `json:"params"`
}

// Run authenticates when needed, enables heartbeats, subscribes channels and
// delivers notifications until ctx ends (nil) or the connection fails.
func (s *Stream) Run(ctx context.Context, channels []string, handler Handler) error {
	if len(channels) == 0 {
		return fmt.Errorf("deribit ws: no channels")
	}
	private := false
	for _, channel := range channels {
		if strings.HasPrefix(channel, "user.") {
			private = true
		}
	}
	if private && (s.clientID == "" || s.clientSecret == "") {
		return ErrCredentialsRequired
	}
	conn, err := dial(ctx, s.url)
	if err != nil {
		return fmt.Errorf("deribit ws: dial: %w", err)
	}
	session := &wsSession{conn: conn}
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	defer session.close()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-runCtx.Done()
		session.close()
	}()
	reads := make(chan wsRead, 256)
	go session.readLoop(reads)

	const (
		idAuth = iota + 1
		idHeartbeat
		idSubscribe
	)
	if private {
		if err := session.call(idAuth, "public/auth", map[string]any{"grant_type": "client_credentials", "client_id": s.clientID, "client_secret": s.clientSecret}); err != nil {
			return err
		}
	} else if err := s.start(session, channels, idHeartbeat, idSubscribe); err != nil {
		return err
	}
	deadline := time.NewTimer(wsAckTimeout)
	defer deadline.Stop()
	subscribed := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			if !subscribed {
				return fmt.Errorf("deribit ws: subscription not acknowledged")
			}
		case read := <-reads:
			if read.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return read.err
			}
			var message rpcMessage
			if err := json.Unmarshal(read.data, &message); err != nil {
				return fmt.Errorf("deribit ws: malformed frame")
			}
			if message.Error != nil {
				if message.ID == idAuth {
					return fmt.Errorf("%w: %d %s", ErrAuthRejected, message.Error.Code, message.Error.Message)
				}
				return fmt.Errorf("deribit ws: %d %s", message.Error.Code, message.Error.Message)
			}
			switch {
			case message.ID == idAuth:
				if err := s.start(session, channels, idHeartbeat, idSubscribe); err != nil {
					return err
				}
				deadline.Reset(wsAckTimeout)
			case message.ID == idSubscribe:
				var accepted []string
				_ = json.Unmarshal(message.Result, &accepted)
				if len(accepted) != len(channels) {
					return fmt.Errorf("deribit ws: subscribed %d of %d channels", len(accepted), len(channels))
				}
				subscribed = true
				if handler.Ready != nil {
					handler.Ready()
				}
			case message.Method == "heartbeat":
				if message.Params.Type == "test_request" {
					if err := session.call(0, "public/test", map[string]any{}); err != nil {
						return err
					}
				}
			case message.Method == "subscription":
				if handler.Notification != nil {
					handler.Notification(Notification{Channel: message.Params.Channel, Data: message.Params.Data})
				}
			}
		}
	}
}

func (s *Stream) start(session *wsSession, channels []string, heartbeatID, subscribeID int64) error {
	if err := session.call(heartbeatID, "public/set_heartbeat", map[string]any{"interval": wsHeartbeatSeconds}); err != nil {
		return err
	}
	method := "public/subscribe"
	for _, channel := range channels {
		if strings.HasPrefix(channel, "user.") {
			method = "private/subscribe"
		}
	}
	return session.call(subscribeID, method, map[string]any{"channels": channels})
}

// ---------------------------------------------------------------------------
// Channel payloads
// ---------------------------------------------------------------------------

// BookChange is book.<instrument>.100ms: the first message is a snapshot;
// later ones are changes with PrevChangeID. Levels are [action, price,
// amount] where action is new, change or delete.
type BookChange struct {
	Type           string               `json:"type"` // snapshot, change
	InstrumentName string               `json:"instrument_name"`
	Timestamp      int64                `json:"timestamp"`
	ChangeID       int64                `json:"change_id"`
	PrevChangeID   int64                `json:"prev_change_id"`
	Bids           [][3]json.RawMessage `json:"bids"`
	Asks           [][3]json.RawMessage `json:"asks"`
}

// ChartCandle is chart.trades.<instrument>.<resolution>.
type ChartCandle struct {
	Tick   int64       `json:"tick"`
	Open   json.Number `json:"open"`
	High   json.Number `json:"high"`
	Low    json.Number `json:"low"`
	Close  json.Number `json:"close"`
	Volume json.Number `json:"volume"`
}

// Decode decodes a notification payload, preserving numbers.
func Decode(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode(out)
}

// ---------------------------------------------------------------------------
// Connection
// ---------------------------------------------------------------------------

type wsRead struct {
	data []byte
	err  error
}

type wsSession struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	ids       atomic.Int64
}

func (s *wsSession) call(id int64, method string, params any) error {
	if id == 0 {
		id = 1000 + s.ids.Add(1)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (s *wsSession) readLoop(out chan<- wsRead) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			out <- wsRead{err: fmt.Errorf("deribit ws: read: %w", err)}
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		out <- wsRead{data: data}
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
		return nil, fmt.Errorf("deribit ws: invalid proxy URL")
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
