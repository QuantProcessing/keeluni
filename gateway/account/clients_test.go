package account

import (
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// Binance USDT and USDC perps are one USD-M account: one client trades both,
// so balances and the private stream are not duplicated. Kraken and Deribit
// accounts build their single perp client.
func TestBuildClientsSharesOneAccountAcrossSettlements(t *testing.T) {
	build := func(name string) []routeClient {
		t.Helper()
		clients, err := buildClients("acc_"+name, &credential.Decoded{Venue: name, Credentials: &credential.APIKeySecret{APIKey: "key", APISecret: "c2VjcmV0"}}, &http.Client{}, nil)
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
	for _, name := range []string{"kraken", "deribit"} {
		clients := build(name)
		if len(clients) != 1 || clients[0].perp == nil || clients[0].key.Venue != exchange.Venue(name) {
			t.Fatalf("%s clients = %+v", name, clients)
		}
	}
}
