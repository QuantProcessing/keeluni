package factory

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/exchange/internal/factoryclient"
)

// PublicMarketTickers reads every instrument's 24h ticker of one venue's
// spot or perpetual markets in one (or a few) credential-free reads: the
// market list's price, change and volume columns. A perpetual read covers
// every settlement the venue serves, so one reader serves all of them.
// Construction performs no I/O.
type PublicMarketTickers struct {
	load factoryclient.MarketTickerLoader
}

// NewPublicMarketTickers builds the reader from any served market of the
// venue and product (the settlement only selects the endpoint profile);
// venues without a whole-market ticker read are unsupported.
func NewPublicMarketTickers(venue exchange.Venue, product exchange.Product, settlement string, environment Environment, httpClient *http.Client) (*PublicMarketTickers, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public market tickers profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	load := factoryclient.NewMarketTickerLoader(venue, product, factorySettingsForConfig(configName, settings))
	if load == nil {
		return nil, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: venue, Product: product, Operation: "MarketTickers", SafeMessage: "this market has no whole-market ticker read"})
	}
	return &PublicMarketTickers{load: load}, nil
}

// Load reads the market's tickers.
func (tickers *PublicMarketTickers) Load(ctx context.Context) ([]exchange.MarketTicker, error) {
	if tickers == nil || tickers.load == nil {
		return nil, invalidConfig("public market tickers are unavailable")
	}
	return tickers.load(ctx)
}
