package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/optionmath"
	"github.com/QuantProcessing/keeluni/sdk/okx"
	"github.com/shopspring/decimal"
)

// okxOptionsFixture is one live read (2026-10-21) of two strikes of one
// expiry in each BTC family: coin-margined BTC-USD and USD-margined
// BTC-USD_UM.
type okxOptionsFixture struct {
	Instruments  []json.RawMessage `json:"instruments"`
	TickBands    []json.RawMessage `json:"tickBands"`
	Summary      []json.RawMessage `json:"summary"`
	Tickers      []json.RawMessage `json:"tickers"`
	Marks        []json.RawMessage `json:"marks"`
	OpenInterest []json.RawMessage `json:"openInterest"`
	Index        []json.RawMessage `json:"index"`
}

// okxOptionStub serves the fixture as OKX's public endpoints and records
// signed calls.
type okxOptionStub struct {
	server  *httptest.Server
	mu      sync.Mutex
	orders  []map[string]any
	paths   []string
	acctLv  string
	private map[string]string
}

func newOKXOptionStub(t *testing.T) *okxOptionStub {
	t.Helper()
	raw, err := os.ReadFile("testdata/okx_options.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture okxOptionsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	family := func(rows []json.RawMessage, request *http.Request) []json.RawMessage {
		want := request.URL.Query().Get("instFamily")
		if want == "" {
			return rows
		}
		var out []json.RawMessage
		for _, row := range rows {
			var probe struct {
				InstId     string `json:"instId"`
				InstFamily string `json:"instFamily"`
			}
			_ = json.Unmarshal(row, &probe)
			if probe.InstFamily == want || okxOptionFamily(probe.InstId) == want {
				out = append(out, row)
			}
		}
		return out
	}
	stub := &okxOptionStub{acctLv: "2", private: map[string]string{}}
	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		stub.mu.Lock()
		stub.paths = append(stub.paths, request.URL.Path)
		stub.mu.Unlock()
		reply := func(data any) {
			_ = json.NewEncoder(writer).Encode(map[string]any{"code": "0", "msg": "", "data": data})
		}
		switch request.URL.Path {
		case "/api/v5/public/underlying":
			reply([][]string{{"BTC-USD"}})
		case "/api/v5/public/instrument-tick-bands":
			reply(fixture.TickBands)
		case "/api/v5/public/instruments":
			reply(fixture.Instruments)
		case "/api/v5/public/opt-summary":
			reply(family(fixture.Summary, request))
		case "/api/v5/market/tickers":
			reply(family(fixture.Tickers, request))
		case "/api/v5/public/mark-price":
			reply(family(fixture.Marks, request))
		case "/api/v5/public/open-interest":
			reply(family(fixture.OpenInterest, request))
		case "/api/v5/market/index-tickers":
			reply(fixture.Index)
		case "/api/v5/account/config":
			reply([]map[string]string{{"acctLv": stub.acctLv}})
		case "/api/v5/trade/order":
			var body map[string]any
			data, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(data, &body)
			stub.mu.Lock()
			stub.orders = append(stub.orders, body)
			stub.mu.Unlock()
			reply([]map[string]string{{"ordId": "777", "clOrdId": body["clOrdId"].(string), "sCode": "0"}})
		default:
			if body, ok := stub.private[request.URL.Path]; ok {
				_, _ = io.WriteString(writer, body)
				return
			}
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *okxOptionStub) settings(t *testing.T) Settings {
	t.Helper()
	catalog := exchange.NewDefaultMemoryCatalog()
	settings := Settings{Endpoint: stub.server.URL, WebSocketEndpoint: "ws://127.0.0.1:1/ws", HTTPClient: stub.server.Client(), InstrumentCatalog: catalog}
	settings.SourceDescriptor = exchange.SourceDescriptor{Name: "okx-option", Endpoint: settings.Endpoint, WebSocketEndpoint: settings.WebSocketEndpoint, Transport: "test"}
	settings.CatalogScope = exchange.CatalogScope{Venue: exchange.VenueOKX, Environment: exchange.EnvironmentLive, Product: exchange.ProductOption,
		EndpointFingerprint: settings.SourceDescriptor.Fingerprint()}
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, NewOKXOptionInstrumentLoader(settings)); err != nil {
		t.Fatal(err)
	}
	return settings
}

