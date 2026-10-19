package factoryclient

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	crossex "github.com/QuantProcessing/boltertrader/sdk/crossex"
	"github.com/shopspring/decimal"
)

func TestCrossExOrderStatusKeepsNewApartFromResting(t *testing.T) {
	cases := map[string]struct {
		executed string
		want     string
	}{
		"NEW":              {"0", "pending"},
		"OPEN":             {"0", "open"},
		"PARTIALLY_FILLED": {"1", "partially_filled"},
		"FILLED":           {"2", "filled"},
		"CANCELLED":        {"0", "canceled"},
		"FAIL":             {"0", "rejected"},
		"REJECT":           {"0", "rejected"},
	}
	for state, tc := range cases {
		if got := crossExOrderStatus(crossex.Order{State: state, ExecutedQty: tc.executed}); got != tc.want {
			t.Fatalf("%s -> %q, want %q", state, got, tc.want)
		}
	}
	if got := crossExOrderStatus(crossex.Order{State: "CANCELLED", ExecutedQty: "0.5"}); got != "partially_filled_canceled" {
		t.Fatalf("partially filled cancel -> %q", got)
	}
}

func TestCrossExOrderRejectionSeparatesCrossExFromExchange(t *testing.T) {
	fail := crossex.Order{State: "FAIL", Reason: `{"label":"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR","message":"Insufficient availableMargin"}`}
	rejection, ok := crossExOrderRejection(fail)
	if !ok || rejection.ByExchange || rejection.Code != "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR" || rejection.Message != "Insufficient availableMargin" {
		t.Fatalf("FAIL rejection = %+v", rejection)
	}
	reject := crossex.Order{State: "REJECT", Reason: "Due to the order could not be executed as maker,\nthe Post Only order will be rejected."}
	rejection, ok = crossExOrderRejection(reject)
	if !ok || !rejection.ByExchange || rejection.Code != "" || rejection.Message != "Due to the order could not be executed as maker, the Post Only order will be rejected." {
		t.Fatalf("REJECT rejection = %+v", rejection)
	}
	if _, ok := crossExOrderRejection(crossex.Order{State: "OPEN"}); ok {
		t.Fatal("open order reported as rejected")
	}
}

func TestCrossExOrderProjection(t *testing.T) {
	order := crossExOrder(crossex.Order{
		OrderID: "2048529119934720", Text: "1700000000123", State: "PARTIALLY_FILLED", Symbol: "OKX_FUTURE_ADA_USDT",
		Side: "SELL", Type: "LIMIT", TimeInForce: "POC", Qty: "10", Price: "0.85", ExecutedQty: "4",
		ExecutedAvgPrice: "0.851", ReduceOnly: "true", CreateTime: "1750682602377", UpdateTime: "1750682602413",
	})
	if order.NativeSymbol != "OKX_FUTURE_ADA_USDT" || order.ClientOrderID != "1700000000123" || order.Side != exchange.SideSell ||
		order.Type != exchange.OrderTypeLimit || order.LimitPolicy != exchange.LimitPolicyPostOnly || !order.ReduceOnly ||
		!order.Quantity.Equal(decimal.NewFromInt(10)) || !order.Filled.Equal(decimal.NewFromInt(4)) ||
		!order.LimitPrice.Equal(decimal.RequireFromString("0.85")) || !order.AverageFillPrice.Valid ||
		order.Status != "partially_filled" || order.UpdatedAt != time.UnixMilli(1750682602413).UTC() {
		t.Fatalf("order = %+v", order)
	}
	// Market orders carry no limit price; CrossEx's own ids are not portable.
	market := crossExOrder(crossex.Order{OrderID: "1", Text: "2048522992198912", Type: "MARKET", Price: "0", State: "FILLED", ExecutedAvgPrice: "0"})
	if market.Type != exchange.OrderTypeMarket || !market.LimitPrice.IsZero() || market.LimitPolicy != "" ||
		market.ClientOrderID != "" || market.AverageFillPrice.Valid {
		t.Fatalf("market order = %+v", market)
	}
}

