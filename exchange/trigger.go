package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// Trigger orders are a position's take-profit and stop-loss: resting on the
// venue, they send a reduce-only market order when the trigger price is
// crossed. They are an optional facet (TriggerOrderREST), discovered by type
// assertion, because not every venue has them (Gate CrossEx does not); a
// venue without it offers limit closes only.

// TriggerKind is which way the trigger closes the position.
type TriggerKind string

const (
	// TriggerTakeProfit closes in profit: a sell (closing a long) fires when
	// the price rises to the trigger, a buy (closing a short) when it falls.
	TriggerTakeProfit TriggerKind = "take_profit"
	// TriggerStopLoss closes at a loss: a sell fires when the price falls to
	// the trigger, a buy when it rises.
	TriggerStopLoss TriggerKind = "stop_loss"
)

// TriggerPriceType is the price the venue compares with the trigger.
type TriggerPriceType string

const (
	TriggerPriceLast TriggerPriceType = "last"
	TriggerPriceMark TriggerPriceType = "mark"
)

// TriggerOrderRequest places one take-profit or stop-loss. Side is the
// closing side; the order is always reduce-only and executes at market.
type TriggerOrderRequest struct {
	Instrument    Instrument       `json:"instrument"`
	ClientOrderID string           `json:"client_order_id,omitempty"`
	Side          Side             `json:"side,omitempty"`
	Kind          TriggerKind      `json:"kind,omitempty"`
	TriggerPrice  decimal.Decimal  `json:"trigger_price"`
	PriceType     TriggerPriceType `json:"price_type,omitempty"`
	Quantity      decimal.Decimal  `json:"quantity"`
}

func (request TriggerOrderRequest) Validate() error {
	if err := request.Instrument.Validate(); err != nil {
		return err
	}
	if !IsDerivative(request.Instrument.Identity.Product) {
		return invalidTrigger("trigger orders close derivative positions only")
	}
	if request.Side != SideBuy && request.Side != SideSell {
		return invalidTrigger("side must be buy or sell")
	}
	if request.Kind != TriggerTakeProfit && request.Kind != TriggerStopLoss {
		return invalidTrigger("kind must be take_profit or stop_loss")
	}
	if request.PriceType != TriggerPriceLast && request.PriceType != TriggerPriceMark {
		return invalidTrigger("price type must be last or mark")
	}
	if !request.TriggerPrice.IsPositive() {
		return invalidTrigger("trigger price must be positive")
	}
	if !decimalAligned(request.TriggerPrice, request.Instrument.Rules.TickAt(request.TriggerPrice)) {
		return invalidTrigger("trigger price is not aligned to instrument price increment")
	}
	if !request.Quantity.IsPositive() {
		return invalidTrigger("quantity must be positive")
	}
	if !decimalAligned(request.Quantity, request.Instrument.Rules.QuantityIncrement) {
		return invalidTrigger("quantity is not aligned to instrument quantity increment")
	}
	if !isPortableClientOrderID(request.ClientOrderID) {
		return invalidTrigger("client order id must be a positive decimal uint48")
	}
	return nil
}

// TriggerOrder is a trigger order as the venue reports it. Status is
// "open" while it waits; once it fires it leaves the open list and the
// market order it sent appears with the ordinary orders.
type TriggerOrder struct {
	NativeSymbol  string           `json:"native_symbol,omitempty"`
	NativeID      string           `json:"native_id,omitempty"`
	OrderID       string           `json:"order_id,omitempty"`
	ClientOrderID string           `json:"client_order_id,omitempty"`
	Side          Side             `json:"side,omitempty"`
	Kind          TriggerKind      `json:"kind,omitempty"`
	TriggerPrice  decimal.Decimal  `json:"trigger_price"`
	PriceType     TriggerPriceType `json:"price_type,omitempty"`
	Quantity      decimal.Decimal  `json:"quantity"`
	// ClosePosition is set when the order closes whatever position is open
	// when it fires rather than a fixed quantity (placed on the venue's own
	// interface).
	ClosePosition bool      `json:"close_position,omitempty"`
	Status        string    `json:"status,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// TriggerLookupStatus is the answer to "does this trigger order exist?".
type TriggerLookupStatus string

const (
	TriggerFound    TriggerLookupStatus = "found"
	TriggerNotFound TriggerLookupStatus = "not_found"
)

// TriggerOrderREST is the optional take-profit / stop-loss facet of a
// derivative client.
//
// PlaceTriggerOrder follows the order rules: a KindAmbiguousOutcome error
// means the venue may hold the order, and the caller must look it up by
// client order id before anything else. TriggerPriceTypes lists the trigger
// prices the venue compares with (Hyperliquid: mark only).
type TriggerOrderREST interface {
	TriggerPriceTypes() []TriggerPriceType
	PlaceTriggerOrder(context.Context, TriggerOrderRequest) (TriggerOrder, error)
	CancelTriggerOrder(context.Context, CancelOrderRequest) error
	OpenTriggerOrders(context.Context, OpenOrdersRequest) ([]TriggerOrder, error)
	LookupTriggerOrder(ctx context.Context, instrument Instrument, clientOrderID string) (TriggerOrder, TriggerLookupStatus, error)
}

func invalidTrigger(message string) error {
	return NewError(KindInvalidRequest, ErrorDetails{Operation: "TriggerOrderRequest.Validate", SafeMessage: message})
}
