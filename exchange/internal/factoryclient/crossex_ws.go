package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
)

// crossExWebSocket is the PerpWebSocket facet of a CrossEx client. One login
// stream with "!all" carries every venue's orders, fills, positions and the
// shared pool's assets. Account watches project it through the instrument
// index; per-instrument watches filter it. Commands use REST, matching the
// documented rate limits that REST and WebSocket share.
type crossExWebSocket struct {
	client *crossExPerpClient

	mu        sync.Mutex
	closed    bool
	cancels   map[uint64]context.CancelFunc
	nextWatch uint64
	index     []exchange.AccountInstrument
}

var crossExAccountChannels = []string{crossex.ChannelOrder, crossex.ChannelUserTrades, crossex.ChannelPosition, crossex.ChannelAsset}

// Reconnect backoff for the private stream; overridable in tests.
var crossExReconnectDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

func (socket *crossExWebSocket) String() string   { return socket.client.meta.redactedString() }
func (socket *crossExWebSocket) GoString() string { return socket.String() }

// SetAccountInstruments installs the gateway's native-to-id index. Brokered
// instruments may belong to any reachable venue but must be perps.
func (socket *crossExWebSocket) SetAccountInstruments(instruments []exchange.AccountInstrument) error {
	if len(instruments) == 0 {
		return fmt.Errorf("account instrument index is invalid")
	}
	reachable := map[exchange.Venue]bool{}
	for _, route := range socket.client.Reaches() {
		reachable[route.Venue] = true
	}
	seen := make(map[string]struct{}, len(instruments))
	for _, item := range instruments {
		identity := item.Instrument.Identity
		if item.InstrumentID == "" || item.Instrument.Validate() != nil || !reachable[identity.Venue] || identity.Product != exchange.ProductPerp {
			return fmt.Errorf("account instrument index is invalid")
		}
		if _, duplicate := seen[item.InstrumentID]; duplicate {
			return fmt.Errorf("account instrument index contains duplicate")
		}
		seen[item.InstrumentID] = struct{}{}
	}
	socket.mu.Lock()
	socket.index = append([]exchange.AccountInstrument(nil), instruments...)
	socket.mu.Unlock()
	return nil
}

// WatchAccountEvents streams every account event of the CrossEx account.
func (socket *crossExWebSocket) WatchAccountEvents(ctx context.Context, request exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	if ctx == nil || ctx.Err() != nil || request.Route.Validate() != nil || request.Options.Buffer < 0 || request.Options.Buffer > maxWebSocketBuffer {
		return nil, fmt.Errorf("account event watch is invalid")
	}
	if request.Route.Venue != exchange.VenueCrossEx || request.Route.Product != exchange.ProductPerp {
		return nil, fmt.Errorf("account event route does not match websocket")
	}
	socket.mu.Lock()
	catalog := append([]exchange.AccountInstrument(nil), socket.index...)
	socket.mu.Unlock()
	if len(catalog) == 0 {
		return nil, fmt.Errorf("account instrument index is unavailable")
	}
	index, err := newCrossExAccountIndex(catalog)
	if err != nil {
		return nil, err
	}
	buffer := request.Options.Buffer
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	route := request.Route
	return startCrossExWatch[exchange.AccountEvent](ctx, socket, "account:"+route.RouteID, buffer, func(push crossex.Push) ([]exchange.AccountEvent, error) {
		events, err := crossExAccountEvent(push)
		if err != nil {
			return nil, err
		}
		out := make([]exchange.AccountEvent, 0, len(events))
		for _, event := range events {
			projected, err := projectAccountBackendEvent(route, index, event)
			if err != nil {
				// An instrument the gateway does not serve (Kraken, a newly
				// listed market): not this stream's failure.
				continue
			}
			out = append(out, projected...)
		}
		return out, nil
	})
}

