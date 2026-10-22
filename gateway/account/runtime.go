package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/hub"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/venue"
)

// Account channel names pushed on the "account" topic.
const (
	ChannelOrders    = "account.orders"
	ChannelFills     = "account.fills"
	ChannelPositions = "account.positions"
	ChannelBalances  = "account.balances"
	ChannelStatus    = "account.status"
	ChannelMargin    = "account.margin"
)

const (
	// refreshDelay coalesces bursts of fills into one REST position read.
	refreshDelay = 300 * time.Millisecond
	// resyncInterval repairs anything a private stream missed.
	resyncInterval = 60 * time.Second
)

// runtime is the live state of one account: its clients and an in-memory
// position/balance snapshot fed by REST and the private stream. Snapshots are
// kept per client (routeClient.key): one per market for a native account, one
// for all markets of a broker account.
type runtime struct {
	id      string
	venue   exchange.Venue
	clients []routeClient
	catalog *market.Catalog

	mu        sync.Mutex
	positions map[venue.Market][]Position
	balances  map[venue.Market][]Balance
	margin    map[venue.Market]*Margin
	loaded    map[venue.Market]bool
	connected map[venue.Market]bool
	emit      hub.Emit
	refresh   chan venue.Market
}

func newRuntime(id string, venueName exchange.Venue, clients []routeClient, catalog *market.Catalog) *runtime {
	return &runtime{
		id: id, venue: venueName, clients: clients, catalog: catalog,
		positions: map[venue.Market][]Position{}, balances: map[venue.Market][]Balance{}, margin: map[venue.Market]*Margin{},
		loaded: map[venue.Market]bool{}, connected: map[venue.Market]bool{},
		refresh: make(chan venue.Market, 16),
	}
}

func (rt *runtime) close() {
	for _, client := range rt.clients {
		client.close()
	}
}

func (rt *runtime) client(key venue.Market) (routeClient, bool) {
	for _, client := range rt.clients {
		if client.key == key {
			return client, true
		}
	}
	return routeClient{}, false
}

// clientFor returns the client that trades instrument.
func (rt *runtime) clientFor(instrument market.Instrument) (routeClient, error) {
	owner := venue.MarketKey(instrument.Native())
	for _, client := range rt.clients {
		if client.trades(owner) {
			return client, nil
		}
	}
	return routeClient{}, fmt.Errorf("%w: instrument %s is not tradable by this account", ErrInvalidRequest, instrument.ID)
}

// resolve maps a client-reported native symbol to the gateway instrument.
func (rt *runtime) resolve(client routeClient, nativeSymbol, nativeID string) (market.Instrument, bool) {
	if client.broker && client.perp != nil {
		// A broker reports its own symbols. Its instrument for that symbol
		// carries the venue identity, which is the gateway id.
		if brokered, err := client.perp.ResolveInstrument(context.Background(), nativeSymbol); err == nil {
			identity := brokered.Identity
			if instrument, err := rt.catalog.Get(venue.NewInstrumentID(identity.Venue, identity.Product, identity.Symbol)); err == nil &&
				client.trades(venue.MarketKey(instrument.Native())) {
				return instrument, true
			}
		}
		return market.Instrument{}, false
	}
	for _, reached := range client.markets {
		if instrument, ok := rt.catalog.ResolveNative(reached, nativeSymbol, nativeID); ok {
			return instrument, true
		}
	}
	return market.Instrument{}, false
}

// ---------------------------------------------------------------------------
// Snapshot reads
// ---------------------------------------------------------------------------

// Positions returns the current snapshot. While a private stream keeps the
// snapshot live it is served from memory; otherwise each call reads the venue.
func (rt *runtime) Positions(ctx context.Context) ([]Position, error) {
	var errs []error
	for _, client := range rt.clients {
		if !client.holdsPositions() {
			continue
		}
		rt.mu.Lock()
		fresh := rt.loaded[client.key] && rt.emit != nil && rt.connected[client.key]
		rt.mu.Unlock()
		if !fresh {
			if err := rt.loadMarket(ctx, client); err != nil {
				errs = append(errs, err)
			}
		}
	}
	positions := rt.positionList()
	if len(positions) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return positions, nil
}

func (rt *runtime) positionList() []Position {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var result []Position
	for _, client := range rt.clients {
		result = append(result, rt.positions[client.key]...)
	}
	return result
}

