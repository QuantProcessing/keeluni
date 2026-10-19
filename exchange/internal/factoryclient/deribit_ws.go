package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	deribit "github.com/QuantProcessing/boltertrader/sdk/deribit"
)

// deribitWSBackend serves Deribit streams over JSON-RPC. Each public topic
// owns one supervised connection; private watches share none (each opens its
// own authenticated connection), matching how the socket layer shares topics.
type deribitWSBackend struct {
	client    *deribitPerpClient
	lifecycle context.Context
	cancel    context.CancelFunc
}

func newDeribitWSBackend(client *deribitPerpClient) *deribitWSBackend {
	lifecycle, cancel := context.WithCancel(context.Background())
	return &deribitWSBackend{client: client, lifecycle: lifecycle, cancel: cancel}
}

func (backend *deribitWSBackend) Close() error {
	backend.cancel()
	return nil
}

func (backend *deribitWSBackend) meta() clientMeta { return backend.client.meta }

func (backend *deribitWSBackend) publicStream() *deribit.Stream {
	stream := deribit.NewStream()
	if backend.client.wsURL != "" {
		stream.WithURL(backend.client.wsURL)
	}
	return stream
}

func (backend *deribitWSBackend) public(ctx context.Context, channel string, status func(backendStatus), handle func(json.RawMessage) error) (func() error, error) {
	return startSupervisedStream(ctx, backend.lifecycle, func(runCtx context.Context, ready func()) error {
		sessionCtx, cancel := context.WithCancel(runCtx)
		defer cancel()
		var failure error
		err := backend.publicStream().Run(sessionCtx, []string{channel}, deribit.Handler{
			Ready: ready,
			Notification: func(note deribit.Notification) {
				if note.Channel != channel {
					return
				}
				if err := handle(note.Data); err != nil && failure == nil {
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

func (backend *deribitWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	native, err := backend.client.native("WatchOrderBook", instrument)
	if err != nil {
		return nil, err
	}
	var last int64
	return backend.public(ctx, "book."+native+".100ms", callbacks.Status, func(data json.RawMessage) error {
		var book deribit.BookChange
		if deribit.Decode(data, &book) != nil {
			return fmt.Errorf("deribit book message is malformed")
		}
		bids, err := deribitBookLevels(book.Bids)
		if err != nil {
			return err
		}
		asks, err := deribitBookLevels(book.Asks)
		if err != nil {
			return err
		}
		event := exchange.BookEvent{Instrument: instrument, Sequence: strconv.FormatInt(book.ChangeID, 10), Bids: bids, Asks: asks, Time: krakenMillis(book.Timestamp)}
		if book.Type == "snapshot" {
			last, event.Kind = book.ChangeID, exchange.EventSnapshot
			callbacks.Event(event)
			return nil
		}
		if last == 0 {
			return nil
		}
		if book.PrevChangeID != last {
			return errStreamResync
		}
		last = book.ChangeID
		event.Kind, event.Previous = exchange.EventDelta, strconv.FormatInt(book.PrevChangeID, 10)
		callbacks.Event(event)
		return nil
	})
}

// deribitBookLevels decodes [action, price, amount]; delete is quantity 0.
func deribitBookLevels(rows [][3]json.RawMessage) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		var action string
		var price, amount json.Number
		if json.Unmarshal(row[0], &action) != nil || json.Unmarshal(row[1], &price) != nil || json.Unmarshal(row[2], &amount) != nil {
			return nil, fmt.Errorf("deribit book level is malformed")
		}
		level := exchange.BookLevel{Price: numberDecimal(price), Quantity: numberDecimal(amount)}
		if action == "delete" {
			level.Quantity = level.Quantity.Sub(level.Quantity)
		}
		out = append(out, level)
	}
	return out, nil
}

func (backend *deribitWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	native, err := backend.client.native("WatchBBO", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, "ticker."+native+".100ms", callbacks.Status, func(data json.RawMessage) error {
		var ticker deribit.Ticker
		if deribit.Decode(data, &ticker) != nil {
			return fmt.Errorf("deribit ticker message is malformed")
		}
		callbacks.Event(exchange.BBOEvent{Instrument: instrument,
			Bid: exchange.BookLevel{Price: numberDecimal(ticker.BestBidPrice), Quantity: numberDecimal(ticker.BestBidAmount)},
			Ask: exchange.BookLevel{Price: numberDecimal(ticker.BestAskPrice), Quantity: numberDecimal(ticker.BestAskAmount)}, Time: krakenMillis(ticker.Timestamp)})
		return nil
	})
}

func (backend *deribitWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	native, err := backend.client.native("WatchPublicTrades", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, "trades."+native+".100ms", callbacks.Status, func(data json.RawMessage) error {
		var trades []deribit.Trade
		if deribit.Decode(data, &trades) != nil {
			return fmt.Errorf("deribit trades message is malformed")
		}
		for _, trade := range trades {
			callbacks.Event(exchange.PublicTradeEvent{Instrument: instrument, TradeID: trade.TradeID, Side: deribitSide(trade.Direction),
				Price: numberDecimal(trade.Price), Quantity: numberDecimal(trade.Amount), Time: krakenMillis(trade.Timestamp)})
		}
		return nil
	})
}

func (backend *deribitWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	spec, ok := deribitResolutions[interval]
	if !ok {
		return nil, websocketError(backend.meta(), "WatchCandles", exchange.KindUnsupported, "deribit has no "+interval+" candles")
	}
	native, err := backend.client.native("WatchCandles", instrument)
	if err != nil {
		return nil, err
	}
	// For built intervals the stream keeps the source bars of the current
	// aggregated bar; a bar from an earlier period starts a new one.
	var bucket []exchange.Candle
	return backend.public(ctx, "chart.trades."+native+"."+spec.resolution, callbacks.Status, func(data json.RawMessage) error {
		var candle deribit.ChartCandle
		if deribit.Decode(data, &candle) != nil {
			return fmt.Errorf("deribit candle message is malformed")
		}
		open := krakenMillis(candle.Tick)
		bar := exchange.Candle{OpenTime: open, CloseTime: open.Add(spec.source), Open: numberDecimal(candle.Open), High: numberDecimal(candle.High),
			Low: numberDecimal(candle.Low), Close: numberDecimal(candle.Close), Volume: numberDecimal(candle.Volume), Complete: !open.Add(spec.source).After(time.Now())}
		if spec.step != spec.source {
			if len(bucket) > 0 && !bucket[0].OpenTime.Truncate(spec.step).Equal(open.Truncate(spec.step)) {
				bucket = bucket[:0]
			}
			if len(bucket) > 0 && bucket[len(bucket)-1].OpenTime.Equal(open) {
				bucket[len(bucket)-1] = bar
			} else {
				bucket = append(bucket, bar)
			}
			bar = deribitAggregate(bucket, spec.step)[0]
		}
		callbacks.Event(exchange.CandleEvent{Instrument: instrument, Interval: interval, Candle: bar})
		return nil
	})
}

// StartReference streams the ticker: mark, index, funding and open interest
// (open_interest is in base units for linear perps). The rate is hourly.
func (backend *deribitWSBackend) StartReference(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	native, err := backend.client.native("WatchDerivativeReference", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, "ticker."+native+".100ms", callbacks.Status, func(data json.RawMessage) error {
		var ticker deribit.Ticker
		if deribit.Decode(data, &ticker) != nil {
			return fmt.Errorf("deribit ticker message is malformed")
		}
		at := krakenMillis(ticker.Timestamp)
		mark, rate := numberDecimal(ticker.MarkPrice), deribitHourly(ticker.Funding8h)
		next := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
		event := perpReferenceEvent{
			MarkPrice: exchange.MarkPriceEvent{Instrument: instrument, Price: mark, Time: at}, MarkValid: mark.IsPositive(),
			FundingRate: exchange.FundingRateEvent{Instrument: instrument, Rate: rate, EffectiveAt: at, NextAt: next}, FundingValid: ticker.Funding8h != "",
		}
		derivative := exchange.DerivativeReferenceEvent{Instrument: instrument, MarkPrice: numberOptional(ticker.MarkPrice), IndexPrice: numberOptional(ticker.IndexPrice),
			FundingIntervalHours: exchange.OptionalInt{Value: 1, Valid: true}, NextFundingTime: next,
			OpenInterest: numberOptional(ticker.OpenInterest), OpenInterestAsset: instrument.Identity.BaseAsset,
			MarkObservedAt: at, IndexObservedAt: at, FundingObservedAt: at, OpenInterestObservedAt: at}
		if ticker.Funding8h != "" {
			derivative.FundingRate = exchange.OptionalDecimal{Value: rate, Valid: true}
		}
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

var deribitAccountChannels = []string{"user.changes.future." + deribitCurrency + ".raw", "user.portfolio." + deribitCurrency}

type deribitChanges struct {
	InstrumentName string             `json:"instrument_name"`
	Trades         []deribit.Trade    `json:"trades"`
	Orders         []deribit.Order    `json:"orders"`
	Positions      []deribit.Position `json:"positions"`
}

func deribitAccountEvents(note deribit.Notification) ([]accountBackendEvent, error) {
	if note.Channel == deribitAccountChannels[1] {
		var summary deribit.AccountSummary
		if deribit.Decode(note.Data, &summary) != nil {
			return nil, fmt.Errorf("deribit portfolio message is malformed")
		}
		return []accountBackendEvent{{Kind: exchange.AccountEventBalance, Balance: &exchange.BalanceEvent{Kind: exchange.EventSnapshot, Balances: deribitBalances(summary), Time: time.Now().UTC()}}}, nil
	}
	var changes deribitChanges
	if deribit.Decode(note.Data, &changes) != nil {
		return nil, fmt.Errorf("deribit changes message is malformed")
	}
	var out []accountBackendEvent
	for _, row := range changes.Orders {
		order := deribitOrder(row)
		out = append(out, accountBackendEvent{Kind: exchange.AccountEventOrder, NativeSymbol: order.NativeSymbol, Order: &exchange.OrderEvent{Kind: exchange.EventDelta, Order: order}})
	}
	for _, row := range changes.Trades {
		fill := deribitFill(row)
		out = append(out, accountBackendEvent{Kind: exchange.AccountEventFill, NativeSymbol: fill.NativeSymbol, Fill: &exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}})
	}
	for _, row := range changes.Positions {
		position := deribitPosition(row)
		positions := []exchange.Position{}
		if !position.Quantity.IsZero() {
			positions = append(positions, position)
		}
		out = append(out, accountBackendEvent{Kind: exchange.AccountEventPosition, NativeSymbol: row.InstrumentName,
			Position: &exchange.PositionEvent{Kind: exchange.EventSnapshot, Positions: positions, Time: time.Now().UTC()}})
	}
	return out, nil
}

func (backend *deribitWSBackend) private(ctx context.Context, status func(backendStatus), onError func(error), emit func(accountBackendEvent)) (func() error, error) {
	return startSupervisedStream(ctx, backend.lifecycle, func(runCtx context.Context, ready func()) error {
		return backend.client.stream().Run(runCtx, deribitAccountChannels, deribit.Handler{
			Ready: ready,
			Notification: func(note deribit.Notification) {
				events, err := deribitAccountEvents(note)
				if err != nil {
					onError(exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: exchange.VenueDeribit, Product: exchange.ProductPerp, Operation: "Watch", SafeMessage: err.Error()}))
					return
				}
				for _, event := range events {
					emit(event)
				}
			},
		})
	}, status, onError, func(err error) bool {
		if errors.Is(err, deribit.ErrAuthRejected) || errors.Is(err, deribit.ErrCredentialsRequired) {
			onError(exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: exchange.VenueDeribit, Product: exchange.ProductPerp, Operation: "Watch",
				SafeMessage: "deribit private stream authentication rejected"}))
		}
		return false
	})
}

// StartAccountEvents streams the account. Options, dated and inverse
// futures share it; only indexed USDC linear perps are routed.
func (backend *deribitWSBackend) StartAccountEvents(ctx context.Context, index accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	return backend.private(ctx, func(status backendStatus) { callbacks.Status(accountStreamStatus(backend.meta(), status)) }, callbacks.Error, func(event accountBackendEvent) {
		if servedAccountEvent(index, event) {
			callbacks.Event(event)
		}
	})
}

func deribitServed(native string) bool {
	return len(native) > len("_USDC-PERPETUAL") && native[len(native)-len("_USDC-PERPETUAL"):] == "_USDC-PERPETUAL"
}

func deribitPrivateFilter[T any](backend *deribitWSBackend, ctx context.Context, instrument *exchange.Instrument, callbacks streamCallbacks[T], pick func(accountBackendEvent) (T, bool)) (func() error, error) {
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

func (backend *deribitWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	return deribitPrivateFilter(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.OrderEvent, bool) {
		if event.Order == nil {
			return exchange.OrderEvent{}, false
		}
		return *event.Order, true
	})
}

func (backend *deribitWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	return deribitPrivateFilter(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.FillEvent, bool) {
		if event.Fill == nil {
			return exchange.FillEvent{}, false
		}
		return *event.Fill, true
	})
}

func (backend *deribitWSBackend) StartPositions(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	return deribitPrivateFilter(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.PositionEvent, bool) {
		if event.Position == nil {
			return exchange.PositionEvent{}, false
		}
		return *event.Position, true
	})
}

func (backend *deribitWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	return deribitPrivateFilter(backend, ctx, nil, callbacks, func(event accountBackendEvent) (exchange.BalanceEvent, bool) {
		if event.Balance == nil {
			return exchange.BalanceEvent{}, false
		}
		return *event.Balance, true
	})
}

func (backend *deribitWSBackend) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.PlaceOrder(ctx, request)
}

func (backend *deribitWSBackend) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.CancelOrder(ctx, request)
}

var (
	_ perpWSBackend        = (*deribitWSBackend)(nil)
	_ perpPrivateWSBackend = (*deribitWSBackend)(nil)
	_ accountEventBackend  = (*deribitWSBackend)(nil)
)
