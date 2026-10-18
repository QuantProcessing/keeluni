package exchange

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
