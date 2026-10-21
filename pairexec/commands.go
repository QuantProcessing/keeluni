package pairexec

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/shopspring/decimal"
)

// LegInput selects one leg of a new hedge.
type LegInput struct {
	AccountID  string `json:"accountId"`
	Instrument string `json:"inst"`
	Side       string `json:"side"` // buy (long) | sell (short)
}

// OpenRequest opens a new hedge.
type OpenRequest struct {
	Legs   [2]LegInput     `json:"legs"`
	Size   decimal.Decimal `json:"size"`
	Unit   string          `json:"unit"` // base | quote
	Mode   string          `json:"mode"` // market | limit
	Maker  int             `json:"makerLeg"`
	Price  decimal.Decimal `json:"limitPrice"`
	TIF    string          `json:"tif"` // gtc | post_only
	Policy string          `json:"policy"`
	Label  string          `json:"label"`
	Note   string          `json:"note"`
}

// CloseRequest closes part or all of an open hedge.
type CloseRequest struct {
	// Fraction of the current holdings to close, (0, 1]. Default 1.
	Fraction decimal.Decimal `json:"fraction"`
	Mode     string          `json:"mode"`
	Maker    int             `json:"makerLeg"`
	Price    decimal.Decimal `json:"limitPrice"`
	TIF      string          `json:"tif"`
}

// marketOf names an instrument's market as the gateway does:
// venue:product[:settlement].
func marketOf(instrument gwclient.Instrument) string {
	if instrument.Product == "spot" {
		return instrument.Venue + ":spot"
	}
	settle := instrument.Settle
	if settle == "" {
		settle = instrument.Quote
	}
	return instrument.Venue + ":perp:" + strings.ToUpper(settle)
}

// resolveLeg checks a leg against the gateway catalog and accounts. An
// account trades the leg when it is active, its markets include the
// instrument's, and its routes list the instrument — a native account on the
// venue itself, or a broker (Gate CrossEx) that reaches it. The leg takes the
// rules of that route: a broker can trade finer steps than the venue and set
// its own minimum value. A preview may leave the account empty and then sizes
// with the venue's rules.
func (engine *Engine) ResolveLeg(ctx context.Context, index int, input LegInput, instruments map[string]gwclient.Instrument, accounts map[string]gwclient.Account, requireAccount bool) (Leg, error) {
	instrument, ok := instruments[input.Instrument]
	if !ok {
		return Leg{}, Invalid("invalid.legUnknownInst", "leg", index+1, "inst", input.Instrument)
	}
	rules := gwclient.Route{Instrument: instrument.ID, TickSize: instrument.TickSize, StepSize: instrument.StepSize, MinQty: instrument.MinQty, MinNotional: instrument.MinNotional}
	broker := false
	if requireAccount || input.AccountID != "" {
		account, ok := accounts[input.AccountID]
		if !ok {
			return Leg{}, Invalid("invalid.legUnknownAccount", "leg", index+1, "account", input.AccountID)
		}
		if !account.Active {
			return Leg{}, Invalid("invalid.legInactive", "leg", index+1, "account", account.Label)
		}
		if !slices.Contains(account.Markets, marketOf(instrument)) {
			return Leg{}, Invalid("invalid.legMarket", "leg", index+1, "account", account.Label, "market", marketOf(instrument))
		}
		route, err := engine.route(ctx, account.ID, instrument.ID)
		if err != nil {
			return Leg{}, err
		}
		if route == nil {
			return Leg{}, Invalid("invalid.legRoute", "leg", index+1, "account", account.Label, "inst", instrument.ID)
		}
		rules, broker = *route, account.Broker()
	}
	if input.Side != "buy" && input.Side != "sell" {
		return Leg{}, Invalid("invalid.legSide", "leg", index+1)
	}
	if instrument.Product == "spot" && input.Side == "sell" {
		return Leg{}, Invalid("invalid.legSpotShort", "leg", index+1)
	}
	parse := func(text string) decimal.Decimal {
		value, _ := decimal.NewFromString(text)
		return value
	}
	leg := Leg{
		Index: index, AccountID: input.AccountID, Broker: broker, Instrument: instrument.ID, Venue: instrument.Venue, Product: instrument.Product,
		Base: instrument.Base, Quote: instrument.Quote, Side: input.Side,
		StepSize: parse(rules.StepSize), TickSize: parse(rules.TickSize), MinQty: parse(rules.MinQty), MinNotional: parse(rules.MinNotional),
	}
	if instrument.Settle != "" && instrument.Product == "perp" {
		leg.Quote = instrument.Settle
	}
	if !leg.StepSize.IsPositive() || !leg.TickSize.IsPositive() {
		return Leg{}, Invalid("invalid.legRules", "leg", index+1)
	}
	return leg, nil
}

