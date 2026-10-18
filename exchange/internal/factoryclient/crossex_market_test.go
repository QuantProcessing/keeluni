package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestMapCrossExOnlyInstruments(t *testing.T) {
	natives := []exchange.Instrument{nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.1", "0.001")}
	rows := []crossex.Symbol{
		crossExRow("KRAKEN_FUTURE_BTC_USD", "1", "0.0001", "0.0001", nil, ""),
		crossExRow("DERIBIT_FUTURE_ETH_USDC", "0.01", "0.001", "0.001", nil, ""),
		crossExRow("BINANCE_FUTURE_AMZN_USDT", "0.01", "0.01", "0.01", ptr("5"), "Amazon.com"),
		crossExRow("BINANCE_FUTURE_BTC_USDC", "0.1", "0.001", "0.001", ptr("5"), ""),
		crossExRow("BINANCE_FUTURE_BTC_USDT", "0.1", "0.001", "0.001", ptr("50"), ""), // native: not CrossEx-only
		crossExRow("OKX_FUTURE_BTC_USDC", "0.1", "0.0001", "0.0001", nil, ""),         // contracts of unknown size
		crossExRow("GATE_FUTURE_BTC_USDT", "0.1", "0.0001", "0.0001", nil, ""),        // a native market
	}
	instruments, err := MapCrossExOnlyInstruments(rows, natives)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]exchange.Instrument{}
	for _, instrument := range instruments {
		got[string(instrument.Identity.Venue)+":"+instrument.Identity.Symbol] = instrument
		if err := instrument.Validate(); err != nil || !IsCrossExOnly(instrument) {
			t.Fatalf("%+v: %v", instrument.Identity, err)
		}
	}
	for _, key := range []string{"kraken:BTC-USD", "deribit:ETH-USDC", "binance:AMZN-USDT", "binance:BTC-USDC"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %s in %v", key, mapKeysOf(got))
		}
	}
	if len(got) != 4 {
		t.Fatalf("instruments = %v", mapKeysOf(got))
	}
	if got["binance:AMZN-USDT"].Metadata.RawAssetType != "Amazon.com" || got["kraken:BTC-USD"].Identity.SettleAsset != "USD" {
		t.Fatalf("metadata = %+v", got["binance:AMZN-USDT"].Metadata)
	}
}

// publicStub serves the CrossEx public endpoint. Every subscription is
// recorded; the test pushes frames.
func publicStub(t *testing.T) (string, chan map[string]any, chan string) {
	t.Helper()
	requests := make(chan map[string]any, 32)
	frames := make(chan string, 32)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			for {
				var request map[string]any
				if conn.ReadJSON(&request) != nil {
					return
				}
				requests <- request
			}
		}()
		for frame := range frames {
			if conn.WriteMessage(websocket.TextMessage, []byte(frame)) != nil {
				return
			}
		}
	}))
	t.Cleanup(func() { close(frames); server.Close() })
	return "ws" + strings.TrimPrefix(server.URL, "http"), requests, frames
}

func crossExOnly(t *testing.T, symbol string) exchange.Instrument {
	t.Helper()
	parts := strings.Split(symbol, "_")
	instruments, err := MapCrossExOnlyInstruments([]crossex.Symbol{crossExRow(symbol, "0.1", "0.0001", "0.0001", nil, "")}, nil)
	if err != nil || len(instruments) != 1 {
		t.Fatalf("%s: %v %v", parts, instruments, err)
	}
	return instruments[0]
}

