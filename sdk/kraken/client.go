// Package kraken is a client for Kraken Derivatives (Kraken Futures): the
// REST API at https://futures.kraken.com/derivatives/api/v3, the chart API at
// https://futures.kraken.com/api/charts/v1 and the WebSocket API at
// wss://futures.kraken.com/ws/v1.
//
// Only multi-collateral linear perpetuals ("PF_*", type flexible_futures) are
// modelled; sizes are in base units (contractSize 1). Numbers arrive as JSON
// numbers and are decoded as json.Number so no precision is lost.
//
// Reference: docs.kraken.com/api/docs/futures-api and the official Go module
// github.com/krakenfx/api-go (pkg/derivatives).
package kraken

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
	// Venue is the request-venue tag used for rate-limit accounting.
	Venue = "kraken"

	DefaultRESTBaseURL = "https://futures.kraken.com"
	restPrefix         = "/derivatives/api/v3"
	chartsPrefix       = "/api/charts/v1"

	maxResponseBytes = 16 << 20
)

var ErrCredentialsRequired = errors.New("kraken sdk: credentials required")

// Client is the Kraken Derivatives REST client.
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

// WithCredentials sets the API key and the base64-encoded API secret.
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

// ---------------------------------------------------------------------------
// Signing
// ---------------------------------------------------------------------------

// Sign computes Authent: base64(HMAC-SHA512(base64decode(secret),
// SHA256(postData + nonce + endpointPath))). endpointPath excludes the
// "/derivatives" prefix, matching the official client.
func Sign(secret, postData, nonce, endpointPath string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("kraken sdk: secret is not base64")
	}
	digest := sha256.Sum256([]byte(postData + nonce + strings.TrimPrefix(endpointPath, "/derivatives")))
	mac := hmac.New(sha512.New, key)
	mac.Write(digest[:])
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// SignChallenge signs a WebSocket challenge: base64(HMAC-SHA512(
// base64decode(secret), SHA256(challenge))).
func SignChallenge(secret, challenge string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("kraken sdk: secret is not base64")
	}
	digest := sha256.Sum256([]byte(challenge))
	mac := hmac.New(sha512.New, key)
	mac.Write(digest[:])
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// nextNonce is strictly increasing even within one millisecond.
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

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// params encodes deterministically (sorted keys) so the signature covers the
// exact query string or body sent.
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

type envelope struct {
	Result string `json:"result"`
	Error  string `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path string, p params, signed bool, out any) (returnErr error) {
	if signed && !c.HasCredentials() {
		return ErrCredentialsRequired
	}
	encoded := p.encode()
	target := c.baseURL + path
	var body io.Reader
	if method == http.MethodGet || method == http.MethodDelete {
		if encoded != "" {
			target += "?" + encoded
		}
	} else if encoded != "" {
		body = bytes.NewBufferString(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if signed {
		nonce := c.nextNonce()
		authent, err := Sign(c.secret, encoded, nonce, path)
		if err != nil {
			return err
		}
		request.Header.Set("APIKey", c.apiKey)
		request.Header.Set("Nonce", nonce)
		request.Header.Set("Authent", authent)
	}
	response, err := venuehttp.DoHTTP(c.httpClient, request, Venue)
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, response)
		var httpErr *venuehttp.HTTPError
		var apiErr *APIError
		if errors.As(returnErr, &httpErr) && errors.As(returnErr, &apiErr) && httpErr.Code == "" {
			httpErr.Code = apiErr.Code
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
		return fmt.Errorf("kraken sdk: %s %s response is too large", method, path)
	}
	var head envelope
	_ = json.Unmarshal(payload, &head)
	if response.StatusCode >= 300 || head.Result == "error" {
		return &APIError{StatusCode: response.StatusCode, Method: method, Path: path, Code: head.Error}
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("kraken sdk: %s %s: %w: %v", method, path, venuehttp.ErrMalformedResponse, err)
	}
	return nil
}

// APIError is an error response: a non-2xx status or result "error". Code is
// Kraken's error string (e.g. "authenticationError", "apiLimitExceeded").
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Code       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kraken sdk: %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Code)
}

// IsAuthError reports a key, signature, nonce or permission failure.
func IsAuthError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "authenticationError", "nonceBelowThreshold", "nonceDuplicate", "requiredArgumentMissing:apikey", "Unauthorized":
		return true
	}
	return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
}

// ---------------------------------------------------------------------------
// Public
// ---------------------------------------------------------------------------

func (c *Client) Instruments(ctx context.Context) ([]Instrument, error) {
	var out struct {
		Instruments []Instrument `json:"instruments"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/instruments", nil, false, &out)
	return out.Instruments, err
}