// route is the account's rules for one instrument, or nil when the account
// does not trade it.
func (engine *Engine) route(ctx context.Context, accountID, instrumentID string) (*gwclient.Route, error) {
	routes, err := engine.gateway.Routes(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("gateway routes of %s: %w", accountID, err)
	}
	for _, route := range routes {
		if route.Instrument == instrumentID {
			return &route, nil
		}
	}
	return nil, nil
}

func (engine *Engine) Catalog(ctx context.Context) (map[string]gwclient.Instrument, map[string]gwclient.Account, error) {
	instruments, err := engine.gateway.Instruments(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway instruments: %w", err)
	}
	accounts, err := engine.gateway.Accounts(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway accounts: %w", err)
	}
	byID := make(map[string]gwclient.Instrument, len(instruments))
	for _, instrument := range instruments {
		byID[instrument.ID] = instrument
	}
	byAccount := make(map[string]gwclient.Account, len(accounts))
	for _, account := range accounts {
		byAccount[account.ID] = account
	}
	return byID, byAccount, nil
}

func validateMode(mode string, maker int, price decimal.Decimal, tif string) (string, error) {
	switch mode {
	case ModeMarket:
		return "", nil
	case ModeLimit:
		if maker != 0 && maker != 1 {
			return "", Invalid("invalid.makerLeg")
		}
		if !price.IsPositive() {
			return "", Invalid("invalid.limitPrice")
		}
		switch tif {
		case "", "gtc":
			return "gtc", nil
		case "post_only":
			return "post_only", nil
		}
		return "", Invalid("invalid.tif")
	}
	return "", Invalid("invalid.mode")
}

// Open validates and starts a new hedge. Sizing uses the live books, so the
// stored quantities are exactly what is sent.
func (engine *Engine) Open(ctx context.Context, market *Market, request OpenRequest) (Group, error) {
	instruments, accounts, err := engine.Catalog(ctx)
	if err != nil {
		return Group{}, err
	}
	var legs [2]Leg
	for i := range legs {
		if legs[i], err = engine.ResolveLeg(ctx, i, request.Legs[i], instruments, accounts, true); err != nil {
			return Group{}, err
		}
	}
	if legs[0].Instrument == legs[1].Instrument && legs[0].AccountID == legs[1].AccountID {
		return Group{}, Invalid("invalid.sameLegs")
	}
	match, ratio := MatchLegs(legs)
	if legs[0].Side == legs[1].Side {
		return Group{}, Invalid("invalid.sameSide", "side", legs[0].Side)
	}
	tif, err := validateMode(request.Mode, request.Maker, request.Price, request.TIF)
	if err != nil {
		return Group{}, err
	}
	policy := request.Policy
	if policy == "" {
		policy = PolicyUnwind
	}
	if policy != PolicyUnwind && policy != PolicyHold {
		return Group{}, Invalid("invalid.policy")
	}
	var prices [2]decimal.Decimal
	for i, leg := range legs {
		_, _, ok := market.Book(ctx, leg.Instrument, 5*time.Second)
		if !ok {
			return Group{}, Invalid("invalid.noBook", "inst", leg.Instrument)
		}
		prices[i] = market.TradePrice(leg.Instrument, leg.Side)
	}
	if request.Mode == ModeLimit {
		prices[request.Maker] = request.Price
	}
	plan, err := PlanSize(SizeInput{Size: request.Size, Unit: request.Unit, Match: match, Price: prices, Ratio: ratio}, legs)
	if err != nil {
		return Group{}, err
	}
	if len(plan.Problems) > 0 {
		return Group{}, errInvalid{text: plan.Problems[0]}
	}
	now := engine.now().UTC()
	group := Group{
		ID: newID("hg_"), Label: strings.TrimSpace(request.Label), Note: strings.TrimSpace(request.Note),
		Status: StatusOpening, Policy: policy, Match: match, Ratio: plan.Ratio, Legs: legs, CreatedAt: now, UpdatedAt: now,
	}
	if group.Label == "" {
		group.Label = defaultLabel(legs)
	}
	action := Action{ID: newID("ha_"), GroupID: group.ID, Kind: KindOpen, Mode: request.Mode, TIF: tif, Target: plan.Qty, Status: ActionWorking, CreatedAt: now}
	if request.Mode == ModeLimit {
		maker := request.Maker
		action.MakerLeg, action.LimitPrice = &maker, request.Price
	}
	if err := engine.store.CreateGroup(ctx, group, action); err != nil {
		return Group{}, err
	}
	engine.event(group.ID, "info", textOf("event.opening",
		"side0", legs[0].Side, "qty0", plan.Qty[0], "inst0", legs[0].Instrument,
		"side1", legs[1].Side, "qty1", plan.Qty[1], "inst1", legs[1].Instrument, "ratio", plan.Ratio, "mode", request.Mode))
	engine.spawn(group, action)
	return group, nil
}

