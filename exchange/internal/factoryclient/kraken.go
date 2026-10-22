package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	kraken "github.com/QuantProcessing/keeluni/sdk/kraken"
	"github.com/shopspring/decimal"
)

// Kraken Futures multi-collateral linear perpetuals (PF_*): USD-quoted,
// contract size 1 base unit, so quantities need no conversion. Kraken trades
// "XBT"; the canonical symbol uses the instrument's base field ("BTC").
//
// Orders are synchronous: sendorder answers with the placement outcome and
// the events it caused (immediate executions, rejections), so the ack is
// definitive whenever Kraken answers.

type krakenPerpClient struct {
	*perpClient
	sdk    *kraken.Client
	ws     *perpWebSocket
	wsURL  string
	stream func() *kraken.Stream
}

func NewKrakenPerp(apiKey, secret string, settings Settings) exchange.PerpClient {
	sdk := newKrakenSDK(settings).WithCredentials(apiKey, secret)
	wsURL := settings.WebSocketEndpoint
	client := &krakenPerpClient{
		perpClient: &perpClient{meta: clientMetaFromSettings(exchange.VenueKraken, exchange.ProductPerp, settings)},
		sdk:        sdk,
		wsURL:      wsURL,
		stream: func() *kraken.Stream {
			stream := kraken.NewStream().WithCredentials(apiKey, secret)
			if wsURL != "" {
				stream.WithURL(wsURL)
			}
			return stream
		},
	}
	backend := newKrakenWSBackend(client)
	client.ws = newPerpWebSocket(client.meta, backend, backend)
	return client
}

