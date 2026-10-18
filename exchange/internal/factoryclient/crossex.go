package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/shopspring/decimal"
)

// crossExPerpClient is a brokered perp client: one CrossEx account trading
// several venues' order books. Every instrument it accepts or reports keeps
// its underlying venue identity; meta.venue (crossex) owns errors, credentials
// and rate limits only.
//
// Market data is not served here. CrossEx publishes no history and forwards
// each venue's native size units, so callers read market data from the
// instrument's own venue. Market methods return exchange.ErrUnsupported.
type crossExPerpClient struct {
	meta    clientMeta
	sdk     *crossex.Client
	natives func(context.Context) ([]exchange.Instrument, error)

	stream func() *crossex.PrivateStream

	mu          sync.RWMutex
	instruments map[string]exchange.Instrument // by CrossEx symbol

	ws *crossExWebSocket
}

// CrossExNativeSource supplies the native perp catalogs CrossEx symbols are
// matched against.
type CrossExNativeSource func(context.Context) ([]exchange.Instrument, error)

func NewCrossExPerp(apiKey, secretKey string, natives CrossExNativeSource, settings Settings) exchange.PerpClient {
	sdk := crossex.NewClient().WithCredentials(apiKey, secretKey)
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	wsURL := settings.WebSocketEndpoint
	client := &crossExPerpClient{
		meta:    clientMeta{venue: exchange.VenueCrossEx, product: exchange.ProductPerp},
		sdk:     sdk,
		natives: natives,
		stream: func() *crossex.PrivateStream {
			stream := crossex.NewPrivateStream(apiKey, secretKey)
			if wsURL != "" {
				stream.WithURL(wsURL)
			}
			return stream
		},
		instruments: map[string]exchange.Instrument{},
	}
	client.ws = &crossExWebSocket{client: client}
	return client
}

func (client *crossExPerpClient) String() string   { return client.meta.redactedString() }
func (client *crossExPerpClient) GoString() string { return client.String() }

// Broker identifies the brokered client and the native markets it reaches.
func (client *crossExPerpClient) Broker() exchange.Venue { return exchange.VenueCrossEx }

func (client *crossExPerpClient) Reaches() []exchange.MarketRoute {
	out := make([]exchange.MarketRoute, 0, len(crossExMarkets))
	for _, market := range crossExMarkets {
		out = append(out, exchange.MarketRoute{Venue: market.venue, Product: exchange.ProductPerp, Settlement: market.settlement})
	}
	return out
}

func (client *crossExPerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }
func (client *crossExPerpClient) Close() error                      { return client.ws.Close() }

// ---------------------------------------------------------------------------
// Instruments
// ---------------------------------------------------------------------------

func (client *crossExPerpClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := crossExContext(ctx, client.meta, "RefreshInstruments"); err != nil {
		return nil, err
	}
	if client.natives == nil {
		return nil, client.meta.catalogMetadataResolution("RefreshInstruments", "native instrument source is not configured")
	}
	rows, err := client.sdk.Symbols(ctx)
	if err != nil {
		return nil, crossExNormalizeErr(client.meta, "RefreshInstruments", err)
	}
	natives, err := client.natives(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "RefreshInstruments")
	}
	mapping, err := MapCrossExInstruments(rows, natives)
	if err != nil {
		return nil, crossExMalformed(client.meta, "RefreshInstruments", "crossex rules are inconsistent")
	}
	bySymbol := make(map[string]exchange.Instrument, len(mapping.Instruments))
	for _, instrument := range mapping.Instruments {
		bySymbol[instrument.Identity.NativeSymbol] = instrument
	}
	client.mu.Lock()
	client.instruments = bySymbol
	client.mu.Unlock()
	return append([]exchange.Instrument(nil), mapping.Instruments...), nil
}

func (client *crossExPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	client.mu.RLock()
	loaded := len(client.instruments) > 0
	out := make([]exchange.Instrument, 0, len(client.instruments))
	for _, instrument := range client.instruments {
		out = append(out, instrument)
	}
	client.mu.RUnlock()
	if !loaded {
		return client.RefreshInstruments(ctx)
	}
	return out, nil
}