func defaultLabel(legs [2]Leg) string {
	name := func(leg Leg) string {
		direction := "L"
		if leg.Side == "sell" {
			direction = "S"
		}
		return fmt.Sprintf("%s %s %s", direction, leg.Base, leg.Venue)
	}
	return name(legs[0]) + " / " + name(legs[1])
}

// Close starts closing a fraction of a group's current holdings.
func (engine *Engine) Close(ctx context.Context, groupID string, request CloseRequest) (Action, error) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return Action{}, err
	}
	if group.Status != StatusOpen && group.Status != StatusBroken {
		return Action{}, Invalid("invalid.cannotClose", "status", group.Status)
	}
	fraction := request.Fraction
	if fraction.IsZero() {
		fraction = decimal.NewFromInt(1)
	}
	if !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return Action{}, Invalid("invalid.fraction")
	}
	tif, err := validateMode(request.Mode, request.Maker, request.Price, request.TIF)
	if err != nil {
		return Action{}, err
	}
	var target [2]decimal.Decimal
	full := fraction.Equal(decimal.NewFromInt(1))
	for i, leg := range group.Legs {
		qty := books[i].Held
		if !full {
			qty = FloorToStep(books[i].Held.Mul(fraction), leg.StepSize)
		}
		target[i] = qty.Neg()
	}
	if !full && !target[0].IsZero() && !target[1].IsZero() {
		// Keep the remaining legs in ratio: derive leg 1 from leg 0.
		target[1] = FloorToStep(target[0].Abs().Mul(group.Ratio), group.Legs[1].StepSize).Neg()
		target[1] = decimal.Max(target[1], books[1].Held.Neg())
	}
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, Invalid("invalid.nothingToClose")
	}
	if request.Mode == ModeLimit && target[request.Maker].IsZero() {
		return Action{}, Invalid("invalid.makerNothing")
	}
	action := Action{ID: newID("ha_"), GroupID: group.ID, Kind: KindClose, Mode: request.Mode, TIF: tif, Target: target, Status: ActionWorking, CreatedAt: engine.now().UTC()}
	if request.Mode == ModeLimit {
		maker := request.Maker
		action.MakerLeg, action.LimitPrice = &maker, request.Price
	}
	if err := engine.store.StartAction(ctx, action, StatusClosing); err != nil {
		if err == ErrBusy {
			return Action{}, Invalid("invalid.busy")
		}
		return Action{}, err
	}
	group.Status = StatusClosing
	engine.event(group.ID, "info", textOf("event.closing", "pct", fraction.Mul(decimal.NewFromInt(100)).StringFixed(0), "qty0", target[0].Abs(), "qty1", target[1].Abs(), "mode", request.Mode))
	engine.spawn(group, action)
	return action, nil
}

// Rehedge restores an unbalanced group to full size: the smaller leg is
// topped up at market to match the larger one.
func (engine *Engine) Rehedge(ctx context.Context, groupID string) (Action, error) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return Action{}, err
	}
	if group.Status != StatusBroken && group.Status != StatusOpen {
		return Action{}, Invalid("invalid.cannotRehedge", "status", group.Status)
	}
	if Negligible(group, Imbalance(group, books)) {
		return Action{}, Invalid("invalid.balanced")
	}
	target := RehedgeTargets(group, books)
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, Invalid("invalid.belowStep")
	}
	return engine.startRepair(ctx, group, KindRehedge, target, withReason(textOf("event.rehedging"), describeTargets(group, target)))
}

// Trim balances an unbalanced group by reducing the larger leg to match the
// smaller one (a full unwind when the smaller leg is empty).
func (engine *Engine) Trim(ctx context.Context, groupID string) (Action, error) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return Action{}, err
	}
	if group.Status != StatusBroken && group.Status != StatusOpen {
		return Action{}, Invalid("invalid.cannotTrim", "status", group.Status)
	}
	if Negligible(group, Imbalance(group, books)) {
		return Action{}, Invalid("invalid.balanced")
	}
	target := TrimTargets(group, books)
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, Invalid("invalid.nothingToTrim")
	}
	return engine.startRepair(ctx, group, KindUnwind, target, withReason(textOf("event.trimming"), describeTargets(group, target)))
}

