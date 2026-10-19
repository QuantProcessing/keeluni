package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	kraken "github.com/QuantProcessing/keeluni/sdk/kraken"
)

// krakenWSBackend serves Kraken Futures streams. Each public topic owns one
// supervised connection (the socket layer shares topics between watchers).
// Kraken publishes no candle feed, so candles poll the charts endpoint.
type krakenWSBackend struct {
	client    *krakenPerpClient
	lifecycle context.Context
	cancel    context.CancelFunc
}

// Candle poll cadence; overridable in tests.
var krakenCandlePoll = 5 * time.Second

func newKrakenWSBackend(client *krakenPerpClient) *krakenWSBackend {
	lifecycle, cancel := context.WithCancel(context.Background())
	return &krakenWSBackend{client: client, lifecycle: lifecycle, cancel: cancel}
}

func (backend *krakenWSBackend) Close() error {
	backend.cancel()
	return nil
}

func (backend *krakenWSBackend) meta() clientMeta { return backend.client.meta }

// public runs one public subscription, reconnecting until stopped.
func (backend *krakenWSBackend) public(ctx context.Context, feed string, instrument exchange.Instrument, status func(backendStatus), handle func(kraken.Message) error) (func() error, error) {
	native, err := backend.client.native("Watch", instrument)
	if err != nil {
		return nil, err
	}
	subscriptions := []kraken.Subscription{{Feed: feed, Products: []string{native}}}
	return startSupervisedStream(ctx, backend.lifecycle, func(runCtx context.Context, ready func()) error {
		sessionCtx, cancel := context.WithCancel(runCtx)
		defer cancel()
		var failure error
		err := kraken.NewStream().WithURL(backend.wsURL()).Run(sessionCtx, subscriptions, kraken.Handler{
			Ready: ready,
			Message: func(message kraken.Message) {
				if message.Product != "" && message.Product != native {
					return
				}
				if err := handle(message); err != nil && failure == nil {
					failure = err
					cancel()
				}
			},
		})
		if failure != nil {
			return failure
		}
		return err
	}, status, nil, nil)
}

func (backend *krakenWSBackend) wsURL() string {
	if backend.client.wsURL != "" {
		return backend.client.wsURL
	}
	return kraken.DefaultWSURL
}

func (backend *krakenWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	var last int64
	return backend.public(ctx, kraken.FeedBook, instrument, callbacks.Status, func(message kraken.Message) error {
		var book kraken.BookMessage
		if kraken.Decode(message.Raw, &book) != nil {
			return fmt.Errorf("kraken book message is malformed")
		}
		at := krakenMillis(book.Timestamp)
		if message.Snapshot {
			last = book.Seq
			callbacks.Event(exchange.BookEvent{Kind: exchange.EventSnapshot, Instrument: instrument, Sequence: strconv.FormatInt(book.Seq, 10),
				Bids: krakenWSLevels(book.Bids), Asks: krakenWSLevels(book.Asks), Time: at})
			return nil
		}
		if last == 0 {
			return nil
		}
		if book.Seq <= last {
			return nil
		}
		if book.Seq != last+1 {
			// A missed delta: the local book is wrong until a new snapshot.
			return errStreamResync
		}
		last = book.Seq
		level := []exchange.BookLevel{{Price: numberDecimal(book.Price), Quantity: numberDecimal(book.Qty)}}
		event := exchange.BookEvent{Kind: exchange.EventDelta, Instrument: instrument, Sequence: strconv.FormatInt(book.Seq, 10), Time: at}
		if book.Side == "sell" {
			event.Asks = level
		} else {
			event.Bids = level
		}
		callbacks.Event(event)
		return nil
	})
}

func krakenWSLevels(rows []kraken.BookLevel) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if quantity := numberDecimal(row.Qty); quantity.IsPositive() {
			out = append(out, exchange.BookLevel{Price: numberDecimal(row.Price), Quantity: quantity})
		}
	}
	return out
}

func (backend *krakenWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	return backend.public(ctx, kraken.FeedTicker, instrument, callbacks.Status, func(message kraken.Message) error {
		var ticker kraken.TickerMessage
		if kraken.Decode(message.Raw, &ticker) != nil {
			return fmt.Errorf("kraken ticker message is malformed")
		}
		callbacks.Event(exchange.BBOEvent{Instrument: instrument,
			Bid: exchange.BookLevel{Price: numberDecimal(ticker.Bid), Quantity: numberDecimal(ticker.BidSize)},
			Ask: exchange.BookLevel{Price: numberDecimal(ticker.Ask), Quantity: numberDecimal(ticker.AskSize)}, Time: krakenMillis(ticker.Time)})
		return nil
	})
}

