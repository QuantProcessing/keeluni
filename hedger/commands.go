package hedger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gwclient"
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
	Unit   string          `json:"unit"`  // base | quote
	Match  string          `json:"match"` // base | notional
	Ratio  decimal.Decimal `json:"ratio"` // MatchBase only; default 1
	Mode   string          `json:"mode"`  // market | limit
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

// resolveLeg checks a leg against the gateway catalog and accounts. A
// preview may leave the account empty: sizing and costs need only the
// instrument.
func (engine *Engine) resolveLeg(ctx context.Context, index int, input LegInput, instruments map[string]gwclient.Instrument, accounts map[string]gwclient.Account, requireAccount bool) (Leg, error) {
	instrument, ok := instruments[input.Instrument]
	if !ok {
		return Leg{}, invalidf("leg %d: unknown instrument %q", index+1, input.Instrument)
	}
	if requireAccount || input.AccountID != "" {
		account, ok := accounts[input.AccountID]
		if !ok {
			return Leg{}, invalidf("leg %d: unknown account %q", index+1, input.AccountID)
		}
		if !account.Active {
			return Leg{}, invalidf("leg %d: account %s is not active", index+1, account.Label)
		}
		if account.Venue != instrument.Venue {
			return Leg{}, invalidf("leg %d: account %s trades on %s, not %s", index+1, account.Label, account.Venue, instrument.Venue)
		}
	}
	if input.Side != "buy" && input.Side != "sell" {
		return Leg{}, invalidf("leg %d: side must be buy or sell", index+1)
	}
	if instrument.Product == "spot" && input.Side == "sell" {
		return Leg{}, invalidf("leg %d: a spot leg can only be long (shorting spot needs margin borrowing)", index+1)
	}
	parse := func(text string) decimal.Decimal {
		value, _ := decimal.NewFromString(text)
		return value
	}
	leg := Leg{
		Index: index, AccountID: input.AccountID, Instrument: instrument.ID, Venue: instrument.Venue, Product: instrument.Product,
		Base: instrument.Base, Quote: instrument.Quote, Side: input.Side,
		StepSize: parse(instrument.StepSize), TickSize: parse(instrument.TickSize), MinQty: parse(instrument.MinQty), MinNotional: parse(instrument.MinNotional),
	}
	if instrument.Settle != "" && instrument.Product == "perp" {
		leg.Quote = instrument.Settle
	}
	if !leg.StepSize.IsPositive() || !leg.TickSize.IsPositive() {
		return Leg{}, invalidf("leg %d: instrument rules are incomplete", index+1)
	}
	return leg, nil
}

func (engine *Engine) catalog(ctx context.Context) (map[string]gwclient.Instrument, map[string]gwclient.Account, error) {
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
			return "", invalidf("maker leg must be 0 or 1")
		}
		if !price.IsPositive() {
			return "", invalidf("limit mode needs a positive limit price for the maker leg")
		}
		switch tif {
		case "", "gtc":
			return "gtc", nil
		case "post_only":
			return "post_only", nil
		}
		return "", invalidf("tif must be gtc or post_only")
	}
	return "", invalidf("mode must be market or limit")
}