func expectSubscription(t *testing.T, requests chan map[string]any, channel, symbol string) {
	t.Helper()
	select {
	case request := <-requests:
		payload, _ := json.Marshal(request["payload"])
		if request["event"] != "subscribe" || request["channel"] != channel || string(payload) != `["`+symbol+`"]` {
			t.Fatalf("subscription = %v, want %s %s", request, channel, symbol)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s subscription", channel)
	}
}

func TestCrossExMarketStreamsBooksTradesAndCandles(t *testing.T) {
	url, requests, frames := publicStub(t)
	market := NewCrossExMarket(nil, Settings{WebSocketEndpoint: url})
	defer market.Close()
	ctx := context.Background()

	// Kraken: no snapshot book; top of book comes from the ticker, each push a
	// complete one-level book.
	kraken := crossExOnly(t, "KRAKEN_FUTURE_BTC_USD")
	books, err := market.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: kraken})
	if err != nil {
		t.Fatal(err)
	}
	defer books.Close()
	expectSubscription(t, requests, "ticker", "KRAKEN_FUTURE_BTC_USD")
	frames <- `{"channel":"ticker","event":"update","result":{"s":"KRAKEN_FUTURE_BTC_USD","lp":"81922.0","bp":"81921.0","bs":"0.1462","ap":"81922.0","as":"0.471","o":"81484.0","h":"82191.0","l":"80714.0","v":"1536.6746","q":null,"ts":1792321329606}}`
	top := <-books.Events()
	if top.Kind != exchange.EventSnapshot || len(top.Bids) != 1 || len(top.Asks) != 1 ||
		!top.Bids[0].Price.Equal(decimal.RequireFromString("81921")) || !top.Asks[0].Quantity.Equal(decimal.RequireFromString("0.471")) {
		t.Fatalf("top of book = %+v", top)
	}

	// Kraken has no candle stream; the refusal is local and typed.
	if _, err := market.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: kraken, Interval: "1m"}); !errors.Is(err, exchange.ErrUnsupported) {
		t.Fatalf("kraken candles err = %v", err)
	}

	// Deribit: 20-level snapshots, candles, trades with case-colliding keys.
	deribit := crossExOnly(t, "DERIBIT_FUTURE_BTC_USDC")
	snapshots, _ := market.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: deribit})
	defer snapshots.Close()
	expectSubscription(t, requests, "order_book_20", "DERIBIT_FUTURE_BTC_USDC")
	frames <- `{"channel":"order_book_20","event":"update","result":{"ts":5,"s":"DERIBIT_FUTURE_BTC_USDC","a":[["82009.7","0.79"]],"b":[["82009.6","3.22"]]}}`
	if snapshot := <-snapshots.Events(); snapshot.Kind != exchange.EventSnapshot || len(snapshot.Bids) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	candles, _ := market.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: deribit, Interval: "1m"})
	defer candles.Close()
	expectSubscription(t, requests, "kline_1m", "DERIBIT_FUTURE_BTC_USDC")
	frames <- `{"channel":"kline_1m","event":"update","result":{"s":"DERIBIT_FUTURE_BTC_USDC","o":"81943.2","h":"81943.2","l":"81941.7","c":"81941.8","v":"2.0858","t":1792326420000,"T":1792326480000,"x":false}}`
	if candle := <-candles.Events(); candle.Candle.OpenTime != time.UnixMilli(1792326420000).UTC() || candle.Candle.Complete || !candle.Candle.Volume.Equal(decimal.RequireFromString("2.0858")) {
		t.Fatalf("candle = %+v", candle)
	}

	trades, _ := market.WatchPublicTrades(ctx, exchange.WatchRequest{Instrument: deribit})
	defer trades.Close()
	expectSubscription(t, requests, "trade", "DERIBIT_FUTURE_BTC_USDC")
	frames <- `{"channel":"trade","event":"update","result":{"s":"DERIBIT_FUTURE_BTC_USDC","i":"9","p":"82009.7","q":"0.0016","S":"SELL","ts":7}}`
	if trade := <-trades.Events(); trade.Side != exchange.SideSell || trade.TradeID != "9" || trade.Instrument.Identity.Venue != exchange.VenueDeribit {
		t.Fatalf("trade = %+v", trade)
	}

	// History does not exist.
	if _, err := market.Candles(ctx, exchange.CandlesRequest{Instrument: deribit}); !errors.Is(err, exchange.ErrUnsupported) {
		t.Fatalf("history err = %v", err)
	}
	// A native-backed instrument is not served here.
	if _, err := market.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: crossExNatives()[0]}); err == nil {
		t.Fatal("native instrument accepted by the crossex market")
	}
}

func TestCrossExMarketReferenceMergesMarkAndFunding(t *testing.T) {
	url, requests, frames := publicStub(t)
	market := NewCrossExMarket(nil, Settings{WebSocketEndpoint: url})
	defer market.Close()
	instrument := crossExOnly(t, "BINANCE_FUTURE_AMZN_USDT")
	reference, err := market.WatchDerivativeReference(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Close()
	channels := map[string]bool{}
	for len(channels) < 2 {
		select {
		case request := <-requests:
			channels[request["channel"].(string)] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("subscriptions = %v", channels)
		}
	}
	if !channels["mark_price"] || !channels["funding_rate"] {
		t.Fatalf("subscriptions = %v", channels)
	}
	frames <- `{"channel":"mark_price","event":"update","result":{"s":"BINANCE_FUTURE_AMZN_USDT","mp":"249.73529376"},"time_ms":1}`
	frames <- `{"channel":"funding_rate","event":"update","result":{"s":"BINANCE_FUTURE_AMZN_USDT","r":"0.0001","T":1792339200000},"time_ms":2}`
	<-reference.Events()
	last := <-reference.Events()
	if !last.MarkPrice.Valid || !last.FundingRate.Valid || last.NextFundingTime != time.UnixMilli(1792339200000).UTC() {
		t.Fatalf("reference = %+v", last)
	}
}
