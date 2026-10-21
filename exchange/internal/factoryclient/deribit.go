package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	deribit "github.com/QuantProcessing/keeluni/sdk/deribit"
	"github.com/shopspring/decimal"
)

// Deribit USDC linear perpetuals ("BASE_USDC-PERPETUAL"). Amounts are in the
// base currency, so quantities need no conversion; the amount step is the
// contract size. Orders are synchronous: private/buy answers with the order's
// state and any immediate trades.
//
// Client order ids travel as Deribit labels. Deribit keeps labelled orders
// queryable for 30 minutes after they close.

const deribitCurrency = "USDC"

type deribitPerpClient struct {
	*deribitBase
	ws *perpWebSocket
}

func NewDeribitPerp(clientID, clientSecret string, settings Settings) exchange.PerpClient {
	client := &deribitPerpClient{deribitBase: newDeribitBase(clientID, clientSecret, exchange.ProductPerp, deribitScope{
		kind: deribit.KindFuture, currencies: []string{deribitCurrency}, eventCurrency: deribitCurrency,
		serves: func(name string) bool { return strings.HasSuffix(name, "_USDC-PERPETUAL") },
	}, settings)}
	backend := newDeribitWSBackend(client.deribitBase)
	client.ws = newPerpWebSocket(client.meta, backend, backend)
	return client
}

func newDeribitSDK(settings Settings) *deribit.Client {
	sdk := deribit.NewClient()
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func (client *deribitPerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }
func (client *deribitPerpClient) Close() error                      { return client.ws.Close() }

func (client *deribitBase) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := deribitContext(ctx, client.meta, "Instruments"); err != nil {
		return nil, err
	}
	if client.meta.catalog == nil || client.meta.catalogScope == (exchange.CatalogScope{}) {
		return nil, client.meta.catalogMetadataResolution("Instruments", "instrument catalog is not configured")
	}
	snapshot, err := client.meta.catalog.Snapshot(ctx, client.meta.catalogScope)
	if err != nil {
		return nil, withExchangeOperation(err, "Instruments")
	}
	return append([]exchange.Instrument(nil), snapshot.Instruments...), nil
}

func (client *deribitBase) native(operation string, instrument exchange.Instrument) (string, error) {
	if instrument.Identity.Venue != exchange.VenueDeribit || instrument.Identity.Product != client.meta.product || instrument.Identity.NativeSymbol == "" {
		return "", client.meta.catalogInvalidRequest(operation, "instrument venue/product does not match this Deribit client")
	}
	return instrument.Identity.NativeSymbol, nil
}

// ---------------------------------------------------------------------------
// Market data
// ---------------------------------------------------------------------------

