package pairexec

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/shopspring/decimal"
)

// Gateway is what the engine needs from the gateway (gwclient.Client in
// production, a fake in tests).
type Gateway interface {
	Instruments(context.Context) ([]gwclient.Instrument, error)
	Accounts(context.Context) ([]gwclient.Account, error)
	Routes(context.Context, string) ([]gwclient.Route, error)
	Margin(context.Context, string) (*gwclient.Margin, error)
	OpenOrders(context.Context, string, string) ([]gwclient.Order, error)
	Positions(context.Context, string) ([]gwclient.Position, error)
	Book(context.Context, string, int) (gwclient.Book, error)
	Place(context.Context, string, gwclient.PlaceRequest) (gwclient.PlaceResult, error)
	Lookup(context.Context, string, string, string) (gwclient.LookupResult, error)
	Cancel(context.Context, string, string, string) error
	Fills(context.Context, string, string, time.Time) ([]gwclient.Fill, error)
	Funding(context.Context, string) (gwclient.FundingQuote, error)
}

// Observer is told when an action finishes and its group's status has been
// settled. An app that drives groups by rules (the spreader) uses it to close
// the loop; the hedger needs none. It is called from the action's runner and
// must not block.
type Observer interface {
	ActionSettled(group Group, action Action, status string)
}

// SetObserver installs the observer; call before Start.
func (engine *Engine) SetObserver(observer Observer) { engine.observer = observer }

// Engine runs every hedge action. Each action is driven by one goroutine
// (its runner) that is the only writer of that action's orders; venue
// updates reach it through a channel. On restart, every working action gets
// a runner again and starts by reconciling its orders with the venues.
type Engine struct {
	gateway Gateway
	store   *Store
	market  *Market
	now     func() time.Time
	// Timeouts and retries; tests shorten them.
	lookupDelays []time.Duration
	pollEvery    time.Duration

	mu      sync.Mutex
	runners map[string]*runner // by action id
	byOrder map[string]string  // client order id -> action id
	// mismatch: groups whose venue position disagrees, since when.
	mismatch map[string]time.Time
	// riskSeen: risk-engine orders already recorded, by order id -> status.
	riskSeen map[string]string
	changes  chan string // group ids whose state changed (for the UI)
	observer Observer
	ctx      context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup
}

func NewEngine(gateway Gateway, store *Store, market *Market) *Engine {
	return &Engine{
		gateway: gateway, store: store, market: market, now: time.Now,
		lookupDelays: []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second, 5 * time.Second},
		pollEvery:    5 * time.Second,
		runners:      map[string]*runner{}, byOrder: map[string]string{}, mismatch: map[string]time.Time{}, riskSeen: map[string]string{},
		changes: make(chan string, 1024),
	}
}

// Stop halts every runner and waits for them. Working actions stay working
// in the database and resume on the next Start.
func (engine *Engine) Stop() {
	engine.mu.Lock()
	stop := engine.stop
	engine.mu.Unlock()
	if stop != nil {
		stop()
	}
	engine.wg.Wait()
}

// Changes delivers group ids whose state changed.
func (engine *Engine) Changes() <-chan string { return engine.changes }

func (engine *Engine) changed(groupID string) {
	select {
	case engine.changes <- groupID:
	default:
	}
}

