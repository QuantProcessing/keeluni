package exchange

import (
	"context"

	"github.com/shopspring/decimal"
)

// A brokered client trades other venues' order books from one account.
// Gate CrossEx is the only one today.
//
// The instruments it accepts and reports keep the identity of the venue whose
// order book they trade: an order for VenueBinance BTC-USDT placed through
// CrossEx is on Binance's book. Only the native symbol and trading rules
// differ, because the broker defines its own symbol and its own rules.
//
// The broker venue itself never appears in an instrument identity. It owns
// what belongs to the account: errors, credentials, rate limits and the
// private event stream (AccountRoute.Venue).

// MarketRoute names one venue market by venue, product and settlement asset.
type MarketRoute struct {
	Venue      Venue   `json:"venue"`
	Product    Product `json:"product"`
	Settlement string  `json:"settlement,omitempty"`
}

// BrokeredClient is implemented by clients whose instruments belong to other
// venues. Callers discover it by type assertion.
type BrokeredClient interface {
	// Broker is the venue that owns the account.
	Broker() Venue
	// Reaches lists the venue markets whose instruments the client trades.
	Reaches() []MarketRoute
}

func knownBroker(venue Venue) bool {
	return venue == VenueCrossEx
}

// IsBroker reports whether venue is a broker venue rather than an order-book
// venue. A broker venue never identifies an instrument.
func IsBroker(venue Venue) bool { return knownBroker(venue) }

// MarginPool is a shared margin account's state: one pool backing positions
// on several venues. Rates are margin balance over requirement; at or below
// 1 the broker restricts or liquidates the account.
type MarginPool struct {
	Equity                OptionalDecimal `json:"equity"`
	Available             OptionalDecimal `json:"available"`
	InitialMargin         OptionalDecimal `json:"initial_margin"`
	MaintenanceMargin     OptionalDecimal `json:"maintenance_margin"`
	InitialMarginRate     OptionalDecimal `json:"initial_margin_rate"`
	MaintenanceMarginRate OptionalDecimal `json:"maintenance_margin_rate"`
	UnrealizedPnL         OptionalDecimal `json:"unrealized_pnl"`
	// Venues is the per-venue share of the pool.
	Venues []VenueMarginShare `json:"venues,omitempty"`
}

// VenueMarginShare is one venue's part of a margin pool in one asset.
type VenueMarginShare struct {
	Venue         Venue           `json:"venue"`
	Asset         string          `json:"asset"`
	Equity        decimal.Decimal `json:"equity"`
	Available     decimal.Decimal `json:"available"`
	InitialMargin decimal.Decimal `json:"initial_margin"`
	UnrealizedPnL decimal.Decimal `json:"unrealized_pnl"`
}

// MarginPoolREST is an optional facet of brokered clients, discovered by type
// assertion.
type MarginPoolREST interface {
	MarginPool(context.Context) (MarginPool, error)
}
