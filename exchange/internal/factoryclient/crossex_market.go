package factoryclient

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/shopspring/decimal"
)

// CrossEx-only instruments have no native client. Their market data is the
// CrossEx public stream: base-unit sizes, no history. They are identified by
// the exchange whose book they trade (VenueKraken, VenueDeribit, VenueBinance
// for TradFi and USDC perps), like every brokered instrument.

// crossExOnlyMarket maps a CrossEx (exchange_type, quote) with no native
// counterpart to the venue and settlement that identify its instruments.
type crossExOnlyMarket struct {
	exchangeType string
	quote        string
	venue        exchange.Venue
	// books is the snapshot channel depth, or 0 when only top of book is
	// available (served from the ticker).
	books int
	// candles reports whether CrossEx streams klines for the exchange.
	candles bool
}

var crossExOnlyMarkets = []crossExOnlyMarket{
	{"KRAKEN", "USD", exchange.VenueKraken, 0, false},
	{"DERIBIT", "USDC", exchange.VenueDeribit, 20, true},
	{"BINANCE", "USDT", exchange.VenueBinance, 20, true},
	{"BINANCE", "USDC", exchange.VenueBinance, 20, true},
}

// CrossExOnlyMarketRoutes lists the markets whose instruments exist only on
// CrossEx. Binance USDT appears here for its TradFi perps; its crypto perps
// are native and are excluded per instrument by MapCrossExOnlyInstruments.
func CrossExOnlyMarketRoutes() []CrossExMarketRoute {
	out := make([]CrossExMarketRoute, 0, len(crossExOnlyMarkets))
	for _, market := range crossExOnlyMarkets {
		out = append(out, CrossExMarketRoute{Venue: market.venue, Settlement: market.quote})
	}
	return out
}

func crossExOnlyMarketFor(exchangeType, quote string) (crossExOnlyMarket, bool) {
	for _, market := range crossExOnlyMarkets {
		if market.exchangeType == exchangeType && market.quote == quote {
			return market, true
		}
	}
	return crossExOnlyMarket{}, false
}

// MapCrossExOnlyInstruments builds instruments for live CrossEx perps of the
// CrossEx-only markets that natives does not already cover. Their canonical
// symbol is BASE-QUOTE; their rules are CrossEx's.
func MapCrossExOnlyInstruments(rows []crossex.Symbol, natives []exchange.Instrument) ([]exchange.Instrument, error) {
	nativeSymbols := map[string]bool{}
	for _, instrument := range natives {
		nativeSymbols[string(instrument.Identity.Venue)+":"+instrument.Identity.Symbol] = true
	}
	var out []exchange.Instrument
	seen := map[string]bool{}
	for _, row := range rows {
		if row.BusinessType != crossex.BusinessFuture || row.State != "live" {
			continue
		}
		market, ok := crossExOnlyMarketFor(row.ExchangeType, row.Quote())
		if !ok {
			continue
		}
		base := row.Base()
		symbol := base + "-" + row.Quote()
		key := string(market.venue) + ":" + symbol
		if base == "" || nativeSymbols[key] {
			continue
		}
		// A native venue can list the same canonical symbol; Binance crypto
		// perps are matched by native symbol in MapCrossExInstruments.
		if market.venue == exchange.VenueBinance && nativeBinanceListed(natives, base, row.Quote()) {
			continue
		}
		if seen[key] {
			return nil, fmt.Errorf("crossex symbols map twice onto %s", key)
		}
		seen[key] = true
		instrument := newInstrument(market.venue, exchange.ProductPerp, symbol, base, row.Quote(), row.Quote(), row.Symbol, "",
			decimal.NewFromInt(1), decimal.NewFromInt(1), decimal.Zero, exchange.OptionalDecimal{}, oneContract())
		mapped, err := crossExInstrument(instrument, row)
		if err != nil {
			return nil, fmt.Errorf("crossex symbol %s: %w", row.Symbol, err)
		}
		mapped.Metadata.ClassificationSource = "crossex"
		mapped.Metadata.RawCategories = exchange.NewInstrumentCategories([]string{"crossex"})
		if strings.TrimSpace(row.Tag) != "" {
			mapped.Metadata.RawAssetType = strings.TrimSpace(row.Tag)
		}
		out = append(out, mapped)
	}
	return out, nil
}