func (backend *krakenWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	return backend.public(ctx, kraken.FeedTrade, instrument, callbacks.Status, func(message kraken.Message) error {
		if message.Snapshot {
			// Recent history replayed on subscribe, not live trades.
			return nil
		}
		var trade kraken.TradeMessage
		if kraken.Decode(message.Raw, &trade) != nil {
			return fmt.Errorf("kraken trade message is malformed")
		}
		callbacks.Event(exchange.PublicTradeEvent{Instrument: instrument, TradeID: trade.UID, Side: krakenSide(trade.Side),
			Price: numberDecimal(trade.Price), Quantity: numberDecimal(trade.Qty), Time: krakenMillis(trade.Time)})
		return nil
	})
}

// StartCandles polls the charts endpoint and emits the latest candles.
func (backend *krakenWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	step, ok := krakenResolutions[interval]
	if !ok {
		return nil, websocketError(backend.meta(), "WatchCandles", exchange.KindInvalidRequest, "unsupported candle interval")
	}
	if _, err := backend.client.native("WatchCandles", instrument); err != nil {
		return nil, err
	}
	var lastOpen time.Time
	poll := func(pollCtx context.Context) error {
		now := time.Now().UTC()
		page, err := backend.client.Candles(pollCtx, exchange.CandlesRequest{Instrument: instrument, Interval: interval, Start: now.Add(-2 * step), End: now, Limit: 2})
		if err != nil {
			return err
		}
		for _, candle := range page.Candles {
			if candle.OpenTime.Before(lastOpen) {
				continue
			}
			lastOpen = candle.OpenTime
			callbacks.Event(exchange.CandleEvent{Instrument: instrument, Interval: interval, Candle: candle})
		}
		return nil
	}
	return startPolling(ctx, backend.lifecycle, krakenCandlePoll, poll, callbacks.Status)
}

// StartReference streams mark, index, funding and open interest from the
// ticker. Funding settles hourly.
func (backend *krakenWSBackend) StartReference(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	return backend.public(ctx, kraken.FeedTicker, instrument, callbacks.Status, func(message kraken.Message) error {
		var ticker kraken.TickerMessage
		if kraken.Decode(message.Raw, &ticker) != nil {
			return fmt.Errorf("kraken ticker message is malformed")
		}
		at := krakenMillis(ticker.Time)
		mark, rate := numberDecimal(ticker.MarkPrice), numberDecimal(ticker.RelativeFundingRate)
		next := krakenMillis(ticker.NextFundingRateTime)
		event := perpReferenceEvent{
			MarkPrice: exchange.MarkPriceEvent{Instrument: instrument, Price: mark, Time: at}, MarkValid: mark.IsPositive(),
			FundingRate: exchange.FundingRateEvent{Instrument: instrument, Rate: rate, EffectiveAt: at, NextAt: next}, FundingValid: ticker.RelativeFundingRate != "",
		}
		derivative := exchange.DerivativeReferenceEvent{Instrument: instrument, MarkPrice: numberOptional(ticker.MarkPrice), IndexPrice: numberOptional(ticker.Index),
			FundingRate: numberOptional(ticker.RelativeFundingRate), FundingIntervalHours: exchange.OptionalInt{Value: 1, Valid: true}, NextFundingTime: next,
			OpenInterest: numberOptional(ticker.OpenInterest), OpenInterestAsset: instrument.Identity.BaseAsset,
			MarkObservedAt: at, IndexObservedAt: at, FundingObservedAt: at, OpenInterestObservedAt: at}
		if oi := numberDecimal(ticker.OpenInterest); oi.IsPositive() && mark.IsPositive() {
			derivative.OpenInterestNotional = exchange.OptionalDecimal{Value: oi.Mul(mark), Valid: true}
		}
		event.Derivative, event.DerivativeValid = derivative, true
		callbacks.Event(event)
		return nil
	})
}

// ---------------------------------------------------------------------------
// Private
// ---------------------------------------------------------------------------

