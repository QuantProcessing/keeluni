package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

const crossExSpotSymbolsFixture = `[
 {"symbol":"BINANCE_FUTURE_BTC_USDT","exchange_type":"BINANCE","business_type":"FUTURE","state":"live","min_size":"0.001","min_notional":"50","lot_size":"0.001","tick_size":"0.10","contract_size":null,"tag":""},
 {"symbol":"BINANCE_SPOT_BTC_USDT","exchange_type":"BINANCE","business_type":"SPOT","state":"live","min_size":"0.00001000","min_notional":"5.00000000","lot_size":"0.00001000","tick_size":"0.01000000","contract_size":null,"tag":""},
 {"symbol":"GATE_MARGIN_BTC_USDT","exchange_type":"GATE","business_type":"MARGIN","state":"live","min_size":"0.0001","min_notional":"3","lot_size":"0.0001","tick_size":"0.1","contract_size":null,"tag":""}
]`

func newTestCrossExSpotClient(t *testing.T, stub *crossExStub, ask decimal.Decimal) *crossExPerpClient {
	t.Helper()
	stub.reply("GET /api/v4/crossex/rule/symbols", 200, crossExSpotSymbolsFixture)
	natives := func(context.Context) ([]exchange.Instrument, error) {
		return []exchange.Instrument{
			nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BTCUSDT", "0.10", "0.001"),
			newInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTC", "USDT", "", "BTCUSDT", "",
				decimal.RequireFromString("0.01"), decimal.RequireFromString("0.00001"), decimal.RequireFromString("0.00001"), exchange.OptionalDecimal{}, oneContract()),
		}, nil
	}
	books := func(context.Context, exchange.Instrument) (decimal.Decimal, error) { return ask, nil }
	client := NewCrossExPerp("key", "secret", natives, Settings{
		Endpoint: stub.server.URL + "/api/v4", WebSocketEndpoint: "ws" + strings.TrimPrefix(stub.server.URL, "http") + "/ws/crossex", HTTPClient: stub.server.Client(),
	}, books).(*crossExPerpClient)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestCrossExTradesSpotBesidePerps(t *testing.T) {
	stub := newCrossExStub(t)
	client := newTestCrossExSpotClient(t, stub, decimal.RequireFromString("81234.56"))
	ctx := context.Background()
	instruments, err := client.RefreshInstruments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 2 {
		t.Fatalf("instruments = %d (margin rows are not spot)", len(instruments))
	}
	spot, err := client.ResolveInstrument(ctx, "binance:spot:BTC-USDT")
	if err != nil || spot.Identity.Product != exchange.ProductSpot || spot.Identity.NativeSymbol != "BINANCE_SPOT_BTC_USDT" {
		t.Fatalf("spot = %+v, %v", spot.Identity, err)
	}
	perp, err := client.ResolveInstrument(ctx, "binance:BTC-USDT")
	if err != nil || perp.Identity.Product != exchange.ProductPerp {
		t.Fatalf("perp = %+v, %v", perp.Identity, err)
	}

	stub.reply("POST /api/v4/crossex/orders", 200, `{"order_id":"9","text":"31"}`)
	ack, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: spot, ClientOrderID: "31", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket,
		Quantity: decimal.RequireFromString("0.001")})
	if err != nil || ack.State != exchange.AckAcceptedPending || ack.Product != exchange.ProductSpot {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	sent, _ := stub.last("POST", "/api/v4/crossex/orders")
	var body map[string]string
	_ = json.Unmarshal([]byte(sent.body), &body)
	if body["symbol"] != "BINANCE_SPOT_BTC_USDT" || body["qty"] != "" || body["quote_qty"] != "81.24" {
		t.Fatalf("market buy body = %v (quote sized from the best ask, rounded up)", body)
	}
	if _, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: spot, ClientOrderID: "32", Side: exchange.SideSell, Type: exchange.OrderTypeMarket,
		Quantity: decimal.RequireFromString("0.001")}); err != nil {
		t.Fatal(err)
	}
	sent, _ = stub.last("POST", "/api/v4/crossex/orders")
	body = nil
	_ = json.Unmarshal([]byte(sent.body), &body)
	if body["qty"] != "0.001" || body["quote_qty"] != "" {
		t.Fatalf("market sell body = %v", body)
	}
	if _, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: spot, ClientOrderID: "33", Side: exchange.SideSell, Type: exchange.OrderTypeMarket,
		Quantity: decimal.RequireFromString("0.001"), ReduceOnly: true}); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("reduce-only on spot = %v", err)
	}
}

func TestCrossExSpotMarketBuyWithoutPriceIsNotSent(t *testing.T) {
	stub := newCrossExStub(t)
	client := newTestCrossExSpotClient(t, stub, decimal.Zero)
	spot, err := client.ResolveInstrument(context.Background(), "binance:spot:BTC-USDT")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{Instrument: spot, ClientOrderID: "34", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket,
		Quantity: decimal.RequireFromString("0.001")})
	var normalized *exchange.Error
	if !errors.As(err, &normalized) || !normalized.Details().CommandNotSent {
		t.Fatalf("err = %v", err)
	}
	if _, sent := stub.last("POST", "/api/v4/crossex/orders"); sent {
		t.Fatal("an unsized market buy was sent")
	}
}
