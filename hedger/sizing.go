package hedger

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Sizing turns the operator's size into executable base quantities for both
// legs, rounded to each leg's step so the two legs stay as equal as the two
// venues allow. Everything here is pure arithmetic on decimals.

// SizeUnit says what the entered size is measured in.
const (
	UnitBase  = "base"  // base asset of leg 0 (e.g. BTC)
	UnitQuote = "quote" // quote currency notional (e.g. USDT)
)

// SizeInput is the operator's request.
type SizeInput struct {
	Size  decimal.Decimal
	Unit  string
	Match string // base | notional
	// Prices are the reference prices of each leg (the side each leg would
	// trade at: ask for a buy, bid for a sell).
	Price [2]decimal.Decimal
	// Ratio is leg-1 base per leg-0 base for MatchBase (normally 1, e.g. 1000
	// for 1000PEPE against PEPE); ignored for MatchNotional.
	Ratio decimal.Decimal
}

// SizePlan is the executable result.
type SizePlan struct {
	Qty      [2]decimal.Decimal `json:"qty"`
	Notional [2]decimal.Decimal `json:"notional"`
	// Ratio is fixed at open and drives every later close and rehedge:
	// leg-1 base per leg-0 base.
	Ratio decimal.Decimal `json:"ratio"`
	// Mismatch is leg 1's notional minus leg 0's at the reference prices: the
	// directional exposure left by rounding to two different steps.
	Mismatch decimal.Decimal `json:"mismatch"`
	Problems []string        `json:"problems,omitempty"`
}

// PlanSize sizes both legs. Leg quantities are floored to their steps; with
// MatchBase the coarser leg drives the other so the hedge is never larger on
// one side than the operator asked for.
func PlanSize(input SizeInput, legs [2]Leg) (SizePlan, error) {
	if !input.Size.IsPositive() {
		return SizePlan{}, invalidf("size must be positive")
	}
	for i := range legs {
		if !input.Price[i].IsPositive() {
			return SizePlan{}, invalidf("no price for leg %d (%s)", i+1, legs[i].Instrument)
		}
	}
	base0 := input.Size
	switch input.Unit {
	case UnitBase:
	case UnitQuote:
		base0 = input.Size.Div(input.Price[0])
	default:
		return SizePlan{}, invalidf("unit must be base or quote")
	}
	var plan SizePlan
	switch input.Match {
	case MatchBase:
		ratio := input.Ratio
		if ratio.IsZero() {
			ratio = decimal.NewFromInt(1)
		}
		if !ratio.IsPositive() {
			return SizePlan{}, invalidf("ratio must be positive")
		}
		plan.Ratio = ratio
		// Round leg 0, derive leg 1, round it, then re-derive leg 0 from the
		// rounded leg 1 so neither leg exceeds the other's equivalent.
		q0 := FloorToStep(base0, legs[0].StepSize)
		q1 := FloorToStep(q0.Mul(ratio), legs[1].StepSize)
		q0 = FloorToStep(q1.Div(ratio), legs[0].StepSize)
		plan.Qty = [2]decimal.Decimal{q0, q1}
	case MatchNotional:
		q0 := FloorToStep(base0, legs[0].StepSize)
		notional := q0.Mul(input.Price[0])
		q1 := FloorToStep(notional.Div(input.Price[1]), legs[1].StepSize)
		plan.Qty = [2]decimal.Decimal{q0, q1}
		if q0.IsPositive() {
			plan.Ratio = q1.Div(q0)
		}
	default:
		return SizePlan{}, invalidf("match must be base or notional")
	}
	for i := range legs {
		plan.Notional[i] = plan.Qty[i].Mul(input.Price[i])
	}
	plan.Mismatch = plan.Notional[1].Sub(plan.Notional[0])
	plan.Problems = checkMinimums(plan.Qty, input.Price, legs)
	return plan, nil
}

// checkMinimums lists every venue minimum the quantities violate.
func checkMinimums(qty [2]decimal.Decimal, price [2]decimal.Decimal, legs [2]Leg) []string {
	var problems []string
	for i, leg := range legs {
		label := fmt.Sprintf("leg %d (%s)", i+1, leg.Instrument)
		switch {
		case !qty[i].IsPositive():
			problems = append(problems, fmt.Sprintf("%s: size rounds to zero at step %s", label, leg.StepSize))
		case leg.MinQty.IsPositive() && qty[i].LessThan(leg.MinQty):
			problems = append(problems, fmt.Sprintf("%s: %s is below the minimum size %s", label, qty[i], leg.MinQty))
		case leg.MinNotional.IsPositive() && qty[i].Mul(price[i]).LessThan(leg.MinNotional):
			problems = append(problems, fmt.Sprintf("%s: value %s is below the minimum %s %s", label, qty[i].Mul(price[i]).StringFixed(2), leg.MinNotional, leg.Quote))
		}
	}
	return problems
}

// HedgeQuantity is the leg-1 base quantity that hedges q0 of leg 0.
func HedgeQuantity(q0, ratio decimal.Decimal) decimal.Decimal { return q0.Mul(ratio) }

// ---------------------------------------------------------------------------
// Execution estimate from order books
// ---------------------------------------------------------------------------

// BookSide is one side of a book: [price, quantity] levels, best first.
type BookSide [][2]decimal.Decimal

// Sweep is the result of taking qty from one side of a book.
type Sweep struct {
	Filled   decimal.Decimal `json:"filled"`
	AvgPrice decimal.Decimal `json:"avgPrice"`
	Best     decimal.Decimal `json:"best"`
	// SlippageBps is (avg - best) / best in basis points, signed so that a
	// worse price is positive for either side.
	SlippageBps decimal.Decimal `json:"slippageBps"`
	// Short means the visible book did not cover qty.
	Short bool `json:"short"`
}

// SweepBook walks levels until qty is filled. buy=true walks asks.
func SweepBook(levels BookSide, qty decimal.Decimal, buy bool) Sweep {
	var result Sweep
	if len(levels) == 0 || !qty.IsPositive() {
		result.Short = qty.IsPositive()
		return result
	}
	result.Best = levels[0][0]
	remaining, cost := qty, decimal.Zero
	for _, level := range levels {
		take := decimal.Min(remaining, level[1])
		cost = cost.Add(take.Mul(level[0]))
		remaining = remaining.Sub(take)
		if !remaining.IsPositive() {
			break
		}
	}
	result.Filled = qty.Sub(remaining)
	result.Short = remaining.IsPositive()
	if result.Filled.IsPositive() {
		result.AvgPrice = cost.Div(result.Filled)
		diff := result.AvgPrice.Sub(result.Best)
		if !buy {
			diff = diff.Neg()
		}
		result.SlippageBps = diff.Div(result.Best).Mul(decimal.NewFromInt(10000)).Round(2)
	}
	return result
}
