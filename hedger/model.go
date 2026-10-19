// Package hedger is the hedge-trading app: it opens, tracks and closes a
// position made of two legs on (possibly) different venues as one unit. It
// trades only through the gateway and keeps its own state in PostgreSQL.
//
// A group's state is derived from its orders and fills, never asserted: the
// filled quantity of each leg is the sum of its fills, so a restart, a lost
// response or a stream gap cannot make the app believe in a hedge that the
// venues do not hold.
package hedger

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Group statuses.
const (
	StatusOpening = "opening" // an open action is working
	StatusOpen    = "open"    // both legs held, no action working
	StatusClosing = "closing" // a close or unwind action is working
	StatusClosed  = "closed"  // both legs flat
	StatusFailed  = "failed"  // the open never filled; nothing is held
	StatusBroken  = "broken"  // one leg is exposed and needs the operator
)

// Unhedged-leg policies when the second leg of a market action cannot fill.
const (
	PolicyUnwind = "unwind" // reverse the filled leg at market
	PolicyHold   = "hold"   // keep it and mark the group broken
)

// Size matching between legs.
const (
	MatchBase     = "base"     // same base quantity × ratio (same underlying)
	MatchNotional = "notional" // same quote notional at entry (different underlyings)
)

// Action kinds and modes.
const (
	KindOpen    = "open"
	KindClose   = "close"
	KindUnwind  = "unwind"
	KindRehedge = "rehedge"

	ModeMarket = "market" // both legs at market, concurrently
	ModeLimit  = "limit"  // maker leg rests; every maker fill is hedged at market
)

// Action statuses.
const (
	ActionWorking  = "working"
	ActionDone     = "done"
	ActionCanceled = "canceled"
	ActionFailed   = "failed"
)

// Order states. "sending" is written before the request; "unknown" means the
// send may have reached the venue and the outcome must be looked up.
const (
	OrderSending  = "sending"
	OrderUnknown  = "unknown"
	OrderOpen     = "open"
	OrderPartial  = "partially_filled"
	OrderFilled   = "filled"
	OrderCanceled = "canceled"
	OrderRejected = "rejected"
	OrderNotFound = "not_found"
	OrderPending  = "pending"
	// OrderUnresolved: the venue can neither show the order nor prove its
	// absence (it forgets closed orders), and no fill or open order of it
	// was found. It is final for the action; its group is left for the
	// operator because the leg's true size is unknown.
	OrderUnresolved = "unresolved"
)

// OrderTerminal reports whether an order can no longer fill.
func OrderTerminal(state string) bool {
	switch state {
	case OrderFilled, OrderCanceled, OrderRejected, OrderNotFound, OrderUnresolved:
		return true
	}
	return false
}

// Order roles within an action.
const (
	RoleMaker = "maker" // resting limit order of a limit action
	RoleTaker = "taker" // market order of a market action
	RoleHedge = "hedge" // market order hedging a maker fill
)

// Leg is one side of a hedge.
type Leg struct {
	Index     int    `json:"leg"`
	AccountID string `json:"accountId"`
	// Broker is set when the account is a broker (Gate CrossEx): the leg's
	// margin is the broker's shared pool, and its rules are the broker's.
	Broker      bool            `json:"broker,omitempty"`
	Instrument  string          `json:"inst"`
	Venue       string          `json:"venue"`
	Product     string          `json:"product"`
	Base        string          `json:"base"`
	Quote       string          `json:"quote"`
	Side        string          `json:"side"` // opening side: buy (long) | sell (short)
	StepSize    decimal.Decimal `json:"stepSize"`
	TickSize    decimal.Decimal `json:"tickSize"`
	MinQty      decimal.Decimal `json:"minQty"`
	MinNotional decimal.Decimal `json:"minNotional"`
}

// Direction is +1 for a long leg and -1 for a short one.
func (leg Leg) Direction() int64 {
	if leg.Side == "buy" {
		return 1
	}
	return -1
}

// CloseSide is the side that reduces the leg.
func (leg Leg) CloseSide() string {
	if leg.Side == "buy" {
		return "sell"
	}
	return "buy"
}