// Balances reads balances fresh from every market.
func (rt *runtime) Balances(ctx context.Context) ([]Balance, error) {
	var result []Balance
	var errs []error
	for _, client := range rt.clients {
		balances, err := rt.fetchBalances(ctx, client)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", client.key, err))
			continue
		}
		result = append(result, balances...)
	}
	if len(result) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return uniqueBalances(result), nil
}

// uniqueBalances keeps one row per (market, asset): clients that share a
// wallet report the same balance.
func uniqueBalances(balances []Balance) []Balance {
	seen := map[[2]string]bool{}
	out := balances[:0:0]
	for _, balance := range balances {
		key := [2]string{balance.Market, balance.Asset}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, balance)
	}
	return out
}

func (rt *runtime) fetchBalances(ctx context.Context, client routeClient) ([]Balance, error) {
	natives, err := client.balances(ctx)
	if err != nil {
		return nil, err
	}
	balances := make([]Balance, 0, len(natives))
	for _, item := range natives {
		if item.Total.IsZero() && item.Available.IsZero() && item.Locked.IsZero() {
			continue
		}
		balances = append(balances, Balance{Market: client.balanceMarket(), Asset: item.Asset,
			Total: item.Total.String(), Available: item.Available.String(), Locked: item.Locked.String()})
	}
	sort.Slice(balances, func(i, j int) bool { return balances[i].Asset < balances[j].Asset })
	rt.mu.Lock()
	rt.balances[client.key] = balances
	rt.mu.Unlock()
	return balances, nil
}

// fetchMargin reads a client's margin pool when it has one.
func (rt *runtime) fetchMargin(ctx context.Context, client routeClient) (*Margin, error) {
	reader, ok := client.perp.(exchange.MarginPoolREST)
	if client.perp == nil || !ok {
		return nil, nil
	}
	pool, err := reader.MarginPool(ctx)
	if err != nil {
		return nil, err
	}
	margin := projectMargin(pool)
	rt.mu.Lock()
	rt.margin[client.key] = margin
	rt.mu.Unlock()
	return margin, nil
}

// Margin returns the account's margin pool, read fresh. Accounts without a
// shared pool report nil.
func (rt *runtime) Margin(ctx context.Context) (*Margin, error) {
	for _, client := range rt.clients {
		margin, err := rt.fetchMargin(ctx, client)
		if err != nil || margin != nil {
			return margin, err
		}
	}
	return nil, nil
}

func (rt *runtime) publishMargin() {
	rt.mu.Lock()
	emit := rt.emit
	var margin *Margin
	for _, client := range rt.clients {
		if value := rt.margin[client.key]; value != nil {
			margin = value
		}
	}
	rt.mu.Unlock()
	if emit != nil && margin != nil {
		emit(hub.Message{Channel: ChannelMargin, Account: rt.id, Data: margin}, true)
	}
}

func (rt *runtime) fetchPositions(ctx context.Context, client routeClient) ([]Position, error) {
	natives, err := client.positions(ctx)
	if err != nil {
		return nil, err
	}
	positions := make([]Position, 0, len(natives))
	for _, native := range natives {
		instrument, ok := rt.resolve(client, native.NativeSymbol, native.NativeID)
		if !ok {
			slog.Warn("position on unknown instrument skipped", "account", rt.id, "native", native.NativeSymbol)
			continue
		}
		if position, ok := projectPosition(instrument.ID, native); ok {
			positions = append(positions, position)
		}
	}
	sortPositions(positions)
	return positions, nil
}

func sortPositions(positions []Position) {
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Instrument != positions[j].Instrument {
			return positions[i].Instrument < positions[j].Instrument
		}
		return positions[i].Side < positions[j].Side
	})
}

// loadMarket replaces the market's positions with a REST read and publishes.
func (rt *runtime) loadMarket(ctx context.Context, client routeClient) error {
	if !client.holdsPositions() {
		return nil
	}
	positions, err := rt.fetchPositions(ctx, client)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	rt.positions[client.key] = positions
	rt.loaded[client.key] = true
	rt.mu.Unlock()
	rt.publishPositions()
	return nil
}

func (rt *runtime) publishPositions() {
	rt.mu.Lock()
	emit := rt.emit
	rt.mu.Unlock()
	if emit == nil {
		return
	}
	positions := rt.positionList()
	if positions == nil {
		positions = []Position{}
	}
	emit(hub.Message{Channel: ChannelPositions, Account: rt.id, Data: positions}, true)
}

