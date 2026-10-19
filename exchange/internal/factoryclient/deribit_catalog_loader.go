package factoryclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	deribit "github.com/QuantProcessing/keeluni/sdk/deribit"
	"github.com/shopspring/decimal"
)

type deribitInstrumentLoader struct{ settings Settings }

func NewDeribitInstrumentLoader(settings Settings) exchange.InstrumentLoader {
	return deribitInstrumentLoader{settings: settings}
}

func (loader deribitInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueDeribit, product: exchange.ProductPerp}
	rows, err := newDeribitSDK(loader.settings).Instruments(ctx, deribitCurrency)
	if err != nil {
		return exchange.InstrumentSnapshot{}, deribitNormalizeErr(meta, "InstrumentCatalog.Load", err)
	}
	instruments, err := deribitCatalogInstruments(rows)
	if err != nil {
		return exchange.InstrumentSnapshot{}, deribitMalformed(meta, "InstrumentCatalog.Load", err.Error())
	}
	return exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: filterInstrumentsForScope(instruments, scope), LoadedAt: time.Now().UTC()}, nil
}

// deribitCatalogInstruments keeps active USDC linear perpetuals. The amount
// step is the contract size and the minimum is min_trade_amount. Tick size
// steps (a coarser tick above a price) are not modelled; the base tick is
// the finest Deribit accepts and is used.
func deribitCatalogInstruments(rows []deribit.Instrument) ([]exchange.Instrument, error) {
	out := make([]exchange.Instrument, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.LinearPerpetual() || !strings.EqualFold(row.SettlementCurrency, deribitCurrency) {
			continue
		}
		base := strings.ToUpper(strings.TrimSpace(row.BaseCurrency))
		tick, step, minimum := numberDecimal(row.TickSize), numberDecimal(row.ContractSize), numberDecimal(row.MinTradeAmount)
		if base == "" || !tick.IsPositive() || !step.IsPositive() {
			return nil, fmt.Errorf("deribit instrument %s rules are invalid", row.InstrumentName)
		}
		if minimum.LessThan(step) {
			minimum = step
		}
		symbol := base + "-" + deribitCurrency
		if seen[symbol] {
			return nil, fmt.Errorf("deribit instruments map twice onto %s", symbol)
		}
		seen[symbol] = true
		instrument := newInstrument(exchange.VenueDeribit, exchange.ProductPerp, symbol, base, deribitCurrency, deribitCurrency, row.InstrumentName, "",
			tick, step, minimum, exchange.OptionalDecimal{}, oneContract())
		instrument.Metadata.AssetClass = deribitAssetClass(row.UnderlyingType)
		instrument.Metadata.ClassificationSource = "deribit.instruments"
		if row.UnderlyingType != "" {
			instrument.Metadata.RawAssetType = row.UnderlyingType
		}
		if err := instrument.Validate(); err != nil {
			return nil, fmt.Errorf("deribit instrument %s: %w", row.InstrumentName, err)
		}
		out = append(out, instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}

func deribitAssetClass(underlying string) exchange.AssetClass {
	switch strings.ToLower(strings.TrimSpace(underlying)) {
	case "crypto":
		return exchange.AssetClassCrypto
	case "crypto_index":
		return exchange.AssetClassIndex
	case "equity", "equity_etf":
		return exchange.AssetClassEquity
	case "commodity":
		return exchange.AssetClassCommodity
	case "preipo":
		return exchange.AssetClassPreIPO
	}
	return exchange.AssetClassUnknown
}

// ---------------------------------------------------------------------------
// Funding
// ---------------------------------------------------------------------------

type deribitDerivativeSnapshotLoader struct{ settings Settings }

func NewDeribitDerivativeSnapshotLoader(settings Settings) DerivativeSnapshotLoader {
	return deribitDerivativeSnapshotLoader{settings: settings}
}

// LoadDerivativeSnapshot reads every USDC future's book summary in one call.
// funding_8h is converted to the hourly rate.
func (loader deribitDerivativeSnapshotLoader) LoadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope) (DerivativeSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueDeribit, product: exchange.ProductPerp}
	sdk := newDeribitSDK(loader.settings)
	rows, err := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueDeribit, "book_summary", "perpetual", func(callCtx context.Context) ([]deribit.BookSummary, error) {
		return sdk.BookSummaries(callCtx, deribitCurrency)
	})
	if err != nil {
		return DerivativeSnapshot{}, deribitNormalizeErr(meta, derivativeSnapshotOperation, err)
	}
	next := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	entries := make([]DerivativeSnapshotEntry, 0, len(rows))
	for _, row := range rows {
		if !deribitServed(row.InstrumentName) || row.Funding8h == "" {
			continue
		}
		base := strings.ToUpper(row.BaseCurrency)
		entries = append(entries, DerivativeSnapshotEntry{
			Symbol: base + "-" + deribitCurrency, NativeSymbol: row.InstrumentName, BaseAsset: base, QuoteAsset: deribitCurrency,
			Rate: deribitHourly(row.Funding8h), IntervalHours: exchange.OptionalInt{Value: 1, Valid: true}, NextFundingTime: next,
			MarkPrice:            fundingOptionalDecimal(string(row.MarkPrice)),
			OpenInterest:         fundingOptionalNonNegative(string(row.OpenInterest)),
			OpenInterestNotional: fundingOpenInterestNotional(string(row.OpenInterest), string(row.MarkPrice), decimal.NewFromInt(1)),
		})
	}
	return derivativeSnapshot(scope, entries), nil
}

type deribitFundingHistoryLoader struct{ settings Settings }

func NewDeribitFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return deribitFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads the hourly interest series (interest_1h).
func (loader deribitFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	since, until, err := fundingHistoryWindow(request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	if until == 0 {
		until = time.Now().UnixMilli()
	}
	if since == 0 {
		since = until - int64(30*24*time.Hour/time.Millisecond)
	}
	meta := clientMeta{venue: exchange.VenueDeribit, product: exchange.ProductPerp}
	rows, err := newDeribitSDK(loader.settings).FundingRateHistory(ctx, fundingHistoryNative(request), since, until)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, deribitNormalizeErr(meta, fundingHistoryOperation, err)
	}
	points := make([]exchange.FundingHistoryPoint, 0, len(rows))
	for _, row := range rows {
		rate, err := decimalOrSkip(string(row.Interest1h))
		if err != nil {
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{FundingTime: fundingHistoryMillisTime(row.Timestamp), Rate: rate})
	}
	snapshot := fundingHistorySnapshot(request, points)
	if limit := fundingHistoryLimit(request, len(snapshot.Points)); limit > 0 && len(snapshot.Points) > limit {
		snapshot.Points = snapshot.Points[len(snapshot.Points)-limit:]
	}
	return snapshot, nil
}
