package factoryclient

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	binancespot "github.com/QuantProcessing/keeluni/sdk/binance/spot"
	hyperliquidperp "github.com/QuantProcessing/keeluni/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/keeluni/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

// Whole-market 24h tickers: one or a few public reads per market, never one
// per instrument. They feed the market list (the picker), so a venue that
// cannot answer is simply left without the columns.

// MarketTickerLoader reads every instrument's 24h ticker of one market.
type MarketTickerLoader func(context.Context) ([]exchange.MarketTicker, error)

// NewMarketTickerLoader returns the loader of a venue's spot or perpetual
// tickers, or nil when the venue has no bulk ticker read. A perpetual loader
// reads every settlement the venue serves (Binance's and Bybit's single read
// carries USDT and USDC alike; Bitget reads both categories), so one read
// serves all of a venue's perpetual markets.
func NewMarketTickerLoader(venue exchange.Venue, product exchange.Product, settings Settings) MarketTickerLoader {
	if product != exchange.ProductPerp && product != exchange.ProductSpot {
		return nil
	}
	perp := product == exchange.ProductPerp
	switch venue {
	case exchange.VenueBinance:
		if perp {
			return func(ctx context.Context) ([]exchange.MarketTicker, error) { return binancePerpTickers(ctx, settings) }
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) { return binanceSpotTickers(ctx, settings) }
	case exchange.VenueOKX:
		instType := "SPOT"
		if perp {
			instType = "SWAP"
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) { return okxTickers(ctx, settings, instType) }
	case exchange.VenueBybit:
		category := "spot"
		if perp {
			category = "linear"
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) {
			return bybitTickers(ctx, settings, category)
		}
	case exchange.VenueBitget:
		if !perp {
			return func(ctx context.Context) ([]exchange.MarketTicker, error) {
				return bitgetTickers(ctx, settings, "SPOT")
			}
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) {
			usdt, err := bitgetTickers(ctx, settings, "USDT-FUTURES")
			if err != nil {
				return nil, err
			}
			usdc, err := bitgetTickers(ctx, settings, "USDC-FUTURES")
			if err != nil {
				return nil, err
			}
			return append(usdt, usdc...), nil
		}
	case exchange.VenueGate:
		return func(ctx context.Context) ([]exchange.MarketTicker, error) { return gateTickers(ctx, settings, perp) }
	case exchange.VenueHyperliquid:
		if perp {
			return func(ctx context.Context) ([]exchange.MarketTicker, error) {
				return hyperliquidPerpTickers(ctx, settings)
			}
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) {
			return hyperliquidSpotTickers(ctx, settings)
		}
	case exchange.VenueLighter:
		return func(ctx context.Context) ([]exchange.MarketTicker, error) { return lighterTickers(ctx, settings) }
	case exchange.VenueKraken:
		if perp {
			return func(ctx context.Context) ([]exchange.MarketTicker, error) { return krakenPerpTickers(ctx, settings) }
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) { return krakenSpotTickers(ctx, settings) }
	case exchange.VenueDeribit:
		kind := "spot"
		if perp {
			kind = "future"
		}
		return func(ctx context.Context) ([]exchange.MarketTicker, error) { return deribitTickers(ctx, settings, kind) }
	}
	return nil
}

func tickerDecimal(raw string) exchange.OptionalDecimal {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: value, Valid: true}
}

func tickerNumber(raw json.Number) exchange.OptionalDecimal { return tickerDecimal(raw.String()) }

func tickerFloat(value float64) exchange.OptionalDecimal {
	return exchange.OptionalDecimal{Value: decimal.NewFromFloat(value), Valid: true}
}

// openFromChange recovers the 24h open from the last price and a change
// fraction (0.025 for +2.5%).
func openFromChange(last, change exchange.OptionalDecimal) exchange.OptionalDecimal {
	if !last.Valid || !change.Valid {
		return exchange.OptionalDecimal{}
	}
	divisor := decimal.NewFromInt(1).Add(change.Value)
	if !divisor.IsPositive() {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: last.Value.Div(divisor), Valid: true}
}

func binancePerpTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	rows, err := newBinancePerpPublicSDK(settings).Tickers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.MarketTicker{NativeSymbol: row.Symbol, Last: tickerDecimal(row.LastPrice), Open24h: tickerDecimal(row.OpenPrice), QuoteVolume24h: tickerDecimal(row.QuoteVolume)})
	}
	return out, nil
}

func binanceSpotTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	sdk := binancespot.NewClient()
	sdk.WithBaseURL(binancespot.MainnetEndpointProfile().RESTBaseURL)
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	rows, err := sdk.Tickers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.MarketTicker{NativeSymbol: row.Symbol, Last: tickerDecimal(row.LastPrice), Open24h: tickerDecimal(row.OpenPrice), QuoteVolume24h: tickerDecimal(row.QuoteVolume)})
	}
	return out, nil
}

func okxTickers(ctx context.Context, settings Settings, instType string) ([]exchange.MarketTicker, error) {
	rows, err := newOKXPublicSDK(settings).GetTickers(ctx, instType, nil)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		last := tickerDecimal(row.Last)
		ticker := exchange.MarketTicker{NativeSymbol: row.InstId, Last: last, Open24h: tickerDecimal(row.Open24h)}
		// volCcy24h: quote currency for spot, base currency for swaps.
		if volume := tickerDecimal(row.VolCcy24h); volume.Valid {
			if instType == "SWAP" && last.Valid {
				volume.Value = volume.Value.Mul(last.Value)
			}
			ticker.QuoteVolume24h = volume
		}
		out = append(out, ticker)
	}
	return out, nil
}

func bybitTickers(ctx context.Context, settings Settings, category string) ([]exchange.MarketTicker, error) {
	rows, err := newBybitPublicSDK(settings).GetTickers(ctx, category)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.MarketTicker{NativeSymbol: row.Symbol, Last: tickerDecimal(row.LastPrice), Open24h: tickerDecimal(row.PrevPrice24h), QuoteVolume24h: tickerDecimal(row.Turnover24h)})
	}
	return out, nil
}

func bitgetTickers(ctx context.Context, settings Settings, category string) ([]exchange.MarketTicker, error) {
	rows, err := newBitgetPublicSDK(settings).GetTickers(ctx, category)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		last := tickerDecimal(row.LastPrice)
		open := tickerDecimal(row.OpenPrice24h)
		if !open.Valid {
			open = openFromChange(last, tickerDecimal(row.Price24hPcnt))
		}
		out = append(out, exchange.MarketTicker{NativeSymbol: row.Symbol, Last: last, Open24h: open, QuoteVolume24h: tickerDecimal(row.Turnover24h)})
	}
	return out, nil
}

func gateTickers(ctx context.Context, settings Settings, perp bool) ([]exchange.MarketTicker, error) {
	sdk, _ := newGateSDK("", "", settings)
	percent := decimal.NewFromInt(100)
	change := func(raw string) exchange.OptionalDecimal {
		value := tickerDecimal(raw)
		if value.Valid {
			value.Value = value.Value.Div(percent)
		}
		return value
	}
	if perp {
		rows, err := sdk.ListFuturesTickers(ctx, "usdt", "")
		if err != nil {
			return nil, err
		}
		out := make([]exchange.MarketTicker, 0, len(rows))
		for _, row := range rows {
			last := tickerDecimal(row.Last)
			out = append(out, exchange.MarketTicker{NativeSymbol: row.Contract, Last: last, Open24h: openFromChange(last, change(row.ChangePercentage)), QuoteVolume24h: tickerDecimal(row.Volume24hQuote)})
		}
		return out, nil
	}
	rows, err := sdk.ListSpotTickers(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		last := tickerDecimal(row.Last)
		out = append(out, exchange.MarketTicker{NativeSymbol: row.CurrencyPair, Last: last, Open24h: openFromChange(last, change(row.ChangePercent)), QuoteVolume24h: tickerDecimal(row.QuoteVolume)})
	}
	return out, nil
}

// hyperliquidPerpTickers reads the main dex and every builder dex: native
// symbols are the coin names ("BTC", "xyz:TSLA").
func hyperliquidPerpTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	sdk := hyperliquidperp.NewClient(newHyperliquidPublicSDK(settings))
	dexs, err := sdk.GetPerpDexs(ctx)
	if err != nil {
		return nil, err
	}
	names := []string{""}
	for _, dex := range dexs {
		if dex.Name != "" {
			names = append(names, dex.Name)
		}
	}
	var out []exchange.MarketTicker
	for _, name := range names {
		full, err := sdk.GetMetaAndAssetCtxsForDex(ctx, name)
		if err != nil {
			if name == "" {
				return nil, err
			}
			continue
		}
		for index, universe := range full.Meta.Universe {
			if index >= len(full.AssetCtxs) {
				break
			}
			row := full.AssetCtxs[index]
			last := tickerDecimal(row.MidPx)
			if !last.Valid {
				last = tickerDecimal(row.MarkPx)
			}
			out = append(out, exchange.MarketTicker{NativeSymbol: universe.Name, Last: last, Open24h: tickerDecimal(row.PrevDayPx), QuoteVolume24h: tickerDecimal(row.DayNtlVlm)})
		}
	}
	return out, nil
}

func hyperliquidSpotTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	rows, err := hyperliquidspot.NewClient(newHyperliquidPublicSDK(settings)).GetSpotAssetCtxs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		last := tickerDecimal(row.MidPx)
		if !last.Valid {
			last = tickerDecimal(row.MarkPx)
		}
		out = append(out, exchange.MarketTicker{NativeSymbol: row.Coin, Last: last, Open24h: tickerDecimal(row.PrevDayPx), QuoteVolume24h: tickerDecimal(row.DayNtlVlm)})
	}
	return out, nil
}

// lighterTickers serves both Lighter markets: perps are "BTC", spot pairs
// "LINK/USDC".
func lighterTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	stats, err := newLighterPublicSDK(settings).GetExchangeStats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(stats.OrderBookStats))
	for _, row := range stats.OrderBookStats {
		if row == nil {
			continue
		}
		last := tickerFloat(row.LastTradePrice)
		// daily_price_change is a percentage.
		change := tickerFloat(row.DailyPriceChange / 100)
		out = append(out, exchange.MarketTicker{NativeSymbol: row.Symbol, Last: last, Open24h: openFromChange(last, change), QuoteVolume24h: tickerFloat(row.DailyQuoteTokenVolume)})
	}
	return out, nil
}

func krakenPerpTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	rows, err := newKrakenSDK(settings).Tickers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		last := tickerNumber(row.Last)
		ticker := exchange.MarketTicker{NativeSymbol: row.Symbol, Last: last, Open24h: tickerNumber(row.Open24h)}
		// vol24h counts contracts of one unit: value it at the last price.
		if volume := tickerNumber(row.Vol24h); volume.Valid && last.Valid {
			ticker.QuoteVolume24h = exchange.OptionalDecimal{Value: volume.Value.Mul(last.Value), Valid: true}
		}
		out = append(out, ticker)
	}
	return out, nil
}

// krakenSpotTickers keys by the legacy REST name (XXBTZUSD): the catalog's
// native id. Kraken's open is today's UTC midnight open, not 24h ago.
func krakenSpotTickers(ctx context.Context, settings Settings) ([]exchange.MarketTicker, error) {
	rows, err := newKrakenSpotSDK(settings).Tickers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.MarketTicker, 0, len(rows))
	for key, row := range rows {
		ticker := exchange.MarketTicker{NativeID: key, Open24h: tickerDecimal(row.Open)}
		if len(row.Close) > 0 {
			ticker.Last = tickerDecimal(row.Close[0])
		}
		if len(row.Volume) > 1 && len(row.VWAP) > 1 {
			volume, vwap := tickerDecimal(row.Volume[1]), tickerDecimal(row.VWAP[1])
			if volume.Valid && vwap.Valid {
				ticker.QuoteVolume24h = exchange.OptionalDecimal{Value: volume.Value.Mul(vwap.Value), Valid: true}
			}
		}
		out = append(out, ticker)
	}
	return out, nil
}

// deribitTickers reads the USDC book summary (linear perpetuals and spot
// pairs); price_change is a percentage.
func deribitTickers(ctx context.Context, settings Settings, kind string) ([]exchange.MarketTicker, error) {
	rows, err := newDeribitSDK(settings).BookSummariesOf(ctx, "USDC", kind)
	if err != nil {
		return nil, err
	}
	percent := decimal.NewFromInt(100)
	out := make([]exchange.MarketTicker, 0, len(rows))
	for _, row := range rows {
		last := tickerNumber(row.Last)
		change := tickerNumber(row.PriceChange)
		if change.Valid {
			change.Value = change.Value.Div(percent)
		}
		ticker := exchange.MarketTicker{NativeSymbol: row.InstrumentName, Last: last, Open24h: openFromChange(last, change)}
		if volume := tickerNumber(row.VolumeUSD); volume.Valid && volume.Value.IsPositive() {
			ticker.QuoteVolume24h = volume
		} else if volume := tickerNumber(row.Volume); volume.Valid && last.Valid {
			ticker.QuoteVolume24h = exchange.OptionalDecimal{Value: volume.Value.Mul(last.Value), Valid: true}
		}
		out = append(out, ticker)
	}
	return out, nil
}
