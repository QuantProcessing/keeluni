package factoryclient

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	bybit "github.com/QuantProcessing/keeluni/sdk/bybit"
	"github.com/shopspring/decimal"
)

// Bybit linear take-profit / stop-loss: a conditional market order on
// /v5/order/create (orderFilter StopOrder) with triggerPrice and
// triggerDirection (1: fires as the price rises to it, 2: as it falls),
// reduce-only, triggerBy LastPrice or MarkPrice. Its orderLinkId is the
// client order id; conditional orders list apart from ordinary ones.

var _ exchange.TriggerOrderREST = (*bybitPerpClient)(nil)

const bybitStopOrder = "StopOrder"

func (c *bybitPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (c *bybitPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := ctxErr(ctx, c.meta, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, invalid(c.meta, operation, safeTriggerMessage(err))
	}
	native, _, err := c.symbols(req.Instrument, operation)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	resp, err := c.sdk.PlaceOrder(ctx, bybit.PlaceOrderRequest{
		Category: c.category, Symbol: native, Side: bybitSide(req.Side), OrderType: "Market", Qty: req.Quantity.String(),
		ReduceOnly: true, OrderLinkID: req.ClientOrderID, TriggerPrice: req.TriggerPrice.String(),
		TriggerDirection: triggerDirection(req.Kind, req.Side), TriggerBy: bybitTriggerBy(req.PriceType), OrderFilter: bybitStopOrder,
	})
	if err != nil {
		return exchange.TriggerOrder{}, bybitBitgetTriggerCommandErr(c.meta, operation, native, req.ClientOrderID, err)
	}
	return exchange.TriggerOrder{
		NativeSymbol: native, OrderID: resp.OrderID, ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind,
		TriggerPrice: req.TriggerPrice, PriceType: req.PriceType, Quantity: req.Quantity, Status: "open",
	}, nil
}

func (c *bybitPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	if _, err := c.CancelOrder(ctx, req); err != nil {
		return withExchangeOperation(err, "CancelTriggerOrder")
	}
	return nil
}

func (c *bybitPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := ctxErr(ctx, c.meta, operation); err != nil {
		return nil, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, operation)
	if err != nil {
		return nil, err
	}
	settle := ""
	if native == "" {
		settle = c.settleCoin
	}
	rows, err := c.sdk.GetRealtimeOrdersFiltered(ctx, c.category, native, settle, "", "", 0, bybitStopOrder)
	if err != nil {
		return nil, normErr(c.meta, operation, err)
	}
	out := []exchange.TriggerOrder{}
	for _, row := range rows {
		if native != "" && row.Symbol != native {
			continue
		}
		if order, ok := bybitTriggerOrder(row); ok {
			out = append(out, order)
		}
	}
	return out, nil
}

func (c *bybitPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	native, _, err := c.symbols(instrument, operation)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", invalid(c.meta, operation, "client order id is required")
	}
	rows, err := c.sdk.GetRealtimeOrdersFiltered(ctx, c.category, native, "", "", clientOrderID, 0, bybitStopOrder)
	if err != nil {
		return exchange.TriggerOrder{}, "", normErr(c.meta, operation, err)
	}
	if len(rows) == 0 {
		history, err := c.sdk.GetOrderHistoryFilteredScoped(ctx, c.category, native, "", "", clientOrderID)
		if err != nil {
			return exchange.TriggerOrder{}, "", normErr(c.meta, operation, err)
		}
		rows = history
	}
	for _, row := range rows {
		if row.OrderLinkID != clientOrderID || row.Symbol != native {
			continue
		}
		if order, ok := bybitTriggerOrder(row); ok {
			return order, exchange.TriggerFound, nil
		}
		return exchange.TriggerOrder{}, "", malformed(c.meta, operation, "order is not a take-profit / stop-loss")
	}
	// Realtime covers open and recently closed orders, history the rest:
	// absent from both is authoritative.
	return exchange.TriggerOrder{}, exchange.TriggerNotFound, nil
}

// bybitIsTriggerRow tells conditional orders (resting until their trigger)
// from ordinary ones.
func bybitIsTriggerRow(row bybit.OrderRecord) bool {
	if row.OrderFilter == bybitStopOrder || row.OrderFilter == "tpslOrder" {
		return true
	}
	switch row.OrderStatus {
	case "Untriggered", "Deactivated":
		return true
	}
	return false
}

func bybitTriggerOrder(row bybit.OrderRecord) (exchange.TriggerOrder, bool) {
	if row.PositionIdx != 0 || !strings.EqualFold(row.OrderType, "Market") {
		return exchange.TriggerOrder{}, false
	}
	side := exchange.SideBuy
	if strings.EqualFold(row.Side, "Sell") {
		side = exchange.SideSell
	}
	trigger, err := decimal.NewFromString(row.TriggerPrice)
	if err != nil || !trigger.IsPositive() {
		return exchange.TriggerOrder{}, false
	}
	// The direction says which way the trigger fires; with the closing side
	// that is a take-profit or a stop-loss.
	kind := exchange.TriggerStopLoss
	switch {
	case strings.Contains(row.StopOrderType, "TakeProfit"):
		kind = exchange.TriggerTakeProfit
	case strings.Contains(row.StopOrderType, "StopLoss"):
		kind = exchange.TriggerStopLoss
	case row.TriggerDirection == 1 && side == exchange.SideSell, row.TriggerDirection == 2 && side == exchange.SideBuy:
		kind = exchange.TriggerTakeProfit
	}
	quantity, _ := decimal.NewFromString(row.Qty)
	order := exchange.TriggerOrder{
		NativeSymbol: row.Symbol, OrderID: row.OrderID, ClientOrderID: row.OrderLinkID, Side: side, Kind: kind,
		TriggerPrice: trigger, PriceType: exchange.TriggerPriceLast, Quantity: quantity,
		ClosePosition: row.CloseOnTrigger && quantity.IsZero(), Status: bybitTriggerStatus(row.OrderStatus),
	}
	switch row.TriggerBy {
	case "MarkPrice":
		order.PriceType = exchange.TriggerPriceMark
	case "IndexPrice":
		return exchange.TriggerOrder{}, false
	}
	if created, err := parseMilliString(row.CreatedTime); err == nil {
		order.CreatedAt = created
	}
	return order, true
}

func bybitTriggerStatus(status string) string {
	switch status {
	case "Untriggered", "New":
		return "open"
	case "Triggered":
		return "triggered"
	case "Filled":
		return "filled"
	case "Rejected":
		return "rejected"
	}
	return "canceled"
}

func bybitTriggerBy(priceType exchange.TriggerPriceType) string {
	if priceType == exchange.TriggerPriceMark {
		return "MarkPrice"
	}
	return "LastPrice"
}

// triggerDirection is Bybit's and Bitget's trigger direction: 1 fires as
// the price rises to the trigger, 2 as it falls. A take-profit closing a long
// (sell) and a stop-loss closing a short (buy) fire on a rise.
func triggerDirection(kind exchange.TriggerKind, side exchange.Side) int {
	rises := (kind == exchange.TriggerTakeProfit) == (side == exchange.SideSell)
	if rises {
		return 1
	}
	return 2
}

// parseMilliString reads a millisecond timestamp string.
func parseMilliString(value string) (time.Time, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return time.Time{}, errors.New("invalid millisecond timestamp")
	}
	return time.UnixMilli(parsed).UTC(), nil
}
