package pairexec

import (
	"testing"

	"github.com/shopspring/decimal"
)

func legPair(step0, step1 string) [2]Leg {
	return [2]Leg{
		{Index: 0, Instrument: "binance:perp:BTC-USDT", Side: "buy", StepSize: d(step0), TickSize: d("0.1"), MinQty: d(step0), MinNotional: d("5"), Quote: "USDT"},
		{Index: 1, Instrument: "okx:perp:BTC-USDT-SWAP", Side: "sell", StepSize: d(step1), TickSize: d("0.1"), MinQty: d(step1), Quote: "USDT"},
	}
}

func TestPlanSizeMatchesBaseAcrossDifferentSteps(t *testing.T) {
	// 0.0157 BTC: leg 0 steps 0.001, leg 1 steps 0.01 (OKX 0.01 contracts of
	// 0.01 BTC each would be 0.0001; use a coarse step to force the case).
	plan, err := PlanSize(SizeInput{Size: d("0.0157"), Unit: UnitBase, Match: MatchBase, Price: [2]decimal.Decimal{d("65000"), d("65010")}}, legPair("0.001", "0.01"))
	if err != nil {
		t.Fatal(err)
	}
	// Leg 1 floors to 0.01; leg 0 is re-derived from it so neither is larger.
	if !plan.Qty[0].Equal(d("0.01")) || !plan.Qty[1].Equal(d("0.01")) || !plan.Ratio.Equal(d("1")) {
		t.Fatalf("plan = %+v", plan)
	}
	if !plan.Mismatch.Equal(d("0.1")) || len(plan.Problems) != 0 {
		t.Fatalf("mismatch/problems = %s %v", plan.Mismatch, plan.Problems)
	}
}

func TestPlanSizeQuoteUnitAndRatio(t *testing.T) {
	// 1000 USDT of PEPE on leg 0 hedged by 1000PEPE on leg 1 (ratio 0.001).
	legs := legPair("1", "1")
	plan, err := PlanSize(SizeInput{Size: d("1000"), Unit: UnitQuote, Match: MatchBase, Ratio: d("0.001"), Price: [2]decimal.Decimal{d("0.00001"), d("0.01")}}, legs)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Qty[0].Equal(d("100000000")) || !plan.Qty[1].Equal(d("100000")) {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanSizeNotionalMatchesDifferentUnderlyings(t *testing.T) {
	// Long 0.1 BTC at 65000 against short ETH at 2500: 6500 USDT each side.
	plan, err := PlanSize(SizeInput{Size: d("0.1"), Unit: UnitBase, Match: MatchNotional, Price: [2]decimal.Decimal{d("65000"), d("2500")}}, legPair("0.001", "0.001"))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Qty[0].Equal(d("0.1")) || !plan.Qty[1].Equal(d("2.6")) || !plan.Ratio.Equal(d("26")) || !plan.Mismatch.IsZero() {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanSizeReportsEveryMinimum(t *testing.T) {
	plan, err := PlanSize(SizeInput{Size: d("0.00005"), Unit: UnitBase, Match: MatchBase, Price: [2]decimal.Decimal{d("65000"), d("65000")}}, legPair("0.001", "0.0001"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Problems) != 2 {
		t.Fatalf("problems = %v", plan.Problems)
	}
	if _, err := PlanSize(SizeInput{Size: d("1"), Unit: UnitBase, Match: MatchBase, Price: [2]decimal.Decimal{d("1"), decimal.Zero}}, legPair("1", "1")); err == nil || !IsInvalid(err) {
		t.Fatalf("missing price err = %v", err)
	}
	if _, err := PlanSize(SizeInput{Size: d("-1"), Unit: UnitBase, Match: MatchBase}, legPair("1", "1")); err == nil {
		t.Fatal("negative size accepted")
	}
	// Minimum notional: 0.001 BTC at 1000 is 1 USDT < 5.
	plan, _ = PlanSize(SizeInput{Size: d("0.001"), Unit: UnitBase, Match: MatchBase, Price: [2]decimal.Decimal{d("1000"), d("1000")}}, legPair("0.001", "0.001"))
	if len(plan.Problems) != 1 {
		t.Fatalf("min notional problems = %v", plan.Problems)
	}
}

func TestSweepBookAveragesAndFlagsShortBooks(t *testing.T) {
	asks := BookSide{{d("100"), d("1")}, {d("101"), d("1")}, {d("102"), d("10")}}
	sweep := SweepBook(asks, d("2"), true)
	if !sweep.AvgPrice.Equal(d("100.5")) || !sweep.SlippageBps.Equal(d("50")) || sweep.Short {
		t.Fatalf("buy sweep = %+v", sweep)
	}
	bids := BookSide{{d("100"), d("1")}, {d("99"), d("1")}}
	sweep = SweepBook(bids, d("3"), false)
	if !sweep.Short || !sweep.Filled.Equal(d("2")) || !sweep.SlippageBps.Equal(d("50")) {
		t.Fatalf("sell sweep = %+v", sweep)
	}
}

func TestRoundingHelpers(t *testing.T) {
	if !FloorToStep(d("0.01579"), d("0.001")).Equal(d("0.015")) || !FloorToStep(d("5"), d("2")).Equal(d("4")) {
		t.Fatal("FloorToStep")
	}
	if !tickToward(d("100.07"), d("0.1"), "buy").Equal(d("100")) || !tickToward(d("100.01"), d("0.1"), "sell").Equal(d("100.1")) {
		t.Fatal("tickToward")
	}
}
