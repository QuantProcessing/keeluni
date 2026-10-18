package exchange

import (
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
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

func TestBrokeredOnlyVenuesIdentifyInstruments(t *testing.T) {
	for venue, want := range map[Venue]string{VenueKraken: "kraken", VenueDeribit: "deribit"} {
		if string(venue) != want || IsBroker(venue) {
			t.Fatalf("venue %q", venue)
		}
		instrument := testInstrument(ProductPerp)
		instrument.Identity.Venue = venue
		if err := instrument.Validate(); err != nil {
			t.Fatalf("%s instrument rejected: %v", venue, err)
		}
	}
}

func TestMarginPoolAndOrderOriginModels(t *testing.T) {
	pool := MarginPool{
		Available:             OptionalDecimal{Value: decimal.NewFromInt(1200), Valid: true},
		MaintenanceMarginRate: OptionalDecimal{Value: decimal.RequireFromString("4.8"), Valid: true},
		Venues:                []VenueMarginShare{{Venue: VenueBinance, Asset: "USDT", Equity: decimal.NewFromInt(875)}},
	}
	if !pool.Available.Valid || pool.InitialMarginRate.Valid || pool.Venues[0].Venue != VenueBinance {
		t.Fatalf("pool = %+v", pool)
	}
	reader := reflect.TypeOf((*MarginPoolREST)(nil)).Elem()
	if reader.NumMethod() != 1 || reader.Method(0).Name != "MarginPool" {
		t.Fatal("MarginPoolREST shape changed")
	}
	if reflect.TypeOf((*PerpClient)(nil)).Elem().Implements(reader) {
		t.Fatal("PerpClient must not require a margin pool")
	}
	order := Order{Status: "rejected", RejectCode: "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR", RejectMessage: "Insufficient availableMargin", Origin: OrderOriginLiquidation}
	if order.RejectCode == "" || order.Origin != "liquidation" || OrderOriginADL != "adl" || OrderOriginSettlement != "settlement" {
		t.Fatalf("order = %+v", order)
	}
}
