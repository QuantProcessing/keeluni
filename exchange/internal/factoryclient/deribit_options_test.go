package factoryclient

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

func newTestDeribitProduct(t *testing.T, stub *deribitStub, product exchange.Product) Settings {
	t.Helper()
	catalog := exchange.NewDefaultMemoryCatalog()
	settings := Settings{Endpoint: stub.server.URL, WebSocketEndpoint: "ws" + strings.TrimPrefix(stub.server.URL, "http") + "/ws/api/v2",
		HTTPClient: stub.server.Client(), InstrumentCatalog: catalog}
	settings.SourceDescriptor = exchange.SourceDescriptor{Name: "deribit-" + string(product), Endpoint: settings.Endpoint, WebSocketEndpoint: settings.WebSocketEndpoint, Transport: "test"}
	settings.CatalogScope = exchange.CatalogScope{Venue: exchange.VenueDeribit, Environment: exchange.EnvironmentLive, Product: product,
		EndpointFingerprint: settings.SourceDescriptor.Fingerprint()}
	loader := NewDeribitOptionInstrumentLoader(settings)
	if product == exchange.ProductSpot {
		loader = NewDeribitSpotInstrumentLoader(settings)
	}
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, loader); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestDeribitOptionCatalogNormalizesTerms(t *testing.T) {
	stub := newDeribitStub(t)
	stub.set("public/get_instruments", "@options_instruments.json")
	client := NewDeribitOption("id", "secret", newTestDeribitProduct(t, stub, exchange.ProductOption)).(*deribitOptionClient)
	defer client.Close()
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The fixture holds one BTC expiry and two SOL_USDC options; the loader
	// reads BTC, ETH and USDC, each served the same file, so symbols must
	// still be unique after filtering nothing out.
	var inverse, linear *exchange.Instrument
	for index := range instruments {
		instrument := &instruments[index]
		if instrument.Identity.Product != exchange.ProductOption || instrument.Metadata.MarketFamily != exchange.MarketFamilyOption {
			t.Fatalf("instrument = %+v", instrument.Identity)
		}
		if instrument.Option.Inverse && inverse == nil {
			inverse = instrument
		}
		if !instrument.Option.Inverse && linear == nil {
			linear = instrument
		}
	}
	if inverse == nil || linear == nil {
		t.Fatalf("inverse=%v linear=%v", inverse, linear)
	}
	if inverse.Identity.QuoteAsset != "USD" || inverse.Identity.SettleAsset != "BTC" || inverse.Option.PremiumAsset != "BTC" ||
		!strings.HasPrefix(inverse.Identity.Symbol, "BTC-USD-2026") || !inverse.Rules.CoarseTick.Valid {
		t.Fatalf("inverse = %+v %+v %+v", inverse.Identity, inverse.Option, inverse.Rules)
	}
	if linear.Identity.QuoteAsset != "USDC" || linear.Option.PremiumAsset != "USDC" || !linear.Rules.QuantityIncrement.Equal(decimal.NewFromInt(10)) || !inverse.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("linear = %+v %+v %+v", linear.Identity, linear.Option, linear.Rules)
	}
	if hour := inverse.Option.Expiry.Hour(); hour != 8 {
		t.Fatalf("Deribit expires at 08:00 UTC, got %v", inverse.Option.Expiry)
	}
	resolved, err := client.ResolveInstrument(context.Background(), inverse.Identity.NativeSymbol)
	if err != nil || resolved.Identity.Symbol != inverse.Identity.Symbol {
		t.Fatalf("resolve = %+v, %v", resolved.Identity, err)
	}
}

func TestDeribitOptionSymbolSpellsDecimalStrikes(t *testing.T) {
	expiry := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	if got := DeribitOptionSymbol("AVAX", "USDC", expiry, decimal.RequireFromString("8.2"), exchange.OptionCall); got != "AVAX-USDC-20261020-8.2-C" {
		t.Fatal(got)
	}
}

