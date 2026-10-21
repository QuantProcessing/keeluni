package factoryclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	deribit "github.com/QuantProcessing/keeluni/sdk/deribit"
	"github.com/shopspring/decimal"
)

// deribitOptionClient serves every Deribit option (BTC, ETH inverse; USDC
// linear) from one account. Orders, books and trades are the shared Deribit
// paths; this file adds the chain, quotes and the option account.
type deribitOptionClient struct {
	*deribitBase
	ws *optionWebSocket
}

func NewDeribitOption(clientID, clientSecret string, settings Settings) exchange.OptionClient {
	client := &deribitOptionClient{deribitBase: newDeribitBase(clientID, clientSecret, exchange.ProductOption, deribitScope{
		kind: deribit.KindOption, currencies: deribitOptionCurrencies, eventCurrency: deribit.CurrencyAny,
		serves: deribitIsOption,
	}, settings)}
	backend := newDeribitWSBackend(client.deribitBase)
	client.ws = newOptionWebSocket(client.meta, backend, backend, backend)
	return client
}

// deribitIsOption: option names end in -C or -P after a strike.
func deribitIsOption(native string) bool {
	parts := strings.Split(native, "-")
	return len(parts) == 4 && (parts[3] == "C" || parts[3] == "P")
}

func (client *deribitOptionClient) WebSocket() exchange.OptionWebSocket { return client.ws }
func (client *deribitOptionClient) Close() error                        { return client.ws.Close() }

// ---------------------------------------------------------------------------
// Chain
// ---------------------------------------------------------------------------

// OptionChain reads every option of the underlying's currencies in one call
// each. An underlying settles in its own coin (BTC, ETH) and in USDC (BTC,
// ETH and the altcoins), so both summaries are read and filtered.
func (client *deribitOptionClient) OptionChain(ctx context.Context, request exchange.OptionChainRequest) ([]exchange.OptionQuote, error) {
	if err := deribitContext(ctx, client.meta, "OptionChain"); err != nil {
		return nil, err
	}
	instruments, err := client.Instruments(ctx)
	if err != nil {
		return nil, err
	}
	byNative := make(map[string]exchange.Instrument, len(instruments))
	needed := map[string]bool{}
	underlying := strings.ToUpper(strings.TrimSpace(request.Underlying))
	for _, instrument := range instruments {
		if underlying != "" && instrument.Option.Underlying != underlying {
			continue
		}
		byNative[instrument.Identity.NativeSymbol] = instrument
		needed[instrument.Identity.SettleAsset] = true
	}
	var quotes []exchange.OptionQuote
	for _, currency := range deribitOptionCurrencies {
		if !needed[currency] {
			continue
		}
		rows, err := coordinatedMarketRequest(ctx, Settings{MarketRequests: client.requests}, exchange.VenueDeribit, "book_summary", "option:"+currency, func(callCtx context.Context) ([]deribit.BookSummary, error) {
			return client.sdk.BookSummariesOf(callCtx, currency, deribit.KindOption)
		})
		if err != nil {
			return nil, deribitNormalizeErr(client.meta, "OptionChain", err)
		}
		for _, row := range rows {
			instrument, ok := byNative[row.InstrumentName]
			if !ok {
				continue
			}
			quotes = append(quotes, deribitSummaryQuote(instrument, row))
		}
	}
	return quotes, nil
}

// deribitPremium converts a Deribit option price into the uniform premium:
// inverse options quote in the coin per coin, which stays as is (premium
// asset = the coin). Prices need no conversion; this exists to make that
// decision explicit at every call site.
func deribitPremium(value jsonNumber) exchange.OptionalDecimal {
	return positiveOptional(value)
}

func positiveOptional(value jsonNumber) exchange.OptionalDecimal {
	parsed := numberOptional(value)
	if !parsed.Valid || !parsed.Value.IsPositive() {
		return exchange.OptionalDecimal{}
	}
	return parsed
}

// deribitIV converts a percent IV (40.39) into a fraction (0.4039).
func deribitIV(value jsonNumber) exchange.OptionalDecimal {
	parsed := positiveOptional(value)
	if parsed.Valid {
		parsed.Value = parsed.Value.Div(decimal.NewFromInt(100))
	}
	return parsed
}

