package factoryclient

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/shopspring/decimal"
)

func nativePerp(venue exchange.Venue, symbol, base, settle, native, tick, step string) exchange.Instrument {
	instrument := newInstrument(venue, exchange.ProductPerp, symbol, base, settle, settle, native, "",
		decimal.RequireFromString(tick), decimal.RequireFromString(step), decimal.RequireFromString(step),
		exchange.OptionalDecimal{}, oneContract())
	return instrument
}

func crossExRow(symbol, tick, lot, minSize string, minNotional *string, tag string) crossex.Symbol {
	parts := strings.SplitN(symbol, "_", 2)
	return crossex.Symbol{Symbol: symbol, ExchangeType: parts[0], BusinessType: "FUTURE", State: "live",
		TickSize: tick, LotSize: lot, MinSize: minSize, MinNotional: minNotional, Tag: tag}
}

func ptr(value string) *string { return &value }

// Rows mirror the live CrossEx catalog of 2026-10-18.
func TestMapCrossExInstrumentsMatchesByNativeSymbol(t *testing.T) {
	gateSOL := nativePerp(exchange.VenueGate, "SOL-USDT", "SOL", "USDT", "SOL_USDT", "0.01", "1")
	gateSOL.Encoding.ContractMultiplier = exchange.OptionalDecimal{Value: decimal.RequireFromString("1"), Valid: true}
	natives := []exchange.Instrument{
		nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.10", "0.001"),
		nativePerp(exchange.VenueBinance, "1000PEPE-USDT", "1000PEPE", "USDT", "1000PEPEUSDT", "0.0000001", "1"),
		nativePerp(exchange.VenueOKX, "BTC-USDT-SWAP", "BTC", "USDT", "BTC-USDT-SWAP", "0.1", "0.01"),
		nativePerp(exchange.VenueBybit, "BTC-USDC", "BTC", "USDC", "BTCPERP", "0.1", "0.001"),
		gateSOL,
		nativePerp(exchange.VenueHyperliquid, "kPEPE-USDC", "kPEPE", "USDC", "kPEPE", "0.000001", "1"),
		nativePerp(exchange.VenueHyperliquid, "xyz:TSLA-USDC", "TSLA", "USDC", "xyz:TSLA", "0.01", "0.001"),
		nativePerp(exchange.VenueLighter, "1000PEPE-USDC", "1000PEPE", "USDC", "1000PEPE", "0.000001", "1"),
		// A native spot instrument with the same native symbol must not match.
		newInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETH", "USDT", "", "ETHUSDT", "",
			decimal.RequireFromString("0.01"), decimal.RequireFromString("0.0001"), decimal.RequireFromString("0.0001"), exchange.OptionalDecimal{}, oneContract()),
	}
	rows := []crossex.Symbol{
		crossExRow("BINANCE_FUTURE_BTC_USDT", "0.10", "0.001", "0.001", ptr("50"), ""),
		crossExRow("BINANCE_FUTURE_1000PEPE_USDT", "0.0000001", "1", "1", ptr("5"), ""),
		crossExRow("BINANCE_FUTURE_ETH_USDT", "0.01", "0.001", "0.001", ptr("20"), ""), // native catalog lacks it
		crossExRow("BINANCE_FUTURE_AMZN_USDT", "0.01", "0.01", "0.01", nil, ""),        // TradFi, not native
		crossExRow("OKX_FUTURE_BTC_USDT", "0.1", "0.0001", "0.0001", nil, ""),
		crossExRow("OKX_FUTURE_BTC_USDC", "0.1", "0.0001", "0.0001", nil, ""), // USDC not served
		crossExRow("BYBIT_FUTURE_BTC_USDC", "0.1", "0.001", "0.001", ptr("5"), ""),
		crossExRow("GATE_FUTURE_SOL_USDT", "0.01", "0.1", "0.1", nil, ""), // finer than native 1 contract
		crossExRow("HYPERLIQUID_FUTURE_kPEPE_USDC", "0.000001", "1", "1", ptr("10"), ""),
		crossExRow("HYPERLIQUID_FUTURE_TSLA_USDC", "0.01", "0.001", "0.001", ptr("10"), "xyz"),
		crossExRow("LIGHTER_FUTURE_1000PEPE_USDC", "0.000001", "1", "1", nil, ""),
		crossExRow("KRAKEN_FUTURE_BTC_USD", "0.5", "0.0001", "0.0001", nil, ""),
	}
	suspended := crossExRow("BINANCE_FUTURE_LUNA_USDT", "0.1", "1", "1", nil, "")
	suspended.State = "suspend"
	spot := crossExRow("BINANCE_SPOT_ETH_USDT", "0.01", "0.0001", "0.0001", nil, "")
	spot.BusinessType = "SPOT"
	rows = append(rows, suspended, spot)

	mapping, err := MapCrossExInstruments(rows, natives)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]exchange.Instrument{}
	for _, instrument := range mapping.Instruments {
		got[instrument.Identity.NativeSymbol] = instrument
	}
	wantIdentity := map[string]struct {
		venue  exchange.Venue
		symbol string
	}{
		"BINANCE_FUTURE_BTC_USDT":       {exchange.VenueBinance, "BTC-USDT"},
		"BINANCE_FUTURE_1000PEPE_USDT":  {exchange.VenueBinance, "1000PEPE-USDT"},
		"OKX_FUTURE_BTC_USDT":           {exchange.VenueOKX, "BTC-USDT-SWAP"},
		"BYBIT_FUTURE_BTC_USDC":         {exchange.VenueBybit, "BTC-USDC"},
		"GATE_FUTURE_SOL_USDT":          {exchange.VenueGate, "SOL-USDT"},
		"HYPERLIQUID_FUTURE_kPEPE_USDC": {exchange.VenueHyperliquid, "kPEPE-USDC"},
		"HYPERLIQUID_FUTURE_TSLA_USDC":  {exchange.VenueHyperliquid, "xyz:TSLA-USDC"},
		"LIGHTER_FUTURE_1000PEPE_USDC":  {exchange.VenueLighter, "1000PEPE-USDC"},
	}
	if len(got) != len(wantIdentity) {
		t.Fatalf("mapped %d instruments, want %d: %v", len(got), len(wantIdentity), mapKeysOf(got))
	}
	for native, want := range wantIdentity {
		instrument, ok := got[native]
		if !ok {
			t.Fatalf("%s was not mapped", native)
		}
		if instrument.Identity.Venue != want.venue || instrument.Identity.Symbol != want.symbol || instrument.Identity.Product != exchange.ProductPerp {
			t.Fatalf("%s identity = %+v", native, instrument.Identity)
		}
		if err := instrument.Validate(); err != nil {
			t.Fatalf("%s invalid: %v", native, err)
		}
	}

	// Rules and encoding come from CrossEx, not from the native venue.
	sol := got["GATE_FUTURE_SOL_USDT"]
	if !sol.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.1")) || !sol.Rules.MinQuantity.Equal(decimal.RequireFromString("0.1")) ||
		!sol.Encoding.ContractMultiplier.Value.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("gate SOL rules = %+v encoding = %+v", sol.Rules, sol.Encoding)
	}
	btc := got["BINANCE_FUTURE_BTC_USDT"]
	if !btc.Rules.MinNotional.Valid || !btc.Rules.MinNotional.Value.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("binance BTC min notional = %+v", btc.Rules.MinNotional)
	}
	if got["OKX_FUTURE_BTC_USDT"].Rules.MinNotional.Valid {
		t.Fatal("absent min notional became a value")
	}

	wantUnmatched := map[string]int{"BINANCE": 2, "OKX": 1, "KRAKEN": 1}
	for exchangeType, count := range wantUnmatched {
		if mapping.Unmatched[exchangeType] != count {
			t.Fatalf("unmatched = %v, want %v", mapping.Unmatched, wantUnmatched)
		}
	}
	if len(mapping.Unmatched) != len(wantUnmatched) {
		t.Fatalf("unmatched = %v, want %v (suspended and spot rows are not counted)", mapping.Unmatched, wantUnmatched)
	}
}

