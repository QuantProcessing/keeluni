package hedger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gwclient"
	"github.com/shopspring/decimal"
)

// A runner drives one action to completion. It is the only writer of its
// orders: placements, lookups and stream updates all arrive as events on
// one channel and are applied in order.
//
// Market mode places both legs concurrently. Limit mode rests a maker order
// on one leg and, every time it fills, hedges the filled amount on the other
// leg at market. Every order is written to the database before it is sent;
// a send whose outcome is unknown is looked up by its client order id and is
// never placed again.

type event struct {
	order  *gwclient.Order
	fill   *gwclient.Fill
	placed *placement
	looked *lookedUp
	cancel *cancelResult
}

type placement struct {
	clientOrderID string
	result        gwclient.PlaceResult
	err           error
}

type lookedUp struct {
	clientOrderID string
	result        gwclient.LookupResult
	err           error
}

type cancelResult struct {
	clientOrderID string
	err           error
}

type runner struct {
	engine  *Engine
	group   Group
	action  Action
	events  chan event
	control chan string
	done    chan struct{} // closed when run returns

	orders   map[string]*Order
	sequence []string
	fills    map[string][]Fill

	placing        map[string]bool
	looking        map[string]bool
	lastLookup     map[string]time.Time
	cancelSent     map[string]bool
	cancelAsked    bool
	failure        string // why the action must stop (a hedge was rejected)
	finished       bool
	placedOnce     bool
	unknownWarned  map[string]bool
	lastRemainder  decimal.Decimal
	makerAnnounced decimal.Decimal
}

func (r *runner) deliver(ev event) {
	select {
	case r.events <- ev:
	default:
		// A full queue only delays an update: the poll refreshes every order.
		slog.Warn("hedge runner queue full; update dropped", "action", r.action.ID)
	}
}

func (r *runner) run(ctx context.Context) {
	defer close(r.done)
	r.orders, r.fills = map[string]*Order{}, map[string][]Fill{}
	r.placing, r.looking, r.lastLookup, r.cancelSent, r.unknownWarned = map[string]bool{}, map[string]bool{}, map[string]time.Time{}, map[string]bool{}, map[string]bool{}
	if err := r.load(ctx); err != nil {
		slog.Error("hedge runner could not load its orders", "action", r.action.ID, "error", err)
		return
	}
	ticker := time.NewTicker(r.engine.pollEvery)
	defer ticker.Stop()
	r.refresh(ctx, true)
	r.step(ctx)
	for !r.finished {
		select {
		case <-ctx.Done():
			return
		case ev := <-r.events:
			r.apply(ctx, ev)
		case command := <-r.control:
			if command == "cancel" && !r.cancelAsked {
				r.cancelAsked = true
				r.engine.event(r.group.ID, "info", "Cancel requested")
			}
		case <-ticker.C:
			r.refresh(ctx, false)
		}
		r.step(ctx)
	}
}