// describeTargets lists what a repair trades: "leg 1 sell 0.004 at market".
func describeTargets(group Group, target [2]decimal.Decimal) Text {
	var text Text
	for i, qty := range target {
		if qty.IsZero() {
			continue
		}
		part := textOf("event.target", "leg", i+1, "side", group.Legs[i].TradeSide(qty), "qty", qty.Abs())
		if text.IsZero() {
			text = part
		} else {
			text = joinTexts(text, part)
		}
	}
	return text
}

// Unwind closes every held quantity of both legs at market.
func (engine *Engine) Unwind(ctx context.Context, groupID string) (Action, error) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return Action{}, err
	}
	if group.Status != StatusBroken && group.Status != StatusOpen {
		return Action{}, Invalid("invalid.cannotUnwind", "status", group.Status)
	}
	target := [2]decimal.Decimal{books[0].Held.Neg(), books[1].Held.Neg()}
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, Invalid("invalid.nothingHeld")
	}
	return engine.startRepair(ctx, group, KindUnwind, target, textOf("event.unwinding", "qty0", target[0].Abs(), "qty1", target[1].Abs()))
}

func (engine *Engine) startRepair(ctx context.Context, group Group, kind string, target [2]decimal.Decimal, message Text) (Action, error) {
	action := Action{ID: newID("ha_"), GroupID: group.ID, Kind: kind, Mode: ModeMarket, Target: target, Status: ActionWorking, CreatedAt: engine.now().UTC()}
	status := StatusClosing
	if kind == KindRehedge {
		status = StatusOpening
	}
	if err := engine.store.StartAction(ctx, action, status); err != nil {
		if err == ErrBusy {
			return Action{}, Invalid("invalid.busy")
		}
		return Action{}, err
	}
	group.Status = status
	engine.event(group.ID, "info", message)
	engine.spawn(group, action)
	return action, nil
}

// snapshot loads a group and derives its books.
func (engine *Engine) snapshot(ctx context.Context, groupID string) (Group, [2]LegBook, error) {
	group, err := engine.store.Group(ctx, groupID)
	if err != nil {
		return Group{}, [2]LegBook{}, err
	}
	orders, err := engine.store.Orders(ctx, groupID)
	if err != nil {
		return Group{}, [2]LegBook{}, err
	}
	fills, err := engine.store.Fills(ctx, groupID)
	if err != nil {
		return Group{}, [2]LegBook{}, err
	}
	return group, Accounts(group, orders, fills), nil
}

// statusFor derives a group's status from what it holds: closed when flat
// (failed if nothing ever filled), open when the legs match as closely as
// the venues allow, broken otherwise.
func statusFor(group Group, books [2]LegBook, message Text) (string, Text) {
	flat := books[0].Held.IsZero() && books[1].Held.IsZero()
	switch {
	case flat && books[0].Opened.IsZero() && books[1].Opened.IsZero():
		return StatusFailed, message
	case flat:
		return StatusClosed, Text{}
	case Negligible(group, Imbalance(group, books)):
		return StatusOpen, Text{}
	}
	problem := textOf("problem.unbalanced", "held0", books[0].Held, "held1", books[1].Held, "need", books[0].Held.Mul(group.Ratio))
	return StatusBroken, withReason(problem, message)
}

// recheck re-derives an idle group's status after an update that arrived
// outside any action, e.g. a fill reported after its action finished.
func (engine *Engine) recheck(ctx context.Context, groupID string) {
	if _, working := engine.Working(groupID); working {
		return
	}
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return
	}
	status, problem := statusFor(group, books, textOf("problem.lateFill"))
	if status == group.Status || status == StatusFailed {
		return
	}
	if engine.store.SetGroupStatus(ctx, groupID, status, problem) == nil {
		level := "warn"
		if status == StatusBroken {
			level = "error"
		}
		engine.event(groupID, level, textOf("event.late", "from", group.Status, "to", status))
	}
}

