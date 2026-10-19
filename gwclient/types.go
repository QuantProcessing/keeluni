// Package gwclient is the Go client of the gateway's /v1 API for apps that
// trade through the gateway (hedging, arbitrage). It mirrors the wire types in
// docs/gateway-api.md; all numbers are decimal strings, as on the wire.
package gwclient

// Instrument is one tradable instrument and its rules.
type Instrument struct {
	ID          string `json:"id"`
	Venue       string `json:"venue"`
	Product     string `json:"product"` // spot | perp
	Symbol      string `json:"symbol"`
	Base        string `json:"base"`
	Quote       string `json:"quote"`
	Settle      string `json:"settle,omitempty"`
	TickSize    string `json:"tickSize"`
	StepSize    string `json:"stepSize"`
	MinQty      string `json:"minQty"`
	MinNotional string `json:"minNotional,omitempty"`
}

// Account is a connected venue account. It never carries credentials.
type Account struct {
	ID      string   `json:"id"`
	Venue   string   `json:"venue"`
	Label   string   `json:"label"`
	Markets []string `json:"markets"`
	Active  bool     `json:"active"`
}

// Book is a top-N order book snapshot: [price, quantity] levels.
type Book struct {
	Bids [][2]string `json:"bids"`
	Asks [][2]string `json:"asks"`
	Time int64       `json:"ts"`
}

// PlaceRequest is a new order. ClientOrderID must come from NewClientOrderID
// and be recorded before the request is sent.
type PlaceRequest struct {
	Instrument    string `json:"inst"`
	Side          string `json:"side"` // buy | sell
	Type          string `json:"type"` // market | limit
	Quantity      string `json:"qty"`
	Price         string `json:"price,omitempty"`
	TimeInForce   string `json:"tif,omitempty"` // gtc | ioc | post_only
	ReduceOnly    bool   `json:"reduceOnly,omitempty"`
	ClientOrderID string `json:"clientOrderId"`
}

// Place outcomes.
const (
	OutcomeAccepted = "accepted"
	OutcomeRejected = "rejected"
	OutcomeUnknown  = "unknown"
)

// PlaceResult is the venue's answer to a placement.
type PlaceResult struct {
	Status       string `json:"status"`
	Order        *Order `json:"order,omitempty"`
	Category     string `json:"category,omitempty"`
	VenueCode    string `json:"venueCode,omitempty"`
	VenueMessage string `json:"venueMessage,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Order is a venue order's latest state. Status is one of
// open | partially_filled | filled | canceled | rejected | pending.
type Order struct {
	Instrument    string `json:"inst"`
	OrderID       string `json:"orderId"`
	ClientOrderID string `json:"clientOrderId,omitempty"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Price         string `json:"price,omitempty"`
	Quantity      string `json:"qty"`
	Filled        string `json:"filled"`
	AvgPrice      string `json:"avgPrice,omitempty"`
	TimeInForce   string `json:"tif,omitempty"`
	ReduceOnly    bool   `json:"reduceOnly,omitempty"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"createdAt,omitempty"`
	UpdatedAt     int64  `json:"updatedAt,omitempty"`
}

// Terminal reports whether the order can no longer fill.
func (order Order) Terminal() bool {
	switch order.Status {
	case "filled", "canceled", "rejected":
		return true
	}
	return false
}

// LookupResult answers "does this order exist?".
type LookupResult struct {
	Status string `json:"status"` // found | not_found | unconfirmed
	Order  *Order `json:"order,omitempty"`
}

// Fill is one execution. Fee is a cost in FeeAsset (negative: rebate).
type Fill struct {
	Instrument    string `json:"inst"`
	FillID        string `json:"fillId"`
	OrderID       string `json:"orderId"`
	ClientOrderID string `json:"clientOrderId,omitempty"`
	Side          string `json:"side"`
	Price         string `json:"price"`
	Quantity      string `json:"qty"`
	Fee           string `json:"fee"`
	FeeAsset      string `json:"feeAsset,omitempty"`
	Liquidity     string `json:"liquidity,omitempty"`
	Time          int64  `json:"ts"`
}

// Position is one open position; Size is positive, Side says which way.
type Position struct {
	Instrument       string `json:"inst"`
	Side             string `json:"side"`
	Size             string `json:"size"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice,omitempty"`
	UnrealizedPnL    string `json:"unrealizedPnl"`
	LiquidationPrice string `json:"liquidationPrice,omitempty"`
	Leverage         string `json:"leverage,omitempty"`
	Margin           string `json:"margin,omitempty"`
}

// Balance is one asset balance of one market of an account.
type Balance struct {
	Market    string `json:"market"`
	Asset     string `json:"asset"`
	Total     string `json:"total"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
}

// FundingQuote is one perpetual's current funding (see gateway/funding).
type FundingQuote struct {
	Instrument    string `json:"inst"`
	Venue         string `json:"venue"`
	Base          string `json:"base"`
	Rate          string `json:"rate"`
	IntervalHours int64  `json:"intervalHours,omitempty"`
	Apr           string `json:"apr,omitempty"`
	NextFunding   int64  `json:"nextFunding,omitempty"`
	ObservedAt    int64  `json:"observedAt"`
	Stale         bool   `json:"stale"`
}

// FundingPoint is one settled funding event.
type FundingPoint struct {
	Time          int64  `json:"t"`
	Rate          string `json:"rate"`
	IntervalHours int64  `json:"intervalHours,omitempty"`
	Apr           string `json:"apr,omitempty"`
}