func (c *Client) Tickers(ctx context.Context) ([]Ticker, error) {
	var out struct {
		Tickers []Ticker `json:"tickers"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/tickers", nil, false, &out)
	return out.Tickers, err
}

func (c *Client) Ticker(ctx context.Context, symbol string) (Ticker, error) {
	var out struct {
		Ticker Ticker `json:"ticker"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/tickers/"+url.PathEscape(symbol), nil, false, &out)
	return out.Ticker, err
}

func (c *Client) OrderBook(ctx context.Context, symbol string) (OrderBook, error) {
	var out struct {
		OrderBook  OrderBook `json:"orderBook"`
		ServerTime string    `json:"serverTime"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/orderbook", params{"symbol": symbol}, false, &out)
	out.OrderBook.ServerTime = out.ServerTime
	return out.OrderBook, err
}

func (c *Client) TradeHistory(ctx context.Context, symbol string) ([]PublicTrade, error) {
	var out struct {
		History []PublicTrade `json:"history"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/history", params{"symbol": symbol}, false, &out)
	return out.History, err
}

// Candles reads trade candles. resolution is 1m, 5m, 15m, 30m, 1h, 4h, 12h,
// 1d or 1w; from and to are optional unix seconds.
func (c *Client) Candles(ctx context.Context, symbol, resolution string, from, to int64) ([]Candle, error) {
	p := params{}
	if from > 0 {
		p["from"] = strconv.FormatInt(from, 10)
	}
	if to > 0 {
		p["to"] = strconv.FormatInt(to, 10)
	}
	var out struct {
		Candles []Candle `json:"candles"`
	}
	path := chartsPrefix + "/trade/" + url.PathEscape(symbol) + "/" + url.PathEscape(resolution)
	err := c.do(ctx, http.MethodGet, path, p, false, &out)
	return out.Candles, err
}

func (c *Client) FundingRates(ctx context.Context, symbol string) ([]FundingRate, error) {
	var out struct {
		Rates []FundingRate `json:"rates"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/historical-funding-rates", params{"symbol": symbol}, false, &out)
	return out.Rates, err
}

// ---------------------------------------------------------------------------
// Private
// ---------------------------------------------------------------------------

func (c *Client) Accounts(ctx context.Context) (Accounts, error) {
	var out struct {
		Accounts Accounts `json:"accounts"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/accounts", nil, true, &out)
	return out.Accounts, err
}

func (c *Client) OpenPositions(ctx context.Context) ([]OpenPosition, error) {
	var out struct {
		OpenPositions []OpenPosition `json:"openPositions"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/openpositions", nil, true, &out)
	return out.OpenPositions, err
}

func (c *Client) OpenOrders(ctx context.Context) ([]OpenOrder, error) {
	var out struct {
		OpenOrders []OpenOrder `json:"openOrders"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/openorders", nil, true, &out)
	return out.OpenOrders, err
}

// Fills returns the most recent fills, newest first. lastFillTime (RFC 3339)
// pages backwards.
func (c *Client) Fills(ctx context.Context, lastFillTime string) ([]Fill, error) {
	var out struct {
		Fills []Fill `json:"fills"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/fills", params{"lastFillTime": lastFillTime}, true, &out)
	return out.Fills, err
}

// SendOrder places an order. Rejections are a success response whose
// SendStatus.Status names the reason (e.g. "insufficientAvailableFunds").
func (c *Client) SendOrder(ctx context.Context, order OrderRequest) (SendStatus, error) {
	p := params{
		"orderType": order.OrderType, "symbol": order.Symbol, "side": order.Side, "size": order.Size,
		"limitPrice": order.LimitPrice, "cliOrdId": order.ClientOrderID,
	}
	if order.ReduceOnly {
		p["reduceOnly"] = "true"
	}
	if order.StopPrice != "" {
		p["stopPrice"] = order.StopPrice
	}
	if order.TriggerSignal != "" {
		p["triggerSignal"] = order.TriggerSignal
	}
	var out struct {
		SendStatus SendStatus `json:"sendStatus"`
	}
	err := c.do(ctx, http.MethodPost, restPrefix+"/sendorder", p, true, &out)
	return out.SendStatus, err
}

// CancelOrder cancels by order id or client order id (exactly one).
func (c *Client) CancelOrder(ctx context.Context, orderID, clientOrderID string) (CancelStatus, error) {
	var out struct {
		CancelStatus CancelStatus `json:"cancelStatus"`
	}
	err := c.do(ctx, http.MethodPost, restPrefix+"/cancelorder", params{"order_id": orderID, "cliOrdId": clientOrderID}, true, &out)
	return out.CancelStatus, err
}

// OrderStatus reads orders by id or client id, open or recently closed.
func (c *Client) OrderStatus(ctx context.Context, orderIDs, clientOrderIDs []string) ([]OrderStatusDetails, error) {
	var out struct {
		Orders []OrderStatusDetails `json:"orders"`
	}
	p := params{"orderIds": strings.Join(orderIDs, ","), "cliOrdIds": strings.Join(clientOrderIDs, ",")}
	err := c.do(ctx, http.MethodPost, restPrefix+"/orders/status", p, true, &out)
	return out.Orders, err
}

// LeveragePreferences returns per-symbol isolated leverage settings; symbols
// absent from the list trade in cross margin.
func (c *Client) LeveragePreferences(ctx context.Context) ([]LeveragePreference, error) {
	var out struct {
		LeveragePreferences []LeveragePreference `json:"leveragePreferences"`
	}
	err := c.do(ctx, http.MethodGet, restPrefix+"/leveragepreferences", nil, true, &out)
	return out.LeveragePreferences, err
}

// SetLeveragePreference sets isolated maximum leverage for symbol. An empty
// maxLeverage returns the symbol to cross margin.
func (c *Client) SetLeveragePreference(ctx context.Context, symbol, maxLeverage string) error {
	return c.do(ctx, http.MethodPut, restPrefix+"/leveragepreferences", params{"symbol": symbol, "maxLeverage": maxLeverage}, true, nil)
}
