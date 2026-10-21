package factoryclient

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/sdk/okx"
	"github.com/shopspring/decimal"
)

// okxOptionClient serves every OKX option, coin- and USD-margined, of one
// trading account. Orders, books, trades and the private stream are the
// shared OKX paths in contract units; this file converts to the uniform
// units (quantity in the underlying) and adds the chain, quotes and the
// option account.
type okxOptionClient struct {
	meta     clientMeta
	sdk      *okx.Client
	ws       *optionWebSocket
	requests exchange.MarketRequestCoordinator

	modeMu    sync.Mutex
	tradeMode string
}

func NewOKXOption(apiKey, secretKey, passphrase string, settings Settings) exchange.OptionClient {
	sdkClient := okx.NewClient().WithCredentials(apiKey, secretKey, passphrase)
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.WithHTTPClient(settings.HTTPClient)
	}
	client := &okxOptionClient{
		meta:     clientMetaFromSettings(exchange.VenueOKX, exchange.ProductOption, settings),
		sdk:      sdkClient,
		requests: settings.MarketRequests,
	}
	wsClient := okx.NewWSClient(context.Background())
	businessWSClient := okx.NewWSClient(context.Background()).WithBusinessURL()
	privateWSClient := okx.NewWSClient(context.Background()).WithCredentials(apiKey, secretKey, passphrase)
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
		businessWSClient.WithURL(settings.WebSocketEndpoint)
		privateWSClient.WithURL(settings.WebSocketEndpoint)
	}
	contracts := func(ctx context.Context, instrument exchange.Instrument) (okxContractMeta, error) {
		return client.contractMeta(ctx, "WatchOption", instrument)
	}
	public := newOKXPerpWSBackendWithClients(client.meta, wsClient, businessWSClient, contracts).(*okxPublicWSBackend)
	private := newOKXPerpPrivateWSBackendWithClient(client.meta, privateWSClient, okxCatalogInstrumentCodeLoader(client.meta, exchange.ProductOption), contracts).(*okxPrivateWSBackend)
	private.optionTradeMode = client.optionTradeMode
	client.ws = newOptionWebSocket(client.meta, public, private, public)
	return client
}

func (client *okxOptionClient) WebSocket() exchange.OptionWebSocket { return client.ws }
func (client *okxOptionClient) Close() error                        { return client.ws.Close() }
func (client *okxOptionClient) String() string                      { return client.meta.redactedString() }
func (client *okxOptionClient) GoString() string                    { return client.String() }

func (client *okxOptionClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *okxOptionClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return client.meta.RefreshInstruments(ctx)
}

func (client *okxOptionClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := okxReady(ctx, exchange.ProductOption, "Instruments", client.sdk); err != nil {
		return nil, err
	}
	snapshot, err := client.snapshot(ctx, "Instruments")
	if err != nil {
		return nil, err
	}
	out := append([]exchange.Instrument(nil), snapshot.Instruments...)
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}

func (client *okxOptionClient) snapshot(ctx context.Context, operation string) (exchange.InstrumentSnapshot, error) {
	if client.meta.catalog == nil || client.meta.catalogScope == (exchange.CatalogScope{}) {
		return exchange.InstrumentSnapshot{}, client.meta.catalogMetadataResolution(operation, "instrument catalog is not configured")
	}
	snapshot, err := client.meta.catalog.Snapshot(ctx, client.meta.catalogScope)
	return snapshot, withExchangeOperation(err, operation)
}

// contractMeta is an option's contract size from the catalog: the venue's
// listing, never the caller's copy.
func (client *okxOptionClient) contractMeta(ctx context.Context, operation string, instrument exchange.Instrument) (okxContractMeta, error) {
	if err := okxValidateOptionInstrument(instrument); err != nil {
		return okxContractMeta{}, okxInvalid(exchange.ProductOption, operation, err.Error())
	}
	return client.contractMetaByNative(ctx, operation, instrument.Identity.NativeSymbol)
}

