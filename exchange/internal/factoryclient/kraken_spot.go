package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	krakenspot "github.com/QuantProcessing/keeluni/sdk/krakenspot"
	"github.com/shopspring/decimal"
)

// Kraken Spot. The native symbol is the v2 pair name ("BTC/USD"), which REST
// and WebSocket v2 both accept; the canonical symbol is BASE-QUOTE.
//
// Client order ids travel as cl_ord_id (free text up to 18 characters; the
// portable uint48 is at most 15 digits). AddOrder answers with the order's
// transaction id once Kraken accepted it, so a placed order is resting or
// already filling; its fills arrive on the executions stream.

type krakenSpotClient struct {
	meta   clientMeta
	sdk    *krakenspot.Client
	ws     *spotWebSocket
	wsURL  string
	authWS string

	assetsMu sync.Mutex
	assets   map[string]krakenspot.Asset
	// legacy maps AssetPairs keys ("XXBTZUSD"), which TradesHistory reports,
	// to v2 symbols.
	legacy map[string]string
}

func NewKrakenSpot(apiKey, secret string, settings Settings) exchange.SpotClient {
	client := &krakenSpotClient{
		meta:   clientMetaFromSettings(exchange.VenueKraken, exchange.ProductSpot, settings),
		sdk:    newKrakenSpotSDK(settings).WithCredentials(apiKey, secret),
		wsURL:  krakenspot.DefaultWSURL,
		authWS: krakenspot.DefaultAuthWSURL,
	}
	if settings.WebSocketEndpoint != "" {
		client.wsURL, client.authWS = settings.WebSocketEndpoint, settings.WebSocketEndpoint
	}
	backend := newKrakenSpotWSBackend(client)
	client.ws = newSpotWebSocket(newPublicWebSocket(client.meta, backend), backend)
	return client
}

func newKrakenSpotSDK(settings Settings) *krakenspot.Client {
	sdk := krakenspot.NewClient()
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func (client *krakenSpotClient) WebSocket() exchange.SpotWebSocket { return client.ws }
func (client *krakenSpotClient) Close() error                      { return client.ws.Close() }
func (client *krakenSpotClient) String() string                    { return client.meta.redactedString() }
func (client *krakenSpotClient) GoString() string                  { return client.String() }

func (client *krakenSpotClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return client.meta.ResolveInstrument(ctx, symbol)
}

func (client *krakenSpotClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return client.meta.RefreshInstruments(ctx)
}

func (client *krakenSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
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

func (client *krakenSpotClient) native(operation string, instrument exchange.Instrument) (string, error) {
	if instrument.Identity.Venue != exchange.VenueKraken || instrument.Identity.Product != exchange.ProductSpot || instrument.Identity.NativeSymbol == "" {
		return "", client.meta.catalogInvalidRequest(operation, "instrument venue/product does not match Kraken spot")
	}
	return instrument.Identity.NativeSymbol, nil
}

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

type krakenSpotLoader struct{ settings Settings }

func NewKrakenSpotInstrumentLoader(settings Settings) exchange.InstrumentLoader {
	return krakenSpotLoader{settings: settings}
}

func (loader krakenSpotLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueKraken, product: exchange.ProductSpot}
	pairs, err := newKrakenSpotSDK(loader.settings).AssetPairs(ctx)
	if err != nil {
		return exchange.InstrumentSnapshot{}, krakenSpotNormalizeErr(meta, "InstrumentCatalog.Load", err)
	}
	instruments, err := krakenSpotInstruments(pairs)
	if err != nil {
		return exchange.InstrumentSnapshot{}, deribitMalformed(meta, "InstrumentCatalog.Load", err.Error())
	}
	return exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: instruments, LoadedAt: time.Now().UTC()}, nil
}