func TestDeribitOptionChainAndOrders(t *testing.T) {
	stub := newDeribitStub(t)
	stub.set("public/get_instruments", "@options_instruments.json")
	stub.set("public/get_book_summary_by_currency", "@options_summary.json")
	client := NewDeribitOption("id", "secret", newTestDeribitProduct(t, stub, exchange.ProductOption)).(*deribitOptionClient)
	defer client.Close()
	ctx := context.Background()
	chain, err := client.OptionChain(ctx, exchange.OptionChainRequest{Underlying: "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) == 0 {
		t.Fatal("empty chain")
	}
	for _, quote := range chain {
		if quote.Instrument.Option.Underlying != "BTC" {
			t.Fatalf("SOL leaked into the BTC chain: %s", quote.Instrument.Identity.Symbol)
		}
		if quote.MarkIV.Valid && quote.MarkIV.Value.GreaterThan(decimal.NewFromInt(5)) {
			t.Fatalf("IV not a fraction: %v", quote.MarkIV.Value)
		}
	}
	called := map[string]bool{}
	for _, request := range stub.seen {
		if strings.HasSuffix(request.URL.Path, "get_book_summary_by_currency") {
			called[request.URL.Query().Get("currency")+"/"+request.URL.Query().Get("kind")] = true
		}
	}
	if !called["BTC/option"] || called["ETH/option"] {
		t.Fatalf("chain reads = %v", called)
	}

	option := chain[0].Instrument
	stub.set("private/buy", `{"jsonrpc":"2.0","result":{"order":{"order_id":"9","label":"21","instrument_name":"`+option.Identity.NativeSymbol+`","direction":"buy","order_type":"limit","order_state":"open","price":0.01,"amount":0.1,"filled_amount":0},"trades":[]}}`)
	ack, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: option, ClientOrderID: "21", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.1"), LimitPrice: decimal.RequireFromString("0.01"), LimitPolicy: exchange.LimitPolicyResting})
	if err != nil || ack.State != exchange.AckResting || ack.Product != exchange.ProductOption {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	// Above the coarse tick boundary prices must align to the coarse tick.
	if option.Rules.CoarseTick.Valid {
		price := option.Rules.CoarseTickFrom.Value.Add(option.Rules.PriceIncrement)
		_, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: option, ClientOrderID: "22", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
			Quantity: decimal.RequireFromString("0.1"), LimitPrice: price, LimitPolicy: exchange.LimitPolicyResting})
		if err == nil {
			t.Fatal("price off the coarse tick was accepted")
		}
	}
	stub.set("private/get_positions", `{"jsonrpc":"2.0","result":[{"instrument_name":"`+option.Identity.NativeSymbol+`","kind":"option","direction":"sell","size":-0.5,"average_price":0.02,"mark_price":0.015},{"instrument_name":"BTC-PERPETUAL","kind":"future","direction":"buy","size":10}]}`)
	positions, err := client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil || len(positions) != 1 || !positions[0].Quantity.Equal(decimal.RequireFromString("-0.5")) {
		t.Fatalf("positions = %+v, %v", positions, err)
	}
	if query := stub.last("private/get_positions").URL.RawQuery; query != "currency=any&kind=option" {
		t.Fatalf("positions query = %q", query)
	}
	stub.set("private/get_account_summaries", `{"jsonrpc":"2.0","result":{"summaries":[{"currency":"BTC","balance":0.5,"equity":0.51,"available_funds":0.4},{"currency":"USDC","balance":1000,"equity":1010,"available_funds":900,"initial_margin":100,"options_pl":10},{"currency":"SOL","balance":3}]}}`)
	account, err := client.OptionAccount(ctx)
	if err != nil || account.SettleAsset != "USDC" || !account.Available.Value.Equal(decimal.NewFromInt(900)) || len(account.Balances) != 2 {
		t.Fatalf("account = %+v, %v", account, err)
	}
	stub.set("private/get_open_orders", `{"jsonrpc":"2.0","result":[{"order_id":"9","label":"21","instrument_name":"`+option.Identity.NativeSymbol+`","direction":"buy","order_type":"limit","order_state":"open","price":0.01,"amount":0.1,"filled_amount":0}]}`)
	page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{})
	if err != nil || len(page.Orders) != 1 || stub.last("private/get_open_orders").URL.RawQuery != "kind=option" {
		t.Fatalf("open orders = %+v, %v", page, err)
	}
}

func TestDeribitSpotCatalogAndBalances(t *testing.T) {
	stub := newDeribitStub(t)
	stub.set("public/get_instruments", "@spot_instruments.json")
	client := NewDeribitSpot("id", "secret", newTestDeribitProduct(t, stub, exchange.ProductSpot)).(*deribitSpotClient)
	defer client.Close()
	ctx := context.Background()
	instruments, err := client.Instruments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	symbols := map[string]bool{}
	for _, instrument := range instruments {
		symbols[instrument.Identity.Symbol] = true
	}
	if !symbols["BTC-USDC"] || !symbols["STETH-ETH"] || symbols["SOL-ETH"] {
		t.Fatalf("spot symbols = %v (SOL_ETH is locked)", symbols)
	}
	stub.set("private/get_account_summaries", `{"jsonrpc":"2.0","result":{"summaries":[{"currency":"BTC","balance":0.5,"available_funds":0.5},{"currency":"USDC","balance":1000,"available_funds":800}]}}`)
	balances, err := client.Balances(ctx)
	if err != nil || len(balances) != 2 || balances[0].Asset != "BTC" || !balances[1].Locked.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("balances = %+v, %v", balances, err)
	}
	btc, _ := client.ResolveInstrument(ctx, "BTC-USDC")
	if _, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: btc, ClientOrderID: "3", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket,
		Quantity: decimal.RequireFromString("0.001"), ReduceOnly: true}); err == nil {
		t.Fatal("reduce-only accepted on spot")
	}
}