// settle decides a group's status after an action finished, and applies the
// unhedged-leg policy.
func (engine *Engine) settle(ctx context.Context, groupID string, action Action, message Text) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return
	}
	status, problem := statusFor(group, books, message)
	if err := engine.store.SetGroupStatus(ctx, groupID, status, problem); err != nil {
		return
	}
	engine.changed(groupID)
	if engine.observer != nil {
		group.Status, group.Problem = status, problem
		finished := action
		if updated, err := engine.store.Action(ctx, action.ID); err == nil {
			finished = updated
		}
		engine.observer.ActionSettled(group, finished, status)
	}
	if status != StatusBroken {
		return
	}
	engine.event(groupID, "error", withReason(textOf("event.broken"), problem))
	// An order whose outcome could not be established leaves the leg's real
	// size unknown: repairing from our own books could make it worse.
	if engine.hasUnresolved(ctx, groupID) {
		engine.event(groupID, "error", textOf("event.repairSkipped"))
		return
	}
	// Policy: when an open or a close leaves the legs unequal, trim the larger
	// leg to the smaller one — for an open, the hedge shrinks to what filled
	// (a full unwind if one leg filled nothing); for a close, the close is
	// completed. Repairs are never retried automatically, so a venue that
	// keeps refusing leaves the group broken for the operator.
	if group.Policy == PolicyUnwind && (action.Kind == KindOpen || action.Kind == KindClose) {
		if _, err := engine.Trim(ctx, groupID); err != nil {
			engine.event(groupID, "error", withReason(textOf("event.trimNotStarted"), errText(err)))
		}
	}
}

func (engine *Engine) hasUnresolved(ctx context.Context, groupID string) bool {
	orders, err := engine.store.Orders(ctx, groupID)
	if err != nil {
		return true
	}
	for _, order := range orders {
		if order.State == OrderUnresolved {
			return true
		}
	}
	return false
}

// refPrice is the book price a market order of side starts at (for slippage
// reporting); zero when no book is cached.
func (engine *Engine) refPrice(instrument, side string) decimal.Decimal {
	if engine.market == nil {
		return decimal.Zero
	}
	return engine.market.TradePrice(instrument, side)
}

// ---------------------------------------------------------------------------
// Venue reconciliation
// ---------------------------------------------------------------------------

// venueMismatchAfter is how long a disagreement between a leg and the venue
// position must persist before the group is marked unhedged: fills and
// position snapshots arrive on different streams and can cross briefly.
const venueMismatchAfter = 20 * time.Second

// ReconcileVenue compares every idle open group's legs with the venues'
// positions and marks a group broken when a venue holds less of a leg than
// the hedge accounts for: the leg was closed outside the hedge, liquidated or
// auto-deleveraged. A venue holding more is only shown (the account may hold
// other positions in the instrument), and spot legs are not compared
// (balances, not positions). Venue positions are the account stream's
// snapshots; accounts whose stream is down are skipped.
func (engine *Engine) ReconcileVenue(ctx context.Context) {
	if engine.market == nil {
		return
	}
	groups, err := engine.store.Groups(ctx, engine.now())
	if err != nil {
		return
	}
	now := engine.now()
	for _, group := range groups {
		if group.Status != StatusOpen {
			engine.clearMismatch(group.ID)
			continue
		}
		if _, working := engine.Working(group.ID); working {
			continue
		}
		_, books, err := engine.snapshot(ctx, group.ID)
		if err != nil {
			continue
		}
		var problem Text
		for i, leg := range group.Legs {
			if leg.Product != "perp" || !books[i].Held.IsPositive() || !engine.market.AccountLive(leg.AccountID) {
				continue
			}
			position, held, known := engine.market.Position(leg.AccountID, leg.Instrument)
			if !known {
				continue
			}
			venue := decimal.Zero
			if held {
				size, _ := decimal.NewFromString(position.Size)
				if (position.Side == "long") == (leg.Side == "buy") {
					venue = size
				} else {
					venue = size.Neg()
				}
			}
			// Tolerate one step of rounding between venue and hedge.
			if venue.Add(leg.StepSize).LessThan(books[i].Held) {
				problem = textOf("problem.venueShort", "leg", i+1, "venue", venue, "base", leg.Base, "inst", leg.Instrument, "held", books[i].Held)
				break
			}
		}
		if problem.IsZero() {
			engine.clearMismatch(group.ID)
			continue
		}
		since := engine.noteMismatch(group.ID, now)
		if now.Sub(since) < venueMismatchAfter {
			continue
		}
		engine.clearMismatch(group.ID)
		if err := engine.store.SetGroupStatus(ctx, group.ID, StatusBroken, problem); err == nil {
			engine.event(group.ID, "error", withReason(textOf("event.unhedged"), problem))
		}
	}
}

func (engine *Engine) noteMismatch(groupID string, now time.Time) time.Time {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if since, ok := engine.mismatch[groupID]; ok {
		return since
	}
	engine.mismatch[groupID] = now
	return now
}

func (engine *Engine) clearMismatch(groupID string) {
	engine.mu.Lock()
	delete(engine.mismatch, groupID)
	engine.mu.Unlock()
}