func (rt *runtime) publishBalances() {
	rt.mu.Lock()
	emit := rt.emit
	var balances []Balance
	for _, client := range rt.clients {
		balances = append(balances, rt.balances[client.key]...)
	}
	rt.mu.Unlock()
	if emit == nil {
		return
	}
	balances = uniqueBalances(balances)
	if balances == nil {
		balances = []Balance{}
	}
	emit(hub.Message{Channel: ChannelBalances, Account: rt.id, Data: balances}, true)
}

func (rt *runtime) publishStatus() {
	rt.mu.Lock()
	emit := rt.emit
	markets := map[string]bool{}
	connected := len(rt.clients) > 0
	for _, client := range rt.clients {
		markets[client.balanceMarket()] = rt.connected[client.key]
		connected = connected && rt.connected[client.key]
	}
	rt.mu.Unlock()
	if emit != nil {
		emit(hub.Message{Channel: ChannelStatus, Account: rt.id, Data: map[string]any{"connected": connected, "markets": markets}}, true)
	}
}

// scheduleRefresh asks the stream loop to re-read positions for a market.
func (rt *runtime) scheduleRefresh(market venue.Market) {
	select {
	case rt.refresh <- market:
	default:
	}
}

// ---------------------------------------------------------------------------
// Private stream (hub source for the "account" topic)
// ---------------------------------------------------------------------------

// stream runs every market's private stream and keeps the snapshot fresh.
// It returns when ctx ends or any market's stream fails (the hub retries).
func (rt *runtime) stream(ctx context.Context, emit hub.Emit) error {
	rt.mu.Lock()
	rt.emit = emit
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		rt.emit = nil
		for market := range rt.connected {
			rt.connected[market] = false
		}
		rt.mu.Unlock()
	}()

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, len(rt.clients))
	for _, client := range rt.clients {
		go func(client routeClient) {
			if err := rt.streamMarket(streamCtx, client); err != nil && streamCtx.Err() == nil {
				failures <- fmt.Errorf("%s: %w", client.key, err)
			}
		}(client)
	}

	// Initial REST snapshot so subscribers see state immediately.
	rt.resync(ctx)

	pending := map[venue.Market]bool{}
	var timer <-chan time.Time
	resync := time.NewTicker(resyncInterval)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failures:
			return err
		case market := <-rt.refresh:
			pending[market] = true
			if timer == nil {
				timer = time.After(refreshDelay)
			}
		case <-timer:
			timer = nil
			for market := range pending {
				if client, ok := rt.client(market); ok {
					if err := rt.loadMarket(ctx, client); err != nil {
						slog.Warn("position refresh failed", "account", rt.id, "market", market.String(), "error", err)
					}
					if _, err := rt.fetchBalances(ctx, client); err == nil {
						rt.publishBalances()
					}
					if margin, err := rt.fetchMargin(ctx, client); err == nil && margin != nil {
						rt.publishMargin()
					}
				}
			}
			pending = map[venue.Market]bool{}
		case <-resync.C:
			rt.resync(ctx)
		}
	}
}

func (rt *runtime) resync(ctx context.Context) {
	for _, client := range rt.clients {
		if err := rt.loadMarket(ctx, client); err != nil {
			slog.Warn("position snapshot failed", "account", rt.id, "market", client.key.String(), "error", err)
		}
		if _, err := rt.fetchBalances(ctx, client); err != nil {
			slog.Warn("balance snapshot failed", "account", rt.id, "market", client.key.String(), "error", err)
		}
		if _, err := rt.fetchMargin(ctx, client); err != nil {
			slog.Warn("margin snapshot failed", "account", rt.id, "market", client.key.String(), "error", err)
		}
	}
	rt.publishPositions()
	rt.publishBalances()
	rt.publishMargin()
	rt.publishStatus()
}

