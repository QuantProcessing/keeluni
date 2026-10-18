package exchange

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

/*
FundingQuote is where every venue's funding quote is made to mean the same
thing. The venues disagree on almost every axis, and each disagreement is a
silent multiplication error in a cross-venue comparison:

  - Unit. Most quote a fraction per settlement; Lighter's live stream quotes a
    percentage per hour and its REST board an eight-hour equivalent.
  - Period. Settlements are hourly (Hyperliquid, Lighter) or every 2/4/8 hours
    (OKX, Bitget, Binance, Gate, Bybit — per instrument, and Binance shortens an
    instrument's period when its rate hits the cap), so the same raw number can
    mean an eightfold different yield.
  - Clock. "Next funding time" is the next settlement on most venues, but OKX's
    field of that name is the one after; its fundingTime is the next one.

A quote fixes all three. Rate is the fraction of position notional paid at the
settlement at NextFundingTime (positive: longs pay shorts), IntervalHours is
that instrument's own period, and Annualized scales Rate to a year, which is
the only number that compares across venues. A period that cannot be
established stays absent, and so does Annualized: a row that assumes eight
hours is worse than one that admits it does not know.
*/

// FundingQuote is one perpetual's current funding, normalized. It is keyed by
// the venue's native identity; callers map it onto their own instrument ids.
type FundingQuote struct {
	NativeSymbol string `json:"nativeSymbol"`
	NativeID     string `json:"nativeId,omitempty"`
	// Rate is the fraction of position notional paid at NextFundingTime.
	// Positive: longs pay shorts.
	Rate decimal.Decimal `json:"rate"`
	// IntervalHours is this instrument's settlement period.
	IntervalHours OptionalInt `json:"intervalHours"`
	// NextFundingTime is the settlement Rate applies to; zero when the venue
	// publishes no clock and none can be derived.
	NextFundingTime time.Time `json:"nextFundingTime,omitzero"`
	// RateCap and RateFloor bound Rate where the venue publishes them.
	RateCap   OptionalDecimal `json:"rateCap"`
	RateFloor OptionalDecimal `json:"rateFloor"`
	// ObservedAt is when the venue produced (or we received) the quote.
	ObservedAt time.Time `json:"observedAt"`
}

// fundingHoursPerYear is the simple-interest year funding is annualized over.
const fundingHoursPerYear = 24 * 365

// Annualized scales Rate to a simple-interest year: Rate × 8760 / IntervalHours.
// It is absent when the period is unknown.
func (quote FundingQuote) Annualized() OptionalDecimal {
	if !quote.IntervalHours.Valid || quote.IntervalHours.Value <= 0 {
		return OptionalDecimal{}
	}
	periods := decimal.NewFromInt(fundingHoursPerYear).Div(decimal.NewFromInt(quote.IntervalHours.Value))
	return OptionalDecimal{Value: quote.Rate.Mul(periods), Valid: true}
}

// FundingBoard is the funding of every perpetual a venue lists, at one moment.
// It is complete: an instrument absent from it has no current quote.
type FundingBoard struct {
	Scope      CatalogScope   `json:"scope"`
	ObservedAt time.Time      `json:"observedAt"`
	Quotes     []FundingQuote `json:"quotes"`
}

// Validate rejects a board that would put a wrong number on screen rather than
// a missing one: another venue's rows, unnamed or repeated instruments, or
// impossible periods.
func (board FundingBoard) Validate(venue Venue) error {
	if board.Scope.Venue != venue || board.Scope.Product != ProductPerp {
		return fundingBoardInvalid("funding board does not belong to the requested venue")
	}
	if board.ObservedAt.IsZero() {
		return fundingBoardInvalid("funding board has no observation time")
	}
	seen := make(map[string]struct{}, len(board.Quotes))
	for _, quote := range board.Quotes {
		if strings.TrimSpace(quote.NativeSymbol) == "" {
			return fundingBoardInvalid("funding quote has no native symbol")
		}
		if _, duplicate := seen[quote.NativeSymbol]; duplicate {
			return fundingBoardInvalid(fmt.Sprintf("funding board repeats %s", quote.NativeSymbol))
		}
		seen[quote.NativeSymbol] = struct{}{}
		if quote.IntervalHours.Valid && (quote.IntervalHours.Value <= 0 || quote.IntervalHours.Value > 24) {
			return fundingBoardInvalid(fmt.Sprintf("funding period of %s is outside 1..24h", quote.NativeSymbol))
		}
		if quote.ObservedAt.IsZero() {
			return fundingBoardInvalid(fmt.Sprintf("funding quote %s has no observation time", quote.NativeSymbol))
		}
	}
	return nil
}

func fundingBoardInvalid(message string) error {
	return NewError(KindMalformedResponse, ErrorDetails{Operation: "FundingBoard", SafeMessage: message})
}
