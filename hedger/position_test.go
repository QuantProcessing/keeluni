package hedger

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestAccountsDeriveHoldingsPnLAndFees(t *testing.T) {
	group, _ := sampleGroup("hg")
	at := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	orders := []Order{
		// Open: the venue reported cumulative fills and averages.
		{ClientOrderID: "1", Leg: 0, Side: "buy", Filled: d("2"), AvgPrice: d("101"), CreatedAt: at},
		{ClientOrderID: "2", Leg: 1, Side: "sell", Filled: d("2"), AvgPrice: d("103"), CreatedAt: at},
		// Close half of each leg.
		{ClientOrderID: "3", Leg: 0, Side: "sell", Filled: d("1"), AvgPrice: d("110"), CreatedAt: at.Add(time.Hour)},
		{ClientOrderID: "4", Leg: 1, Side: "buy", Filled: d("1"), AvgPrice: d("108"), CreatedAt: at.Add(time.Hour)},
	}
	fills := []Fill{
		{ClientOrderID: "1", FillID: "a", Price: d("100"), Qty: d("1"), Fee: d("0.05"), FeeAsset: "USDT", Time: at},
		// The second fill of order 1 was never received: quantities still come
		// from the order's cumulative report.
		{ClientOrderID: "2", FillID: "c", Price: d("103"), Qty: d("2"), Fee: d("0.001"), FeeAsset: "BNB", Time: at},
		{ClientOrderID: "3", FillID: "d", Price: d("110"), Qty: d("1"), Fee: d("-0.01"), FeeAsset: "USDT", Time: at.Add(time.Hour)},
	}
	books := Accounts(group, orders, fills)
	long, short := books[0], books[1]
	if !long.Held.Equal(d("1")) || !long.AvgEntry.Equal(d("101")) || !long.RealizedPnL.Equal(d("9")) || !long.Fees.Equal(d("0.04")) || !long.AvgOpen.Equal(d("101")) {
		t.Fatalf("long book = %+v", long)
	}
	if !short.Held.Equal(d("1")) || !short.AvgEntry.Equal(d("103")) || !short.RealizedPnL.Equal(d("-5")) || !short.FeesOther["BNB"].Equal(d("0.001")) || !short.AvgClose.Equal(d("108")) {
		t.Fatalf("short book = %+v", short)
	}
	if !Exposure(group, books).IsZero() || !Imbalance(group, books).IsZero() {
		t.Fatalf("balanced hedge exposure=%s imbalance=%s", Exposure(group, books), Imbalance(group, books))
	}
	pnl := UnrealizedPnL(group, books, [2]decimal.Decimal{d("105"), d("105")})
	if !pnl[0].Equal(d("4")) || !pnl[1].Equal(d("-2")) {
		t.Fatalf("unrealized = %v", pnl)
	}
}

func TestEffectiveFillPrefersTheLargerReportAndMarksEstimates(t *testing.T) {
	order := Order{Filled: d("1"), RefPrice: d("100")}
	got := EffectiveFill(order, []Fill{{Qty: d("0.4"), Price: d("99")}, {Qty: d("0.8"), Price: d("101")}})
	if !got.Qty.Equal(d("1.2")) || got.Estimated || !got.AvgPrice.Round(4).Equal(d("100.3333")) {
		t.Fatalf("fills larger than the report: %+v", got)
	}
	got = EffectiveFill(order, nil)
	if !got.Qty.Equal(d("1")) || !got.Estimated || !got.AvgPrice.Equal(d("100")) {
		t.Fatalf("report without a price: %+v", got)
	}
}

func TestSpotBaseFeesReduceTheHeldQuantity(t *testing.T) {
	group, _ := sampleGroup("hg")
	group.Legs[0].Product = "spot"
	orders := []Order{{ClientOrderID: "1", Leg: 0, Side: "buy", Filled: d("0.01"), AvgPrice: d("65000"), CreatedAt: time.Now()}}
	fills := []Fill{{ClientOrderID: "1", FillID: "a", Price: d("65000"), Qty: d("0.01"), Fee: d("0.00001"), FeeAsset: "BTC", Time: time.Now()}}
	books := Accounts(group, orders, fills)
	if !books[0].Held.Equal(d("0.00999")) || !books[0].Fees.Equal(d("0.65")) {
		t.Fatalf("spot book = %+v", books[0])
	}
}

func TestExposureOfAnUnhedgedLegAndNegligibleImbalance(t *testing.T) {
	group, _ := sampleGroup("hg")
	orders := []Order{{ClientOrderID: "1", Leg: 0, Side: "buy", Filled: d("0.5"), AvgPrice: d("100"), CreatedAt: time.Now()}}
	books := Accounts(group, orders, nil)
	if !Exposure(group, books).Equal(d("0.5")) || !Imbalance(group, books).Equal(d("0.5")) || Negligible(group, Imbalance(group, books)) {
		t.Fatalf("exposure=%s imbalance=%s", Exposure(group, books), Imbalance(group, books))
	}
	// Below leg 0's step (0.001) and leg 1's minimum (0.0001): negligible.
	if !Negligible(group, d("0.00005")) {
		t.Fatal("dust imbalance not negligible")
	}
}

func TestSpreadBps(t *testing.T) {
	group, _ := sampleGroup("hg")
	if got := SpreadBps(group, d("100"), d("100.25")); !got.Equal(d("25")) {
		t.Fatalf("spread = %s", got)
	}
	group.Match = MatchNotional
	if !SpreadBps(group, d("100"), d("2")).IsZero() {
		t.Fatal("spread across underlyings")
	}
}

func TestTrimAndRehedgeTargets(t *testing.T) {
	group, _ := sampleGroup("hg") // steps 0.001 / 0.0001, ratio 1
	books := [2]LegBook{{Held: d("0.01")}, {Held: d("0.0042")}}
	trim := TrimTargets(group, books)
	if !trim[0].Equal(d("-0.006")) || !trim[1].Equal(d("-0.0002")) {
		t.Fatalf("trim = %v", trim)
	}
	rehedge := RehedgeTargets(group, books)
	if !rehedge[0].IsZero() || !rehedge[1].Equal(d("0.0058")) {
		t.Fatalf("rehedge = %v", rehedge)
	}
	// Leg 2 larger: rehedge tops up leg 1, floored to its coarser step; the
	// 0.0007 left is below leg 1's step and counts as balanced.
	books = [2]LegBook{{Held: d("0.004")}, {Held: d("0.0057")}}
	rehedge = RehedgeTargets(group, books)
	if !rehedge[0].Equal(d("0.001")) || !rehedge[1].IsZero() {
		t.Fatalf("rehedge leg 1 = %v", rehedge)
	}
	after := [2]LegBook{{Held: d("0.005")}, {Held: d("0.0057")}}
	if !Negligible(group, Imbalance(group, after)) {
		t.Fatal("a residual below the coarser step is not balanced")
	}
	// An empty leg trims the other to zero.
	trim = TrimTargets(group, [2]LegBook{{Held: d("0.01")}, {}})
	if !trim[0].Equal(d("-0.01")) || !trim[1].IsZero() {
		t.Fatalf("trim empty = %v", trim)
	}
}
