package tickers

import (
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func dec(value string) exchange.OptionalDecimal {
	return exchange.OptionalDecimal{Value: decimal.RequireFromString(value), Valid: true}
}

func TestProjectComputesChangeAndKeepsMissingFieldsEmpty(t *testing.T) {
	row := project("binance:perp:BTC-USDT", exchange.MarketTicker{Last: dec("102"), Open24h: dec("100"), QuoteVolume24h: dec("1234567.8")})
	if row.Last != "102" || row.Open24h != "100" || row.Change24h != 0.02 || row.quoteVolume != 1234567.8 {
		t.Fatalf("row = %+v", row)
	}
	empty := project("x:perp:Y", exchange.MarketTicker{Last: dec("0")})
	if empty.Last != "" || empty.Change24h != 0 || empty.quoteVolume != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestDollarPricesValueQuotesFromStablePairsEitherWay(t *testing.T) {
	prices := dollarPrices([]Row{
		{base: "BTC", quote: "USDT", last: 80000, quoteVolume: 1e9, spot: true},
		{base: "BTC", quote: "USDC", last: 79000, quoteVolume: 1e6, spot: true}, // thinner: ignored
		{base: "USDT", quote: "IDR", last: 16000, quoteVolume: 16000 * 5e7, spot: true},
		{base: "ETH", quote: "BTC", last: 0.03, quoteVolume: 10, spot: true},
		// An FX perp quotes the inverse: never a dollar price.
		{base: "IDR", quote: "USDC", last: 16000, quoteVolume: 1e12},
	})
	if prices["BTC"] != 80000 || prices["USDC"] != 1 || prices["IDR"] != 1.0/16000 {
		t.Fatalf("prices = %v", prices)
	}
	if _, ok := prices["ETH"]; ok {
		t.Fatal("ETH has no stable pair here")
	}
}