func (client *deribitBase) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := deribitContext(ctx, client.meta, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	native, err := client.native("OrderBook", request.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	depth := request.Limit
	if depth <= 0 || depth > 1000 {
		depth = 50
	}
	book, err := client.sdk.OrderBook(ctx, native, depth)
	if err != nil {
		return exchange.OrderBook{}, deribitNormalizeErr(client.meta, "OrderBook", err)
	}
	return exchange.OrderBook{Instrument: request.Instrument, Bids: deribitRESTLevels(book.Bids), Asks: deribitRESTLevels(book.Asks),
		Time: krakenMillis(book.Timestamp), Sequence: strconv.FormatInt(book.ChangeID, 10), Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

func deribitRESTLevels(rows [][2]jsonNumber) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if quantity := numberDecimal(row[1]); quantity.IsPositive() {
			out = append(out, exchange.BookLevel{Price: numberDecimal(row[0]), Quantity: quantity})
		}
	}
	return out
}

// deribitResolutions maps portable intervals onto Deribit chart
// resolutions. Deribit has no 4h bar: 4h candles are built from hourly ones
// (source is the bar fetched, step the bar returned).
var deribitResolutions = map[string]struct {
	resolution string
	source     time.Duration
	step       time.Duration
}{
	"1m": {"1", time.Minute, time.Minute}, "5m": {"5", 5 * time.Minute, 5 * time.Minute}, "15m": {"15", 15 * time.Minute, 15 * time.Minute},
	"30m": {"30", 30 * time.Minute, 30 * time.Minute}, "1h": {"60", time.Hour, time.Hour}, "4h": {"60", time.Hour, 4 * time.Hour},
	"12h": {"720", 12 * time.Hour, 12 * time.Hour}, "1d": {"1D", 24 * time.Hour, 24 * time.Hour},
}

// deribitAggregate folds source bars into step bars aligned to UTC multiples
// of step. A bar is complete once every source bar in it closed.
func deribitAggregate(bars []exchange.Candle, step time.Duration) []exchange.Candle {
	var out []exchange.Candle
	for _, bar := range bars {
		open := bar.OpenTime.Truncate(step)
		if len(out) == 0 || !out[len(out)-1].OpenTime.Equal(open) {
			out = append(out, exchange.Candle{OpenTime: open, CloseTime: open.Add(step), Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume})
		} else {
			current := &out[len(out)-1]
			current.High, current.Low = decimal.Max(current.High, bar.High), decimal.Min(current.Low, bar.Low)
			current.Close, current.Volume = bar.Close, current.Volume.Add(bar.Volume)
		}
	}
	now := time.Now()
	for index := range out {
		out[index].Complete = !out[index].CloseTime.After(now)
	}
	return out
}

func (client *deribitBase) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := deribitContext(ctx, client.meta, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	spec, ok := deribitResolutions[request.Interval]
	if !ok {
		return exchange.CandlePage{}, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "Candles",
			SafeMessage: "deribit has no " + request.Interval + " candles"})
	}
	native, err := client.native("Candles", request.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	limit := request.Limit
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	end := request.End
	if end.IsZero() {
		end = time.Now().UTC()
	}
	start := request.Start
	if start.IsZero() {
		start = end.Add(-time.Duration(limit) * spec.step)
	}
	// Whole aggregated bars: fetch from the start of the first one.
	fetchStart := start.Truncate(spec.step)
	chart, err := client.sdk.Chart(ctx, native, spec.resolution, fetchStart.UnixMilli(), end.UnixMilli())
	if err != nil {
		return exchange.CandlePage{}, deribitNormalizeErr(client.meta, "Candles", err)
	}
	if len(chart.Open) != len(chart.Ticks) || len(chart.High) != len(chart.Ticks) || len(chart.Low) != len(chart.Ticks) ||
		len(chart.Close) != len(chart.Ticks) || len(chart.Volume) != len(chart.Ticks) {
		return exchange.CandlePage{}, deribitMalformed(client.meta, "Candles", "deribit chart columns differ in length")
	}
	now := time.Now()
	bars := make([]exchange.Candle, 0, len(chart.Ticks))
	for index, tick := range chart.Ticks {
		open := time.UnixMilli(tick).UTC()
		closeTime := open.Add(spec.source)
		bars = append(bars, exchange.Candle{OpenTime: open, CloseTime: closeTime,
			Open: numberDecimal(chart.Open[index]), High: numberDecimal(chart.High[index]), Low: numberDecimal(chart.Low[index]),
			Close: numberDecimal(chart.Close[index]), Volume: numberDecimal(chart.Volume[index]), Complete: !closeTime.After(now)})
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTime.Before(bars[j].OpenTime) })
	if spec.step != spec.source {
		bars = deribitAggregate(bars, spec.step)
	}
	candles := make([]exchange.Candle, 0, len(bars))
	for _, bar := range bars {
		if withinHalfOpen(bar.OpenTime, start.Truncate(spec.step), end.Add(time.Nanosecond)) {
			candles = append(candles, bar)
		}
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: limit, WindowStart: start, WindowEnd: end}}, nil
}

