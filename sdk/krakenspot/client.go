// Package krakenspot is a client for the Kraken Spot API: REST at
// https://api.kraken.com/0 and WebSocket v2 at wss://ws.kraken.com/v2
// (public) and wss://ws-auth.kraken.com/v2 (private).
//
// Kraken Spot and Kraken Futures (package kraken) are separate platforms
// with separate keys, signatures and symbols. Spot pairs are named by their
// v2 symbol "BTC/USD"; REST accepts it wherever a pair is taken. The legacy
// REST key ("XXBTZUSD") appears only in AssetPairs and TradesHistory.
//
// Reference: docs.kraken.com/api/docs/rest-api and /websocket-v2.
package krakenspot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/keeluni/internal/venuehttp"
)

const (
	// Venue is the request-venue tag used for rate-limit accounting. Kraken
	// spot and futures keep separate limits but share the venue name.
	Venue = "kraken"

	DefaultRESTBaseURL = "https://api.kraken.com"
	DefaultWSURL       = "wss://ws.kraken.com/v2"
	DefaultAuthWSURL   = "wss://ws-auth.kraken.com/v2"

	maxResponseBytes = 16 << 20
)

var ErrCredentialsRequired = errors.New("kraken spot sdk: credentials required")

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secret     string
	now        func() time.Time
	nonce      atomic.Int64
}

func NewClient() *Client {
	return &Client{baseURL: DefaultRESTBaseURL, httpClient: &http.Client{Timeout: 15 * time.Second}, now: time.Now}
}

// WithCredentials sets the API key and its base64 private key.
func (c *Client) WithCredentials(apiKey, secret string) *Client {
	c.apiKey, c.secret = apiKey, secret
	return c
}

func (c *Client) HasCredentials() bool { return c.apiKey != "" && c.secret != "" }

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	if httpClient != nil {
		c.httpClient = httpClient
	}
	return c
}

func (c *Client) WithClock(now func() time.Time) *Client {
	if now != nil {
		c.now = now
	}
	return c
}

// Sign computes API-Sign: base64(HMAC-SHA512(base64decode(secret),
// path + SHA256(nonce + postData))).
func Sign(secret, path, nonce, postData string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("kraken spot sdk: secret is not base64")
	}
	digest := sha256.Sum256([]byte(nonce + postData))
	mac := hmac.New(sha512.New, key)
	mac.Write([]byte(path))
	mac.Write(digest[:])
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c *Client) nextNonce() string {
	now := c.now().UnixMilli() * 1000
	for {
		last := c.nonce.Load()
		next := max(now, last+1)
		if c.nonce.CompareAndSwap(last, next) {
			return strconv.FormatInt(next, 10)
		}
	}
}

// params encodes deterministically (sorted keys).
type params map[string]string

func (p params) encode() string {
	keys := make([]string, 0, len(p))
	for key, value := range p {
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, url.QueryEscape(key)+"="+url.QueryEscape(p[key]))
	}
	return strings.Join(values, "&")
}

// APIError is an error list Kraken returned ("EOrder:Insufficient funds").
type APIError struct {
	StatusCode int
	Path       string
	Errors     []string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kraken spot sdk: %s returned %d: %s", e.Path, e.StatusCode, strings.Join(e.Errors, "; "))
}

// Code is the first error's "Category:Message" text.
func (e *APIError) Code() string {
	if len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0]
}

func hasPrefix(err error, prefixes ...string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, text := range apiErr.Errors {
		for _, prefix := range prefixes {
			if strings.HasPrefix(text, prefix) {
				return true
			}
		}
	}
	return false
}

// IsAuthError reports a key, signature, nonce or permission failure.
func IsAuthError(err error) bool {
	return hasPrefix(err, "EAPI:Invalid key", "EAPI:Invalid signature", "EAPI:Invalid nonce", "EGeneral:Permission denied", "EAPI:Feature disabled")
}

func IsRateLimit(err error) bool {
	return hasPrefix(err, "EAPI:Rate limit", "EOrder:Rate limit", "EGeneral:Too many requests")
}

func IsUnknownOrder(err error) bool { return hasPrefix(err, "EOrder:Unknown order") }

// IsOrderRejection reports a business refusal of an order command: the
// order was not created. Service and transport errors are not refusals.
func IsOrderRejection(err error) bool {
	return hasPrefix(err, "EOrder:", "EGeneral:Invalid arguments", "EFunding:", "EAPI:Invalid arguments")
}

type envelope struct {
	Error  []string        `json:"error"`
	Result json.RawMessage `json:"result"`
}

