package optioner

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/QuantProcessing/keeluni/optionmath"
)

// Risk is the portfolio's exposure per underlying, in the strike currency
// (USD) per unit move: what an options desk watches before anything else.
//
// Delta is "cash" delta in units of the underlying and includes:
//   - option deltas (Black-76 on each expiry's forward);
//   - perp and spot positions on the same underlying, when included;
//   - for inverse options (premium in the coin), the coin the position's
//     value is held in: a short inverse call's premium is coin you are long.
//     Deribit calls this premium-adjusted delta; without it a "flat" BTC
//     book quoted in BTC is not flat in dollars.
//
// Gamma is per 1% move ($ delta change per 1% of spot), vega per vol point,
// theta per calendar day — the units a trader sizes in.
type Risk struct {
	Underlyings []UnderlyingRisk `json:"underlyings"`
	UpdatedAt   int64            `json:"updatedAt"`
	// Stale is set when a chain or an account stream is not live.
	Stale bool `json:"stale"`
}

type UnderlyingRisk struct {
	Underlying string  `json:"underlying"`
	Spot       float64 `json:"spot"`
	Greeks     Greeks  `json:"greeks"`
	// HedgeDelta is the delta held in perps and spot (part of Greeks.Delta).
	HedgeDelta float64        `json:"hedgeDelta"`
	Expiries   []ExpiryRisk   `json:"expiries"`
	Positions  []PositionRisk `json:"positions"`
	// Scenario is the PnL grid: spot moves × vol moves, full revaluation.
	Scenario Scenario `json:"scenario"`
}

// Greeks of a position or a sum, in USD per the unit named on Risk.
type Greeks struct {
	Delta     float64 `json:"delta"`     // units of the underlying
	DeltaUSD  float64 `json:"deltaUsd"`  // delta × spot
	Gamma1Pct float64 `json:"gamma1pct"` // $ delta change per +1% spot
	Vega      float64 `json:"vega"`      // $ per +1 vol point
	Theta     float64 `json:"theta"`     // $ per day
	Value     float64 `json:"value"`     // mark value, $
	PnL       float64 `json:"pnl"`       // unrealized vs entry, $
}

func (g *Greeks) add(other Greeks) {
	g.Delta += other.Delta
	g.DeltaUSD += other.DeltaUSD
	g.Gamma1Pct += other.Gamma1Pct
	g.Vega += other.Vega
	g.Theta += other.Theta
	g.Value += other.Value
	g.PnL += other.PnL
}

type ExpiryRisk struct {
	Expiry int64   `json:"expiry"`
	DTE    float64 `json:"dte"`
	Greeks Greeks  `json:"greeks"`
}

// PositionRisk is one held contract with its greeks (signed by side).
type PositionRisk struct {
	Account    string  `json:"account"`
	Instrument string  `json:"inst"`
	Product    string  `json:"product"`
	Kind       string  `json:"kind,omitempty"`
	Strike     string  `json:"strike,omitempty"`
	Expiry     int64   `json:"expiry,omitempty"`
	Size       float64 `json:"size"` // signed, units of the underlying
	Entry      string  `json:"entry"`
	Mark       string  `json:"mark"`
	MarkIV     float64 `json:"markIv,omitempty"`
	// PremiumAsset is what entry and mark are quoted in.
	PremiumAsset string `json:"premiumAsset,omitempty"`
	Greeks       Greeks `json:"greeks"`
}

// Scenario is a revaluation grid. PnL[i][j] is the change in portfolio
// value ($) when spot moves by SpotMoves[i] and every IV by VolMoves[j]
// points, holding time; forwards move with spot.
type Scenario struct {
	SpotMoves []float64   `json:"spotMoves"` // fractions: -0.2 is -20%
	VolMoves  []float64   `json:"volMoves"`  // vol points
	PnL       [][]float64 `json:"pnl"`
	// Days is the horizon the grid also rolls forward (theta included).
	Days float64 `json:"days"`
}

var (
	defaultSpotMoves = []float64{-0.3, -0.2, -0.15, -0.1, -0.05, -0.02, 0, 0.02, 0.05, 0.1, 0.15, 0.2, 0.3}
	defaultVolMoves  = []float64{-20, -10, -5, 0, 5, 10, 20}
)

// RiskOptions selects what the report includes.
type RiskOptions struct {
	// Accounts filters (empty: every account).
	Accounts map[string]bool
	// IncludeHedges adds perps and spot of the same underlyings.
	IncludeHedges bool
	// HorizonDays rolls the scenario grid forward in time.
	HorizonDays float64
}

func parse(text string) float64 {
	value, _ := strconv.ParseFloat(text, 64)
	return value
}

// leg is one position normalized for valuation.
type leg struct {
	risk       PositionRisk
	underlying string
	option     bool
	call       bool
	strike     float64
	expiry     time.Time
	forward    float64
	discount   float64
	iv         float64
	inverse    bool
	entry      float64 // in USD per unit for options, price for linear
	premium    float64 // mark in USD per unit
}