func (client *deribitBase) PublicTrades(ctx context.Context, request exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := deribitContext(ctx, client.meta, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	native, err := client.native("PublicTrades", request.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	count := request.Limit
	if count <= 0 || count > 1000 {
		count = 100
	}
	rows, err := client.sdk.LastTrades(ctx, native, count)
	if err != nil {
		return exchange.PublicTradePage{}, deribitNormalizeErr(client.meta, "PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		trades = append(trades, exchange.PublicTrade{Instrument: request.Instrument, TradeID: row.TradeID, Side: deribitSide(row.Direction),
			Price: numberDecimal(row.Price), Quantity: numberDecimal(row.Amount), Time: krakenMillis(row.Timestamp)})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

// FundingRate reports the current hourly rate. Deribit's perpetual funding
// accrues continuously; funding_8h is the 8-hour equivalent, so the hourly
// figure is an eighth of it, comparable with the hourly venues.
func (client *deribitPerpClient) FundingRate(ctx context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := deribitContext(ctx, client.meta, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	native, err := client.native("FundingRate", request.Instrument)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	ticker, err := client.sdk.Ticker(ctx, native)
	if err != nil {
		return exchange.FundingRate{}, deribitNormalizeErr(client.meta, "FundingRate", err)
	}
	now := time.Now().UTC()
	rate := exchange.FundingRate{NativeSymbol: native, Rate: deribitHourly(ticker.Funding8h), ObservedAt: now, NextFundingTime: now.Truncate(time.Hour).Add(time.Hour)}
	if mark := numberDecimal(ticker.MarkPrice); mark.IsPositive() {
		rate.MarkPrice = exchange.OptionalDecimal{Value: mark, Valid: true}
	}
	return rate, nil
}

func deribitHourly(funding8h jsonNumber) decimal.Decimal {
	return numberDecimal(funding8h).Div(decimal.NewFromInt(8))
}

func (client *deribitPerpClient) FundingRateHistory(ctx context.Context, request exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := deribitContext(ctx, client.meta, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	native, err := client.native("FundingRateHistory", request.Instrument)
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	end := request.End
	if end.IsZero() {
		end = time.Now().UTC()
	}
	start := request.Start
	if start.IsZero() {
		start = end.Add(-7 * 24 * time.Hour)
	}
	rows, err := client.sdk.FundingRateHistory(ctx, native, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return exchange.FundingRatePage{}, deribitNormalizeErr(client.meta, "FundingRateHistory", err)
	}
	rates := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		rates = append(rates, exchange.FundingRate{NativeSymbol: native, Rate: numberDecimal(row.Interest1h), FundingTime: krakenMillis(row.Timestamp)})
	}
	sort.Slice(rates, func(i, j int) bool { return rates[i].FundingTime.After(rates[j].FundingTime) })
	if request.Limit > 0 && len(rates) > request.Limit {
		rates = rates[:request.Limit]
	}
	return exchange.FundingRatePage{Rates: rates, Page: exchange.PageInfo{Limit: request.Limit, WindowStart: start, WindowEnd: end}}, nil
}

func (client *deribitBase) MarkPrice(ctx context.Context, instrument exchange.Instrument) (exchange.MarkPriceEvent, error) {
	native, err := client.native("MarkPrice", instrument)
	if err != nil {
		return exchange.MarkPriceEvent{}, err
	}
	ticker, err := client.sdk.Ticker(ctx, native)
	if err != nil {
		return exchange.MarkPriceEvent{}, deribitNormalizeErr(client.meta, "MarkPrice", err)
	}
	return exchange.MarkPriceEvent{Instrument: instrument, Price: numberDecimal(ticker.MarkPrice), Time: krakenMillis(ticker.Timestamp)}, nil
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

func (client *deribitPerpClient) summary(ctx context.Context, operation string) (deribit.AccountSummary, error) {
	if err := deribitContext(ctx, client.meta, operation); err != nil {
		return deribit.AccountSummary{}, err
	}
	summary, err := client.sdk.AccountSummary(ctx, deribitCurrency)
	if err != nil {
		return deribit.AccountSummary{}, deribitNormalizeErr(client.meta, operation, err)
	}
	return summary, nil
}

// deribitSummaryBalances is one currency summary as a balance in that
// currency.
func deribitSummaryBalances(summary deribit.AccountSummary) []exchange.Balance {
	currency := strings.ToUpper(strings.TrimSpace(summary.Currency))
	if currency == "" {
		currency = deribitCurrency
	}
	balances := deribitBalances(summary)
	balances[0].Asset = currency
	return balances
}

func deribitBalances(summary deribit.AccountSummary) []exchange.Balance {
	total, available := numberDecimal(summary.Balance), numberDecimal(summary.AvailableFunds)
	if available.GreaterThan(total) {
		// Available funds include unrealized profit; the free balance cannot
		// exceed what is held.
		available = total
	}
	locked := total.Sub(available)
	if locked.IsNegative() {
		locked = decimal.Zero
	}
	return []exchange.Balance{{Asset: deribitCurrency, Total: total, Available: available, Locked: locked}}
}

func (client *deribitPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	summary, err := client.summary(ctx, "Balances")
	if err != nil {
		return nil, err
	}
	return deribitBalances(summary), nil
}

func (client *deribitPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	summary, err := client.summary(ctx, "PerpAccount")
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	return exchange.PerpAccount{
		Balances: deribitBalances(summary), Equity: numberOptional(summary.Equity), Available: numberOptional(summary.AvailableFunds),
		MarginUsed: numberOptional(summary.InitialMargin), UnrealizedPnL: numberOptional(summary.SessionUPL),
	}, nil
}

func (client *deribitPerpClient) Positions(ctx context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := deribitContext(ctx, client.meta, "Positions"); err != nil {
		return nil, err
	}
	filter := ""
	if request.Instrument != nil {
		native, err := client.native("Positions", *request.Instrument)
		if err != nil {
			return nil, err
		}
		filter = native
	}
	rows, err := client.sdk.Positions(ctx, deribitCurrency)
	if err != nil {
		return nil, deribitNormalizeErr(client.meta, "Positions", err)
	}
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if !strings.HasSuffix(row.InstrumentName, "_USDC-PERPETUAL") || (filter != "" && row.InstrumentName != filter) {
			continue
		}
		if position := deribitPosition(row); !position.Quantity.IsZero() {
			out = append(out, position)
		}
	}
	return out, nil
}

// deribitPosition: size is signed base units for linear instruments.
func deribitPosition(row deribit.Position) exchange.Position {
	quantity := numberDecimal(row.Size)
	if row.Direction == "sell" && quantity.IsPositive() {
		quantity = quantity.Neg()
	}
	position := exchange.Position{NativeSymbol: row.InstrumentName, PositionSlot: exchange.PositionSlotNet, Quantity: quantity,
		EntryPrice: numberDecimal(row.AveragePrice), MarkPrice: numberDecimal(row.MarkPrice), UnrealizedPnL: numberDecimal(row.FloatingProfitLoss)}
	switch {
	case quantity.IsPositive():
		position.Side = exchange.SideBuy
	case quantity.IsNegative():
		position.Side = exchange.SideSell
	}
	if liq := numberDecimal(row.EstimatedLiquidationPrice); liq.IsPositive() {
		position.LiquidationPrice = exchange.OptionalDecimal{Value: liq, Valid: true}
	}
	if leverage := numberDecimal(row.Leverage); leverage.IsPositive() {
		position.Leverage = exchange.OptionalDecimal{Value: leverage.Round(2), Valid: true}
	}
	if margin := numberDecimal(row.InitialMargin); margin.IsPositive() {
		position.MarginUsed = exchange.OptionalDecimal{Value: margin, Valid: true}
	}
	return position
}

// SetLeverage is not an instrument setting on Deribit: margin is account
// wide and leverage follows from position size.
func (client *deribitPerpClient) SetLeverage(ctx context.Context, request exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := deribitContext(ctx, client.meta, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	if _, err := client.native("SetLeverage", request.Instrument); err != nil {
		return exchange.Leverage{}, err
	}
	return exchange.Leverage{}, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "SetLeverage",
		SafeMessage: "deribit sets no per-instrument leverage; margin is account-wide"})
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

func deribitOrderRequest(request exchange.PlaceOrderRequest) deribit.OrderRequest {
	out := deribit.OrderRequest{Direction: "buy", Instrument: request.Instrument.Identity.NativeSymbol, Amount: request.Quantity.String(),
		Label: request.ClientOrderID, ReduceOnly: request.ReduceOnly}
	if request.Side == exchange.SideSell {
		out.Direction = "sell"
	}
	switch request.Type {
	case exchange.OrderTypeMarket:
		out.Type = "market"
	default:
		out.Type, out.Price, out.TimeInForce = "limit", request.LimitPrice.String(), "good_til_cancelled"
		switch request.LimitPolicy {
		case exchange.LimitPolicyIOC:
			out.TimeInForce = "immediate_or_cancel"
		case exchange.LimitPolicyPostOnly:
			out.PostOnly = true
		}
	}
	return out
}

func (client *deribitBase) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := deribitContext(ctx, client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, err := client.native("PlaceOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(client.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueDeribit, Product: client.meta.product, Operation: exchange.OrderOperationPlace,
		NativeSymbol: native, OrderType: request.Type, ClientOrderID: request.ClientOrderID}
	result, err := client.sdk.Place(ctx, deribitOrderRequest(request))
	if err != nil {
		return deribitCommandErr(client.meta, ack, err)
	}
	order := result.Order
	if order.OrderID == "" {
		ack.State = exchange.AckAmbiguous
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "PlaceOrder",
			SafeMessage: "deribit returned no order id"})
	}
	ack.OrderID = order.OrderID
	ack.FilledQuantity = numberDecimal(order.FilledAmount)
	if price := numberDecimal(order.AveragePrice); price.IsPositive() && ack.FilledQuantity.IsPositive() {
		ack.AverageFillPrice = exchange.OptionalDecimal{Value: price, Valid: true}
	}
	switch order.OrderState {
	case "filled":
		ack.State = exchange.AckImmediatelyFilled
	case "cancelled":
		ack.State = exchange.AckCanceled
	case "rejected":
		ack.State, ack.VenueCode, ack.VenueMessage = exchange.AckRejected, order.CancelReason, deribitReasonText(order.CancelReason)
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "PlaceOrder",
			Code: order.CancelReason, SafeMessage: "deribit rejected the order"})
	default:
		ack.State = exchange.AckResting
		if ack.FilledQuantity.IsPositive() {
			ack.State = exchange.AckPartiallyFilled
		}
	}
	return ack, nil
}

