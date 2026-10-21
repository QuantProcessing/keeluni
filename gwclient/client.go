package gwclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Error is a gateway error response. Status 4xx means the gateway refused the
// request before it reached a venue (or the venue definitively refused it);
// 5xx means the venue could not be reached or answered unusably.
type Error struct {
	Status       int    `json:"-"`
	Code         string `json:"code"`
	Category     string `json:"category,omitempty"`
	Message      string `json:"message"`
	VenueCode    string `json:"venueCode,omitempty"`
	VenueMessage string `json:"venueMessage,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
}

func (e *Error) Error() string {
	if e.VenueCode != "" && !strings.Contains(e.Message, e.VenueCode) {
		return fmt.Sprintf("%s (%s)", e.Message, e.VenueCode)
	}
	return e.Message
}

// Definite reports whether err proves a request had no effect: a 4xx from the
// gateway. Transport failures and 5xx are never definite.
func Definite(err error) bool {
	var gatewayErr *Error
	return errors.As(err, &gatewayErr) && gatewayErr.Status >= 400 && gatewayErr.Status < 500
}

// Client talks to one gateway with its bearer token.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New returns a client for baseURL (e.g. http://gateway:8081).
func New(baseURL, token string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("gateway URL %q is invalid", baseURL)
	}
	if len(token) < 32 {
		return nil, fmt.Errorf("gateway token is missing or too short")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{base: parsed, token: token, http: httpClient}, nil
}

func (client *Client) endpoint(path string, query url.Values) string {
	target := *client.base
	target.Path = client.base.Path + path
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}
	return target.String()
}

// do sends one request. A status in accept decodes into out; anything else
// becomes *Error (or a transport error).
func (client *Client) do(ctx context.Context, method, path string, query url.Values, body, out any, accept ...int) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.endpoint(path, query), reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return response.StatusCode, err
	}
	if len(accept) == 0 {
		accept = []int{http.StatusOK}
	}
	for _, status := range accept {
		if response.StatusCode == status {
			if out == nil || len(data) == 0 {
				return response.StatusCode, nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				return response.StatusCode, fmt.Errorf("gateway %s %s: decode: %w", method, path, err)
			}
			return response.StatusCode, nil
		}
	}
	var envelope struct {
		Error *Error `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil {
		envelope.Error = &Error{Code: "HTTP_" + strconv.Itoa(response.StatusCode), Message: http.StatusText(response.StatusCode)}
	}
	envelope.Error.Status = response.StatusCode
	return response.StatusCode, envelope.Error
}

func (client *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	_, err := client.do(ctx, http.MethodGet, path, query, nil, out)
	return err
}

func account(id, path string) string { return "/v1/accounts/" + url.PathEscape(id) + path }

func (client *Client) Instruments(ctx context.Context) ([]Instrument, error) {
	var out []Instrument
	return out, client.get(ctx, "/v1/instruments", nil, &out)
}

// InstrumentsOf lists one product's instruments; options are listed only
// this way.
func (client *Client) InstrumentsOf(ctx context.Context, product string) ([]Instrument, error) {
	var out []Instrument
	return out, client.get(ctx, "/v1/instruments", url.Values{"product": {product}}, &out)
}

// OptionSeries lists every option series the gateway serves.
func (client *Client) OptionSeries(ctx context.Context) ([]OptionSeries, error) {
	var out []OptionSeries
	return out, client.get(ctx, "/v1/options", nil, &out)
}

// OptionChain reads one series' chain.
func (client *Client) OptionChain(ctx context.Context, series string) (OptionChain, error) {
	var out OptionChain
	return out, client.get(ctx, "/v1/options/chain", url.Values{"series": {series}}, &out)
}

func (client *Client) Accounts(ctx context.Context) ([]Account, error) {
	var out []Account
	return out, client.get(ctx, "/v1/accounts", nil, &out)
}

// Routes lists the instruments an account trades and the rules its orders
// follow there.
func (client *Client) Routes(ctx context.Context, accountID string) ([]Route, error) {
	var out []Route
	return out, client.get(ctx, account(accountID, "/instruments"), nil, &out)
}

// Margin is a broker account's pool; nil for a native account.
func (client *Client) Margin(ctx context.Context, accountID string) (*Margin, error) {
	var out *Margin
	return out, client.get(ctx, account(accountID, "/margin"), nil, &out)
}

