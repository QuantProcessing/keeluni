package crossex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
)

const (
	// Venue is the request-venue tag used for rate-limit accounting.
	Venue = "crossex"

	DefaultRESTBaseURL = "https://api.gateio.ws/api/v4"

	maxResponseBytes = 16 << 20 // rule/symbols is ~6 MB today
)

// Client is the CrossEx REST client. Public endpoints work without
// credentials; private endpoints return ErrCredentialsRequired.
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secretKey  string
	now        func() time.Time
}

var ErrCredentialsRequired = errors.New("crossex sdk: credentials required")

func NewClient() *Client {
	return &Client{baseURL: DefaultRESTBaseURL, httpClient: &http.Client{Timeout: 15 * time.Second}, now: time.Now}
}

func (c *Client) WithCredentials(apiKey, secretKey string) *Client {
	c.apiKey, c.secretKey = apiKey, secretKey
	return c
}

func (c *Client) HasCredentials() bool { return c.apiKey != "" && c.secretKey != "" }

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
// Public
// ---------------------------------------------------------------------------

// Symbols lists trading rules. symbols filters by exact CrossEx symbol.
func (c *Client) Symbols(ctx context.Context, symbols ...string) ([]Symbol, error) {
	var out []Symbol
	err := c.do(ctx, http.MethodGet, "/crossex/rule/symbols", query{"symbols": strings.Join(symbols, ",")}, nil, false, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

// Account reads the margin account. In cross-exchange mode no exchange type
// is passed; isolated mode would require one and is not supported here.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var out Account
	err := c.do(ctx, http.MethodGet, "/crossex/accounts", nil, nil, true, &out)
	return out, err
}

func (c *Client) Positions(ctx context.Context, q PositionsQuery) ([]Position, error) {
	var out []Position
	err := c.do(ctx, http.MethodGet, "/crossex/positions", query{"symbol": q.Symbol, "exchange_type": q.ExchangeType}, nil, true, &out)
	return out, err
}

// Leverages returns the configured leverage per futures symbol.
func (c *Client) Leverages(ctx context.Context, symbols ...string) (map[string]string, error) {
	out := map[string]string{}
	err := c.do(ctx, http.MethodGet, "/crossex/positions/leverage", query{"symbols": strings.Join(symbols, ",")}, nil, true, &out)
	return out, err
}

func (c *Client) SetLeverage(ctx context.Context, symbol, leverage string) (Leverage, error) {
	var out Leverage
	err := c.do(ctx, http.MethodPost, "/crossex/positions/leverage", nil, map[string]string{"symbol": symbol, "leverage": leverage}, true, &out)
	return out, err
}

func (c *Client) FeeRates(ctx context.Context) ([]FeeRate, error) {
	var out []FeeRate
	err := c.do(ctx, http.MethodGet, "/crossex/fee", nil, nil, true, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

// PlaceOrder submits an order. CrossEx accepts asynchronously: success only
// returns identifiers, and the order may still FAIL or be REJECTed later.
func (c *Client) PlaceOrder(ctx context.Context, request OrderRequest) (OrderAction, error) {
	var out OrderAction
	err := c.do(ctx, http.MethodPost, "/crossex/orders", nil, request, true, &out)
	return out, err
}

// CancelOrder cancels by order id or client text.
func (c *Client) CancelOrder(ctx context.Context, id string) (OrderAction, error) {
	path, err := orderPath(id)
	if err != nil {
		return OrderAction{}, err
	}
	var out OrderAction
	err = c.do(ctx, http.MethodDelete, path, nil, nil, true, &out)
	return out, err
}

// Order reads one order by order id or client text. CrossEx documents that a
// finished order may no longer be found by text.
func (c *Client) Order(ctx context.Context, id string) (Order, error) {
	path, err := orderPath(id)
	if err != nil {
		return Order{}, err
	}
	var out Order
	err = c.do(ctx, http.MethodGet, path, nil, nil, true, &out)
	return out, err
}

func (c *Client) OpenOrders(ctx context.Context, q OpenOrdersQuery) ([]Order, error) {
	var out []Order
	err := c.do(ctx, http.MethodGet, "/crossex/open_orders", query{"symbol": q.Symbol, "exchange_type": q.ExchangeType, "business_type": q.BusinessType}, nil, true, &out)
	return out, err
}

func (c *Client) OrderHistory(ctx context.Context, q HistoryQuery) ([]Order, error) {
	var out []Order
	err := c.do(ctx, http.MethodGet, "/crossex/history_orders", q.values(), nil, true, &out)
	return out, err
}

func (c *Client) Trades(ctx context.Context, q HistoryQuery) ([]Trade, error) {
	var out []Trade
	err := c.do(ctx, http.MethodGet, "/crossex/history_trades", q.values(), nil, true, &out)
	return out, err
}

func orderPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 64 || strings.ContainsAny(id, "/?#% ") {
		return "", fmt.Errorf("crossex sdk: invalid order id")
	}
	return "/crossex/orders/" + url.PathEscape(id), nil
}

func (q HistoryQuery) values() query {
	values := query{"symbol": q.Symbol}
	if q.From > 0 {
		values["from"] = strconv.FormatInt(q.From, 10)
	}
	if q.To > 0 {
		values["to"] = strconv.FormatInt(q.To, 10)
	}
	if q.Page > 0 {
		values["page"] = strconv.Itoa(q.Page)
	}
	if q.Limit > 0 {
		values["limit"] = strconv.Itoa(q.Limit)
	}
	return values
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// query keeps insertion-independent, deterministic encoding (sorted keys) and
// drops empty values. The signature covers the exact encoded string.
type query map[string]string

func (q query) encode() string {
	values := url.Values{}
	for key, value := range q {
		if value != "" {
			values.Set(key, value)
		}
	}
	return values.Encode()
}

func (c *Client) do(ctx context.Context, method, path string, q query, body any, signed bool, out any) (returnErr error) {
	if signed && !c.HasCredentials() {
		return ErrCredentialsRequired
	}
	target, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	target.RawQuery = q.encode()

	var bodyText string
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyText = string(payload)
	}
	var reader io.Reader
	if bodyText != "" {
		reader = bytes.NewBufferString(bodyText)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if bodyText != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if signed {
		ts := timestamp(c.now())
		request.Header.Set("KEY", c.apiKey)
		request.Header.Set("Timestamp", ts)
		request.Header.Set("SIGN", sign(c.secretKey, signingPayload(method, target.Path, target.RawQuery, bodyText, ts)))
	}

	response, err := venuehttp.DoHTTP(c.httpClient, request, Venue)
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, response)
		var httpErr *venuehttp.HTTPError
		var apiErr *APIError
		if errors.As(returnErr, &httpErr) && errors.As(returnErr, &apiErr) && httpErr.Code == "" {
			httpErr.Code = apiErr.Label
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
		return fmt.Errorf("crossex sdk: %s %s response exceeds %d bytes", method, path, maxResponseBytes)
	}
	if response.StatusCode >= 300 {
		return newAPIError(response.StatusCode, method, path, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("crossex sdk: %s %s: %w: %v", method, path, venuehttp.ErrMalformedResponse, err)
	}
	return nil
}
