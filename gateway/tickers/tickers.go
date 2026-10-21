// Package tickers is the gateway's resident market list: every served
// spot and perpetual instrument's last price, 24h open, change and 24h traded
// value, read whole per market every 30 s and joined with the funding
// dataset (rate, APR, open interest) for the market picker.
//
// It is a normalization of venue facts (one bulk public read per market)
// with no business logic, so it fits the gateway: rebuildable, nothing
// accumulates.
package tickers

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/exchange/factory"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/resident"
	"github.com/QuantProcessing/keeluni/gateway/venue"
)

const datasetName = "tickers"

// Cadence is how often each market is read.
const Cadence = 30 * time.Second

// Row is one instrument of the market list.
type Row struct {
	Instrument venue.InstrumentID `json:"inst"`
	Last       string             `json:"last,omitempty"`
	Open24h    string             `json:"open24h,omitempty"`
	// Change24h is a fraction (0.025 is +2.5%).
	Change24h float64 `json:"change24h,omitempty"`
	// Volume24h is the 24h traded value in USD: the quote asset's value
	// converted at its dollar price (stablecoins at 1, others from their
	// own USD-stable pairs); absent when the quote asset has no price.
	Volume24h float64 `json:"volume24h,omitempty"`
	// Funding (perpetuals) from the funding dataset.
	FundingRate string  `json:"fundingRate,omitempty"`
	FundingApr  string  `json:"fundingApr,omitempty"`
	OiUsd       float64 `json:"oiUsd,omitempty"`

	base, quote string
	spot        bool
	last        float64
	quoteVolume float64
}

// FundingSource is the funding dataset as the market list reads it.
type FundingSource interface {
	FundingRow(id venue.InstrumentID) (rate, apr string, oiUsd float64, ok bool)
}

// Source loads one market's tickers.
type Source interface {
	Load(context.Context) ([]exchange.MarketTicker, error)
}

type Dataset struct {
	catalog  *market.Catalog
	registry *resident.Registry
	funding  FundingSource
	sources  map[venue.Market]Source

	mu   sync.RWMutex
	rows map[venue.Market]map[venue.InstrumentID]Row
}

// New builds the dataset for every served spot and perpetual market that has
// a whole-market ticker read.
func New(catalog *market.Catalog, registry *resident.Registry, funding FundingSource, httpClient *http.Client) *Dataset {
	dataset := &Dataset{catalog: catalog, registry: registry, funding: funding, sources: map[venue.Market]Source{}, rows: map[venue.Market]map[venue.InstrumentID]Row{}}
	for _, served := range venue.Markets {
		if served.Product == exchange.ProductOption {
			continue
		}
		source, err := factory.NewPublicMarketTickers(served.Venue, served.Product, served.Settlement, factory.EnvironmentLive, httpClient)
		if err != nil {
			continue
		}
		dataset.sources[served] = source
		registry.Declare(datasetName, served.String(), resident.ModePoll, Cadence)
	}
	return dataset
}

// Run polls every market until ctx ends.
func (dataset *Dataset) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for served, source := range dataset.sources {
		wg.Add(1)
		go func(served venue.Market, source Source) {
			defer wg.Done()
			resident.Run(ctx, dataset.registry, resident.Spec[[]exchange.MarketTicker]{
				Dataset: datasetName, Market: served.String(), Interval: Cadence,
				Load:  source.Load,
				Apply: func(rows []exchange.MarketTicker) { dataset.apply(served, rows) },
			})
		}(served, source)
	}
	wg.Wait()
}

func (dataset *Dataset) apply(served venue.Market, tickers []exchange.MarketTicker) {
	bySymbol := make(map[string]exchange.MarketTicker, len(tickers))
	byID := make(map[string]exchange.MarketTicker, len(tickers))
	for _, ticker := range tickers {
		if ticker.NativeSymbol != "" {
			bySymbol[ticker.NativeSymbol] = ticker
		}
		if ticker.NativeID != "" {
			byID[ticker.NativeID] = ticker
		}
	}
	rows := map[venue.InstrumentID]Row{}
	for _, instrument := range dataset.catalog.MarketInstruments(served) {
		identity := instrument.Native().Identity
		ticker, ok := bySymbol[identity.NativeSymbol]
		if !ok && identity.NativeID != "" {
			ticker, ok = byID[identity.NativeID]
		}
		if !ok {
			continue
		}
		row := project(instrument.ID, ticker)
		row.base, row.quote = strings.ToUpper(identity.BaseAsset), strings.ToUpper(identity.QuoteAsset)
		row.spot = served.Product == exchange.ProductSpot
		rows[instrument.ID] = row
	}
	dataset.mu.Lock()
	dataset.rows[served] = rows
	dataset.mu.Unlock()
}

