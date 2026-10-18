package factoryclient

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

// Whole-market funding boards, one reader per venue market.
//
// Each reader turns one venue's reply into exchange.FundingQuote rows whose
// Rate, IntervalHours and NextFundingTime mean the same thing on every venue
// (see exchange/funding.go). The per-venue comments say which field answers
// which question, because the field names alone mislead on at least two
// venues.

const fundingBoardOperation = "FundingBoard.Load"

// FundingBoardReader loads one market's complete funding board.
type FundingBoardReader interface {
	LoadFundingBoard(context.Context) (exchange.FundingBoard, error)
}

// NewFundingBoardReader returns the whole-market funding reader of a served
// perpetual market, or nil when the venue has none.
func NewFundingBoardReader(scope exchange.CatalogScope, settings Settings) FundingBoardReader {
	switch scope.Venue {
	case exchange.VenueBinance:
		return &binanceFundingBoard{scope: scope, settings: settings}
	case exchange.VenueOKX:
		return okxFundingBoard{scope: scope, settings: settings}
	case exchange.VenueBybit:
		return bybitFundingBoard{scope: scope, settings: settings}
	case exchange.VenueBitget:
		return bitgetFundingBoard{scope: scope, settings: settings}
	case exchange.VenueGate:
		return &gateFundingBoard{scope: scope, settings: settings}
	case exchange.VenueLighter:
		return lighterFundingBoard{scope: scope, settings: settings}
	case exchange.VenueHyperliquid:
		return hyperliquidFundingBoard{scope: scope, settings: settings}
	}
	return nil
}

func newFundingBoard(scope exchange.CatalogScope, observedAt time.Time, quotes []exchange.FundingQuote) exchange.FundingBoard {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	for index := range quotes {
		if quotes[index].ObservedAt.IsZero() {
			quotes[index].ObservedAt = observedAt
		}
	}
	return exchange.FundingBoard{Scope: scope, ObservedAt: observedAt.UTC(), Quotes: quotes}
}

func optionalHours(hours int64) exchange.OptionalInt {
	if hours <= 0 || hours > 24 {
		return exchange.OptionalInt{}
	}
	return exchange.OptionalInt{Value: hours, Valid: true}
}

func optionalSigned(value string) exchange.OptionalDecimal {
	parsed, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: parsed, Valid: true}
}

// ---------------------------------------------------------------------------
// Binance USD-M
// ---------------------------------------------------------------------------

// binanceFundingBoard reads premiumIndex (rate, next settlement) and
// fundingInfo (period). fundingInfo lists only instruments whose period or cap
// deviates from the default, and the default period is eight hours, so an
// instrument absent from it settles every eight hours. fundingInfo changes
// rarely and is re-read every five minutes; a failed read keeps the previous
// one, and without any the period stays unknown rather than assumed.
type binanceFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings

	mu         sync.Mutex
	periods    map[string]int64
	periodsAt  time.Time
	periodsSet bool
}

const binanceDefaultFundingHours = 8

func (board *binanceFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	sdk := newBinancePerpPublicSDK(board.settings)
	rows, err := sdk.GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoard{}, binancePerpNormalizeError(fundingBoardOperation, err)
	}
	periods, known := board.fundingPeriods(ctx)
	observed := time.Now().UTC()
	quotes := make([]exchange.FundingQuote, 0, len(rows))
	for _, row := range rows {
		_, quote, ok := binanceSplitPerpSymbol(row.Symbol)
		if !ok || quote != board.scope.Settlement {
			continue // delivery futures and the other settlement
		}
		rate, err := decimalOrSkip(row.LastFundingRate)
		if err != nil {
			continue
		}
		item := exchange.FundingQuote{NativeSymbol: row.Symbol, Rate: rate}
		if row.NextFundingTime > 0 {
			item.NextFundingTime = time.UnixMilli(row.NextFundingTime).UTC()
		}
		if row.Time > 0 {
			item.ObservedAt = time.UnixMilli(row.Time).UTC()
		}
		if hours, listed := periods[row.Symbol]; listed {
			item.IntervalHours = optionalHours(hours)
		} else if known {
			item.IntervalHours = optionalHours(binanceDefaultFundingHours)
		}
		quotes = append(quotes, item)
	}
	return newFundingBoard(board.scope, observed, quotes), nil
}

