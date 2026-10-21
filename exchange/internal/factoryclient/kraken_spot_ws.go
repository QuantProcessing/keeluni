package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	krakenspot "github.com/QuantProcessing/keeluni/sdk/krakenspot"
	"github.com/shopspring/decimal"
)

// krakenSpotWSBackend serves Kraken Spot over WebSocket v2: one supervised
// connection per public topic; the private executions and balances channels
// on the authenticated endpoint with a token fetched on every connect.
type krakenSpotWSBackend struct {
	client    *krakenSpotClient
	lifecycle context.Context
	cancel    context.CancelFunc
}

func newKrakenSpotWSBackend(client *krakenSpotClient) *krakenSpotWSBackend {
	lifecycle, cancel := context.WithCancel(context.Background())
	return &krakenSpotWSBackend{client: client, lifecycle: lifecycle, cancel: cancel}
}

func (backend *krakenSpotWSBackend) Close() error {
	backend.cancel()
	return nil
}

func (backend *krakenSpotWSBackend) meta() clientMeta { return backend.client.meta }

func (backend *krakenSpotWSBackend) public(ctx context.Context, subscription krakenspot.Subscription, status func(backendStatus), handle func(krakenspot.Message) error) (func() error, error) {
	return startSupervisedStream(ctx, backend.lifecycle, func(runCtx context.Context, ready func()) error {
		sessionCtx, cancel := context.WithCancel(runCtx)
		defer cancel()
		var failure error
		err := krakenspot.NewStream().WithURL(backend.client.wsURL).Run(sessionCtx, []krakenspot.Subscription{subscription}, krakenspot.Handler{
			Ready: ready,
			Message: func(message krakenspot.Message) {
				if message.Channel != subscription.Channel {
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

func krakenSpotLevels(rows []krakenspot.BookLevel) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.BookLevel{Price: numberDecimal(row.Price), Quantity: numberDecimal(row.Qty)})
	}
	return out
}

// StartOrderBook keeps depth 100: a snapshot, then level updates where a
// zero quantity deletes the price. Kraken sends no sequence; its checksum
// guards integrity and is left to the consumer's full-book resyncs.
func (backend *krakenSpotWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	native, err := backend.client.native("WatchOrderBook", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, krakenspot.Subscription{Channel: "book", Symbols: []string{native}, Params: map[string]any{"depth": 100}}, callbacks.Status, func(message krakenspot.Message) error {
		var books []krakenspot.Book
		if krakenspot.Decode(message.Data, &books) != nil {
			return fmt.Errorf("kraken spot book message is malformed")
		}
		for _, book := range books {
			if book.Symbol != native {
				continue
			}
			event := exchange.BookEvent{Instrument: instrument, Kind: exchange.EventDelta, Bids: krakenSpotLevels(book.Bids), Asks: krakenSpotLevels(book.Asks), Time: krakenspot.ParseTime(book.Timestamp)}
			if message.Type == "snapshot" {
				event.Kind = exchange.EventSnapshot
			}
			callbacks.Event(event)
		}
		return nil
	})
}

// StartBBO uses the ticker channel triggered on best bid/offer changes.
func (backend *krakenSpotWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	native, err := backend.client.native("WatchBBO", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, krakenspot.Subscription{Channel: "ticker", Symbols: []string{native}, Params: map[string]any{"event_trigger": "bbo"}}, callbacks.Status, func(message krakenspot.Message) error {
		var tickers []krakenspot.Ticker
		if krakenspot.Decode(message.Data, &tickers) != nil {
			return fmt.Errorf("kraken spot ticker message is malformed")
		}
		for _, ticker := range tickers {
			callbacks.Event(exchange.BBOEvent{Instrument: instrument, Time: time.Now().UTC(),
				Bid: exchange.BookLevel{Price: numberDecimal(ticker.Bid), Quantity: numberDecimal(ticker.BidQty)},
				Ask: exchange.BookLevel{Price: numberDecimal(ticker.Ask), Quantity: numberDecimal(ticker.AskQty)}})
		}
		return nil
	})
}

func (backend *krakenSpotWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	native, err := backend.client.native("WatchPublicTrades", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, krakenspot.Subscription{Channel: "trade", Symbols: []string{native}}, callbacks.Status, func(message krakenspot.Message) error {
		var trades []krakenspot.Trade
		if krakenspot.Decode(message.Data, &trades) != nil {
			return fmt.Errorf("kraken spot trade message is malformed")
		}
		for _, trade := range trades {
			side := exchange.SideBuy
			if trade.Side == "sell" {
				side = exchange.SideSell
			}
			callbacks.Event(exchange.PublicTradeEvent{Instrument: instrument, TradeID: trade.TradeID.String(), Side: side,
				Price: numberDecimal(trade.Price), Quantity: numberDecimal(trade.Qty), Time: krakenspot.ParseTime(trade.Timestamp)})
		}
		return nil
	})
}

func (backend *krakenSpotWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	minutes, ok := krakenSpotIntervals[interval]
	if !ok {
		return nil, websocketError(backend.meta(), "WatchCandles", exchange.KindUnsupported, "kraken spot has no "+interval+" candles")
	}
	native, err := backend.client.native("WatchCandles", instrument)
	if err != nil {
		return nil, err
	}
	step := time.Duration(minutes) * time.Minute
	return backend.public(ctx, krakenspot.Subscription{Channel: "ohlc", Symbols: []string{native}, Params: map[string]any{"interval": minutes, "snapshot": false}}, callbacks.Status, func(message krakenspot.Message) error {
		var candles []krakenspot.Candle
		if krakenspot.Decode(message.Data, &candles) != nil {
			return fmt.Errorf("kraken spot ohlc message is malformed")
		}
		for _, candle := range candles {
			open := krakenspot.ParseTime(candle.IntervalBegin)
			callbacks.Event(exchange.CandleEvent{Instrument: instrument, Interval: interval, Candle: exchange.Candle{
				OpenTime: open, CloseTime: open.Add(step), Open: numberDecimal(candle.Open), High: numberDecimal(candle.High), Low: numberDecimal(candle.Low),
				Close: numberDecimal(candle.Close), Volume: numberDecimal(candle.Volume), Complete: !open.Add(step).After(time.Now()),
			}})
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Private
// ---------------------------------------------------------------------------

// krakenSpotExecution projects one executions row. Order-state rows become
// order events; trade rows become a fill and an order update. Partial rows
// (status transitions carry only the changed fields) still name the order.
func krakenSpotExecution(row krakenspot.Execution) []accountBackendEvent {
	var events []accountBackendEvent
	native := row.Symbol
	if row.ExecType == "trade" {
		fill := exchange.Fill{NativeSymbol: native, OrderID: row.OrderID, ClientOrderID: crossExPortableClientOrderID(row.ClientOrderID), FillID: row.ExecID,
			Side: exchange.SideBuy, Price: numberDecimal(row.LastPrice), Quantity: numberDecimal(row.LastQty), Liquidity: exchange.LiquidityTaker,
			Time: krakenspot.ParseTime(row.Timestamp)}
		if row.Side == "sell" {
			fill.Side = exchange.SideSell
		}
		if row.LiquidityInd == "m" {
			fill.Liquidity = exchange.LiquidityMaker
		}
		for _, fee := range row.Fees {
			fill.Fee, fill.FeeAsset = fill.Fee.Add(numberDecimal(fee.Qty)), krakenspot.AssetName(fee.Asset)
		}
		events = append(events, accountBackendEvent{Kind: exchange.AccountEventFill, NativeSymbol: native, Fill: &exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}})
	}
	order := exchange.Order{NativeSymbol: native, OrderID: row.OrderID, ClientOrderID: crossExPortableClientOrderID(row.ClientOrderID),
		Quantity: numberDecimal(row.OrderQty), Filled: numberDecimal(row.CumQty), UpdatedAt: krakenspot.ParseTime(row.Timestamp)}
	if row.Side == "sell" {
		order.Side = exchange.SideSell
	} else if row.Side == "buy" {
		order.Side = exchange.SideBuy
	}
	switch row.OrderType {
	case "market":
		order.Type = exchange.OrderTypeMarket
	case "limit":
		order.Type, order.LimitPrice, order.LimitPolicy = exchange.OrderTypeLimit, numberDecimal(row.LimitPrice), exchange.LimitPolicyResting
		if row.PostOnly {
			order.LimitPolicy = exchange.LimitPolicyPostOnly
		} else if row.TimeInForce == "IOC" {
			order.LimitPolicy = exchange.LimitPolicyIOC
		}
	}
	if price := numberDecimal(row.AvgPrice); price.IsPositive() {
		order.AverageFillPrice = exchange.OptionalDecimal{Value: price, Valid: true}
	}
	switch row.OrderStatus {
	case "pending_new":
		order.Status = "pending"
	case "new":
		order.Status = "open"
	case "partially_filled":
		order.Status = "partially_filled"
	case "filled":
		order.Status = "filled"
	case "canceled", "expired":
		order.Status = "canceled"
		if order.Filled.IsPositive() {
			order.Status = "partially_filled_canceled"
		}
		if row.Reason != "" && strings.Contains(strings.ToLower(row.Reason), "reject") {
			order.Status, order.RejectMessage = "rejected", cleanVenueMessage(row.Reason)
		}
	default:
		order.Status = row.OrderStatus
	}
	if order.Status == "" || native == "" {
		// A row without a status or symbol cannot be routed; the REST
		// reconciliation repairs what it would have said.
		return events
	}
	events = append(events, accountBackendEvent{Kind: exchange.AccountEventOrder, NativeSymbol: native, Order: &exchange.OrderEvent{Kind: exchange.EventDelta, Order: order}})
	return events
}

// krakenSpotBalanceRows reads a balances push. Snapshots carry each asset's
// spot wallet; updates the new balance of one wallet. Earn wallets are not
// spot balances. Held amounts are not on this channel: the account
// reconciliation reads BalanceEx for them.
func krakenSpotBalanceRows(message krakenspot.Message) ([]exchange.Balance, error) {
	var rows []krakenspot.BalanceRow
	if krakenspot.Decode(message.Data, &rows) != nil {
		return nil, fmt.Errorf("kraken spot balances message is malformed")
	}
	var out []exchange.Balance
	for _, row := range rows {
		amount, found := decimal.Zero, false
		if message.Type == "snapshot" {
			for _, wallet := range row.Wallets {
				if wallet.Type == "spot" {
					amount, found = amount.Add(numberDecimal(wallet.Balance)), true
				}
			}
			if len(row.Wallets) == 0 {
				amount, found = numberDecimal(row.Balance), true
			}
		} else if row.WalletType == "" || row.WalletType == "spot" {
			amount, found = numberDecimal(row.Balance), true
		}
		if !found {
			continue
		}
		asset := krakenspot.AssetName(row.Asset)
		out = append(out, exchange.Balance{Asset: asset, Total: amount, Available: amount})
	}
	return out, nil
}

func (backend *krakenSpotWSBackend) private(ctx context.Context, status func(backendStatus), onError func(error), emit func(accountBackendEvent)) (func() error, error) {
	subscriptions := []krakenspot.Subscription{
		{Channel: "executions", Params: map[string]any{"snap_orders": true, "snap_trades": false}},
		{Channel: "balances", Params: map[string]any{"snapshot": true}},
	}
	return startSupervisedStream(ctx, backend.lifecycle, func(runCtx context.Context, ready func()) error {
		stream := krakenspot.NewStream().WithURL(backend.client.authWS).WithToken(backend.client.sdk.WebSocketsToken)
		return stream.Run(runCtx, subscriptions, krakenspot.Handler{
			Ready: ready,
			Message: func(message krakenspot.Message) {
				switch message.Channel {
				case "executions":
					var rows []krakenspot.Execution
					if krakenspot.Decode(message.Data, &rows) != nil {
						onError(exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: exchange.VenueKraken, Product: exchange.ProductSpot, Operation: "Watch", SafeMessage: "kraken spot executions message is malformed"}))
						return
					}
					for _, row := range rows {
						for _, event := range krakenSpotExecution(row) {
							emit(event)
						}
					}
				case "balances":
					balances, err := krakenSpotBalanceRows(message)
					if err != nil {
						onError(exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: exchange.VenueKraken, Product: exchange.ProductSpot, Operation: "Watch", SafeMessage: err.Error()}))
						return
					}
					if len(balances) > 0 {
						emit(accountBackendEvent{Kind: exchange.AccountEventBalance, Balance: &exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: time.Now().UTC()}})
					}
				}
			},
		})
	}, status, onError, func(err error) bool {
		if errors.Is(err, krakenspot.ErrCredentialsRequired) || krakenspot.IsAuthError(err) || errors.Is(err, krakenspot.ErrSubscriptionRejected) {
			onError(exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: exchange.VenueKraken, Product: exchange.ProductSpot, Operation: "Watch",
				SafeMessage: "kraken spot private stream authentication rejected"}))
		}
		return false
	})
}

