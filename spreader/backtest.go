package spreader

import (
	"math"
	"sort"
	"time"
)

// Backtest replays the signal over minute bars of both legs: the same Fair,
// Entry, Exit and Persistence the evaluator runs, one decision per minute.
//
// Bars carry closes, not books, so the executable spreads are approximated
// as the close spread ± half of the round-trip crossing cost measured on the
// live books at creation (HalfSpread); results say so.

// Bar is one minute of a leg: its close.
type Bar struct {
	Time  time.Time
	Close float64
}

// FundingPoint is one settled funding rate of a leg (fraction per period,
// positive: longs pay).
type FundingPoint struct {
	Time time.Time
	Rate float64
}

// BacktestInput is a replay request.
type BacktestInput struct {
	Rule  Rule
	Ratio float64
	// A and B are the legs' minute bars (any order; matched by time).
	A, B []Bar
	// HalfSpread is half the round-trip crossing cost at the strategy's size,
	// in bps (from the live books).
	HalfSpread float64
	// FundingA, FundingB are the legs' settled funding over the window.
	FundingA, FundingB []FundingPoint
	// Warmup is how many leading minutes only feed the fair value.
	Warmup int
	// Size is the notional per cycle in leg A's quote (for PnL in money).
	Size float64
}

// BacktestCycle is one replayed open → close.
type BacktestCycle struct {
	Direction string    `json:"direction"`
	OpenedAt  time.Time `json:"openedAt"`
	ClosedAt  time.Time `json:"closedAt"`
	Reason    string    `json:"reason"`
	EntryDev  float64   `json:"entryDev"`
	ExitDev   float64   `json:"exitDev"`
	// Gross is the spread captured (bps); Cost the fees; Funding the net
	// funding over the hold (bps); Net their sum.
	Gross   float64 `json:"gross"`
	Cost    float64 `json:"cost"`
	Funding float64 `json:"funding"`
	Net     float64 `json:"net"`
	// MaxAdverse is the widest the deviation moved against the cycle (bps).
	MaxAdverse float64 `json:"maxAdverse"`
}

// BacktestPoint is one minute of the replay for the chart.
type BacktestPoint struct {
	Time  int64   `json:"t"`
	Mid   float64 `json:"mid"`
	Fair  float64 `json:"fair"`
	Sigma float64 `json:"sigma"`
}

// BacktestResult is the replay's cycles and summary.
type BacktestResult struct {
	Cycles  []BacktestCycle `json:"cycles"`
	Summary BacktestSummary `json:"summary"`
	// Points are the minutes after warm-up, thinned to at most 2000.
	Points []BacktestPoint `json:"points"`
	// Approximate: executable spreads came from closes and today's books.
	Approximate bool `json:"approximate"`
}

// BacktestSummary aggregates the cycles.
type BacktestSummary struct {
	Minutes     int     `json:"minutes"`
	Cycles      int     `json:"cycles"`
	Wins        int     `json:"wins"`
	NetBps      float64 `json:"netBps"`
	NetMoney    float64 `json:"netMoney"`
	AvgHoldMin  float64 `json:"avgHoldMin"`
	MaxAdverse  float64 `json:"maxAdverse"`
	Stops       int     `json:"stops"`
	Timeouts    int     `json:"timeouts"`
	OpenAtEnd   bool    `json:"openAtEnd"`
	FundingBps  float64 `json:"fundingBps"`
	AvgEntryDev float64 `json:"avgEntryDev"`
}