func (rt *runtime) streamMarket(ctx context.Context, client routeClient) error {
	socket, ok := client.socket().(exchange.AccountWebSocket)
	if !ok {
		return fmt.Errorf("private account stream unsupported")
	}
	var instruments []market.Instrument
	for _, reached := range client.markets {
		instruments = append(instruments, rt.catalog.MarketInstruments(reached)...)
	}
	if len(instruments) == 0 {
		return fmt.Errorf("instrument catalog for %s is empty", client.key)
	}
	if indexer, ok := client.socket().(exchange.AccountInstrumentIndexer); ok {
		items := make([]exchange.AccountInstrument, 0, len(instruments))
		for _, instrument := range instruments {
			native := instrument.Native()
			if client.broker {
				// The broker's index needs its own symbols for the instrument.
				brokered, ok := rt.brokerInstrument(ctx, client, instrument)
				if !ok {
					continue
				}
				native = brokered
			}
			items = append(items, exchange.AccountInstrument{InstrumentID: string(instrument.ID), Instrument: native})
		}
		if err := indexer.SetAccountInstruments(items); err != nil {
			return err
		}
	}
	route := exchange.AccountRoute{
		AccountID: rt.id, RouteID: client.key.String(), Venue: client.key.Venue,
		Environment: exchange.EnvironmentLive, Product: client.key.Product, Settlement: client.key.Settlement,
	}
	if client.broker {
		route.NativeFamily = "brokered"
	} else {
		identity := instruments[0].Native().Identity
		route.NativeFamily = exchange.NativeMarketFamily(identity.Venue, identity.Product, client.key.Settlement)
	}
	subscription, err := socket.WatchAccountEvents(ctx, exchange.WatchAccountEventsRequest{Route: route, Options: exchange.WatchOptions{Buffer: 1024}})
	if err != nil {
		return err
	}
	defer subscription.Close()

	setConnected := func(connected bool) {
		rt.mu.Lock()
		changed := rt.connected[client.key] != connected
		rt.connected[client.key] = connected
		rt.mu.Unlock()
		if changed {
			rt.publishStatus()
		}
	}
	events, failures, statuses := subscription.Events(), subscription.Errors(), subscription.Status()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			if err != nil {
				setConnected(false)
				return err
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			switch status.State {
			case exchange.SubscriptionActive:
				setConnected(true)
				// A (re)connect may have missed events: refresh.
				rt.scheduleRefresh(client.key)
			case exchange.SubscriptionClosed:
				setConnected(false)
				return fmt.Errorf("private stream closed")
			default:
				setConnected(false)
			}
		case event, open := <-events:
			if !open {
				setConnected(false)
				return fmt.Errorf("private stream ended")
			}
			rt.handleEvent(client, event)
		}
	}
}

func (rt *runtime) handleEvent(client routeClient, event exchange.AccountEvent) {
	rt.mu.Lock()
	emit := rt.emit
	rt.mu.Unlock()
	id := venue.InstrumentID(event.InstrumentID)
	switch event.Kind {
	case exchange.AccountEventOrder:
		if event.Order != nil && emit != nil {
			emit(hub.Message{Channel: ChannelOrders, Account: rt.id, Data: projectOrder(id, event.Order.Order)}, false)
		}
	case exchange.AccountEventFill:
		if event.Fill != nil && emit != nil {
			emit(hub.Message{Channel: ChannelFills, Account: rt.id, Data: projectFill(id, event.Fill.Fill)}, false)
		}
		// Not every venue pushes positions; a fill always changes them.
		rt.scheduleRefresh(client.key)
	case exchange.AccountEventPosition:
		if event.Position != nil {
			rt.applyPositionEvent(client.key, id, *event.Position)
		}
	case exchange.AccountEventBalance:
		if event.Balance != nil {
			rt.applyBalanceEvent(client, *event.Balance)
			if client.broker {
				// The pool's rates and per-venue shares come only from REST.
				rt.scheduleRefresh(client.key)
			}
		}
	}
}

// applyPositionEvent updates one instrument's rows. A snapshot event replaces
// all rows of that instrument; a delta event replaces only the slots present.
func (rt *runtime) applyPositionEvent(market venue.Market, id venue.InstrumentID, event exchange.PositionEvent) {
	rt.mu.Lock()
	current := rt.positions[market]
	kept := current[:0:0]
	incomingSlots := map[exchange.PositionSlot]bool{}
	for _, native := range event.Positions {
		incomingSlots[native.PositionSlot] = true
	}
	for _, position := range current {
		if position.Instrument != id {
			kept = append(kept, position)
			continue
		}
		if event.Kind != exchange.EventSnapshot && !incomingSlots[position.slot] {
			kept = append(kept, position)
		}
	}
	for _, native := range event.Positions {
		if position, ok := projectPosition(id, native); ok {
			kept = append(kept, position)
		}
	}
	sortPositions(kept)
	rt.positions[market] = kept
	rt.loaded[market] = true
	rt.mu.Unlock()
	rt.publishPositions()
}

