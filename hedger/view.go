package hedger

import (
	"context"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/shopspring/decimal"
)

// GroupView is a group as the trader sees it: one hedge, not two positions.
type GroupView struct {
	Group
	Working *Action    `json:"working,omitempty"`
	Legs    [2]LegView `json:"legView"`
	// Exposure is the net directional exposure in leg 0 base units (0 when
	// hedged); ExposureValue is it in quote at leg 0's mark.
	Exposure      decimal.Decimal `json:"exposure"`
	ExposureValue decimal.Decimal `json:"exposureValue"`
	Imbalance     decimal.Decimal `json:"imbalance"`
	Balanced      bool            `json:"balanced"`
	// Spreads (bps of leg 0, same-underlying hedges only): at entry (average
	// open prices), now (marks) and to exit now (the prices a close would
	// trade at).
	EntrySpreadBps decimal.Decimal `json:"entrySpreadBps"`
	SpreadBps      decimal.Decimal `json:"spreadBps"`
	ExitSpreadBps  decimal.Decimal `json:"exitSpreadBps"`
	// Funding: the net per-year rate the hedge earns at the current quotes
	// (positive: earns), the next settlement amount of each leg, and the
	// estimated funding earned so far.
	NetFundingApr decimal.Decimal `json:"netFundingApr"`
	FundingEarned decimal.Decimal `json:"fundingEarned"`
	// PnL in quote: legs' unrealized + realized − fees + funding.
	UnrealizedPnL decimal.Decimal `json:"unrealizedPnl"`
	RealizedPnL   decimal.Decimal `json:"realizedPnl"`
	Fees          decimal.Decimal `json:"fees"`
	NetPnL        decimal.Decimal `json:"netPnl"`
	Notional      decimal.Decimal `json:"notional"`
	Estimated     bool            `json:"estimated,omitempty"`
}

// LegView is one leg's live state inside a group.
type LegView struct {
	LegBook
	Leg
	Mark          decimal.Decimal `json:"mark"`
	Bid           decimal.Decimal `json:"bid"`
	Ask           decimal.Decimal `json:"ask"`
	Notional      decimal.Decimal `json:"notional"`
	UnrealizedPnL decimal.Decimal `json:"unrealizedPnl"`
	// Funding of this leg: rate per settlement, period, APR, and the amount
	// the held position pays (<0) or receives (>0) at the next settlement.
	FundingRate     decimal.Decimal `json:"fundingRate"`
	FundingHours    int64           `json:"fundingHours,omitempty"`
	FundingApr      decimal.Decimal `json:"fundingApr"`
	NextFunding     int64           `json:"nextFunding,omitempty"`
	NextFundingCash decimal.Decimal `json:"nextFundingCash"`
	FundingEarned   decimal.Decimal `json:"fundingEarned"`
	FundingStale    bool            `json:"fundingStale,omitempty"`
	// Venue position (the account's whole position in the instrument, which
	// may include trades outside this hedge) and its liquidation distance.
	VenueSize        decimal.Decimal `json:"venueSize"`
	VenueSide        string          `json:"venueSide,omitempty"`
	LiquidationPrice decimal.Decimal `json:"liquidationPrice"`
	LiquidationPct   decimal.Decimal `json:"liquidationPct"` // % move to liquidation
	Leverage         string          `json:"leverage,omitempty"`
	AccountLive      bool            `json:"accountLive"`
}

// fundingSign is the cash sign for a held leg: a long pays a positive rate.
func fundingSign(leg Leg) decimal.Decimal {
	return decimal.NewFromInt(-leg.Direction())
}