// Backtest runs the replay.
func Backtest(input BacktestInput) BacktestResult {
	rule := input.Rule.Normalize()
	closesB := make(map[int64]float64, len(input.B))
	for _, bar := range input.B {
		closesB[bar.Time.Unix()] = bar.Close
	}
	bars := append([]Bar(nil), input.A...)
	sort.Slice(bars, func(i, j int) bool { return bars[i].Time.Before(bars[j].Time) })

	result := BacktestResult{Approximate: true}
	var fair Fair
	var entry, exit Persistence
	var held *Held
	var heldCycle BacktestCycle
	var lastClose time.Time
	minutes := 0
	for index, bar := range bars {
		priceB, ok := closesB[bar.Time.Unix()]
		if !ok {
			continue
		}
		mid := SpreadBps(bar.Close, priceB, input.Ratio)
		if math.IsNaN(mid) {
			continue
		}
		quote := Quote{Mid: mid, OpenLongA: mid - input.HalfSpread, OpenLongB: mid + input.HalfSpread}
		now := bar.Time
		warm := index >= input.Warmup && fair.Warm(rule.FairHalfLife)
		if warm {
			minutes++
			result.Points = append(result.Points, BacktestPoint{Time: now.UnixMilli(), Mid: round2(mid), Fair: round2(fair.Value), Sigma: round2(fair.Sigma())})
		}
		if held != nil {
			dev := ExitDeviation(held.Direction, quote, fair)
			adverse := dev
			if held.Direction == DirectionLongB {
				adverse = -dev
			}
			heldCycle.MaxAdverse = math.Max(heldCycle.MaxAdverse, adverse-math.Abs(held.EntryDev))
			reason := rule.Exit(*held, quote, fair, now)
			var lasted time.Duration
			exit, lasted = exit.Observe(now, reason != "", reason)
			if reason != "" && (reason == ExitStop || reason == ExitTimeout || lasted >= time.Duration(rule.ClosePersist)*time.Minute) {
				heldCycle.ClosedAt, heldCycle.Reason, heldCycle.ExitDev = now, reason, dev
				// Captured: entry deviation minus exit deviation, signed for
				// the direction (long A gains when the spread falls back).
				if held.Direction == DirectionLongA {
					heldCycle.Gross = heldCycle.EntryDev - dev
				} else {
					heldCycle.Gross = dev - heldCycle.EntryDev
				}
				heldCycle.Cost = 2 * (rule.FeeA + rule.FeeB)
				heldCycle.Funding = fundingBps(held.Direction, input.FundingA, input.FundingB, heldCycle.OpenedAt, now)
				heldCycle.Net = heldCycle.Gross - heldCycle.Cost + heldCycle.Funding
				result.Cycles = append(result.Cycles, roundCycle(heldCycle))
				held, fair.Frozen, lastClose = nil, false, now
				exit, entry = exit.Reset(), entry.Reset()
			}
		}
		fair = fair.Update(mid, rule.FairHalfLife)
		if held == nil && warm && now.Sub(lastClose) >= time.Minute {
			direction, dev := rule.Entry(quote, fair)
			var lasted time.Duration
			entry, lasted = entry.Observe(now, direction != "", direction)
			if direction != "" && lasted >= time.Duration(rule.Persist)*time.Minute {
				threshold := rule.Threshold(quote, fair.Sigma())
				held = &Held{Direction: direction, EntryDev: dev, Threshold: threshold, OpenedAt: now}
				heldCycle = BacktestCycle{Direction: direction, OpenedAt: now, EntryDev: dev}
				fair.Frozen = true
				entry = entry.Reset()
			}
		}
	}
	result.Summary = summarize(result.Cycles, minutes, input.Size)
	result.Summary.OpenAtEnd = held != nil
	result.Points = thin(result.Points, 2000)
	return result
}

// fundingBps is the net funding a cycle earned over its hold, in bps of
// notional: a short leg receives its rate, a long leg pays it.
func fundingBps(direction string, a, b []FundingPoint, from, to time.Time) float64 {
	sum := func(points []FundingPoint, sign float64) float64 {
		total := 0.0
		for _, point := range points {
			if point.Time.After(from) && !point.Time.After(to) {
				total += sign * point.Rate
			}
		}
		return total
	}
	signA, signB := -1.0, 1.0 // long A pays A's rate; short B receives B's
	if direction == DirectionLongB {
		signA, signB = 1, -1
	}
	return (sum(a, signA) + sum(b, signB)) * 1e4
}

func summarize(cycles []BacktestCycle, minutes int, size float64) BacktestSummary {
	summary := BacktestSummary{Minutes: minutes, Cycles: len(cycles)}
	if len(cycles) == 0 {
		return summary
	}
	hold, entry := 0.0, 0.0
	for _, cycle := range cycles {
		if cycle.Net > 0 {
			summary.Wins++
		}
		summary.NetBps += cycle.Net
		summary.FundingBps += cycle.Funding
		hold += cycle.ClosedAt.Sub(cycle.OpenedAt).Minutes()
		entry += math.Abs(cycle.EntryDev)
		summary.MaxAdverse = math.Max(summary.MaxAdverse, cycle.MaxAdverse)
		switch cycle.Reason {
		case ExitStop:
			summary.Stops++
		case ExitTimeout:
			summary.Timeouts++
		}
	}
	summary.AvgHoldMin = round2(hold / float64(len(cycles)))
	summary.AvgEntryDev = round2(entry / float64(len(cycles)))
	summary.NetBps = round2(summary.NetBps)
	summary.FundingBps = round2(summary.FundingBps)
	summary.NetMoney = round2(summary.NetBps / 1e4 * size)
	summary.MaxAdverse = round2(summary.MaxAdverse)
	return summary
}

func roundCycle(cycle BacktestCycle) BacktestCycle {
	cycle.EntryDev, cycle.ExitDev = round2(cycle.EntryDev), round2(cycle.ExitDev)
	cycle.Gross, cycle.Cost, cycle.Funding, cycle.Net = round2(cycle.Gross), round2(cycle.Cost), round2(cycle.Funding), round2(cycle.Net)
	cycle.MaxAdverse = round2(math.Max(cycle.MaxAdverse, 0))
	return cycle
}

func round2(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*100) / 100
}

func thin(points []BacktestPoint, most int) []BacktestPoint {
	if len(points) <= most {
		return points
	}
	step := float64(len(points)) / float64(most)
	out := make([]BacktestPoint, 0, most)
	for i := 0.0; int(i) < len(points); i += step {
		out = append(out, points[int(i)])
	}
	return out
}
