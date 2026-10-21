package spreader

import (
	"math"
	"time"
)

// The signal: pure functions shared by the live evaluator and the backtest,
// so both make the same decision on the same series.
//
// Spreads are in basis points of leg A: (priceB / ratio − priceA) / priceA ×
// 10⁴. A positive deviation means B is rich against A, so the trade is long
// A, short B; a negative one the reverse.

// Rule is a strategy's trading rule (see the design document, section 3).
type Rule struct {
	// Direction limits which way the strategy trades: both, long_a, long_b.
	Direction string `json:"direction"`
	// FairHalfLife is the EWMA half-life of the fair value, in minutes.
	FairHalfLife int `json:"fairHalfLifeMin"`
	// KSigma: a deviation must reach k·σ (and the cost floor).
	KSigma float64 `json:"kSigma"`
	// MinProfit is the net profit a cycle must expect, in bps.
	MinProfit float64 `json:"minProfit"`
	// FeeA, FeeB are each leg's taker fee in bps (entered by the user).
	FeeA float64 `json:"feeA"`
	FeeB float64 `json:"feeB"`
	// Persist is how long the entry condition must hold, in minutes.
	Persist int `json:"persistMin"`
	// CloseSigma: a cycle closes when the deviation is back within c·σ.
	CloseSigma float64 `json:"closeSigma"`
	// ClosePersist is how long the exit condition must hold, in minutes.
	ClosePersist int `json:"closePersistMin"`
	// StopMult: a cycle is stopped when the deviation reaches stop × threshold
	// on the wrong side.
	StopMult float64 `json:"stopMult"`
	// MaxHold closes a cycle held longer, in minutes.
	MaxHold int `json:"maxHoldMin"`
	// LadderStep (in σ) adds a rung every step of further deviation; 0 is off.
	LadderStep float64 `json:"ladderStepSigma"`
	// LadderMax is the most rungs held at once (1 without a ladder).
	LadderMax int `json:"ladderMax"`
}

// Directions.
const (
	DirectionBoth  = "both"
	DirectionLongA = "long_a" // long A, short B: taken when B is rich
	DirectionLongB = "long_b" // long B, short A: taken when A is rich
)

// DefaultRule is the starting rule (section 3 defaults).
func DefaultRule() Rule {
	return Rule{
		Direction: DirectionBoth, FairHalfLife: 1440, KSigma: 2, MinProfit: 2, FeeA: 5, FeeB: 5,
		Persist: 10, CloseSigma: 0.5, ClosePersist: 2, StopMult: 3, MaxHold: 4320, LadderMax: 1,
	}
}

// Normalize fills zero fields with defaults and clamps the rest to sane
// ranges; it returns the rule a strategy actually runs.
func (rule Rule) Normalize() Rule {
	base := DefaultRule()
	if rule.Direction != DirectionLongA && rule.Direction != DirectionLongB {
		rule.Direction = DirectionBoth
	}
	if rule.FairHalfLife <= 0 {
		rule.FairHalfLife = base.FairHalfLife
	}
	rule.FairHalfLife = clampInt(rule.FairHalfLife, 30, 30*1440)
	if rule.KSigma <= 0 {
		rule.KSigma = base.KSigma
	}
	if rule.MinProfit < 0 {
		rule.MinProfit = 0
	}
	if rule.FeeA < 0 {
		rule.FeeA = 0
	}
	if rule.FeeB < 0 {
		rule.FeeB = 0
	}
	if rule.Persist < 0 {
		rule.Persist = 0
	}
	if rule.CloseSigma < 0 {
		rule.CloseSigma = base.CloseSigma
	}
	if rule.ClosePersist < 0 {
		rule.ClosePersist = 0
	}
	if rule.StopMult <= 1 {
		rule.StopMult = base.StopMult
	}
	if rule.MaxHold <= 0 {
		rule.MaxHold = base.MaxHold
	}
	if rule.LadderStep <= 0 {
		rule.LadderStep, rule.LadderMax = 0, 1
	}
	rule.LadderMax = clampInt(rule.LadderMax, 1, 10)
	return rule
}