// Group is one hedge.
type Group struct {
	ID        string          `json:"id"`
	Label     string          `json:"label"`
	Note      string          `json:"note"`
	Status    string          `json:"status"`
	Policy    string          `json:"policy"`
	Match     string          `json:"match"`
	Ratio     decimal.Decimal `json:"ratio"`
	Problem   string          `json:"problem,omitempty"`
	Legs      [2]Leg          `json:"legs"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	ClosedAt  *time.Time      `json:"closedAt,omitempty"`
}

// Action is one open/close/unwind/rehedge of a group.
type Action struct {
	ID         string          `json:"id"`
	GroupID    string          `json:"groupId"`
	Kind       string          `json:"kind"`
	Mode       string          `json:"mode"`
	MakerLeg   *int            `json:"makerLeg,omitempty"`
	LimitPrice decimal.Decimal `json:"limitPrice"`
	TIF        string          `json:"tif,omitempty"`
	// Target is the signed base quantity each leg trades in this action:
	// positive trades the leg's opening side, negative its closing side.
	Target    [2]decimal.Decimal `json:"target"`
	Status    string             `json:"status"`
	Error     string             `json:"error,omitempty"`
	CreatedAt time.Time          `json:"createdAt"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

// TradeSide is the venue side for a signed quantity on a leg.
func (leg Leg) TradeSide(signed decimal.Decimal) string {
	if signed.IsNegative() {
		return leg.CloseSide()
	}
	return leg.Side
}

// Order is one venue order placed by the hedger.
type Order struct {
	ClientOrderID string          `json:"clientOrderId"`
	GroupID       string          `json:"groupId"`
	ActionID      string          `json:"actionId"`
	Leg           int             `json:"leg"`
	Role          string          `json:"role"`
	AccountID     string          `json:"accountId"`
	Instrument    string          `json:"inst"`
	Side          string          `json:"side"`
	Type          string          `json:"type"`
	Qty           decimal.Decimal `json:"qty"`
	Price         decimal.Decimal `json:"price"`
	RefPrice      decimal.Decimal `json:"refPrice"` // book price when sent (slippage reference)
	TIF           string          `json:"tif,omitempty"`
	ReduceOnly    bool            `json:"reduceOnly,omitempty"`
	State         string          `json:"state"`
	VenueOrderID  string          `json:"orderId,omitempty"`
	Filled        decimal.Decimal `json:"filled"`
	AvgPrice      decimal.Decimal `json:"avgPrice"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// Fill is one execution of a hedger order.
type Fill struct {
	ClientOrderID string          `json:"clientOrderId"`
	FillID        string          `json:"fillId"`
	Price         decimal.Decimal `json:"price"`
	Qty           decimal.Decimal `json:"qty"`
	Fee           decimal.Decimal `json:"fee"`
	FeeAsset      string          `json:"feeAsset,omitempty"`
	Liquidity     string          `json:"liquidity,omitempty"`
	Time          time.Time       `json:"ts"`
}

// Event is one line of a group's audit trail.
type Event struct {
	ID      int64     `json:"id"`
	GroupID string    `json:"groupId"`
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// FundingAccrual is one leg's estimated funding at one settlement; Amount > 0
// was received.
type FundingAccrual struct {
	GroupID   string          `json:"groupId"`
	Leg       int             `json:"leg"`
	SettledAt time.Time       `json:"settledAt"`
	Rate      decimal.Decimal `json:"rate"`
	Qty       decimal.Decimal `json:"qty"`
	Price     decimal.Decimal `json:"price"`
	Amount    decimal.Decimal `json:"amount"`
}

// FloorToStep rounds a positive quantity down to a multiple of step.
func FloorToStep(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() || !value.IsPositive() {
		return decimal.Zero
	}
	return value.Div(step).Floor().Mul(step)
}

// RoundToTick rounds a price to the nearest multiple of tick.
func RoundToTick(value, tick decimal.Decimal) decimal.Decimal {
	if !tick.IsPositive() {
		return value
	}
	return value.Div(tick).Round(0).Mul(tick)
}

// tickToward rounds a limit price so it never crosses the requested level:
// down for a buy, up for a sell.
func tickToward(value, tick decimal.Decimal, side string) decimal.Decimal {
	if !tick.IsPositive() {
		return value
	}
	units := value.Div(tick)
	if side == "buy" {
		return units.Floor().Mul(tick)
	}
	return units.Ceil().Mul(tick)
}

// errInvalid is a request the hedger refuses before anything is sent.
type errInvalid struct{ message string }

func (e errInvalid) Error() string { return e.message }

func invalidf(format string, args ...any) error {
	return errInvalid{message: fmt.Sprintf(format, args...)}
}

// IsInvalid reports whether err is a refused request (HTTP 400).
func IsInvalid(err error) bool {
	_, ok := err.(errInvalid)
	return ok
}
