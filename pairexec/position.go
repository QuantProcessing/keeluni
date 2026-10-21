package pairexec

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// A group's position is derived from its orders and fills; nothing here is
// stored. Quantities and prices come from each order's cumulative fill
// (orders report cumulative filled quantity, so a lost fill event cannot
// change them); fill records add fees. Accounting is average-cost: closing
// fills realize PnL against the running average entry.

// LegBook is one leg's accounting.
type LegBook struct {
	Held        decimal.Decimal            `json:"held"`     // base quantity held (>= 0)
	AvgEntry    decimal.Decimal            `json:"avgEntry"` // average price of the held quantity
	Opened      decimal.Decimal            `json:"opened"`   // base ever opened
	Closed      decimal.Decimal            `json:"closed"`   // base ever closed
	AvgOpen     decimal.Decimal            `json:"avgOpen"`  // average price of all opening fills
	AvgClose    decimal.Decimal            `json:"avgClose"` // average price of all closing fills
	RealizedPnL decimal.Decimal            `json:"realizedPnl"`
	Fees        decimal.Decimal            `json:"fees"`                // in quote; negative is a net rebate
	FeesOther   map[string]decimal.Decimal `json:"feesOther,omitempty"` // fees charged in other assets
	// Estimated is set when a fill price had to be taken from the order's
	// reference price because the venue reported no average.
	Estimated bool `json:"estimated,omitempty"`
}

// OrderFill is an order's effective execution.
type OrderFill struct {
	Qty       decimal.Decimal
	AvgPrice  decimal.Decimal
	Estimated bool
}

// EffectiveFill combines an order's cumulative report with its fill records:
// the larger quantity wins, and the price is the venue's average, else the
// fills' VWAP, else the reference price (estimated).
func EffectiveFill(order Order, fills []Fill) OrderFill {
	sumQty, sumCost := decimal.Zero, decimal.Zero
	for _, fill := range fills {
		sumQty = sumQty.Add(fill.Qty)
		sumCost = sumCost.Add(fill.Qty.Mul(fill.Price))
	}
	result := OrderFill{Qty: decimal.Max(order.Filled, sumQty)}
	switch {
	case !result.Qty.IsPositive():
	case order.AvgPrice.IsPositive():
		result.AvgPrice = order.AvgPrice
	case sumQty.IsPositive():
		result.AvgPrice = sumCost.Div(sumQty)
	case order.Price.IsPositive():
		result.AvgPrice, result.Estimated = order.Price, true
	default:
		result.AvgPrice, result.Estimated = order.RefPrice, true
	}
	return result
}

func fillsByOrder(fills []Fill) map[string][]Fill {
	result := make(map[string][]Fill, len(fills))
	for _, fill := range fills {
		result[fill.ClientOrderID] = append(result[fill.ClientOrderID], fill)
	}
	return result
}

