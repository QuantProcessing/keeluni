package spreader

import (
	"math"
	"testing"
	"time"
)

func TestSpreadBps(t *testing.T) {
	if got := SpreadBps(100, 100.1, 1); math.Abs(got-10) > 1e-9 {
		t.Fatal(got)
	}
	// 1000PEPE against PEPE: B quotes 1000 units.
	if got := SpreadBps(0.00001, 0.01001, 1000); math.Abs(got-10) > 1e-6 {
		t.Fatal(got)
	}
	if !math.IsNaN(SpreadBps(0, 1, 1)) {
		t.Fatal("missing price must be NaN")
	}
}

// A structural offset is absorbed: after a few half-lives at a constant
// −10 bps the fair value is −10 and a −10 sample is no deviation at all.
func TestFairAbsorbsAStructuralOffset(t *testing.T) {
	var fair Fair
	for i := 0; i < 600; i++ {
		fair = fair.Update(-10+math.Sin(float64(i))*0.5, 60)
	}
	if math.Abs(fair.Value+10) > 0.3 || fair.Sigma() < 0.2 || fair.Sigma() > 0.6 {
		t.Fatalf("fair = %+v sigma %.3f", fair, fair.Sigma())
	}
	if !fair.Warm(60) || (Fair{Samples: 100}).Warm(60) {
		t.Fatal("warm-up")
	}
}

// While a cycle is held the estimate stands still, so the deviation it trades
// is not absorbed into the fair value.
func TestFrozenFairDoesNotMove(t *testing.T) {
	fair := Fair{Value: 1, Variance: 4, Samples: 1000, Frozen: true}
	if next := fair.Update(50, 60); next != fair {
		t.Fatalf("frozen fair moved: %+v", next)
	}
	fair.Frozen = false
	if next := fair.Update(50, 60); next.Value <= 1 {
		t.Fatal("unfrozen fair did not move")
	}
	if next := fair.Update(math.NaN(), 60); next != fair {
		t.Fatal("NaN moved the fair value")
	}
}

func TestThresholdIsTheLargerOfCostAndSigma(t *testing.T) {
	rule := Rule{FeeA: 2, FeeB: 3, MinProfit: 2, KSigma: 2}.Normalize()
	quote := Quote{OpenLongA: 1, OpenLongB: -1}
	// cost = 2 × (2 + 3) + |1 − (−1)| = 12; + min profit 2 = 14.
	if cost := rule.Cost(quote); cost != 12 {
		t.Fatal(cost)
	}
	if got := rule.Threshold(quote, 1); got != 14 {
		t.Fatal(got)
	}
	if got := rule.Threshold(quote, 10); got != 20 {
		t.Fatal(got)
	}
}

func TestEntryPicksTheRichSideOnExecutablePrices(t *testing.T) {
	rule := Rule{FeeA: 1, FeeB: 1, MinProfit: 1, KSigma: 2}.Normalize()
	fair := Fair{Value: -10, Variance: 1, Samples: 5000}
	// Threshold: cost 2×2 + 1 = 5, +1 = 6 vs 2σ = 2 → 6.
	// B rich: selling B at its bid against A at its ask is 7 above fair.
	if direction, dev := rule.Entry(Quote{OpenLongA: -3, OpenLongB: -4}, fair); direction != DirectionLongA || dev != 7 {
		t.Fatalf("got %s %.1f", direction, dev)
	}
	// Mid deviates by 6.5 but the executable long-A deviation is only 5.5.
	if direction, _ := rule.Entry(Quote{OpenLongA: -4.5, OpenLongB: -5.5}, fair); direction != "" {
		t.Fatalf("entered on mid: %s", direction)
	}
	// A rich: buying B at its ask against A at its bid is 8 below fair.
	if direction, dev := rule.Entry(Quote{OpenLongA: -17, OpenLongB: -18}, fair); direction != DirectionLongB || dev != -8 {
		t.Fatalf("got %s %.1f", direction, dev)
	}
	// A one-sided rule ignores the other side.
	rule.Direction = DirectionLongA
	if direction, _ := rule.Entry(Quote{OpenLongA: -17, OpenLongB: -18}, fair); direction != "" {
		t.Fatal("long_a rule entered long B")
	}
}