// ResolveInstrument accepts a CrossEx symbol or "venue:SYMBOL".
func (client *crossExPerpClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return exchange.Instrument{}, client.meta.catalogInvalidRequest("ResolveInstrument", "instrument symbol is required")
	}
	instruments, err := client.Instruments(ctx)
	if err != nil {
		return exchange.Instrument{}, withExchangeOperation(err, "ResolveInstrument")
	}
	for _, instrument := range instruments {
		identity := instrument.Identity
		if identity.NativeSymbol == symbol || string(identity.Venue)+":"+identity.Symbol == symbol {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, client.meta.catalogMetadataResolution("ResolveInstrument", "instrument metadata not found")
}

// instrument returns the CrossEx-bound instrument for a request instrument.
// Callers may pass either the CrossEx instrument or the native one.
func (client *crossExPerpClient) instrument(ctx context.Context, operation string, requested exchange.Instrument) (exchange.Instrument, error) {
	if requested.Identity.Product != exchange.ProductPerp {
		return exchange.Instrument{}, client.meta.catalogInvalidRequest(operation, "crossex trades perpetual instruments only")
	}
	instruments, err := client.Instruments(ctx)
	if err != nil {
		return exchange.Instrument{}, withExchangeOperation(err, operation)
	}
	for _, instrument := range instruments {
		identity := instrument.Identity
		if identity.Venue == requested.Identity.Venue && identity.Symbol == requested.Identity.Symbol {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, client.meta.catalogInvalidRequest(operation, "instrument is not tradable through crossex")
}

func (client *crossExPerpClient) bySymbol(symbol string) (exchange.Instrument, bool) {
	client.mu.RLock()
	defer client.mu.RUnlock()
	instrument, ok := client.instruments[symbol]
	return instrument, ok
}

// ---------------------------------------------------------------------------
// Market data: served by the instrument's own venue
// ---------------------------------------------------------------------------

func (client *crossExPerpClient) OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error) {
	return exchange.OrderBook{}, crossExUnsupported(client.meta, "OrderBook")
}

func (client *crossExPerpClient) Candles(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error) {
	return exchange.CandlePage{}, crossExUnsupported(client.meta, "Candles")
}

func (client *crossExPerpClient) PublicTrades(context.Context, exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	return exchange.PublicTradePage{}, crossExUnsupported(client.meta, "PublicTrades")
}

func (client *crossExPerpClient) FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, error) {
	return exchange.FundingRate{}, crossExUnsupported(client.meta, "FundingRate")
}

func (client *crossExPerpClient) FundingRateHistory(context.Context, exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	return exchange.FundingRatePage{}, crossExUnsupported(client.meta, "FundingRateHistory")
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

// account reads the margin account and enforces the supported modes.
func (client *crossExPerpClient) account(ctx context.Context, operation string) (crossex.Account, error) {
	if err := crossExContext(ctx, client.meta, operation); err != nil {
		return crossex.Account{}, err
	}
	account, err := client.sdk.Account(ctx)
	if err != nil {
		return crossex.Account{}, crossExNormalizeErr(client.meta, operation, err)
	}
	if !strings.EqualFold(account.AccountMode, crossex.AccountModeCross) {
		return crossex.Account{}, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: operation,
			SafeMessage: "crossex account must use cross-exchange mode"})
	}
	if !strings.EqualFold(account.PositionMode, crossex.PositionModeSingle) {
		return crossex.Account{}, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: operation,
			SafeMessage: "crossex account must use single (one-way) position mode"})
	}
	return account, nil
}

// Balances reports the shared margin pool per coin. CrossEx publishes one
// account-level row per coin (exchange_type CROSSEX) plus per-venue rows; the
// pool rows are the balances, the venue rows are a breakdown.
func (client *crossExPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.account(ctx, "Balances")
	if err != nil {
		return nil, err
	}
	return crossExBalances(account.Assets), nil
}

func (client *crossExPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	account, err := client.account(ctx, "PerpAccount")
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	return crossExPerpAccount(account), nil
}

