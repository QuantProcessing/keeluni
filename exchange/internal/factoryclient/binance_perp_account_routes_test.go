package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

// One USD-M account serves both the USDT and the USDC route. Each route's
// stream keeps only its own instruments and lets account-level balances
// through, so a USDC position never surfaces as an unknown instrument on the
// USDT route.
func TestBinancePerpAccountRoutesKeepTheirOwnSettlement(t *testing.T) {
	usdt := binanceCatalogTestInstrument(exchange.ProductPerp, "BTC", "USDT", "USDT", decimal.RequireFromString("0.001"))
	index, err := newAccountInstrumentIndex(exchange.AccountRoute{Venue: exchange.VenueBinance, Product: exchange.ProductPerp},
		[]exchange.AccountInstrument{{InstrumentID: "binance:perp:BTC-USDT", Instrument: usdt}})
	if err != nil {
		t.Fatal(err)
	}
	var got []accountBackendEvent
	filter := binancePerpRouteFilter(index, func(event accountBackendEvent) { got = append(got, event) })
	order := func(native string) accountBackendEvent {
		return accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &exchange.OrderEvent{Order: exchange.Order{NativeSymbol: native}}}
	}
	filter(order("BTCUSDT"))
	filter(order("BTCUSDC"))
	filter(accountBackendEvent{Kind: exchange.AccountEventFill, Fill: &exchange.FillEvent{Fill: exchange.Fill{NativeSymbol: "BTCUSDC"}}})
	filter(accountBackendEvent{Kind: exchange.AccountEventBalance, Balance: &exchange.BalanceEvent{}})
	filter(accountBackendEvent{Kind: exchange.AccountEventPosition, Position: &exchange.PositionEvent{Positions: []exchange.Position{
		{NativeSymbol: "BTCUSDT", Quantity: decimal.NewFromInt(1)}, {NativeSymbol: "BTCUSDC", Quantity: decimal.NewFromInt(2)},
	}}})
	filter(accountBackendEvent{Kind: exchange.AccountEventPosition, Position: &exchange.PositionEvent{Positions: []exchange.Position{{NativeSymbol: "ETHUSDC"}}}})
	if len(got) != 3 || got[0].Kind != exchange.AccountEventOrder || got[1].Kind != exchange.AccountEventBalance {
		t.Fatalf("events = %+v", got)
	}
	if positions := got[2].Position.Positions; len(positions) != 1 || positions[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("positions = %+v", positions)
	}
}