func (client *deribitBase) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := deribitContext(ctx, client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, err := client.native("CancelOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(request.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, client.meta.catalogInvalidRequest("CancelOrder", "order id is required")
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueDeribit, Product: client.meta.product, Operation: exchange.OrderOperationCancel, NativeSymbol: native, OrderID: request.OrderID}
	order, err := client.sdk.Cancel(ctx, request.OrderID)
	if err != nil {
		return deribitCommandErr(client.meta, ack, err)
	}
	if order.OrderID != "" && order.OrderID != request.OrderID {
		return ack, deribitMalformed(client.meta, "CancelOrder", "deribit cancel response is for another order")
	}
	ack.State = exchange.AckCanceled
	return ack, nil
}

func (client *deribitBase) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := deribitContext(ctx, client.meta, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	filter := ""
	if request.Instrument != nil {
		native, err := client.native("OpenOrders", *request.Instrument)
		if err != nil {
			return exchange.OrderPage{}, err
		}
		filter = native
	}
	rows, err := client.sdk.AllOpenOrders(ctx, client.scope.kind)
	if err != nil {
		return exchange.OrderPage{}, deribitNormalizeErr(client.meta, "OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if !client.scope.serves(row.InstrumentName) || (filter != "" && row.InstrumentName != filter) {
			continue
		}
		orders = append(orders, deribitOrder(row))
	}
	return boundedOrderPage(orders, request.Limit, ""), nil
}