func nativeBinanceListed(natives []exchange.Instrument, base, quote string) bool {
	for _, instrument := range natives {
		if instrument.Identity.Venue == exchange.VenueBinance && instrument.Identity.NativeSymbol == base+quote {
			return true
		}
	}
	return false
}

// IsCrossExOnly reports whether an instrument's market data comes from the
// CrossEx public stream.
func IsCrossExOnly(instrument exchange.Instrument) bool {
	return instrument.Metadata.ClassificationSource == "crossex"
}

// ---------------------------------------------------------------------------
// Market client
// ---------------------------------------------------------------------------

// CrossExMarket serves market data for CrossEx-only instruments. It satisfies
// exchange.MarketREST and exchange.PerpMarketWebSocket. REST history does not
// exist; snapshot reads are answered from the live stream once it has data.
type CrossExMarket struct {
	meta    clientMeta
	symbols func(context.Context) ([]crossex.Symbol, error)
	natives func(context.Context) ([]exchange.Instrument, error)
	stream  *crossex.PublicStream

	mu          sync.RWMutex
	instruments map[string]exchange.Instrument // by canonical symbol
}

func NewCrossExMarket(natives CrossExNativeSource, settings Settings) *CrossExMarket {
	rest := crossex.NewClient()
	if settings.Endpoint != "" {
		rest.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		rest.WithHTTPClient(settings.HTTPClient)
	}
	stream := crossex.NewPublicStream()
	if settings.WebSocketEndpoint != "" {
		stream.WithURL(settings.WebSocketEndpoint)
	}
	return &CrossExMarket{
		meta:        clientMeta{venue: exchange.VenueCrossEx, product: exchange.ProductPerp},
		symbols:     func(ctx context.Context) ([]crossex.Symbol, error) { return rest.Symbols(ctx) },
		natives:     natives,
		stream:      stream,
		instruments: map[string]exchange.Instrument{},
	}
}

func (market *CrossExMarket) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := crossExContext(ctx, market.meta, "RefreshInstruments"); err != nil {
		return nil, err
	}
	rows, err := market.symbols(ctx)
	if err != nil {
		return nil, crossExNormalizeErr(market.meta, "RefreshInstruments", err)
	}
	var natives []exchange.Instrument
	if market.natives != nil {
		if natives, err = market.natives(ctx); err != nil {
			return nil, withExchangeOperation(err, "RefreshInstruments")
		}
	}
	instruments, err := MapCrossExOnlyInstruments(rows, natives)
	if err != nil {
		return nil, crossExMalformed(market.meta, "RefreshInstruments", "crossex rules are inconsistent")
	}
	bySymbol := make(map[string]exchange.Instrument, len(instruments))
	for _, instrument := range instruments {
		bySymbol[string(instrument.Identity.Venue)+":"+instrument.Identity.Symbol] = instrument
	}
	market.mu.Lock()
	market.instruments = bySymbol
	market.mu.Unlock()
	return instruments, nil
}

func (market *CrossExMarket) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	market.mu.RLock()
	out := make([]exchange.Instrument, 0, len(market.instruments))
	for _, instrument := range market.instruments {
		out = append(out, instrument)
	}
	market.mu.RUnlock()
	if len(out) == 0 {
		return market.RefreshInstruments(ctx)
	}
	return out, nil
}

