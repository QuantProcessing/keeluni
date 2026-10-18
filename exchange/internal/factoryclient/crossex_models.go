package factoryclient

import (
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/shopspring/decimal"
)

// Portable order status words emitted for CrossEx orders. "pending" covers
// NEW: CrossEx accepted the order but has not placed it on the exchange, so
// it is neither resting nor final.
const (
	crossExStatusPending   = "pending"
	crossExStatusOpen      = "open"
	crossExStatusPartial   = "partially_filled"
	crossExStatusFilled    = "filled"
	crossExStatusCanceled  = "canceled"
	crossExStatusRejected  = "rejected"
	crossExStatusCancelled = "partially_filled_canceled"
)

// crossExOrderStatus maps a CrossEx state to a portable status word. A
// cancelled order that had fills keeps that fact for the caller.
func crossExOrderStatus(order crossex.Order) string {
	switch strings.ToUpper(strings.TrimSpace(order.State)) {
	case crossex.StateNew:
		return crossExStatusPending
	case crossex.StateOpen:
		return crossExStatusOpen
	case crossex.StatePartiallyFilled:
		return crossExStatusPartial
	case crossex.StateFilled:
		return crossExStatusFilled
	case crossex.StateCancelled:
		if crossExDecimal(order.ExecutedQty).IsPositive() {
			return crossExStatusCancelled
		}
		return crossExStatusCanceled
	case crossex.StateFail, crossex.StateReject:
		return crossExStatusRejected
	}
	return strings.ToLower(strings.TrimSpace(order.State))
}

// CrossExRejection explains a FAIL or REJECT order. Code is the CrossEx label
// for FAIL; REJECT carries only the exchange's raw text.
type CrossExRejection struct {
	ByExchange bool
	Code       string
	Message    string
}

func crossExOrderRejection(order crossex.Order) (CrossExRejection, bool) {
	switch strings.ToUpper(strings.TrimSpace(order.State)) {
	case crossex.StateFail:
		if reason, ok := order.FailReason(); ok {
			return CrossExRejection{Code: reason.Label, Message: cleanVenueMessage(reason.Message)}, true
		}
		return CrossExRejection{Message: cleanVenueMessage(order.Reason)}, true
	case crossex.StateReject:
		return CrossExRejection{ByExchange: true, Message: cleanVenueMessage(order.Reason)}, true
	}
	return CrossExRejection{}, false
}

func crossExOrder(row crossex.Order) exchange.Order {
	order := exchange.Order{
		NativeSymbol:  row.Symbol,
		OrderID:       row.OrderID,
		ClientOrderID: crossExPortableClientOrderID(row.Text),
		Side:          crossExSide(row.Side),
		Type:          crossExOrderType(row.Type),
		Quantity:      crossExDecimal(row.Qty),
		Filled:        crossExDecimal(row.ExecutedQty),
		ReduceOnly:    strings.EqualFold(strings.TrimSpace(row.ReduceOnly), "true"),
		Status:        crossExOrderStatus(row),
		CreatedAt:     crossExTimeMS(row.CreateTime),
		UpdatedAt:     crossExTimeMS(row.UpdateTime),
	}
	if order.Type == exchange.OrderTypeLimit {
		order.LimitPrice = crossExDecimal(row.Price)
		order.LimitPolicy = crossExLimitPolicy(row.TimeInForce)
	}
	if price := crossExDecimal(row.ExecutedAvgPrice); price.IsPositive() {
		order.AverageFillPrice = exchange.OptionalDecimal{Value: price, Valid: true}
	}
	return order
}

func crossExFill(row crossex.Trade) exchange.Fill {
	return exchange.Fill{
		NativeSymbol:  row.Symbol,
		OrderID:       row.OrderID,
		ClientOrderID: crossExPortableClientOrderID(row.Text),
		FillID:        row.TransactionID,
		Side:          crossExSide(row.Side),
		Price:         crossExDecimal(row.Price),
		Quantity:      crossExDecimal(row.Qty),
		// CrossEx reports fees as positive charges; a rebate is negative.
		Fee:       crossExDecimal(row.Fee),
		FeeAsset:  strings.ToUpper(strings.TrimSpace(row.FeeCoin)),
		Liquidity: crossExLiquidity(row.MatchRole),
		Time:      crossExTimeMS(row.CreateTime),
	}
}

