package factoryclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

type wantQuote struct {
	native   string
	nativeID string
	rate     string
	hours    int64  // 0 = unknown
	next     int64  // unix seconds; 0 = unknown; -1 = next whole hour
	apr      string // "" = absent
	rateCap  string
}

func fundingScope(venue exchange.Venue, settlement string) exchange.CatalogScope {
	return exchange.CatalogScope{Venue: venue, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: settlement}
}

// TestFundingBoardReadersNormalizeEveryVenue pins each venue's mapping onto one
// meaning: Rate per settlement at NextFundingTime, the instrument's own period,
// and an annualized value only when the period is known. The fixtures are
// trimmed copies of live replies and include the rows that must not survive.
func TestFundingBoardReadersNormalizeEveryVenue(t *testing.T) {
	cases := []struct {
		name       string
		venue      exchange.Venue
		settlement string
		responses  map[string]string
		want       []wantQuote
	}{
		{
			name: "binance: fundingInfo lists only deviations; absent means the 8h default", venue: exchange.VenueBinance, settlement: "USDT",
			responses: map[string]string{
				"/fapi/v1/premiumIndex": `[
					{"symbol":"BTCUSDT","lastFundingRate":"0.00010000","nextFundingTime":1792339200000,"time":1792330000000},
					{"symbol":"DOGEUSDT","lastFundingRate":"-0.00020000","nextFundingTime":1792339200000,"time":1792330000000},
					{"symbol":"ETHUSDC","lastFundingRate":"0.00005000","nextFundingTime":1792339200000,"time":1792330000000},
					{"symbol":"BTCUSDT_261225","lastFundingRate":"0.00000000","nextFundingTime":0,"time":1792330000000}
				]`,
				"/fapi/v1/fundingInfo": `[{"symbol":"DOGEUSDT","fundingIntervalHours":4,"adjustedFundingRateCap":"0.02","adjustedFundingRateFloor":"-0.02"}]`,
			},
			want: []wantQuote{
				{native: "BTCUSDT", rate: "0.0001", hours: 8, next: 1792339200, apr: "0.1095"},
				{native: "DOGEUSDT", rate: "-0.0002", hours: 4, next: 1792339200, apr: "-0.438"},
			},
		},
		{
			name: "okx: fundingTime is the next settlement; nextFundingTime is the one after", venue: exchange.VenueOKX, settlement: "USDT",
			responses: map[string]string{
				"/api/v5/public/funding-rate": `{"code":"0","msg":"","data":[
					{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.0000639557541469","fundingTime":"1792339200000","nextFundingTime":"1792368000000","maxFundingRate":"0.00375","minFundingRate":"-0.00375","ts":"1792337605142"},
					{"instType":"SWAP","instId":"XTZ-USDT-SWAP","fundingRate":"0.00005","fundingTime":"1792339200000","nextFundingTime":"1792353600000","ts":"1792337605142"},
					{"instType":"SWAP","instId":"BTC-USD-SWAP","fundingRate":"0.0001","fundingTime":"1792339200000","nextFundingTime":"1792368000000","ts":"1792337605142"},
					{"instType":"FUTURES","instId":"XAU-USD_UM_XPERP-310502","fundingRate":"0","fundingTime":"1792339200000","nextFundingTime":"1792368000000","ts":"1792337605142"},
					{"instType":"SWAP","instId":"ETH-USDT-SWAP","fundingRate":"","fundingTime":"1792339200000","nextFundingTime":"1792368000000","ts":"1792337605142"}
				]}`,
			},
			want: []wantQuote{
				{native: "BTC-USDT-SWAP", rate: "0.0000639557541469", hours: 8, next: 1792339200, apr: "0.0700315507908555", rateCap: "0.00375"},
				{native: "XTZ-USDT-SWAP", rate: "0.00005", hours: 4, next: 1792339200, apr: "0.1095"},
			},
		},
		{
			name: "bybit: linear tickers carry everything; settlement from the symbol", venue: exchange.VenueBybit, settlement: "USDC",
			responses: map[string]string{
				"/v5/market/tickers": `{"retCode":0,"retMsg":"OK","time":1792330000000,"result":{"category":"linear","list":[
					{"symbol":"BTCUSDT","fundingRate":"0.0001","nextFundingTime":"1792339200000","fundingIntervalHour":"8"},
					{"symbol":"BTCPERP","fundingRate":"0.00002","nextFundingTime":"1792339200000","fundingIntervalHour":"8"},
					{"symbol":"ETHUSDC","fundingRate":"0.00004","nextFundingTime":"1792324800000","fundingIntervalHour":"4"}
				]}}`,
			},
			want: []wantQuote{
				{native: "BTCPERP", rate: "0.00002", hours: 8, next: 1792339200, apr: "0.0219"},
				{native: "ETHUSDC", rate: "0.00004", hours: 4, next: 1792324800, apr: "0.0876"},
			},
		},
		{
			name: "bitget: current-fund-rate has period and clock; tickers would not", venue: exchange.VenueBitget, settlement: "USDT",
			responses: map[string]string{
				"/api/v2/mix/market/current-fund-rate": `{"code":"00000","msg":"success","requestTime":1792326676064,"data":[
					{"symbol":"BTCUSDT","fundingRate":"0.000062","fundingRateInterval":"8","nextUpdate":"1792339200000","minFundingRate":"-0.003","maxFundingRate":"0.003"},
					{"symbol":"XTZUSDT","fundingRate":"-0.0001","fundingRateInterval":"4","nextUpdate":"1792339200000","minFundingRate":"-0.02","maxFundingRate":"0.02"}
				]}`,
			},
			want: []wantQuote{
				{native: "BTCUSDT", rate: "0.000062", hours: 8, next: 1792339200, apr: "0.06789", rateCap: "0.003"},
				{native: "XTZUSDT", rate: "-0.0001", hours: 4, next: 1792339200, apr: "-0.219", rateCap: "0.02"},
			},
		},
		{
			name: "gate: period in seconds from contracts; a delisting contract has no clock", venue: exchange.VenueGate, settlement: "USDT",
			responses: map[string]string{
				"/futures/usdt/tickers": `[
					{"contract":"BTC_USDT","funding_rate":"0.000044"},
					{"contract":"OLD_USDT","funding_rate":"0.0003"}
				]`,
				"/futures/usdt/contracts": `[
					{"name":"BTC_USDT","funding_interval":28800,"funding_next_apply":4102444800,"status":"trading"},
					{"name":"OLD_USDT","funding_interval":28800,"funding_next_apply":4102444800,"status":"delisting"}
				]`,
			},
			want: []wantQuote{
				{native: "BTC_USDT", rate: "0.000044", hours: 8, next: 4102444800, apr: "0.04818"},
				{native: "OLD_USDT", rate: "0.0003"},
			},
		},
		{
			name: "lighter: the board quotes an 8h equivalent of hourly settlements", venue: exchange.VenueLighter, settlement: "USDC",
			responses: map[string]string{
				"/api/v1/funding-rates": `{"code":200,"funding_rates":[
					{"market_id":1,"exchange":"lighter","symbol":"BTC","rate":0.000096},
					{"market_id":1,"exchange":"binance","symbol":"BTC","rate":0.0001},
					{"market_id":30,"exchange":"lighter","symbol":"UNI","rate":-0.000128}
				]}`,
			},
			want: []wantQuote{
				// 0.000096 / 8 = 0.000012 per hour, the stream's 0.0012 %/h.
				{native: "BTC", nativeID: "1", rate: "0.000012", hours: 1, next: -1, apr: "0.10512"},
				{native: "UNI", nativeID: "30", rate: "-0.000016", hours: 1, next: -1, apr: "-0.14016"},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := fundingBoardFixtureServer(t, testCase.responses)
			scope := fundingScope(testCase.venue, testCase.settlement)
			before := time.Now()
			board, err := NewFundingBoardReader(scope, Settings{Endpoint: server.URL, HTTPClient: server.Client()}).LoadFundingBoard(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := board.Validate(testCase.venue); err != nil {
				t.Fatal(err)
			}
			if len(board.Quotes) != len(testCase.want) {
				t.Fatalf("quotes = %+v, want %d", board.Quotes, len(testCase.want))
			}
			for index, want := range testCase.want {
				assertQuote(t, board.Quotes[index], want, before)
			}
		})
	}
}

func assertQuote(t *testing.T, got exchange.FundingQuote, want wantQuote, before time.Time) {
	t.Helper()
	if got.NativeSymbol != want.native || got.NativeID != want.nativeID {
		t.Fatalf("identity = %s/%s, want %s/%s", got.NativeSymbol, got.NativeID, want.native, want.nativeID)
	}
	if !got.Rate.Equal(decimal.RequireFromString(want.rate)) {
		t.Fatalf("%s rate = %s, want %s", want.native, got.Rate, want.rate)
	}
	if (want.hours == 0) == got.IntervalHours.Valid || (want.hours != 0 && got.IntervalHours.Value != want.hours) {
		t.Fatalf("%s period = %+v, want %d", want.native, got.IntervalHours, want.hours)
	}
	switch want.next {
	case 0:
		if !got.NextFundingTime.IsZero() {
			t.Fatalf("%s next = %s, want none", want.native, got.NextFundingTime)
		}
	case -1:
		if expected := before.UTC().Truncate(time.Hour).Add(time.Hour); !got.NextFundingTime.Equal(expected) && !got.NextFundingTime.Equal(expected.Add(time.Hour)) {
			t.Fatalf("%s next = %s, want the next whole hour", want.native, got.NextFundingTime)
		}
	default:
		if got.NextFundingTime.Unix() != want.next {
			t.Fatalf("%s next = %d, want %d", want.native, got.NextFundingTime.Unix(), want.next)
		}
	}
	apr := got.Annualized()
	if (want.apr == "") == apr.Valid || (want.apr != "" && !apr.Value.Equal(decimal.RequireFromString(want.apr))) {
		t.Fatalf("%s annualized = %+v, want %q", want.native, apr, want.apr)
	}
	if want.rateCap != "" && (!got.RateCap.Valid || !got.RateCap.Value.Equal(decimal.RequireFromString(want.rateCap))) {
		t.Fatalf("%s cap = %+v, want %s", want.native, got.RateCap, want.rateCap)
	}
	if got.ObservedAt.IsZero() {
		t.Fatalf("%s has no observation time", want.native)
	}
}

func TestBinanceFundingPeriodStaysUnknownWithoutFundingInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/fapi/v1/fundingInfo" {
			http.Error(writer, `{"code":-1000,"msg":"down"}`, http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(writer, `[{"symbol":"BTCUSDT","lastFundingRate":"0.0001","nextFundingTime":1792339200000,"time":1792330000000}]`)
	}))
	defer server.Close()
	board, err := NewFundingBoardReader(fundingScope(exchange.VenueBinance, "USDT"), Settings{Endpoint: server.URL, HTTPClient: server.Client()}).LoadFundingBoard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Without fundingInfo the 8h default cannot be told apart from a 4h
	// deviation, so the period (and the annualized rate) must stay absent.
	if quote := board.Quotes[0]; quote.IntervalHours.Valid || quote.Annualized().Valid {
		t.Fatalf("period assumed without fundingInfo: %+v", quote)
	}
}