func (board *binanceFundingBoard) fundingPeriods(ctx context.Context) (map[string]int64, bool) {
	board.mu.Lock()
	fresh := board.periodsSet && time.Since(board.periodsAt) < 5*time.Minute
	periods, known := board.periods, board.periodsSet
	board.mu.Unlock()
	if fresh {
		return periods, known
	}
	info, err := newBinancePerpPublicSDK(board.settings).GetFundingInfo(ctx)
	if err != nil {
		return periods, known
	}
	next := make(map[string]int64, len(info))
	for _, row := range info {
		if row.FundingIntervalHours > 0 {
			next[row.Symbol] = row.FundingIntervalHours
		}
	}
	board.mu.Lock()
	board.periods, board.periodsAt, board.periodsSet = next, time.Now(), true
	board.mu.Unlock()
	return next, true
}

// ---------------------------------------------------------------------------
// OKX
// ---------------------------------------------------------------------------

// okxFundingBoard reads funding-rate?instId=ANY. Its fields are named for the
// period being priced, not for the clock: fundingTime is the NEXT settlement
// (the one fundingRate is charged at) and nextFundingTime is the one after.
// The period is their difference, which is exact (OKX runs 2h, 4h and 8h
// swaps side by side). The reply also carries delivery futures and coin-margined
// swaps; only USDT-margined swaps belong to the served market.
type okxFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings
}

func (board okxFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	rows, err := newOKXPublicSDK(board.settings).GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoard{}, okxNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}
	suffix := "-" + board.scope.Settlement + "-SWAP"
	quotes := make([]exchange.FundingQuote, 0, len(rows))
	for _, row := range rows {
		if !stringsEqualFold(row.InstrumentType, okxSwapType) || !strings.HasSuffix(row.InstrumentID, suffix) {
			continue
		}
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue // OKX leaves the rate blank when it has none to quote
		}
		settles, _ := okxOptionalMillis(row.FundingTime)
		after, _ := okxOptionalMillis(row.NextFundingTime)
		item := exchange.FundingQuote{NativeSymbol: row.InstrumentID, Rate: rate, NextFundingTime: settles}
		if !settles.IsZero() && after.After(settles) {
			item.IntervalHours = optionalHours(roundHours(after.Sub(settles)))
		}
		if observed, err := okxOptionalMillis(row.Ts); err == nil {
			item.ObservedAt = observed
		}
		item.RateCap = optionalSigned(row.MaxFundingRate)
		item.RateFloor = optionalSigned(row.MinFundingRate)
		quotes = append(quotes, item)
	}
	return newFundingBoard(board.scope, time.Now(), quotes), nil
}

func roundHours(duration time.Duration) int64 {
	return int64(duration.Hours() + 0.5)
}

// ---------------------------------------------------------------------------
// Bybit
// ---------------------------------------------------------------------------

// bybitFundingBoard reads the linear tickers, which carry the rate, the next
// settlement and the period on every row. The linear category mixes USDT and
// USDC contracts; the symbol suffix decides which market a row belongs to
// (Bybit's USDC perpetuals are spelled BTCPERP).
type bybitFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings
}

func (board bybitFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	rows, err := loadBybitLinearTickers(ctx, board.settings)
	if err != nil {
		return exchange.FundingBoard{}, normErr(clientMeta{venue: exchange.VenueBybit, product: exchange.ProductPerp}, fundingBoardOperation, err)
	}
	quotes := make([]exchange.FundingQuote, 0, len(rows))
	for _, row := range rows {
		if bybitSettlement(row.Symbol) != board.scope.Settlement {
			continue
		}
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		item := exchange.FundingQuote{
			NativeSymbol:    row.Symbol,
			Rate:            rate,
			IntervalHours:   fundingIntervalHours(row.FundingIntervalHour),
			NextFundingTime: fundingUnixMillis(row.NextFundingTime),
			ObservedAt:      fundingUnixMillis(row.Time),
		}
		quotes = append(quotes, item)
	}
	return newFundingBoard(board.scope, time.Now(), quotes), nil
}