// Accounts derives both legs' books from a group's orders and fills.
func Accounts(group Group, orders []Order, fills []Fill) [2]LegBook {
	sorted := append([]Order(nil), orders...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) })
	byOrder := fillsByOrder(fills)

	var books [2]LegBook
	var openCost, closeCost [2]decimal.Decimal
	for _, order := range sorted {
		if order.Leg < 0 || order.Leg > 1 {
			continue
		}
		leg := group.Legs[order.Leg]
		book := &books[order.Leg]
		executed := EffectiveFill(order, byOrder[order.ClientOrderID])
		book.Estimated = book.Estimated || executed.Estimated
		// Fees: quote-asset fees are a cost; base-asset fees on a spot leg
		// also reduce the quantity actually held.
		baseFee := decimal.Zero
		for _, fill := range byOrder[order.ClientOrderID] {
			switch {
			case fill.Fee.IsZero():
			case fill.FeeAsset == "" || fill.FeeAsset == leg.Quote:
				book.Fees = book.Fees.Add(fill.Fee)
			case fill.FeeAsset == leg.Base && leg.Product == "spot":
				baseFee = baseFee.Add(fill.Fee)
				book.Fees = book.Fees.Add(fill.Fee.Mul(fill.Price))
			default:
				if book.FeesOther == nil {
					book.FeesOther = map[string]decimal.Decimal{}
				}
				book.FeesOther[fill.FeeAsset] = book.FeesOther[fill.FeeAsset].Add(fill.Fee)
			}
		}
		if !executed.Qty.IsPositive() {
			continue
		}
		if order.Side == leg.Side {
			cost := book.Held.Mul(book.AvgEntry).Add(executed.Qty.Mul(executed.AvgPrice))
			book.Held = book.Held.Add(executed.Qty)
			book.AvgEntry = cost.Div(book.Held)
			book.Held = book.Held.Sub(baseFee)
			book.Opened = book.Opened.Add(executed.Qty)
			openCost[order.Leg] = openCost[order.Leg].Add(executed.Qty.Mul(executed.AvgPrice))
			continue
		}
		qty := decimal.Min(executed.Qty, book.Held)
		direction := decimal.NewFromInt(leg.Direction())
		book.RealizedPnL = book.RealizedPnL.Add(executed.AvgPrice.Sub(book.AvgEntry).Mul(qty).Mul(direction))
		book.Held = book.Held.Sub(qty).Sub(baseFee)
		book.Closed = book.Closed.Add(executed.Qty)
		closeCost[order.Leg] = closeCost[order.Leg].Add(executed.Qty.Mul(executed.AvgPrice))
		if !book.Held.IsPositive() {
			book.Held, book.AvgEntry = decimal.Zero, decimal.Zero
		}
	}
	for i := range books {
		if books[i].Opened.IsPositive() {
			books[i].AvgOpen = openCost[i].Div(books[i].Opened)
		}
		if books[i].Closed.IsPositive() {
			books[i].AvgClose = closeCost[i].Div(books[i].Closed)
		}
	}
	return books
}

// AccountsAt derives the books as they stood just before at: fills recorded
// after at are ignored; orders without fill records count from their
// creation (market orders) or their last update (resting orders).
func AccountsAt(group Group, orders []Order, fills []Fill, at time.Time) [2]LegBook {
	byOrder := fillsByOrder(fills)
	var keptOrders []Order
	var keptFills []Fill
	for _, order := range orders {
		if !order.CreatedAt.Before(at) {
			continue
		}
		if records := byOrder[order.ClientOrderID]; len(records) > 0 {
			order.Filled = decimal.Zero // use only the records before at
			for _, fill := range records {
				if fill.Time.Before(at) {
					keptFills = append(keptFills, fill)
				}
			}
		} else if order.Type == "limit" && !order.UpdatedAt.Before(at) {
			continue
		}
		keptOrders = append(keptOrders, order)
	}
	return Accounts(group, keptOrders, keptFills)
}

// Exposure is the group's net directional exposure in leg 0 base units: leg 0
// held (signed) plus leg 1 held converted back through the ratio (signed).
// Zero for a balanced hedge.
func Exposure(group Group, books [2]LegBook) decimal.Decimal {
	held0 := books[0].Held.Mul(decimal.NewFromInt(group.Legs[0].Direction()))
	held1 := books[1].Held.Mul(decimal.NewFromInt(group.Legs[1].Direction()))
	if !group.Ratio.IsPositive() {
		return held0
	}
	return held0.Add(held1.Div(group.Ratio))
}

// Imbalance is how much leg 1 must trade (signed, in leg 1 base; positive on
// its opening side) to match leg 0 at the group's ratio.
func Imbalance(group Group, books [2]LegBook) decimal.Decimal {
	return books[0].Held.Mul(group.Ratio).Sub(books[1].Held)
}

