package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// routeClient is one authenticated client of an account and the markets it
// trades. A native account has one client per market; a broker account has
// one client for all the markets it reaches.
type routeClient struct {
	// key identifies the client within the account: the market for native
	// clients, the broker venue for a broker client.
	key     venue.Market
	markets []venue.Market
	spot    exchange.SpotClient
	perp    exchange.PerpClient
	broker  bool
}

func (client routeClient) orders() exchange.OrderREST {
	if client.perp != nil {
		return client.perp
	}
	return client.spot
}

func (client routeClient) socket() any {
	if client.perp != nil {
		return client.perp.WebSocket()
	}
	return client.spot.WebSocket()
}

func (client routeClient) close() {
	if client.perp != nil {
		_ = client.perp.Close()
	}
	if client.spot != nil {
		_ = client.spot.Close()
	}
}

func (client routeClient) trades(market venue.Market) bool {
	for _, reached := range client.markets {
		if reached == market {
			return true
		}
	}
	return false
}

// balanceMarket labels the client's balances: the market for a native client,
// the broker's single pool for a broker client.
func (client routeClient) balanceMarket() string {
	if client.broker {
		return string(client.key.Venue)
	}
	return client.key.String()
}

// buildClients constructs the account's clients. Construction is local; no
// network I/O happens here.
func buildClients(accountID string, decoded *credential.Decoded, httpClient *http.Client, catalog *market.Catalog) ([]routeClient, error) {
	// Each account gets its own endpoint budget key so one busy account cannot
	// starve another's writes; the venue-wide egress budget stays shared.
	hash := sha256.Sum256([]byte(accountID))
	scoped := ratelimit.WithPrincipal(httpClient, hex.EncodeToString(hash[:8]))
	options := []factory.Option{factory.WithEnvironment(factory.EnvironmentLive), factory.WithHTTPClient(scoped)}

	if broker, ok := venue.BrokerOf(exchange.Venue(decoded.Venue)); ok {
		credentials, ok := decoded.Credentials.(*credential.APIKeySecret)
		if !ok {
			return nil, fmt.Errorf("%s credentials have the wrong shape", decoded.Venue)
		}
		perp, err := factory.New(factory.CrossExPerpConfig(credentials.APIKey, credentials.APISecret, brokerNatives(catalog, broker), options...))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", broker.Venue, err)
		}
		return []routeClient{{key: venue.Market{Venue: broker.Venue, Product: exchange.ProductPerp}, markets: broker.Markets, perp: perp, broker: true}}, nil
	}

	var clients []routeClient
	for _, market := range venue.MarketsOf(exchange.Venue(decoded.Venue)) {
		if shared, ok := sharedAccountMarkets[market]; ok && shared != market {
			// Traded by the client of the market that owns the account.
			continue
		}
		client := routeClient{key: market, markets: append([]venue.Market{market}, sharedWith(market)...)}
		var err error
		switch credentials := decoded.Credentials.(type) {
		case *credential.APIKeySecret:
			key, secret := credentials.APIKey, credentials.APISecret
			switch market.String() {
			case "binance:spot":
				client.spot, err = factory.New(factory.BinanceSpotConfig(key, secret, options...))
			case "binance:perp:USDT":
				client.perp, err = factory.New(factory.BinanceUSDPerpConfig(key, secret, options...))
			case "kraken:perp:USD":
				client.perp, err = factory.New(factory.KrakenPerpConfig(key, secret, options...))
			case "deribit:perp:USDC":
				client.perp, err = factory.New(factory.DeribitPerpConfig(key, secret, options...))
			case "bybit:spot":
				client.spot, err = factory.New(factory.BybitSpotConfig(key, secret, options...))
			case "bybit:perp:USDT":
				client.perp, err = factory.New(factory.BybitUSDTPerpConfig(key, secret, options...))
			case "bybit:perp:USDC":
				client.perp, err = factory.New(factory.BybitUSDCPerpConfig(key, secret, options...))
			case "gate:spot":
				client.spot, err = factory.New(factory.GateSpotConfig(key, secret, options...))
			case "gate:perp:USDT":
				client.perp, err = factory.New(factory.GateUSDTPerpConfig(key, secret, options...))
			}
		case *credential.APIKeySecretPassphrase:
			key, secret, passphrase := credentials.APIKey, credentials.APISecret, credentials.Passphrase
			switch market.String() {
			case "okx:spot":
				client.spot, err = factory.New(factory.OKXSpotConfig(key, secret, passphrase, options...))
			case "okx:perp:USDT":
				client.perp, err = factory.New(factory.OKXUSDTPerpConfig(key, secret, passphrase, options...))
			case "bitget:spot":
				client.spot, err = factory.New(factory.BitgetSpotConfig(key, secret, passphrase, options...))
			case "bitget:perp:USDT":
				client.perp, err = factory.New(factory.BitgetUSDTPerpConfig(key, secret, passphrase, options...))
			case "bitget:perp:USDC":
				client.perp, err = factory.New(factory.BitgetUSDCPerpConfig(key, secret, passphrase, options...))
			}
		case *credential.LighterSigner:
			switch market.String() {
			case "lighter:spot":
				client.spot, err = factory.New(factory.LighterSpotConfig(credentials.PrivateKey, credentials.AccountIndex, credentials.KeyIndex, options...))
			case "lighter:perp:USDC":
				client.perp, err = factory.New(factory.LighterPerpConfig(credentials.PrivateKey, credentials.AccountIndex, credentials.KeyIndex, options...))
			}
		case *credential.EVMPrivateKey:
			hlOptions := options
			if credentials.AccountAddress != "" {
				hlOptions = append(append([]factory.Option(nil), options...), factory.WithAccountAddress(credentials.AccountAddress))
			}
			switch market.String() {
			case "hyperliquid:spot":
				client.spot, err = factory.New(factory.HyperliquidSpotConfig(credentials.PrivateKey, hlOptions...))
			case "hyperliquid:perp:USDC":
				client.perp, err = factory.New(factory.HyperliquidPerpConfig(credentials.PrivateKey, hlOptions...))
			}
		}
		if err != nil {
			for _, built := range clients {
				built.close()
			}
			return nil, fmt.Errorf("%s: %w", market, err)
		}
		if client.spot == nil && client.perp == nil {
			continue
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		return nil, fmt.Errorf("venue %s is not supported", decoded.Venue)
	}
	return clients, nil
}

// sharedAccountMarkets maps a market to the market whose client trades it,
// for markets that are one venue account: Binance USDT and USDC perps share
// the USD-M account, its balances and its private stream.
var sharedAccountMarkets = map[venue.Market]venue.Market{
	{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDC"}: {Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT"},
}

func sharedWith(owner venue.Market) []venue.Market {
	var out []venue.Market
	for market, shared := range sharedAccountMarkets {
		if shared == owner && market != owner {
			out = append(out, market)
		}
	}
	return out
}

// brokerNatives supplies the broker client with the native instruments of the
// markets it reaches, from the gateway's own catalog.
func brokerNatives(catalog *market.Catalog, broker venue.Broker) factory.NativeInstrumentSource {
	return func(context.Context) ([]exchange.Instrument, error) {
		var natives []exchange.Instrument
		for _, reached := range broker.Markets {
			for _, instrument := range catalog.MarketInstruments(reached) {
				natives = append(natives, instrument.Native())
			}
		}
		return natives, nil
	}
}