func (market *CrossExMarket) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	instruments, err := market.Instruments(ctx)
	if err != nil {
		return exchange.Instrument{}, err
	}
	for _, instrument := range instruments {
		if instrument.Identity.NativeSymbol == symbol || string(instrument.Identity.Venue)+":"+instrument.Identity.Symbol == symbol {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, market.meta.catalogMetadataResolution("ResolveInstrument", "instrument metadata not found")
}

func (market *CrossExMarket) spec(instrument exchange.Instrument) (crossExOnlyMarket, error) {
	parts := strings.Split(instrument.Identity.NativeSymbol, "_")
	if len(parts) != 4 || !IsCrossExOnly(instrument) {
		return crossExOnlyMarket{}, market.meta.catalogInvalidRequest("Watch", "instrument is not a crossex-only instrument")
	}
	spec, ok := crossExOnlyMarketFor(parts[0], parts[3])
	if !ok {
		return crossExOnlyMarket{}, market.meta.catalogInvalidRequest("Watch", "instrument is not a crossex-only instrument")
	}
	return spec, nil
}

// History does not exist on CrossEx.
func (market *CrossExMarket) OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error) {
	return exchange.OrderBook{}, crossExNoHistory(market.meta, "OrderBook")
}

func (market *CrossExMarket) Candles(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error) {
	return exchange.CandlePage{}, crossExNoHistory(market.meta, "Candles")
}

func (market *CrossExMarket) PublicTrades(context.Context, exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	return exchange.PublicTradePage{}, crossExNoHistory(market.meta, "PublicTrades")
}

func crossExNoHistory(meta clientMeta, operation string) error {
	return exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation,
		SafeMessage: "crossex publishes no market history; data is live from the public stream"})
}

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// watchPublic opens one public topic for a subscription. A refusal from
// CrossEx (unsupported channel for the exchange) ends the subscription with
// an Unsupported error.
func watchPublic[T any](ctx context.Context, market *CrossExMarket, operation, channel string, instrument exchange.Instrument, buffer int,
	project func(crossex.PublicUpdate) ([]T, bool)) (exchange.Subscription[T], error) {
	if ctx == nil || buffer < 0 || buffer > maxWebSocketBuffer {
		return nil, websocketError(market.meta, operation, exchange.KindInvalidRequest, "watch request is invalid")
	}
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	watchCtx, cancel := context.WithCancel(ctx)
	var stop func()
	var stopOnce sync.Once
	id := fmt.Sprintf("crossex:public:%s:%s:%d", channel, instrument.Identity.NativeSymbol, websocketSubscriptionSequence.Add(1))
	subscription := newTypedSubscription[T](id, market.meta, buffer, func() error {
		cancel()
		stopOnce.Do(func() {
			if stop != nil {
				stop()
			}
		})
		return nil
	})
	active := sync.Once{}
	var err error
	stop, err = market.stream.Subscribe(channel, instrument.Identity.NativeSymbol, func(update crossex.PublicUpdate) {
		if watchCtx.Err() != nil {
			return
		}
		events, ok := project(update)
		if !ok {
			subscription.emitError(exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: market.meta.venue, Product: market.meta.product, Operation: operation, SafeMessage: "crossex public push is malformed"}))
			return
		}
		active.Do(func() {
			subscription.emitStatus(backendStatus{State: exchange.SubscriptionActive, Time: time.Now().UTC()})
		})
		for _, event := range events {
			subscription.emit(event)
		}
	}, func(refusal error) {
		subscription.emitError(exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: market.meta.venue, Product: market.meta.product, Operation: operation, SafeMessage: refusal.Error()}))
		_ = subscription.Close()
	})
	if err != nil {
		cancel()
		return nil, websocketError(market.meta, operation, exchange.KindSubscriptionClosed, err.Error())
	}
	subscription.emitStatus(backendStatus{State: exchange.SubscriptionConnecting, Time: time.Now().UTC()})
	go func() {
		<-watchCtx.Done()
		_ = subscription.Close()
	}()
	return subscription, nil
}

