package factoryclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	kraken "github.com/QuantProcessing/keeluni/sdk/kraken"
	"github.com/shopspring/decimal"
)

type krakenInstrumentLoader struct{ settings Settings }

func NewKrakenInstrumentLoader(settings Settings) exchange.InstrumentLoader {
	return krakenInstrumentLoader{settings: settings}
}

func (loader krakenInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueKraken, product: exchange.ProductPerp}
	rows, err := newKrakenSDK(loader.settings).Instruments(ctx)
	if err != nil {
		return exchange.InstrumentSnapshot{}, krakenNormalizeErr(meta, "InstrumentCatalog.Load", err)
	}
	instruments, err := krakenCatalogInstruments(rows)
	if err != nil {
		return exchange.InstrumentSnapshot{}, exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product,
			Operation: "InstrumentCatalog.Load", SafeMessage: err.Error()})
	}
	return exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: filterInstrumentsForScope(instruments, scope), LoadedAt: time.Now().UTC()}, nil
}

// krakenCatalogInstruments keeps tradeable multi-collateral linear perps.
// Their contract is one base unit; the quantity step is
// 10^-contractValueTradePrecision (a negative precision means steps of 10,
// 100, ...).
func krakenCatalogInstruments(rows []kraken.Instrument) ([]exchange.Instrument, error) {
	out := make([]exchange.Instrument, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.Linear() || !row.Tradeable || row.Suspended || row.IsExpired {
			continue
		}
		base, quote := strings.ToUpper(strings.TrimSpace(row.Base)), strings.ToUpper(strings.TrimSpace(row.Quote))
		if base == "" || quote != "USD" {
			continue
		}
		if size := numberDecimal(row.ContractSize); !size.Equal(decimal.NewFromInt(1)) {
			// Linear contracts are one base unit; anything else would need a
			// conversion the adapter does not perform.
			continue
		}
		tick := numberDecimal(row.TickSize)
		precision, err := row.ContractValueTradePrecision.Int64()
		if !tick.IsPositive() || err != nil || precision < -8 || precision > 12 {
			return nil, fmt.Errorf("kraken instrument %s rules are invalid", row.Symbol)
		}
		step := decimal.New(1, -int32(precision))
		symbol := base + "-" + quote
		if seen[symbol] {
			return nil, fmt.Errorf("kraken instruments map twice onto %s", symbol)
		}
		seen[symbol] = true
		instrument := newInstrument(exchange.VenueKraken, exchange.ProductPerp, symbol, base, quote, quote, row.Symbol, "",
			tick, step, step, exchange.OptionalDecimal{}, oneContract())
		instrument.Metadata.AssetClass = krakenAssetClass(row.Category, row.TradFi)
		instrument.Metadata.ClassificationSource = "kraken.instruments"
		if category := strings.TrimSpace(row.Category); category != "" {
			instrument.Metadata.RawCategories = exchange.NewInstrumentCategories([]string{category})
		}
		if err := instrument.Validate(); err != nil {
			return nil, fmt.Errorf("kraken instrument %s: %w", row.Symbol, err)
		}
		out = append(out, instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}

func krakenAssetClass(category string, tradfi bool) exchange.AssetClass {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "equities", "xstocks":
		return exchange.AssetClassEquity
	case "commodities":
		return exchange.AssetClassCommodity
	case "forex":
		return exchange.AssetClassFX
	case "pre-ipo":
		return exchange.AssetClassPreIPO
	case "indices":
		return exchange.AssetClassIndex
	}
	if tradfi {
		return exchange.AssetClassRWAOther
	}
	return exchange.AssetClassCrypto
}

// ---------------------------------------------------------------------------
// Funding
// ---------------------------------------------------------------------------

type krakenDerivativeSnapshotLoader struct{ settings Settings }

func NewKrakenDerivativeSnapshotLoader(settings Settings) DerivativeSnapshotLoader {
	return krakenDerivativeSnapshotLoader{settings: settings}
}

// LoadDerivativeSnapshot reads every ticker at once. Kraken funds hourly at
// the top of the hour; relativeFundingRate is the hourly fraction.
func (loader krakenDerivativeSnapshotLoader) LoadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope) (DerivativeSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueKraken, product: exchange.ProductPerp}
	sdk := newKrakenSDK(loader.settings)
	tickers, err := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueKraken, "tickers", "perpetual", sdk.Tickers)
	if err != nil {
		return DerivativeSnapshot{}, krakenNormalizeErr(meta, derivativeSnapshotOperation, err)
	}
	instruments, err := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueKraken, "instruments", "perpetual", sdk.Instruments)
	if err != nil {
		return DerivativeSnapshot{}, krakenNormalizeErr(meta, derivativeSnapshotOperation, err)
	}
	catalog, err := krakenCatalogInstruments(instruments)
	if err != nil {
		return DerivativeSnapshot{}, exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: derivativeSnapshotOperation, SafeMessage: err.Error()})
	}
	byNative := map[string]exchange.Instrument{}
	for _, instrument := range catalog {
		byNative[instrument.Identity.NativeSymbol] = instrument
	}
	next := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	entries := make([]DerivativeSnapshotEntry, 0, len(tickers))
	for _, ticker := range tickers {
		instrument, ok := byNative[ticker.Symbol]
		if !ok {
			continue
		}
		rate, err := decimalOrSkip(string(ticker.RelativeFundingRate))
		if err != nil {
			continue
		}
		entries = append(entries, DerivativeSnapshotEntry{
			Symbol: instrument.Identity.Symbol, NativeSymbol: ticker.Symbol, BaseAsset: instrument.Identity.BaseAsset, QuoteAsset: instrument.Identity.QuoteAsset,
			Rate: rate, IntervalHours: exchange.OptionalInt{Value: 1, Valid: true}, NextFundingTime: next,
			MarkPrice: fundingOptionalDecimal(string(ticker.MarkPrice)), IndexPrice: fundingOptionalDecimal(string(ticker.IndexPrice)),
			OpenInterest:         fundingOptionalNonNegative(string(ticker.OpenInterest)),
			OpenInterestNotional: fundingOpenInterestNotional(string(ticker.OpenInterest), string(ticker.MarkPrice), decimal.NewFromInt(1)),
		})
	}
	return derivativeSnapshot(scope, entries), nil
}

type krakenFundingHistoryLoader struct{ settings Settings }

func NewKrakenFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return krakenFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads one contract's settled hourly series. Kraken
// returns the full series in one response; the window is applied here.
func (loader krakenFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	if _, _, err := fundingHistoryWindow(request); err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	meta := clientMeta{venue: exchange.VenueKraken, product: exchange.ProductPerp}
	rows, err := newKrakenSDK(loader.settings).FundingRates(ctx, fundingHistoryNative(request))
	if err != nil {
		return exchange.FundingHistorySnapshot{}, krakenNormalizeErr(meta, fundingHistoryOperation, err)
	}
	points := make([]exchange.FundingHistoryPoint, 0, len(rows))
	for _, row := range rows {
		rate, err := decimalOrSkip(string(row.RelativeFundingRate))
		at, timeErr := time.Parse(time.RFC3339, row.Timestamp)
		if err != nil || timeErr != nil {
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{FundingTime: at.UTC(), Rate: rate})
	}
	snapshot := fundingHistorySnapshot(request, points)
	if limit := fundingHistoryLimit(request, len(snapshot.Points)); limit > 0 && len(snapshot.Points) > limit {
		snapshot.Points = snapshot.Points[len(snapshot.Points)-limit:]
	}
	return snapshot, nil
}