func newKrakenSDK(settings Settings) *kraken.Client {
	sdk := kraken.NewClient()
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func (client *krakenPerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }
func (client *krakenPerpClient) Close() error                      { return client.ws.Close() }
func (client *krakenPerpClient) String() string                    { return client.meta.redactedString() }
func (client *krakenPerpClient) GoString() string                  { return client.String() }

func (client *krakenPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := krakenContext(ctx, client.meta, "Instruments"); err != nil {
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

// native validates that instrument belongs to this client.
func (client *krakenPerpClient) native(operation string, instrument exchange.Instrument) (string, error) {
	if instrument.Identity.Venue != exchange.VenueKraken || instrument.Identity.Product != exchange.ProductPerp || instrument.Identity.NativeSymbol == "" {
		return "", client.meta.catalogInvalidRequest(operation, "instrument venue/product does not match Kraken perp")
	}
	return instrument.Identity.NativeSymbol, nil
}

// byNative resolves a Kraken symbol through the catalog.
func (client *krakenPerpClient) byNative(ctx context.Context, native string) (exchange.Instrument, bool) {
	if client.meta.catalog == nil {
		return exchange.Instrument{}, false
	}
	instrument, err := client.meta.catalog.Resolve(ctx, client.meta.catalogScope, exchange.InstrumentQuery{NativeSymbol: native})
	return instrument, err == nil
}

// ---------------------------------------------------------------------------
// Market data
// ---------------------------------------------------------------------------

func (client *krakenPerpClient) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := krakenContext(ctx, client.meta, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	native, err := client.native("OrderBook", request.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	book, err := client.sdk.OrderBook(ctx, native)
	if err != nil {
		return exchange.OrderBook{}, krakenNormalizeErr(client.meta, "OrderBook", err)
	}
	bids, asks := krakenRESTLevels(book.Bids, true), krakenRESTLevels(book.Asks, false)
	if request.Limit > 0 {
		bids, asks = bids[:min(len(bids), request.Limit)], asks[:min(len(asks), request.Limit)]
	}
	observed, _ := time.Parse(time.RFC3339Nano, book.ServerTime)
	return exchange.OrderBook{Instrument: request.Instrument, Bids: bids, Asks: asks, Time: observed.UTC(), Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

func krakenRESTLevels(rows [][2]jsonNumber, descending bool) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		price, quantity := numberDecimal(row[0]), numberDecimal(row[1])
		if price.IsPositive() && quantity.IsPositive() {
			out = append(out, exchange.BookLevel{Price: price, Quantity: quantity})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if descending {
			return out[i].Price.GreaterThan(out[j].Price)
		}
		return out[i].Price.LessThan(out[j].Price)
	})
	return out
}

var krakenResolutions = map[string]time.Duration{
	"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute,
	"1h": time.Hour, "4h": 4 * time.Hour, "12h": 12 * time.Hour, "1d": 24 * time.Hour,
}

func (client *krakenPerpClient) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := krakenContext(ctx, client.meta, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	interval, ok := krakenResolutions[request.Interval]
	if !ok {
		return exchange.CandlePage{}, client.meta.catalogInvalidRequest("Candles", "interval must be one of 1m, 5m, 15m, 30m, 1h, 4h, 12h, or 1d")
	}
	native, err := client.native("Candles", request.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	start, end := request.Start, request.End
	limit := request.Limit
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		start = end.Add(-time.Duration(limit) * interval)
	}
	rows, err := client.sdk.Candles(ctx, native, request.Interval, start.Unix(), end.Unix())
	if err != nil {
		return exchange.CandlePage{}, krakenNormalizeErr(client.meta, "Candles", err)
	}
	return exchange.CandlePage{Candles: krakenCandles(rows, interval, start, end, limit), Page: exchange.PageInfo{Limit: limit, WindowStart: start, WindowEnd: end}}, nil
}

// krakenCandles keeps candles opening in [start, end) and the newest limit.
// The open candle (closing after now) is incomplete.
func krakenCandles(rows []kraken.Candle, interval time.Duration, start, end time.Time, limit int) []exchange.Candle {
	now := time.Now()
	out := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		open := time.UnixMilli(row.Time).UTC()
		if !withinHalfOpen(open, start, end.Add(time.Nanosecond)) {
			continue
		}
		closeTime := open.Add(interval)
		out = append(out, exchange.Candle{
			OpenTime: open, CloseTime: closeTime,
			Open: stringDecimal(row.Open), High: stringDecimal(row.High), Low: stringDecimal(row.Low), Close: stringDecimal(row.Close),
			Volume: stringDecimal(row.Volume), Complete: !closeTime.After(now),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenTime.Before(out[j].OpenTime) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (client *krakenPerpClient) PublicTrades(ctx context.Context, request exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := krakenContext(ctx, client.meta, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	native, err := client.native("PublicTrades", request.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	rows, err := client.sdk.TradeHistory(ctx, native)
	if err != nil {
		return exchange.PublicTradePage{}, krakenNormalizeErr(client.meta, "PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		at, _ := time.Parse(time.RFC3339Nano, row.Time)
		trades = append(trades, exchange.PublicTrade{Instrument: request.Instrument, TradeID: row.UID, Side: krakenSide(row.Side),
			Price: numberDecimal(row.Price), Quantity: numberDecimal(row.Size), Time: at.UTC()})
	}
	if request.Limit > 0 && len(trades) > request.Limit {
		trades = trades[:request.Limit]
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

// FundingRate reports the current hourly rate. Kraken's relativeFundingRate
// is the fraction of position value paid per hour, the portable unit;
// fundingRate is an absolute USD amount per contract and is not used.
func (client *krakenPerpClient) FundingRate(ctx context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := krakenContext(ctx, client.meta, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	native, err := client.native("FundingRate", request.Instrument)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	ticker, err := client.sdk.Ticker(ctx, native)
	if err != nil {
		return exchange.FundingRate{}, krakenNormalizeErr(client.meta, "FundingRate", err)
	}
	now := time.Now().UTC()
	rate := exchange.FundingRate{NativeSymbol: native, Rate: numberDecimal(ticker.RelativeFundingRate), ObservedAt: now,
		NextFundingTime: now.Truncate(time.Hour).Add(time.Hour)}
	if mark := numberDecimal(ticker.MarkPrice); mark.IsPositive() {
		rate.MarkPrice = exchange.OptionalDecimal{Value: mark, Valid: true}
	}
	return rate, nil
}

func (client *krakenPerpClient) FundingRateHistory(ctx context.Context, request exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := krakenContext(ctx, client.meta, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	native, err := client.native("FundingRateHistory", request.Instrument)
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	rows, err := client.sdk.FundingRates(ctx, native)
	if err != nil {
		return exchange.FundingRatePage{}, krakenNormalizeErr(client.meta, "FundingRateHistory", err)
	}
	rates := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		at, err := time.Parse(time.RFC3339, row.Timestamp)
		if err != nil || !withinHalfOpen(at, request.Start, request.End) {
			continue
		}
		rates = append(rates, exchange.FundingRate{NativeSymbol: native, Rate: numberDecimal(row.RelativeFundingRate), FundingTime: at.UTC()})
	}
	// Newest first, bounded.
	sort.Slice(rates, func(i, j int) bool { return rates[i].FundingTime.After(rates[j].FundingTime) })
	if request.Limit > 0 && len(rates) > request.Limit {
		rates = rates[:request.Limit]
	}
	return exchange.FundingRatePage{Rates: rates, Page: exchange.PageInfo{Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}}, nil
}

// MarkPrice serves the optional REST mark facet.
func (client *krakenPerpClient) MarkPrice(ctx context.Context, instrument exchange.Instrument) (exchange.MarkPriceEvent, error) {
	native, err := client.native("MarkPrice", instrument)
	if err != nil {
		return exchange.MarkPriceEvent{}, err
	}
	ticker, err := client.sdk.Ticker(ctx, native)
	if err != nil {
		return exchange.MarkPriceEvent{}, krakenNormalizeErr(client.meta, "MarkPrice", err)
	}
	return exchange.MarkPriceEvent{Instrument: instrument, Price: numberDecimal(ticker.MarkPrice), Time: time.Now().UTC()}, nil
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

// flex reads the multi-collateral account. PF_ perps trade only from it.
func (client *krakenPerpClient) flex(ctx context.Context, operation string) (*kraken.FlexAccount, error) {
	if err := krakenContext(ctx, client.meta, operation); err != nil {
		return nil, err
	}
	accounts, err := client.sdk.Accounts(ctx)
	if err != nil {
		return nil, krakenNormalizeErr(client.meta, operation, err)
	}
	if accounts.Flex == nil {
		return nil, exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: operation,
			SafeMessage: "kraken multi-collateral account is missing"})
	}
	return accounts.Flex, nil
}

func (client *krakenPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	flex, err := client.flex(ctx, "Balances")
	if err != nil {
		return nil, err
	}
	return krakenBalances(flex.Currencies), nil
}

// krakenBalances lists collateral currencies. Kraken reports a currency's
// quantity and how much of it is available; the rest backs margin.
func krakenBalances(currencies map[string]kraken.FlexCurrency) []exchange.Balance {
	assets := make([]string, 0, len(currencies))
	for asset := range currencies {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	out := make([]exchange.Balance, 0, len(assets))
	for _, asset := range assets {
		row := currencies[asset]
		total, available := numberDecimal(row.Quantity), numberDecimal(row.Available)
		if total.IsZero() && available.IsZero() {
			continue
		}
		locked := total.Sub(available)
		if locked.IsNegative() {
			locked = decimal.Zero
		}
		out = append(out, exchange.Balance{Asset: krakenAsset(asset), Total: total, Available: available, Locked: locked})
	}
	return out
}

// PerpAccount reports the margin account in USD: equity is margin equity
// (collateral after haircuts plus unrealized PnL), available is margin free
// for new orders.
func (client *krakenPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	flex, err := client.flex(ctx, "PerpAccount")
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	return exchange.PerpAccount{
		Balances:      krakenBalances(flex.Currencies),
		Equity:        numberOptional(flex.MarginEquity),
		Available:     numberOptional(flex.AvailableMargin),
		MarginUsed:    numberOptional(flex.InitialMarginWithOrders),
		UnrealizedPnL: numberOptional(flex.TotalUnrealized),
	}, nil
}

func (client *krakenPerpClient) Positions(ctx context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := krakenContext(ctx, client.meta, "Positions"); err != nil {
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
	rows, err := client.sdk.OpenPositions(ctx)
	if err != nil {
		return nil, krakenNormalizeErr(client.meta, "Positions", err)
	}
	// Mark prices come from the tickers; positions carry none.
	marks := map[string]decimal.Decimal{}
	if len(rows) > 0 {
		if tickers, err := client.sdk.Tickers(ctx); err == nil {
			for _, ticker := range tickers {
				marks[ticker.Symbol] = numberDecimal(ticker.MarkPrice)
			}
		}
	}
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if !strings.HasPrefix(row.Symbol, "PF_") || (filter != "" && row.Symbol != filter) {
			continue
		}
		position := krakenRESTPosition(row, marks[row.Symbol])
		if position.Quantity.IsZero() {
			continue
		}
		out = append(out, position)
	}
	return out, nil
}

func krakenRESTPosition(row kraken.OpenPosition, mark decimal.Decimal) exchange.Position {
	size := numberDecimal(row.Size).Abs()
	position := exchange.Position{NativeSymbol: row.Symbol, PositionSlot: exchange.PositionSlotNet, Quantity: size,
		EntryPrice: numberDecimal(row.Price), MarkPrice: mark, Side: exchange.SideBuy}
	if strings.EqualFold(row.Side, "short") {
		position.Quantity, position.Side = size.Neg(), exchange.SideSell
	}
	if pnl := numberDecimal(row.UnrealizedPnl); !pnl.IsZero() {
		position.UnrealizedPnL = pnl
	} else if mark.IsPositive() {
		position.UnrealizedPnL = mark.Sub(position.EntryPrice).Mul(position.Quantity)
	}
	if leverage := numberDecimal(row.MaxFixedLeverage); leverage.IsPositive() {
		position.Leverage = exchange.OptionalDecimal{Value: leverage, Valid: true}
	}
	return position
}

// SetLeverage sets an isolated maximum leverage for the symbol. Kraken's
// leverage preference is per symbol and puts that symbol in isolated margin;
// without one the symbol trades cross margin at the account's leverage.
func (client *krakenPerpClient) SetLeverage(ctx context.Context, request exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := krakenContext(ctx, client.meta, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	native, err := client.native("SetLeverage", request.Instrument)
	if err != nil {
		return exchange.Leverage{}, err
	}
	if request.Leverage <= 0 {
		return exchange.Leverage{}, client.meta.catalogInvalidRequest("SetLeverage", "leverage must be positive")
	}
	if err := client.sdk.SetLeveragePreference(ctx, native, strconv.Itoa(request.Leverage)); err != nil {
		return exchange.Leverage{}, krakenNormalizeErr(client.meta, "SetLeverage", err)
	}
	return exchange.Leverage{NativeSymbol: native, Effective: request.Leverage}, nil
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

func krakenOrderRequest(request exchange.PlaceOrderRequest) kraken.OrderRequest {
	out := kraken.OrderRequest{Symbol: request.Instrument.Identity.NativeSymbol, Side: "buy", Size: request.Quantity.String(),
		ClientOrderID: request.ClientOrderID, ReduceOnly: request.ReduceOnly}
	if request.Side == exchange.SideSell {
		out.Side = "sell"
	}
	switch request.Type {
	case exchange.OrderTypeMarket:
		out.OrderType = "mkt"
	default:
		out.LimitPrice = request.LimitPrice.String()
		switch request.LimitPolicy {
		case exchange.LimitPolicyIOC:
			out.OrderType = "ioc"
		case exchange.LimitPolicyPostOnly:
			out.OrderType = "post"
		default:
			out.OrderType = "lmt"
		}
	}
	return out
}

func (client *krakenPerpClient) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := krakenContext(ctx, client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, err := client.native("PlaceOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueKraken, Product: exchange.ProductPerp, Operation: exchange.OrderOperationPlace,
		NativeSymbol: native, OrderType: request.Type, ClientOrderID: request.ClientOrderID}
	status, err := client.sdk.SendOrder(ctx, krakenOrderRequest(request))
	if err != nil {
		return krakenCommandErr(client.meta, ack, err)
	}
	return krakenPlaceAck(client.meta, ack, request.Quantity, status)
}

// krakenPlaceAck reads the placement outcome. A status other than "placed"
// is Kraken's reason for not placing the order, and is definitive. A placed
// order's events tell whether it rested, filled, or was cancelled at once
// (IOC remainder, post-only that would cross).
func krakenPlaceAck(meta clientMeta, ack exchange.OrderAcknowledgement, requested decimal.Decimal, status kraken.SendStatus) (exchange.OrderAcknowledgement, error) {
	ack.OrderID = status.OrderID
	if !status.IsAccepted() {
		ack.State = exchange.AckRejected
		ack.VenueCode = status.Status
		ack.VenueMessage = krakenRejectionText(status.Status)
		for _, event := range status.OrderEvents {
			if event.Type == "REJECT" && event.Reason != "" {
				ack.VenueCode = event.Reason
			}
		}
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "PlaceOrder",
			Code: ack.VenueCode, SafeMessage: "kraken rejected the order"})
	}
	if ack.OrderID == "" {
		ack.State = exchange.AckAmbiguous
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "PlaceOrder",
			SafeMessage: "kraken placed the order without an order id"})
	}
	filled, cost, cancelled := decimal.Zero, decimal.Zero, false
	for _, event := range status.OrderEvents {
		switch event.Type {
		case "EXECUTION":
			amount, price := numberDecimal(event.Amount), numberDecimal(event.Price)
			filled, cost = filled.Add(amount), cost.Add(amount.Mul(price))
		case "CANCEL", "REJECT":
			cancelled = true
		}
	}
	ack.FilledQuantity = filled
	if filled.IsPositive() {
		ack.AverageFillPrice = exchange.OptionalDecimal{Value: cost.Div(filled), Valid: true}
	}
	switch {
	case filled.GreaterThanOrEqual(requested) && filled.IsPositive():
		ack.State = exchange.AckImmediatelyFilled
	case cancelled && filled.IsZero():
		ack.State = exchange.AckCanceled
	case cancelled:
		ack.State = exchange.AckCanceled
	case filled.IsPositive():
		ack.State = exchange.AckPartiallyFilled
	default:
		ack.State = exchange.AckResting
	}
	return ack, nil
}

// krakenRejectionText turns Kraken's camelCase status into words.
func krakenRejectionText(status string) string {
	var words []string
	start := 0
	for index, r := range status {
		if index > 0 && r >= 'A' && r <= 'Z' {
			words = append(words, strings.ToLower(status[start:index]))
			start = index
		}
	}
	words = append(words, strings.ToLower(status[start:]))
	text := strings.Join(words, " ")
	if text == "" {
		return "kraken rejected the order"
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

func (client *krakenPerpClient) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := krakenContext(ctx, client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, err := client.native("CancelOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(request.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, client.meta.catalogInvalidRequest("CancelOrder", "order id is required")
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueKraken, Product: exchange.ProductPerp, Operation: exchange.OrderOperationCancel,
		NativeSymbol: native, OrderID: request.OrderID}
	status, err := client.sdk.CancelOrder(ctx, request.OrderID, "")
	if err != nil {
		return krakenCommandErr(client.meta, ack, err)
	}
	switch status.Status {
	case "cancelled":
		ack.State = exchange.AckCanceled
		return ack, nil
	case "filled":
		ack.State, ack.VenueCode, ack.VenueMessage = exchange.AckRejected, status.Status, "Order already filled"
	case "notFound":
		ack.State, ack.VenueCode, ack.VenueMessage = exchange.AckRejected, status.Status, "Order not found"
		return ack, exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "CancelOrder",
			Code: status.Status, SafeMessage: "kraken order not found"})
	default:
		ack.State, ack.VenueCode, ack.VenueMessage = exchange.AckRejected, status.Status, krakenRejectionText(status.Status)
	}
	return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "CancelOrder",
		Code: status.Status, SafeMessage: "kraken did not cancel the order"})
}

func (client *krakenPerpClient) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := krakenContext(ctx, client.meta, "OpenOrders"); err != nil {
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
	rows, err := client.sdk.OpenOrders(ctx)
	if err != nil {
		return exchange.OrderPage{}, krakenNormalizeErr(client.meta, "OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if !strings.HasPrefix(row.Symbol, "PF_") || (filter != "" && row.Symbol != filter) || krakenIsTriggerType(row.OrderType) {
			continue
		}
		orders = append(orders, krakenOpenOrder(row))
	}
	return boundedOrderPage(orders, request.Limit, ""), nil
}

func krakenOpenOrder(row kraken.OpenOrder) exchange.Order {
	filled, unfilled := numberDecimal(row.FilledSize), numberDecimal(row.UnfilledSize)
	order := exchange.Order{NativeSymbol: row.Symbol, OrderID: row.OrderID, ClientOrderID: krakenPortableClientOrderID(row.ClientOrderID),
		Side: krakenSide(row.Side), Type: exchange.OrderTypeLimit, Quantity: filled.Add(unfilled), Filled: filled, ReduceOnly: row.ReduceOnly,
		LimitPrice: numberDecimal(row.LimitPrice), LimitPolicy: krakenLimitPolicy(row.OrderType), Status: "open",
		CreatedAt: krakenTime(row.ReceivedTime), UpdatedAt: krakenTime(row.LastUpdateTime)}
	if filled.IsPositive() {
		order.Status = "partially_filled"
	}
	return order
}

// OrderHistory is served from fills: Kraken's account history endpoints are
// not part of the trading API, so closed orders are reconstructed from their
// executions. Orders cancelled without fills are not listed.
func (client *krakenPerpClient) OrderHistory(ctx context.Context, request exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	page, err := client.Fills(ctx, exchange.FillsRequest{Instrument: request.Instrument, Start: request.Start, End: request.End, Cursor: request.Cursor, Limit: 0})
	if err != nil {
		return exchange.OrderPage{}, withExchangeOperation(err, "OrderHistory")
	}
	byID := map[string]*exchange.Order{}
	var order []string
	for _, fill := range page.Fills {
		current, ok := byID[fill.OrderID]
		if !ok {
			current = &exchange.Order{NativeSymbol: fill.NativeSymbol, OrderID: fill.OrderID, ClientOrderID: fill.ClientOrderID, Side: fill.Side,
				Type: exchange.OrderTypeLimit, Status: "filled", CreatedAt: fill.Time, UpdatedAt: fill.Time}
			byID[fill.OrderID] = current
			order = append(order, fill.OrderID)
		}
		cost := decimal.Zero
		if current.AverageFillPrice.Valid {
			cost = current.AverageFillPrice.Value.Mul(current.Filled)
		}
		current.Filled = current.Filled.Add(fill.Quantity)
		current.Quantity = current.Filled
		current.AverageFillPrice = exchange.OptionalDecimal{Value: cost.Add(fill.Price.Mul(fill.Quantity)).Div(current.Filled), Valid: true}
		if fill.Time.Before(current.CreatedAt) {
			current.CreatedAt = fill.Time
		}
		if fill.Time.After(current.UpdatedAt) {
			current.UpdatedAt = fill.Time
		}
	}
	orders := make([]exchange.Order, 0, len(order))
	for _, id := range order {
		orders = append(orders, *byID[id])
	}
	return boundedOrderPage(orders, request.Limit, page.Page.Cursor), nil
}

// Fills pages backwards by fill time: the cursor is the oldest fill time of
// the previous page.
func (client *krakenPerpClient) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	if err := krakenContext(ctx, client.meta, "Fills"); err != nil {
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
	rows, err := client.sdk.Fills(ctx, request.Cursor)
	if err != nil {
		return exchange.FillPage{}, krakenNormalizeErr(client.meta, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	oldest := ""
	for _, row := range rows {
		oldest = row.FillTime
		if !strings.HasPrefix(row.Symbol, "PF_") || (filter != "" && row.Symbol != filter) {
			continue
		}
		if request.OrderID != "" && row.OrderID != request.OrderID {
			continue
		}
		fill := krakenRESTFill(row)
		if !withinHalfOpen(fill.Time, request.Start, request.End) {
			continue
		}
		fills = append(fills, fill)
	}
	if request.Limit > 0 && len(fills) > request.Limit {
		fills = fills[:request.Limit]
	}
	page := exchange.PageInfo{Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}
	// Kraken returns up to 100 fills per call; a full page may have more.
	if len(rows) >= 100 && oldest != "" && (request.Start.IsZero() || krakenTime(oldest).After(request.Start)) {
		page.Cursor, page.HasMore, page.HasMoreKnown = oldest, true, true
	} else {
		page.HasMoreKnown = true
	}
	return exchange.FillPage{Fills: fills, Page: page}, nil
}

func krakenRESTFill(row kraken.Fill) exchange.Fill {
	return exchange.Fill{NativeSymbol: row.Symbol, OrderID: row.OrderID, ClientOrderID: krakenPortableClientOrderID(row.ClientOrderID), FillID: row.FillID,
		Side: krakenSide(row.Side), Price: numberDecimal(row.Price), Quantity: numberDecimal(row.Size), Liquidity: krakenLiquidity(row.FillType),
		Time: krakenTime(row.FillTime)}
}

// LookupExactOrder reads one order by client order id or order id. Kraken
// keeps an order queryable while open and for 5 seconds after it closes, so
// a miss is only authoritative while that window could not have passed; past
// it, recent fills are searched, and otherwise the answer is unconfirmed.
func (client *krakenPerpClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueKraken, exchange.ProductPerp)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	var ids, clientIDs []string
	if request.ClientOrderID != "" {
		clientIDs = []string{request.ClientOrderID}
	} else {
		ids = []string{request.VenueOrderID}
	}
	rows, err := client.sdk.OrderStatus(ctx, ids, clientIDs)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, krakenNormalizeErr(client.meta, exactLookupOperation, err)
	}
	for _, row := range rows {
		if row.Order.Symbol != instrument.Identity.NativeSymbol {
			continue
		}
		if (request.ClientOrderID != "" && row.Order.ClientOrderID == request.ClientOrderID) || (request.VenueOrderID != "" && row.Order.OrderID == request.VenueOrderID) {
			order := krakenStatusOrder(row)
			if request.ClientOrderID != "" {
				order.ClientOrderID = request.ClientOrderID
			}
			return exactLookupFound(request, exchange.VenueKraken, exchange.ProductPerp, instrument, order)
		}
	}
	// Closed more than 5 seconds ago: its fills, if any, prove it existed.
	if fills, err := client.sdk.Fills(ctx, ""); err == nil {
		matched := []kraken.Fill{}
		for _, fill := range fills {
			if fill.Symbol == instrument.Identity.NativeSymbol &&
				((request.ClientOrderID != "" && fill.ClientOrderID == request.ClientOrderID) || (request.VenueOrderID != "" && fill.OrderID == request.VenueOrderID)) {
				matched = append(matched, fill)
			}
		}
		if len(matched) > 0 {
			order := exchange.Order{NativeSymbol: matched[0].Symbol, OrderID: matched[0].OrderID, ClientOrderID: request.ClientOrderID,
				Side: krakenSide(matched[0].Side), Type: exchange.OrderTypeLimit, Status: "filled", UpdatedAt: krakenTime(matched[0].FillTime)}
			cost := decimal.Zero
			for _, fill := range matched {
				quantity := numberDecimal(fill.Size)
				order.Filled = order.Filled.Add(quantity)
				cost = cost.Add(quantity.Mul(numberDecimal(fill.Price)))
			}
			order.Quantity = order.Filled
			order.AverageFillPrice = exchange.OptionalDecimal{Value: cost.Div(order.Filled), Valid: true}
			return exactLookupFound(request, exchange.VenueKraken, exchange.ProductPerp, instrument, order)
		}
	}
	return exactLookupUnconfirmed(request, exchange.VenueKraken, exchange.ProductPerp, instrument, "")
}

func krakenStatusOrder(row kraken.OrderStatusDetails) exchange.Order {
	source := row.Order
	filled, quantity := numberDecimal(source.Filled), numberDecimal(source.Quantity)
	order := exchange.Order{NativeSymbol: source.Symbol, OrderID: source.OrderID, ClientOrderID: krakenPortableClientOrderID(source.ClientOrderID),
		Side: krakenSide(source.Side), Type: exchange.OrderTypeLimit, Quantity: quantity, Filled: filled, ReduceOnly: source.ReduceOnly,
		LimitPrice: numberDecimal(source.LimitPrice), LimitPolicy: krakenLimitPolicy(source.Type),
		CreatedAt: krakenTime(source.Timestamp), UpdatedAt: krakenTime(source.LastUpdateTimestamp)}
	if source.Type == "mkt" || source.Type == "market" {
		order.Type, order.LimitPrice, order.LimitPolicy = exchange.OrderTypeMarket, decimal.Zero, ""
	}
	switch row.Status {
	case "FULLY_EXECUTED":
		order.Status = "filled"
	case "REJECTED":
		order.Status, order.RejectCode, order.RejectMessage = "rejected", row.Error, krakenRejectionText(row.Error)
	case "CANCELLED":
		order.Status = "canceled"
	default:
		order.Status = "open"
		if filled.IsPositive() {
			order.Status = "partially_filled"
		}
	}
	return order
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func krakenSide(value string) exchange.Side {
	if strings.EqualFold(strings.TrimSpace(value), "sell") {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

func krakenLimitPolicy(orderType string) exchange.LimitPolicy {
	switch orderType {
	case "ioc":
		return exchange.LimitPolicyIOC
	case "post":
		return exchange.LimitPolicyPostOnly
	}
	return exchange.LimitPolicyResting
}

func krakenLiquidity(fillType string) exchange.Liquidity {
	if fillType == "maker" {
		return exchange.LiquidityMaker
	}
	return exchange.LiquidityTaker
}

// krakenAsset names a collateral currency; Kraken writes bitcoin "XBT".
func krakenAsset(asset string) string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "XBT" {
		return "BTC"
	}
	return asset
}

// krakenPortableClientOrderID accepts only portable uint48 ids; orders placed
// elsewhere carry UUIDs.
func krakenPortableClientOrderID(value string) string { return crossExPortableClientOrderID(value) }

func krakenTime(value string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return at.UTC()
}

func krakenContext(ctx context.Context, meta clientMeta, operation string) error {
	if ctx == nil {
		return meta.catalogInvalidRequest(operation, "context is required")
	}
	return meta.catalogContextErr(ctx, operation)
}

func krakenNormalizeErr(meta clientMeta, operation string, err error) (returnErr error) {
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
	if errors.Is(err, kraken.ErrCredentialsRequired) {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "kraken credentials are required"})
	}
	var apiErr *kraken.APIError
	if errors.As(err, &apiErr) {
		kind := exchange.KindVenueRejected
		switch {
		case kraken.IsAuthError(err):
			kind = exchange.KindAuthentication
			if apiErr.StatusCode == http.StatusForbidden {
				kind = exchange.KindPermission
			}
		case apiErr.Code == "apiLimitExceeded" || apiErr.StatusCode == http.StatusTooManyRequests:
			kind = exchange.KindRateLimit
		case apiErr.Code == "notFound" || apiErr.StatusCode == http.StatusNotFound:
			kind = exchange.KindNotFound
		case apiErr.Code == "invalidArgument" || apiErr.Code == "requiredArgumentMissing":
			kind = exchange.KindInvalidRequest
		}
		return exchange.NewError(kind, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, Code: apiErr.Code, SafeMessage: "kraken request failed"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "kraken transport failed"})
}

// krakenCommandErr: a failed command is a rejection only when Kraken
// answered with an error it documents as refusing the request; a transport
// failure, 5xx or 429 leaves the outcome unknown.
func krakenCommandErr(meta clientMeta, ack exchange.OrderAcknowledgement, err error) (returnAck exchange.OrderAcknowledgement, returnErr error) {
	operation := "PlaceOrder"
	if ack.Operation == exchange.OrderOperationCancel {
		operation = "CancelOrder"
	}
	defer func() { normalizeHTTPCommand(&returnAck, &returnErr, err, meta.venue, meta.product, operation) }()
	var apiErr *kraken.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode < 500 && apiErr.StatusCode != http.StatusTooManyRequests && apiErr.Code != "" && apiErr.Code != "unknownError" && apiErr.Code != "Unavailable" {
		ack.State, ack.VenueCode, ack.VenueMessage = exchange.AckRejected, apiErr.Code, krakenRejectionText(apiErr.Code)
		return ack, krakenNormalizeErr(meta, operation, err)
	}
	if errors.Is(err, kraken.ErrCredentialsRequired) {
		ack.State = exchange.AckRejected
		return ack, krakenNormalizeErr(meta, operation, err)
	}
	ack.State = exchange.AckAmbiguous
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "kraken command outcome is ambiguous"})
}

var (
	_ exchange.PerpClient        = (*krakenPerpClient)(nil)
	_ exchange.ExactOrderLookup  = (*krakenPerpClient)(nil)
	_ exchange.PerpMarkPriceREST = (*krakenPerpClient)(nil)
)