func okxTestOption(t *testing.T, client *okxOptionClient, native string) exchange.Instrument {
	t.Helper()
	instrument, err := client.ResolveInstrument(context.Background(), native)
	if err != nil {
		t.Fatal(err)
	}
	return instrument
}

func TestOKXOptionCatalogNormalizesBothFamilies(t *testing.T) {
	stub := newOKXOptionStub(t)
	client := NewOKXOption("key", "secret", "pass", stub.settings(t)).(*okxOptionClient)
	defer client.Close()
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 8 {
		t.Fatalf("instruments = %d", len(instruments))
	}
	coin := okxTestOption(t, client, "BTC-USD-261030-82000-C")
	usd := okxTestOption(t, client, "BTC-USD_UM-261106-82000-P")
	// Coin-margined: premium and settlement in BTC, strike in USD, inverse.
	if coin.Identity.Symbol != "BTC-USD-20261030-82000-C" || coin.Identity.SettleAsset != "BTC" || coin.Option.PremiumAsset != "BTC" ||
		!coin.Option.Inverse || coin.Option.Index != "BTC-USD" || coin.Option.Expiry.Hour() != 8 {
		t.Fatalf("coin = %+v %+v", coin.Identity, coin.Option)
	}
	// One contract is 0.01 BTC: the quantity step and minimum are in BTC.
	if !coin.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) || !coin.Rules.MinQuantity.Equal(decimal.RequireFromString("0.01")) ||
		!coin.Encoding.ContractMultiplier.Value.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("coin rules = %+v %+v", coin.Rules, coin.Encoding)
	}
	// The coin family's tick bands: 0.0001 below 0.005, 0.0005 above.
	if !coin.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.0001")) || !coin.Rules.CoarseTick.Value.Equal(decimal.RequireFromString("0.0005")) ||
		!coin.Rules.CoarseTickFrom.Value.Equal(decimal.RequireFromString("0.005")) {
		t.Fatalf("coin ticks = %+v", coin.Rules)
	}
	// USD-margined: premium in USD, its own symbol so identical terms of the
	// two families never collide; one band only, so no coarse tick.
	if usd.Identity.Symbol != "BTC-USDM-20261106-82000-P" || usd.Identity.QuoteAsset != "USD" || usd.Identity.SettleAsset != "USD" || usd.Option.PremiumAsset != "USD" ||
		usd.Option.Inverse || usd.Rules.CoarseTick.Valid || !usd.Rules.PriceIncrement.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("usd = %+v %+v %+v", usd.Identity, usd.Option, usd.Rules)
	}
}

