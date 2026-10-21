package account

import (
	"net/http"
	"testing"

	"github.com/QuantProcessing/keeluni/credential"
	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/venue"
)

// Binance USDT and USDC perps are one USD-M account: one client trades both,
// so balances and the private stream are not duplicated. A Deribit account
// trades perps, spot and options; a Kraken account trades what its keys
// unlock: Futures keys perps, Spot keys spot.
func TestBuildClientsSharesOneAccountAcrossSettlements(t *testing.T) {
	build := func(name string, keys ...string) []routeClient {
		t.Helper()
		credentials := &credential.APIKeySecret{APIKey: "key", APISecret: "c2VjcmV0"}
		if len(keys) == 4 {
			credentials = &credential.APIKeySecret{APIKey: keys[0], APISecret: keys[1], SpotAPIKey: keys[2], SpotAPISecret: keys[3]}
		}
		clients, err := buildClients("acc_"+name, &credential.Decoded{Venue: name, Credentials: credentials}, &http.Client{}, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Cleanup(func() {
			for _, client := range clients {
				client.close()
			}
		})
		return clients
	}
	binance := build("binance")
	if len(binance) != 2 {
		t.Fatalf("binance clients = %d, want perp and spot", len(binance))
	}
	usdt := venue.Market{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT"}
	usdc := venue.Market{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDC"}
	if binance[0].key != usdt || !binance[0].trades(usdc) || !binance[0].trades(usdt) {
		t.Fatalf("binance perp client = %+v", binance[0].markets)
	}
	deribit := build("deribit")
	if len(deribit) != 3 || deribit[0].perp == nil || deribit[1].spot == nil || deribit[2].option == nil || !deribit[2].holdsPositions() || deribit[1].holdsPositions() {
		t.Fatalf("deribit clients = %+v", deribit)
	}
	if futures := build("kraken"); len(futures) != 1 || futures[0].perp == nil {
		t.Fatalf("kraken futures-only clients = %+v", futures)
	}
	if spot := build("kraken", "", "", "key", "c2VjcmV0"); len(spot) != 1 || spot[0].spot == nil || spot[0].key.Product != exchange.ProductSpot {
		t.Fatalf("kraken spot-only clients = %+v", spot)
	}
	if both := build("kraken", "key", "c2VjcmV0", "key2", "c2VjcmV0"); len(both) != 2 {
		t.Fatalf("kraken clients = %+v", both)
	}
}
