package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	krakenspot "github.com/QuantProcessing/keeluni/sdk/krakenspot"
	"github.com/shopspring/decimal"
)

type krakenSpotStub struct {
	server *httptest.Server
	mu     sync.Mutex
	routes map[string]string
	bodies map[string]url.Values
}

func newKrakenSpotStub(t *testing.T) *krakenSpotStub {
	t.Helper()
	stub := &krakenSpotStub{routes: map[string]string{
		"/0/public/AssetPairs": "@asset_pairs.json", "/0/public/Assets": "@assets.json",
		"/0/public/Depth": "@depth.json", "/0/public/OHLC": "@ohlc.json", "/0/public/Trades": "@trades.json",
	}, bodies: map[string]url.Values{}}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		stub.mu.Lock()
		stub.bodies[r.URL.Path] = values
		payload, ok := stub.routes[r.URL.Path]
		stub.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasPrefix(payload, "@") {
			data, err := os.ReadFile("../../../sdk/krakenspot/testdata/" + payload[1:])
			if err != nil {
				t.Error(err)
			}
			payload = string(data)
		}
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *krakenSpotStub) set(path, body string) {
	stub.mu.Lock()
	stub.routes[path] = body
	stub.mu.Unlock()
}

func (stub *krakenSpotStub) sent(path string) url.Values {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.bodies[path]
}

func newTestKrakenSpot(t *testing.T, stub *krakenSpotStub) *krakenSpotClient {
	t.Helper()
	catalog := exchange.NewDefaultMemoryCatalog()
	settings := Settings{Endpoint: stub.server.URL, WebSocketEndpoint: "ws" + strings.TrimPrefix(stub.server.URL, "http") + "/ws", HTTPClient: stub.server.Client(), InstrumentCatalog: catalog}
	settings.SourceDescriptor = exchange.SourceDescriptor{Name: "kraken-spot", Endpoint: settings.Endpoint, WebSocketEndpoint: settings.WebSocketEndpoint, Transport: "test"}
	settings.CatalogScope = exchange.CatalogScope{Venue: exchange.VenueKraken, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, EndpointFingerprint: settings.SourceDescriptor.Fingerprint()}
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, NewKrakenSpotInstrumentLoader(settings)); err != nil {
		t.Fatal(err)
	}
	client := NewKrakenSpot("key", "kQH5HW/8p1uGOVjbgWA7FunAmGO8lsSUXNsu3eow76sz84Q18fWxnyRzBHCd3pd5nE9qa99HAZtuZuj6F1huXg==", settings).(*krakenSpotClient)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestKrakenSpotCatalogUsesV2Symbols(t *testing.T) {
	client := newTestKrakenSpot(t, newKrakenSpotStub(t))
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bySymbol := map[string]exchange.Instrument{}
	for _, instrument := range instruments {
		bySymbol[instrument.Identity.Symbol] = instrument
	}
	btc, ok := bySymbol["BTC-USD"]
	if !ok || btc.Identity.NativeSymbol != "BTC/USD" || btc.Identity.NativeID != "XXBTZUSD" || !btc.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.00000001")) || !btc.Rules.MinNotional.Valid {
		t.Fatalf("BTC-USD = %+v %+v", btc.Identity, btc.Rules)
	}
	if _, ok := bySymbol["DOGE-USD"]; !ok {
		t.Fatalf("XDG not renamed: %v", bySymbol)
	}
	if _, ok := bySymbol["ETH-BTC"]; !ok {
		t.Fatalf("ETH/XBT not renamed: %v", bySymbol)
	}
	if _, ok := bySymbol["ACA-EUR"]; ok {
		t.Fatal("a cancel-only pair is listed as tradable")
	}
}

