package factory

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
)

// cannedTransport answers each request by the first registered path
// fragment its URL (path and body) contains.
type cannedTransport struct {
	replies map[string]string
	calls   int
}

func (transport *cannedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	key := request.URL.Path + "?" + request.URL.RawQuery
	if request.Body != nil {
		body, _ := io.ReadAll(request.Body)
		key += " " + string(body)
	}
	for fragment, reply := range transport.replies {
		if strings.Contains(key, fragment) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(reply)), Request: request}, nil
		}
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Request: request}, nil
}

// Every served spot and perpetual market builds a whole-market ticker
// reader without I/O; options and unknown markets are refused.
func TestPublicMarketTickersConstructWithoutIO(t *testing.T) {
	transport := new(countingTransport)
	client := &http.Client{Transport: transport}
	for _, market := range []struct {
		venue      exchange.Venue
		product    exchange.Product
		settlement string
	}{
		{exchange.VenueBinance, exchange.ProductPerp, "USDT"}, {exchange.VenueBinance, exchange.ProductSpot, ""},
		{exchange.VenueOKX, exchange.ProductPerp, "USDT"}, {exchange.VenueOKX, exchange.ProductSpot, ""},
		{exchange.VenueBybit, exchange.ProductPerp, "USDC"}, {exchange.VenueBitget, exchange.ProductPerp, "USDC"},
		{exchange.VenueGate, exchange.ProductSpot, ""}, {exchange.VenueHyperliquid, exchange.ProductPerp, "USDC"},
		{exchange.VenueLighter, exchange.ProductSpot, ""}, {exchange.VenueKraken, exchange.ProductSpot, ""},
		{exchange.VenueDeribit, exchange.ProductPerp, "USDC"},
	} {
		if _, err := NewPublicMarketTickers(market.venue, market.product, market.settlement, EnvironmentLive, client); err != nil {
			t.Fatalf("%s %s: %v", market.venue, market.product, err)
		}
	}
	if calls := transport.requests.Load(); calls != 0 {
		t.Fatalf("construction performed %d requests", calls)
	}
	if _, err := NewPublicMarketTickers(exchange.VenueDeribit, exchange.ProductOption, "", EnvironmentLive, client); err == nil {
		t.Fatal("options have no market tickers")
	}
	if _, err := NewPublicMarketTickers(exchange.VenueBinance, exchange.ProductPerp, "USDT", EnvironmentLive, nil); err == nil {
		t.Fatal("a nil HTTP client must be refused")
	}
}

// Each venue's units come out the same: prices in the quote asset, the open
// recovered from a percentage change where that is all the venue sends, and
// volume as traded value in the quote asset.
func TestPublicMarketTickersNormalizeVenueUnits(t *testing.T) {
	cases := []struct {
		name       string
		venue      exchange.Venue
		product    exchange.Product
		settlement string
		replies    map[string]string
		symbol, id string
		last, open string
		volume     string
	}{
		{
			name: "binance perp", venue: exchange.VenueBinance, product: exchange.ProductPerp, settlement: "USDT",
			replies: map[string]string{"ticker/24hr": `[{"symbol":"BTCUSDT","lastPrice":"102","openPrice":"100","quoteVolume":"5000"}]`},
			symbol:  "BTCUSDT", last: "102", open: "100", volume: "5000",
		},
		{
			// Swap volCcy24h counts the base asset: valued at the last price.
			name: "okx swap", venue: exchange.VenueOKX, product: exchange.ProductPerp, settlement: "USDT",
			replies: map[string]string{"market/tickers": `{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","last":"102","open24h":"100","volCcy24h":"3"}]}`},
			symbol:  "BTC-USDT-SWAP", last: "102", open: "100", volume: "306",
		},
		{
			// Keyed by the legacy name; value is 24h base volume × 24h VWAP.
			name: "kraken spot", venue: exchange.VenueKraken, product: exchange.ProductSpot,
			replies: map[string]string{"/0/public/Ticker": `{"error":[],"result":{"XXBTZUSD":{"c":["102","0.1"],"o":"100","v":["1","4"],"p":["101","101.5"]}}}`},
			id:      "XXBTZUSD", last: "102", open: "100", volume: "406",
		},
		{
			// price_change is a percentage: +2% from 100 is 102.
			name: "deribit perp", venue: exchange.VenueDeribit, product: exchange.ProductPerp, settlement: "USDC",
			replies: map[string]string{"get_book_summary_by_currency": `{"jsonrpc":"2.0","result":[{"instrument_name":"BTC_USDC-PERPETUAL","last":102,"price_change":2,"volume":3,"volume_usd":306}]}`},
			symbol:  "BTC_USDC-PERPETUAL", last: "102", open: "100", volume: "306",
		},
		{
			name: "lighter", venue: exchange.VenueLighter, product: exchange.ProductPerp, settlement: "USDC",
			replies: map[string]string{"exchangeStats": `{"code":200,"order_book_stats":[{"symbol":"BTC","last_trade_price":102,"daily_quote_token_volume":5000,"daily_price_change":2}]}`},
			symbol:  "BTC", last: "102", open: "100", volume: "5000",
		},
		{
			// The main dex and each builder dex; the mid price is the last.
			name: "hyperliquid perp", venue: exchange.VenueHyperliquid, product: exchange.ProductPerp, settlement: "USDC",
			replies: map[string]string{
				`"perpDexs"`:         `[null,{"name":"xyz"}]`,
				`"dex":"xyz"`:        `[{"universe":[{"name":"xyz:TSLA","szDecimals":2}]},[{"markPx":"250","midPx":"251","prevDayPx":"240","dayNtlVlm":"9000"}]]`,
				`"metaAndAssetCtxs"`: `[{"universe":[{"name":"BTC","szDecimals":5}]},[{"markPx":"101","midPx":"102","prevDayPx":"100","dayNtlVlm":"5000"}]]`,
			},
			symbol: "BTC", last: "102", open: "100", volume: "5000",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: &cannedTransport{replies: test.replies}}
			reader, err := NewPublicMarketTickers(test.venue, test.product, test.settlement, EnvironmentLive, client)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := reader.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var found *exchange.MarketTicker
			for index := range rows {
				if (test.symbol != "" && rows[index].NativeSymbol == test.symbol) || (test.id != "" && rows[index].NativeID == test.id) {
					found = &rows[index]
				}
			}
			if found == nil {
				t.Fatalf("no row for %s%s in %+v", test.symbol, test.id, rows)
			}
			for field, pair := range map[string][2]any{"last": {found.Last, test.last}, "open": {found.Open24h, test.open}, "volume": {found.QuoteVolume24h, test.volume}} {
				value := pair[0].(exchange.OptionalDecimal)
				if !value.Valid || value.Value.String() != pair[1].(string) {
					t.Fatalf("%s = %v (valid %v), want %s", field, value.Value, value.Valid, pair[1])
				}
			}
			if test.venue == exchange.VenueHyperliquid && len(rows) != 2 {
				t.Fatalf("builder dex rows missing: %+v", rows)
			}
		})
	}
	var nilReader *PublicMarketTickers
	if _, err := nilReader.Load(context.Background()); err == nil {
		t.Fatal("a nil reader must fail")
	}
}