var krakenAccountFeeds = []kraken.Subscription{{Feed: kraken.FeedOpenOrders}, {Feed: kraken.FeedFills}, {Feed: kraken.FeedOpenPosition}, {Feed: kraken.FeedBalances}}

// krakenAccountState follows one private connection: open orders by id (a
// cancel delta carries only the id) and the symbols with positions (each
// open_positions message is the full set, so a missing symbol closed).
type krakenAccountState struct {
	orders    map[string]exchange.Order
	positions map[string]bool
}

// private runs the authenticated feeds and hands each decoded event to emit.
func (backend *krakenWSBackend) private(ctx context.Context, status func(backendStatus), onError func(error), emit func(accountBackendEvent)) (func() error, error) {
	return startSupervisedStream(ctx, backend.lifecycle, func(runCtx context.Context, ready func()) error {
		state := &krakenAccountState{orders: map[string]exchange.Order{}, positions: map[string]bool{}}
		return backend.client.stream().Run(runCtx, krakenAccountFeeds, kraken.Handler{
			Ready: ready,
			Message: func(message kraken.Message) {
				events, err := state.decode(message)
				if err != nil {
					onError(exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: exchange.VenueKraken, Product: exchange.ProductPerp, Operation: "Watch", SafeMessage: err.Error()}))
					return
				}
				for _, event := range events {
					emit(event)
				}
			},
		})
	}, status, onError, func(err error) bool {
		if errors.Is(err, kraken.ErrChallengeRejected) || errors.Is(err, kraken.ErrCredentialsRequired) {
			onError(exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: exchange.VenueKraken, Product: exchange.ProductPerp, Operation: "Watch",
				SafeMessage: "kraken private stream authentication rejected"}))
		}
		return false
	})
}

func (state *krakenAccountState) decode(message kraken.Message) ([]accountBackendEvent, error) {
	switch message.Feed {
	case kraken.FeedOpenOrders:
		var payload kraken.OpenOrdersMessage
		if kraken.Decode(message.Raw, &payload) != nil {
			return nil, fmt.Errorf("kraken open_orders message is malformed")
		}
		if message.Snapshot {
			state.orders = map[string]exchange.Order{}
			out := make([]accountBackendEvent, 0, len(payload.Orders))
			for _, row := range payload.Orders {
				order := krakenWSOrder(row)
				state.orders[order.OrderID] = order
				out = append(out, krakenOrderEvent(order))
			}
			return out, nil
		}
		return state.orderDelta(payload), nil
	case kraken.FeedFills:
		if message.Snapshot {
			// Historical fills; REST reconciliation covers them.
			return nil, nil
		}
		var payload kraken.FillsMessage
		if kraken.Decode(message.Raw, &payload) != nil {
			return nil, fmt.Errorf("kraken fills message is malformed")
		}
		out := make([]accountBackendEvent, 0, len(payload.Fills))
		for _, row := range payload.Fills {
			fill := krakenWSFill(row)
			out = append(out, accountBackendEvent{Kind: exchange.AccountEventFill, NativeSymbol: fill.NativeSymbol, Fill: &exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}})
		}
		return out, nil
	case kraken.FeedOpenPosition:
		var payload kraken.PositionsMessage
		if kraken.Decode(message.Raw, &payload) != nil {
			return nil, fmt.Errorf("kraken open_positions message is malformed")
		}
		return state.positionSet(payload), nil
	case kraken.FeedBalances:
		var payload kraken.BalancesMessage
		if kraken.Decode(message.Raw, &payload) != nil {
			return nil, fmt.Errorf("kraken balances message is malformed")
		}
		if payload.FlexFutures == nil {
			return nil, nil
		}
		currencies := map[string]kraken.FlexCurrency{}
		for asset, row := range payload.FlexFutures.Currencies {
			currencies[asset] = kraken.FlexCurrency{Quantity: row.Quantity, Value: row.Value, Available: row.Available}
		}
		balances := krakenBalances(currencies)
		for asset, row := range currencies {
			// A currency that went to zero must be removed downstream.
			if numberDecimal(row.Quantity).IsZero() {
				balances = append(balances, exchange.Balance{Asset: krakenAsset(asset)})
			}
		}
		return []accountBackendEvent{{Kind: exchange.AccountEventBalance, Balance: &exchange.BalanceEvent{Kind: exchange.EventSnapshot, Balances: balances, Time: krakenMillis(payload.Timestamp)}}}, nil
	}
	return nil, nil
}

