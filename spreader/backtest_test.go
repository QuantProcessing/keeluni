package spreader

import (
	"math"
	"testing"
	"time"
)

// series builds two legs whose spread is offset + the given excursion at
// each minute.
func series(start time.Time, minutes int, offset float64, excursion func(int) float64) ([]Bar, []Bar) {
	a := make([]Bar, 0, minutes)
	b := make([]Bar, 0, minutes)
	for i := 0; i < minutes; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		priceA := 100000.0
		spread := offset + excursion(i)
		a = append(a, Bar{Time: at, Close: priceA})
		b = append(b, Bar{Time: at, Close: priceA * (1 + spread/1e4)})
	}
	return a, b
}

// A persistent structural offset with one sustained excursion: the replay
// absorbs the offset, opens once after the persistence window, closes on
// reversion, and earns the excursion minus costs.
func TestBacktestTradesOnePersistentExcursion(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	excursion := func(i int) float64 {
		noise := math.Sin(float64(i)*0.7) * 0.8
		switch {
		case i >= 3000 && i < 3060: // one hour 15 bps rich
			return 15 + noise
		case i >= 1500 && i < 1505: // five minutes: too short to trade
			return 15 + noise
		}
		return noise
	}
	a, b := series(start, 4000, -10, excursion)
	rule := Rule{FairHalfLife: 240, KSigma: 2, MinProfit: 1, FeeA: 1, FeeB: 1, Persist: 10, ClosePersist: 2}.Normalize()
	result := Backtest(BacktestInput{Rule: rule, Ratio: 1, A: a, B: b, HalfSpread: 0.5, Size: 10000})
	if len(result.Cycles) != 1 {
		t.Fatalf("cycles = %+v", result.Cycles)
	}
	cycle := result.Cycles[0]
	if cycle.Direction != DirectionLongA || cycle.Reason != ExitRevert {
		t.Fatalf("cycle = %+v", cycle)
	}
	opened := cycle.OpenedAt.Sub(start).Minutes()
	if opened < 3009 || opened > 3012 {
		t.Fatalf("opened at minute %.0f, want after the 10-minute window", opened)
	}
	closed := cycle.ClosedAt.Sub(start).Minutes()
	if closed < 3062 || closed > 3075 { // reversion confirmed over ClosePersist, through the noise
		t.Fatalf("closed at minute %.0f", closed)
	}
	// Entry ≈ 15 − 0.5 above fair; exit ≈ 0 + 0.5; gross ≈ 14, cost 4.
	if cycle.Gross < 12 || cycle.Gross > 16 || cycle.Cost != 4 || cycle.Net < 8 {
		t.Fatalf("pnl = %+v", cycle)
	}
	if result.Summary.Cycles != 1 || result.Summary.Wins != 1 || result.Summary.NetMoney <= 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}

// A level shift that never comes back stops out instead of averaging into it.
func TestBacktestStopsOnAStructuralBreak(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	excursion := func(i int) float64 {
		noise := math.Sin(float64(i)*0.7) * 0.8
		if i >= 3000 {
			return 10 + float64(i-3000)*0.2 + noise // drifts away for good
		}
		return noise
	}
	a, b := series(start, 3400, 0, excursion)
	rule := Rule{FairHalfLife: 240, KSigma: 2, MinProfit: 1, FeeA: 1, FeeB: 1, Persist: 5, StopMult: 3}.Normalize()
	result := Backtest(BacktestInput{Rule: rule, Ratio: 1, A: a, B: b, HalfSpread: 0.5, Size: 10000})
	if len(result.Cycles) == 0 || result.Cycles[0].Reason != ExitStop || result.Cycles[0].Net >= 0 {
		t.Fatalf("cycles = %+v", result.Cycles)
	}
}

func TestFundingCountsTheShortLegAsReceived(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	a := []FundingPoint{{Time: from.Add(8 * time.Hour), Rate: 0.0001}}
	b := []FundingPoint{{Time: from.Add(8 * time.Hour), Rate: 0.0003}, {Time: to.Add(time.Hour), Rate: 1}}
	// Long A pays 1 bp, short B receives 3 bps: +2.
	if got := fundingBps(DirectionLongA, a, b, from, to); math.Abs(got-2) > 1e-9 {
		t.Fatal(got)
	}
	if got := fundingBps(DirectionLongB, a, b, from, to); math.Abs(got+2) > 1e-9 {
		t.Fatal(got)
	}
}