// StartAccountEvents streams orders and fills. Balance pushes are left out:
// they carry wallet totals without the amounts open orders hold, so they
// would overstate what is available; the account reads BalanceEx after
// every fill and placement instead.
func (backend *krakenSpotWSBackend) StartAccountEvents(ctx context.Context, index accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	return backend.private(ctx, func(status backendStatus) { callbacks.Status(accountStreamStatus(backend.meta(), status)) }, callbacks.Error, func(event accountBackendEvent) {
		if event.Kind == exchange.AccountEventBalance {
			return
		}
		if servedAccountEvent(index, event) {
			callbacks.Event(event)
		}
	})
}

func krakenSpotFiltered[T any](backend *krakenSpotWSBackend, ctx context.Context, instrument *exchange.Instrument, callbacks streamCallbacks[T], pick func(accountBackendEvent) (T, bool)) (func() error, error) {
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

func (backend *krakenSpotWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	return krakenSpotFiltered(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.OrderEvent, bool) {
		if event.Order == nil {
			return exchange.OrderEvent{}, false
		}
		return *event.Order, true
	})
}

func (backend *krakenSpotWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	return krakenSpotFiltered(backend, ctx, &instrument, callbacks, func(event accountBackendEvent) (exchange.FillEvent, bool) {
		if event.Fill == nil {
			return exchange.FillEvent{}, false
		}
		return *event.Fill, true
	})
}

func (backend *krakenSpotWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	return krakenSpotFiltered(backend, ctx, nil, callbacks, func(event accountBackendEvent) (exchange.BalanceEvent, bool) {
		if event.Balance == nil {
			return exchange.BalanceEvent{}, false
		}
		return *event.Balance, true
	})
}

func (backend *krakenSpotWSBackend) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.PlaceOrder(ctx, request)
}

func (backend *krakenSpotWSBackend) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.CancelOrder(ctx, request)
}

var (
	_ publicWSBackend     = (*krakenSpotWSBackend)(nil)
	_ privateWSBackend    = (*krakenSpotWSBackend)(nil)
	_ accountEventBackend = (*krakenSpotWSBackend)(nil)
)