// orderDelta applies an open_orders delta. Kraken removes an order with
// is_cancel and a reason; the reason says whether it filled, was cancelled
// or was rejected.
func (state *krakenAccountState) orderDelta(payload kraken.OpenOrdersMessage) []accountBackendEvent {
	var order exchange.Order
	if payload.Order != nil {
		order = krakenWSOrder(*payload.Order)
		if previous, ok := state.orders[order.OrderID]; ok && order.ClientOrderID == "" {
			order.ClientOrderID = previous.ClientOrderID
		}
	} else {
		previous, ok := state.orders[payload.OrderID]
		if !ok {
			// An order this connection never saw open: nothing to remove.
			return nil
		}
		order = previous
	}
	if !payload.IsCancel {
		state.orders[order.OrderID] = order
		return []accountBackendEvent{krakenOrderEvent(order)}
	}
	delete(state.orders, order.OrderID)
	order.UpdatedAt = time.Now().UTC()
	switch payload.Reason {
	case "full_fill":
		order.Status, order.Filled = "filled", order.Quantity
	case "not_enough_margin", "post_order_failed_because_it_would_filled", "would_execute_self", "would_not_reduce_position", "market_inactive":
		order.Status, order.RejectCode, order.RejectMessage = "rejected", payload.Reason, krakenReasonText(payload.Reason)
	default:
		order.Status = "canceled"
		if order.Filled.IsPositive() {
			order.Status = "partially_filled_canceled"
		}
		if payload.Reason == "liquidation" {
			order.Origin = exchange.OrderOriginLiquidation
		}
	}
	return []accountBackendEvent{krakenOrderEvent(order)}
}

func krakenReasonText(reason string) string {
	switch reason {
	case "not_enough_margin":
		return "Insufficient margin"
	case "post_order_failed_because_it_would_filled":
		return "Post-only order would have filled"
	case "would_execute_self":
		return "Order would trade against your own order"
	case "would_not_reduce_position":
		return "Reduce-only order would not reduce the position"
	case "market_inactive":
		return "Market is inactive"
	}
	return reason
}

func (state *krakenAccountState) positionSet(payload kraken.PositionsMessage) []accountBackendEvent {
	current := map[string]bool{}
	var out []accountBackendEvent
	for _, row := range payload.Positions {
		position := krakenWSPosition(row)
		if position.Quantity.IsZero() {
			continue
		}
		current[row.Instrument] = true
		out = append(out, accountBackendEvent{Kind: exchange.AccountEventPosition, NativeSymbol: row.Instrument,
			Position: &exchange.PositionEvent{Kind: exchange.EventSnapshot, Positions: []exchange.Position{position}, Time: time.Now().UTC()}})
	}
	for symbol := range state.positions {
		if !current[symbol] {
			out = append(out, accountBackendEvent{Kind: exchange.AccountEventPosition, NativeSymbol: symbol,
				Position: &exchange.PositionEvent{Kind: exchange.EventSnapshot, Positions: []exchange.Position{}, Time: time.Now().UTC()}})
		}
	}
	state.positions = current
	return out
}

func krakenOrderEvent(order exchange.Order) accountBackendEvent {
	return accountBackendEvent{Kind: exchange.AccountEventOrder, NativeSymbol: order.NativeSymbol, Order: &exchange.OrderEvent{Kind: exchange.EventDelta, Order: order}}
}

func krakenWSOrder(row kraken.WSOrder) exchange.Order {
	filled := numberDecimal(row.Filled)
	order := exchange.Order{NativeSymbol: row.Instrument, OrderID: row.OrderID, ClientOrderID: krakenPortableClientOrderID(row.ClientOrderID),
		Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: numberDecimal(row.Qty), Filled: filled, ReduceOnly: row.ReduceOnly,
		LimitPrice: numberDecimal(row.LimitPrice), LimitPolicy: exchange.LimitPolicyResting, Status: "open",
		CreatedAt: krakenMillis(row.Time), UpdatedAt: krakenMillis(row.LastUpdateTime)}
	if row.Direction == 1 {
		order.Side = exchange.SideSell
	}
	if filled.IsPositive() {
		order.Status = "partially_filled"
	}
	return order
}