func (c *Client) do(ctx context.Context, path string, p params, signed bool, out any) (returnErr error) {
	if signed && !c.HasCredentials() {
		return ErrCredentialsRequired
	}
	method := http.MethodGet
	target := c.baseURL + path
	var body io.Reader
	var encoded string
	if signed {
		method = http.MethodPost
		if p == nil {
			p = params{}
		}
		nonce := c.nextNonce()
		p["nonce"] = nonce
		encoded = p.encode()
		body = bytes.NewBufferString(encoded)
	} else if query := p.encode(); query != "" {
		target += "?" + query
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if signed {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		signature, err := Sign(c.secret, path, p["nonce"], encoded)
		if err != nil {
			return err
		}
		request.Header.Set("API-Key", c.apiKey)
		request.Header.Set("API-Sign", signature)
	}
	response, err := venuehttp.DoHTTP(c.httpClient, request, Venue)
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, response)
		var httpErr *venuehttp.HTTPError
		var apiErr *APIError
		if errors.As(returnErr, &httpErr) && errors.As(returnErr, &apiErr) && httpErr.Code == "" {
			httpErr.Code = apiErr.Code()
		}
	}()
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("kraken spot sdk: %s response is too large", path)
	}
	var head envelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&head); err != nil {
		if response.StatusCode >= 300 {
			return &APIError{StatusCode: response.StatusCode, Path: path}
		}
		return fmt.Errorf("kraken spot sdk: %s: %w: %v", path, venuehttp.ErrMalformedResponse, err)
	}
	if len(head.Error) > 0 || response.StatusCode >= 300 {
		return &APIError{StatusCode: response.StatusCode, Path: path, Errors: head.Error}
	}
	if out == nil {
		return nil
	}
	decoder = json.NewDecoder(bytes.NewReader(head.Result))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("kraken spot sdk: %s: %w: %v", path, venuehttp.ErrMalformedResponse, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Public
// ---------------------------------------------------------------------------

// AssetPairs lists every pair keyed by the legacy REST name.
func (c *Client) AssetPairs(ctx context.Context) (map[string]AssetPair, error) {
	out := map[string]AssetPair{}
	err := c.do(ctx, "/0/public/AssetPairs", nil, false, &out)
	return out, err
}

// RESTTicker is one pair's 24h market from the public Ticker endpoint: c is
// [last price, lot volume], o today's open (UTC midnight), v [today, 24h]
// base volume, p [today, 24h] volume-weighted price.
type RESTTicker struct {
	Close  []string `json:"c"`
	Open   string   `json:"o"`
	Volume []string `json:"v"`
	VWAP   []string `json:"p"`
	High   []string `json:"h"`
	Low    []string `json:"l"`
}

// Tickers reads every pair's ticker, keyed by the legacy REST name.
func (c *Client) Tickers(ctx context.Context) (map[string]RESTTicker, error) {
	out := map[string]RESTTicker{}
	err := c.do(ctx, "/0/public/Ticker", nil, false, &out)
	return out, err
}

// Assets lists every asset keyed by its legacy name.
func (c *Client) Assets(ctx context.Context) (map[string]Asset, error) {
	out := map[string]Asset{}
	err := c.do(ctx, "/0/public/Assets", nil, false, &out)
	return out, err
}

// Depth returns the book of one pair (v2 symbol or legacy name).
func (c *Client) Depth(ctx context.Context, pair string, count int) (Depth, error) {
	p := params{"pair": pair}
	if count > 0 {
		p["count"] = strconv.Itoa(count)
	}
	out := map[string]Depth{}
	if err := c.do(ctx, "/0/public/Depth", p, false, &out); err != nil {
		return Depth{}, err
	}
	for _, book := range out {
		return book, nil
	}
	return Depth{}, fmt.Errorf("kraken spot sdk: depth for %s is missing", pair)
}

// OHLC returns candles; interval is minutes, since is unix seconds.
func (c *Client) OHLC(ctx context.Context, pair string, interval int, since int64) ([]OHLC, error) {
	p := params{"pair": pair, "interval": strconv.Itoa(interval)}
	if since > 0 {
		p["since"] = strconv.FormatInt(since, 10)
	}
	var raw map[string]json.RawMessage
	if err := c.do(ctx, "/0/public/OHLC", p, false, &raw); err != nil {
		return nil, err
	}
	for key, value := range raw {
		if key == "last" {
			continue
		}
		var rows [][]json.RawMessage
		if err := decodeNumbers(value, &rows); err != nil {
			return nil, err
		}
		out := make([]OHLC, 0, len(rows))
		for _, row := range rows {
			if len(row) < 7 {
				return nil, fmt.Errorf("kraken spot sdk: ohlc row is short")
			}
			var candle OHLC
			if decodeNumbers(row[0], &candle.Time) != nil || decodeString(row[1], &candle.Open) != nil || decodeString(row[2], &candle.High) != nil ||
				decodeString(row[3], &candle.Low) != nil || decodeString(row[4], &candle.Close) != nil || decodeString(row[6], &candle.Volume) != nil {
				return nil, fmt.Errorf("kraken spot sdk: ohlc row is malformed")
			}
			out = append(out, candle)
		}
		return out, nil
	}
	return nil, nil
}

// Trades returns recent public trades.
func (c *Client) Trades(ctx context.Context, pair string, count int) ([]PublicTrade, error) {
	p := params{"pair": pair}
	if count > 0 {
		p["count"] = strconv.Itoa(count)
	}
	var raw map[string]json.RawMessage
	if err := c.do(ctx, "/0/public/Trades", p, false, &raw); err != nil {
		return nil, err
	}
	for key, value := range raw {
		if key == "last" {
			continue
		}
		var rows [][]json.RawMessage
		if err := decodeNumbers(value, &rows); err != nil {
			return nil, err
		}
		out := make([]PublicTrade, 0, len(rows))
		for _, row := range rows {
			if len(row) < 7 {
				return nil, fmt.Errorf("kraken spot sdk: trade row is short")
			}
			var trade PublicTrade
			var at json.Number
			if decodeString(row[0], &trade.Price) != nil || decodeString(row[1], &trade.Volume) != nil || decodeNumbers(row[2], &at) != nil ||
				decodeString(row[3], &trade.Side) != nil || decodeNumbers(row[6], &trade.TradeID) != nil {
				return nil, fmt.Errorf("kraken spot sdk: trade row is malformed")
			}
			trade.Time = secondsTime(at)
			out = append(out, trade)
		}
		return out, nil
	}
	return nil, nil
}

func decodeNumbers(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(out)
}

func decodeString(raw json.RawMessage, out *string) error {
	var number json.Number
	if err := decodeNumbers(raw, &number); err == nil {
		*out = number.String()
		return nil
	}
	return json.Unmarshal(raw, out)
}

// secondsTime converts Kraken's fractional unix seconds.
func secondsTime(value json.Number) time.Time {
	seconds, err := value.Float64()
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	whole := int64(seconds)
	return time.Unix(whole, int64((seconds-float64(whole))*1e9)).UTC()
}

// ---------------------------------------------------------------------------
// Private
// ---------------------------------------------------------------------------

// BalanceEx returns balances with amounts held by open orders.
func (c *Client) BalanceEx(ctx context.Context) (map[string]ExtendedBalance, error) {
	out := map[string]ExtendedBalance{}
	err := c.do(ctx, "/0/private/BalanceEx", nil, true, &out)
	return out, err
}

func (c *Client) OpenOrders(ctx context.Context) (map[string]OrderInfo, error) {
	var out struct {
		Open map[string]OrderInfo `json:"open"`
	}
	err := c.do(ctx, "/0/private/OpenOrders", params{"cl_ord_id": ""}, true, &out)
	return out.Open, err
}

// ClosedOrders pages by offset, newest first; clientOrderID filters.
func (c *Client) ClosedOrders(ctx context.Context, offset int, start, end int64, clientOrderID string) (map[string]OrderInfo, int, error) {
	p := params{"cl_ord_id": clientOrderID}
	if offset > 0 {
		p["ofs"] = strconv.Itoa(offset)
	}
	if start > 0 {
		p["start"] = strconv.FormatInt(start, 10)
	}
	if end > 0 {
		p["end"] = strconv.FormatInt(end, 10)
	}
	var out struct {
		Closed map[string]OrderInfo `json:"closed"`
		Count  int                  `json:"count"`
	}
	err := c.do(ctx, "/0/private/ClosedOrders", p, true, &out)
	return out.Closed, out.Count, err
}

// QueryOrders reads orders by transaction id (comma separated, max 50).
func (c *Client) QueryOrders(ctx context.Context, txids ...string) (map[string]OrderInfo, error) {
	out := map[string]OrderInfo{}
	err := c.do(ctx, "/0/private/QueryOrders", params{"txid": strings.Join(txids, ","), "trades": "false"}, true, &out)
	return out, err
}

// TradesHistory pages own trades by offset, newest first.
func (c *Client) TradesHistory(ctx context.Context, offset int, start, end int64) (map[string]OwnTrade, int, error) {
	p := params{"type": "all"}
	if offset > 0 {
		p["ofs"] = strconv.Itoa(offset)
	}
	if start > 0 {
		p["start"] = strconv.FormatInt(start, 10)
	}
	if end > 0 {
		p["end"] = strconv.FormatInt(end, 10)
	}
	var out struct {
		Trades map[string]OwnTrade `json:"trades"`
		Count  int                 `json:"count"`
	}
	err := c.do(ctx, "/0/private/TradesHistory", p, true, &out)
	return out.Trades, out.Count, err
}

func (c *Client) AddOrder(ctx context.Context, order OrderRequest) (AddOrderResult, error) {
	p := params{
		"pair": order.Pair, "type": order.Side, "ordertype": order.Type, "volume": order.Volume, "price": order.Price,
		"timeinforce": order.TimeInForce, "oflags": order.Flags, "cl_ord_id": order.ClientOrderID,
	}
	var out AddOrderResult
	err := c.do(ctx, "/0/private/AddOrder", p, true, &out)
	return out, err
}

// CancelOrder cancels by transaction id or client order id.
func (c *Client) CancelOrder(ctx context.Context, txid, clientOrderID string) (int, error) {
	var out struct {
		Count int `json:"count"`
	}
	err := c.do(ctx, "/0/private/CancelOrder", params{"txid": txid, "cl_ord_id": clientOrderID}, true, &out)
	return out.Count, err
}

// WebSocketsToken issues the token private WebSocket subscriptions carry.
func (c *Client) WebSocketsToken(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, "/0/private/GetWebSocketsToken", nil, true, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("kraken spot sdk: no websocket token")
	}
	return out.Token, nil
}