// newCrossExAccountIndex indexes brokered instruments by CrossEx symbol.
// It mirrors newAccountInstrumentIndex without the single-venue constraint.
func newCrossExAccountIndex(instruments []exchange.AccountInstrument) (accountInstrumentIndex, error) {
	index := accountInstrumentIndex{byNative: make(map[string]exchange.AccountInstrument, len(instruments))}
	for _, item := range instruments {
		native := strings.TrimSpace(item.Instrument.Identity.NativeSymbol)
		if native == "" {
			continue
		}
		if existing, duplicate := index.byNative[native]; duplicate && existing.InstrumentID != item.InstrumentID {
			return accountInstrumentIndex{}, fmt.Errorf("account event native identity is ambiguous")
		}
		index.byNative[native] = item
	}
	if len(index.byNative) == 0 {
		return accountInstrumentIndex{}, fmt.Errorf("account instrument index is unavailable")
	}
	return index, nil
}

// crossExAccountEvent decodes one private push into backend events.
func crossExAccountEvent(push crossex.Push) ([]accountBackendEvent, error) {
	switch push.Channel {
	case crossex.ChannelOrder:
		row, err := crossex.DecodeOrder(push.Payload)
		if err != nil {
			return nil, fmt.Errorf("crossex order push is malformed")
		}
		if row.BusinessType != "" && row.BusinessType != crossex.BusinessFuture {
			return nil, nil
		}
		order := crossExOrder(row)
		return []accountBackendEvent{{Kind: exchange.AccountEventOrder, NativeSymbol: row.Symbol, Order: &exchange.OrderEvent{Kind: exchange.EventDelta, Order: order}}}, nil
	case crossex.ChannelUserTrades:
		row, err := crossex.DecodeTrade(push.Payload)
		if err != nil {
			return nil, fmt.Errorf("crossex trade push is malformed")
		}
		if row.BusinessType != "" && row.BusinessType != crossex.BusinessFuture {
			return nil, nil
		}
		fill := crossExFill(row)
		return []accountBackendEvent{{Kind: exchange.AccountEventFill, NativeSymbol: row.Symbol, Fill: &exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}}}, nil
	case crossex.ChannelPosition:
		row, err := crossex.DecodePosition(push.Payload)
		if err != nil {
			return nil, fmt.Errorf("crossex position push is malformed")
		}
		position := crossExPosition(row)
		// A closed position is pushed with zero quantity; a snapshot for the
		// instrument with no rows removes it.
		positions := []exchange.Position{}
		if !position.Quantity.IsZero() {
			positions = append(positions, position)
		}
		at := crossExTimeMS(row.UpdateTime)
		return []accountBackendEvent{{Kind: exchange.AccountEventPosition, NativeSymbol: row.Symbol, Position: &exchange.PositionEvent{Kind: exchange.EventSnapshot, Positions: positions, Time: at}}}, nil
	case crossex.ChannelAsset:
		row, err := crossex.DecodeAsset(push.Payload)
		if err != nil {
			return nil, fmt.Errorf("crossex asset push is malformed")
		}
		// Per-venue breakdown rows are not balances of the pool.
		if !crossExIsPoolRow(row) {
			return nil, nil
		}
		balances := crossExBalances([]crossex.AccountAsset{row})
		var at time.Time
		if push.TimeMS > 0 {
			at = time.UnixMilli(push.TimeMS).UTC()
		}
		return []accountBackendEvent{{Kind: exchange.AccountEventBalance, Balance: &exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: at}}}, nil
	}
	return nil, nil
}