// risk builds the report from the current state.
func (s *state) risk(options RiskOptions, at time.Time) Risk {
	s.mu.RLock()
	defer s.mu.RUnlock()
	report := Risk{UpdatedAt: at.UnixMilli(), Underlyings: []UnderlyingRisk{}}
	if !s.connected {
		report.Stale = true
	}
	spots := map[string]float64{}
	for _, chain := range s.chains {
		if chain.Index > 0 {
			spots[chain.Underlying] = chain.Index
		}
		if chain.Stale {
			report.Stale = true
		}
	}
	var legs []leg
	optionUnderlyings := map[string]bool{}
	for accountID, positions := range s.positions {
		if len(options.Accounts) > 0 && !options.Accounts[accountID] {
			continue
		}
		for _, position := range positions {
			instrument, ok := s.instruments[position.Instrument]
			if !ok {
				continue
			}
			size := parse(position.Size)
			if position.Side == "short" {
				size = -size
			}
			risk := PositionRisk{Account: accountID, Instrument: position.Instrument, Product: instrument.Product, Size: size,
				Entry: position.EntryPrice, Mark: position.MarkPrice}
			if instrument.Product == "option" && instrument.Option != nil {
				terms := instrument.Option
				row, known := s.rows[position.Instrument]
				item := leg{underlying: terms.Underlying, option: true, call: terms.Kind == "call", strike: parse(terms.Strike),
					expiry: time.UnixMilli(terms.Expiry), inverse: terms.Inverse}
				risk.Kind, risk.Strike, risk.Expiry, risk.PremiumAsset = terms.Kind, terms.Strike, terms.Expiry, terms.PremiumAsset
				if known {
					item.forward, item.discount, item.iv = row.forward, row.discount, row.row.MarkIV
					risk.MarkIV = row.row.MarkIV
					if row.row.Mark != "" {
						risk.Mark = row.row.Mark
					}
				} else {
					report.Stale = true
				}
				if item.forward == 0 {
					item.forward = spots[terms.Underlying]
				}
				toUSD := 1.0
				if item.inverse {
					toUSD = item.forward
				}
				item.premium, item.entry = parse(risk.Mark)*toUSD, parse(position.EntryPrice)*toUSD
				item.risk = risk
				legs = append(legs, item)
				optionUnderlyings[terms.Underlying] = true
				continue
			}
			if !options.IncludeHedges || (instrument.Product != "perp" && instrument.Product != "spot") {
				continue
			}
			mark := parse(position.MarkPrice)
			if mark == 0 {
				mark = parse(position.EntryPrice)
			}
			legs = append(legs, leg{underlying: normalizeBase(instrument.Base), risk: risk, entry: parse(position.EntryPrice), premium: mark, forward: mark})
		}
	}
	byUnderlying := map[string][]leg{}
	for _, item := range legs {
		if !item.option && !optionUnderlyings[item.underlying] {
			continue // a hedge is shown with the options it hedges
		}
		byUnderlying[item.underlying] = append(byUnderlying[item.underlying], item)
	}
	names := make([]string, 0, len(byUnderlying))
	for name := range byUnderlying {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return rank(names[i]) < rank(names[j]) || (rank(names[i]) == rank(names[j]) && names[i] < names[j])
	})
	for _, name := range names {
		report.Underlyings = append(report.Underlyings, underlyingRisk(name, spots[name], byUnderlying[name], options, at))
	}
	return report
}

func rank(underlying string) int {
	switch underlying {
	case "BTC":
		return 0
	case "ETH":
		return 1
	}
	return 2
}

// normalizeBase maps hedge instruments' bases onto option underlyings
// (Kraken and Hyperliquid name bitcoin differently in places).
func normalizeBase(base string) string {
	switch base {
	case "XBT", "UBTC", "WBTC":
		return "BTC"
	case "UETH", "WETH":
		return "ETH"
	}
	return base
}

// model is the leg's valuation model at a time (forward and discount from
// its expiry; a time later than now keeps the venue's carry).
func (item leg) model(at time.Time) optionmath.Model {
	years := optionmath.Years(at, item.expiry)
	discount := item.discount
	if discount > 0 && discount < 1 {
		// The carry shortens with time: rescale the discount to the horizon.
		now := optionmath.Years(time.Now(), item.expiry)
		if now > 0 {
			discount = math.Pow(discount, years/now)
		}
	}
	return optionmath.Model{Forward: item.forward, Years: years, Discount: discount}
}

// valueOf is one leg's value in USD at a shifted spot, vol and time.
// Inverse options hold their value in the coin: the USD value of the option
// is the Black-76 price, and the coin premium collected or paid moves with
// spot — captured by valuing cash at the shifted spot.
func valueOf(item leg, spotShift, volShift float64, at time.Time) float64 {
	size := item.risk.Size
	if !item.option {
		return size * item.forward * (1 + spotShift)
	}
	vol := math.Max(item.iv+volShift/100, 0.01)
	return size * item.model(at).Shifted(spotShift).Value(item.call, item.strike, vol)
}