// TrimTargets returns the signed quantities (≤ 0) that reduce the larger leg
// so both legs match at the group's ratio, keeping as much of the hedge as
// the two venues' steps allow. When one leg holds nothing, the other is
// closed entirely. The residual left is always Negligible.
func TrimTargets(group Group, books [2]LegBook) [2]decimal.Decimal {
	if !group.Ratio.IsPositive() {
		return [2]decimal.Decimal{books[0].Held.Neg(), books[1].Held.Neg()}
	}
	keep0 := FloorToStep(decimal.Min(books[0].Held, books[1].Held.Div(group.Ratio)), group.Legs[0].StepSize)
	keep1 := FloorToStep(keep0.Mul(group.Ratio), group.Legs[1].StepSize)
	// When leg 1 is the coarser leg, flooring it can leave more than a step
	// of leg 0 unmatched: re-derive leg 0 from the rounded leg 1.
	keep0 = FloorToStep(keep1.Div(group.Ratio), group.Legs[0].StepSize)
	return [2]decimal.Decimal{keep0.Sub(books[0].Held), keep1.Sub(books[1].Held)}
}

// RehedgeTargets returns the signed quantities (≥ 0) that add to the smaller
// leg so it matches the larger one: the hedge is restored to full size.
func RehedgeTargets(group Group, books [2]LegBook) [2]decimal.Decimal {
	imbalance := Imbalance(group, books) // leg-1 units; > 0: leg 1 is short of leg 0
	if imbalance.IsPositive() {
		return [2]decimal.Decimal{decimal.Zero, FloorToStep(imbalance, group.Legs[1].StepSize)}
	}
	if !group.Ratio.IsPositive() {
		return [2]decimal.Decimal{}
	}
	return [2]decimal.Decimal{FloorToStep(imbalance.Neg().Div(group.Ratio), group.Legs[0].StepSize), decimal.Zero}
}

// Negligible reports whether an imbalance (leg-1 units) is as small as the
// two venues allow: two legs can only be matched to within the coarser of
// their steps, and a residual below both legs' minimum order sizes cannot be
// traded at all.
func Negligible(group Group, imbalance decimal.Decimal) bool {
	ratio := group.Ratio
	if !ratio.IsPositive() {
		ratio = decimal.NewFromInt(1)
	}
	abs1 := imbalance.Abs()
	abs0 := abs1.Div(ratio)
	coarse0 := decimal.Max(group.Legs[0].StepSize, group.Legs[1].StepSize.Div(ratio))
	if abs0.LessThan(coarse0) {
		return true
	}
	minimum := func(leg Leg) decimal.Decimal { return decimal.Max(leg.StepSize, leg.MinQty) }
	return abs1.LessThan(minimum(group.Legs[1])) && abs0.LessThan(minimum(group.Legs[0]))
}

// SpreadBps is the price spread between the legs in basis points of leg 0:
// (leg-1 price ÷ ratio − leg-0 price) ÷ leg-0 price. It is meaningful for
// same-underlying hedges only (MatchBase); zero otherwise.
func SpreadBps(group Group, price0, price1 decimal.Decimal) decimal.Decimal {
	if group.Match != MatchBase || !price0.IsPositive() || !price1.IsPositive() || !group.Ratio.IsPositive() {
		return decimal.Zero
	}
	return price1.Div(group.Ratio).Sub(price0).Div(price0).Mul(decimal.NewFromInt(10000)).Round(2)
}

// UnrealizedPnL marks each leg's held quantity at its mark price.
func UnrealizedPnL(group Group, books [2]LegBook, marks [2]decimal.Decimal) [2]decimal.Decimal {
	var result [2]decimal.Decimal
	for i, book := range books {
		if book.Held.IsPositive() && marks[i].IsPositive() {
			direction := decimal.NewFromInt(group.Legs[i].Direction())
			result[i] = marks[i].Sub(book.AvgEntry).Mul(book.Held).Mul(direction)
		}
	}
	return result
}
