package deribit

import (
	"context"
	"testing"
)

// Option and spot payloads captured from www.deribit.com on 2026-10-20.
func TestOptionAndSpotPublicPayloads(t *testing.T) {
	client, stub := newStub(t, map[string]string{
		"public/get_instruments":              "@options_instruments.json",
		"public/get_book_summary_by_currency": "@options_summary.json",
		"public/ticker":                       "@option_ticker.json",
	})
	ctx := context.Background()
	options, err := client.InstrumentsOf(ctx, "BTC", KindOption)
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.calls[0].URL.RawQuery; got != "currency=BTC&kind=option" {
		t.Fatalf("query = %q", got)
	}
	inverse, linear, coarse := 0, 0, 0
	for _, option := range options {
		if !option.Option() || option.Strike.String() == "" || option.ExpirationTimestamp == 0 || (option.OptionType != "call" && option.OptionType != "put") {
			t.Fatalf("option = %+v", option)
		}
		switch option.InstrumentType {
		case "reversed":
			inverse++
		case "linear":
			linear++
		}
		if len(option.TickSizeSteps) > 0 {
			coarse++
		}
	}
	if coarse == 0 {
		t.Fatal("no option carries a coarse tick step")
	}
	if inverse == 0 || linear == 0 {
		t.Fatalf("inverse=%d linear=%d", inverse, linear)
	}
	summaries, err := client.BookSummariesOf(ctx, "BTC", KindOption)
	if err != nil || len(summaries) == 0 {
		t.Fatalf("summaries = %v, %v", summaries, err)
	}
	priced := 0
	for _, row := range summaries {
		if row.MarkIV.String() != "" && row.UnderlyingPrice.String() != "" {
			priced++
		}
	}
	if priced == 0 {
		t.Fatal("no summary carries mark IV and the underlying price")
	}
	ticker, err := client.Ticker(ctx, options[0].InstrumentName)
	if err != nil || ticker.Greeks == nil || ticker.Greeks.Delta.String() == "" || ticker.MarkIV.String() == "" || ticker.UnderlyingPrice.String() == "" {
		t.Fatalf("ticker = %+v, %v", ticker, err)
	}
}

func TestSpotInstruments(t *testing.T) {
	client, stub := newStub(t, map[string]string{"public/get_instruments": "@spot_instruments.json"})
	rows, err := client.InstrumentsOf(context.Background(), CurrencyAny, KindSpot)
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.calls[0].URL.RawQuery; got != "currency=any&kind=spot" {
		t.Fatalf("query = %q", got)
	}
	open, routed := 0, 0
	for _, row := range rows {
		if row.Spot() {
			open++
		}
		if row.IsCBERouted {
			routed++
		}
	}
	if open < 4 || routed == 0 || open == len(rows) {
		t.Fatalf("open=%d routed=%d of %d (SOL_ETH is locked)", open, routed, len(rows))
	}
}

func TestPrivateCallsTakeKindAndCurrency(t *testing.T) {
	client, stub := newStub(t, map[string]string{
		"private/get_positions":               `{"jsonrpc":"2.0","result":[{"instrument_name":"BTC-23OCT26-70000-C","kind":"option","direction":"sell","size":-1.5,"average_price":0.012,"mark_price":0.01,"delta":-0.4,"gamma":-0.0001,"vega":-20,"theta":12}]}`,
		"private/get_account_summaries":       `{"jsonrpc":"2.0","result":{"id":1,"summaries":[{"currency":"BTC","balance":1,"equity":1.01,"options_delta":-0.4,"options_vega":-20},{"currency":"USDC","balance":1000}]}}`,
		"private/get_open_orders":             `{"jsonrpc":"2.0","result":[]}`,
		"private/get_user_trades_by_currency": `{"jsonrpc":"2.0","result":{"trades":[{"trade_id":"1","iv":40.1,"instrument_name":"BTC-23OCT26-70000-C","direction":"sell","price":0.012,"amount":1.5}]}}`,
	})
	ctx := context.Background()
	positions, err := client.PositionsOf(ctx, CurrencyAny, KindOption)
	if err != nil || len(positions) != 1 || positions[0].Vega.String() != "-20" {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	summaries, err := client.AccountSummaries(ctx)
	if err != nil || len(summaries) != 2 || summaries[0].OptionsDelta.String() != "-0.4" {
		t.Fatalf("summaries = %+v, %v", summaries, err)
	}
	if _, err := client.AllOpenOrders(ctx, KindOption); err != nil {
		t.Fatal(err)
	}
	trades, err := client.UserTradesOf(ctx, "BTC", KindOption, 0, 0, 10)
	if err != nil || len(trades) != 1 || trades[0].IV.String() != "40.1" {
		t.Fatalf("trades = %+v, %v", trades, err)
	}
	for _, call := range stub.calls {
		switch call.URL.Path {
		case "/api/v2/private/get_positions":
			if call.URL.RawQuery != "currency=any&kind=option" {
				t.Fatalf("positions query = %q", call.URL.RawQuery)
			}
		case "/api/v2/private/get_open_orders":
			if call.URL.RawQuery != "kind=option" {
				t.Fatalf("open orders query = %q", call.URL.RawQuery)
			}
		}
	}
}
