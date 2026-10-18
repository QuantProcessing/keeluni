package factoryclient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/shopspring/decimal"
)

// CrossEx brokers orders to other venues' order books. An instrument reached
// through CrossEx keeps the identity of the underlying venue instrument — the
// same order book, the same gateway instrument id — while its native symbol
// becomes the CrossEx symbol and its trading rules become CrossEx's.

// crossExMarket is one native perp market CrossEx can reach, keyed by the
// CrossEx exchange_type and settlement.
type crossExMarket struct {
	exchangeType string
	venue        exchange.Venue
	settlement   string
	// native returns the underlying venue's native symbol for a CrossEx row.
	native func(row crossex.Symbol) string
}

// crossExMarkets lists the reachable markets. Every one has a native
// counterpart, whose catalog supplies identity; OKX USDC perps are absent
// because CrossEx quotes them in contracts of unknown size.
var crossExMarkets = []crossExMarket{
	{"BINANCE", exchange.VenueBinance, "USDT", func(row crossex.Symbol) string { return row.Base() + "USDT" }},
	{"BINANCE", exchange.VenueBinance, "USDC", func(row crossex.Symbol) string { return row.Base() + "USDC" }},
	{"OKX", exchange.VenueOKX, "USDT", func(row crossex.Symbol) string { return row.Base() + "-USDT-SWAP" }},
	{"BYBIT", exchange.VenueBybit, "USDT", func(row crossex.Symbol) string { return row.Base() + "USDT" }},
	{"BYBIT", exchange.VenueBybit, "USDC", func(row crossex.Symbol) string { return row.Base() + "PERP" }},
	{"GATE", exchange.VenueGate, "USDT", func(row crossex.Symbol) string { return row.Base() + "_USDT" }},
	{"HYPERLIQUID", exchange.VenueHyperliquid, "USDC", func(row crossex.Symbol) string {
		// Builder-deployed markets carry their dex in the tag.
		if dex := strings.TrimSpace(row.Tag); dex != "" {
			return dex + ":" + row.Base()
		}
		return row.Base()
	}},
	{"LIGHTER", exchange.VenueLighter, "USDC", func(row crossex.Symbol) string { return row.Base() }},
	{"KRAKEN", exchange.VenueKraken, "USD", func(row crossex.Symbol) string { return "PF_" + krakenNativeBase(row.Base()) + "USD" }},
	{"DERIBIT", exchange.VenueDeribit, "USDC", func(row crossex.Symbol) string { return row.Base() + "_USDC-PERPETUAL" }},
}

// krakenNativeBase spells a CrossEx base as Kraken does: bitcoin is XBT.
func krakenNativeBase(base string) string {
	if base == "BTC" {
		return "XBT"
	}
	return base
}

// CrossExMarketRoute names one reachable native market.
type CrossExMarketRoute struct {
	Venue      exchange.Venue
	Settlement string
}

// CrossExReachableMarkets is the fixed set of native perp markets a CrossEx
// account can trade.
func CrossExReachableMarkets() []CrossExMarketRoute {
	out := make([]CrossExMarketRoute, 0, len(crossExMarkets))
	for _, market := range crossExMarkets {
		out = append(out, CrossExMarketRoute{Venue: market.venue, Settlement: market.settlement})
	}
	return out
}

func crossExMarketFor(row crossex.Symbol) (crossExMarket, bool) {
	for _, market := range crossExMarkets {
		if market.exchangeType == row.ExchangeType && market.settlement == row.Quote() {
			return market, true
		}
	}
	return crossExMarket{}, false
}

// CrossExMapping is the result of matching CrossEx rules to native catalogs.
type CrossExMapping struct {
	// Instruments are tradable through CrossEx: native identity, CrossEx
	// native symbol and CrossEx rules.
	Instruments []exchange.Instrument
	// Unmatched counts live CrossEx perp rows per exchange_type that have no
	// native counterpart in the supplied catalogs.
	Unmatched map[string]int
}

