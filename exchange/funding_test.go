package exchange

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestFundingQuoteAnnualizesOnlyAKnownPeriod(t *testing.T) {
	hourly := FundingQuote{NativeSymbol: "BTC", Rate: decimal.RequireFromString("0.000012"), IntervalHours: OptionalInt{Value: 1, Valid: true}}
	eightHourly := FundingQuote{NativeSymbol: "BTCUSDT", Rate: decimal.RequireFromString("0.0001"), IntervalHours: OptionalInt{Value: 8, Valid: true}}
	// The raw numbers differ eightfold; the yields are within 4% of each other.
	if got := hourly.Annualized(); !got.Valid || !got.Value.Equal(decimal.RequireFromString("0.10512")) {
		t.Fatalf("hourly annualized = %+v", got)
	}
	if got := eightHourly.Annualized(); !got.Valid || !got.Value.Equal(decimal.RequireFromString("0.1095")) {
		t.Fatalf("8h annualized = %+v", got)
	}
	if got := (FundingQuote{NativeSymbol: "X", Rate: decimal.RequireFromString("0.0001")}).Annualized(); got.Valid {
		t.Fatalf("unknown period annualized to %s", got.Value)
	}
}

func TestFundingBoardValidateKeepsWrongNumbersOut(t *testing.T) {
	now := time.Now()
	scope := CatalogScope{Venue: VenueOKX, Environment: EnvironmentLive, Product: ProductPerp, Settlement: "USDT"}
	quote := FundingQuote{NativeSymbol: "BTC-USDT-SWAP", ObservedAt: now, IntervalHours: OptionalInt{Value: 8, Valid: true}}
	if err := (FundingBoard{Scope: scope, ObservedAt: now, Quotes: []FundingQuote{quote}}).Validate(VenueOKX); err != nil {
		t.Fatal(err)
	}
	if err := (FundingBoard{Scope: scope, ObservedAt: now, Quotes: []FundingQuote{quote}}).Validate(VenueGate); err == nil {
		t.Fatal("accepted another venue's board")
	}
	if err := (FundingBoard{Scope: scope, ObservedAt: now, Quotes: []FundingQuote{quote, quote}}).Validate(VenueOKX); err == nil {
		t.Fatal("accepted a repeated instrument")
	}
}