// Open validates and starts a new hedge. Sizing uses the live books, so the
// stored quantities are exactly what is sent.
func (engine *Engine) Open(ctx context.Context, market *Market, request OpenRequest) (Group, error) {
	instruments, accounts, err := engine.catalog(ctx)
	if err != nil {
		return Group{}, err
	}
	var legs [2]Leg
	for i := range legs {
		if legs[i], err = engine.resolveLeg(ctx, i, request.Legs[i], instruments, accounts, true); err != nil {
			return Group{}, err
		}
	}
	if legs[0].Instrument == legs[1].Instrument && legs[0].AccountID == legs[1].AccountID {
		return Group{}, invalidf("the two legs are the same instrument on the same account")
	}
	if legs[0].Side == legs[1].Side && request.Match == MatchBase {
		return Group{}, invalidf("both legs are %s: a same-underlying hedge needs one long and one short leg", legs[0].Side)
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
		return Group{}, invalidf("policy must be unwind or hold")
	}
	var prices [2]decimal.Decimal
	for i, leg := range legs {
		_, _, ok := market.Book(ctx, leg.Instrument, 5*time.Second)
		if !ok {
			return Group{}, invalidf("no order book for %s yet; try again in a moment", leg.Instrument)
		}
		prices[i] = market.TradePrice(leg.Instrument, leg.Side)
	}
	if request.Mode == ModeLimit {
		prices[request.Maker] = request.Price
	}
	plan, err := PlanSize(SizeInput{Size: request.Size, Unit: request.Unit, Match: request.Match, Price: prices, Ratio: request.Ratio}, legs)
	if err != nil {
		return Group{}, err
	}
	if len(plan.Problems) > 0 {
		return Group{}, invalidf("%s", strings.Join(plan.Problems, "; "))
	}
	now := engine.now().UTC()
	group := Group{
		ID: newID("hg_"), Label: strings.TrimSpace(request.Label), Note: strings.TrimSpace(request.Note),
		Status: StatusOpening, Policy: policy, Match: request.Match, Ratio: plan.Ratio, Legs: legs, CreatedAt: now, UpdatedAt: now,
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
	engine.event(group.ID, "info", "Opening: %s %s %s / %s %s %s (ratio %s, %s mode)",
		legs[0].Side, plan.Qty[0], legs[0].Instrument, legs[1].Side, plan.Qty[1], legs[1].Instrument, plan.Ratio, request.Mode)
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
		return Action{}, invalidf("a %s group cannot be closed now", group.Status)
	}
	fraction := request.Fraction
	if fraction.IsZero() {
		fraction = decimal.NewFromInt(1)
	}
	if !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return Action{}, invalidf("fraction must be in (0, 1]")
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
		return Action{}, invalidf("nothing to close at this fraction")
	}
	if request.Mode == ModeLimit && target[request.Maker].IsZero() {
		return Action{}, invalidf("the maker leg has nothing to close")
	}
	action := Action{ID: newID("ha_"), GroupID: group.ID, Kind: KindClose, Mode: request.Mode, TIF: tif, Target: target, Status: ActionWorking, CreatedAt: engine.now().UTC()}
	if request.Mode == ModeLimit {
		maker := request.Maker
		action.MakerLeg, action.LimitPrice = &maker, request.Price
	}
	if err := engine.store.StartAction(ctx, action, StatusClosing); err != nil {
		if err == ErrBusy {
			return Action{}, invalidf("the group already has an action working")
		}
		return Action{}, err
	}
	group.Status = StatusClosing
	engine.event(group.ID, "info", "Closing %s%%: leg 1 %s, leg 2 %s (%s mode)", fraction.Mul(decimal.NewFromInt(100)).StringFixed(0), target[0].Abs(), target[1].Abs(), request.Mode)
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
		return Action{}, invalidf("a %s group cannot be rehedged now", group.Status)
	}
	if Negligible(group, Imbalance(group, books)) {
		return Action{}, invalidf("the legs are balanced")
	}
	target := RehedgeTargets(group, books)
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, invalidf("the imbalance is below the smaller leg's step")
	}
	return engine.startRepair(ctx, group, KindRehedge, target, "Rehedging: "+describeTargets(group, target))
}

// Trim balances an unbalanced group by reducing the larger leg to match the
// smaller one (a full unwind when the smaller leg is empty).
func (engine *Engine) Trim(ctx context.Context, groupID string) (Action, error) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return Action{}, err
	}
	if group.Status != StatusBroken && group.Status != StatusOpen {
		return Action{}, invalidf("a %s group cannot be trimmed now", group.Status)
	}
	if Negligible(group, Imbalance(group, books)) {
		return Action{}, invalidf("the legs are balanced")
	}
	target := TrimTargets(group, books)
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, invalidf("nothing to trim")
	}
	return engine.startRepair(ctx, group, KindUnwind, target, "Trimming to the matched size: "+describeTargets(group, target))
}

func describeTargets(group Group, target [2]decimal.Decimal) string {
	var parts []string
	for i, qty := range target {
		if !qty.IsZero() {
			parts = append(parts, fmt.Sprintf("leg %d %s %s", i+1, group.Legs[i].TradeSide(qty), qty.Abs()))
		}
	}
	return strings.Join(parts, ", ") + " at market"
}

