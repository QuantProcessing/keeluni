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
	AssetClass  string `json:"assetClass,omitempty"`
}

// Account kinds.
const (
	AccountNative = "native" // one venue
	AccountBroker = "broker" // one margin pool trading several venues (Gate CrossEx)
)

// Account is a connected venue account. It never carries credentials. Markets
// lists the markets it trades ("binance:perp:USDT"); for a broker these span
// venues. Whether it trades a given instrument, and with which rules, is
// answered by its routes (Client.Routes).
type Account struct {
	ID      string   `json:"id"`
	Venue   string   `json:"venue"`
	Kind    string   `json:"kind"`
	Label   string   `json:"label"`
	Markets []string `json:"markets"`
	Active  bool     `json:"active"`
}

// Broker reports a broker account.
func (account Account) Broker() bool { return account.Kind == AccountBroker }

// Route is one instrument an account trades and the rules its orders follow
// there. A broker's rules can differ from the venue's (finer steps, its own
// minimum value).
type Route struct {
	Instrument  string `json:"inst"`
	TickSize    string `json:"tickSize"`
	StepSize    string `json:"stepSize"`
	MinQty      string `json:"minQty"`
	MinNotional string `json:"minNotional,omitempty"`
}

// Margin is a broker account's shared margin pool. Rates are ratios (2.4 is
// 240%): below 1.1 initial, margin cannot be moved out; below 1.0 initial,
// open orders are cancelled and only closing is allowed; at 1.0 maintenance,
// the account is liquidated.
type Margin struct {
	Equity                string        `json:"equity,omitempty"`
	Available             string        `json:"available,omitempty"`
	InitialMargin         string        `json:"initialMargin,omitempty"`
	MaintenanceMargin     string        `json:"maintenanceMargin,omitempty"`
	InitialMarginRate     string        `json:"initialMarginRate,omitempty"`
	MaintenanceMarginRate string        `json:"maintenanceMarginRate,omitempty"`
	UnrealizedPnL         string        `json:"unrealizedPnl,omitempty"`
	ByVenue               []VenueMargin `json:"byVenue,omitempty"`
	Time                  int64         `json:"ts"`
}

// VenueMargin is one reached venue's share of a broker pool.
type VenueMargin struct {
	Venue         string `json:"venue"`
	Asset         string `json:"asset"`
	Equity        string `json:"equity"`
	Available     string `json:"available"`
	InitialMargin string `json:"initialMargin,omitempty"`
	UnrealizedPnL string `json:"unrealizedPnl,omitempty"`
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
// "pending" is an order a broker accepted but has not yet placed on the
// venue: it can still be rejected, and that rejection arrives later as an
// order update with RejectCode/RejectMessage.
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
	RejectCode    string `json:"rejectCode,omitempty"`
	RejectMessage string `json:"rejectMessage,omitempty"`
	// Origin is set for orders the venue's risk engine placed:
	// liquidation | adl | settlement.
	Origin string `json:"origin,omitempty"`
}

// Terminal reports whether the order can no longer fill.
func (order Order) Terminal() bool {
	switch order.Status {
	case "filled", "canceled", "rejected":
		return true
	}
	return false
}

// Lookup outcomes. Unconfirmed means the venue cannot prove absence (some
// venues forget closed orders after seconds or minutes): the order may exist.
const (
	LookupFound       = "found"
	LookupNotFound    = "not_found"
	LookupUnconfirmed = "unconfirmed"
)

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

// FundingQuote is one perpetual's current funding (see gateway/funding): Rate
// is the fraction of notional paid at NextFunding (positive: longs pay),
// IntervalHours the instrument's own period, Apr Rate annualized.
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
