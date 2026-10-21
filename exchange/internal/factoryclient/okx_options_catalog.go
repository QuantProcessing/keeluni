package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/sdk/okx"
	"github.com/shopspring/decimal"
)

// OKX options. Every underlying (BTC-USD, ETH-USD, SOL-USD, XAU-USD) is
// listed in up to two instrument families, one account trades them all:
//
//	BTC-USD-261030-82000-C       coin-margined: premium and P&L in BTC
//	BTC-USD_UM-261106-82000-C    USD-margined: premium and P&L in USD (USDⓈ)
//
// Canonical symbol, as Deribit's (BASE-QUOTE-YYYYMMDD-STRIKE-C|P, QUOTE the
// strike currency), except that both families strike in USD and list ~400
// identical terms. The coin-margined family keeps QUOTE = USD, matching
// Deribit's inverse options; the USD-margined family writes USDM in the
// symbol (its quote asset stays USD, the strike currency):
//
//	BTC-USD-20261030-82000-C     (OKX BTC-USD-261030-82000-C)
//	BTC-USDM-20261106-82000-C    (OKX BTC-USD_UM-261106-82000-C)
//
// Quantities are in the underlying: an order of sz contracts is
// sz × ctVal × ctMult underlying (BTC 0.01, ETH 0.1 or 0.01, SOL 0.1).
// Prices are per one unit of underlying, as OKX quotes them.

const (
	okxUSDMarginedSuffix = "_UM"
	okxUSDMarginedQuote  = "USDM"
)

type okxOptionLoader struct{ settings Settings }

func NewOKXOptionInstrumentLoader(settings Settings) exchange.InstrumentLoader {
	return okxOptionLoader{settings: settings}
}