func clampInt(value, low, high int) int {
	return int(math.Max(float64(low), math.Min(float64(high), float64(value))))
}

// SpreadBps is (priceB / ratio − priceA) / priceA in bps; NaN when either
// price is missing.
func SpreadBps(priceA, priceB, ratio float64) float64 {
	if !(priceA > 0) || !(priceB > 0) || !(ratio > 0) {
		return math.NaN()
	}
	return (priceB/ratio - priceA) / priceA * 1e4
}

// ---------------------------------------------------------------------------
// Fair value: an EWMA of minute samples
// ---------------------------------------------------------------------------

// Fair is the fair value and the deviation's spread around it.
type Fair struct {
	Value    float64 `json:"fair"`
	Variance float64 `json:"variance"`
	// Samples counts the minutes folded in; the estimate is warm after a
	// couple of half-lives.
	Samples int `json:"samples"`
	// Frozen: a cycle is held, so the estimate stands still.
	Frozen bool `json:"frozen"`
}

// Sigma is the standard deviation of the deviation.
func (fair Fair) Sigma() float64 { return math.Sqrt(math.Max(fair.Variance, 0)) }

// Warm reports whether enough minutes have been folded in to trade on: two
// half-lives, but at least a day of data is not required for short
// half-lives.
func (fair Fair) Warm(halfLife int) bool { return fair.Samples >= 2*halfLife }

// Update folds one minute sample in (unless frozen).
func (fair Fair) Update(sample float64, halfLife int) Fair {
	if fair.Frozen || math.IsNaN(sample) || math.IsInf(sample, 0) {
		return fair
	}
	if fair.Samples == 0 {
		fair.Value, fair.Variance, fair.Samples = sample, 0, 1
		return fair
	}
	alpha := 1 - math.Pow(2, -1/float64(halfLife))
	diff := sample - fair.Value
	fair.Value += alpha * diff
	fair.Variance = (1 - alpha) * (fair.Variance + alpha*diff*diff)
	fair.Samples++
	return fair
}

// ---------------------------------------------------------------------------
// Thresholds and decisions
// ---------------------------------------------------------------------------

// Quote is one evaluation's view of the pair at the strategy's size, in bps
// of leg A: the mid spread and the spreads a market entry would trade at in
// each direction (long A: buy A's asks, sell B's bids; long B: the reverse)
// and the spreads the exit would trade at.
type Quote struct {
	Mid float64 `json:"mid"`
	// OpenLongA: B sold at its bid against A bought at its ask.
	OpenLongA float64 `json:"openLongA"`
	// OpenLongB: B bought at its ask against A sold at its bid.
	OpenLongB float64 `json:"openLongB"`
}

// Cost is the round trip a cycle pays, in bps: both legs' taker fees twice
// and the book-crossing cost (the open-to-close spread width at the size).
func (rule Rule) Cost(quote Quote) float64 {
	crossing := math.Abs(quote.OpenLongA - quote.OpenLongB)
	if math.IsNaN(crossing) {
		crossing = 0
	}
	return 2*(rule.FeeA+rule.FeeB) + crossing
}

// Threshold is the deviation a cycle needs: enough to pay the costs and the
// minimum profit, and k·σ so noise does not trigger it.
func (rule Rule) Threshold(quote Quote, sigma float64) float64 {
	return math.Max(rule.Cost(quote)+rule.MinProfit, rule.KSigma*sigma)
}