func (market *CrossExMarket) WatchOrderBook(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.BookEvent], error) {
	spec, err := market.spec(request.Instrument)
	if err != nil {
		return nil, err
	}
	instrument := request.Instrument
	if spec.books > 0 {
		// Each snapshot is a self-contained top-N book.
		return watchPublic(ctx, market, "WatchOrderBook", crossex.ChannelOrderBook(spec.books), instrument, request.Options.Buffer,
			func(update crossex.PublicUpdate) ([]exchange.BookEvent, bool) {
				var book crossex.PublicBook
				if crossex.DecodePublic(update.Result, &book) != nil {
					return nil, false
				}
				return []exchange.BookEvent{{Kind: exchange.EventSnapshot, Instrument: instrument, Bids: crossExLevels(book.Bids), Asks: crossExLevels(book.Asks), Time: crossExMillis(book.Time)}}, true
			})
	}
	// No snapshot channel (Kraken). CrossEx's incremental stream does not
	// reliably open with a snapshot (measured: often deltas only), so a full
	// book cannot be built. The ticker's best bid and ask is the honest book:
	// one level per side, replaced on every push.
	return watchPublic(ctx, market, "WatchOrderBook", crossex.ChannelTicker, instrument, request.Options.Buffer,
		func(update crossex.PublicUpdate) ([]exchange.BookEvent, bool) {
			var ticker crossex.PublicTicker
			if crossex.DecodePublic(update.Result, &ticker) != nil {
				return nil, false
			}
			event := exchange.BookEvent{Kind: exchange.EventSnapshot, Instrument: instrument, Time: crossExMillis(ticker.Time)}
			if bid := crossExDecimal(ticker.BidPrice); bid.IsPositive() {
				event.Bids = []exchange.BookLevel{{Price: bid, Quantity: crossExDecimal(ticker.BidSize)}}
			}
			if ask := crossExDecimal(ticker.AskPrice); ask.IsPositive() {
				event.Asks = []exchange.BookLevel{{Price: ask, Quantity: crossExDecimal(ticker.AskSize)}}
			}
			return []exchange.BookEvent{event}, true
		})
}

func (market *CrossExMarket) WatchBBO(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.BBOEvent], error) {
	if _, err := market.spec(request.Instrument); err != nil {
		return nil, err
	}
	instrument := request.Instrument
	return watchPublic(ctx, market, "WatchBBO", crossex.ChannelTicker, instrument, request.Options.Buffer,
		func(update crossex.PublicUpdate) ([]exchange.BBOEvent, bool) {
			var ticker crossex.PublicTicker
			if crossex.DecodePublic(update.Result, &ticker) != nil {
				return nil, false
			}
			return []exchange.BBOEvent{{Instrument: instrument,
				Bid:  exchange.BookLevel{Price: crossExDecimal(ticker.BidPrice), Quantity: crossExDecimal(ticker.BidSize)},
				Ask:  exchange.BookLevel{Price: crossExDecimal(ticker.AskPrice), Quantity: crossExDecimal(ticker.AskSize)},
				Time: crossExMillis(ticker.Time)}}, true
		})
}

func (market *CrossExMarket) WatchPublicTrades(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.PublicTradeEvent], error) {
	if _, err := market.spec(request.Instrument); err != nil {
		return nil, err
	}
	instrument := request.Instrument
	return watchPublic(ctx, market, "WatchPublicTrades", crossex.ChannelTrade, instrument, request.Options.Buffer,
		func(update crossex.PublicUpdate) ([]exchange.PublicTradeEvent, bool) {
			var trade crossex.PublicTrade
			if crossex.DecodePublic(update.Result, &trade) != nil {
				return nil, false
			}
			return []exchange.PublicTradeEvent{{Instrument: instrument, TradeID: trade.ID, Side: crossExSide(trade.Side),
				Price: crossExDecimal(trade.Price), Quantity: crossExDecimal(trade.Qty), Time: crossExMillis(trade.Time)}}, true
		})
}

var crossExKlineIntervals = map[string]bool{"1m": true, "5m": true, "15m": true, "30m": true, "1h": true, "4h": true, "1d": true}

