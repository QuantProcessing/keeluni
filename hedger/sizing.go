package hedger

import (
	"strings"

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
	Problems []Text          `json:"problems,omitempty"`
}

// PlanSize sizes both legs. Leg quantities are floored to their steps; with
// MatchBase the coarser leg drives the other so the hedge is never larger on
// one side than the operator asked for.
func PlanSize(input SizeInput, legs [2]Leg) (SizePlan, error) {
	if !input.Size.IsPositive() {
		return SizePlan{}, invalid("invalid.sizePositive")
	}
	for i := range legs {
		if !input.Price[i].IsPositive() {
			return SizePlan{}, invalid("invalid.noPrice", "leg", i+1, "inst", legs[i].Instrument)
		}
	}
	base0 := input.Size
	switch input.Unit {
	case UnitBase:
	case UnitQuote:
		base0 = input.Size.Div(input.Price[0])
	default:
		return SizePlan{}, invalid("invalid.unit")
	}
	var plan SizePlan
	switch input.Match {
	case MatchBase:
		ratio := input.Ratio
		if ratio.IsZero() {
			ratio = decimal.NewFromInt(1)
		}
		if !ratio.IsPositive() {
			return SizePlan{}, invalid("invalid.ratio")
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
		return SizePlan{}, invalid("invalid.match")
	}
	for i := range legs {
		plan.Notional[i] = plan.Qty[i].Mul(input.Price[i])
	}
	plan.Mismatch = plan.Notional[1].Sub(plan.Notional[0])
	plan.Problems = checkMinimums(plan.Qty, input.Price, legs)
	return plan, nil
}

// checkMinimums lists every venue minimum the quantities violate.
func checkMinimums(qty [2]decimal.Decimal, price [2]decimal.Decimal, legs [2]Leg) []Text {
	var problems []Text
	for i, leg := range legs {
		switch {
		case !qty[i].IsPositive():
			problems = append(problems, textOf("sizing.roundsToZero", "leg", i+1, "step", leg.StepSize))
		case leg.MinQty.IsPositive() && qty[i].LessThan(leg.MinQty):
			problems = append(problems, textOf("sizing.belowMinQty", "leg", i+1, "qty", qty[i], "min", leg.MinQty))
		case leg.MinNotional.IsPositive() && qty[i].Mul(price[i]).LessThan(leg.MinNotional):
			problems = append(problems, textOf("sizing.belowMinNotional", "leg", i+1, "value", qty[i].Mul(price[i]).StringFixed(2), "min", leg.MinNotional, "quote", leg.Quote))
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

// ---------------------------------------------------------------------------
// Matching two legs
// ---------------------------------------------------------------------------

// Venues list contracts of cheap coins in multiples, spelled into the base:
// Binance 1000PEPE, Bitget 1MCHEEMS, Bybit 1000000MOG and, on Hyperliquid
// only, kPEPE. Longer prefixes are tried first.
var contractMultipliers = []struct {
	prefix     string
	multiplier int64
	venue      string // "" for any venue
}{
	{"1000000", 1_000_000, ""}, {"1M", 1_000_000, ""}, {"10000", 10_000, ""}, {"1000", 1_000, ""}, {"k", 1_000, "hyperliquid"},
}

// underlying splits a base into its underlying asset and contract multiplier.
func underlying(venue, base string) (string, decimal.Decimal) {
	for _, item := range contractMultipliers {
		if item.venue != "" && item.venue != venue {
			continue
		}
		rest, found := strings.CutPrefix(base, item.prefix)
		// The underlying must start with a letter: 1INCH and 10Y are names,
		// not multiples.
		if found && rest != "" && rest[0] >= 'A' && rest[0] <= 'Z' {
			return strings.ToUpper(rest), decimal.NewFromInt(item.multiplier)
		}
	}
	return strings.ToUpper(base), decimal.NewFromInt(1)
}

// MatchLegs decides how two legs are sized against each other from the
// instruments alone: the same underlying is matched by quantity through the
// contract multipliers (ratio = leg-1 base per leg-0 base, e.g. 0.001 for
// PEPE against 1000PEPE); different underlyings by notional at entry.
func MatchLegs(legs [2]Leg) (string, decimal.Decimal) {
	root0, multiplier0 := underlying(legs[0].Venue, legs[0].Base)
	root1, multiplier1 := underlying(legs[1].Venue, legs[1].Base)
	if root0 != root1 {
		return MatchNotional, decimal.Zero
	}
	return MatchBase, multiplier0.Div(multiplier1)
}