// Entry is the direction a quote's deviation calls for, and the executable
// deviation in that direction; "" when none reaches the threshold.
//
// Long A pays B's bid and A's ask, so its executable deviation is the
// executable long-A spread minus fair; it is taken when B is rich (positive).
func (rule Rule) Entry(quote Quote, fair Fair) (string, float64) {
	threshold := rule.Threshold(quote, fair.Sigma())
	if rule.Direction != DirectionLongB {
		if dev := quote.OpenLongA - fair.Value; dev >= threshold {
			return DirectionLongA, dev
		}
	}
	if rule.Direction != DirectionLongA {
		if dev := quote.OpenLongB - fair.Value; -dev >= threshold {
			return DirectionLongB, dev
		}
	}
	return "", 0
}

// Exit reasons.
const (
	ExitRevert  = "revert"
	ExitStop    = "stop"
	ExitTimeout = "timeout"
	ExitManual  = "manual"
)

// Held is an open cycle as the signal sees it.
type Held struct {
	Direction string
	// EntryDev is the executable deviation the cycle was opened at (bps).
	EntryDev float64
	// Threshold at entry (bps): the stop is a multiple of it.
	Threshold float64
	OpenedAt  time.Time
}

// ExitDeviation is the deviation a market exit would realize now: closing a
// long-A cycle buys B back at its ask and sells A at its bid — the long-B
// entry spread — and vice versa.
func ExitDeviation(direction string, quote Quote, fair Fair) float64 {
	if direction == DirectionLongA {
		return quote.OpenLongB - fair.Value
	}
	return quote.OpenLongA - fair.Value
}

// Exit decides whether a held cycle should close now ("" to hold), from the
// executable exit deviation against the fair value frozen at entry.
func (rule Rule) Exit(held Held, quote Quote, fair Fair, now time.Time) string {
	dev := ExitDeviation(held.Direction, quote, fair)
	if math.IsNaN(dev) {
		if now.Sub(held.OpenedAt) >= time.Duration(rule.MaxHold)*time.Minute {
			return ExitTimeout
		}
		return ""
	}
	// Signed so that "further against us" is positive for both directions.
	adverse := dev
	if held.Direction == DirectionLongB {
		adverse = -dev
	}
	if adverse >= rule.StopMult*held.Threshold {
		return ExitStop
	}
	if now.Sub(held.OpenedAt) >= time.Duration(rule.MaxHold)*time.Minute {
		return ExitTimeout
	}
	if adverse <= rule.CloseSigma*fair.Sigma() {
		return ExitRevert
	}
	return ""
}

// RungDue reports whether a ladder adds a rung: the entry deviation has
// widened by another step beyond the last rung's.
func (rule Rule) RungDue(rungs int, lastEntryDev, dev float64, sigma float64) bool {
	if rule.LadderStep <= 0 || rungs >= rule.LadderMax {
		return false
	}
	return math.Abs(dev)-math.Abs(lastEntryDev) >= rule.LadderStep*sigma
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// Persistence tracks how long a condition has held, tolerating a share of
// samples that miss it (one stale push must not reset ten minutes).
type Persistence struct {
	Since  time.Time `json:"since"`
	Hits   int       `json:"hits"`
	Misses int       `json:"misses"`
	Key    string    `json:"key"`
}

// tolerance is the share of samples that may miss without resetting, with
// an allowance of a few misses early on (one stale push in the first seconds
// is not a broken condition).
const (
	tolerance = 0.1
	allowance = 3
)

// Observe records one sample: met is whether the condition held, key what
// it held for (a direction: switching direction restarts the clock). It
// returns how long the condition has held.
func (p Persistence) Observe(now time.Time, met bool, key string) (Persistence, time.Duration) {
	if met && (p.Since.IsZero() || p.Key != key) {
		return Persistence{Since: now, Hits: 1, Key: key}, 0
	}
	if p.Since.IsZero() {
		return p, 0
	}
	if met {
		p.Hits++
	} else {
		p.Misses++
		if p.Misses > allowance && float64(p.Misses) > tolerance*float64(p.Hits+p.Misses) {
			return Persistence{}, 0
		}
	}
	return p, now.Sub(p.Since)
}

// Reset clears the clock.
func (p Persistence) Reset() Persistence { return Persistence{} }
