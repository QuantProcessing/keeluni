package exchange

import (
	"reflect"
	"testing"
	"time"
)

// A broker venue owns accounts, never instruments.
func TestBrokerVenueOwnsAccountsNotInstruments(t *testing.T) {
	if VenueCrossEx != "crossex" || !IsBroker(VenueCrossEx) || IsBroker(VenueBinance) {
		t.Fatal("crossex must be the broker venue and only it")
	}
	instrument := testInstrument(ProductPerp)
	instrument.Identity.Venue = VenueCrossEx
	if err := instrument.Validate(); err == nil {
		t.Fatal("an instrument identified by a broker venue was accepted")
	}

	route := AccountRoute{AccountID: "acc", RouteID: "crossex:perp", Venue: VenueCrossEx, Environment: EnvironmentLive, Product: ProductPerp, NativeFamily: "brokered"}
	if err := route.Validate(); err != nil {
		t.Fatalf("broker account route rejected: %v", err)
	}
	event := AccountEvent{Kind: AccountEventBalance, Route: route, ObservedAt: time.Now().UTC(), Balance: &BalanceEvent{Kind: EventSnapshot}}
	if err := event.Validate(); err != nil {
		t.Fatalf("broker account event rejected: %v", err)
	}
	route.Venue = Venue("unknown")
	if route.Validate() == nil {
		t.Fatal("unknown account venue accepted")
	}
}

func TestBrokeredClientInterfaceShape(t *testing.T) {
	brokered := reflect.TypeOf((*BrokeredClient)(nil)).Elem()
	want := map[string]bool{"Broker": true, "Reaches": true}
	if brokered.NumMethod() != len(want) {
		t.Fatalf("BrokeredClient has %d methods", brokered.NumMethod())
	}
	for i := 0; i < brokered.NumMethod(); i++ {
		if !want[brokered.Method(i).Name] {
			t.Fatalf("unexpected BrokeredClient method %s", brokered.Method(i).Name)
		}
	}
	route := MarketRoute{Venue: VenueBinance, Product: ProductPerp, Settlement: "USDT"}
	if route.Venue != VenueBinance || route.Product != ProductPerp || route.Settlement != "USDT" {
		t.Fatalf("market route = %+v", route)
	}
	// Brokered clients are discovered by assertion, not through PerpClient.
	if reflect.TypeOf((*PerpClient)(nil)).Elem().Implements(brokered) {
		t.Fatal("PerpClient must not require brokerage")
	}
}