// premiumDelta is the delta of the coin an inverse option position holds as
// its premium: its USD value divided by spot is coin. Short options hold the
// premium received (+), long options the premium paid away (-).
func premiumDelta(item leg) float64 {
	if !item.option || !item.inverse || item.forward == 0 {
		return 0
	}
	return -item.risk.Size * item.premium / item.forward
}

func legGreeks(item leg, spot float64, at time.Time) Greeks {
	size := item.risk.Size
	if !item.option {
		mark := item.forward
		pnl := size * (mark - item.entry)
		// A perp is a future: what it is worth is its unrealized PnL. Spot
		// is an asset worth its notional.
		value := pnl
		if item.risk.Product == "spot" {
			value = size * mark
		}
		return Greeks{Delta: size, DeltaUSD: size * spot, Value: value, PnL: pnl}
	}
	unit := item.model(at).Greeks(item.call, item.strike, item.iv)
	delta := size*unit.Delta + premiumDelta(item)
	// Gamma per 1% of spot: the $ delta change for a 1% move.
	gamma1 := size * unit.Gamma * item.forward * item.forward * 0.01
	value := size * item.premium
	return Greeks{Delta: delta, DeltaUSD: delta * spot, Gamma1Pct: gamma1, Vega: size * unit.Vega, Theta: size * unit.Theta,
		Value: value, PnL: size * (item.premium - item.entry)}
}

func underlyingRisk(name string, spot float64, legs []leg, options RiskOptions, at time.Time) UnderlyingRisk {
	result := UnderlyingRisk{Underlying: name, Spot: spot}
	if result.Spot == 0 && len(legs) > 0 {
		result.Spot = legs[0].forward
	}
	expiries := map[int64]*ExpiryRisk{}
	for _, item := range legs {
		greeks := legGreeks(item, result.Spot, at)
		item.risk.Greeks = roundGreeks(greeks)
		result.Positions = append(result.Positions, item.risk)
		result.Greeks.add(greeks)
		if !item.option {
			result.HedgeDelta += greeks.Delta
			continue
		}
		key := item.expiry.UnixMilli()
		bucket := expiries[key]
		if bucket == nil {
			bucket = &ExpiryRisk{Expiry: key, DTE: round(item.expiry.Sub(at).Hours()/24, 2)}
			expiries[key] = bucket
		}
		bucket.Greeks.add(greeks)
	}
	for _, bucket := range expiries {
		bucket.Greeks = roundGreeks(bucket.Greeks)
		result.Expiries = append(result.Expiries, *bucket)
	}
	sort.Slice(result.Expiries, func(i, j int) bool { return result.Expiries[i].Expiry < result.Expiries[j].Expiry })
	sort.Slice(result.Positions, func(i, j int) bool {
		a, b := result.Positions[i], result.Positions[j]
		if a.Product != b.Product {
			return a.Product == "option"
		}
		if a.Expiry != b.Expiry {
			return a.Expiry < b.Expiry
		}
		if a.Strike != b.Strike {
			return parse(a.Strike) < parse(b.Strike)
		}
		return a.Kind < b.Kind
	})
	result.Greeks = roundGreeks(result.Greeks)
	result.HedgeDelta = round(result.HedgeDelta, 6)
	result.Scenario = scenario(legs, options.HorizonDays, at)
	return result
}

// scenario revalues every leg on the grid. Inverse options' coin premium is
// marked at the shifted spot (premiumDelta × shifted spot).
func scenario(legs []leg, horizonDays float64, at time.Time) Scenario {
	grid := Scenario{SpotMoves: defaultSpotMoves, VolMoves: defaultVolMoves, Days: horizonDays}
	later := at.Add(time.Duration(horizonDays * 24 * float64(time.Hour)))
	base := 0.0
	for _, item := range legs {
		base += valueOf(item, 0, 0, at) + premiumDelta(item)*item.forward
	}
	grid.PnL = make([][]float64, len(grid.SpotMoves))
	for i, spotMove := range grid.SpotMoves {
		grid.PnL[i] = make([]float64, len(grid.VolMoves))
		for j, volMove := range grid.VolMoves {
			total := 0.0
			for _, item := range legs {
				total += valueOf(item, spotMove, volMove, later) + premiumDelta(item)*item.forward*(1+spotMove)
			}
			grid.PnL[i][j] = round(total-base, 2)
		}
	}
	return grid
}

func round(value float64, places int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}

func roundGreeks(g Greeks) Greeks {
	return Greeks{Delta: round(g.Delta, 6), DeltaUSD: round(g.DeltaUSD, 2), Gamma1Pct: round(g.Gamma1Pct, 2), Vega: round(g.Vega, 2),
		Theta: round(g.Theta, 2), Value: round(g.Value, 2), PnL: round(g.PnL, 2)}
}