func krakenWSFill(row kraken.WSFill) exchange.Fill {
	fill := exchange.Fill{NativeSymbol: row.Instrument, OrderID: row.OrderID, ClientOrderID: krakenPortableClientOrderID(row.ClientOrderID), FillID: row.FillID,
		Side: exchange.SideSell, Price: numberDecimal(row.Price), Quantity: numberDecimal(row.Qty), Fee: numberDecimal(row.FeePaid),
		FeeAsset: krakenAsset(row.FeeCurrency), Liquidity: krakenLiquidity(row.FillType), Time: krakenMillis(row.Time)}
	if row.Buy {
		fill.Side = exchange.SideBuy
	}
	return fill
}

// krakenWSPosition: balance is the signed size in contracts (base units).
func krakenWSPosition(row kraken.WSPosition) exchange.Position {
	quantity := numberDecimal(row.Balance)
	position := exchange.Position{NativeSymbol: row.Instrument, PositionSlot: exchange.PositionSlotNet, Quantity: quantity,
		EntryPrice: numberDecimal(row.EntryPrice), MarkPrice: numberDecimal(row.MarkPrice), UnrealizedPnL: numberDecimal(row.PnL)}
	switch {
	case quantity.IsPositive():
		position.Side = exchange.SideBuy
	case quantity.IsNegative():
		position.Side = exchange.SideSell
	}
	if liq := numberDecimal(row.LiquidationThreshold); liq.IsPositive() {
		position.LiquidationPrice = exchange.OptionalDecimal{Value: liq, Valid: true}
	}
	if leverage := numberDecimal(row.EffectiveLeverage); leverage.IsPositive() {
		position.Leverage = exchange.OptionalDecimal{Value: leverage.Round(2), Valid: true}
	}
	if margin := numberDecimal(row.InitialMargin); margin.IsPositive() {
		position.MarginUsed = exchange.OptionalDecimal{Value: margin, Valid: true}
	}
	return position
}

// StartAccountEvents streams the account. The account also holds inverse
// and dated contracts the gateway does not serve; their events are dropped
// here rather than failing the stream.
func (backend *krakenWSBackend) StartAccountEvents(ctx context.Context, index accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	return backend.private(ctx, func(status backendStatus) { callbacks.Status(accountStreamStatus(backend.meta(), status)) }, callbacks.Error, func(event accountBackendEvent) {
		if servedAccountEvent(index, event) {
			callbacks.Event(event)
		}
	})
}

// Per-instrument private watches filter the account stream.

func krakenPrivateFilter[T any](backend *krakenWSBackend, ctx context.Context, instrument *exchange.Instrument, callbacks streamCallbacks[T], pick func(accountBackendEvent) (T, bool)) (func() error, error) {
	native := ""
	if instrument != nil {
		var err error
		if native, err = backend.client.native("Watch", *instrument); err != nil {
			return nil, err
		}
	}
	return backend.private(ctx, callbacks.Status, callbacks.Error, func(event accountBackendEvent) {
		if native != "" && event.NativeSymbol != native {
			return
		}
		if value, ok := pick(event); ok {
			callbacks.Event(value)
		}
	})
}

func (backend *krakenWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	return krakenPrivateFilter(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.OrderEvent, bool) {
		if event.Order == nil {
			return exchange.OrderEvent{}, false
		}
		return *event.Order, true
	})
}

func (backend *krakenWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	return krakenPrivateFilter(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.FillEvent, bool) {
		if event.Fill == nil {
			return exchange.FillEvent{}, false
		}
		return *event.Fill, true
	})
}

func (backend *krakenWSBackend) StartPositions(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	return krakenPrivateFilter(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.PositionEvent, bool) {
		if event.Position == nil {
			return exchange.PositionEvent{}, false
		}
		return *event.Position, true
	})
}

func (backend *krakenWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	return krakenPrivateFilter(backend, ctx, nil, callbacks, func(event accountBackendEvent) (exchange.BalanceEvent, bool) {
		if event.Balance == nil {
			return exchange.BalanceEvent{}, false
		}
		return *event.Balance, true
	})
}

func (backend *krakenWSBackend) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.PlaceOrder(ctx, request)
}

func (backend *krakenWSBackend) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.CancelOrder(ctx, request)
}

func krakenMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

var (
	_ perpWSBackend        = (*krakenWSBackend)(nil)
	_ perpPrivateWSBackend = (*krakenWSBackend)(nil)
	_ accountEventBackend  = (*krakenWSBackend)(nil)
)