// Start resumes every working action. It must be called once, before
// OnOrder/OnFill are fed, with the context that bounds the engine's life.
func (engine *Engine) Start(ctx context.Context) error {
	engine.mu.Lock()
	engine.ctx, engine.stop = context.WithCancel(ctx)
	ctx = engine.ctx
	engine.mu.Unlock()
	actions, err := engine.store.WorkingActions(ctx)
	if err != nil {
		return err
	}
	for _, action := range actions {
		group, err := engine.store.Group(ctx, action.GroupID)
		if err != nil {
			return err
		}
		slog.Info("resuming hedge action", "group", group.ID, "action", action.ID, "kind", action.Kind)
		_ = engine.store.AddEvent(ctx, group.ID, "info", textOf("event.resumed", "kind", action.Kind))
		engine.spawn(group, action)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Venue updates (from the gateway account streams)
// ---------------------------------------------------------------------------

// OnOrder routes a venue order update to the runner owning it, or stores it
// when no runner is active (late updates after an action finished).
func (engine *Engine) OnOrder(accountID string, update gwclient.Order) {
	if update.Origin != "" {
		engine.riskOrder(accountID, update)
	}
	if update.ClientOrderID == "" {
		return
	}
	if r := engine.runnerFor(update.ClientOrderID); r != nil {
		r.deliver(event{order: &update})
		return
	}
	engine.storeLate(accountID, update, nil)
}

// riskOrder records an order the venue's risk engine placed (liquidation,
// auto-deleverage, delisting) on every active group with a leg on that
// account and instrument. The venue-position check then marks the group
// unhedged if the leg shrank; this explains why.
func (engine *Engine) riskOrder(accountID string, update gwclient.Order) {
	key := accountID + "|" + update.OrderID
	engine.mu.Lock()
	seen := engine.riskSeen[key] == update.Status
	if len(engine.riskSeen) > 10_000 {
		engine.riskSeen = map[string]string{}
	}
	engine.riskSeen[key] = update.Status
	engine.mu.Unlock()
	if seen {
		return
	}
	ctx := context.Background()
	groups, err := engine.store.Groups(ctx, engine.now())
	if err != nil {
		return
	}
	label := map[string]string{"liquidation": "Liquidation", "adl": "Auto-deleverage", "settlement": "Delisting"}[update.Origin]
	if label == "" {
		label = update.Origin
	}
	for _, group := range groups {
		if group.Status == StatusClosed || group.Status == StatusFailed {
			continue
		}
		for i, leg := range group.Legs {
			if leg.AccountID == accountID && leg.Instrument == update.Instrument {
				engine.event(group.ID, "error", textOf("event.riskOrder", "origin", update.Origin, "leg", i+1, "inst", leg.Instrument, "side", update.Side, "qty", update.Quantity, "status", update.Status))
			}
		}
	}
}

// OnFill routes a venue fill.
func (engine *Engine) OnFill(accountID string, fill gwclient.Fill) {
	clientOrderID := fill.ClientOrderID
	if clientOrderID == "" {
		// Some venues omit the client id on fills; resolve by venue order id.
		if order, found, err := engine.store.OrderByVenueID(context.Background(), accountID, fill.OrderID); err == nil && found {
			clientOrderID = order.ClientOrderID
		}
	}
	if clientOrderID == "" {
		return
	}
	fill.ClientOrderID = clientOrderID
	if r := engine.runnerFor(clientOrderID); r != nil {
		r.deliver(event{fill: &fill})
		return
	}
	engine.storeLate(accountID, gwclient.Order{}, &fill)
}

func (engine *Engine) runnerFor(clientOrderID string) *runner {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.runners[engine.byOrder[clientOrderID]]
}

// storeLate records an update for an order whose action is no longer
// running, so fills arriving after a restart or a finished action are kept.
func (engine *Engine) storeLate(_ string, update gwclient.Order, fill *gwclient.Fill) {
	ctx := context.Background()
	clientOrderID := update.ClientOrderID
	if fill != nil {
		clientOrderID = fill.ClientOrderID
	}
	order, found, err := engine.store.OrderByClientID(ctx, clientOrderID)
	if err != nil || !found {
		return
	}
	if fill != nil {
		if added, _ := engine.store.AddFill(ctx, convertFill(*fill)); added {
			engine.changed(order.GroupID)
			engine.recheck(ctx, order.GroupID)
		}
		return
	}
	before := order
	applyVenueOrder(&order, update)
	if OrderTerminal(before.State) && !OrderTerminal(order.State) {
		order.State = before.State
	}
	if order.State == before.State && order.Filled.Equal(before.Filled) && order.AvgPrice.Equal(before.AvgPrice) && order.VenueOrderID == before.VenueOrderID {
		return
	}
	if engine.store.UpdateOrder(ctx, order) == nil {
		engine.changed(order.GroupID)
		if !order.Filled.Equal(before.Filled) {
			engine.recheck(ctx, order.GroupID)
		}
	}
}

func convertFill(fill gwclient.Fill) Fill {
	result := Fill{ClientOrderID: fill.ClientOrderID, FillID: fill.FillID, FeeAsset: fill.FeeAsset, Liquidity: fill.Liquidity, Time: time.UnixMilli(fill.Time).UTC()}
	result.Price, _ = decimal.NewFromString(fill.Price)
	result.Qty, _ = decimal.NewFromString(fill.Quantity)
	result.Fee, _ = decimal.NewFromString(fill.Fee)
	if fill.Time == 0 {
		result.Time = time.Now().UTC()
	}
	if result.FillID == "" {
		result.FillID = fill.OrderID + ":" + fill.Price + ":" + fill.Quantity + ":" + fmt.Sprint(fill.Time)
	}
	return result
}

// applyVenueOrder merges a venue report into a stored order. Filled never
// decreases.
func applyVenueOrder(order *Order, update gwclient.Order) {
	if update.OrderID != "" {
		order.VenueOrderID = update.OrderID
	}
	if update.Status != "" {
		order.State = update.Status
	}
	if update.Status == OrderRejected && update.RejectMessage+update.RejectCode != "" {
		message := update.RejectMessage
		if update.RejectCode != "" && !strings.Contains(message, update.RejectCode) {
			message = strings.TrimSpace(message + " (" + update.RejectCode + ")")
		}
		order.Error = venueText(message)
	}
	if filled, err := decimal.NewFromString(update.Filled); err == nil && filled.GreaterThan(order.Filled) {
		order.Filled = filled
	}
	if avg, err := decimal.NewFromString(update.AvgPrice); err == nil && avg.IsPositive() {
		order.AvgPrice = avg
	}
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newID(prefix string) string {
	var entropy [8]byte
	_, _ = rand.Read(entropy[:])
	return prefix + idEncoding.EncodeToString(entropy[:])
}

// spawn starts a runner for a working action.
func (engine *Engine) spawn(group Group, action Action) {
	engine.mu.Lock()
	if _, running := engine.runners[action.ID]; running {
		engine.mu.Unlock()
		return
	}
	r := &runner{engine: engine, group: group, action: action, events: make(chan event, 256), control: make(chan string, 4), done: make(chan struct{})}
	engine.runners[action.ID] = r
	ctx := engine.ctx
	engine.wg.Add(1)
	engine.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer engine.wg.Done()
		defer func() {
			engine.mu.Lock()
			delete(engine.runners, action.ID)
			for clientID, actionID := range engine.byOrder {
				if actionID == action.ID {
					delete(engine.byOrder, clientID)
				}
			}
			engine.mu.Unlock()
			engine.changed(group.ID)
		}()
		r.run(ctx)
	}()
}

func (engine *Engine) track(clientOrderID, actionID string) {
	engine.mu.Lock()
	engine.byOrder[clientOrderID] = actionID
	engine.mu.Unlock()
}

// Cancel asks a working action to stop: a limit action cancels its maker
// order and hedges whatever filled; a market action cannot be stopped.
func (engine *Engine) Cancel(groupID string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, r := range engine.runners {
		if r.group.ID == groupID {
			select {
			case r.control <- "cancel":
			default:
			}
			return nil
		}
	}
	return Invalid("invalid.noWorking")
}

// Working reports the working action of a group, if any.
func (engine *Engine) Working(groupID string) (Action, bool) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, r := range engine.runners {
		if r.group.ID == groupID {
			return r.action, true
		}
	}
	return Action{}, false
}

func (engine *Engine) event(groupID, level string, message Text) {
	if err := engine.store.AddEvent(context.Background(), groupID, level, message); err != nil {
		slog.Warn("hedge event not stored", "group", groupID, "error", err)
	}
	engine.changed(groupID)
}

// errorText is a short, credential-free description of a gateway failure.
func errorText(err error) string {
	var gatewayErr *gwclient.Error
	if errors.As(err, &gatewayErr) {
		return gatewayErr.Error()
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