// krakenSpotInstruments keeps tradable pairs (online, post_only). The size
// step is 10^-lot_decimals; the minimum is ordermin; costmin the minimum
// notional.
func krakenSpotInstruments(pairs map[string]krakenspot.AssetPair) ([]exchange.Instrument, error) {
	out := make([]exchange.Instrument, 0, len(pairs))
	seen := map[string]bool{}
	for key, pair := range pairs {
		if pair.Status != "online" && pair.Status != "post_only" {
			continue
		}
		native := krakenspot.SymbolOf(pair.WSName)
		base, quote, ok := strings.Cut(native, "/")
		if !ok || base == "" || quote == "" {
			return nil, fmt.Errorf("kraken pair %s has no ws name", key)
		}
		tick := numberDecimal(pair.TickSize)
		if !tick.IsPositive() {
			tick = decimal.New(1, -int32(pair.PairDecimals))
		}
		step := decimal.New(1, -int32(pair.LotDecimals))
		minimum := numberDecimal(pair.OrderMin)
		if minimum.LessThan(step) {
			minimum = step
		}
		var minNotional exchange.OptionalDecimal
		if cost := numberDecimal(pair.CostMin); cost.IsPositive() {
			minNotional = exchange.OptionalDecimal{Value: cost, Valid: true}
		}
		symbol := base + "-" + quote
		if seen[symbol] {
			return nil, fmt.Errorf("kraken pairs map twice onto %s", symbol)
		}
		seen[symbol] = true
		instrument := newInstrument(exchange.VenueKraken, exchange.ProductSpot, symbol, base, quote, "", native, key,
			tick, step, minimum, minNotional, oneContract())
		instrument.Metadata.AssetClass = exchange.AssetClassCrypto
		instrument.Metadata.ClassificationSource = "kraken.asset_pairs"
		instrument.Metadata.RawAssetType = pair.AClassBase
		if err := instrument.Validate(); err != nil {
			return nil, fmt.Errorf("kraken pair %s: %w", key, err)
		}
		out = append(out, instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}

// ---------------------------------------------------------------------------
// Market data
// ---------------------------------------------------------------------------

func (client *krakenSpotClient) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	native, err := client.native("OrderBook", request.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	depth := request.Limit
	if depth <= 0 || depth > 500 {
		depth = 50
	}
	book, err := client.sdk.Depth(ctx, native, depth)
	if err != nil {
		return exchange.OrderBook{}, krakenSpotNormalizeErr(client.meta, "OrderBook", err)
	}
	levels := func(rows []krakenspot.Level) []exchange.BookLevel {
		out := make([]exchange.BookLevel, 0, len(rows))
		for _, row := range rows {
			out = append(out, exchange.BookLevel{Price: numberDecimal(row[0]), Quantity: numberDecimal(row[1])})
		}
		return out
	}
	return exchange.OrderBook{Instrument: request.Instrument, Bids: levels(book.Bids), Asks: levels(book.Asks), Time: time.Now().UTC(), Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

var krakenSpotIntervals = map[string]int{"1m": 1, "5m": 5, "15m": 15, "30m": 30, "1h": 60, "4h": 240, "1d": 1440}

func (client *krakenSpotClient) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	minutes, ok := krakenSpotIntervals[request.Interval]
	if !ok {
		return exchange.CandlePage{}, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "Candles",
			SafeMessage: "kraken spot has no " + request.Interval + " candles"})
	}
	native, err := client.native("Candles", request.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	// Kraken returns at most the last 720 candles of an interval.
	since := int64(0)
	if !request.Start.IsZero() {
		since = request.Start.Unix() - 1
	}
	rows, err := client.sdk.OHLC(ctx, native, minutes, since)
	if err != nil {
		return exchange.CandlePage{}, krakenSpotNormalizeErr(client.meta, "Candles", err)
	}
	step := time.Duration(minutes) * time.Minute
	now := time.Now()
	candles := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		open := time.Unix(row.Time, 0).UTC()
		if !request.End.IsZero() && open.After(request.End) {
			continue
		}
		candles = append(candles, exchange.Candle{OpenTime: open, CloseTime: open.Add(step), Open: stringDecimal(row.Open), High: stringDecimal(row.High),
			Low: stringDecimal(row.Low), Close: stringDecimal(row.Close), Volume: stringDecimal(row.Volume), Complete: !open.Add(step).After(now)})
	}
	if request.Limit > 0 && len(candles) > request.Limit {
		candles = candles[len(candles)-request.Limit:]
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}}, nil
}