// startCrossExWatch runs one private stream for the life of a subscription,
// reconnecting with backoff. project turns a push into zero or more events.
func startCrossExWatch[T any](ctx context.Context, socket *crossExWebSocket, name string, buffer int, project func(crossex.Push) ([]T, error)) (exchange.Subscription[T], error) {
	socket.mu.Lock()
	if socket.closed {
		socket.mu.Unlock()
		return nil, websocketError(socket.client.meta, "Watch", exchange.KindSubscriptionClosed, "websocket is closed")
	}
	if socket.cancels == nil {
		socket.cancels = map[uint64]context.CancelFunc{}
	}
	socket.nextWatch++
	key := socket.nextWatch
	watchCtx, cancel := context.WithCancel(ctx)
	socket.cancels[key] = cancel
	socket.mu.Unlock()

	meta := socket.client.meta
	id := fmt.Sprintf("crossex:perp:%s:%d", name, websocketSubscriptionSequence.Add(1))
	done := make(chan struct{})
	subscription := newTypedSubscription[T](id, meta, buffer, func() error {
		cancel()
		<-done
		socket.mu.Lock()
		delete(socket.cancels, key)
		socket.mu.Unlock()
		return nil
	})
	status := func(state exchange.SubscriptionState, reason string) {
		subscription.emitStatus(backendStatus{State: state, Reason: reason, Time: time.Now().UTC()})
	}
	status(exchange.SubscriptionConnecting, "")

	go func() {
		defer close(done)
		attempt := 0
		for watchCtx.Err() == nil {
			ready := false
			err := socket.client.stream().Run(watchCtx, crossExAccountChannels, crossex.StreamHandler{
				Ready: func() {
					ready = true
					attempt = 0
					status(exchange.SubscriptionActive, "")
				},
				Push: func(push crossex.Push) {
					events, err := project(push)
					if err != nil {
						subscription.emitError(exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Watch", SafeMessage: err.Error()}))
						return
					}
					for _, event := range events {
						subscription.emit(event)
					}
				},
			})
			if watchCtx.Err() != nil {
				return
			}
			if errors.Is(err, crossex.ErrWSLogin) || errors.Is(err, crossex.ErrCredentialsRequired) {
				// A rejected key does not heal by retrying quickly.
				subscription.emitError(exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Watch", SafeMessage: "crossex private stream login rejected"}))
			}
			reason := "crossex private stream disconnected"
			if !ready {
				reason = "crossex private stream could not start"
			}
			status(exchange.SubscriptionConnecting, reason)
			delay := crossExReconnectDelays[min(attempt, len(crossExReconnectDelays)-1)]
			attempt++
			select {
			case <-watchCtx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
	return subscription, nil
}

// ---------------------------------------------------------------------------
// Per-instrument private watches filter the account stream
// ---------------------------------------------------------------------------

func (socket *crossExWebSocket) WatchOrders(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.OrderEvent], error) {
	instrument, buffer, err := socket.watchTarget(ctx, "WatchOrders", request)
	if err != nil {
		return nil, err
	}
	return startCrossExWatch[exchange.OrderEvent](ctx, socket, "orders", buffer, func(push crossex.Push) ([]exchange.OrderEvent, error) {
		return crossExFiltered(push, instrument, func(event accountBackendEvent) (exchange.OrderEvent, bool) {
			if event.Order == nil {
				return exchange.OrderEvent{}, false
			}
			return *event.Order, true
		})
	})
}

func (socket *crossExWebSocket) WatchFills(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.FillEvent], error) {
	instrument, buffer, err := socket.watchTarget(ctx, "WatchFills", request)
	if err != nil {
		return nil, err
	}
	return startCrossExWatch[exchange.FillEvent](ctx, socket, "fills", buffer, func(push crossex.Push) ([]exchange.FillEvent, error) {
		return crossExFiltered(push, instrument, func(event accountBackendEvent) (exchange.FillEvent, bool) {
			if event.Fill == nil {
				return exchange.FillEvent{}, false
			}
			return *event.Fill, true
		})
	})
}

func (socket *crossExWebSocket) WatchPositions(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.PositionEvent], error) {
	instrument, buffer, err := socket.watchTarget(ctx, "WatchPositions", request)
	if err != nil {
		return nil, err
	}
	return startCrossExWatch[exchange.PositionEvent](ctx, socket, "positions", buffer, func(push crossex.Push) ([]exchange.PositionEvent, error) {
		return crossExFiltered(push, instrument, func(event accountBackendEvent) (exchange.PositionEvent, bool) {
			if event.Position == nil {
				return exchange.PositionEvent{}, false
			}
			return *event.Position, true
		})
	})
}

func (socket *crossExWebSocket) WatchBalances(ctx context.Context, request exchange.WatchAccountRequest) (exchange.Subscription[exchange.BalanceEvent], error) {
	if ctx == nil || request.Options.Buffer < 0 || request.Options.Buffer > maxWebSocketBuffer {
		return nil, websocketError(socket.client.meta, "WatchBalances", exchange.KindInvalidRequest, "watch request is invalid")
	}
	buffer := request.Options.Buffer
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	return startCrossExWatch[exchange.BalanceEvent](ctx, socket, "balances", buffer, func(push crossex.Push) ([]exchange.BalanceEvent, error) {
		events, err := crossExAccountEvent(push)
		if err != nil {
			return nil, err
		}
		var out []exchange.BalanceEvent
		for _, event := range events {
			if event.Balance != nil {
				out = append(out, *event.Balance)
			}
		}
		return out, nil
	})
}

func (socket *crossExWebSocket) watchTarget(ctx context.Context, operation string, request exchange.WatchRequest) (exchange.Instrument, int, error) {
	if ctx == nil || request.Options.Buffer < 0 || request.Options.Buffer > maxWebSocketBuffer {
		return exchange.Instrument{}, 0, websocketError(socket.client.meta, operation, exchange.KindInvalidRequest, "watch request is invalid")
	}
	instrument, err := socket.client.instrument(ctx, operation, request.Instrument)
	if err != nil {
		return exchange.Instrument{}, 0, err
	}
	buffer := request.Options.Buffer
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	return instrument, buffer, nil
}

func crossExFiltered[T any](push crossex.Push, instrument exchange.Instrument, pick func(accountBackendEvent) (T, bool)) ([]T, error) {
	events, err := crossExAccountEvent(push)
	if err != nil {
		return nil, err
	}
	var out []T
	for _, event := range events {
		if event.NativeSymbol != instrument.Identity.NativeSymbol {
			continue
		}
		if value, ok := pick(event); ok {
			out = append(out, value)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Commands and market data
// ---------------------------------------------------------------------------

func (socket *crossExWebSocket) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return socket.client.PlaceOrder(ctx, request)
}

func (socket *crossExWebSocket) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return socket.client.CancelOrder(ctx, request)
}

func (socket *crossExWebSocket) WatchOrderBook(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.BookEvent], error) {
	return nil, crossExUnsupported(socket.client.meta, "WatchOrderBook")
}

func (socket *crossExWebSocket) WatchBBO(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.BBOEvent], error) {
	return nil, crossExUnsupported(socket.client.meta, "WatchBBO")
}

func (socket *crossExWebSocket) WatchPublicTrades(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.PublicTradeEvent], error) {
	return nil, crossExUnsupported(socket.client.meta, "WatchPublicTrades")
}

func (socket *crossExWebSocket) WatchCandles(context.Context, exchange.WatchCandlesRequest) (exchange.Subscription[exchange.CandleEvent], error) {
	return nil, crossExUnsupported(socket.client.meta, "WatchCandles")
}

func (socket *crossExWebSocket) WatchMarkPrice(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.MarkPriceEvent], error) {
	return nil, crossExUnsupported(socket.client.meta, "WatchMarkPrice")
}

func (socket *crossExWebSocket) WatchFundingRate(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.FundingRateEvent], error) {
	return nil, crossExUnsupported(socket.client.meta, "WatchFundingRate")
}

// Close stops every watch. It is idempotent and safe before any watch.
func (socket *crossExWebSocket) Close() error {
	socket.mu.Lock()
	socket.closed = true
	cancels := socket.cancels
	socket.cancels = nil
	socket.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

var (
	_ exchange.PerpWebSocket            = (*crossExWebSocket)(nil)
	_ exchange.AccountWebSocket         = (*crossExWebSocket)(nil)
	_ exchange.AccountInstrumentIndexer = (*crossExWebSocket)(nil)
)
