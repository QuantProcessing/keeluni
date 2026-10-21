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

// Deribit options. Three settlement families share one account:
//
//	BTC-27NOV26-73000-P          inverse: premium and P&L in BTC, 1 BTC per contract
//	ETH-27NOV26-2500-C           inverse: premium and P&L in ETH
//	SOL_USDC-23OCT26-75-C        linear: premium and P&L in USDC, contract_size 10 SOL
//
// Canonical symbol: BASE-QUOTE-YYYYMMDD-STRIKE-C|P with the quote the
// premium currency ("BTC-BTC-..." would be noise, so inverse options use
// USD, the strike currency, and say Inverse in their terms):
//
//	BTC-USD-20261127-73000-P     (Deribit BTC-27NOV26-73000-P)
//	SOL-USDC-20261023-75-C       (Deribit SOL_USDC-23OCT26-75-C)
//	AVAX-USDC-20261020-8.2-C     (Deribit AVAX_USDC-20OCT26-8d2-C)
//
// Quantities are in the underlying: Deribit's amount for every option kind
// is already in base units (min_trade_amount is the step), so no conversion.

var deribitOptionCurrencies = []string{"BTC", "ETH", "USDC"}

const deribitOptionSymbolDate = "20060102"

type deribitOptionLoader struct{ settings Settings }

func NewDeribitOptionInstrumentLoader(settings Settings) exchange.InstrumentLoader {
	return deribitOptionLoader{settings: settings}
}

func (loader deribitOptionLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueDeribit, product: exchange.ProductOption}
	sdk := newDeribitSDK(loader.settings)
	var rows []deribit.Instrument
	for _, currency := range deribitOptionCurrencies {
		batch, err := sdk.InstrumentsOf(ctx, currency, deribit.KindOption)
		if err != nil {
			return exchange.InstrumentSnapshot{}, deribitNormalizeErr(meta, "InstrumentCatalog.Load", err)
		}
		rows = append(rows, batch...)
	}
	instruments, err := deribitOptionInstruments(rows)
	if err != nil {
		return exchange.InstrumentSnapshot{}, deribitMalformed(meta, "InstrumentCatalog.Load", err.Error())
	}
	return exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: instruments, LoadedAt: time.Now().UTC()}, nil
}