// The chain carries OKX's forward, IVs and USD greeks, quantities in BTC,
// and a mark that Black-76 on the forward reproduces: the uniform model
// prices OKX options without a venue-specific rate.
func TestOKXOptionChainMatchesBlack76OnTheForward(t *testing.T) {
	stub := newOKXOptionStub(t)
	client := NewOKXOption("key", "secret", "pass", stub.settings(t)).(*okxOptionClient)
	defer client.Close()
	quotes, err := client.OptionChain(context.Background(), exchange.OptionChainRequest{Underlying: "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 8 {
		t.Fatalf("quotes = %d", len(quotes))
	}
	for _, quote := range quotes {
		terms, contract := quote.Instrument.Option, quote.Instrument.Encoding.ContractMultiplier.Value
		if !quote.UnderlyingPrice.Valid || !quote.IndexPrice.Valid || !quote.MarkIV.Valid || !quote.Mark.Valid || quote.Greeks == nil || !quote.OpenInterest.Valid {
			t.Fatalf("%s quote is incomplete: %+v", quote.Instrument.Identity.Symbol, quote)
		}
		if quote.BidSize.Valid && !quote.BidSize.Value.Mod(contract).IsZero() {
			t.Fatalf("%s bid size %s is not in the underlying", quote.Instrument.Identity.Symbol, quote.BidSize.Value)
		}
		// Value one option at its mark IV on the published forward, at the
		// summary's timestamp, in the premium asset.
		forward := quote.UnderlyingPrice.Value.InexactFloat64()
		model := optionmath.OnForward(forward, optionmath.Years(quote.Time, terms.Expiry))
		value := model.Value(terms.Kind == exchange.OptionCall, terms.Strike.InexactFloat64(), quote.MarkIV.Value.InexactFloat64())
		if terms.Inverse {
			value /= forward
		}
		mark := quote.Mark.Value.InexactFloat64()
		if diff := (value - mark) / mark; diff > 0.02 || diff < -0.02 {
			t.Fatalf("%s: Black-76 %.6f vs mark %.6f", quote.Instrument.Identity.Symbol, value, mark)
		}
		greeks := model.Greeks(terms.Kind == exchange.OptionCall, terms.Strike.InexactFloat64(), quote.MarkIV.Value.InexactFloat64())
		if delta := quote.Greeks.Delta.InexactFloat64(); delta-greeks.Delta > 0.02 || greeks.Delta-delta > 0.02 {
			t.Fatalf("%s: delta %.4f vs %.4f", quote.Instrument.Identity.Symbol, delta, greeks.Delta)
		}
	}
}

func TestOKXOptionOrdersAreLimitContractsInTheAccountMode(t *testing.T) {
	stub := newOKXOptionStub(t)
	client := NewOKXOption("key", "secret", "pass", stub.settings(t)).(*okxOptionClient)
	defer client.Close()
	ctx := context.Background()
	coin := okxTestOption(t, client, "BTC-USD-261030-82000-C")
	request := exchange.PlaceOrderRequest{Instrument: coin, Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("0.25"),
		LimitPrice: decimal.RequireFromString("0.0265"), LimitPolicy: exchange.LimitPolicyResting, ClientOrderID: "123"}
	ack, err := client.PlaceOrder(ctx, request)
	if err != nil || ack.OrderID != "777" {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	order := stub.orders[0]
	// 0.25 BTC is 25 contracts of 0.01; Futures mode trades options cross.
	if order["sz"] != "25" || order["tdMode"] != "cross" || order["ordType"] != "limit" || order["px"] != "0.0265" || order["reduceOnly"] != nil {
		t.Fatalf("order = %+v", order)
	}
	for name, change := range map[string]func(*exchange.PlaceOrderRequest){
		"market": func(r *exchange.PlaceOrderRequest) {
			r.Type, r.LimitPrice, r.LimitPolicy = exchange.OrderTypeMarket, decimal.Zero, ""
		},
		"reduce-only": func(r *exchange.PlaceOrderRequest) { r.ReduceOnly = true },
		"off-tick":    func(r *exchange.PlaceOrderRequest) { r.LimitPrice = decimal.RequireFromString("0.0263") },
		"off-lot":     func(r *exchange.PlaceOrderRequest) { r.Quantity = decimal.RequireFromString("0.255") },
	} {
		changed := request
		change(&changed)
		if _, err := client.PlaceOrder(ctx, changed); !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if len(stub.orders) != 1 {
		t.Fatalf("rejected orders reached the venue: %d", len(stub.orders))
	}
}

func TestOKXOptionTradeModeFollowsTheAccountMode(t *testing.T) {
	for level, want := range map[string]string{"1": "cash", "2": "cross", "3": "cross", "4": "cross"} {
		mode, err := okxOptionTradeModeFromAccountConfig([]okx.AccountConfig{{AcctLv: level}})
		if err != nil || mode != want {
			t.Fatalf("acctLv %s = %q, %v", level, mode, err)
		}
	}
	if _, err := okxOptionTradeModeFromAccountConfig([]okx.AccountConfig{{AcctLv: "9"}}); err == nil {
		t.Fatal("unknown account level accepted")
	}
}

func TestOKXOptionPositionsAndFillsAreInTheUnderlying(t *testing.T) {
	stub := newOKXOptionStub(t)
	stub.private["/api/v5/account/positions"] = `{"code":"0","msg":"","data":[{"instType":"OPTION","instId":"BTC-USD_UM-261106-82000-P","pos":"-30","posSide":"net","avgPx":"2100","markPx":"2021.2","upl":"23.6","mgnMode":"cross"}]}`
	stub.private["/api/v5/trade/fills"] = `{"code":"0","msg":"","data":[{"instType":"OPTION","instId":"BTC-USD-261030-82000-C","tradeId":"9","ordId":"777","clOrdId":"123","side":"buy","fillPx":"0.0265","fillSz":"25","fee":"-0.00003","feeCcy":"BTC","execType":"M","ts":"1792539125088"}]}`
	client := NewOKXOption("key", "secret", "pass", stub.settings(t)).(*okxOptionClient)
	defer client.Close()
	ctx := context.Background()
	positions, err := client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil || len(positions) != 1 {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	if !positions[0].Quantity.Equal(decimal.RequireFromString("-0.3")) || positions[0].Side != exchange.SideSell {
		t.Fatalf("position = %+v", positions[0])
	}
	fills, err := client.Fills(ctx, exchange.FillsRequest{})
	if err != nil || len(fills.Fills) != 1 || !fills.Fills[0].Quantity.Equal(decimal.RequireFromString("0.25")) || fills.Fills[0].Liquidity != exchange.LiquidityMaker {
		t.Fatalf("fills = %+v, %v", fills, err)
	}
}

// One opt-summary subscription per family feeds every streamed option of
// it; the book top is scaled into the underlying.
func TestOKXOptionQuoteStreamSharesTheFamilySummary(t *testing.T) {
	fake := &fakeOKXPublicWSClient{}
	backend := newOKXPerpWSBackendWithClient(fake, nil).(*okxPublicWSBackend)
	backend.meta = clientMeta{venue: exchange.VenueOKX, product: exchange.ProductOption}
	stub := newOKXOptionStub(t)
	client := NewOKXOption("key", "secret", "pass", stub.settings(t)).(*okxOptionClient)
	defer client.Close()
	call := okxTestOption(t, client, "BTC-USD-261030-82000-C")
	put := okxTestOption(t, client, "BTC-USD-261030-82000-P")
	var mu sync.Mutex
	got := map[string]exchange.OptionQuote{}
	start := func(instrument exchange.Instrument) func() error {
		stop, err := backend.StartOptionQuote(context.Background(), instrument, streamCallbacks[exchange.OptionQuote]{Event: func(quote exchange.OptionQuote) {
			mu.Lock()
			got[quote.Instrument.Identity.NativeSymbol] = quote
			mu.Unlock()
		}})
		if err != nil {
			t.Fatal(err)
		}
		return stop
	}
	stopCall, stopPut := start(call), start(put)
	if len(fake.summaryFamilies) != 1 || fake.summaryFamilies[0] != "BTC-USD" {
		t.Fatalf("summary subscriptions = %v", fake.summaryFamilies)
	}
	fake.bboHandler(&okx.OrderBook{Bids: [][]string{{"0.0185", "120", "0", "3"}}, Asks: [][]string{{"0.019", "40", "0", "1"}}, Ts: "1792539125088"})
	fake.summaryHandler(&okx.OptionSummary{InstId: put.Identity.NativeSymbol, MarkVol: "0.31", FwdPx: "82924.3", DeltaBS: "-0.47", GammaBS: "0.00004", VegaBS: "61", ThetaBS: "-120", Ts: "1792539125088"})
	mu.Lock()
	quote := got[put.Identity.NativeSymbol]
	mu.Unlock()
	if !quote.MarkIV.Value.Equal(decimal.RequireFromString("0.31")) || !quote.UnderlyingPrice.Valid || quote.Greeks == nil || !quote.Greeks.Delta.Equal(decimal.RequireFromString("-0.47")) {
		t.Fatalf("put quote = %+v", quote)
	}
	// The fake keeps the last bbo handler (the put's): 120 contracts is 1.2 BTC.
	if !quote.BidSize.Value.Equal(decimal.RequireFromString("1.2")) || !quote.Bid.Value.Equal(decimal.RequireFromString("0.0185")) {
		t.Fatalf("put book top = %+v / %+v", quote.Bid, quote.BidSize)
	}
	if _, ok := got[call.Identity.NativeSymbol]; ok {
		t.Fatal("the put's summary reached the call's stream")
	}
	_ = stopCall()
	for _, args := range fake.unsubscribed {
		if args.Channel == "opt-summary" {
			t.Fatal("summary unsubscribed while an option still streams")
		}
	}
	_ = stopPut()
	found := false
	for _, args := range fake.unsubscribed {
		found = found || (args.Channel == "opt-summary" && args.InstFamily == "BTC-USD")
	}
	if !found {
		t.Fatalf("summary not unsubscribed: %+v", fake.unsubscribed)
	}
}