func crossExPerpAccount(account crossex.Account) exchange.PerpAccount {
	upnl := decimal.Zero
	for _, asset := range crossExPoolAssets(account.Assets) {
		upnl = upnl.Add(crossExDecimal(asset.Upnl))
	}
	optional := func(value string) exchange.OptionalDecimal {
		if strings.TrimSpace(value) == "" {
			return exchange.OptionalDecimal{}
		}
		return exchange.OptionalDecimal{Value: crossExDecimal(value), Valid: true}
	}
	return exchange.PerpAccount{
		Balances:      crossExBalances(account.Assets),
		Equity:        optional(account.MarginBalance),
		Available:     optional(account.AvailableMargin),
		MarginUsed:    optional(account.InitialMargin),
		UnrealizedPnL: exchange.OptionalDecimal{Value: upnl, Valid: true},
	}
}

// crossExBalances reports one balance per coin. In cross-exchange mode the
// account-level rows (exchange_type CROSSEX) are the pool; when only per-venue
// rows are present they are summed. Locked is total minus available.
func crossExBalances(assets []crossex.AccountAsset) []exchange.Balance {
	assets = crossExPoolAssets(assets)
	type sum struct{ total, available decimal.Decimal }
	byCoin := map[string]*sum{}
	var order []string
	for _, asset := range assets {
		coin := strings.ToUpper(strings.TrimSpace(asset.Coin))
		if coin == "" {
			continue
		}
		current, ok := byCoin[coin]
		if !ok {
			current = &sum{}
			byCoin[coin] = current
			order = append(order, coin)
		}
		current.total = current.total.Add(crossExDecimal(asset.Balance))
		current.available = current.available.Add(crossExDecimal(asset.AvailableBalance))
	}
	out := make([]exchange.Balance, 0, len(order))
	for _, coin := range order {
		value := byCoin[coin]
		locked := value.total.Sub(value.available)
		if locked.IsNegative() {
			locked = decimal.Zero
		}
		out = append(out, exchange.Balance{Asset: coin, Total: value.total, Available: value.available, Locked: locked})
	}
	return out
}

// crossExPoolAssets selects the rows that describe the shared pool without
// double counting: account-level rows when CrossEx reports them, otherwise
// the per-venue rows.
func crossExPoolAssets(assets []crossex.AccountAsset) []crossex.AccountAsset {
	pool := assets[:0:0]
	for _, asset := range assets {
		if crossExIsPoolRow(asset) {
			pool = append(pool, asset)
		}
	}
	if len(pool) > 0 {
		return pool
	}
	return assets
}

func crossExIsPoolRow(asset crossex.AccountAsset) bool {
	return strings.EqualFold(strings.TrimSpace(asset.ExchangeType), "CROSSEX")
}

func (client *crossExPerpClient) Positions(ctx context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := crossExContext(ctx, client.meta, "Positions"); err != nil {
		return nil, err
	}
	query := crossex.PositionsQuery{}
	if request.Instrument != nil {
		instrument, err := client.instrument(ctx, "Positions", *request.Instrument)
		if err != nil {
			return nil, err
		}
		query.Symbol = instrument.Identity.NativeSymbol
	}
	if _, err := client.Instruments(ctx); err != nil {
		return nil, withExchangeOperation(err, "Positions")
	}
	rows, err := client.sdk.Positions(ctx, query)
	if err != nil {
		return nil, crossExNormalizeErr(client.meta, "Positions", err)
	}
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		position := crossExPosition(row)
		if position.Quantity.IsZero() {
			continue
		}
		out = append(out, position)
	}
	return out, nil
}

