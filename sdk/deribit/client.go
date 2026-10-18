// Package deribit is a client for Deribit API v2 (JSON-RPC over HTTPS and
// WebSocket) covering USDC-settled linear perpetuals ("*_USDC-PERPETUAL").
// For linear instruments order amounts are in the base currency.
//
// Private HTTP calls carry an OAuth bearer token obtained with
// client_credentials and refreshed before it expires.
//
// Reference: https://docs.deribit.com (OpenAPI deribit_openapi.json and
// AsyncAPI deribit_asyncapi.json).
package deribit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
)

const (
	// Venue is the request-venue tag used for rate-limit accounting.
	Venue = "deribit"

	DefaultRESTBaseURL = "https://www.deribit.com"
	apiPrefix          = "/api/v2"

	maxResponseBytes = 16 << 20
)

var ErrCredentialsRequired = errors.New("deribit sdk: credentials required")

// Client is the Deribit HTTP JSON-RPC client.
type Client struct {
	baseURL      string
	httpClient   *http.Client
	clientID     string
	clientSecret string
	now          func() time.Time

	tokenMu sync.Mutex
	token   string
	expires time.Time
}

func NewClient() *Client {
	return &Client{baseURL: DefaultRESTBaseURL, httpClient: &http.Client{Timeout: 15 * time.Second}, now: time.Now}
}

func (c *Client) WithCredentials(clientID, clientSecret string) *Client {
	c.clientID, c.clientSecret = clientID, clientSecret
	return c
}

func (c *Client) HasCredentials() bool { return c.clientID != "" && c.clientSecret != "" }

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
// Transport
// ---------------------------------------------------------------------------

// RPCError is a JSON-RPC error object.
type RPCError struct {
	StatusCode int
	Method     string
	Code       int
	Message    string
	Reason     string
}

func (e *RPCError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("deribit sdk: %s: %d %s (%s)", e.Method, e.Code, e.Message, e.Reason)
	}
	return fmt.Sprintf("deribit sdk: %s: %d %s", e.Method, e.Code, e.Message)
}

// Error codes used for classification (see the API's error table).
const (
	CodeUnauthorized      = 13009
	CodeInvalidCredential = 13004
	CodeForbidden         = 13021
	CodeNotEnoughFunds    = 10009
	CodeOrderNotFound     = 10004
	CodeAlreadyClosed     = 10010
	CodeTooManyRequests   = 10028
	CodeInvalidArguments  = -32602
	CodeLabelTooLong      = 10046
)

func IsAuthError(err error) bool {
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	switch rpcErr.Code {
	case CodeUnauthorized, CodeInvalidCredential, CodeForbidden, 13000, 13001, 13002, 13003, 13005, 13006, 13007, 13008, 13010, 13011:
		return true
	}
	return rpcErr.StatusCode == http.StatusUnauthorized
}

func IsOrderNotFound(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == CodeOrderNotFound
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

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

func (c *Client) call(ctx context.Context, method string, p params, private bool, out any) (returnErr error) {
	target := c.baseURL + apiPrefix + "/" + method
	if query := p.encode(); query != "" {
		target += "?" + query
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if private {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := venuehttp.DoHTTP(c.httpClient, request, Venue)
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, response)
		var httpErr *venuehttp.HTTPError
		var rpcErr *RPCError
		if errors.As(returnErr, &httpErr) && errors.As(returnErr, &rpcErr) && httpErr.Code == "" {
			httpErr.Code = strconv.Itoa(rpcErr.Code)
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
		return fmt.Errorf("deribit sdk: %s response is too large", method)
	}
	var envelope rpcResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		if response.StatusCode >= 300 {
			return &RPCError{StatusCode: response.StatusCode, Method: method}
		}
		return fmt.Errorf("deribit sdk: %s: %w: %v", method, venuehttp.ErrMalformedResponse, err)
	}
	if envelope.Error != nil {
		rpcErr := &RPCError{StatusCode: response.StatusCode, Method: method, Code: envelope.Error.Code, Message: envelope.Error.Message}
		var data struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(envelope.Error.Data, &data)
		rpcErr.Reason = data.Reason
		if rpcErr.Code == CodeUnauthorized {
			c.dropToken()
		}
		return rpcErr
	}
	if response.StatusCode >= 300 {
		return &RPCError{StatusCode: response.StatusCode, Method: method}
	}
	if out == nil {
		return nil
	}
	decoder = json.NewDecoder(bytes.NewReader(envelope.Result))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("deribit sdk: %s: %w: %v", method, venuehttp.ErrMalformedResponse, err)
	}
	return nil
}

// accessToken returns a valid token, authenticating with client_credentials
// when none is cached or it expires within a minute.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if !c.HasCredentials() {
		return "", ErrCredentialsRequired
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && c.now().Add(time.Minute).Before(c.expires) {
		return c.token, nil
	}
	var auth AuthResult
	if err := c.call(ctx, "public/auth", params{"grant_type": "client_credentials", "client_id": c.clientID, "client_secret": c.clientSecret}, false, &auth); err != nil {
		return "", err
	}
	if auth.AccessToken == "" {
		return "", fmt.Errorf("deribit sdk: auth returned no token")
	}
	c.token = auth.AccessToken
	c.expires = c.now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *Client) dropToken() {
	c.tokenMu.Lock()
	c.token = ""
	c.tokenMu.Unlock()
}

// ---------------------------------------------------------------------------
// Public
// ---------------------------------------------------------------------------