func (client *krakenSpotClient) PublicTrades(ctx context.Context, request exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	native, err := client.native("PublicTrades", request.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	rows, err := client.sdk.Trades(ctx, native, request.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, krakenSpotNormalizeErr(client.meta, "PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		side := exchange.SideBuy
		if row.Side == "s" {
			side = exchange.SideSell
		}
		trades = append(trades, exchange.PublicTrade{Instrument: request.Instrument, TradeID: strconv.FormatInt(row.TradeID, 10), Side: side,
			Price: stringDecimal(row.Price), Quantity: stringDecimal(row.Volume), Time: row.Time})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

func (client *krakenSpotClient) assetTable(ctx context.Context) map[string]krakenspot.Asset {
	client.assetsMu.Lock()
	defer client.assetsMu.Unlock()
	if client.assets == nil {
		if assets, err := client.sdk.Assets(ctx); err == nil {
			client.assets = assets
		}
	}
	return client.assets
}

func (client *krakenSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	rows, err := client.sdk.BalanceEx(ctx)
	if err != nil {
		return nil, krakenSpotNormalizeErr(client.meta, "Balances", err)
	}
	assets := client.assetTable(ctx)
	byAsset := map[string]exchange.Balance{}
	for key, row := range rows {
		asset, ok := krakenspot.BalanceAsset(key, assets)
		if !ok {
			continue
		}
		total := numberDecimal(row.Balance).Add(numberDecimal(row.Credit)).Sub(numberDecimal(row.CreditUse))
		held := numberDecimal(row.HoldTrade)
		current := byAsset[asset]
		current.Asset = asset
		current.Total = current.Total.Add(total)
		current.Locked = current.Locked.Add(held)
		current.Available = current.Total.Sub(current.Locked)
		byAsset[asset] = current
	}
	out := make([]exchange.Balance, 0, len(byAsset))
	for _, balance := range byAsset {
		if balance.Total.IsZero() && balance.Locked.IsZero() {
			continue
		}
		out = append(out, balance)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out, nil
}

func (client *krakenSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	balances, err := client.Balances(ctx)
	return exchange.SpotAccount{Balances: balances}, err
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

func krakenSpotOrderRequest(request exchange.PlaceOrderRequest) krakenspot.OrderRequest {
	out := krakenspot.OrderRequest{Pair: request.Instrument.Identity.NativeSymbol, Side: string(request.Side), Volume: request.Quantity.String(), ClientOrderID: request.ClientOrderID}
	if request.Type == exchange.OrderTypeMarket {
		out.Type = "market"
		return out
	}
	out.Type, out.Price = "limit", request.LimitPrice.String()
	switch request.LimitPolicy {
	case exchange.LimitPolicyIOC:
		out.TimeInForce = "IOC"
	case exchange.LimitPolicyPostOnly:
		out.Flags = "post"
	}
	return out
}

func (client *krakenSpotClient) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	native, err := client.native("PlaceOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueKraken, Product: exchange.ProductSpot, Operation: exchange.OrderOperationPlace,
		NativeSymbol: native, OrderType: request.Type, ClientOrderID: request.ClientOrderID}
	result, err := client.sdk.AddOrder(ctx, krakenSpotOrderRequest(request))
	if err != nil {
		return krakenSpotCommandErr(client.meta, ack, err)
	}
	if len(result.TxID) == 0 {
		ack.State = exchange.AckAmbiguous
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "PlaceOrder",
			SafeMessage: "kraken returned no transaction id"})
	}
	ack.OrderID = result.TxID[0]
	// A market order or a crossing limit fills at once; the stream reports
	// how much. Kraken has accepted the order either way.
	ack.State = exchange.AckAcceptedPending
	return ack, nil
}

func (client *krakenSpotClient) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	native, err := client.native("CancelOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(request.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, client.meta.catalogInvalidRequest("CancelOrder", "order id is required")
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueKraken, Product: exchange.ProductSpot, Operation: exchange.OrderOperationCancel, NativeSymbol: native, OrderID: request.OrderID}
	if _, err := client.sdk.CancelOrder(ctx, request.OrderID, ""); err != nil {
		return krakenSpotCommandErr(client.meta, ack, err)
	}
	ack.State = exchange.AckCanceled
	return ack, nil
}

func krakenSpotOrder(txid string, row krakenspot.OrderInfo, native string) exchange.Order {
	filled := numberDecimal(row.VolumeExec)
	order := exchange.Order{NativeSymbol: native, OrderID: txid, ClientOrderID: crossExPortableClientOrderID(row.ClientOrderID),
		Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: numberDecimal(row.Volume), Filled: filled,
		CreatedAt: row.Opened(), UpdatedAt: row.Closed()}
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = order.CreatedAt
	}
	if row.Description.Type == "sell" {
		order.Side = exchange.SideSell
	}
	if row.Description.OrderType == "market" {
		order.Type = exchange.OrderTypeMarket
	} else {
		order.LimitPrice = stringDecimal(row.Description.Price)
		order.LimitPolicy = exchange.LimitPolicyResting
		if strings.Contains(row.Flags, "post") {
			order.LimitPolicy = exchange.LimitPolicyPostOnly
		}
	}
	if price := numberDecimal(row.Price); price.IsPositive() && filled.IsPositive() {
		order.AverageFillPrice = exchange.OptionalDecimal{Value: price, Valid: true}
	}
	switch row.Status {
	case "pending":
		order.Status = "pending"
	case "open":
		order.Status = "open"
		if filled.IsPositive() {
			order.Status = "partially_filled"
		}
	case "closed":
		order.Status = "filled"
	case "canceled", "expired":
		order.Status = "canceled"
		if filled.IsPositive() {
			order.Status = "partially_filled_canceled"
		}
	default:
		order.Status = row.Status
	}
	if row.Reason != nil && order.Status == "canceled" && !filled.IsPositive() && strings.Contains(strings.ToLower(*row.Reason), "reject") {
		order.Status, order.RejectMessage = "rejected", cleanVenueMessage(*row.Reason)
	}
	return order
}

// nativeOf maps an order's pair (v2 symbol or altname) to the native symbol.
func (client *krakenSpotClient) nativeOf(ctx context.Context, pair string) string {
	if strings.Contains(pair, "/") {
		return pair
	}
	if symbol, ok := client.legacyMap(ctx)[pair]; ok {
		return symbol
	}
	return pair
}

func (client *krakenSpotClient) legacyMap(ctx context.Context) map[string]string {
	client.assetsMu.Lock()
	ready := client.legacy != nil
	client.assetsMu.Unlock()
	if ready {
		return client.legacy
	}
	instruments, err := client.Instruments(ctx)
	if err != nil {
		return map[string]string{}
	}
	pairs, _ := client.sdk.AssetPairs(ctx)
	out := map[string]string{}
	for _, instrument := range instruments {
		out[instrument.Identity.NativeID] = instrument.Identity.NativeSymbol
	}
	for key, pair := range pairs {
		out[pair.Altname] = krakenspot.SymbolOf(pair.WSName)
		out[key] = krakenspot.SymbolOf(pair.WSName)
	}
	client.assetsMu.Lock()
	client.legacy = out
	client.assetsMu.Unlock()
	return out
}

func (client *krakenSpotClient) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
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
		return exchange.OrderPage{}, krakenSpotNormalizeErr(client.meta, "OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for txid, row := range rows {
		native := client.nativeOf(ctx, row.Description.Pair)
		if filter != "" && native != filter {
			continue
		}
		orders = append(orders, krakenSpotOrder(txid, row, native))
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAt.After(orders[j].CreatedAt) })
	return boundedOrderPage(orders, request.Limit, ""), nil
}