func (client *crossExPerpClient) SetLeverage(ctx context.Context, request exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := crossExContext(ctx, client.meta, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	if request.Leverage <= 0 {
		return exchange.Leverage{}, client.meta.catalogInvalidRequest("SetLeverage", "instrument and positive leverage are required")
	}
	instrument, err := client.instrument(ctx, "SetLeverage", request.Instrument)
	if err != nil {
		return exchange.Leverage{}, err
	}
	result, err := client.sdk.SetLeverage(ctx, instrument.Identity.NativeSymbol, strconv.Itoa(request.Leverage))
	if err != nil {
		return exchange.Leverage{}, crossExNormalizeErr(client.meta, "SetLeverage", err)
	}
	if result.Symbol != "" && result.Symbol != instrument.Identity.NativeSymbol {
		return exchange.Leverage{}, crossExMalformed(client.meta, "SetLeverage", "crossex leverage response is for another symbol")
	}
	effective, err := strconv.Atoi(strings.TrimSpace(result.Leverage))
	if err != nil || effective <= 0 {
		return exchange.Leverage{}, crossExMalformed(client.meta, "SetLeverage", "crossex leverage response is invalid")
	}
	return exchange.Leverage{NativeSymbol: instrument.Identity.NativeSymbol, Effective: effective}, nil
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

func (client *crossExPerpClient) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := crossExContext(ctx, client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	instrument, err := client.instrument(ctx, "PlaceOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	// Validate against CrossEx rules, which may differ from the native venue's.
	request.Instrument = instrument
	if err := request.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, client.meta.catalogInvalidRequest("PlaceOrder", crossExSafeValidation(err))
	}
	action, err := client.sdk.PlaceOrder(ctx, crossExOrderRequest(request))
	ack := exchange.OrderAcknowledgement{
		Venue: instrument.Identity.Venue, Product: exchange.ProductPerp, Operation: exchange.OrderOperationPlace,
		NativeSymbol: instrument.Identity.NativeSymbol, OrderType: request.Type, ClientOrderID: request.ClientOrderID,
	}
	if err != nil {
		return crossExCommandErr(client.meta, ack, err)
	}
	if action.OrderID == "" {
		ack.State = exchange.AckAmbiguous
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
			Operation: "PlaceOrder", SafeMessage: "crossex accepted the order without an order id"})
	}
	// CrossEx acknowledges asynchronously: the order may still FAIL its own
	// checks or be REJECTed by the exchange. The private stream reports which.
	ack.State, ack.OrderID = exchange.AckAcceptedPending, action.OrderID
	return ack, nil
}

func (client *crossExPerpClient) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := crossExContext(ctx, client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	instrument, err := client.instrument(ctx, "CancelOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(request.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, client.meta.catalogInvalidRequest("CancelOrder", "order id is required")
	}
	ack := exchange.OrderAcknowledgement{
		Venue: instrument.Identity.Venue, Product: exchange.ProductPerp, Operation: exchange.OrderOperationCancel,
		NativeSymbol: instrument.Identity.NativeSymbol, OrderID: request.OrderID,
	}
	action, err := client.sdk.CancelOrder(ctx, request.OrderID)
	if err != nil {
		return crossExCommandErr(client.meta, ack, err)
	}
	if action.OrderID != "" && action.OrderID != request.OrderID {
		return ack, crossExMalformed(client.meta, "CancelOrder", "crossex cancel response is for another order")
	}
	// The cancel request is accepted; the final state arrives on the stream.
	ack.State = exchange.AckAcceptedPending
	return ack, nil
}