// View builds the trader's view of one group from stored facts and the live
// market cache. It never calls a venue.
func (engine *Engine) View(ctx context.Context, group Group, funding [2]decimal.Decimal) (GroupView, error) {
	orders, err := engine.store.Orders(ctx, group.ID)
	if err != nil {
		return GroupView{}, err
	}
	fills, err := engine.store.Fills(ctx, group.ID)
	if err != nil {
		return GroupView{}, err
	}
	books := Accounts(group, orders, fills)
	view := GroupView{Group: group, FundingEarned: funding[0].Add(funding[1])}
	if action, working := engine.Working(group.ID); working {
		view.Working = &action
	}
	var marks [2]decimal.Decimal
	for i, leg := range group.Legs {
		legView := LegView{LegBook: books[i], Leg: leg, FundingEarned: funding[i]}
		if engine.market != nil {
			legView.Bid, legView.Ask = engine.market.Top(leg.Instrument)
			legView.AccountLive = engine.market.AccountLive(leg.AccountID)
			if position, held, _ := engine.market.Position(leg.AccountID, leg.Instrument); held {
				legView.VenueSize, _ = decimal.NewFromString(position.Size)
				legView.VenueSide = position.Side
				legView.Leverage = position.Leverage
				legView.LiquidationPrice, _ = decimal.NewFromString(position.LiquidationPrice)
				legView.Mark, _ = decimal.NewFromString(position.MarkPrice)
			}
			if leg.Product == "perp" {
				if quote, ok := engine.market.Funding(leg.Instrument); ok {
					legView.FundingRate, _ = decimal.NewFromString(quote.Rate)
					legView.FundingApr, _ = decimal.NewFromString(quote.Apr)
					legView.FundingHours, legView.NextFunding, legView.FundingStale = quote.IntervalHours, quote.NextFunding, quote.Stale
				}
			}
		}
		if !legView.Mark.IsPositive() && legView.Bid.IsPositive() && legView.Ask.IsPositive() {
			legView.Mark = legView.Bid.Add(legView.Ask).Div(decimal.NewFromInt(2))
		}
		marks[i] = legView.Mark
		legView.Notional = books[i].Held.Mul(legView.Mark)
		if legView.LiquidationPrice.IsPositive() && legView.Mark.IsPositive() {
			legView.LiquidationPct = legView.LiquidationPrice.Sub(legView.Mark).Div(legView.Mark).Mul(decimal.NewFromInt(100)).Abs().Round(2)
		}
		if leg.Product == "perp" && books[i].Held.IsPositive() {
			legView.NextFundingCash = legView.FundingRate.Mul(legView.Notional).Mul(fundingSign(leg)).Round(6)
		}
		view.Legs[i] = legView
		view.Estimated = view.Estimated || books[i].Estimated
	}
	unrealized := UnrealizedPnL(group, books, marks)
	for i := range view.Legs {
		view.Legs[i].UnrealizedPnL = unrealized[i]
		view.UnrealizedPnL = view.UnrealizedPnL.Add(unrealized[i])
		view.RealizedPnL = view.RealizedPnL.Add(books[i].RealizedPnL)
		view.Fees = view.Fees.Add(books[i].Fees)
		view.Notional = view.Notional.Add(view.Legs[i].Notional)
	}
	view.NetPnL = view.UnrealizedPnL.Add(view.RealizedPnL).Sub(view.Fees).Add(view.FundingEarned)
	view.Exposure = Exposure(group, books)
	view.ExposureValue = view.Exposure.Mul(marks[0])
	view.Imbalance = Imbalance(group, books)
	view.Balanced = Negligible(group, view.Imbalance)
	view.EntrySpreadBps = SpreadBps(group, books[0].AvgOpen, books[1].AvgOpen)
	view.SpreadBps = SpreadBps(group, marks[0], marks[1])
	if engine.market != nil {
		exit0 := engine.market.TradePrice(group.Legs[0].Instrument, group.Legs[0].CloseSide())
		exit1 := engine.market.TradePrice(group.Legs[1].Instrument, group.Legs[1].CloseSide())
		view.ExitSpreadBps = SpreadBps(group, exit0, exit1)
	}
	// Net funding APR weighted by each leg's notional, over the hedge's
	// gross notional / 2 (one side's capital).
	if view.Notional.IsPositive() {
		earn := decimal.Zero
		for i, leg := range group.Legs {
			if leg.Product != "perp" {
				continue
			}
			earn = earn.Add(view.Legs[i].FundingApr.Mul(view.Legs[i].Notional).Mul(fundingSign(leg)))
		}
		view.NetFundingApr = earn.Div(view.Notional.Div(decimal.NewFromInt(2))).Round(6)
	}
	return view, nil
}

// ---------------------------------------------------------------------------
// Funding accrual
// ---------------------------------------------------------------------------

// FundingSource reads settled funding (the gateway's funding history).
type FundingSource interface {
	FundingHistory(ctx context.Context, instrument string, since, until time.Time) ([]gwclient.FundingPoint, error)
}

// accrueFunding estimates every perp leg's funding from the venue's settled
// rates: at each settlement after the group opened (and before it closed)
// at which the leg held a position, amount = rate × held × price, paid by a
// long and received by a short. Price is the mark for recent settlements
// and the leg's average entry for older ones (catch-up after downtime), so
// amounts are estimates; exact figures are on each venue's statement.
// Accruals are keyed by settlement, so repeated runs add nothing twice.
func (engine *Engine) accrueFunding(ctx context.Context, source FundingSource) {
	now := engine.now().UTC()
	groups, err := engine.store.Groups(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return
	}
	for _, group := range groups {
		if group.Status == StatusFailed {
			continue
		}
		orders, err := engine.store.Orders(ctx, group.ID)
		if err != nil || len(orders) == 0 {
			continue
		}
		fills, err := engine.store.Fills(ctx, group.ID)
		if err != nil {
			continue
		}
		since := orders[0].CreatedAt
		if oldest := now.Add(-29 * 24 * time.Hour); since.Before(oldest) {
			since = oldest
		}
		until := now
		if group.ClosedAt != nil && group.ClosedAt.Before(until) {
			until = *group.ClosedAt
		}
		if !since.Before(until) {
			continue
		}
		for i, leg := range group.Legs {
			if leg.Product != "perp" {
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			points, err := source.FundingHistory(callCtx, leg.Instrument, since, until)
			cancel()
			if err != nil {
				continue
			}
			for _, point := range points {
				at := time.UnixMilli(point.Time).UTC()
				if !at.After(since) || at.After(until) {
					continue
				}
				rate, err := decimal.NewFromString(point.Rate)
				if err != nil {
					continue
				}
				books := AccountsAt(group, orders, fills, at)
				held := books[i].Held
				if !held.IsPositive() {
					continue
				}
				price := books[i].AvgEntry
				if now.Sub(at) < time.Hour && engine.market != nil {
					if bid, ask := engine.market.Top(leg.Instrument); bid.IsPositive() && ask.IsPositive() {
						price = bid.Add(ask).Div(decimal.NewFromInt(2))
					}
				}
				if !price.IsPositive() {
					continue
				}
				amount := rate.Mul(held).Mul(price).Mul(fundingSign(leg)).Round(8)
				accrual := FundingAccrual{GroupID: group.ID, Leg: i, SettledAt: at, Rate: rate, Qty: held, Price: price.Round(8), Amount: amount}
				if added, err := engine.store.AddFunding(ctx, accrual); err == nil && added {
					engine.changed(group.ID)
				}
			}
		}
	}
}