func TestGateClockRollsForwardPastASettlement(t *testing.T) {
	now := time.Date(2026, 10, 19, 8, 0, 1, 0, time.UTC)
	stale := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	if got := rollForward(stale, 8, now); !got.Equal(stale.Add(8 * time.Hour)) {
		t.Fatalf("rollForward = %s", got)
	}
	if got := rollForward(stale.Add(time.Hour), 8, now); !got.Equal(stale.Add(time.Hour)) {
		t.Fatalf("future clock moved: %s", got)
	}
}

func TestFundingBoardValidateRejectsWrongNumbers(t *testing.T) {
	now := time.Now()
	scope := fundingScope(exchange.VenueOKX, "USDT")
	good := exchange.FundingQuote{NativeSymbol: "BTC-USDT-SWAP", ObservedAt: now}
	for name, board := range map[string]exchange.FundingBoard{
		"foreign venue":  {Scope: fundingScope(exchange.VenueGate, "USDT"), ObservedAt: now, Quotes: []exchange.FundingQuote{good}},
		"duplicate":      {Scope: scope, ObservedAt: now, Quotes: []exchange.FundingQuote{good, good}},
		"no symbol":      {Scope: scope, ObservedAt: now, Quotes: []exchange.FundingQuote{{ObservedAt: now}}},
		"absurd period":  {Scope: scope, ObservedAt: now, Quotes: []exchange.FundingQuote{{NativeSymbol: "X", ObservedAt: now, IntervalHours: exchange.OptionalInt{Value: 28800, Valid: true}}}},
		"no observation": {Scope: scope, Quotes: []exchange.FundingQuote{good}},
	} {
		if err := board.Validate(exchange.VenueOKX); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHyperliquidFundingDirectoryKeepsOnlyListedSettlementMarkets(t *testing.T) {
	var metas []hyperliquidperp.PrepMeta
	if err := json.Unmarshal([]byte(`[
		{"universe":[{"name":"BTC","szDecimals":5},{"name":"MATIC","szDecimals":1,"isDelisted":true},{"name":"ETH","szDecimals":4}],"collateralToken":0},
		{"universe":[{"name":"xyz:TSLA","szDecimals":2}],"collateralToken":0},
		{"universe":[{"name":"flx:GOLD","szDecimals":2}],"collateralToken":360}
	]`), &metas); err != nil {
		t.Fatal(err)
	}
	directory, err := buildHyperliquidFundingDirectory(metas, map[int]string{0: "", 1: "xyz", 2: "flx"}, map[int]string{0: "USDC", 360: "USDH"}, "USDC", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stream := NewHyperliquidFundingStream(fundingScope(exchange.VenueHyperliquid, "USDC"), Settings{})
	board := stream.board(directory, hyperliquid.WsAllDexsAssetCtxs{Ctxs: map[string][]hyperliquid.WsAssetCtx{
		"":    {{Funding: "0.0000125"}, {Funding: "0.0"}, {Funding: "-0.00001"}},
		"xyz": {{Funding: "0.00002"}},
		"flx": {{Funding: "0.5"}},
	}})
	got := map[string]string{}
	for _, quote := range board.Quotes {
		got[quote.NativeSymbol+"#"+quote.NativeID] = quote.Rate.String()
		if quote.IntervalHours.Value != 1 || quote.NextFundingTime.IsZero() {
			t.Fatalf("hourly clock missing: %+v", quote)
		}
	}
	want := map[string]string{"BTC#0": "0.0000125", "ETH#2": "-0.00001", "xyz:TSLA#110000": "0.00002"}
	if len(got) != len(want) {
		t.Fatalf("quotes = %v, want %v", got, want)
	}
	for key, rate := range want {
		if got[key] != rate {
			t.Fatalf("quotes = %v, want %v", got, want)
		}
	}
	if directory.outgrown(hyperliquid.WsAllDexsAssetCtxs{Ctxs: map[string][]hyperliquid.WsAssetCtx{"": make([]hyperliquid.WsAssetCtx, 4)}}) != true {
		t.Fatal("a grown dex must trigger a directory reload")
	}
	if directory.outgrown(hyperliquid.WsAllDexsAssetCtxs{Ctxs: map[string][]hyperliquid.WsAssetCtx{"flx": make([]hyperliquid.WsAssetCtx, 9)}}) {
		t.Fatal("a foreign-collateral dex must not trigger reloads")
	}
}

// TestHyperliquidFundingStreamEmitsCompleteBoards drives the stream against a
// fake venue: REST directory reads, one WebSocket subscription, and a pushed
// allDexsAssetCtxs frame in the live tuple-list shape.
func TestHyperliquidFundingStreamEmitsCompleteBoards(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(writer, request, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			var command struct {
				Method       string          `json:"method"`
				Subscription json.RawMessage `json:"subscription"`
			}
			if conn.ReadJSON(&command) != nil || !strings.Contains(string(command.Subscription), "allDexsAssetCtxs") {
				return
			}
			_ = conn.WriteJSON(map[string]any{"channel": "subscriptionResponse", "data": map[string]any{"method": "subscribe", "subscription": json.RawMessage(command.Subscription)}})
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"channel":"allDexsAssetCtxs","data":{"ctxs":[["",[{"funding":"0.0000125","markPx":"64000"}]],["xyz",[{"funding":"-0.00002"}]]]}}`))
			_, _, _ = conn.ReadMessage()
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		switch payload["type"] {
		case "allPerpMetas":
			_, _ = io.WriteString(writer, `[{"universe":[{"name":"BTC","szDecimals":5}],"collateralToken":0},{"universe":[{"name":"xyz:TSLA","szDecimals":2}],"collateralToken":0}]`)
		case "perpDexs":
			_, _ = io.WriteString(writer, `[null,{"name":"xyz","fullName":"XYZ"}]`)
		case "spotMeta":
			_, _ = io.WriteString(writer, `{"tokens":[{"name":"USDC","index":0,"szDecimals":8,"weiDecimals":8}],"universe":[]}`)
		default:
			t.Errorf("unexpected info request %v", payload["type"])
		}
	}))
	defer server.Close()
	settings := Settings{Endpoint: server.URL, HTTPClient: server.Client(), WebSocketEndpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"}
	stream := NewHyperliquidFundingStream(fundingScope(exchange.VenueHyperliquid, "USDC"), settings)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	boards := make(chan exchange.FundingBoard, 1)
	go func() {
		_ = stream.StreamFundingBoard(ctx, func(board exchange.FundingBoard) {
			select {
			case boards <- board:
			default:
			}
		})
	}()
	select {
	case board := <-boards:
		if err := board.Validate(exchange.VenueHyperliquid); err != nil {
			t.Fatal(err)
		}
		if len(board.Quotes) != 2 {
			t.Fatalf("quotes = %+v", board.Quotes)
		}
	case <-ctx.Done():
		t.Fatal("no board emitted")
	}
}