func (client *crossExPerpClient) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := crossExContext(ctx, client.meta, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	query := crossex.OpenOrdersQuery{BusinessType: crossex.BusinessFuture}
	if request.Instrument != nil {
		instrument, err := client.instrument(ctx, "OpenOrders", *request.Instrument)
		if err != nil {
			return exchange.OrderPage{}, err
		}
		query.Symbol = instrument.Identity.NativeSymbol
	}
	rows, err := client.sdk.OpenOrders(ctx, query)
	if err != nil {
		return exchange.OrderPage{}, crossExNormalizeErr(client.meta, "OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		orders = append(orders, crossExOrder(row))
	}
	return boundedOrderPage(orders, request.Limit, ""), nil
}

func (client *crossExPerpClient) OrderHistory(ctx context.Context, request exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	query, err := client.historyQuery(ctx, "OrderHistory", request.Instrument, request.Start, request.End, request.Cursor, request.Limit)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := client.sdk.OrderHistory(ctx, query)
	if err != nil {
		return exchange.OrderPage{}, crossExNormalizeErr(client.meta, "OrderHistory", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if row.BusinessType != "" && row.BusinessType != crossex.BusinessFuture {
			continue
		}
		orders = append(orders, crossExOrder(row))
	}
	return exchange.OrderPage{Orders: orders, Page: crossExNextPage(query, len(rows), request.Start, request.End)}, nil
}

func (client *crossExPerpClient) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	query, err := client.historyQuery(ctx, "Fills", request.Instrument, request.Start, request.End, request.Cursor, request.Limit)
	if err != nil {
		return exchange.FillPage{}, err
	}
	rows, err := client.sdk.Trades(ctx, query)
	if err != nil {
		return exchange.FillPage{}, crossExNormalizeErr(client.meta, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if row.BusinessType != "" && row.BusinessType != crossex.BusinessFuture {
			continue
		}
		if request.OrderID != "" && row.OrderID != request.OrderID {
			continue
		}
		fills = append(fills, crossExFill(row))
	}
	return exchange.FillPage{Fills: fills, Page: crossExNextPage(query, len(rows), request.Start, request.End)}, nil
}

func (client *crossExPerpClient) historyQuery(ctx context.Context, operation string, requested *exchange.Instrument, start, end time.Time, cursor string, limit int) (crossex.HistoryQuery, error) {
	if err := crossExContext(ctx, client.meta, operation); err != nil {
		return crossex.HistoryQuery{}, err
	}
	if err := validateBoundedHistory(cursor, limit, start, end); err != nil {
		return crossex.HistoryQuery{}, client.meta.catalogInvalidRequest(operation, err.Error())
	}
	query := crossex.HistoryQuery{From: optionalMillis(start), To: optionalMillis(end), Page: 1, Limit: limit}
	if query.Limit <= 0 || query.Limit > 1000 {
		query.Limit = 100
	}
	if cursor != "" {
		page, err := strconv.Atoi(cursor)
		if err != nil || page < 1 {
			return crossex.HistoryQuery{}, client.meta.catalogInvalidRequest(operation, "cursor is invalid")
		}
		query.Page = page
	}
	if requested != nil {
		instrument, err := client.instrument(ctx, operation, *requested)
		if err != nil {
			return crossex.HistoryQuery{}, err
		}
		query.Symbol = instrument.Identity.NativeSymbol
	}
	return query, nil
}

// crossExNextPage only claims more rows when a full page came back; a short
// page is the documented end of the window.
func crossExNextPage(query crossex.HistoryQuery, rows int, start, end time.Time) exchange.PageInfo {
	page := exchange.PageInfo{Limit: query.Limit, WindowStart: start, WindowEnd: end, HasMoreKnown: rows < query.Limit, HasMore: false}
	if rows >= query.Limit {
		page.Cursor = strconv.Itoa(query.Page + 1)
	}
	return page
}

// LookupExactOrder reads one order by its client order id (CrossEx "text")
// or order id. CrossEx documents that a finished order may no longer be found
// by text, so "not found" by text is only authoritative after the account's
// recent history also lacks it; otherwise the answer is unconfirmed.
func (client *crossExPerpClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	venue, product := exchange.VenueCrossEx, exchange.ProductPerp
	if ctx == nil {
		return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "context is required")
	}
	if err := request.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "invalid exact order lookup request")
	}
	if request.Instrument == nil {
		return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "instrument is required for exact order lookup")
	}
	instrument, err := client.instrument(ctx, exactLookupOperation, *request.Instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	scopeVenue := instrument.Identity.Venue
	id := request.VenueOrderID
	if request.ClientOrderID != "" {
		id = request.ClientOrderID
	}
	row, err := client.sdk.Order(ctx, id)
	switch {
	case err == nil:
		if row.Symbol != instrument.Identity.NativeSymbol {
			return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindMalformedResponse, "crossex order belongs to another instrument")
		}
		order := crossExOrder(row)
		if request.ClientOrderID != "" {
			order.ClientOrderID = request.ClientOrderID
		}
		return exactLookupFound(request, scopeVenue, product, instrument, order)
	case crossex.IsOrderNotFound(err) && request.VenueOrderID != "":
		return exactLookupNotFound(request, scopeVenue, product, instrument)
	case crossex.IsOrderNotFound(err):
		// By text: confirm against recent history before denying existence.
		history, historyErr := client.sdk.OrderHistory(ctx, crossex.HistoryQuery{Symbol: instrument.Identity.NativeSymbol, Page: 1, Limit: 100})
		if historyErr != nil {
			return exactLookupUnconfirmed(request, scopeVenue, product, instrument, "")
		}
		for _, candidate := range history {
			if strings.TrimSpace(candidate.Text) == request.ClientOrderID {
				order := crossExOrder(candidate)
				order.ClientOrderID = request.ClientOrderID
				return exactLookupFound(request, scopeVenue, product, instrument, order)
			}
		}
		return exactLookupUnconfirmed(request, scopeVenue, product, instrument, "")
	default:
		return exchange.ExactOrderLookupResult{}, crossExNormalizeErr(client.meta, exactLookupOperation, err)
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func crossExContext(ctx context.Context, meta clientMeta, operation string) error {
	if ctx == nil {
		return meta.catalogInvalidRequest(operation, "context is required")
	}
	return meta.catalogContextErr(ctx, operation)
}

func crossExUnsupported(meta clientMeta, operation string) error {
	return exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation,
		SafeMessage: "crossex market data is read from the instrument's own venue"})
}