func (market *CrossExMarket) WatchCandles(ctx context.Context, request exchange.WatchCandlesRequest) (exchange.Subscription[exchange.CandleEvent], error) {
	spec, err := market.spec(request.Instrument)
	if err != nil {
		return nil, err
	}
	if !spec.candles {
		return nil, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: market.meta.venue, Product: market.meta.product, Operation: "WatchCandles",
			SafeMessage: "crossex streams no candles for this exchange"})
	}
	if !crossExKlineIntervals[request.Interval] {
		return nil, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: market.meta.venue, Product: market.meta.product, Operation: "WatchCandles",
			SafeMessage: "crossex does not stream this candle interval"})
	}
	instrument, interval := request.Instrument, request.Interval
	return watchPublic(ctx, market, "WatchCandles", crossex.ChannelKline(interval), instrument, request.Options.Buffer,
		func(update crossex.PublicUpdate) ([]exchange.CandleEvent, bool) {
			var kline crossex.PublicKline
			if crossex.DecodePublic(update.Result, &kline) != nil {
				return nil, false
			}
			return []exchange.CandleEvent{{Instrument: instrument, Interval: interval, Candle: exchange.Candle{
				OpenTime: crossExMillis(kline.Start), CloseTime: crossExMillis(kline.End),
				Open: crossExDecimal(kline.Open), High: crossExDecimal(kline.High), Low: crossExDecimal(kline.Low),
				Close: crossExDecimal(kline.Close), Volume: crossExDecimal(kline.Volume), Complete: kline.Closed,
			}}}, true
		})
}

func (market *CrossExMarket) WatchMarkPrice(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.MarkPriceEvent], error) {
	if _, err := market.spec(request.Instrument); err != nil {
		return nil, err
	}
	instrument := request.Instrument
	return watchPublic(ctx, market, "WatchMarkPrice", crossex.ChannelMarkPrice, instrument, request.Options.Buffer,
		func(update crossex.PublicUpdate) ([]exchange.MarkPriceEvent, bool) {
			var mark crossex.PublicMark
			if crossex.DecodePublic(update.Result, &mark) != nil {
				return nil, false
			}
			return []exchange.MarkPriceEvent{{Instrument: instrument, Price: crossExDecimal(mark.Price), Time: crossExMillis(update.TimeMS)}}, true
		})
}

func (market *CrossExMarket) WatchFundingRate(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.FundingRateEvent], error) {
	if _, err := market.spec(request.Instrument); err != nil {
		return nil, err
	}
	instrument := request.Instrument
	return watchPublic(ctx, market, "WatchFundingRate", crossex.ChannelFundingRate, instrument, request.Options.Buffer,
		func(update crossex.PublicUpdate) ([]exchange.FundingRateEvent, bool) {
			var funding crossex.PublicFunding
			if crossex.DecodePublic(update.Result, &funding) != nil {
				return nil, false
			}
			event := exchange.FundingRateEvent{Instrument: instrument, Rate: crossExDecimal(funding.Rate), EffectiveAt: crossExMillis(update.TimeMS)}
			if funding.Next != nil {
				event.NextAt = crossExMillis(*funding.Next)
			}
			return []exchange.FundingRateEvent{event}, true
		})
}

