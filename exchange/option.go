package exchange

import (
	"context"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// OptionKind is the right an option grants.
type OptionKind string

const (
	OptionCall OptionKind = "call"
	OptionPut  OptionKind = "put"
)

// OptionTerms are the contract terms of one listed option. Every venue in
// scope lists European, cash-settled options; the style fields exist so a
// venue that differs has to say so rather than be assumed identical.
//
// Units, the same on every venue:
//   - Quantity (orders, positions) is in units of the underlying: 1 means an
//     option on one BTC, whatever the venue calls a contract.
//   - Strike is in the underlying's quote currency (USD for BTC-USD).
//   - Premium (order prices, marks) is in PremiumAsset per one unit of the
//     underlying. Inverse options (Deribit BTC/ETH) quote premium in the
//     underlying itself: 0.0124 BTC.
type OptionTerms struct {
	Underlying string          `json:"underlying"`
	Kind       OptionKind      `json:"kind"`
	Strike     decimal.Decimal `json:"strike"`
	Expiry     time.Time       `json:"expiry"`
	// PremiumAsset is the asset prices are quoted in; SettleAsset the asset
	// profit and loss is paid in. Inverse options have both equal to the
	// underlying; linear options a stablecoin.
	PremiumAsset string `json:"premium_asset"`
	// Inverse marks options whose premium and settlement are the underlying.
	Inverse bool `json:"inverse,omitempty"`
	// Index is the venue's settlement index name (btc_usd, sol_usdc).
	Index string `json:"index,omitempty"`
	// European and CashSettled are the exercise and settlement styles.
	European    bool `json:"european"`
	CashSettled bool `json:"cash_settled"`
}

func (terms OptionTerms) IsZero() bool {
	return terms.Underlying == "" && terms.Kind == "" && terms.Strike.IsZero() && terms.Expiry.IsZero() &&
		terms.PremiumAsset == "" && !terms.Inverse && terms.Index == "" && !terms.European && !terms.CashSettled
}

func (terms OptionTerms) Validate() error {
	if strings.TrimSpace(terms.Underlying) == "" || strings.TrimSpace(terms.PremiumAsset) == "" {
		return invalidRequest("option terms are incomplete")
	}
	if terms.Kind != OptionCall && terms.Kind != OptionPut {
		return invalidRequest("option kind must be call or put")
	}
	if !terms.Strike.IsPositive() {
		return invalidRequest("option strike must be positive")
	}
	if terms.Expiry.IsZero() {
		return invalidRequest("option expiry is required")
	}
	return nil
}

// OptionGreeks are per one unit of the underlying, in the strike currency:
//   - Delta: change of value per 1.0 move of the underlying price, in units
//     of the underlying (Black-76, not premium-adjusted).
//   - Gamma: change of delta per 1.0 move of the underlying price.
//   - Vega: change of value per one volatility point (1%), strike currency.
//   - Theta: change of value per calendar day, strike currency.
type OptionGreeks struct {
	Delta decimal.Decimal `json:"delta"`
	Gamma decimal.Decimal `json:"gamma"`
	Vega  decimal.Decimal `json:"vega"`
	Theta decimal.Decimal `json:"theta"`
}

// OptionQuote is one option's current market as a venue publishes it: the
// row of a chain. Every field but the instrument is optional because venues
// publish different subsets; consumers derive what is missing (greeks from
// MarkIV and UnderlyingPrice) rather than assume it.
type OptionQuote struct {
	Instrument Instrument      `json:"instrument"`
	Bid        OptionalDecimal `json:"bid"`
	Ask        OptionalDecimal `json:"ask"`
	BidSize    OptionalDecimal `json:"bid_size"`
	AskSize    OptionalDecimal `json:"ask_size"`
	Mark       OptionalDecimal `json:"mark"`
	Last       OptionalDecimal `json:"last"`
	// IVs are annualized fractions: 0.42 is 42%.
	MarkIV OptionalDecimal `json:"mark_iv"`
	BidIV  OptionalDecimal `json:"bid_iv"`
	AskIV  OptionalDecimal `json:"ask_iv"`
	// UnderlyingPrice is the price the venue values the option against —
	// the expiry's forward (future or synthetic) where the venue has one.
	// Venues that value on spot leave it empty and publish Rate instead.
	UnderlyingPrice OptionalDecimal `json:"underlying_price"`
	IndexPrice      OptionalDecimal `json:"index_price"`
	// Rate is the annual continuously compounded rate the venue carries the
	// index forward with (Binance: riskFreeInterest). Without an
	// UnderlyingPrice the forward is IndexPrice × e^(Rate × T); zero for
	// venues that publish a forward.
	Rate OptionalDecimal `json:"rate"`
	// Greeks are the venue's own, when it publishes them.
	Greeks       *OptionGreeks   `json:"greeks,omitempty"`
	OpenInterest OptionalDecimal `json:"open_interest"`
	Volume24h    OptionalDecimal `json:"volume_24h"`
	Time         time.Time       `json:"time,omitempty"`
}

// OptionChainRequest selects the options of one underlying on one market.
// An empty Underlying selects every underlying the client serves.
type OptionChainRequest struct {
	Underlying string `json:"underlying,omitempty"`
}

// OptionMarketREST reads a whole chain in one venue call.
type OptionMarketREST interface {
	OptionChain(context.Context, OptionChainRequest) ([]OptionQuote, error)
}

// OptionAccount is an options account's margin state in one settle asset.
type OptionAccount struct {
	Balances          []Balance       `json:"balances,omitempty"`
	Equity            OptionalDecimal `json:"equity"`
	Available         OptionalDecimal `json:"available"`
	InitialMargin     OptionalDecimal `json:"initial_margin"`
	MaintenanceMargin OptionalDecimal `json:"maintenance_margin"`
	UnrealizedPnL     OptionalDecimal `json:"unrealized_pnl"`
	// SettleAsset is the asset the figures above are in.
	SettleAsset string `json:"settle_asset"`
}

// OptionAccountREST is the account surface of an options client.
type OptionAccountREST interface {
	Balances(context.Context) ([]Balance, error)
	OptionAccount(context.Context) (OptionAccount, error)
	Positions(context.Context, PositionsRequest) ([]Position, error)
}

// OptionWebSocket is the socket surface of an options account client.
type OptionWebSocket interface {
	SpotWebSocket
	WatchPositions(context.Context, WatchRequest) (Subscription[PositionEvent], error)
	WatchOptionQuote(context.Context, WatchRequest) (Subscription[OptionQuote], error)
}

// OptionQuoteWebSocket is the public facet that streams one option's quote.
type OptionQuoteWebSocket interface {
	// WatchOptionQuote streams one option's quote.
	WatchOptionQuote(context.Context, WatchRequest) (Subscription[OptionQuote], error)
}

// OptionClient is the compile-time surface for option products.
type OptionClient interface {
	MarketREST
	OrderREST
	OptionAccountREST
	OptionMarketREST
	WebSocket() OptionWebSocket
	Close() error
}
