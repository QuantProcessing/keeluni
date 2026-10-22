package sdk

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

func (c *Client) ListSpotAccounts(ctx context.Context, currency string) ([]SpotAccount, error) {
	query := map[string]string{"currency": currency}
	var out []SpotAccount
	err := c.getPrivate(ctx, "/spot/accounts", query, &out)
	return out, err
}

func (c *Client) GetUnifiedMode(ctx context.Context) (*UnifiedMode, error) {
	var out UnifiedMode
	err := c.getPrivate(ctx, "/unified/unified_mode", nil, &out)
	return &out, err
}

func (c *Client) GetUnifiedAccount(ctx context.Context, currency string) (*UnifiedAccount, error) {
	query := map[string]string{"currency": currency}
	var out UnifiedAccount
	err := c.getPrivate(ctx, "/unified/accounts", query, &out)
	return &out, err
}

func (c *Client) CreateSpotOrder(ctx context.Context, order Order) (*Order, error) {
	var out Order
	err := c.postPrivate(ctx, "/spot/orders", order, &out)
	if err == nil && out.ID == "" {
		err = fmt.Errorf("gate sdk: create spot order returned a partial response without order id")
	}
	if err == nil && order.Text != "" && out.Text != "" && out.Text != order.Text {
		err = fmt.Errorf("gate sdk: create spot order returned mismatched client text %q for %q", out.Text, order.Text)
	}
	if err == nil && order.CurrencyPair != "" && out.CurrencyPair != "" && out.CurrencyPair != order.CurrencyPair {
		err = fmt.Errorf("gate sdk: create spot order returned mismatched currency pair %q for %q", out.CurrencyPair, order.CurrencyPair)
	}
	return &out, err
}

func (c *Client) ListSpotOpenOrders(ctx context.Context, currencyPair string) ([]Order, error) {
	query := map[string]string{
		"currency_pair": currencyPair,
		"status":        "open",
	}
	var out []Order
	err := c.getPrivate(ctx, "/spot/orders", query, &out)
	return out, err
}