// OpenOrders lists an account's resting orders on one instrument.
func (client *Client) OpenOrders(ctx context.Context, accountID, instrument string) ([]Order, error) {
	var out []Order
	return out, client.get(ctx, account(accountID, "/orders"), url.Values{"inst": {instrument}}, &out)
}

func (client *Client) Book(ctx context.Context, instrument string, depth int) (Book, error) {
	var out Book
	return out, client.get(ctx, "/v1/orderbook", url.Values{"inst": {instrument}, "depth": {strconv.Itoa(depth)}}, &out)
}

func (client *Client) Positions(ctx context.Context, accountID string) ([]Position, error) {
	var out []Position
	return out, client.get(ctx, account(accountID, "/positions"), nil, &out)
}

func (client *Client) Balances(ctx context.Context, accountID string) ([]Balance, error) {
	var out []Balance
	return out, client.get(ctx, account(accountID, "/balances"), nil, &out)
}

// Fills lists recent fills of one instrument since start.
func (client *Client) Fills(ctx context.Context, accountID, instrument string, start time.Time) ([]Fill, error) {
	query := url.Values{"inst": {instrument}, "limit": {"200"}}
	if !start.IsZero() {
		query.Set("start", strconv.FormatInt(start.UnixMilli(), 10))
	}
	var out []Fill
	return out, client.get(ctx, account(accountID, "/fills"), query, &out)
}

// Place sends one order. The result is the venue's typed outcome; an error is
// either Definite (nothing was sent) or ambiguous (look the order up by its
// client id before doing anything else — never place it again).
func (client *Client) Place(ctx context.Context, accountID string, request PlaceRequest) (PlaceResult, error) {
	var out PlaceResult
	_, err := client.do(ctx, http.MethodPost, account(accountID, "/orders"), nil, request, &out,
		http.StatusOK, http.StatusAccepted, http.StatusUnprocessableEntity)
	return out, err
}

func (client *Client) Lookup(ctx context.Context, accountID, instrument, clientOrderID string) (LookupResult, error) {
	var out LookupResult
	return out, client.get(ctx, account(accountID, "/orders/lookup"), url.Values{"inst": {instrument}, "clientOrderId": {clientOrderID}}, &out)
}

func (client *Client) Cancel(ctx context.Context, accountID, instrument, orderID string) error {
	_, err := client.do(ctx, http.MethodDelete, account(accountID, "/orders/"+url.PathEscape(orderID)), url.Values{"inst": {instrument}}, nil, nil)
	return err
}

// Candles reads one page of an instrument's candles ending at end (zero:
// now), oldest first; the venue caps the page (1000 at most).
func (client *Client) Candles(ctx context.Context, instrument, interval string, end time.Time, limit int) ([]Candle, error) {
	query := url.Values{"inst": {instrument}, "interval": {interval}}
	if !end.IsZero() {
		query.Set("end", strconv.FormatInt(end.UnixMilli(), 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var out []Candle
	return out, client.get(ctx, "/v1/candles", query, &out)
}

func (client *Client) Funding(ctx context.Context, instrument string) (FundingQuote, error) {
	var out FundingQuote
	return out, client.get(ctx, "/v1/funding", url.Values{"inst": {instrument}}, &out)
}

func (client *Client) FundingHistory(ctx context.Context, instrument string, since, until time.Time) ([]FundingPoint, error) {
	query := url.Values{"inst": {instrument}}
	if !since.IsZero() {
		query.Set("since", strconv.FormatInt(since.UnixMilli(), 10))
	}
	if !until.IsZero() {
		query.Set("until", strconv.FormatInt(until.UnixMilli(), 10))
	}
	var out []FundingPoint
	return out, client.get(ctx, "/v1/funding/history", query, &out)
}

var (
	idMu   sync.Mutex
	lastID uint64
)

// NewClientOrderID returns (unix_ms << 7) | random(0..127), strictly
// increasing within the process: a positive uint48 until 2039.
func NewClientOrderID() string {
	idMu.Lock()
	defer idMu.Unlock()
	id := uint64(time.Now().UnixMilli())<<7 | uint64(rand.IntN(128))
	if id <= lastID {
		id = lastID + 1
	}
	lastID = id
	return strconv.FormatUint(id, 10)
}