func crossExMalformed(meta clientMeta, operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: message})
}

func crossExSafeValidation(err error) string {
	var normalized *exchange.Error
	if errors.As(err, &normalized) && normalized.Details().SafeMessage != "" {
		return normalized.Details().SafeMessage
	}
	return "invalid order request"
}

func crossExNormalizeErr(meta clientMeta, operation string, err error) (returnErr error) {
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
	if errors.Is(err, crossex.ErrCredentialsRequired) {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "crossex credentials are required"})
	}
	var apiErr *crossex.APIError
	if errors.As(err, &apiErr) {
		kind := exchange.KindVenueRejected
		switch {
		case crossex.IsCredentialRejection(err):
			kind = exchange.KindAuthentication
			if apiErr.StatusCode == http.StatusForbidden {
				kind = exchange.KindPermission
			}
		case crossex.IsOrderNotFound(err), apiErr.StatusCode == http.StatusNotFound:
			kind = exchange.KindNotFound
		case apiErr.StatusCode == http.StatusTooManyRequests:
			kind = exchange.KindRateLimit
		}
		return exchange.NewError(kind, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, Code: apiErr.Label, SafeMessage: "crossex request failed"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "crossex transport failed"})
}

// crossExCommandErr turns a failed command into a rejection only when CrossEx
// proved it refused the command; anything else is ambiguous.
func crossExCommandErr(meta clientMeta, ack exchange.OrderAcknowledgement, err error) (returnAck exchange.OrderAcknowledgement, returnErr error) {
	operation := "PlaceOrder"
	if ack.Operation == exchange.OrderOperationCancel {
		operation = "CancelOrder"
	}
	defer func() { normalizeHTTPCommand(&returnAck, &returnErr, err, meta.venue, meta.product, operation) }()
	if crossex.IsDefinitiveCommandRejection(err) {
		ack.State = exchange.AckRejected
		var apiErr *crossex.APIError
		if errors.As(err, &apiErr) {
			ack.VenueCode = apiErr.Label
			ack.VenueMessage = cleanVenueMessage(apiErr.Message)
		}
		if ack.VenueCode == "" && ack.VenueMessage == "" {
			ack.VenueMessage = "crossex rejected command"
		}
		return ack, crossExNormalizeErr(meta, operation, err)
	}
	ack.State = exchange.AckAmbiguous
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "crossex command outcome is ambiguous"})
}

var (
	_ exchange.PerpClient       = (*crossExPerpClient)(nil)
	_ exchange.ExactOrderLookup = (*crossExPerpClient)(nil)
	_ exchange.BrokeredClient   = (*crossExPerpClient)(nil)
)