func (rt *runtime) applyBalanceEvent(client routeClient, event exchange.BalanceEvent) {
	market := client.key
	rt.mu.Lock()
	byAsset := map[string]Balance{}
	for _, balance := range rt.balances[market] {
		byAsset[balance.Asset] = balance
	}
	for _, item := range event.Balances {
		if item.Total.IsZero() && item.Available.IsZero() && item.Locked.IsZero() {
			delete(byAsset, item.Asset)
			continue
		}
		byAsset[item.Asset] = Balance{Market: client.balanceMarket(), Asset: item.Asset, Total: item.Total.String(), Available: item.Available.String(), Locked: item.Locked.String()}
	}
	balances := make([]Balance, 0, len(byAsset))
	for _, balance := range byAsset {
		balances = append(balances, balance)
	}
	sort.Slice(balances, func(i, j int) bool { return balances[i].Asset < balances[j].Asset })
	rt.balances[market] = balances
	rt.mu.Unlock()
	rt.publishBalances()
}

// brokerInstrument returns the broker's own instrument for a gateway
// instrument: the same identity with the broker's symbol and rules. Broker
// clients resolve "venue:SYMBOL" to it.
func (rt *runtime) brokerInstrument(ctx context.Context, client routeClient, instrument market.Instrument) (exchange.Instrument, bool) {
	if client.perp == nil {
		return exchange.Instrument{}, false
	}
	identity := instrument.Native().Identity
	brokered, err := client.perp.ResolveInstrument(ctx, brokerKey(identity))
	if err != nil {
		return exchange.Instrument{}, false
	}
	return brokered, true
}

// brokerKey names an instrument the way a broker client resolves it:
// "venue:SYMBOL" for perps, "venue:spot:SYMBOL" for spot.
func brokerKey(identity exchange.InstrumentIdentity) string {
	if identity.Product == exchange.ProductSpot {
		return string(identity.Venue) + ":spot:" + identity.Symbol
	}
	return string(identity.Venue) + ":" + identity.Symbol
}

func kindOf(venueName exchange.Venue) string {
	if _, ok := venue.BrokerOf(venueName); ok {
		return "broker"
	}
	return "native"
}

func (rt *runtime) kind() string { return kindOf(rt.venue) }

// marketNames lists every market the account trades.
func (rt *runtime) marketNames() []string {
	names := []string{}
	for _, client := range rt.clients {
		for _, reached := range client.markets {
			names = append(names, reached.String())
		}
	}
	return names
}

// Routes lists every instrument the account can trade with the rules its
// orders follow: the catalog's for a native account, the broker's own for a
// broker account.
func (rt *runtime) Routes(ctx context.Context) ([]Route, error) {
	routes := []Route{}
	for _, client := range rt.clients {
		var triggers *TriggerSupport
		if !client.broker {
			triggers = triggerSupport(client)
		}
		var brokered map[string]exchange.Instrument
		if client.broker {
			instruments, err := client.perp.Instruments(ctx)
			if err != nil {
				return nil, err
			}
			brokered = make(map[string]exchange.Instrument, len(instruments))
			for _, instrument := range instruments {
				brokered[brokerKey(instrument.Identity)] = instrument
			}
		}
		for _, reached := range client.markets {
			for _, instrument := range rt.catalog.MarketInstruments(reached) {
				native := instrument.Native()
				if client.broker {
					var ok bool
					if native, ok = brokered[brokerKey(native.Identity)]; !ok {
						continue
					}
				}
				route := Route{Instrument: instrument.ID, TickSize: native.Rules.PriceIncrement.String(),
					StepSize: native.Rules.QuantityIncrement.String(), MinQty: native.Rules.MinQuantity.String()}
				if exchange.IsDerivative(native.Identity.Product) {
					route.Triggers = triggers
				}
				if native.Rules.MinNotional.Valid {
					route.MinNotional = native.Rules.MinNotional.Value.String()
				}
				routes = append(routes, route)
			}
		}
	}
	return routes, nil
}