func project(id venue.InstrumentID, ticker exchange.MarketTicker) Row {
	row := Row{Instrument: id}
	if ticker.Last.Valid && ticker.Last.Value.IsPositive() {
		row.Last = ticker.Last.Value.String()
	}
	if ticker.Open24h.Valid && ticker.Open24h.Value.IsPositive() {
		row.Open24h = ticker.Open24h.Value.Round(12).String()
		if ticker.Last.Valid && ticker.Last.Value.IsPositive() {
			change, _ := ticker.Last.Value.Sub(ticker.Open24h.Value).Div(ticker.Open24h.Value).Round(6).Float64()
			row.Change24h = change
		}
	}
	if ticker.Last.Valid {
		row.last, _ = ticker.Last.Value.Float64()
	}
	if ticker.QuoteVolume24h.Valid && ticker.QuoteVolume24h.Value.IsPositive() {
		row.quoteVolume, _ = ticker.QuoteVolume24h.Value.Float64()
	}
	return row
}

// stablecoins are the quote assets valued at one dollar.
var stablecoins = []string{"USD", "USDT", "USDC", "USD1", "FDUSD", "RLUSD", "USDE", "DAI", "PYUSD", "USDG", "TUSD", "USDH", "USDT0", "USDQ", "BFUSD"}

// dollarPrices values every quote asset in USD from the rows themselves: the
// largest-volume spot pair of the asset against a stablecoin, either way
// round (BTC-USDT gives BTC; USDT-IDR gives IDR). Perpetuals are left out:
// FX perps quote the inverse (Hyperliquid's JPY-USDC trades at 158).
func dollarPrices(rows []Row) map[string]float64 {
	stable := map[string]bool{}
	prices := map[string]float64{}
	for _, coin := range stablecoins {
		stable[coin], prices[coin] = true, 1
	}
	depth := map[string]float64{}
	for _, row := range rows {
		if !row.spot || row.last <= 0 || row.quoteVolume <= 0 {
			continue
		}
		switch {
		case stable[row.quote] && !stable[row.base]:
			if row.quoteVolume > depth[row.base] {
				prices[row.base], depth[row.base] = row.last, row.quoteVolume
			}
		case stable[row.base] && !stable[row.quote]:
			// Volume is in the quote asset here: compare in dollars.
			if value := row.quoteVolume / row.last; value > depth[row.quote] {
				prices[row.quote], depth[row.quote] = 1/row.last, value
			}
		}
	}
	return prices
}

// List is every row, funding joined, sorted by 24h value (largest first).
// Instruments the venue's read did not cover are listed without prices.
func (dataset *Dataset) List() []Row {
	dataset.mu.RLock()
	out := make([]Row, 0, 8192)
	seen := map[venue.InstrumentID]bool{}
	for _, rows := range dataset.rows {
		for id, row := range rows {
			out = append(out, row)
			seen[id] = true
		}
	}
	dataset.mu.RUnlock()
	dollars := dollarPrices(out)
	for index := range out {
		if price, ok := dollars[out[index].quote]; ok && out[index].quoteVolume > 0 {
			out[index].Volume24h = math.Round(out[index].quoteVolume * price)
		}
	}
	for _, instrument := range dataset.catalog.List("", "") {
		if !seen[instrument.ID] {
			out = append(out, Row{Instrument: instrument.ID})
		}
	}
	if dataset.funding != nil {
		for index := range out {
			if rate, apr, oi, ok := dataset.funding.FundingRow(out[index].Instrument); ok {
				out[index].FundingRate, out[index].FundingApr, out[index].OiUsd = rate, apr, oi
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Volume24h != out[j].Volume24h {
			return out[i].Volume24h > out[j].Volume24h
		}
		return out[i].Instrument < out[j].Instrument
	})
	return out
}