// OrderHistory pages by offset: the cursor is the next offset.
func (client *deribitBase) OrderHistory(ctx context.Context, request exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := deribitContext(ctx, client.meta, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := validateBoundedHistory(request.Cursor, request.Limit, request.Start, request.End); err != nil {
		return exchange.OrderPage{}, client.meta.catalogInvalidRequest("OrderHistory", err.Error())
	}
	filter := ""
	if request.Instrument != nil {
		native, err := client.native("OrderHistory", *request.Instrument)
		if err != nil {
			return exchange.OrderPage{}, err
		}
		filter = native
	}
	count, offset := request.Limit, 0
	if count <= 0 || count > 100 {
		count = 100
	}
	if request.Cursor != "" {
		parsed, err := strconv.Atoi(request.Cursor)
		if err != nil || parsed < 0 {
			return exchange.OrderPage{}, client.meta.catalogInvalidRequest("OrderHistory", "cursor is invalid")
		}
		offset = parsed
	}
	var rows []deribit.Order
	full := false
	seen := map[string]bool{}
	for _, currency := range client.scope.currencies {
		batch, err := client.sdk.OrderHistoryOf(ctx, currency, client.scope.kind, count, offset)
		if err != nil {
			return exchange.OrderPage{}, deribitNormalizeErr(client.meta, "OrderHistory", err)
		}
		full = full || len(batch) >= count
		for _, row := range batch {
			// A pair can be filed under both of its currencies.
			if !seen[row.OrderID] {
				seen[row.OrderID] = true
				rows = append(rows, row)
			}
		}
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		order := deribitOrder(row)
		if !client.scope.serves(row.InstrumentName) || (filter != "" && row.InstrumentName != filter) || !withinHalfOpen(order.UpdatedAt, request.Start, request.End) {
			continue
		}
		orders = append(orders, order)
	}
	page := exchange.PageInfo{Limit: count, WindowStart: request.Start, WindowEnd: request.End, HasMoreKnown: true}
	if full {
		page.Cursor, page.HasMore = strconv.Itoa(offset+count), true
	}
	return exchange.OrderPage{Orders: orders, Page: page}, nil
}

func (client *deribitBase) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	if err := deribitContext(ctx, client.meta, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	if err := validateBoundedHistory(request.Cursor, request.Limit, request.Start, request.End); err != nil {
		return exchange.FillPage{}, client.meta.catalogInvalidRequest("Fills", err.Error())
	}
	filter := ""
	if request.Instrument != nil {
		native, err := client.native("Fills", *request.Instrument)
		if err != nil {
			return exchange.FillPage{}, err
		}
		filter = native
	}
	count := request.Limit
	if count <= 0 || count > 1000 {
		count = 100
	}
	end := optionalMillis(request.End)
	if request.Cursor != "" {
		parsed, err := strconv.ParseInt(request.Cursor, 10, 64)
		if err != nil || parsed <= 0 {
			return exchange.FillPage{}, client.meta.catalogInvalidRequest("Fills", "cursor is invalid")
		}
		end = parsed
	}
	var rows []deribit.Trade
	seen := map[string]bool{}
	for _, currency := range client.scope.currencies {
		batch, err := client.sdk.UserTradesOf(ctx, currency, client.scope.kind, optionalMillis(request.Start), end, count)
		if err != nil {
			return exchange.FillPage{}, deribitNormalizeErr(client.meta, "Fills", err)
		}
		for _, row := range batch {
			if !seen[row.TradeID] {
				seen[row.TradeID] = true
				rows = append(rows, row)
			}
		}
	}
	fills := make([]exchange.Fill, 0, len(rows))
	var oldest int64
	for _, row := range rows {
		if oldest == 0 || row.Timestamp < oldest {
			oldest = row.Timestamp
		}
		if !client.scope.serves(row.InstrumentName) || (filter != "" && row.InstrumentName != filter) {
			continue
		}
		if request.OrderID != "" && row.OrderID != request.OrderID {
			continue
		}
		fills = append(fills, deribitFill(row))
	}
	page := exchange.PageInfo{Limit: count, WindowStart: request.Start, WindowEnd: request.End, HasMoreKnown: true}
	if len(rows) >= count && oldest > 0 && len(client.scope.currencies) == 1 {
		page.Cursor, page.HasMore = strconv.FormatInt(oldest-1, 10), true
	}
	return exchange.FillPage{Fills: fills, Page: page}, nil
}

// LookupExactOrder finds an order by label (client order id) or id. Labels
// remain queryable for 30 minutes after the order closes; by id the lookup is
// authoritative. A label miss is authoritative only for orders placed in
// that window, which the caller cannot prove, so it is unconfirmed.
func (client *deribitBase) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueDeribit, client.meta.product)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if request.ClientOrderID == "" {
		order, err := client.sdk.OrderState(ctx, request.VenueOrderID)
		switch {
		case err == nil:
			if order.InstrumentName != instrument.Identity.NativeSymbol {
				return exchange.ExactOrderLookupResult{}, exactLookupError(exchange.VenueDeribit, client.meta.product, exchange.KindMalformedResponse, "deribit order belongs to another instrument")
			}
			return exactLookupFound(request, exchange.VenueDeribit, client.meta.product, instrument, deribitOrder(order))
		case deribit.IsOrderNotFound(err):
			return exactLookupNotFound(request, exchange.VenueDeribit, client.meta.product, instrument)
		default:
			return exchange.ExactOrderLookupResult{}, deribitNormalizeErr(client.meta, exactLookupOperation, err)
		}
	}
	rows, err := client.sdk.OrderStateByLabel(ctx, client.scope.labelCurrency(instrument), request.ClientOrderID)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, deribitNormalizeErr(client.meta, exactLookupOperation, err)
	}
	for _, row := range rows {
		if row.Label == request.ClientOrderID && row.InstrumentName == instrument.Identity.NativeSymbol {
			order := deribitOrder(row)
			order.ClientOrderID = request.ClientOrderID
			return exactLookupFound(request, exchange.VenueDeribit, client.meta.product, instrument, order)
		}
	}
	return exactLookupUnconfirmed(request, exchange.VenueDeribit, client.meta.product, instrument, "")
}