func deribitSummaryQuote(instrument exchange.Instrument, row deribit.BookSummary) exchange.OptionQuote {
	quote := exchange.OptionQuote{
		Instrument: instrument, Bid: deribitPremium(row.BidPrice), Ask: deribitPremium(row.AskPrice), Mark: deribitPremium(row.MarkPrice),
		Last: deribitPremium(row.Last), MarkIV: deribitIV(row.MarkIV), UnderlyingPrice: positiveOptional(row.UnderlyingPrice),
		IndexPrice: positiveOptional(row.EstimatedDelivery), OpenInterest: numberOptional(row.OpenInterest), Volume24h: numberOptional(row.Volume),
	}
	if row.CreationTimestamp > 0 {
		quote.Time = time.UnixMilli(row.CreationTimestamp).UTC()
	}
	return quote
}

func deribitTickerQuote(instrument exchange.Instrument, ticker deribit.Ticker) exchange.OptionQuote {
	quote := exchange.OptionQuote{
		Instrument: instrument, Bid: deribitPremium(ticker.BestBidPrice), Ask: deribitPremium(ticker.BestAskPrice),
		BidSize: positiveOptional(ticker.BestBidAmount), AskSize: positiveOptional(ticker.BestAskAmount),
		Mark: deribitPremium(ticker.MarkPrice), Last: deribitPremium(ticker.LastPrice),
		MarkIV: deribitIV(ticker.MarkIV), BidIV: deribitIV(ticker.BidIV), AskIV: deribitIV(ticker.AskIV),
		UnderlyingPrice: positiveOptional(ticker.UnderlyingPrice), IndexPrice: positiveOptional(ticker.IndexPrice),
		OpenInterest: numberOptional(ticker.OpenInterest), Volume24h: numberOptional(ticker.Stats.Volume), Time: krakenMillis(ticker.Timestamp),
	}
	if ticker.Greeks != nil {
		// Deribit's ticker greeks are per contract in the strike currency
		// (USD) for every option, matching the contract's convention.
		quote.Greeks = &exchange.OptionGreeks{
			Delta: numberDecimal(ticker.Greeks.Delta), Gamma: numberDecimal(ticker.Greeks.Gamma),
			Vega: numberDecimal(ticker.Greeks.Vega), Theta: numberDecimal(ticker.Greeks.Theta),
		}
	}
	return quote
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

func (client *deribitOptionClient) summaries(ctx context.Context, operation string) ([]deribit.AccountSummary, error) {
	if err := deribitContext(ctx, client.meta, operation); err != nil {
		return nil, err
	}
	rows, err := client.sdk.AccountSummaries(ctx)
	if err != nil {
		return nil, deribitNormalizeErr(client.meta, operation, err)
	}
	var out []deribit.AccountSummary
	for _, row := range rows {
		for _, currency := range deribitOptionCurrencies {
			if strings.EqualFold(row.Currency, currency) {
				out = append(out, row)
			}
		}
	}
	return out, nil
}

// Balances reports the option collateral currencies: BTC, ETH and USDC.
func (client *deribitOptionClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	rows, err := client.summaries(ctx, "Balances")
	if err != nil {
		return nil, err
	}
	var balances []exchange.Balance
	for _, row := range rows {
		balances = append(balances, deribitSummaryBalances(row)...)
	}
	return balances, nil
}

// OptionAccount reports the USDC margin figures when the account has USDC
// (linear options and cross-collateral accounts margin in USD terms there),
// else the first currency with equity.
func (client *deribitOptionClient) OptionAccount(ctx context.Context) (exchange.OptionAccount, error) {
	rows, err := client.summaries(ctx, "OptionAccount")
	if err != nil {
		return exchange.OptionAccount{}, err
	}
	account := exchange.OptionAccount{}
	var chosen *deribit.AccountSummary
	for index := range rows {
		account.Balances = append(account.Balances, deribitSummaryBalances(rows[index])...)
		if chosen == nil && numberDecimal(rows[index].Equity).IsPositive() {
			chosen = &rows[index]
		}
		if strings.EqualFold(rows[index].Currency, deribitCurrency) && numberDecimal(rows[index].Equity).IsPositive() {
			chosen = &rows[index]
		}
	}
	if chosen != nil {
		account.SettleAsset = strings.ToUpper(chosen.Currency)
		account.Equity, account.Available = numberOptional(chosen.Equity), numberOptional(chosen.AvailableFunds)
		account.InitialMargin, account.MaintenanceMargin = numberOptional(chosen.InitialMargin), numberOptional(chosen.MaintenanceMargin)
		account.UnrealizedPnL = numberOptional(chosen.OptionsPL)
	}
	return account, nil
}

func (client *deribitOptionClient) Positions(ctx context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := deribitContext(ctx, client.meta, "Positions"); err != nil {
		return nil, err
	}
	filter := ""
	if request.Instrument != nil {
		native, err := client.native("Positions", *request.Instrument)
		if err != nil {
			return nil, err
		}
		filter = native
	}
	rows, err := client.sdk.PositionsOf(ctx, deribit.CurrencyAny, deribit.KindOption)
	if err != nil {
		return nil, deribitNormalizeErr(client.meta, "Positions", err)
	}
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if !deribitIsOption(row.InstrumentName) || (filter != "" && row.InstrumentName != filter) {
			continue
		}
		if position := deribitPosition(row); !position.Quantity.IsZero() {
			out = append(out, position)
		}
	}
	return out, nil
}