func okxSDK(settings Settings) *okx.Client {
	sdk := okx.NewClient()
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func (loader okxOptionLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	const operation = "InstrumentCatalog.Load"
	sdk := okxSDK(loader.settings)
	underlyings, err := sdk.GetUnderlyings(ctx, okxOptionType)
	if err != nil {
		return exchange.InstrumentSnapshot{}, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	bands, err := sdk.GetInstrumentTickBands(ctx, okxOptionType)
	if err != nil {
		return exchange.InstrumentSnapshot{}, okxNormalizeErr(exchange.ProductOption, operation, err)
	}
	var rows []okx.Instrument
	for _, underlying := range underlyings {
		batch, err := sdk.GetInstrumentsByUnderlying(ctx, okxOptionType, underlying)
		if err != nil {
			return exchange.InstrumentSnapshot{}, okxNormalizeErr(exchange.ProductOption, operation, err)
		}
		rows = append(rows, batch...)
	}
	instruments, err := okxOptionInstruments(rows, bands)
	if err != nil {
		return exchange.InstrumentSnapshot{}, okxMalformed(exchange.ProductOption, operation, err.Error())
	}
	return exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: filterInstrumentsForScope(instruments, scope), LoadedAt: time.Now().UTC()}, nil
}

func okxOptionInstruments(rows []okx.Instrument, bands []okx.InstrumentTickBands) ([]exchange.Instrument, error) {
	byFamily := make(map[string][]okx.TickBand, len(bands))
	for _, band := range bands {
		byFamily[band.InstFamily] = band.TickBand
	}
	out := make([]exchange.Instrument, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.InstType != okxOptionType {
			return nil, errors.New("non-OPTION row in OPTION response")
		}
		if row.State != "live" {
			continue
		}
		instrument, err := okxOptionInstrument(row, byFamily[row.InstFamily])
		if err != nil {
			return nil, fmt.Errorf("okx option %s: %w", row.InstId, err)
		}
		if seen[instrument.Identity.Symbol] {
			return nil, fmt.Errorf("okx options map twice onto %s", instrument.Identity.Symbol)
		}
		seen[instrument.Identity.Symbol] = true
		out = append(out, instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.Symbol < out[j].Identity.Symbol })
	return out, nil
}

func okxOptionInstrument(row okx.Instrument, bands []okx.TickBand) (exchange.Instrument, error) {
	parts := strings.Split(row.InstId, "-")
	if len(parts) != 5 || row.InstFamily != parts[0]+"-"+parts[1] {
		return exchange.Instrument{}, errors.New("instrument id is not FAMILY-YYMMDD-STRIKE-C|P")
	}
	base := strings.ToUpper(row.CtValCcy)
	if base == "" || base != parts[0] {
		return exchange.Instrument{}, errors.New("contract value currency does not match the underlying")
	}
	usdMargined := strings.HasSuffix(row.InstFamily, okxUSDMarginedSuffix)
	settle := strings.ToUpper(row.SettleCcy)
	switch {
	case usdMargined && settle != "USD":
		return exchange.Instrument{}, fmt.Errorf("USD-margined option settles in %q", row.SettleCcy)
	case !usdMargined && settle != base:
		return exchange.Instrument{}, fmt.Errorf("coin-margined option settles in %q", row.SettleCcy)
	}
	strike, err := okxPositiveDecimal(row.Stk)
	if err != nil {
		return exchange.Instrument{}, errors.New("invalid strike")
	}
	ctVal, err := okxPositiveDecimal(row.CtVal)
	if err != nil {
		return exchange.Instrument{}, errors.New("invalid contract value")
	}
	ctMult, err := okxPositiveDecimal(row.CtMult)
	if err != nil {
		return exchange.Instrument{}, errors.New("invalid contract multiplier")
	}
	lot, err := okxPositiveDecimal(row.LotSz)
	if err != nil {
		return exchange.Instrument{}, errors.New("invalid lot size")
	}
	minimum, err := okxPositiveDecimal(row.MinSz)
	if err != nil {
		return exchange.Instrument{}, errors.New("invalid min size")
	}
	tick, err := okxPositiveDecimal(row.TickSz)
	if err != nil {
		return exchange.Instrument{}, errors.New("invalid tick size")
	}
	expiry, err := okxMillis(row.ExpTime)
	if err != nil || expiry.IsZero() {
		return exchange.Instrument{}, errors.New("invalid expiry")
	}
	nativeID, err := okxInstrumentNativeID(row)
	if err != nil {
		return exchange.Instrument{}, err
	}
	var kind exchange.OptionKind
	switch row.OptType {
	case "C":
		kind = exchange.OptionCall
	case "P":
		kind = exchange.OptionPut
	default:
		return exchange.Instrument{}, fmt.Errorf("option type %q is unknown", row.OptType)
	}
	contract := ctVal.Mul(ctMult)
	// Both families strike in USD (the quote asset); only the symbol tells
	// them apart.
	symbolQuote, premium := "USD", base
	if usdMargined {
		symbolQuote, premium = okxUSDMarginedQuote, "USD"
	}
	symbol := DeribitOptionSymbol(base, symbolQuote, expiry, strike, kind)
	instrument := newInstrument(exchange.VenueOKX, exchange.ProductOption, symbol, base, "USD", settle, row.InstId, nativeID,
		tick, lot.Mul(contract), minimum.Mul(contract), exchange.OptionalDecimal{}, exchange.OptionalDecimal{Value: contract, Valid: true})
	instrument.Metadata.MarketFamily = exchange.MarketFamilyOption
	instrument.Metadata.AssetClass = exchange.AssetClassCrypto
	if base == "XAU" || base == "XAG" {
		instrument.Metadata.AssetClass = exchange.AssetClassCommodity
	}
	instrument.Metadata.ClassificationSource = "okx.instruments"
	if from, coarse, ok := okxCoarseTick(tick, bands); ok {
		instrument.Rules.CoarseTickFrom = exchange.OptionalDecimal{Value: from, Valid: true}
		instrument.Rules.CoarseTick = exchange.OptionalDecimal{Value: coarse, Valid: true}
	}
	instrument.Option = exchange.OptionTerms{
		Underlying: base, Kind: kind, Strike: strike, Expiry: expiry, PremiumAsset: premium, Inverse: !usdMargined,
		Index: row.Uly, European: true, CashSettled: true,
	}
	if err := instrument.Validate(); err != nil {
		return exchange.Instrument{}, err
	}
	return instrument, nil
}

// okxCoarseTick reads the first band whose tick is coarser than the
// instrument's: OKX option prices move in tickSz below that band's floor
// and in its tick above (BTC coin-margined: 0.0001 below 0.005, 0.0005
// above).
func okxCoarseTick(fine decimal.Decimal, bands []okx.TickBand) (decimal.Decimal, decimal.Decimal, bool) {
	for _, band := range bands {
		tick, err := okxPositiveDecimal(band.TickSz)
		if err != nil || !tick.GreaterThan(fine) {
			continue
		}
		from, err := okxPositiveDecimal(band.MinPx)
		if err != nil {
			continue
		}
		return from, tick, true
	}
	return decimal.Zero, decimal.Zero, false
}

// okxOptionFamily is the instrument family of a native option id:
// "BTC-USD_UM-261106-82000-C" → "BTC-USD_UM".
func okxOptionFamily(native string) string {
	parts := strings.SplitN(native, "-", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[0] + "-" + parts[1]
}

func okxValidateOptionInstrument(instrument exchange.Instrument) error {
	if err := instrument.Validate(); err != nil {
		return err
	}
	identity := instrument.Identity
	if identity.Venue != exchange.VenueOKX || identity.Product != exchange.ProductOption {
		return errors.New("instrument must be an OKX OPTION")
	}
	parts := strings.Split(identity.NativeSymbol, "-")
	if len(parts) != 5 || (parts[4] != "C" && parts[4] != "P") || parts[0] != identity.BaseAsset {
		return errors.New("instrument must be an exact OKX option FAMILY-YYMMDD-STRIKE-C|P")
	}
	if !instrument.Encoding.ContractMultiplier.Valid || !instrument.Encoding.ContractMultiplier.Value.IsPositive() {
		return errors.New("OKX option contract size is missing")
	}
	return nil
}