func deribitOrder(row deribit.Order) exchange.Order {
	filled := numberDecimal(row.FilledAmount)
	order := exchange.Order{NativeSymbol: row.InstrumentName, OrderID: row.OrderID, ClientOrderID: crossExPortableClientOrderID(row.Label),
		Side: deribitSide(row.Direction), Type: exchange.OrderTypeLimit, Quantity: numberDecimal(row.Amount), Filled: filled, ReduceOnly: row.ReduceOnly,
		CreatedAt: krakenMillis(row.CreationTimestamp), UpdatedAt: krakenMillis(row.LastUpdateTimestamp)}
	if row.OrderType == "market" {
		order.Type = exchange.OrderTypeMarket
	} else {
		order.LimitPrice = stringDecimal(row.LimitPrice())
		switch {
		case row.PostOnly:
			order.LimitPolicy = exchange.LimitPolicyPostOnly
		case row.TimeInForce == "immediate_or_cancel" || row.TimeInForce == "fill_or_kill":
			order.LimitPolicy = exchange.LimitPolicyIOC
		default:
			order.LimitPolicy = exchange.LimitPolicyResting
		}
	}
	if price := numberDecimal(row.AveragePrice); price.IsPositive() && filled.IsPositive() {
		order.AverageFillPrice = exchange.OptionalDecimal{Value: price, Valid: true}
	}
	switch row.OrderState {
	case "open", "untriggered", "triggered":
		order.Status = "open"
		if filled.IsPositive() {
			order.Status = "partially_filled"
		}
	case "filled":
		order.Status = "filled"
	case "cancelled":
		order.Status = "canceled"
		if filled.IsPositive() {
			order.Status = "partially_filled_canceled"
		}
	case "rejected":
		order.Status, order.RejectCode, order.RejectMessage = "rejected", row.CancelReason, deribitReasonText(row.CancelReason)
	default:
		order.Status = row.OrderState
	}
	if row.IsLiquidation {
		order.Origin = exchange.OrderOriginLiquidation
	}
	return order
}