// crossExPosition projects a single-mode position. position_qty is signed;
// NONE is the only side in single mode.
func crossExPosition(row crossex.Position) exchange.Position {
	quantity := crossExDecimal(row.PositionQty)
	slot := exchange.PositionSlotNet
	switch strings.ToUpper(strings.TrimSpace(row.PositionSide)) {
	case "LONG":
		slot, quantity = exchange.PositionSlotLong, quantity.Abs()
	case "SHORT":
		slot, quantity = exchange.PositionSlotShort, quantity.Abs().Neg()
	}
	position := exchange.Position{
		NativeSymbol:  row.Symbol,
		PositionSlot:  slot,
		Quantity:      quantity,
		EntryPrice:    crossExDecimal(row.EntryPrice),
		MarkPrice:     crossExDecimal(row.MarkPrice),
		UnrealizedPnL: crossExDecimal(row.Upnl),
	}
	switch {
	case quantity.IsPositive():
		position.Side = exchange.SideBuy
	case quantity.IsNegative():
		position.Side = exchange.SideSell
	}
	// CrossEx reports 0 for cross-margin positions: there is no per-position
	// liquidation price, so absence is the truth, not zero.
	if liq := crossExDecimal(row.LiqPrice); liq.IsPositive() {
		position.LiquidationPrice = exchange.OptionalDecimal{Value: liq, Valid: true}
	}
	if leverage := crossExDecimal(row.Leverage); leverage.IsPositive() {
		position.Leverage = exchange.OptionalDecimal{Value: leverage, Valid: true}
	}
	if margin := strings.TrimSpace(row.InitialMargin); margin != "" {
		position.MarginUsed = exchange.OptionalDecimal{Value: crossExDecimal(margin), Valid: true}
	}
	return position
}

// crossExPortableClientOrderID returns text only when it is a portable
// uint48 client order id; orders placed elsewhere carry CrossEx's own ids.
func crossExPortableClientOrderID(text string) string {
	text = strings.TrimSpace(text)
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil || value == 0 || value >= 1<<48 || strings.HasPrefix(text, "0") {
		return ""
	}
	return text
}

func crossExSide(value string) exchange.Side {
	if strings.EqualFold(strings.TrimSpace(value), "SELL") {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

func crossExOrderType(value string) exchange.OrderType {
	if strings.EqualFold(strings.TrimSpace(value), "MARKET") {
		return exchange.OrderTypeMarket
	}
	return exchange.OrderTypeLimit
}

func crossExLimitPolicy(value string) exchange.LimitPolicy {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "IOC", "FOK":
		return exchange.LimitPolicyIOC
	case "POC":
		return exchange.LimitPolicyPostOnly
	}
	return exchange.LimitPolicyResting
}

func crossExLiquidity(value string) exchange.Liquidity {
	if strings.EqualFold(strings.TrimSpace(value), "MAKER") {
		return exchange.LiquidityMaker
	}
	return exchange.LiquidityTaker
}

func crossExDecimal(value string) decimal.Decimal {
	parsed, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero
	}
	return parsed
}

func crossExTimeMS(value string) time.Time {
	millis, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

// crossExOrderRequest encodes a validated portable order. Only single
// position mode is supported, so position_side is always omitted (NONE).
func crossExOrderRequest(request exchange.PlaceOrderRequest) crossex.OrderRequest {
	out := crossex.OrderRequest{
		Text:   request.ClientOrderID,
		Symbol: request.Instrument.Identity.NativeSymbol,
		Side:   "BUY",
		Qty:    request.Quantity.String(),
	}
	if request.Side == exchange.SideSell {
		out.Side = "SELL"
	}
	if request.ReduceOnly {
		out.ReduceOnly = "true"
	}
	switch request.Type {
	case exchange.OrderTypeMarket:
		out.Type, out.TimeInForce = "MARKET", "IOC"
	default:
		out.Type, out.Price = "LIMIT", request.LimitPrice.String()
		switch request.LimitPolicy {
		case exchange.LimitPolicyIOC:
			out.TimeInForce = "IOC"
		case exchange.LimitPolicyPostOnly:
			out.TimeInForce = "POC"
		default:
			out.TimeInForce = "GTC"
		}
	}
	return out
}
