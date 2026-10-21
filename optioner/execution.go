package optioner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/shopspring/decimal"
)

// An Execution sends a package of legs — a spread, a straddle, a roll, a
// delta hedge — as one action. Each leg is its own venue order: venues in
// scope have no atomic combo book, so a package can fill partially, and the
// execution shows each leg's state rather than pretending otherwise.
type Execution struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	Status    string    `json:"status"` // working | done | partial | failed | canceled
	Legs      []ExecLeg `json:"legs"`
	CreatedAt int64     `json:"createdAt"`
	UpdatedAt int64     `json:"updatedAt"`
	Problem   string    `json:"problem,omitempty"`
}

// ExecLeg is one order of an execution.
type ExecLeg struct {
	Account    string `json:"account"`
	Instrument string `json:"inst"`
	Side       string `json:"side"` // buy | sell
	Quantity   string `json:"qty"`
	// Price is the limit in the instrument's premium/quote asset; empty
	// with Type market (perps and spot only).
	Price       string `json:"price,omitempty"`
	Type        string `json:"type"` // limit | market
	TimeInForce string `json:"tif,omitempty"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
	// PricedAt records how the price was chosen (mark, mid, iv:0.45, ...).
	PricedAt string `json:"pricedAt,omitempty"`

	ClientOrderID string `json:"clientOrderId"`
	OrderID       string `json:"orderId,omitempty"`
	// State: pending (not sent) | sent | open | partially_filled | filled |
	// canceled | rejected | unknown (outcome not provable).
	State    string `json:"state"`
	Filled   string `json:"filled"`
	AvgPrice string `json:"avgPrice,omitempty"`
	Message  string `json:"message,omitempty"`
}

// LegRequest is one leg as the operator asks for it.
type LegRequest struct {
	Account    string `json:"account"`
	Instrument string `json:"inst"`
	Side       string `json:"side"`
	Quantity   string `json:"qty"`
	// Pricing: an explicit price, or one of mark | mid | bid | ask | iv with
	// IV a fraction (0.45). The resolved limit is snapped to the tick, away
	// from the touch for passive orders and through it for IOC.
	Price       string  `json:"price,omitempty"`
	PriceBy     string  `json:"priceBy,omitempty"`
	IV          float64 `json:"iv,omitempty"`
	Type        string  `json:"type,omitempty"` // default limit
	TimeInForce string  `json:"tif,omitempty"`
	ReduceOnly  bool    `json:"reduceOnly,omitempty"`
}

// ExecRequest is a new execution.
type ExecRequest struct {
	Label string       `json:"label,omitempty"`
	Legs  []LegRequest `json:"legs"`
}

var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("execution not found")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// Executions owns working and recent executions.
type Executions struct {
	gateway Gateway
	state   *state

	mu    sync.Mutex
	items map[string]*Execution
	// byClient maps a leg's client order id to its execution.
	byClient map[string]string
	changes  chan string
}

func newExecutions(gateway Gateway, state *state) *Executions {
	return &Executions{gateway: gateway, state: state, items: map[string]*Execution{}, byClient: map[string]string{}, changes: make(chan string, 64)}
}

func newID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return "ex_" + hex.EncodeToString(raw)
}

// Prepare resolves prices and validates every leg against the instrument's
// rules without sending anything. The preview the ticket shows is this.
func (executions *Executions) Prepare(request ExecRequest) ([]ExecLeg, error) {
	if len(request.Legs) == 0 || len(request.Legs) > 8 {
		return nil, invalid("an execution has 1 to 8 legs")
	}
	legs := make([]ExecLeg, 0, len(request.Legs))
	for index, item := range request.Legs {
		leg, err := executions.prepareLeg(item)
		if err != nil {
			return nil, fmt.Errorf("leg %d: %w", index+1, err)
		}
		legs = append(legs, leg)
	}
	return legs, nil
}

func (executions *Executions) prepareLeg(request LegRequest) (ExecLeg, error) {
	instrument, ok := executions.state.instrument(request.Instrument)
	if !ok {
		return ExecLeg{}, invalid("unknown instrument %s", request.Instrument)
	}
	account, ok := executions.state.account(request.Account)
	if !ok || !account.Active {
		return ExecLeg{}, invalid("account %s is not connected", request.Account)
	}
	market := marketOf(instrument)
	trades := false
	for _, served := range account.Markets {
		if served == market {
			trades = true
		}
	}
	if !trades {
		return ExecLeg{}, invalid("account %s does not trade %s", account.Label, market)
	}
	if request.Side != "buy" && request.Side != "sell" {
		return ExecLeg{}, invalid("side must be buy or sell")
	}
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return ExecLeg{}, invalid("quantity must be positive")
	}
	step, _ := decimal.NewFromString(instrument.StepSize)
	if step.IsPositive() && !quantity.Mod(step).IsZero() {
		return ExecLeg{}, invalid("quantity %s is not a multiple of %s", quantity, step)
	}
	if minimum, _ := decimal.NewFromString(instrument.MinQty); quantity.LessThan(minimum) {
		return ExecLeg{}, invalid("quantity is below the minimum %s", minimum)
	}
	leg := ExecLeg{Account: request.Account, Instrument: request.Instrument, Side: request.Side, Quantity: quantity.String(),
		Type: request.Type, TimeInForce: request.TimeInForce, ReduceOnly: request.ReduceOnly, State: "pending", Filled: "0"}
	if leg.Type == "" {
		leg.Type = "limit"
	}
	if leg.Type == "market" {
		if instrument.Product == "option" {
			return ExecLeg{}, invalid("options trade with limit orders; use an IOC limit to take liquidity")
		}
		leg.TimeInForce = ""
		return leg, nil
	}
	if leg.Type != "limit" {
		return ExecLeg{}, invalid("type must be limit or market")
	}
	switch leg.TimeInForce {
	case "":
		leg.TimeInForce = "gtc"
	case "gtc", "ioc", "post_only":
	default:
		return ExecLeg{}, invalid("tif must be gtc, ioc or post_only")
	}
	price, by, err := executions.resolvePrice(instrument, request, leg.TimeInForce)
	if err != nil {
		return ExecLeg{}, err
	}
	leg.Price, leg.PricedAt = price, by
	return leg, nil
}

// resolvePrice turns a pricing choice into a limit on the tick grid. A buy
// rounds down and a sell up (never pay more than asked for), except IOC
// orders, which round toward the fill.
func (executions *Executions) resolvePrice(instrument gwclient.Instrument, request LegRequest, tif string) (string, string, error) {
	by := request.PriceBy
	var raw float64
	if request.Price != "" {
		value, err := strconv.ParseFloat(request.Price, 64)
		if err != nil || value <= 0 {
			return "", "", invalid("price must be positive")
		}
		raw, by = value, "price"
	} else {
		executions.state.mu.RLock()
		row, ok := executions.state.rows[instrument.ID]
		executions.state.mu.RUnlock()
		if !ok {
			return "", "", invalid("no market for %s yet; give a price", instrument.Symbol)
		}
		bid, ask, mark := parse(row.row.Bid), parse(row.row.Ask), parse(row.row.Mark)
		switch by {
		case "mark":
			raw = mark
		case "bid":
			raw = bid
		case "ask":
			raw = ask
		case "mid", "":
			by = "mid"
			if bid > 0 && ask > 0 {
				raw = (bid + ask) / 2
			} else {
				raw = mark
			}
		case "iv":
			if request.IV <= 0 || request.IV > 5 || instrument.Option == nil {
				return "", "", invalid("iv must be a fraction between 0 and 5")
			}
			terms := instrument.Option
			usd := row.model(time.Now()).Value(terms.Kind == "call", parse(terms.Strike), request.IV)
			raw = usd
			if row.inverse && row.forward > 0 {
				raw = usd / row.forward
			}
			by = "iv:" + strconv.FormatFloat(request.IV, 'f', 4, 64)
		default:
			return "", "", invalid("priceBy must be mark, mid, bid, ask or iv")
		}
		if raw <= 0 {
			return "", "", invalid("%s has no %s price", instrument.Symbol, by)
		}
	}
	tick := parse(instrument.TickSize)
	if coarse := parse(instrument.CoarseTick); coarse > 0 && raw >= parse(instrument.CoarseTickFrom) {
		tick = coarse
	}
	if tick <= 0 {
		return "", "", invalid("instrument has no tick size")
	}
	up := request.Side == "sell"
	if tif == "ioc" {
		up = !up
	}
	units := raw / tick
	if up {
		units = math.Ceil(units - 1e-9)
	} else {
		units = math.Floor(units + 1e-9)
	}
	if units < 1 {
		units = 1
	}
	price := decimal.NewFromFloat(units).Mul(decimal.RequireFromString(strconv.FormatFloat(tick, 'f', -1, 64)))
	return price.String(), by, nil
}

// Submit prepares and sends every leg in order. A leg the gateway refuses
// does not stop the rest: each leg is independent on the venue, and the
// execution reports exactly which filled.
func (executions *Executions) Submit(ctx context.Context, request ExecRequest) (Execution, error) {
	legs, err := executions.Prepare(request)
	if err != nil {
		return Execution{}, err
	}
	execution := &Execution{ID: newID(), Label: strings.TrimSpace(request.Label), Status: "working", Legs: legs, CreatedAt: now(), UpdatedAt: now()}
	executions.mu.Lock()
	for index := range execution.Legs {
		// The client order id is recorded before the request is sent.
		execution.Legs[index].ClientOrderID = gwclient.NewClientOrderID()
		executions.byClient[execution.Legs[index].ClientOrderID] = execution.ID
	}
	executions.items[execution.ID] = execution
	executions.mu.Unlock()
	for index := range execution.Legs {
		executions.send(ctx, execution.ID, index)
	}
	return executions.Get(execution.ID)
}

func (executions *Executions) send(ctx context.Context, id string, index int) {
	executions.mu.Lock()
	leg := executions.items[id].Legs[index]
	executions.mu.Unlock()
	request := gwclient.PlaceRequest{Instrument: leg.Instrument, Side: leg.Side, Type: leg.Type, Quantity: leg.Quantity, Price: leg.Price,
		TimeInForce: leg.TimeInForce, ReduceOnly: leg.ReduceOnly, ClientOrderID: leg.ClientOrderID}
	result, err := executions.gateway.Place(context.WithoutCancel(ctx), leg.Account, request)
	update := func(change func(*ExecLeg)) {
		executions.mu.Lock()
		change(&executions.items[id].Legs[index])
		executions.settle(executions.items[id])
		executions.mu.Unlock()
		executions.notify(id)
	}
	switch {
	case err != nil && gwclient.Definite(err):
		update(func(leg *ExecLeg) { leg.State, leg.Message = "rejected", err.Error() })
	case err != nil || result.Status == gwclient.OutcomeUnknown:
		update(func(leg *ExecLeg) { leg.State, leg.Message = "unknown", "outcome unknown; checking with the venue" })
		executions.resolve(ctx, id, index)
	case result.Status == gwclient.OutcomeRejected:
		message := result.VenueMessage
		if message == "" {
			message = result.Message
		}
		update(func(leg *ExecLeg) { leg.State, leg.Message = "rejected", message })
	default:
		update(func(leg *ExecLeg) {
			leg.State = "sent"
			if result.Order != nil {
				applyOrder(leg, *result.Order)
			}
		})
	}
}

// resolve looks an unknown order up by its client id. It never resends:
// "not found" is final only when the venue proves it.
func (executions *Executions) resolve(ctx context.Context, id string, index int) {
	executions.mu.Lock()
	leg := executions.items[id].Legs[index]
	executions.mu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		result, err := executions.gateway.Lookup(context.WithoutCancel(ctx), leg.Account, leg.Instrument, leg.ClientOrderID)
		if err != nil {
			continue
		}
		executions.mu.Lock()
		target := &executions.items[id].Legs[index]
		switch result.Status {
		case gwclient.LookupFound:
			if result.Order != nil {
				applyOrder(target, *result.Order)
				target.Message = ""
			}
		case gwclient.LookupNotFound:
			target.State, target.Message = "rejected", "the venue has no such order: it was not placed"
		}
		executions.settle(executions.items[id])
		executions.mu.Unlock()
		executions.notify(id)
		if result.Status != gwclient.LookupUnconfirmed {
			return
		}
	}
	slog.Warn("execution leg outcome unconfirmed", "execution", id, "clientOrderId", leg.ClientOrderID)
}

func applyOrder(leg *ExecLeg, order gwclient.Order) {
	if order.OrderID != "" {
		leg.OrderID = order.OrderID
	}
	if order.Filled != "" {
		leg.Filled = order.Filled
	}
	if order.AvgPrice != "" {
		leg.AvgPrice = order.AvgPrice
	}
	switch order.Status {
	case "pending":
		leg.State = "sent"
	case "rejected":
		leg.State = "rejected"
		leg.Message = order.RejectMessage
		if leg.Message == "" {
			leg.Message = order.RejectCode
		}
	default:
		leg.State = order.Status
	}
}

// settle derives the execution's status from its legs. Caller holds mu.
func (executions *Executions) settle(execution *Execution) {
	execution.UpdatedAt = now()
	working, filled, failed, canceled := 0, 0, 0, 0
	for _, leg := range execution.Legs {
		switch leg.State {
		case "filled":
			filled++
		case "rejected":
			failed++
		case "canceled":
			if parse(leg.Filled) > 0 {
				filled++
			} else {
				canceled++
			}
		default:
			working++
		}
	}
	total := len(execution.Legs)
	switch {
	case working > 0:
		execution.Status = "working"
	case filled == total:
		execution.Status = "done"
	case filled > 0:
		execution.Status = "partial"
		execution.Problem = "some legs did not fill: the package is not complete"
	case canceled > 0 && failed == 0:
		execution.Status = "canceled"
	default:
		execution.Status = "failed"
	}
}

// OnOrder applies an account stream order update to the leg that sent it.
func (executions *Executions) OnOrder(order gwclient.Order) {
	executions.mu.Lock()
	id, ok := executions.byClient[order.ClientOrderID]
	if !ok {
		executions.mu.Unlock()
		return
	}
	execution := executions.items[id]
	for index := range execution.Legs {
		if execution.Legs[index].ClientOrderID == order.ClientOrderID {
			applyOrder(&execution.Legs[index], order)
		}
	}
	executions.settle(execution)
	executions.mu.Unlock()
	executions.notify(id)
}

// Cancel cancels every resting leg of an execution.
func (executions *Executions) Cancel(ctx context.Context, id string) (Execution, error) {
	executions.mu.Lock()
	execution, ok := executions.items[id]
	if !ok {
		executions.mu.Unlock()
		return Execution{}, ErrNotFound
	}
	legs := append([]ExecLeg(nil), execution.Legs...)
	executions.mu.Unlock()
	var errs []error
	for _, leg := range legs {
		if leg.OrderID == "" || (leg.State != "open" && leg.State != "partially_filled" && leg.State != "sent") {
			continue
		}
		if err := executions.gateway.Cancel(ctx, leg.Account, leg.Instrument, leg.OrderID); err != nil {
			errs = append(errs, err)
		}
	}
	result, _ := executions.Get(id)
	return result, errors.Join(errs...)
}

func (executions *Executions) Get(id string) (Execution, error) {
	executions.mu.Lock()
	defer executions.mu.Unlock()
	execution, ok := executions.items[id]
	if !ok {
		return Execution{}, ErrNotFound
	}
	copied := *execution
	copied.Legs = append([]ExecLeg(nil), execution.Legs...)
	return copied, nil
}

// List returns executions of the last day, newest first.
func (executions *Executions) List() []Execution {
	executions.mu.Lock()
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	out := make([]Execution, 0, len(executions.items))
	for id, execution := range executions.items {
		if execution.UpdatedAt < cutoff && execution.Status != "working" {
			for _, leg := range execution.Legs {
				delete(executions.byClient, leg.ClientOrderID)
			}
			delete(executions.items, id)
			continue
		}
		copied := *execution
		copied.Legs = append([]ExecLeg(nil), execution.Legs...)
		out = append(out, copied)
	}
	executions.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (executions *Executions) notify(id string) {
	select {
	case executions.changes <- id:
	default:
	}
}