func deribitReasonText(reason string) string {
	if reason == "" {
		return ""
	}
	text := strings.ReplaceAll(reason, "_", " ")
	return strings.ToUpper(text[:1]) + text[1:]
}

func deribitFill(row deribit.Trade) exchange.Fill {
	fill := exchange.Fill{NativeSymbol: row.InstrumentName, OrderID: row.OrderID, ClientOrderID: crossExPortableClientOrderID(row.Label), FillID: row.TradeID,
		Side: deribitSide(row.Direction), Price: numberDecimal(row.Price), Quantity: numberDecimal(row.Amount), Fee: numberDecimal(row.Fee),
		FeeAsset: strings.ToUpper(row.FeeCurrency), Liquidity: exchange.LiquidityTaker, Time: krakenMillis(row.Timestamp)}
	if row.Liquidity == "M" {
		fill.Liquidity = exchange.LiquidityMaker
	}
	return fill
}

func deribitSide(direction string) exchange.Side {
	if direction == "sell" {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func deribitContext(ctx context.Context, meta clientMeta, operation string) error {
	if ctx == nil {
		return meta.catalogInvalidRequest(operation, "context is required")
	}
	return meta.catalogContextErr(ctx, operation)
}

func deribitMalformed(meta clientMeta, operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: message})
}

func deribitNormalizeErr(meta clientMeta, operation string, err error) (returnErr error) {
	defer func() { returnErr = normalizeHTTPError(returnErr, err, meta.venue, meta.product, operation) }()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "operation canceled"})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "operation deadline exceeded"})
	}
	var normalized *exchange.Error
	if errors.As(err, &normalized) {
		return withExchangeOperation(err, operation)
	}
	if errors.Is(err, deribit.ErrCredentialsRequired) {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "deribit credentials are required"})
	}
	var rpcErr *deribit.RPCError
	if errors.As(err, &rpcErr) {
		kind := exchange.KindVenueRejected
		switch {
		case deribit.IsAuthError(err):
			kind = exchange.KindAuthentication
			if rpcErr.Code == deribit.CodeForbidden {
				kind = exchange.KindPermission
			}
		case rpcErr.Code == deribit.CodeTooManyRequests || rpcErr.StatusCode == http.StatusTooManyRequests:
			kind = exchange.KindRateLimit
		case rpcErr.Code == deribit.CodeOrderNotFound:
			kind = exchange.KindNotFound
		case rpcErr.Code == deribit.CodeInvalidArguments:
			kind = exchange.KindInvalidRequest
		}
		return exchange.NewError(kind, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, Code: strconv.Itoa(rpcErr.Code), SafeMessage: "deribit request failed"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "deribit transport failed"})
}