// WatchOptionQuote streams one option's ticker as a quote.
func (backend *deribitWSBackend) StartOptionQuote(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OptionQuote]) (func() error, error) {
	native, err := backend.client.native("WatchOptionQuote", instrument)
	if err != nil {
		return nil, err
	}
	return backend.public(ctx, "ticker."+native+".100ms", callbacks.Status, func(data json.RawMessage) error {
		var ticker deribit.Ticker
		if deribit.Decode(data, &ticker) != nil {
			return fmt.Errorf("deribit ticker message is malformed")
		}
		callbacks.Event(deribitTickerQuote(instrument, ticker))
		return nil
	})
}

var (
	_ exchange.OptionClient     = (*deribitOptionClient)(nil)
	_ exchange.ExactOrderLookup = (*deribitOptionClient)(nil)
)

// ---------------------------------------------------------------------------
// Spot
// ---------------------------------------------------------------------------

// deribitSpotClient trades Deribit spot pairs. Balances are the account's
// currencies; there are no positions.
type deribitSpotClient struct {
	*deribitBase
	ws *spotWebSocket
}

func NewDeribitSpot(clientID, clientSecret string, settings Settings) exchange.SpotClient {
	client := &deribitSpotClient{deribitBase: newDeribitBase(clientID, clientSecret, exchange.ProductSpot, deribitScope{
		kind: deribit.KindSpot, currencies: []string{"BTC", "ETH", "USDC", "USDT"}, eventCurrency: deribit.CurrencyAny,
		serves: deribitIsSpot,
	}, settings)}
	backend := newDeribitWSBackend(client.deribitBase)
	client.ws = newSpotWebSocket(newPublicWebSocket(client.meta, backend), backend)
	return client
}

// deribitIsSpot: spot names are BASE_QUOTE with no dash.
func deribitIsSpot(native string) bool {
	return strings.Contains(native, "_") && !strings.Contains(native, "-")
}

func (client *deribitSpotClient) WebSocket() exchange.SpotWebSocket { return client.ws }
func (client *deribitSpotClient) Close() error                      { return client.ws.Close() }

func (client *deribitSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := deribitContext(ctx, client.meta, "Balances"); err != nil {
		return nil, err
	}
	rows, err := client.sdk.AccountSummaries(ctx)
	if err != nil {
		return nil, deribitNormalizeErr(client.meta, "Balances", err)
	}
	var balances []exchange.Balance
	for _, row := range rows {
		balances = append(balances, deribitSummaryBalances(row)...)
	}
	return balances, nil
}

func (client *deribitSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	balances, err := client.Balances(ctx)
	return exchange.SpotAccount{Balances: balances}, err
}

var (
	_ exchange.SpotClient       = (*deribitSpotClient)(nil)
	_ exchange.ExactOrderLookup = (*deribitSpotClient)(nil)
)