func (client *okxOptionClient) contractMetaByNative(ctx context.Context, operation, native string) (okxContractMeta, error) {
	snapshot, err := client.snapshot(ctx, operation)
	if err != nil {
		return okxContractMeta{}, err
	}
	for _, candidate := range snapshot.Instruments {
		if candidate.Identity.NativeSymbol == native {
			meta, err := okxContractMetaFromInstrument(candidate)
			if err != nil {
				return okxContractMeta{}, okxMalformed(exchange.ProductOption, operation, err.Error())
			}
			return meta, nil
		}
	}
	return okxContractMeta{}, okxInvalid(exchange.ProductOption, operation, "instrument is not present in OKX OPTION metadata")
}

// ---------------------------------------------------------------------------
// Market
// ---------------------------------------------------------------------------

func (client *okxOptionClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := okxReady(ctx, exchange.ProductOption, "OrderBook", client.sdk); err != nil {
		return exchange.OrderBook{}, err
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, okxInvalid(exchange.ProductOption, "OrderBook", "limit must be non-negative")
	}
	meta, err := client.contractMeta(ctx, "OrderBook", req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	rows, err := client.sdk.GetOrderBook(ctx, req.Instrument.Identity.NativeSymbol, okxLimitPtr(req.Limit))
	if err != nil {
		return exchange.OrderBook{}, okxNormalizeErr(exchange.ProductOption, "OrderBook", err)
	}
	return okxOrderBook(exchange.ProductOption, "OrderBook", req.Instrument, req.Limit, rows, meta.contractValue)
}

func (client *okxOptionClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := okxReady(ctx, exchange.ProductOption, "Candles", client.sdk); err != nil {
		return exchange.CandlePage{}, err
	}
	if err := okxValidateOptionInstrument(req.Instrument); err != nil {
		return exchange.CandlePage{}, okxInvalid(exchange.ProductOption, "Candles", err.Error())
	}
	return okxCandles(ctx, client.sdk, exchange.ProductOption, req)
}

func (client *okxOptionClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := okxReady(ctx, exchange.ProductOption, "PublicTrades", client.sdk); err != nil {
		return exchange.PublicTradePage{}, err
	}
	meta, err := client.contractMeta(ctx, "PublicTrades", req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	return okxPublicTrades(ctx, client.sdk, exchange.ProductOption, req, meta.contractValue)
}

// ---------------------------------------------------------------------------
// Chain
// ---------------------------------------------------------------------------

// OptionChain reads the underlying's families whole: four public calls per
// family (summary for IVs, forward and greeks; tickers for the book top;
// marks; open interest) and one for every USD index.
func (client *okxOptionClient) OptionChain(ctx context.Context, request exchange.OptionChainRequest) ([]exchange.OptionQuote, error) {
	const operation = "OptionChain"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return nil, err
	}
	snapshot, err := client.snapshot(ctx, operation)
	if err != nil {
		return nil, err
	}
	underlying := strings.ToUpper(strings.TrimSpace(request.Underlying))
	byNative := make(map[string]exchange.Instrument, len(snapshot.Instruments))
	var families []string
	seen := map[string]bool{}
	for _, instrument := range snapshot.Instruments {
		if underlying != "" && instrument.Option.Underlying != underlying {
			continue
		}
		byNative[instrument.Identity.NativeSymbol] = instrument
		if family := okxOptionFamily(instrument.Identity.NativeSymbol); !seen[family] {
			seen[family] = true
			families = append(families, family)
		}
	}
	sort.Strings(families)
	settings := Settings{MarketRequests: client.requests}
	indexes, err := coordinatedMarketRequest(ctx, settings, exchange.VenueOKX, "index_tickers", "USD", func(callCtx context.Context) ([]okx.IndexTicker, error) {
		return client.sdk.GetIndexTickers(callCtx, "USD")
	})
	if err != nil {
		return nil, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	index := make(map[string]exchange.OptionalDecimal, len(indexes))
	for _, row := range indexes {
		index[row.InstId] = okxPositiveOptional(row.IdxPx)
	}
	var quotes []exchange.OptionQuote
	for _, family := range families {
		rows, err := client.familyRows(ctx, settings, family)
		if err != nil {
			return nil, okxNormalizeErr(exchange.ProductOption, operation, err)
		}
		for native, row := range rows {
			instrument, ok := byNative[native]
			if !ok {
				continue
			}
			quote := row.quote(instrument)
			quote.IndexPrice = index[instrument.Option.Index]
			quotes = append(quotes, quote)
		}
	}
	sort.Slice(quotes, func(i, j int) bool {
		return quotes[i].Instrument.Identity.Symbol < quotes[j].Instrument.Identity.Symbol
	})
	return quotes, nil
}

// okxOptionRow joins one option's rows from the family reads.
type okxOptionRow struct {
	summary *okx.OptionSummary
	ticker  *okx.Ticker
	mark    string
	oi      string
}

func (client *okxOptionClient) familyRows(ctx context.Context, settings Settings, family string) (map[string]*okxOptionRow, error) {
	rows := map[string]*okxOptionRow{}
	row := func(native string) *okxOptionRow {
		if rows[native] == nil {
			rows[native] = &okxOptionRow{}
		}
		return rows[native]
	}
	summaries, err := coordinatedMarketRequest(ctx, settings, exchange.VenueOKX, "opt_summary", family, func(callCtx context.Context) ([]okx.OptionSummary, error) {
		return client.sdk.GetOptionSummary(callCtx, family, "")
	})
	if err != nil {
		return nil, err
	}
	for index := range summaries {
		row(summaries[index].InstId).summary = &summaries[index]
	}
	tickers, err := coordinatedMarketRequest(ctx, settings, exchange.VenueOKX, "option_tickers", family, func(callCtx context.Context) ([]okx.Ticker, error) {
		return client.sdk.GetTickers(callCtx, okxOptionType, &family)
	})
	if err != nil {
		return nil, err
	}
	for index := range tickers {
		row(tickers[index].InstId).ticker = &tickers[index]
	}
	marks, err := coordinatedMarketRequest(ctx, settings, exchange.VenueOKX, "option_marks", family, func(callCtx context.Context) ([]okx.MarkPrice, error) {
		return client.sdk.GetMarkPricesByFamily(callCtx, okxOptionType, family)
	})
	if err != nil {
		return nil, err
	}
	for _, mark := range marks {
		row(mark.InstId).mark = mark.MarkPx
	}
	interests, err := coordinatedMarketRequest(ctx, settings, exchange.VenueOKX, "option_open_interest", family, func(callCtx context.Context) ([]okx.OpenInterest, error) {
		return client.sdk.GetOpenInterestsByFamily(callCtx, okxOptionType, family)
	})
	if err != nil {
		return nil, err
	}
	for _, interest := range interests {
		row(interest.InstId).oi = interest.OICcy
	}
	return rows, nil
}

func (row *okxOptionRow) quote(instrument exchange.Instrument) exchange.OptionQuote {
	quote := exchange.OptionQuote{Instrument: instrument, Mark: okxPositiveOptional(row.mark), OpenInterest: okxOptional0(row.oi)}
	if ticker := row.ticker; ticker != nil {
		okxApplyTicker(&quote, instrument, ticker)
	}
	if summary := row.summary; summary != nil {
		okxApplySummary(&quote, summary)
	}
	return quote
}

// okxApplyTicker sets the book top, in the underlying's units.
func okxApplyTicker(quote *exchange.OptionQuote, instrument exchange.Instrument, ticker *okx.Ticker) {
	contract := instrument.Encoding.ContractMultiplier.Value
	quote.Bid, quote.Ask, quote.Last = okxPositiveOptional(ticker.BidPx), okxPositiveOptional(ticker.AskPx), okxPositiveOptional(ticker.Last)
	quote.BidSize, quote.AskSize = okxScaled(ticker.BidSz, contract), okxScaled(ticker.AskSz, contract)
	quote.Volume24h = okxOptional0(ticker.VolCcy24h)
	if at, err := okxOptionalMillis(ticker.Ts); err == nil && !at.IsZero() {
		quote.Time = at
	}
}

// okxApplySummary sets the IVs, the forward and the venue's Black-Scholes
// greeks (per unit of underlying, in USD — the uniform convention; OKX's
// "PA" greeks are in coin and are not used).
func okxApplySummary(quote *exchange.OptionQuote, summary *okx.OptionSummary) {
	quote.MarkIV, quote.BidIV, quote.AskIV = okxPositiveOptional(summary.MarkVol), okxPositiveOptional(summary.BidVol), okxPositiveOptional(summary.AskVol)
	quote.UnderlyingPrice = okxPositiveOptional(summary.FwdPx)
	if summary.DeltaBS != "" {
		quote.Greeks = &exchange.OptionGreeks{
			Delta: okxDecimal0(summary.DeltaBS), Gamma: okxDecimal0(summary.GammaBS),
			Vega: okxDecimal0(summary.VegaBS), Theta: okxDecimal0(summary.ThetaBS),
		}
	}
	if quote.Time.IsZero() {
		if at, err := okxOptionalMillis(summary.Ts); err == nil {
			quote.Time = at
		}
	}
}

func okxPositiveOptional(raw string) exchange.OptionalDecimal {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !value.IsPositive() {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: value, Valid: true}
}

func okxOptional0(raw string) exchange.OptionalDecimal {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || value.IsNegative() {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: value, Valid: true}
}

func okxScaled(raw string, contract decimal.Decimal) exchange.OptionalDecimal {
	value := okxPositiveOptional(raw)
	if value.Valid {
		value.Value = value.Value.Mul(contract)
	}
	return value
}

func okxDecimal0(raw string) decimal.Decimal {
	value, _ := decimal.NewFromString(strings.TrimSpace(raw))
	return value
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

// okxOptionOrderShape checks what OKX accepts for an option order and
// returns its trade mode: options take limit orders only (no market type),
// reduce-only applies to futures and swaps only, and the trade mode follows
// the account mode (Spot mode: cash, buyers only; the margin modes: cross).
func okxOptionOrderShape(ctx context.Context, operation string, req exchange.PlaceOrderRequest, mode okxSpotTradeModeLoader) (string, error) {
	if req.Type != exchange.OrderTypeLimit {
		return "", okxInvalid(exchange.ProductOption, operation, "OKX options take limit orders only")
	}
	if req.ReduceOnly {
		return "", okxInvalid(exchange.ProductOption, operation, "OKX options do not support reduce-only")
	}
	if mode == nil {
		return "", okxInvalid(exchange.ProductOption, operation, "OKX option trade mode loader is not configured")
	}
	return mode(ctx, operation)
}

// optionTradeMode reads the account mode once.
func (client *okxOptionClient) optionTradeMode(ctx context.Context, operation string) (string, error) {
	client.modeMu.Lock()
	defer client.modeMu.Unlock()
	if client.tradeMode != "" {
		return client.tradeMode, nil
	}
	rows, err := client.sdk.GetAccountConfig(ctx)
	if err != nil {
		return "", okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	mode, err := okxOptionTradeModeFromAccountConfig(rows)
	if err != nil {
		return "", okxMalformed(exchange.ProductOption, operation, err.Error())
	}
	client.tradeMode = mode
	return mode, nil
}

func okxOptionTradeModeFromAccountConfig(rows []okx.AccountConfig) (string, error) {
	if len(rows) != 1 {
		return "", errors.New("account config response must contain exactly one row")
	}
	switch rows[0].AcctLv {
	case "1":
		return okxCashMode, nil
	case "2", "3", "4":
		return okxCrossMode, nil
	}
	return "", errors.New("unsupported OKX account level")
}

func (client *okxOptionClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	const operation = "PlaceOrder"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductOption); err != nil {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductOption, operation, err.Error())
	}
	meta, err := client.contractMeta(ctx, operation, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	contracts := req.Quantity.Div(meta.contractValue)
	if !contracts.Mod(meta.contractIncrement).IsZero() {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductOption, operation, "quantity must align to OKX contract lot size")
	}
	mode, err := okxOptionOrderShape(ctx, operation, req, client.optionTradeMode)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ordType, px := okxOrderRequestShape(req)
	oid, err := client.sdk.PlaceOrder(ctx, &okx.OrderRequest{
		InstId: req.Instrument.Identity.NativeSymbol, TdMode: mode, ClOrdId: okxStringPtr(req.ClientOrderID),
		Side: string(req.Side), OrdType: ordType, Sz: contracts.String(), Px: px,
	})
	if err != nil {
		return okxCommandTransportAck(exchange.ProductOption, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err)
	}
	ack, err := okxCommandAck(exchange.ProductOption, operation, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, oid)
	ack.OrderType = req.Type
	if err != nil {
		return ack, err
	}
	return ack, ack.Validate()
}

func (client *okxOptionClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	const operation = "CancelOrder"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := okxValidateCancel(exchange.ProductOption, req); err != nil {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductOption, operation, err.Error())
	}
	native := req.Instrument.Identity.NativeSymbol
	oid, err := client.sdk.CancelOrder(ctx, native, req.OrderID, "")
	if err != nil {
		return okxCommandTransportAck(exchange.ProductOption, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", err)
	}
	return okxCommandAck(exchange.ProductOption, operation, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", oid)
}

// optionalNative validates an optional instrument filter.
func (client *okxOptionClient) optionalNative(operation string, instrument *exchange.Instrument) (*string, error) {
	if instrument == nil {
		return nil, nil
	}
	if err := okxValidateOptionInstrument(*instrument); err != nil {
		return nil, okxInvalid(exchange.ProductOption, operation, err.Error())
	}
	native := instrument.Identity.NativeSymbol
	return &native, nil
}

// multipliers caches contract sizes for one response.
func (client *okxOptionClient) multipliers(ctx context.Context, operation string) func(string) (decimal.Decimal, error) {
	cache := map[string]decimal.Decimal{}
	return func(native string) (decimal.Decimal, error) {
		if value, ok := cache[native]; ok {
			return value, nil
		}
		meta, err := client.contractMetaByNative(ctx, operation, native)
		if err != nil {
			return decimal.Zero, err
		}
		cache[native] = meta.contractValue
		return meta.contractValue, nil
	}
}

func (client *okxOptionClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	const operation = "OpenOrders"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := okxValidateOpenOrdersPage(exchange.ProductOption, req.Cursor, req.Limit); err != nil {
		return exchange.OrderPage{}, err
	}
	inst, err := client.optionalNative(operation, req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	instType := okxOptionType
	rows, err := client.sdk.GetOrders(ctx, &instType, inst)
	if err != nil {
		return exchange.OrderPage{}, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	multiplier := client.multipliers(ctx, operation)
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxOptionType || !okxOptionalInstrumentMatches(req.Instrument, row.InstId) {
			return exchange.OrderPage{}, okxMalformed(exchange.ProductOption, operation, "mixed product or instrument row")
		}
		factor, err := multiplier(row.InstId)
		if err != nil {
			return exchange.OrderPage{}, err
		}
		order, err := okxOrder(row, factor)
		if err != nil {
			return exchange.OrderPage{}, okxMalformed(exchange.ProductOption, operation, err.Error())
		}
		orders = append(orders, order)
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *okxOptionClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	const operation = "OrderHistory"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return exchange.OrderPage{}, err
	}
	inst, err := client.optionalNative(operation, req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return okxOrderHistory(ctx, client.sdk, exchange.ProductOption, req, inst, client.multipliers(ctx, operation))
}

func (client *okxOptionClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	const operation = "Fills"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return exchange.FillPage{}, err
	}
	if err := okxValidateFillsPage(exchange.ProductOption, req); err != nil {
		return exchange.FillPage{}, err
	}
	inst, err := client.optionalNative(operation, req.Instrument)
	if err != nil {
		return exchange.FillPage{}, err
	}
	instType := okxOptionType
	rows, err := client.sdk.GetFills(ctx, &instType, inst, okxStringPtr(req.OrderID), req.Limit)
	if err != nil {
		return exchange.FillPage{}, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	multiplier := client.multipliers(ctx, operation)
	fills := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxOptionType || !okxOptionalInstrumentMatches(req.Instrument, row.InstId) {
			return exchange.FillPage{}, okxMalformed(exchange.ProductOption, operation, "mixed product or instrument row")
		}
		factor, err := multiplier(row.InstId)
		if err != nil {
			return exchange.FillPage{}, err
		}
		fill, err := okxFill(row, factor)
		if err != nil {
			return exchange.FillPage{}, okxMalformed(exchange.ProductOption, operation, err.Error())
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *okxOptionClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueOKX, exchange.ProductOption)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if err := okxReady(ctx, exchange.ProductOption, exactLookupOperation, client.sdk); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	meta, err := client.contractMeta(ctx, exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	return okxLookupExactOrder(ctx, client.sdk, exchange.ProductOption, instrument, request, meta.contractValue)
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

// Balances lists the trading account's currencies: one OKX account margins
// options together with spot and swaps.
func (client *okxOptionClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.OptionAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

// OptionAccount reports the account-level figures, which OKX states in USD.
func (client *okxOptionClient) OptionAccount(ctx context.Context) (exchange.OptionAccount, error) {
	const operation = "OptionAccount"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return exchange.OptionAccount{}, err
	}
	rows, err := client.sdk.GetAccountBalance(ctx, nil)
	if err != nil {
		return exchange.OptionAccount{}, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	balances, err := okxBalances(exchange.ProductOption, operation, rows)
	if err != nil {
		return exchange.OptionAccount{}, err
	}
	account := exchange.OptionAccount{Balances: balances, SettleAsset: "USD"}
	if len(rows) > 0 {
		row := rows[0]
		account.Equity, account.Available = okxOptional0(row.TotalEq), okxOptional0(row.AvailEq)
		account.InitialMargin, account.MaintenanceMargin = okxOptional0(row.Imr), okxOptional0(row.Mmr)
		if upl, err := okxOptional(row.Upl); err == nil {
			account.UnrealizedPnL = upl
		}
	}
	return account, nil
}

func (client *okxOptionClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	const operation = "Positions"
	if err := okxReady(ctx, exchange.ProductOption, operation, client.sdk); err != nil {
		return nil, err
	}
	inst, err := client.optionalNative(operation, req.Instrument)
	if err != nil {
		return nil, err
	}
	instType := okxOptionType
	rows, err := client.sdk.GetPositions(ctx, &instType, inst)
	if err != nil {
		return nil, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	multiplier := client.multipliers(ctx, operation)
	positions := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxOptionType || !okxOptionalInstrumentMatches(req.Instrument, row.InstId) {
			return nil, okxMalformed(exchange.ProductOption, operation, "mixed product or instrument row")
		}
		factor, err := multiplier(row.InstId)
		if err != nil {
			return nil, err
		}
		position, err := okxPosition(row, factor)
		if err != nil {
			return nil, okxMalformed(exchange.ProductOption, operation, err.Error())
		}
		if !position.Quantity.IsZero() {
			positions = append(positions, position)
		}
	}
	return positions, nil
}

// ---------------------------------------------------------------------------
// Quote stream
// ---------------------------------------------------------------------------

// okxOptionFamilyStream fans one family's opt-summary push out to the
// option quote streams of that family.
type okxOptionFamilyStream struct {
	listeners map[*okxOptionQuoteListener]struct{}
}

type okxOptionQuoteListener struct {
	native string
	apply  func(*okx.OptionSummary)
}

// StartOptionQuote streams one option's quote: the book top (bbo-tbt), the
// mark (mark-price) and the family's IVs, forward and greeks (opt-summary,
// shared by every streamed option of the family).
func (backend *okxPublicWSBackend) StartOptionQuote(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OptionQuote]) (func() error, error) {
	const operation = "WatchOptionQuote"
	if err := okxValidateOptionInstrument(instrument); err != nil {
		return nil, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, operation); err != nil {
		return nil, err
	}
	registration := backend.registerStatus(callbacks.Status)
	native := instrument.Identity.NativeSymbol
	var mu sync.Mutex
	quote := exchange.OptionQuote{Instrument: instrument}
	update := func(apply func(*exchange.OptionQuote)) {
		mu.Lock()
		apply(&quote)
		current := quote
		mu.Unlock()
		callbacks.Event(current)
	}
	onError := func(err error) {
		emitOKXWSError(callbacks, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, err.Error()))
	}
	var stops []func() error
	stop := func() error {
		var errs []error
		for index := len(stops) - 1; index >= 0; index-- {
			if err := stops[index](); err != nil {
				errs = append(errs, err)
			}
		}
		backend.unregisterStatus(registration)
		return errors.Join(errs...)
	}
	fail := func(err error) (func() error, error) {
		_ = stop()
		return nil, okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	contract := instrument.Encoding.ContractMultiplier.Value
	if err := backend.ws.SubscribeBestBidOfferWithError(native, func(book *okx.OrderBook) {
		update(func(current *exchange.OptionQuote) {
			current.Bid, current.BidSize = okxBBOSide(book.Bids, contract)
			current.Ask, current.AskSize = okxBBOSide(book.Asks, contract)
			if at, err := okxOptionalMillis(book.Ts); err == nil && !at.IsZero() {
				current.Time = at
			}
		})
	}, onError); err != nil {
		return fail(err)
	}
	stops = append(stops, func() error {
		return backend.unsubscribe(operation, okx.WsSubscribeArgs{Channel: "bbo-tbt", InstId: native})
	})
	if err := backend.ws.SubscribeMarkPriceWithError(native, func(mark *okx.MarkPrice) {
		if mark.InstId != native {
			return
		}
		update(func(current *exchange.OptionQuote) { current.Mark = okxPositiveOptional(mark.MarkPx) })
	}, onError); err != nil {
		return fail(err)
	}
	stops = append(stops, func() error {
		return backend.unsubscribe(operation, okx.WsSubscribeArgs{Channel: "mark-price", InstId: native})
	})
	listener := &okxOptionQuoteListener{native: native, apply: func(summary *okx.OptionSummary) {
		update(func(current *exchange.OptionQuote) {
			at := current.Time
			okxApplySummary(current, summary)
			if !at.IsZero() {
				current.Time = at
			}
		})
	}}
	if err := backend.addFamilyListener(okxOptionFamily(native), listener, onError); err != nil {
		return fail(err)
	}
	stops = append(stops, func() error { return backend.removeFamilyListener(operation, okxOptionFamily(native), listener) })
	return stop, nil
}

func okxBBOSide(levels [][]string, contract decimal.Decimal) (exchange.OptionalDecimal, exchange.OptionalDecimal) {
	if len(levels) == 0 || len(levels[0]) < 2 {
		return exchange.OptionalDecimal{}, exchange.OptionalDecimal{}
	}
	return okxPositiveOptional(levels[0][0]), okxScaled(levels[0][1], contract)
}

func (backend *okxPublicWSBackend) addFamilyListener(family string, listener *okxOptionQuoteListener, onError func(error)) error {
	backend.mu.Lock()
	if backend.families == nil {
		backend.families = map[string]*okxOptionFamilyStream{}
	}
	stream := backend.families[family]
	first := stream == nil
	if first {
		stream = &okxOptionFamilyStream{listeners: map[*okxOptionQuoteListener]struct{}{}}
		backend.families[family] = stream
	}
	stream.listeners[listener] = struct{}{}
	backend.mu.Unlock()
	if !first {
		return nil
	}
	err := backend.ws.SubscribeOptionSummaryWithError(family, func(summary *okx.OptionSummary) {
		backend.mu.Lock()
		var targets []*okxOptionQuoteListener
		if current := backend.families[family]; current != nil {
			for candidate := range current.listeners {
				if candidate.native == summary.InstId {
					targets = append(targets, candidate)
				}
			}
		}
		backend.mu.Unlock()
		for _, target := range targets {
			target.apply(summary)
		}
	}, onError)
	if err != nil {
		backend.mu.Lock()
		delete(backend.families, family)
		backend.mu.Unlock()
	}
	return err
}

func (backend *okxPublicWSBackend) removeFamilyListener(operation, family string, listener *okxOptionQuoteListener) error {
	backend.mu.Lock()
	stream := backend.families[family]
	last := false
	if stream != nil {
		delete(stream.listeners, listener)
		if len(stream.listeners) == 0 {
			delete(backend.families, family)
			last = true
		}
	}
	backend.mu.Unlock()
	if last {
		return backend.unsubscribe(operation, okx.WsSubscribeArgs{Channel: "opt-summary", InstFamily: family})
	}
	return nil
}

var (
	_ exchange.OptionClient     = (*okxOptionClient)(nil)
	_ exchange.ExactOrderLookup = (*okxOptionClient)(nil)
	_ optionQuoteBackend        = (*okxPublicWSBackend)(nil)
)
