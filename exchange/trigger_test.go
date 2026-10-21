package exchange

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestTriggerOrderRequestValidation(t *testing.T) {
	valid := TriggerOrderRequest{
		Instrument: testInstrument(ProductPerp), ClientOrderID: "42", Side: SideSell, Kind: TriggerStopLoss,
		TriggerPrice: decimal.RequireFromString("60000.1"), PriceType: TriggerPriceMark, Quantity: decimal.RequireFromString("0.01"),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	take := valid
	take.Kind, take.PriceType = TriggerTakeProfit, TriggerPriceLast
	if err := take.Validate(); err != nil {
		t.Fatalf("take profit on last price: %v", err)
	}
	for name, mutate := range map[string]func(*TriggerOrderRequest){
		"spot":             func(r *TriggerOrderRequest) { r.Instrument = testInstrument(ProductSpot) },
		"side":             func(r *TriggerOrderRequest) { r.Side = "" },
		"kind":             func(r *TriggerOrderRequest) { r.Kind = "trailing" },
		"price type":       func(r *TriggerOrderRequest) { r.PriceType = "index" },
		"zero trigger":     func(r *TriggerOrderRequest) { r.TriggerPrice = decimal.Zero },
		"misaligned price": func(r *TriggerOrderRequest) { r.TriggerPrice = decimal.RequireFromString("60000.15") },
		"zero quantity":    func(r *TriggerOrderRequest) { r.Quantity = decimal.Zero },
		"misaligned qty":   func(r *TriggerOrderRequest) { r.Quantity = decimal.RequireFromString("0.00001") },
		"client id":        func(r *TriggerOrderRequest) { r.ClientOrderID = "abc" },
	} {
		request := valid
		mutate(&request)
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