func TestExitRevertsStopsAndTimesOut(t *testing.T) {
	rule := Rule{CloseSigma: 0.5, StopMult: 3, MaxHold: 60}.Normalize()
	fair := Fair{Value: 0, Variance: 4, Samples: 5000, Frozen: true}
	opened := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	held := Held{Direction: DirectionLongA, EntryDev: 8, Threshold: 6, OpenedAt: opened}
	at := opened.Add(10 * time.Minute)
	// Closing long A trades the long-B spread: back to 0.9 ≤ 0.5σ (1) → revert.
	if got := rule.Exit(held, Quote{OpenLongA: 2, OpenLongB: 0.9}, fair, at); got != ExitRevert {
		t.Fatal(got)
	}
	if got := rule.Exit(held, Quote{OpenLongA: 6, OpenLongB: 5}, fair, at); got != "" {
		t.Fatal(got)
	}
	// Wider still: 18 ≥ 3 × 6 → stop.
	if got := rule.Exit(held, Quote{OpenLongA: 19, OpenLongB: 18}, fair, at); got != ExitStop {
		t.Fatal(got)
	}
	if got := rule.Exit(held, Quote{OpenLongA: 6, OpenLongB: 5}, fair, opened.Add(61*time.Minute)); got != ExitTimeout {
		t.Fatal(got)
	}
	// Long B mirrors: the deviation is negative and reverts upwards.
	held.Direction = DirectionLongB
	if got := rule.Exit(held, Quote{OpenLongA: -0.5, OpenLongB: -1.5}, fair, at); got != ExitRevert {
		t.Fatal(got)
	}
	if got := rule.Exit(held, Quote{OpenLongA: -19, OpenLongB: -20}, fair, at); got != ExitStop {
		t.Fatal(got)
	}
}

func TestPersistenceToleratesASmallShareOfMisses(t *testing.T) {
	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	var p Persistence
	var held time.Duration
	for i := 0; i <= 600; i++ {
		// Every 20th second misses (5%).
		p, held = p.Observe(start.Add(time.Duration(i)*time.Second), i%20 != 7, DirectionLongA)
	}
	if held != 10*time.Minute {
		t.Fatalf("held %v", held)
	}
	// A run of misses beyond 10% resets.
	for i := 601; i < 700; i++ {
		p, held = p.Observe(start.Add(time.Duration(i)*time.Second), false, DirectionLongA)
	}
	if held != 0 || !p.Since.IsZero() {
		t.Fatalf("not reset: %+v %v", p, held)
	}
	// Switching direction restarts the clock.
	p, _ = p.Observe(start, true, DirectionLongA)
	p, held = p.Observe(start.Add(time.Minute), true, DirectionLongB)
	if held != 0 || p.Key != DirectionLongB {
		t.Fatal("direction switch kept the clock")
	}
}

func TestLadderAddsRungsByStep(t *testing.T) {
	rule := Rule{LadderStep: 1, LadderMax: 3}.Normalize()
	if !rule.RungDue(1, 8, 10.5, 2) || rule.RungDue(1, 8, 9, 2) || rule.RungDue(3, 8, 20, 2) {
		t.Fatal("ladder")
	}
	if (Rule{}).Normalize().RungDue(1, 0, 100, 1) {
		t.Fatal("no ladder by default")
	}
}

func TestNormalizeClampsAndDefaults(t *testing.T) {
	rule := Rule{FairHalfLife: 5, KSigma: -1, StopMult: 0.5, LadderStep: 0, LadderMax: 9}.Normalize()
	if rule.FairHalfLife != 30 || rule.KSigma != 2 || rule.StopMult != 3 || rule.LadderMax != 1 || rule.Direction != DirectionBoth {
		t.Fatalf("%+v", rule)
	}
}