func (c *Client) ListAllSpotOpenOrders(ctx context.Context, page, limit int) ([]OpenOrders, error) {
	query := map[string]string{"account": "spot"}
	if page > 0 {
		query["page"] = strconv.Itoa(page)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []OpenOrders
	err := c.getPrivate(ctx, "/spot/open_orders", query, &out)
	return out, err
}

func (c *Client) GetSpotOrder(ctx context.Context, orderID, currencyPair string) (*Order, error) {
	var out Order
	err := c.getPrivate(ctx, "/spot/orders/"+url.PathEscape(orderID), map[string]string{"currency_pair": currencyPair}, &out)
	return &out, err
}

func (c *Client) CancelSpotOrder(ctx context.Context, orderID, currencyPair string) (*Order, error) {
	var out Order
	err := c.deletePrivate(ctx, "/spot/orders/"+url.PathEscape(orderID), map[string]string{"currency_pair": currencyPair}, &out)
	if err == nil && (out.ID == "" || out.ID != orderID) {
		err = fmt.Errorf("gate sdk: cancel spot order returned mismatched order id %q for %q", out.ID, orderID)
	}
	return &out, err
}

func (c *Client) ListSpotMyTrades(ctx context.Context, currencyPair, orderID string, limit int) ([]SpotUserTrade, error) {
	query := map[string]string{}
	if currencyPair != "" {
		query["currency_pair"] = currencyPair
	}
	if orderID != "" {
		query["order_id"] = orderID
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []SpotUserTrade
	err := c.getPrivate(ctx, "/spot/my_trades", query, &out)
	return out, err
}

func (c *Client) GetFuturesAccount(ctx context.Context, settle string) (*FuturesAccount, error) {
	var out FuturesAccount
	err := c.getPrivate(ctx, futuresPath(settle, "accounts"), nil, &out)
	return &out, err
}

func (c *Client) ListPositions(ctx context.Context, settle string, holding bool) ([]Position, error) {
	query := map[string]string{}
	if holding {
		query["holding"] = "true"
	}
	var out []Position
	err := c.getPrivate(ctx, futuresPath(settle, "positions"), query, &out)
	return out, err
}

func (c *Client) CreateFuturesOrder(ctx context.Context, settle string, order FuturesOrder) (*FuturesOrder, error) {
	var out FuturesOrder
	err := c.postPrivate(ctx, futuresPath(settle, "orders"), order, &out)
	if err == nil && out.ID == 0 {
		err = fmt.Errorf("gate sdk: create futures order returned a partial response without order id")
	}
	if err == nil && order.Text != "" && out.Text != "" && out.Text != order.Text {
		err = fmt.Errorf("gate sdk: create futures order returned mismatched client text %q for %q", out.Text, order.Text)
	}
	if err == nil && order.Contract != "" && out.Contract != "" && out.Contract != order.Contract {
		err = fmt.Errorf("gate sdk: create futures order returned mismatched contract %q for %q", out.Contract, order.Contract)
	}
	return &out, err
}

func (c *Client) ListFuturesOpenOrders(ctx context.Context, settle, contract string) ([]FuturesOrder, error) {
	query := map[string]string{
		"status":   "open",
		"contract": contract,
	}
	var out []FuturesOrder
	err := c.getPrivate(ctx, futuresPath(settle, "orders"), query, &out)
	return out, err
}

func (c *Client) GetFuturesOrder(ctx context.Context, settle string, orderID int64) (*FuturesOrder, error) {
	return c.GetFuturesOrderByID(ctx, settle, strconv.FormatInt(orderID, 10))
}

func (c *Client) GetFuturesOrderByID(ctx context.Context, settle, orderID string) (*FuturesOrder, error) {
	var out FuturesOrder
	err := c.getPrivate(ctx, futuresPath(settle, "orders", orderID), nil, &out)
	return &out, err
}

func (c *Client) CancelFuturesOrder(ctx context.Context, settle string, orderID int64) (*FuturesOrder, error) {
	var out FuturesOrder
	err := c.deletePrivate(ctx, futuresPath(settle, "orders", strconv.FormatInt(orderID, 10)), nil, &out)
	if err == nil && out.ID != orderID {
		err = fmt.Errorf("gate sdk: cancel futures order returned mismatched order id %d for %d", out.ID, orderID)
	}
	return &out, err
}

func (c *Client) ListMyFuturesTrades(ctx context.Context, settle, contract string, limit int) ([]MyFuturesTrade, error) {
	query := map[string]string{}
	if contract != "" {
		query["contract"] = contract
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []MyFuturesTrade
	err := c.getPrivate(ctx, futuresPath(settle, "my_trades"), query, &out)
	return out, err
}

// FuturesPriceOrder is a futures price-triggered (take-profit / stop-loss)
// order: Initial is the order sent when Trigger fires.
type FuturesPriceOrder struct {
	Initial    FuturesPriceInitial `json:"initial"`
	Trigger    FuturesPriceTrigger `json:"trigger"`
	ID         int64               `json:"id,omitempty"`
	CreateTime float64             `json:"create_time,omitempty"`
	FinishTime float64             `json:"finish_time,omitempty"`
	Status     string              `json:"status,omitempty"` // open | finished | inactive | invalid
	FinishAs   string              `json:"finish_as,omitempty"`
	Reason     string              `json:"reason,omitempty"`
	OrderType  string              `json:"order_type,omitempty"`
}

// FuturesPriceInitial: Size in contracts (negative closes a long), Price
// "0" with TIF ioc for a market order.
type FuturesPriceInitial struct {
	Contract     string `json:"contract"`
	Size         int64  `json:"size"`
	Price        string `json:"price"`
	Close        bool   `json:"close,omitempty"`
	TIF          string `json:"tif,omitempty"`
	Text         string `json:"text,omitempty"`
	ReduceOnly   bool   `json:"reduce_only,omitempty"`
	AutoSize     string `json:"auto_size,omitempty"`
	IsReduceOnly bool   `json:"is_reduce_only,omitempty"`
	IsClose      bool   `json:"is_close,omitempty"`
}

// FuturesPriceTrigger: PriceType 0 last, 1 mark, 2 index; Rule 1 fires at
// or above Price, 2 at or below.
type FuturesPriceTrigger struct {
	StrategyType int32  `json:"strategy_type"`
	PriceType    int32  `json:"price_type"`
	Price        string `json:"price"`
	Rule         int32  `json:"rule"`
	Expiration   int32  `json:"expiration,omitempty"`
}

// CreateFuturesPriceOrder places a price-triggered order; it answers with
// the order id only.
func (c *Client) CreateFuturesPriceOrder(ctx context.Context, settle string, order FuturesPriceOrder) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.postPrivate(ctx, futuresPath(settle, "price_orders"), order, &out); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("gate sdk: create futures price order returned a partial response without order id")
	}
	return out.ID, nil
}

// ListFuturesPriceOrders lists price-triggered orders of one status (open
// or finished).
func (c *Client) ListFuturesPriceOrders(ctx context.Context, settle, status, contract string, limit int) ([]FuturesPriceOrder, error) {
	query := map[string]string{"status": status, "contract": contract}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []FuturesPriceOrder
	err := c.getPrivate(ctx, futuresPath(settle, "price_orders"), query, &out)
	return out, err
}

// CancelFuturesPriceOrder cancels one price-triggered order.
func (c *Client) CancelFuturesPriceOrder(ctx context.Context, settle string, orderID int64) (*FuturesPriceOrder, error) {
	var out FuturesPriceOrder
	err := c.deletePrivate(ctx, futuresPath(settle, "price_orders", strconv.FormatInt(orderID, 10)), nil, &out)
	if err == nil && out.ID != 0 && out.ID != orderID {
		err = fmt.Errorf("gate sdk: cancel futures price order returned mismatched order id %d for %d", out.ID, orderID)
	}
	return &out, err
}