func deribitOptionInstruments(rows []deribit.Instrument) ([]exchange.Instrument, error) {
	out := make([]exchange.Instrument, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.Option() {
			continue
		}
		instrument, err := deribitOptionInstrument(row)
		if err != nil {
			return nil, fmt.Errorf("deribit option %s: %w", row.InstrumentName, err)
		}
		if seen[instrument.Identity.Symbol] {
			return nil, fmt.Errorf("deribit options map twice onto %s", instrument.Identity.Symbol)
		}
		seen[instrument.Identity.Symbol] = true
		out = append(out, instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}

func deribitOptionInstrument(row deribit.Instrument) (exchange.Instrument, error) {
	base := strings.ToUpper(strings.TrimSpace(row.BaseCurrency))
	settle := strings.ToUpper(strings.TrimSpace(row.SettlementCurrency))
	strike := numberDecimal(row.Strike)
	// min_trade_amount is both the minimum and the amount step (Deribit's
	// quantity increment); contract_size only converts amount to contracts.
	tick, minimum := numberDecimal(row.TickSize), numberDecimal(row.MinTradeAmount)
	step := minimum
	if base == "" || settle == "" || !strike.IsPositive() || !tick.IsPositive() || !step.IsPositive() || row.ExpirationTimestamp <= 0 {
		return exchange.Instrument{}, fmt.Errorf("terms are incomplete")
	}
	var kind exchange.OptionKind
	switch row.OptionType {
	case "call":
		kind = exchange.OptionCall
	case "put":
		kind = exchange.OptionPut
	default:
		return exchange.Instrument{}, fmt.Errorf("option type %q is unknown", row.OptionType)
	}
	inverse := row.InstrumentType == "reversed"
	quote := settle
	if inverse {
		// Strike currency; the premium is in the coin.
		quote = strings.ToUpper(strings.TrimSpace(row.CounterCurrency))
		if quote == "" {
			quote = "USD"
		}
	}
	if minimum.LessThan(step) {
		minimum = step
	}
	expiry := time.UnixMilli(row.ExpirationTimestamp).UTC()
	symbol := DeribitOptionSymbol(base, quote, expiry, strike, kind)
	instrument := newInstrument(exchange.VenueDeribit, exchange.ProductOption, symbol, base, quote, settle, row.InstrumentName, "",
		tick, step, minimum, exchange.OptionalDecimal{}, oneContract())
	instrument.Metadata.MarketFamily = exchange.MarketFamilyOption
	instrument.Metadata.AssetClass = deribitAssetClass(row.UnderlyingType)
	instrument.Metadata.ClassificationSource = "deribit.instruments"
	if len(row.TickSizeSteps) > 0 {
		from, coarse := numberDecimal(row.TickSizeSteps[0].AbovePrice), numberDecimal(row.TickSizeSteps[0].TickSize)
		if from.IsPositive() && coarse.IsPositive() {
			instrument.Rules.CoarseTickFrom = exchange.OptionalDecimal{Value: from, Valid: true}
			instrument.Rules.CoarseTick = exchange.OptionalDecimal{Value: coarse, Valid: true}
		}
	}
	instrument.Option = exchange.OptionTerms{
		Underlying: base, Kind: kind, Strike: strike, Expiry: expiry, PremiumAsset: settle, Inverse: inverse,
		Index: row.PriceIndex, European: true, CashSettled: true,
	}
	if err := instrument.Validate(); err != nil {
		return exchange.Instrument{}, err
	}
	return instrument, nil
}

// DeribitOptionSymbol is the canonical option symbol (see the file comment).
func DeribitOptionSymbol(base, quote string, expiry time.Time, strike decimal.Decimal, kind exchange.OptionKind) string {
	letter := "C"
	if kind == exchange.OptionPut {
		letter = "P"
	}
	return base + "-" + quote + "-" + expiry.UTC().Format(deribitOptionSymbolDate) + "-" + strike.String() + "-" + letter
}

// ---------------------------------------------------------------------------
// Spot
// ---------------------------------------------------------------------------

type deribitSpotLoader struct{ settings Settings }

func NewDeribitSpotInstrumentLoader(settings Settings) exchange.InstrumentLoader {
	return deribitSpotLoader{settings: settings}
}

func (loader deribitSpotLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	meta := clientMeta{venue: exchange.VenueDeribit, product: exchange.ProductSpot}
	rows, err := newDeribitSDK(loader.settings).InstrumentsOf(ctx, deribit.CurrencyAny, deribit.KindSpot)
	if err != nil {
		return exchange.InstrumentSnapshot{}, deribitNormalizeErr(meta, "InstrumentCatalog.Load", err)
	}
	instruments, err := deribitSpotInstruments(rows)
	if err != nil {
		return exchange.InstrumentSnapshot{}, deribitMalformed(meta, "InstrumentCatalog.Load", err.Error())
	}
	return exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: instruments, LoadedAt: time.Now().UTC()}, nil
}

// deribitSpotInstruments keeps open pairs. Deribit names them BASE_QUOTE;
// the canonical symbol is BASE-QUOTE. Amounts are base units.
func deribitSpotInstruments(rows []deribit.Instrument) ([]exchange.Instrument, error) {
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if !row.Spot() {
			continue
		}
		base, quote := strings.ToUpper(row.BaseCurrency), strings.ToUpper(row.QuoteCurrency)
		tick, minimum := numberDecimal(row.TickSize), numberDecimal(row.MinTradeAmount)
		step := minimum
		if base == "" || quote == "" || !tick.IsPositive() || !step.IsPositive() {
			return nil, fmt.Errorf("deribit spot %s rules are invalid", row.InstrumentName)
		}
		if minimum.LessThan(step) {
			minimum = step
		}
		instrument := newInstrument(exchange.VenueDeribit, exchange.ProductSpot, base+"-"+quote, base, quote, "", row.InstrumentName, "",
			tick, step, minimum, exchange.OptionalDecimal{}, oneContract())
		instrument.Metadata.AssetClass = exchange.AssetClassCrypto
		instrument.Metadata.ClassificationSource = "deribit.instruments"
		if row.IsCBERouted {
			instrument.Metadata.RawCategories = exchange.NewInstrumentCategories([]string{"cbe_routed"})
		}
		if err := instrument.Validate(); err != nil {
			return nil, fmt.Errorf("deribit spot %s: %w", row.InstrumentName, err)
		}
		out = append(out, instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}
