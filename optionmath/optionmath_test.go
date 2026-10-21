package optionmath

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

func near(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %v, want %v ± %v", name, got, want, tolerance)
	}
}

// Deribit BTC-27NOV26-73000-P on 2026-10-20: underlying 82032.7, mark IV
// 40.39%, venue greeks delta -0.16925 vega 67.05061 theta -35.3579.
func TestMatchesDeribitPublishedGreeks(t *testing.T) {
	now := time.UnixMilli(1792456815095)
	expiry := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	years := Years(now, expiry)
	greeks := GreeksOf(false, 82032.7, 73000, years, 0.4039)
	near(t, "delta", greeks.Delta, -0.16925, 5e-4)
	near(t, "vega", greeks.Vega, 67.0506, 0.05)
	near(t, "theta", greeks.Theta, -35.3579, 0.05)
	price := Price(false, 82032.7, 73000, years, 0.4039)
	near(t, "price in BTC", price/82032.7, 0.0126, 1e-4)
}

func TestImpliedVolRoundTrips(t *testing.T) {
	for _, call := range []bool{true, false} {
		for _, strike := range []float64{60000, 80000, 100000} {
			for _, vol := range []float64{0.2, 0.55, 1.4} {
				price := Price(call, 81000, strike, 0.1, vol)
				implied, ok := ImpliedVol(call, 81000, strike, 0.1, price)
				if !ok {
					t.Fatalf("no IV for call=%v K=%v vol=%v price=%v", call, strike, vol, price)
				}
				near(t, "iv", implied, vol, 1e-6)
			}
		}
	}
}

func TestBoundsAndExpiry(t *testing.T) {
	if _, ok := ImpliedVol(true, 100, 90, 0.1, 9.5); ok {
		t.Fatal("price below intrinsic has no IV")
	}
	if _, ok := ImpliedVol(true, 100, 90, 0.1, 100); ok {
		t.Fatal("call at the forward has no IV")
	}
	if got := Price(true, 100, 90, 0, 0.5); got != 10 {
		t.Fatalf("expired call = %v", got)
	}
	if got := GreeksOf(false, 100, 110, 0, 0.5); got.Delta != -1 || got.Gamma != 0 {
		t.Fatalf("expired ITM put greeks = %+v", got)
	}
	// Put-call parity on a forward with zero discounting: C - P = F - K.
	call, put := Price(true, 100, 95, 0.3, 0.6), Price(false, 100, 95, 0.3, 0.6)
	near(t, "parity", call-put, 5, 1e-9)
}

// Binance values options on spot with a rate (riskFreeInterest). The
// fixture is one btcusdt@optionMarkPrice push (2026-10-20): index, rate,
// mark, IV and greeks of one instant. OnSpot reproduces Binance's marks to
// within 0.001% and its greeks; a zero-rate model is off by up to 5%.
func TestMatchesBinanceSpotAndRateModel(t *testing.T) {
	data, err := os.ReadFile("testdata/binance_mark.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Rows []struct {
			Symbol                                                            string
			Call                                                              bool
			Expiry, Time                                                      int64
			Strike, Index, Rate, MarkPrice, MarkIV, Delta, Vega, Theta, Gamma string
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	f := func(text string) float64 { value, _ := strconv.ParseFloat(text, 64); return value }
	far := false
	for _, row := range fixture.Rows {
		years := Years(time.UnixMilli(row.Time), time.UnixMilli(row.Expiry))
		model := OnSpot(f(row.Index), f(row.Rate), years)
		mark, strike, vol := f(row.MarkPrice), f(row.Strike), f(row.MarkIV)
		near(t, row.Symbol+" mark", model.Value(row.Call, strike, vol), mark, mark*1e-4)
		greeks := model.Greeks(row.Call, strike, vol)
		near(t, row.Symbol+" delta", greeks.Delta, f(row.Delta), 1e-4)
		// Binance's published gamma is not Black-Scholes: a put and a call
		// of one strike — which must share a gamma — differ by up to 11% in
		// the fixture. Its marks, deltas and vegas match exactly.
		near(t, row.Symbol+" gamma", greeks.Gamma, f(row.Gamma), math.Abs(f(row.Gamma))*0.15)
		near(t, row.Symbol+" vega", greeks.Vega, f(row.Vega), math.Abs(f(row.Vega))*0.01)
		// Binance's theta is close to the volatility decay, not identical.
		near(t, row.Symbol+" theta", greeks.Theta, f(row.Theta), math.Abs(f(row.Theta))*0.15)
		implied, ok := model.ImpliedVol(row.Call, strike, mark)
		if !ok {
			t.Fatalf("%s: no IV", row.Symbol)
		}
		near(t, row.Symbol+" iv", implied, vol, 1e-4)
		if zero := OnForward(f(row.Index), years).Value(row.Call, strike, vol); math.Abs(zero-mark) > mark*0.01 {
			far = true
		}
	}
	if !far {
		t.Fatal("a zero-rate model should miss some of Binance's marks by more than 1%")
	}
}