func TestCrossExFillProjection(t *testing.T) {
	fill := crossExFill(crossex.Trade{
		TransactionID: "2049614605858560", OrderID: "2049614605857536", Text: "77", Symbol: "BINANCE_FUTURE_ADA_USDT",
		Side: "SELL", Qty: "10", Price: "0.5597", Fee: "0.0027985", FeeCoin: "usdt", MatchRole: "MAKER", CreateTime: "1750941402661",
	})
	if fill.FillID != "2049614605858560" || fill.ClientOrderID != "77" || fill.Side != exchange.SideSell ||
		fill.Liquidity != exchange.LiquidityMaker || fill.FeeAsset != "USDT" || !fill.Fee.Equal(decimal.RequireFromString("0.0027985")) ||
		fill.Time != time.UnixMilli(1750941402661).UTC() {
		t.Fatalf("fill = %+v", fill)
	}
}

func TestCrossExPositionProjection(t *testing.T) {
	short := crossExPosition(crossex.Position{Symbol: "OKX_FUTURE_ADA_USDT", PositionSide: "NONE", PositionQty: "-10",
		EntryPrice: "0.5426", MarkPrice: "0.5795", Upnl: "-0.369", LiqPrice: "0", Leverage: "3", InitialMargin: "1.93"})
	if short.PositionSlot != exchange.PositionSlotNet || short.Side != exchange.SideSell || !short.Quantity.Equal(decimal.NewFromInt(-10)) ||
		short.LiquidationPrice.Valid || !short.Leverage.Valid || !short.MarginUsed.Valid {
		t.Fatalf("short = %+v", short)
	}
	isolated := crossExPosition(crossex.Position{Symbol: "HYPERLIQUID_FUTURE_BTC_USDC", PositionSide: "NONE", PositionQty: "0.01", LiqPrice: "61000"})
	if isolated.Side != exchange.SideBuy || !isolated.LiquidationPrice.Valid || !isolated.LiquidationPrice.Value.Equal(decimal.NewFromInt(61000)) {
		t.Fatalf("isolated = %+v", isolated)
	}
	flat := crossExPosition(crossex.Position{Symbol: "OKX_FUTURE_ADA_USDT", PositionSide: "NONE", PositionQty: "0"})
	if flat.Side != "" || !flat.Quantity.IsZero() {
		t.Fatalf("flat = %+v", flat)
	}
}

func TestCrossExOrderRequestEncoding(t *testing.T) {
	instrument := nativePerp(exchange.VenueBinance, "BTC-USDT", "BTC", "USDT", "BINANCE_FUTURE_BTC_USDT", "0.1", "0.001")
	cases := []struct {
		request exchange.PlaceOrderRequest
		want    crossex.OrderRequest
	}{
		{
			exchange.PlaceOrderRequest{Instrument: instrument, ClientOrderID: "17", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit,
				Quantity: decimal.RequireFromString("0.002"), LimitPrice: decimal.RequireFromString("60000.5"), LimitPolicy: exchange.LimitPolicyResting},
			crossex.OrderRequest{Text: "17", Symbol: "BINANCE_FUTURE_BTC_USDT", Side: "BUY", Type: "LIMIT", TimeInForce: "GTC", Qty: "0.002", Price: "60000.5"},
		},
		{
			exchange.PlaceOrderRequest{Instrument: instrument, ClientOrderID: "18", Side: exchange.SideSell, Type: exchange.OrderTypeLimit,
				Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("70000"), LimitPolicy: exchange.LimitPolicyPostOnly},
			crossex.OrderRequest{Text: "18", Symbol: "BINANCE_FUTURE_BTC_USDT", Side: "SELL", Type: "LIMIT", TimeInForce: "POC", Qty: "1", Price: "70000"},
		},
		{
			exchange.PlaceOrderRequest{Instrument: instrument, ClientOrderID: "19", Side: exchange.SideSell, Type: exchange.OrderTypeMarket,
				Quantity: decimal.RequireFromString("0.5"), ReduceOnly: true},
			crossex.OrderRequest{Text: "19", Symbol: "BINANCE_FUTURE_BTC_USDT", Side: "SELL", Type: "MARKET", TimeInForce: "IOC", Qty: "0.5", ReduceOnly: "true"},
		},
	}
	for _, tc := range cases {
		if got := crossExOrderRequest(tc.request); got != tc.want {
			t.Fatalf("request = %+v, want %+v", got, tc.want)
		}
	}
}

func TestCrossExPortableClientOrderID(t *testing.T) {
	for text, want := range map[string]string{"17": "17", "281474976710655": "281474976710655", "281474976710656": "", "0": "", "017": "", "cross-test-1": "", "": ""} {
		if got := crossExPortableClientOrderID(text); got != want {
			t.Fatalf("%q -> %q, want %q", text, got, want)
		}
	}
}