func (client *krakenSpotClient) OrderHistory(ctx context.Context, request exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
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
	offset := 0
	if request.Cursor != "" {
		parsed, err := strconv.Atoi(request.Cursor)
		if err != nil || parsed < 0 {
			return exchange.OrderPage{}, client.meta.catalogInvalidRequest("OrderHistory", "cursor is invalid")
		}
		offset = parsed
	}
	rows, count, err := client.sdk.ClosedOrders(ctx, offset, secondsOf(request.Start), secondsOf(request.End), "")
	if err != nil {
		return exchange.OrderPage{}, krakenSpotNormalizeErr(client.meta, "OrderHistory", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for txid, row := range rows {
		native := client.nativeOf(ctx, row.Description.Pair)
		if filter != "" && native != filter {
			continue
		}
		orders = append(orders, krakenSpotOrder(txid, row, native))
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].UpdatedAt.After(orders[j].UpdatedAt) })
	page := exchange.PageInfo{Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End, HasMoreKnown: true}
	if next := offset + len(rows); next < count {
		page.Cursor, page.HasMore = strconv.Itoa(next), true
	}
	return exchange.OrderPage{Orders: orders, Page: page}, nil
}

func secondsOf(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func (client *krakenSpotClient) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
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
	offset := 0
	if request.Cursor != "" {
		parsed, err := strconv.Atoi(request.Cursor)
		if err != nil || parsed < 0 {
			return exchange.FillPage{}, client.meta.catalogInvalidRequest("Fills", "cursor is invalid")
		}
		offset = parsed
	}
	rows, count, err := client.sdk.TradesHistory(ctx, offset, secondsOf(request.Start), secondsOf(request.End))
	if err != nil {
		return exchange.FillPage{}, krakenSpotNormalizeErr(client.meta, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	for id, row := range rows {
		native := client.nativeOf(ctx, row.Pair)
		if (filter != "" && native != filter) || (request.OrderID != "" && row.OrderTxID != request.OrderID) {
			continue
		}
		// TradesHistory fees are in the quote currency.
		_, quote, _ := strings.Cut(native, "/")
		fill := exchange.Fill{NativeSymbol: native, OrderID: row.OrderTxID, FillID: id, Side: exchange.SideBuy, Price: numberDecimal(row.Price),
			Quantity: numberDecimal(row.Volume), Fee: numberDecimal(row.Fee), FeeAsset: quote,
			Liquidity: exchange.LiquidityTaker, Time: row.At()}
		if row.Type == "sell" {
			fill.Side = exchange.SideSell
		}
		if row.Maker {
			fill.Liquidity = exchange.LiquidityMaker
		}
		fills = append(fills, fill)
	}
	sort.Slice(fills, func(i, j int) bool { return fills[i].Time.After(fills[j].Time) })
	page := exchange.PageInfo{Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End, HasMoreKnown: true}
	if next := offset + len(rows); next < count {
		page.Cursor, page.HasMore = strconv.Itoa(next), true
	}
	return exchange.FillPage{Fills: fills, Page: page}, nil
}

// LookupExactOrder finds an order by transaction id (authoritative) or by
// client order id among open and closed orders. Kraken keeps closed orders
// queryable, so a client id absent from both is not found only when Kraken
// answered both reads.
func (client *krakenSpotClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueKraken, exchange.ProductSpot)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	native := instrument.Identity.NativeSymbol
	if request.ClientOrderID == "" {
		rows, err := client.sdk.QueryOrders(ctx, request.VenueOrderID)
		switch {
		case err == nil:
			if row, ok := rows[request.VenueOrderID]; ok {
				return exactLookupFound(request, exchange.VenueKraken, exchange.ProductSpot, instrument, krakenSpotOrder(request.VenueOrderID, row, native))
			}
			return exactLookupNotFound(request, exchange.VenueKraken, exchange.ProductSpot, instrument)
		case krakenspot.IsUnknownOrder(err) || krakenspotInvalidOrder(err):
			return exactLookupNotFound(request, exchange.VenueKraken, exchange.ProductSpot, instrument)
		default:
			return exchange.ExactOrderLookupResult{}, krakenSpotNormalizeErr(client.meta, exactLookupOperation, err)
		}
	}
	open, err := client.sdk.OpenOrders(ctx)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, krakenSpotNormalizeErr(client.meta, exactLookupOperation, err)
	}
	for txid, row := range open {
		if row.ClientOrderID == request.ClientOrderID {
			return exactLookupFound(request, exchange.VenueKraken, exchange.ProductSpot, instrument, krakenSpotOrder(txid, row, native))
		}
	}
	closed, _, err := client.sdk.ClosedOrders(ctx, 0, 0, 0, request.ClientOrderID)
	if err != nil {
		return exactLookupUnconfirmed(request, exchange.VenueKraken, exchange.ProductSpot, instrument, "")
	}
	for txid, row := range closed {
		if row.ClientOrderID == request.ClientOrderID {
			return exactLookupFound(request, exchange.VenueKraken, exchange.ProductSpot, instrument, krakenSpotOrder(txid, row, native))
		}
	}
	return exactLookupNotFound(request, exchange.VenueKraken, exchange.ProductSpot, instrument)
}