func TestMapCrossExInstrumentsRejectsBrokenRules(t *testing.T) {
	natives := []exchange.Instrument{nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.10", "0.001")}
	for _, row := range []crossex.Symbol{
		crossExRow("BINANCE_FUTURE_BTC_USDT", "0", "0.001", "0.001", nil, ""),
		crossExRow("BINANCE_FUTURE_BTC_USDT", "0.1", "x", "0.001", nil, ""),
		crossExRow("BINANCE_FUTURE_BTC_USDT", "0.1", "0.001", "-1", nil, ""),
		crossExRow("BINANCE_FUTURE_BTC_USDT", "0.1", "0.001", "0.001", ptr("abc"), ""),
	} {
		if _, err := MapCrossExInstruments([]crossex.Symbol{row}, natives); err == nil {
			t.Fatalf("broken rules accepted: %+v", row)
		}
	}
}

func TestMapCrossExInstrumentsRejectsDuplicateTargets(t *testing.T) {
	natives := []exchange.Instrument{nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.10", "0.001")}
	row := crossExRow("BINANCE_FUTURE_BTC_USDT", "0.1", "0.001", "0.001", nil, "")
	if _, err := MapCrossExInstruments([]crossex.Symbol{row, row}, natives); err == nil {
		t.Fatal("two CrossEx rows mapped onto one instrument")
	}
}

func TestCrossExReachableMarketsMatchServedRoutes(t *testing.T) {
	want := map[CrossExMarketRoute]bool{
		{exchange.VenueBinance, "USDT"}: true, {exchange.VenueOKX, "USDT"}: true,
		{exchange.VenueBybit, "USDT"}: true, {exchange.VenueBybit, "USDC"}: true,
		{exchange.VenueGate, "USDT"}: true, {exchange.VenueHyperliquid, "USDC"}: true,
		{exchange.VenueLighter, "USDC"}: true,
	}
	got := CrossExReachableMarkets()
	if len(got) != len(want) {
		t.Fatalf("reachable = %v", got)
	}
	for _, route := range got {
		if !want[route] {
			t.Fatalf("unexpected reachable market %+v", route)
		}
	}
}

func mapKeysOf[T any](values map[string]T) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