// MapCrossExInstruments matches live CrossEx futures rows to native perp
// instruments. natives may mix venues; only reachable markets are consulted.
func MapCrossExInstruments(rows []crossex.Symbol, natives []exchange.Instrument) (CrossExMapping, error) {
	type nativeKey struct {
		venue  exchange.Venue
		settle string
		symbol string
	}
	byNative := make(map[nativeKey]exchange.Instrument, len(natives))
	for _, instrument := range natives {
		identity := instrument.Identity
		if identity.Product != exchange.ProductPerp {
			continue
		}
		byNative[nativeKey{identity.Venue, instrumentSettlement(instrument), identity.NativeSymbol}] = instrument
	}

	mapping := CrossExMapping{Unmatched: map[string]int{}}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.BusinessType != crossex.BusinessFuture || row.State != "live" {
			continue
		}
		market, ok := crossExMarketFor(row)
		if !ok {
			mapping.Unmatched[row.ExchangeType]++
			continue
		}
		native, ok := byNative[nativeKey{market.venue, market.settlement, market.native(row)}]
		if !ok {
			mapping.Unmatched[row.ExchangeType]++
			continue
		}
		instrument, err := crossExInstrument(native, row)
		if err != nil {
			return CrossExMapping{}, fmt.Errorf("crossex symbol %s: %w", row.Symbol, err)
		}
		key := string(instrument.Identity.Venue) + ":" + instrument.Identity.Symbol
		if seen[key] {
			return CrossExMapping{}, fmt.Errorf("crossex symbols map twice onto %s", key)
		}
		seen[key] = true
		mapping.Instruments = append(mapping.Instruments, instrument)
	}
	sort.Slice(mapping.Instruments, func(i, j int) bool {
		a, b := mapping.Instruments[i].Identity, mapping.Instruments[j].Identity
		if a.Venue != b.Venue {
			return a.Venue < b.Venue
		}
		return a.Symbol < b.Symbol
	})
	return mapping, nil
}

func instrumentSettlement(instrument exchange.Instrument) string {
	settle := strings.ToUpper(instrument.Identity.SettleAsset)
	if settle == "" {
		settle = strings.ToUpper(instrument.Identity.QuoteAsset)
	}
	return settle
}

// crossExInstrument rebinds a native instrument to CrossEx: same canonical
// identity, CrossEx native symbol, CrossEx rules, base-unit quantities.
func crossExInstrument(native exchange.Instrument, row crossex.Symbol) (exchange.Instrument, error) {
	tick, err := crossExPositive(row.TickSize)
	if err != nil {
		return exchange.Instrument{}, fmt.Errorf("tick size: %w", err)
	}
	lot, err := crossExPositive(row.LotSize)
	if err != nil {
		return exchange.Instrument{}, fmt.Errorf("lot size: %w", err)
	}
	minQty, err := decimal.NewFromString(strings.TrimSpace(row.MinSize))
	if err != nil || minQty.IsNegative() {
		return exchange.Instrument{}, fmt.Errorf("min size is invalid")
	}
	instrument := native
	instrument.Identity.NativeSymbol = row.Symbol
	instrument.Identity.NativeID = ""
	instrument.Rules = exchange.InstrumentRules{PriceIncrement: tick, QuantityIncrement: lot, MinQuantity: minQty}
	if row.MinNotional != nil && strings.TrimSpace(*row.MinNotional) != "" {
		value, err := decimal.NewFromString(strings.TrimSpace(*row.MinNotional))
		if err != nil || value.IsNegative() {
			return exchange.Instrument{}, fmt.Errorf("min notional is invalid")
		}
		if value.IsPositive() {
			instrument.Rules.MinNotional = exchange.OptionalDecimal{Value: value, Valid: true}
		}
	}
	// CrossEx quantities are base units; no contract or integer encoding.
	instrument.Encoding = exchange.InstrumentEncoding{ContractMultiplier: oneContract()}
	instrument.Revision = ""
	if err := instrument.Validate(); err != nil {
		return exchange.Instrument{}, err
	}
	return instrument, nil
}

func crossExPositive(value string) (decimal.Decimal, error) {
	parsed, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || !parsed.IsPositive() {
		return decimal.Zero, fmt.Errorf("must be a positive decimal")
	}
	return parsed, nil
}