func (c *Client) Instruments(ctx context.Context, currency string) ([]Instrument, error) {
	var out []Instrument
	err := c.call(ctx, "public/get_instruments", params{"currency": currency, "kind": "future"}, false, &out)
	return out, err
}

func (c *Client) Ticker(ctx context.Context, instrument string) (Ticker, error) {
	var out Ticker
	err := c.call(ctx, "public/ticker", params{"instrument_name": instrument}, false, &out)
	return out, err
}

func (c *Client) OrderBook(ctx context.Context, instrument string, depth int) (OrderBook, error) {
	p := params{"instrument_name": instrument}
	if depth > 0 {
		p["depth"] = strconv.Itoa(depth)
	}
	var out OrderBook
	err := c.call(ctx, "public/get_order_book", p, false, &out)
	return out, err
}

func (c *Client) LastTrades(ctx context.Context, instrument string, count int) ([]Trade, error) {
	p := params{"instrument_name": instrument}
	if count > 0 {
		p["count"] = strconv.Itoa(count)
	}
	var out struct {
		Trades []Trade `json:"trades"`
	}
	err := c.call(ctx, "public/get_last_trades_by_instrument", p, false, &out)
	return out.Trades, err
}

// Chart reads TradingView chart data. resolution is minutes ("1", "5", ...)
// or "1D"; times are milliseconds.
func (c *Client) Chart(ctx context.Context, instrument, resolution string, start, end int64) (Chart, error) {
	var out Chart
	err := c.call(ctx, "public/get_tradingview_chart_data", params{
		"instrument_name": instrument, "resolution": resolution,
		"start_timestamp": strconv.FormatInt(start, 10), "end_timestamp": strconv.FormatInt(end, 10),
	}, false, &out)
	return out, err
}

func (c *Client) FundingRateHistory(ctx context.Context, instrument string, start, end int64) ([]FundingPoint, error) {
	var out []FundingPoint
	err := c.call(ctx, "public/get_funding_rate_history", params{
		"instrument_name": instrument, "start_timestamp": strconv.FormatInt(start, 10), "end_timestamp": strconv.FormatInt(end, 10),
	}, false, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Private
// ---------------------------------------------------------------------------

func (c *Client) AccountSummary(ctx context.Context, currency string) (AccountSummary, error) {
	var out AccountSummary
	err := c.call(ctx, "private/get_account_summary", params{"currency": currency}, true, &out)
	return out, err
}

func (c *Client) Positions(ctx context.Context, currency string) ([]Position, error) {
	var out []Position
	err := c.call(ctx, "private/get_positions", params{"currency": currency, "kind": "future"}, true, &out)
	return out, err
}

func (c *Client) OpenOrders(ctx context.Context, currency string) ([]Order, error) {
	var out []Order
	err := c.call(ctx, "private/get_open_orders_by_currency", params{"currency": currency, "kind": "future"}, true, &out)
	return out, err
}

func (c *Client) OrderHistory(ctx context.Context, currency string, count, offset int) ([]Order, error) {
	p := params{"currency": currency, "kind": "future", "include_unfilled": "true"}
	if count > 0 {
		p["count"] = strconv.Itoa(count)
	}
	if offset > 0 {
		p["offset"] = strconv.Itoa(offset)
	}
	var out []Order
	err := c.call(ctx, "private/get_order_history_by_currency", p, true, &out)
	return out, err
}

func (c *Client) UserTrades(ctx context.Context, currency string, start, end int64, count int) ([]Trade, error) {
	p := params{"currency": currency, "kind": "future", "sorting": "desc"}
	if start > 0 {
		p["start_timestamp"] = strconv.FormatInt(start, 10)
	}
	if end > 0 {
		p["end_timestamp"] = strconv.FormatInt(end, 10)
	}
	if count > 0 {
		p["count"] = strconv.Itoa(count)
	}
	var out struct {
		Trades []Trade `json:"trades"`
	}
	err := c.call(ctx, "private/get_user_trades_by_currency", p, true, &out)
	return out.Trades, err
}

func (c *Client) OrderState(ctx context.Context, orderID string) (Order, error) {
	var out Order
	err := c.call(ctx, "private/get_order_state", params{"order_id": orderID}, true, &out)
	return out, err
}

// OrderStateByLabel finds orders carrying label (open or recently closed).
func (c *Client) OrderStateByLabel(ctx context.Context, currency, label string) ([]Order, error) {
	var out []Order
	err := c.call(ctx, "private/get_order_state_by_label", params{"currency": currency, "label": label}, true, &out)
	return out, err
}

// Place sends private/buy or private/sell.
func (c *Client) Place(ctx context.Context, request OrderRequest) (PlaceResult, error) {
	method := "private/buy"
	if request.Direction == "sell" {
		method = "private/sell"
	}
	p := params{
		"instrument_name": request.Instrument, "amount": request.Amount, "type": request.Type,
		"price": request.Price, "time_in_force": request.TimeInForce, "label": request.Label,
	}
	if request.PostOnly {
		p["post_only"] = "true"
		p["reject_post_only"] = "true"
	}
	if request.ReduceOnly {
		p["reduce_only"] = "true"
	}
	var out PlaceResult
	err := c.call(ctx, method, p, true, &out)
	return out, err
}

func (c *Client) Cancel(ctx context.Context, orderID string) (Order, error) {
	var out Order
	err := c.call(ctx, "private/cancel", params{"order_id": orderID}, true, &out)
	return out, err
}