func TestKrakenSpotOrdersAndBalances(t *testing.T) {
	stub := newKrakenSpotStub(t)
	client := newTestKrakenSpot(t, stub)
	ctx := context.Background()
	btc, err := client.ResolveInstrument(ctx, "BTC-USD")
	if err != nil {
		t.Fatal(err)
	}
	stub.set("/0/private/AddOrder", `{"error":[],"result":{"txid":["OUF4EM-FRGI2-MQMWZD"],"descr":{"order":"buy"}}}`)
	ack, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: btc, ClientOrderID: "229835394146432", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.001"), LimitPrice: decimal.NewFromInt(50000), LimitPolicy: exchange.LimitPolicyPostOnly})
	if err != nil || ack.State != exchange.AckAcceptedPending || ack.OrderID != "OUF4EM-FRGI2-MQMWZD" {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	sent := stub.sent("/0/private/AddOrder")
	if sent.Get("pair") != "BTC/USD" || sent.Get("oflags") != "post" || sent.Get("cl_ord_id") != "229835394146432" || sent.Get("volume") != "0.001" {
		t.Fatalf("sent = %v", sent)
	}
	stub.set("/0/private/AddOrder", `{"error":["EOrder:Insufficient funds"]}`)
	ack, err = client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: btc, ClientOrderID: "2", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("0.001")})
	if !errors.Is(err, exchange.ErrVenueRejected) || ack.State != exchange.AckRejected || ack.VenueCode != "EOrder:Insufficient funds" {
		t.Fatalf("rejected = %+v, %v", ack, err)
	}
	stub.set("/0/private/AddOrder", `{"error":["EService:Unavailable"]}`)
	if ack, _ := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: btc, ClientOrderID: "3", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("0.001")}); ack.State == exchange.AckRejected {
		t.Fatal("EService:Unavailable taken as a refusal; Kraken documents such orders may exist")
	}
	stub.set("/0/private/BalanceEx", `{"error":[],"result":{"ZUSD":{"balance":"1000.5","hold_trade":"100"},"XXBT":{"balance":"0.5","hold_trade":"0"},"ETH.F":{"balance":"3"}}}`)
	balances, err := client.Balances(ctx)
	if err != nil || len(balances) != 2 || balances[0].Asset != "BTC" || balances[1].Asset != "USD" || !balances[1].Available.Equal(decimal.RequireFromString("900.5")) {
		t.Fatalf("balances = %+v, %v", balances, err)
	}
	stub.set("/0/private/OpenOrders", `{"error":[],"result":{"open":{}}}`)
	stub.set("/0/private/ClosedOrders", `{"error":[],"result":{"closed":{"OQ1":{"cl_ord_id":"229835394146432","status":"closed","opentm":1688665496.78,"closetm":1688665499.19,"descr":{"pair":"XBTUSD","type":"buy","ordertype":"limit","price":"50000"},"vol":"0.001","vol_exec":"0.001","price":"49990"}},"count":1}}`)
	result, err := client.LookupExactOrder(ctx, exchange.ExactOrderLookupRequest{Instrument: &btc, ClientOrderID: "229835394146432"})
	if err != nil || result.Status != exchange.ExactOrderFound || result.Order.Status != "filled" || result.Order.OrderID != "OQ1" {
		t.Fatalf("lookup = %+v, %v", result, err)
	}
	if stub.sent("/0/private/ClosedOrders").Get("cl_ord_id") != "229835394146432" {
		t.Fatal("closed orders not filtered by client order id")
	}
}

func TestKrakenSpotExecutionsProjectFillsAndOrders(t *testing.T) {
	var row krakenspot.Execution
	if err := krakenspot.Decode([]byte(`{"order_id":"O1","cl_ord_id":"7","exec_id":"T1","exec_type":"trade","order_status":"partially_filled","symbol":"BTC/USD","side":"sell",
		"order_type":"limit","order_qty":0.01,"limit_price":100,"cum_qty":0.005,"avg_price":100.5,"last_qty":0.005,"last_price":100.5,"liquidity_ind":"m",
		"fees":[{"asset":"USD","qty":0.01}],"timestamp":"2023-09-22T10:33:05.709993Z"}`), &row); err != nil {
		t.Fatal(err)
	}
	events := krakenSpotExecution(row)
	if len(events) != 2 || events[0].Fill == nil || events[0].Fill.Fill.Liquidity != exchange.LiquidityMaker || events[0].Fill.Fill.Side != exchange.SideSell ||
		events[1].Order == nil || events[1].Order.Order.Status != "partially_filled" || events[1].Order.Order.ClientOrderID != "7" {
		t.Fatalf("events = %+v", events)
	}
	if events := krakenSpotExecution(krakenspot.Execution{OrderID: "O1", OrderStatus: "new"}); len(events) != 0 {
		t.Fatalf("a symbol-less status row was routed: %+v", events)
	}
}
