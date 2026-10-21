// Package optionmath prices European options with Black-76: an option on
// a forward F, discounted by a factor D. Every crypto venue in scope is an
// instance of it — they differ only in where F and D come from:
//
//	Deribit, OKX   F = the expiry's future or synthetic, D = 1
//	               (premium paid at expiry; measured against both venues)
//	Binance        F = index × e^(rT), D = e^(−rT) with r its riskFreeInterest
//	               (plain Black-Scholes on spot; measured: 0.008% median error)
//
// Model carries F, D and the time to expiry for one expiry; it is the one
// value a chain row, a risk report and an order price all need.
//
// Units: prices and greeks are in the strike currency per one unit of the
// underlying. Vega is per one volatility point (1%), theta per calendar day.
// Volatility is an annualized fraction (0.42 = 42%). Time is in years of 365
// days.
package optionmath

import (
	"math"
	"time"
)

const (
	yearSeconds = 365 * 24 * 3600
	minVol      = 1e-4
	maxVol      = 10
)

// Years is the time from now to expiry, never negative.
func Years(now, expiry time.Time) float64 {
	seconds := expiry.Sub(now).Seconds()
	if seconds <= 0 {
		return 0
	}
	return seconds / yearSeconds
}

// Model is one expiry's valuation inputs.
type Model struct {
	// Forward is the price the option is struck against at expiry.
	Forward float64
	// Years to expiry.
	Years float64
	// Discount is the factor the payoff is discounted by; 0 means 1.
	Discount float64
}

// OnForward is the model of a venue that publishes the expiry's forward and
// does not discount (Deribit, OKX).
func OnForward(forward, years float64) Model {
	return Model{Forward: forward, Years: years, Discount: 1}
}

// OnSpot is the model of a venue that values on spot with a continuously
// compounded rate (Binance): Black-Scholes, expressed as Black-76.
func OnSpot(spot, rate, years float64) Model {
	if years <= 0 || rate == 0 {
		return Model{Forward: spot, Years: years, Discount: 1}
	}
	return Model{Forward: spot * math.Exp(rate*years), Years: years, Discount: math.Exp(-rate * years)}
}

func (model Model) discount() float64 {
	if model.Discount <= 0 {
		return 1
	}
	return model.Discount
}

// Shifted returns the model with the forward moved by a fraction (spot
// scenarios move every forward in proportion).
func (model Model) Shifted(fraction float64) Model {
	model.Forward *= 1 + fraction
	return model
}

// Greeks of one option per unit of the underlying.
type Greeks struct {
	Delta float64 `json:"delta"`
	Gamma float64 `json:"gamma"`
	Vega  float64 `json:"vega"`
	Theta float64 `json:"theta"`
}

func phi(x float64) float64 { return math.Exp(-x*x/2) / math.Sqrt(2*math.Pi) }
func cdf(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// Intrinsic is the value at expiry.
func Intrinsic(call bool, forward, strike float64) float64 {
	if call {
		return math.Max(forward-strike, 0)
	}
	return math.Max(strike-forward, 0)
}

func d1d2(forward, strike, years, vol float64) (float64, float64) {
	root := vol * math.Sqrt(years)
	d1 := (math.Log(forward/strike) + vol*vol*years/2) / root
	return d1, d1 - root
}

func valid(forward, strike float64) bool {
	return forward > 0 && strike > 0 && !math.IsNaN(forward) && !math.IsInf(forward, 0)
}

// Value is the option's price under the model. At or past expiry, or with
// no volatility, it is the discounted intrinsic value.
func (model Model) Value(call bool, strike, vol float64) float64 {
	forward, years, discount := model.Forward, model.Years, model.discount()
	if !valid(forward, strike) {
		return 0
	}
	if years <= 0 || vol <= 0 {
		return discount * Intrinsic(call, forward, strike)
	}
	d1, d2 := d1d2(forward, strike, years, vol)
	if call {
		return discount * (forward*cdf(d1) - strike*cdf(d2))
	}
	return discount * (strike*cdf(-d2) - forward*cdf(-d1))
}

// Greeks are with respect to the underlying price S = F·D: for a forward
// model (D = 1) that is the forward itself; for a spot model it is spot,
// and delta, gamma and vega are the Black-Scholes greeks.
//
// Theta is the volatility decay only (−S·φ(d1)·σ/(2√T) per day): the rate's
// carry on the strike is left out on every venue. It is what a trader means
// by theta, it is what Deribit and OKX report, and Binance's published theta
// is within a few percent of it rather than of the full Black-Scholes theta.
func (model Model) Greeks(call bool, strike, vol float64) Greeks {
	forward, years, discount := model.Forward, model.Years, model.discount()
	if !valid(forward, strike) {
		return Greeks{}
	}
	if years <= 0 || vol <= 0 {
		var delta float64
		switch {
		case call && forward > strike:
			delta = 1
		case !call && forward < strike:
			delta = -1
		}
		return Greeks{Delta: delta}
	}
	d1, _ := d1d2(forward, strike, years, vol)
	root := math.Sqrt(years)
	spot := forward * discount
	delta := cdf(d1)
	if !call {
		delta--
	}
	density := phi(d1)
	theta := -spot * density * vol / (2 * root)
	return Greeks{
		Delta: delta,
		Gamma: density / (spot * vol * root),
		Vega:  spot * density * root / 100,
		Theta: theta / 365,
	}
}

// ImpliedVol inverts Value. It returns false when the price is outside the
// no-arbitrage band (at or below the discounted intrinsic, at or above the
// discounted forward/strike bound) or the option has expired.
func (model Model) ImpliedVol(call bool, strike, price float64) (float64, bool) {
	forward, years, discount := model.Forward, model.Years, model.discount()
	if !valid(forward, strike) || years <= 0 || price <= 0 || math.IsNaN(price) {
		return 0, false
	}
	lower := discount * Intrinsic(call, forward, strike)
	upper := discount * forward
	if !call {
		upper = discount * strike
	}
	if price <= lower || price >= upper {
		return 0, false
	}
	low, high := float64(minVol), float64(maxVol)
	if model.Value(call, strike, high) < price {
		return 0, false
	}
	// Bisection is guaranteed on a monotone function; Newton steps speed it
	// up while they stay inside the bracket.
	vol := 0.5
	for i := 0; i < 200; i++ {
		diff := model.Value(call, strike, vol) - price
		if diff == 0 {
			return vol, true
		}
		if diff > 0 {
			high = vol
		} else {
			low = vol
		}
		if high-low < 1e-10 {
			return vol, true
		}
		vega := model.Greeks(call, strike, vol).Vega * 100
		next := vol - diff/vega
		if vega <= 0 || next <= low || next >= high || math.IsNaN(next) {
			next = (low + high) / 2
		}
		if math.Abs(next-vol) < 1e-12 {
			return next, true
		}
		vol = next
	}
	return vol, true
}

// Price is Black-76 on a forward without discounting.
func Price(call bool, forward, strike, years, vol float64) float64 {
	return OnForward(forward, years).Value(call, strike, vol)
}

// GreeksOf are the greeks of Price.
func GreeksOf(call bool, forward, strike, years, vol float64) Greeks {
	return OnForward(forward, years).Greeks(call, strike, vol)
}

// ImpliedVol inverts Price.
func ImpliedVol(call bool, forward, strike, years, price float64) (float64, bool) {
	return OnForward(forward, years).ImpliedVol(call, strike, price)
}