// Unwind closes every held quantity of both legs at market.
func (engine *Engine) Unwind(ctx context.Context, groupID string) (Action, error) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return Action{}, err
	}
	if group.Status != StatusBroken && group.Status != StatusOpen {
		return Action{}, invalidf("a %s group cannot be unwound now", group.Status)
	}
	target := [2]decimal.Decimal{books[0].Held.Neg(), books[1].Held.Neg()}
	if target[0].IsZero() && target[1].IsZero() {
		return Action{}, invalidf("nothing is held")
	}
	return engine.startRepair(ctx, group, KindUnwind, target, fmt.Sprintf("Unwinding: leg 1 %s, leg 2 %s at market", target[0].Abs(), target[1].Abs()))
}

func (engine *Engine) startRepair(ctx context.Context, group Group, kind string, target [2]decimal.Decimal, message string) (Action, error) {
	action := Action{ID: newID("ha_"), GroupID: group.ID, Kind: kind, Mode: ModeMarket, Target: target, Status: ActionWorking, CreatedAt: engine.now().UTC()}
	status := StatusClosing
	if kind == KindRehedge {
		status = StatusOpening
	}
	if err := engine.store.StartAction(ctx, action, status); err != nil {
		if err == ErrBusy {
			return Action{}, invalidf("the group already has an action working")
		}
		return Action{}, err
	}
	group.Status = status
	engine.event(group.ID, "info", "%s", message)
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
func statusFor(group Group, books [2]LegBook, message string) (string, string) {
	flat := books[0].Held.IsZero() && books[1].Held.IsZero()
	switch {
	case flat && books[0].Opened.IsZero() && books[1].Opened.IsZero():
		return StatusFailed, message
	case flat:
		return StatusClosed, ""
	case Negligible(group, Imbalance(group, books)):
		return StatusOpen, ""
	}
	problem := fmt.Sprintf("legs unbalanced: leg 1 holds %s, leg 2 holds %s (needs %s)", books[0].Held, books[1].Held, books[0].Held.Mul(group.Ratio))
	if message != "" {
		problem += " — " + message
	}
	return StatusBroken, problem
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
	status, problem := statusFor(group, books, "a fill arrived after its action finished")
	if status == group.Status || status == StatusFailed {
		return
	}
	if engine.store.SetGroupStatus(ctx, groupID, status, problem) == nil {
		level := "warn"
		if status == StatusBroken {
			level = "error"
		}
		engine.event(groupID, level, "Late venue update: status %s → %s", group.Status, status)
	}
}

// settle decides a group's status after an action finished, and applies the
// unhedged-leg policy.
func (engine *Engine) settle(ctx context.Context, groupID string, action Action, message string) {
	group, books, err := engine.snapshot(ctx, groupID)
	if err != nil {
		return
	}
	status, problem := statusFor(group, books, message)
	if err := engine.store.SetGroupStatus(ctx, groupID, status, problem); err != nil {
		return
	}
	engine.changed(groupID)
	if status != StatusBroken {
		return
	}
	engine.event(groupID, "error", "Hedge broken: %s", problem)
	// Policy: when an open or a close leaves the legs unequal, trim the larger
	// leg to the smaller one — for an open, the hedge shrinks to what filled
	// (a full unwind if one leg filled nothing); for a close, the close is
	// completed. Repairs are never retried automatically, so a venue that
	// keeps refusing leaves the group broken for the operator.
	if group.Policy == PolicyUnwind && (action.Kind == KindOpen || action.Kind == KindClose) {
		if _, err := engine.Trim(ctx, groupID); err != nil {
			engine.event(groupID, "error", "Automatic trim not started: %s", err)
		}
	}
}

// refPrice is the book price a market order of side starts at (for slippage
// reporting); zero when no book is cached.
func (engine *Engine) refPrice(instrument, side string) decimal.Decimal {
	if engine.market == nil {
		return decimal.Zero
	}
	return engine.market.TradePrice(instrument, side)
}