func krakenspotInvalidOrder(err error) bool {
	var apiErr *krakenspot.APIError
	return errors.As(err, &apiErr) && strings.HasPrefix(apiErr.Code(), "EGeneral:Invalid arguments")
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func krakenSpotNormalizeErr(meta clientMeta, operation string, err error) (returnErr error) {
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
	if errors.Is(err, krakenspot.ErrCredentialsRequired) {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "kraken credentials are required"})
	}
	var apiErr *krakenspot.APIError
	if errors.As(err, &apiErr) {
		kind := exchange.KindVenueRejected
		switch {
		case krakenspot.IsAuthError(err):
			kind = exchange.KindAuthentication
			if strings.Contains(apiErr.Code(), "Permission") {
				kind = exchange.KindPermission
			}
		case krakenspot.IsRateLimit(err) || apiErr.StatusCode == http.StatusTooManyRequests:
			kind = exchange.KindRateLimit
		case krakenspot.IsUnknownOrder(err):
			kind = exchange.KindNotFound
		case strings.HasPrefix(apiErr.Code(), "EGeneral:Invalid arguments"):
			kind = exchange.KindInvalidRequest
		}
		return exchange.NewError(kind, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, Code: apiErr.Code(), SafeMessage: "kraken request failed"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "kraken transport failed"})
}