// WatchDerivativeReference merges mark price and funding into one stream so
// the ticker shows both. Open interest is not offered for every exchange and
// is left out.
func (market *CrossExMarket) WatchDerivativeReference(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.DerivativeReferenceEvent], error) {
	if _, err := market.spec(request.Instrument); err != nil {
		return nil, err
	}
	instrument := request.Instrument
	marks, err := market.WatchMarkPrice(ctx, request)
	if err != nil {
		return nil, err
	}
	funding, err := market.WatchFundingRate(ctx, request)
	if err != nil {
		_ = marks.Close()
		return nil, err
	}
	buffer := request.Options.Buffer
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	id := fmt.Sprintf("crossex:public:reference:%s:%d", instrument.Identity.NativeSymbol, websocketSubscriptionSequence.Add(1))
	done := make(chan struct{})
	var closeOnce sync.Once
	subscription := newTypedSubscription[exchange.DerivativeReferenceEvent](id, market.meta, buffer, func() error {
		closeOnce.Do(func() { close(done) })
		_ = marks.Close()
		_ = funding.Close()
		return nil
	})
	go func() {
		var current exchange.DerivativeReferenceEvent
		current.Instrument = instrument
		markEvents, fundingEvents := marks.Events(), funding.Events()
		for {
			select {
			case <-done:
				return
			case status, ok := <-marks.Status():
				if ok && status.State == exchange.SubscriptionActive {
					subscription.emitStatus(backendStatus{State: exchange.SubscriptionActive, Time: status.Time})
				}
			case err, ok := <-marks.Errors():
				if ok {
					subscription.emitError(err)
				}
			case event, ok := <-markEvents:
				if !ok {
					markEvents = nil
					continue
				}
				current.MarkPrice = exchange.OptionalDecimal{Value: event.Price, Valid: true}
				current.MarkObservedAt = event.Time
				subscription.emit(current)
			case event, ok := <-fundingEvents:
				if !ok {
					fundingEvents = nil
					continue
				}
				current.FundingRate = exchange.OptionalDecimal{Value: event.Rate, Valid: true}
				current.NextFundingTime = event.NextAt
				current.FundingObservedAt = event.EffectiveAt
				subscription.emit(current)
			}
		}
	}()
	return subscription, nil
}

// Ticker24h is the 24-hour summary CrossEx pushes on its ticker channel.
type Ticker24h struct {
	Last, Open, High, Low, Volume decimal.Decimal
	Time                          time.Time
}

// Ticker24h waits for one ticker push. CrossEx publishes no REST history, so
// this is the only source of 24-hour figures for CrossEx-only instruments.
func (market *CrossExMarket) Ticker24h(ctx context.Context, instrument exchange.Instrument) (Ticker24h, error) {
	if _, err := market.spec(instrument); err != nil {
		return Ticker24h{}, err
	}
	received := make(chan crossex.PublicTicker, 1)
	stop, err := market.stream.Subscribe(crossex.ChannelTicker, instrument.Identity.NativeSymbol, func(update crossex.PublicUpdate) {
		var ticker crossex.PublicTicker
		if crossex.DecodePublic(update.Result, &ticker) == nil {
			select {
			case received <- ticker:
			default:
			}
		}
	}, nil)
	if err != nil {
		return Ticker24h{}, websocketError(market.meta, "Ticker24h", exchange.KindSubscriptionClosed, err.Error())
	}
	defer stop()
	select {
	case ticker := <-received:
		optional := func(value *string) decimal.Decimal {
			if value == nil {
				return decimal.Zero
			}
			return crossExDecimal(*value)
		}
		return Ticker24h{Last: crossExDecimal(ticker.Last), Open: optional(ticker.Open24h), High: optional(ticker.High24h),
			Low: optional(ticker.Low24h), Volume: optional(ticker.Volume24h), Time: crossExMillis(ticker.Time)}, nil
	case <-ctx.Done():
		return Ticker24h{}, exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: market.meta.venue, Product: market.meta.product, Operation: "Ticker24h", SafeMessage: "no crossex ticker in time"})
	}
}

func (market *CrossExMarket) Close() error { return market.stream.Close() }

func crossExLevels(rows [][2]string) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.BookLevel{Price: crossExDecimal(row[0]), Quantity: crossExDecimal(row[1])})
	}
	return out
}

func crossExMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

var (
	_ exchange.MarketREST                   = (*CrossExMarket)(nil)
	_ exchange.PerpMarketWebSocket          = (*CrossExMarket)(nil)
	_ exchange.DerivativeReferenceWebSocket = (*CrossExMarket)(nil)
)