func (r *runner) load(ctx context.Context) error {
	orders, err := r.engine.store.ActionOrders(ctx, r.action.ID)
	if err != nil {
		return err
	}
	for i := range orders {
		order := orders[i]
		r.orders[order.ClientOrderID] = &order
		r.sequence = append(r.sequence, order.ClientOrderID)
		r.engine.track(order.ClientOrderID, r.action.ID)
		// An order left in "sending" by a crash may or may not have reached
		// the venue.
		if order.State == OrderSending {
			order.State = OrderUnknown
			r.orders[order.ClientOrderID].State = OrderUnknown
		}
	}
	r.placedOnce = len(orders) > 0
	fills, err := r.engine.store.Fills(ctx, r.group.ID)
	if err != nil {
		return err
	}
	for _, fill := range fills {
		if _, mine := r.orders[fill.ClientOrderID]; mine {
			r.fills[fill.ClientOrderID] = append(r.fills[fill.ClientOrderID], fill)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Decisions
// ---------------------------------------------------------------------------

func (r *runner) step(ctx context.Context) {
	if r.finished {
		return
	}
	if r.action.Mode == ModeLimit {
		r.stepLimit(ctx)
	} else {
		r.stepMarket(ctx)
	}
}

// stepMarket places each leg's single market order once. It is idempotent
// across restarts: a leg is placed only if it has no order yet, so a crash
// between the two placements sends the missing leg on resume.
func (r *runner) stepMarket(ctx context.Context) {
	if r.failure == "" {
		for leg := 0; leg < 2; leg++ {
			if target := r.action.Target[leg]; !target.IsZero() && !r.hasOrder(leg) {
				r.place(ctx, leg, RoleTaker, "market", target, decimal.Zero, "")
			}
		}
	}
	if len(r.orders) == 0 {
		if r.failure != "" {
			r.finish(ctx, ActionFailed, r.failure)
		} else {
			r.finish(ctx, ActionDone, "nothing to trade")
		}
		return
	}
	if r.allTerminal() {
		r.finishMarket(ctx)
	}
}

func (r *runner) hasOrder(leg int) bool {
	for _, id := range r.sequence {
		if r.orders[id].Leg == leg {
			return true
		}
	}
	return false
}

func (r *runner) finishMarket(ctx context.Context) {
	executed := r.executed()
	var problems []string
	for leg := 0; leg < 2; leg++ {
		target := r.action.Target[leg].Abs()
		if target.IsZero() {
			continue
		}
		if executed[leg].Abs().LessThan(target) {
			problems = append(problems, fmt.Sprintf("leg %d filled %s of %s%s", leg+1, executed[leg].Abs(), target, r.reasonFor(leg)))
		}
	}
	if len(problems) == 0 {
		r.finish(ctx, ActionDone, "")
		return
	}
	r.finish(ctx, ActionFailed, strings.Join(problems, "; "))
}

// reasonFor is the rejection text of the leg's last unfilled order.
func (r *runner) reasonFor(leg int) string {
	for i := len(r.sequence) - 1; i >= 0; i-- {
		order := r.orders[r.sequence[i]]
		if order.Leg == leg && order.Error != "" {
			return " (" + order.Error + ")"
		}
	}
	return ""
}

func (r *runner) stepLimit(ctx context.Context) {
	maker := *r.action.MakerLeg
	hedge := 1 - maker
	makerOrder := r.makerOrder()
	if makerOrder == nil {
		if r.cancelAsked {
			r.finish(ctx, ActionCanceled, "canceled before the maker order was placed")
			return
		}
		if r.failure != "" {
			r.finish(ctx, ActionFailed, r.failure)
			return
		}
		price := r.action.LimitPrice
		r.place(ctx, maker, RoleMaker, "limit", r.action.Target[maker], price, r.action.TIF)
		return
	}

	// Hedge every maker fill not yet covered.
	makerFilled := r.effective(makerOrder).Qty
	if makerFilled.GreaterThan(r.makerAnnounced) {
		r.makerAnnounced = makerFilled
		r.engine.event(r.group.ID, "info", "Maker leg %d filled %s of %s", maker+1, makerFilled, r.action.Target[maker].Abs())
	}
	need := r.hedgeNeed(makerFilled)
	if r.failure == "" && need.IsPositive() && r.tradable(hedge, need) {
		sign := decimal.NewFromInt(1)
		if r.action.Target[hedge].IsNegative() {
			sign = sign.Neg()
		}
		r.place(ctx, hedge, RoleHedge, "market", need.Mul(sign), decimal.Zero, "")
	}

	// Stop resting when the operator cancels or a hedge was refused.
	if (r.cancelAsked || r.failure != "") && !OrderTerminal(makerOrder.State) {
		r.cancelMaker(ctx, makerOrder)
	}

	if !OrderTerminal(makerOrder.State) || r.hedgesInFlight() {
		return
	}
	// Maker done. Hedge any final remainder, unless it cannot be traded.
	remainder := r.hedgeNeed(makerFilled)
	if r.failure == "" && remainder.IsPositive() && r.tradable(hedge, remainder) {
		return // the next step places it
	}
	status, message := ActionDone, ""
	switch {
	case r.failure != "":
		status, message = ActionFailed, r.failure
	case r.cancelAsked:
		status, message = ActionCanceled, "canceled by the operator"
	case makerOrder.State == OrderRejected || makerOrder.State == OrderNotFound:
		status, message = ActionFailed, "maker order "+makerOrder.State+r.reasonFor(maker)
	case makerFilled.LessThan(r.action.Target[maker].Abs()):
		status, message = ActionCanceled, fmt.Sprintf("maker order %s after filling %s of %s", makerOrder.State, makerFilled, r.action.Target[maker].Abs())
	}
	if remainder.IsPositive() {
		r.engine.event(r.group.ID, "warn", "Residual %s on leg %d is below its minimum tradable size and stays unhedged", remainder, hedge+1)
	}
	r.finish(ctx, status, message)
}

func (r *runner) makerOrder() *Order {
	for _, id := range r.sequence {
		if r.orders[id].Role == RoleMaker {
			return r.orders[id]
		}
	}
	return nil
}

// hedgeNeed is the unsigned hedge-leg quantity still to send for makerFilled,
// floored to the hedge leg's step. In-flight hedges count as covered at their
// full size; finished ones at what they filled.
func (r *runner) hedgeNeed(makerFilled decimal.Decimal) decimal.Decimal {
	maker := *r.action.MakerLeg
	hedge := 1 - maker
	want := makerFilled.Mul(r.group.Ratio)
	if maker == 1 {
		want = makerFilled.Div(r.group.Ratio)
	}
	// Once the maker is completely filled the hedge leg goes exactly to its
	// own target, so a full close leaves no ratio-rounding dust behind.
	if makerFilled.GreaterThanOrEqual(r.action.Target[maker].Abs()) {
		want = r.action.Target[hedge].Abs()
	}
	want = decimal.Min(want, r.action.Target[hedge].Abs())
	covered := decimal.Zero
	for _, id := range r.sequence {
		order := r.orders[id]
		if order.Leg != hedge || order.Role != RoleHedge {
			continue
		}
		if OrderTerminal(order.State) {
			covered = covered.Add(r.effective(order).Qty)
		} else {
			covered = covered.Add(order.Qty)
		}
	}
	return FloorToStep(want.Sub(covered), r.group.Legs[hedge].StepSize)
}

// tradable reports whether qty clears the leg's venue minimums. Reducing
// orders are not held to the minimum value, which venues waive for them.
func (r *runner) tradable(leg int, qty decimal.Decimal) bool {
	definition := r.group.Legs[leg]
	if !qty.IsPositive() || qty.LessThan(definition.StepSize) {
		return false
	}
	if definition.MinQty.IsPositive() && qty.LessThan(definition.MinQty) {
		return false
	}
	return true
}

func (r *runner) hedgesInFlight() bool {
	for _, id := range r.sequence {
		if order := r.orders[id]; order.Role == RoleHedge && !OrderTerminal(order.State) {
			return true
		}
	}
	return false
}

func (r *runner) allTerminal() bool {
	for _, id := range r.sequence {
		if !OrderTerminal(r.orders[id].State) {
			return false
		}
	}
	return len(r.placing) == 0
}

func (r *runner) effective(order *Order) OrderFill {
	return EffectiveFill(*order, r.fills[order.ClientOrderID])
}

// executed is the signed quantity each leg traded in this action (positive:
// the leg's opening side).
func (r *runner) executed() [2]decimal.Decimal {
	var result [2]decimal.Decimal
	for _, id := range r.sequence {
		order := r.orders[id]
		qty := r.effective(order).Qty
		if order.Side != r.group.Legs[order.Leg].Side {
			qty = qty.Neg()
		}
		result[order.Leg] = result[order.Leg].Add(qty)
	}
	return result
}

// ---------------------------------------------------------------------------
// Venue calls
// ---------------------------------------------------------------------------

// place records an order, then sends it on its own goroutine. signed > 0
// trades the leg's opening side.
func (r *runner) place(ctx context.Context, leg int, role, orderType string, signed, price decimal.Decimal, tif string) {
	definition := r.group.Legs[leg]
	side := definition.TradeSide(signed)
	qty := FloorToStep(signed.Abs(), definition.StepSize)
	if !qty.IsPositive() {
		return
	}
	order := &Order{
		ClientOrderID: gwclient.NewClientOrderID(), GroupID: r.group.ID, ActionID: r.action.ID, Leg: leg, Role: role,
		AccountID: definition.AccountID, Instrument: definition.Instrument, Side: side, Type: orderType, Qty: qty,
		Price: price, TIF: tif, State: OrderSending, CreatedAt: r.engine.now().UTC(),
		RefPrice:   r.engine.refPrice(definition.Instrument, side),
		ReduceOnly: definition.Product == "perp" && side != definition.Side,
	}
	if orderType == "limit" {
		order.Price = tickToward(price, definition.TickSize, side)
	}
	// Record first: if the process dies mid-send, the client order id is what
	// lets the restart find out whether the order exists.
	if err := r.engine.store.InsertOrder(ctx, *order); err != nil {
		r.failure = "could not record the order: " + err.Error()
		r.engine.event(r.group.ID, "error", "Order for leg %d not sent: the database refused it (%s)", leg+1, err)
		return
	}
	r.orders[order.ClientOrderID] = order
	r.sequence = append(r.sequence, order.ClientOrderID)
	r.placing[order.ClientOrderID] = true
	r.engine.track(order.ClientOrderID, r.action.ID)
	request := gwclient.PlaceRequest{
		Instrument: order.Instrument, Side: order.Side, Type: order.Type, Quantity: order.Qty.String(),
		ReduceOnly: order.ReduceOnly, ClientOrderID: order.ClientOrderID,
	}
	if orderType == "limit" {
		request.Price, request.TimeInForce = order.Price.String(), tif
	}
	priceText := ""
	if orderType == "limit" {
		priceText = " @ " + order.Price.String()
	}
	r.engine.event(r.group.ID, "info", "%s %s %s %s on leg %d %s%s (client %s)", titleRole(role), orderType, order.Side, order.Qty, leg+1, definition.Instrument, priceText, order.ClientOrderID)
	go func() {
		// A send is never abandoned: abandoning turns a definite outcome
		// into an unknown one.
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		result, err := r.engine.gateway.Place(callCtx, order.AccountID, request)
		r.deliver(event{placed: &placement{clientOrderID: order.ClientOrderID, result: result, err: err}})
	}()
}

func titleRole(role string) string {
	switch role {
	case RoleMaker:
		return "Maker"
	case RoleHedge:
		return "Hedge"
	}
	return "Taker"
}

func (r *runner) cancelMaker(ctx context.Context, maker *Order) {
	if r.cancelSent[maker.ClientOrderID] || maker.VenueOrderID == "" {
		return // not yet known to the venue; the next lookup will tell
	}
	r.cancelSent[maker.ClientOrderID] = true
	go func() {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		err := r.engine.gateway.Cancel(callCtx, maker.AccountID, maker.Instrument, maker.VenueOrderID)
		r.deliver(event{cancel: &cancelResult{clientOrderID: maker.ClientOrderID, err: err}})
	}()
}

// refresh looks up every order whose state may be stale: unknown outcomes
// on a backoff schedule, live orders every poll — or every six polls while
// the order's account stream is live, since the stream then reports every
// change and the lookup is only a safety net.
func (r *runner) refresh(ctx context.Context, immediate bool) {
	now := r.engine.now()
	for _, id := range r.sequence {
		order := r.orders[id]
		if OrderTerminal(order.State) || r.placing[id] || r.looking[id] {
			continue
		}
		interval := r.engine.pollEvery
		if order.State != OrderUnknown && order.State != OrderSending && r.engine.market != nil && r.engine.market.AccountLive(order.AccountID) {
			interval *= 6
		}
		if !immediate && now.Sub(r.lastLookup[id]) < interval {
			continue
		}
		r.looking[id], r.lastLookup[id] = true, now
		go func(order Order) {
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			result, err := r.engine.gateway.Lookup(callCtx, order.AccountID, order.Instrument, order.ClientOrderID)
			r.deliver(event{looked: &lookedUp{clientOrderID: order.ClientOrderID, result: result, err: err}})
		}(*order)
	}
}

// ---------------------------------------------------------------------------
// Applying results
// ---------------------------------------------------------------------------

func (r *runner) apply(ctx context.Context, ev event) {
	switch {
	case ev.placed != nil:
		r.applyPlacement(ctx, *ev.placed)
	case ev.looked != nil:
		r.applyLookup(ctx, *ev.looked)
	case ev.order != nil:
		if order := r.orders[ev.order.ClientOrderID]; order != nil {
			r.merge(ctx, order, *ev.order, "")
		}
	case ev.fill != nil:
		r.applyFill(ctx, *ev.fill)
	case ev.cancel != nil:
		if ev.cancel.err != nil {
			r.engine.event(r.group.ID, "warn", "Cancel of the maker order failed: %s; checking its state", errorText(ev.cancel.err))
			delete(r.cancelSent, ev.cancel.clientOrderID)
			r.lastLookup[ev.cancel.clientOrderID] = time.Time{}
			r.refresh(ctx, false)
		}
	}
}

func (r *runner) applyPlacement(ctx context.Context, result placement) {
	delete(r.placing, result.clientOrderID)
	order := r.orders[result.clientOrderID]
	if order == nil {
		return
	}
	switch {
	case result.err != nil && gwclient.Definite(result.err):
		r.setTerminal(ctx, order, OrderRejected, errorText(result.err))
	case result.err != nil:
		r.markUnknown(ctx, order, errorText(result.err))
	case result.result.Status == gwclient.OutcomeAccepted && result.result.Order != nil:
		r.merge(ctx, order, *result.result.Order, "")
	case result.result.Status == gwclient.OutcomeRejected:
		reason := result.result.Message
		if reason == "" {
			reason = result.result.VenueMessage
		}
		if result.result.VenueCode != "" && !strings.Contains(reason, result.result.VenueCode) {
			reason += " (" + result.result.VenueCode + ")"
		}
		r.setTerminal(ctx, order, OrderRejected, reason)
	default:
		r.markUnknown(ctx, order, "outcome unknown")
	}
}

func (r *runner) markUnknown(ctx context.Context, order *Order, reason string) {
	order.State, order.Error = OrderUnknown, reason
	r.persist(ctx, order)
	if !r.unknownWarned[order.ClientOrderID] {
		r.unknownWarned[order.ClientOrderID] = true
		r.engine.event(r.group.ID, "warn", "Leg %d order outcome unknown (%s); checking with the exchange — it will not be placed again", order.Leg+1, reason)
	}
	// Look it up soon, on the unknown-outcome schedule. The schedule ends
	// with the runner; the poll keeps looking after it.
	go func(id string) {
		for _, delay := range r.engine.lookupDelays {
			select {
			case <-ctx.Done():
				return
			case <-r.done:
				return
			case <-time.After(delay):
			}
			r.deliver(event{looked: &lookedUp{clientOrderID: id, err: errScheduledLookup}})
		}
	}(order.ClientOrderID)
}

// errScheduledLookup asks the runner to look an unknown order up now.
var errScheduledLookup = errors.New("scheduled lookup")

func (r *runner) applyLookup(ctx context.Context, result lookedUp) {
	order := r.orders[result.clientOrderID]
	if order == nil {
		return
	}
	if errors.Is(result.err, errScheduledLookup) {
		if !OrderTerminal(order.State) && !r.looking[order.ClientOrderID] {
			r.lastLookup[order.ClientOrderID] = time.Time{}
			r.refresh(ctx, false)
		}
		return
	}
	delete(r.looking, result.clientOrderID)
	if result.err != nil {
		return // next poll retries
	}
	switch result.result.Status {
	case "found":
		if result.result.Order != nil {
			r.merge(ctx, order, *result.result.Order, "")
		}
	case gwclient.LookupNotFound:
		// A just-sent order may not be indexed yet: only an order old enough
		// is proven never placed.
		if r.engine.now().Sub(order.CreatedAt) >= notFoundAfter && (order.State == OrderUnknown || order.State == OrderSending) {
			r.setTerminal(ctx, order, OrderNotFound, "the exchange has no such order; it was never placed")
		}
	case gwclient.LookupUnconfirmed:
		r.reconcile(ctx, order)
	}
}

// notFoundAfter is how long after sending a "not found" lookup is trusted.
const notFoundAfter = 10 * time.Second

// unresolvedAfter bounds how long an order the venue can neither show nor
// deny is searched for in fills and open orders before the operator is asked.
const unresolvedAfter = 2 * time.Minute

// reconcile establishes an order the venue's lookup cannot confirm (Kraken
// forgets closed orders after seconds, Deribit labels after 30 minutes,
// CrossEx by text). Its fills prove what it executed; an open order with its
// client id proves it rests. When neither appears within unresolvedAfter the
// order is marked unresolved: the leg's size is then unknown, so the action
// stops and the group is left for the operator.
func (r *runner) reconcile(ctx context.Context, order *Order) {
	if OrderTerminal(order.State) {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if open, err := r.engine.gateway.OpenOrders(callCtx, order.AccountID, order.Instrument); err == nil {
		for _, candidate := range open {
			if candidate.ClientOrderID == order.ClientOrderID || (order.VenueOrderID != "" && candidate.OrderID == order.VenueOrderID) {
				r.merge(ctx, order, candidate, "")
				return
			}
		}
	}
	if fills, err := r.engine.gateway.Fills(callCtx, order.AccountID, order.Instrument, order.CreatedAt.Add(-time.Minute)); err == nil {
		matched := decimal.Zero
		var venueOrderID string
		for _, fill := range fills {
			if fill.ClientOrderID == order.ClientOrderID || (order.VenueOrderID != "" && fill.OrderID == order.VenueOrderID) {
				fill.ClientOrderID = order.ClientOrderID
				r.applyFill(ctx, fill)
				qty, _ := decimal.NewFromString(fill.Quantity)
				matched, venueOrderID = matched.Add(qty), fill.OrderID
			}
		}
		if matched.IsPositive() {
			// Executed and no longer open: filled, or the rest was cancelled.
			state := OrderFilled
			if matched.LessThan(order.Qty) {
				state = OrderCanceled
			}
			r.merge(ctx, order, gwclient.Order{OrderID: venueOrderID, Status: state, Filled: matched.String()}, "")
			return
		}
	}
	if r.engine.now().Sub(order.CreatedAt) >= unresolvedAfter {
		r.setTerminal(ctx, order, OrderUnresolved, "the exchange can neither show nor deny this order and no fill of it was found; check the venue before acting")
	}
}

func (r *runner) setTerminal(ctx context.Context, order *Order, state, reason string) {
	order.State, order.Error = state, reason
	r.persist(ctx, order)
	level := "warn"
	if state == OrderRejected {
		level = "error"
	}
	r.engine.event(r.group.ID, level, "Leg %d %s order %s: %s", order.Leg+1, order.Side, state, reason)
	if state == OrderRejected {
		r.noteRejection(order, reason)
	}
	if state == OrderUnresolved && r.failure == "" {
		r.failure = fmt.Sprintf("leg %d order outcome could not be established: %s", order.Leg+1, reason)
	}
}

// rejected records a rejection reported on the order itself.
func (r *runner) rejected(order *Order, reason string) {
	r.engine.event(r.group.ID, "error", "Leg %d %s order rejected: %s", order.Leg+1, order.Side, reason)
	r.noteRejection(order, reason)
}

// noteRejection stops the action on a rejected taker or hedge: continuing
// would only add exposure. A rejected maker ends the action by itself.
func (r *runner) noteRejection(order *Order, reason string) {
	if r.failure != "" {
		return
	}
	switch order.Role {
	case RoleHedge:
		r.failure = fmt.Sprintf("hedge on leg %d rejected: %s", order.Leg+1, reason)
	case RoleTaker:
		r.failure = fmt.Sprintf("leg %d rejected: %s", order.Leg+1, reason)
	}
}

// merge applies a venue report. Terminal states are final; the filled
// quantity never decreases.
func (r *runner) merge(ctx context.Context, order *Order, update gwclient.Order, _ string) {
	before := *order
	state := order.State
	applyVenueOrder(order, update)
	if OrderTerminal(state) && !OrderTerminal(order.State) {
		order.State = state
	}
	if order.State == OrderFilled && order.Filled.IsZero() {
		order.Filled = order.Qty // a fill report without a quantity
	}
	if order.State != OrderUnknown && order.State != OrderSending && order.State != OrderRejected && before.State == OrderUnknown {
		order.Error = ""
	}
	if order.State == OrderRejected && before.State != OrderRejected {
		// A broker accepts first and rejects later: the rejection arrives here,
		// not as the placement's answer. It stops the action the same way.
		reason := order.Error
		if reason == "" {
			reason = "rejected by the exchange"
		}
		r.persist(ctx, order)
		r.rejected(order, reason)
		return
	}
	if order.State == before.State && order.VenueOrderID == before.VenueOrderID && order.Error == before.Error &&
		order.Filled.Equal(before.Filled) && order.AvgPrice.Equal(before.AvgPrice) {
		return
	}
	r.persist(ctx, order)
	if order.State != before.State || !order.Filled.Equal(before.Filled) {
		price := ""
		if order.AvgPrice.IsPositive() {
			price = " @ " + order.AvgPrice.String()
		}
		if order.Filled.GreaterThan(before.Filled) || OrderTerminal(order.State) {
			r.engine.event(r.group.ID, "info", "Leg %d %s %s: %s/%s filled%s", order.Leg+1, order.Side, order.State, order.Filled, order.Qty, price)
		}
	}
}

func (r *runner) applyFill(ctx context.Context, fill gwclient.Fill) {
	converted := convertFill(fill)
	added, err := r.engine.store.AddFill(ctx, converted)
	if err != nil || !added {
		return
	}
	r.fills[converted.ClientOrderID] = append(r.fills[converted.ClientOrderID], converted)
	r.engine.changed(r.group.ID)
}

func (r *runner) persist(ctx context.Context, order *Order) {
	order.UpdatedAt = r.engine.now().UTC()
	if err := r.engine.store.UpdateOrder(ctx, *order); err != nil {
		slog.Error("hedge order update not stored", "order", order.ClientOrderID, "error", err)
	}
	r.engine.changed(r.group.ID)
}

// ---------------------------------------------------------------------------
// Completion
// ---------------------------------------------------------------------------

func (r *runner) finish(ctx context.Context, status, message string) {
	r.finished = true
	r.collectFills(ctx)
	if err := r.engine.store.FinishAction(ctx, r.action.ID, status, message); err != nil {
		slog.Error("hedge action not finished in the database", "action", r.action.ID, "error", err)
		return
	}
	switch status {
	case ActionDone:
		r.engine.event(r.group.ID, "info", "%s done", titleKind(r.action.Kind))
	case ActionCanceled:
		r.engine.event(r.group.ID, "info", "%s canceled: %s", titleKind(r.action.Kind), message)
	default:
		r.engine.event(r.group.ID, "error", "%s failed: %s", titleKind(r.action.Kind), message)
	}
	r.engine.settle(ctx, r.group.ID, r.action, message)
}

func titleKind(kind string) string {
	switch kind {
	case KindOpen:
		return "Open"
	case KindClose:
		return "Close"
	case KindUnwind:
		return "Unwind"
	case KindRehedge:
		return "Rehedge"
	}
	return kind
}

// collectFills reads the venues' fill records for this action's orders so
// fees and exact prices are known even when stream fill events were missed.
func (r *runner) collectFills(ctx context.Context) {
	type key struct{ account, instrument string }
	seen := map[key]bool{}
	for _, id := range r.sequence {
		order := r.orders[id]
		k := key{order.AccountID, order.Instrument}
		if seen[k] || r.effective(order).Qty.IsZero() {
			continue
		}
		seen[k] = true
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		fills, err := r.engine.gateway.Fills(callCtx, order.AccountID, order.Instrument, r.action.CreatedAt.Add(-time.Minute))
		cancel()
		if err != nil {
			continue
		}
		for _, fill := range fills {
			mine := r.orders[fill.ClientOrderID]
			if mine == nil {
				for _, candidate := range r.orders {
					if candidate.VenueOrderID != "" && candidate.VenueOrderID == fill.OrderID && candidate.AccountID == order.AccountID {
						mine = candidate
						break
					}
				}
			}
			if mine == nil {
				continue
			}
			fill.ClientOrderID = mine.ClientOrderID
			r.applyFill(ctx, fill)
		}
	}
}