func bybitSettlement(symbol string) string {
	upper := strings.ToUpper(symbol)
	switch {
	case strings.HasSuffix(upper, "USDT"):
		return "USDT"
	case strings.HasSuffix(upper, "PERP"), strings.HasSuffix(upper, "USDC"):
		return "USDC"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Bitget
// ---------------------------------------------------------------------------

// bitgetFundingBoard reads current-fund-rate for a whole product type: rate,
// period and next settlement (nextUpdate) on every row, plus the venue's
// cap and floor. The tickers endpoint omits the period and the clock, so it is
// not used for funding.
type bitgetFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings
}

func (board bitgetFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	productType := board.scope.Settlement + "-FUTURES"
	rows, err := newBitgetPublicSDK(board.settings).GetCurrentFundRate(ctx, "", productType)
	if err != nil {
		return exchange.FundingBoard{}, normErr(clientMeta{venue: exchange.VenueBitget, product: exchange.ProductPerp}, fundingBoardOperation, err)
	}
	quotes := make([]exchange.FundingQuote, 0, len(rows))
	var observed time.Time
	for _, row := range rows {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		if row.RequestTime > 0 {
			observed = time.UnixMilli(row.RequestTime).UTC()
		}
		quotes = append(quotes, bitgetQuote(row, rate))
	}
	return newFundingBoard(board.scope, observed, quotes), nil
}

func bitgetQuote(row bitget.CurrentFundRateEntry, rate decimal.Decimal) exchange.FundingQuote {
	return exchange.FundingQuote{
		NativeSymbol:    row.Symbol,
		Rate:            rate,
		IntervalHours:   fundingIntervalHours(row.FundingRateInterval),
		NextFundingTime: fundingUnixMillis(row.NextUpdate),
		RateCap:         optionalSigned(row.MaxFundingRate),
		RateFloor:       optionalSigned(row.MinFundingRate),
	}
}

// ---------------------------------------------------------------------------
// Gate
// ---------------------------------------------------------------------------

// gateFundingBoard joins two surfaces: tickers carry the live rate, contracts
// carry the period (seconds) and the next settlement. Tickers are re-read on
// every call; contracts (1.3 MB, uncompressed) every five minutes, keeping the
// last good copy on failure. A contract that is not trading has no clock.
type gateFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings

	mu          sync.Mutex
	contracts   map[string]gateContractClock
	contractsAt time.Time
}

type gateContractClock struct {
	hours int64
	next  time.Time
}

func (board *gateFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	sdk, _ := newGateSDK("", "", board.settings)
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	settle := strings.ToLower(board.scope.Settlement)
	tickers, err := sdk.ListFuturesTickers(ctx, settle, "")
	if err != nil {
		return exchange.FundingBoard{}, gateNormalizeErr(meta, fundingBoardOperation, err)
	}
	clocks := board.contractClocks(ctx, sdk, settle)
	now := time.Now().UTC()
	quotes := make([]exchange.FundingQuote, 0, len(tickers))
	for _, row := range tickers {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		item := exchange.FundingQuote{NativeSymbol: row.Contract, Rate: rate}
		if clock, ok := clocks[row.Contract]; ok {
			item.IntervalHours = optionalHours(clock.hours)
			item.NextFundingTime = rollForward(clock.next, clock.hours, now)
		}
		quotes = append(quotes, item)
	}
	return newFundingBoard(board.scope, now, quotes), nil
}

func (board *gateFundingBoard) contractClocks(ctx context.Context, sdk *gate.Client, settle string) map[string]gateContractClock {
	board.mu.Lock()
	current, at := board.contracts, board.contractsAt
	board.mu.Unlock()
	if current != nil && time.Since(at) < 5*time.Minute {
		return current
	}
	contracts, err := sdk.ListFuturesContracts(ctx, settle)
	if err != nil {
		return current
	}
	next := make(map[string]gateContractClock, len(contracts))
	for _, contract := range contracts {
		if contract.Status != "" && contract.Status != "trading" {
			continue
		}
		if contract.FundingInterval <= 0 || contract.FundingInterval%3600 != 0 || contract.FundingNextApply <= 0 {
			continue
		}
		next[contract.Name] = gateContractClock{
			hours: contract.FundingInterval / 3600,
			next:  time.Unix(int64(contract.FundingNextApply), 0).UTC(),
		}
	}
	board.mu.Lock()
	board.contracts, board.contractsAt = next, time.Now()
	board.mu.Unlock()
	return next
}