// deribitCommandErr: Deribit refuses a command with a JSON-RPC error; codes
// below 11000 and the argument errors are business refusals. Rate limits,
// transport and server failures leave the outcome unknown.
func deribitCommandErr(meta clientMeta, ack exchange.OrderAcknowledgement, err error) (returnAck exchange.OrderAcknowledgement, returnErr error) {
	operation := "PlaceOrder"
	if ack.Operation == exchange.OrderOperationCancel {
		operation = "CancelOrder"
	}
	defer func() { normalizeHTTPCommand(&returnAck, &returnErr, err, meta.venue, meta.product, operation) }()
	var rpcErr *deribit.RPCError
	if errors.As(err, &rpcErr) && rpcErr.StatusCode < 500 && rpcErr.Code != 0 && rpcErr.Code != deribit.CodeTooManyRequests && rpcErr.Code != 11094 && rpcErr.Code != 10040 {
		ack.State, ack.VenueCode = exchange.AckRejected, strconv.Itoa(rpcErr.Code)
		ack.VenueMessage = deribitReasonText(rpcErr.Message)
		if rpcErr.Reason != "" {
			ack.VenueMessage = fmt.Sprintf("%s: %s", ack.VenueMessage, cleanVenueMessage(rpcErr.Reason))
		}
		return ack, deribitNormalizeErr(meta, operation, err)
	}
	if errors.Is(err, deribit.ErrCredentialsRequired) {
		ack.State = exchange.AckRejected
		return ack, deribitNormalizeErr(meta, operation, err)
	}
	ack.State = exchange.AckAmbiguous
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "deribit command outcome is ambiguous"})
}

// deribitScope is the slice of a Deribit account one client serves: one
// instrument kind in some currencies, and which native names belong to it.
type deribitScope struct {
	kind       string
	currencies []string
	// eventCurrency names the private-stream currency ("any" for all).
	eventCurrency string
	serves        func(native string) bool
}

// labelCurrency is the currency Deribit files an instrument's orders under,
// which get_order_state_by_label requires.
func (scope deribitScope) labelCurrency(instrument exchange.Instrument) string {
	if len(scope.currencies) == 1 {
		return scope.currencies[0]
	}
	if instrument.Identity.Product == exchange.ProductOption {
		return instrument.Identity.SettleAsset
	}
	if instrument.Identity.Product == exchange.ProductSpot {
		return instrument.Identity.BaseAsset
	}
	return instrument.Identity.SettleAsset
}

// deribitBase is what every Deribit product client shares: catalog-backed
// instruments, market data, orders and the lookup. Product clients add the
// account surface of their product.
type deribitBase struct {
	meta   clientMeta
	sdk    *deribit.Client
	wsURL  string
	stream func() *deribit.Stream
	scope  deribitScope
	// requests collapses identical whole-market reads across callers.
	requests exchange.MarketRequestCoordinator
}

func newDeribitBase(clientID, clientSecret string, product exchange.Product, scope deribitScope, settings Settings) *deribitBase {
	wsURL := settings.WebSocketEndpoint
	return &deribitBase{
		meta:     clientMetaFromSettings(exchange.VenueDeribit, product, settings),
		sdk:      newDeribitSDK(settings).WithCredentials(clientID, clientSecret),
		wsURL:    wsURL,
		scope:    scope,
		requests: settings.MarketRequests,
		stream: func() *deribit.Stream {
			stream := deribit.NewStream().WithCredentials(clientID, clientSecret)
			if wsURL != "" {
				stream.WithURL(wsURL)
			}
			return stream
		},
	}
}

func (client *deribitBase) String() string   { return client.meta.redactedString() }
func (client *deribitBase) GoString() string { return client.String() }

func (client *deribitBase) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *deribitBase) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return client.meta.RefreshInstruments(ctx)
}

var (
	_ exchange.PerpClient        = (*deribitPerpClient)(nil)
	_ exchange.ExactOrderLookup  = (*deribitPerpClient)(nil)
	_ exchange.PerpMarkPriceREST = (*deribitPerpClient)(nil)
)