// krakenSpotCommandErr: an E-coded order or argument error is a refusal.
// EService (unavailable, busy) and transport failures leave the outcome
// unknown — Kraken documents that EService:Unavailable orders may exist.
func krakenSpotCommandErr(meta clientMeta, ack exchange.OrderAcknowledgement, err error) (returnAck exchange.OrderAcknowledgement, returnErr error) {
	operation := "PlaceOrder"
	if ack.Operation == exchange.OrderOperationCancel {
		operation = "CancelOrder"
	}
	defer func() { normalizeHTTPCommand(&returnAck, &returnErr, err, meta.venue, meta.product, operation) }()
	var apiErr *krakenspot.APIError
	if errors.As(err, &apiErr) && (krakenspot.IsOrderRejection(err) || krakenspot.IsAuthError(err)) && !krakenspot.IsRateLimit(err) {
		ack.State, ack.VenueCode = exchange.AckRejected, apiErr.Code()
		ack.VenueMessage = cleanVenueMessage(apiErr.Code())
		return ack, krakenSpotNormalizeErr(meta, operation, err)
	}
	if errors.Is(err, krakenspot.ErrCredentialsRequired) {
		ack.State = exchange.AckRejected
		return ack, krakenSpotNormalizeErr(meta, operation, err)
	}
	ack.State = exchange.AckAmbiguous
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "kraken command outcome is ambiguous"})
}

var (
	_ exchange.SpotClient       = (*krakenSpotClient)(nil)
	_ exchange.ExactOrderLookup = (*krakenSpotClient)(nil)
)