// rollForward moves a cached settlement time past now by whole periods, so a
// clock read minutes ago does not point into the past after a settlement.
func rollForward(next time.Time, hours int64, now time.Time) time.Time {
	if next.IsZero() || hours <= 0 {
		return next
	}
	period := time.Duration(hours) * time.Hour
	for !next.After(now) {
		next = next.Add(period)
	}
	return next
}

// ---------------------------------------------------------------------------
// Lighter
// ---------------------------------------------------------------------------

// lighterFundingBoard reads /funding-rates, a comparison feed that also relays
// other venues' rates (dropped here). Lighter settles every hour; this
// endpoint quotes the rate as an eight-hour equivalent (eight hourly
// settlements), so the per-settlement fraction is the quote divided by eight.
// Verified against the live market_stats stream, whose current_funding_rate is
// a percentage per hour: board = 8 × stream / 100 on 200 of 213 markets, the
// rest being zero-rate or moved between the two reads.
type lighterFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings
}

func (board lighterFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	rows, err := newLighterPublicSDK(board.settings).GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoard{}, lighterNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}
	now := time.Now().UTC()
	eight := decimal.NewFromInt(lighterFundingQuoteHours)
	quotes := make([]exchange.FundingQuote, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Symbol == "" || !stringsEqualFold(row.Exchange, "lighter") {
			continue
		}
		quotes = append(quotes, exchange.FundingQuote{
			NativeSymbol:    row.Symbol,
			NativeID:        strconv.Itoa(row.MarketId),
			Rate:            decimal.NewFromFloat(row.Rate).Div(eight),
			IntervalHours:   optionalHours(1),
			NextFundingTime: nextHour(now),
		})
	}
	return newFundingBoard(board.scope, now, quotes), nil
}

func nextHour(now time.Time) time.Time {
	return now.UTC().Truncate(time.Hour).Add(time.Hour)
}

// ---------------------------------------------------------------------------
// Hyperliquid (REST fallback; the gateway prefers the stream below)
// ---------------------------------------------------------------------------

// hyperliquidFundingBoard reads metaAndAssetCtxs of the primary dex only. The
// whole-venue board, including every HIP-3 dex, comes from the stream
// (HyperliquidFundingStream); polling all dexes over REST would spend most of
// the venue's REST weight on funding.
type hyperliquidFundingBoard struct {
	scope    exchange.CatalogScope
	settings Settings
}

func (board hyperliquidFundingBoard) LoadFundingBoard(ctx context.Context) (exchange.FundingBoard, error) {
	full, err := hyperliquidperp.NewClient(newHyperliquidPublicSDK(board.settings)).GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return exchange.FundingBoard{}, hlNormalizeQueryErr(exchange.ProductPerp, fundingBoardOperation, err, nil)
	}
	if full == nil || len(full.Meta.Universe) != len(full.AssetCtxs) {
		return exchange.FundingBoard{}, hlMalformed(exchange.ProductPerp, fundingBoardOperation, "universe and asset contexts differ in length")
	}
	now := time.Now().UTC()
	quotes := make([]exchange.FundingQuote, 0, len(full.Meta.Universe))
	for index, universe := range full.Meta.Universe {
		if !hyperliquidValidPerpCoin(universe.Name, "") {
			continue
		}
		rate, err := decimalOrSkip(full.AssetCtxs[index].Funding)
		if err != nil {
			continue
		}
		quotes = append(quotes, hyperliquidQuote(universe.Name, hyperliquidAssetID(0, index), rate, now))
	}
	return newFundingBoard(board.scope, now, quotes), nil
}

// hyperliquidQuote: Hyperliquid's funding is an hourly fraction settled on
// every hour, on the primary dex and on HIP-3 dexes alike.
func hyperliquidQuote(coin string, assetID int, rate decimal.Decimal, now time.Time) exchange.FundingQuote {
	return exchange.FundingQuote{
		NativeSymbol:    coin,
		NativeID:        strconv.Itoa(assetID),
		Rate:            rate,
		IntervalHours:   optionalHours(1),
		NextFundingTime: nextHour(now),
		ObservedAt:      now,
	}
}
