// Package optioner is the options app: portfolio risk across every options
// account (greeks by underlying and expiry, perp hedges included, a full
// revaluation spot × vol scenario grid) and multi-leg execution.
//
// It keeps no database. Positions are the venue's facts, read from the
// gateway's account streams; chains come from the gateway's option dataset;
// an execution lives in memory for a day and its orders carry client order
// ids, so after a restart the venue — positions and open orders — is the
// whole truth. Options trade with limit orders only: an option book is too
// thin for market orders; taking liquidity is an IOC limit at the far side.
package optioner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/QuantProcessing/keeluni/optionmath"
)

// Gateway is the part of the gateway client the app uses.
type Gateway interface {
	Instruments(context.Context) ([]gwclient.Instrument, error)
	InstrumentsOf(context.Context, string) ([]gwclient.Instrument, error)
	Accounts(context.Context) ([]gwclient.Account, error)
	OptionSeries(context.Context) ([]gwclient.OptionSeries, error)
	OptionChain(context.Context, string) (gwclient.OptionChain, error)
	Positions(context.Context, string) ([]gwclient.Position, error)
	Place(context.Context, string, gwclient.PlaceRequest) (gwclient.PlaceResult, error)
	Lookup(context.Context, string, string, string) (gwclient.LookupResult, error)
	Cancel(context.Context, string, string, string) error
}

// state is the app's view of the gateway: instruments, accounts, chains and
// every account's positions, kept current by the gateway stream.
type state struct {
	mu          sync.RWMutex
	instruments map[string]gwclient.Instrument
	accounts    []gwclient.Account
	chains      map[string]gwclient.OptionChain
	// rows indexes chain rows by instrument id.
	rows      map[string]chainRow
	positions map[string][]gwclient.Position // by account
	live      map[string]bool                // account stream connected
	connected bool
}

type chainRow struct {
	series   string
	row      gwclient.OptionRow
	forward  float64
	discount float64
	inverse  bool
}

// model is the row's valuation model at a time.
func (row chainRow) model(now time.Time) optionmath.Model {
	return optionmath.Model{Forward: row.forward, Years: optionmath.Years(now, time.UnixMilli(row.row.Expiry)), Discount: row.discount}
}

func newState() *state {
	return &state{
		instruments: map[string]gwclient.Instrument{}, chains: map[string]gwclient.OptionChain{},
		rows: map[string]chainRow{}, positions: map[string][]gwclient.Position{}, live: map[string]bool{},
	}
}

func (s *state) setInstruments(items []gwclient.Instrument) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		s.instruments[item.ID] = item
	}
}

func (s *state) instrument(id string) (gwclient.Instrument, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.instruments[id]
	return item, ok
}

func (s *state) setAccounts(accounts []gwclient.Account) {
	s.mu.Lock()
	s.accounts = accounts
	s.mu.Unlock()
}

func (s *state) account(id string) (gwclient.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.ID == id {
			return account, true
		}
	}
	return gwclient.Account{}, false
}

func (s *state) setChain(chain gwclient.OptionChain) {
	forwards := map[int64]float64{}
	discounts := map[int64]float64{}
	for _, expiry := range chain.Expiries {
		forwards[expiry.Expiry] = expiry.Forward
		discounts[expiry.Expiry] = expiry.Discount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.chains[chain.Series]; ok {
		for _, row := range previous.Rows {
			delete(s.rows, row.Instrument)
		}
	}
	s.chains[chain.Series] = chain
	for _, row := range chain.Rows {
		forward := forwards[row.Expiry]
		if forward == 0 {
			forward = chain.Index
		}
		s.rows[row.Instrument] = chainRow{series: chain.Series, row: row, forward: forward, discount: discounts[row.Expiry], inverse: chain.Inverse}
	}
}

func (s *state) setPositions(account string, positions []gwclient.Position) {
	s.mu.Lock()
	s.positions[account] = positions
	s.mu.Unlock()
}

// apply consumes one gateway stream message.
func (s *state) apply(message gwclient.Message) {
	switch message.Channel {
	case "options":
		var chain gwclient.OptionChain
		if json.Unmarshal(message.Data, &chain) == nil && chain.Series != "" {
			s.setChain(chain)
		}
	case "account.positions":
		var positions []gwclient.Position
		if json.Unmarshal(message.Data, &positions) == nil {
			s.setPositions(message.Account, positions)
		}
	case "account.status":
		var status struct {
			Connected bool `json:"connected"`
		}
		if json.Unmarshal(message.Data, &status) == nil {
			s.mu.Lock()
			s.live[message.Account] = status.Connected
			s.mu.Unlock()
		}
	}
}

// seriesOf names the chain an option instrument belongs to.
func seriesOf(instrument gwclient.Instrument) string {
	if instrument.Option == nil {
		return ""
	}
	return instrument.Venue + ":" + instrument.Option.Underlying + ":" + instrument.Option.PremiumAsset
}

// optionAccount reports an account that trades options.
func optionAccount(account gwclient.Account) bool {
	for _, market := range account.Markets {
		if strings.Contains(market, ":option") {
			return true
		}
	}
	return false
}

// marketOf is the gateway market of an instrument.
func marketOf(instrument gwclient.Instrument) string {
	switch instrument.Product {
	case "spot", "option":
		return instrument.Venue + ":" + instrument.Product
	}
	settle := instrument.Settle
	if settle == "" {
		settle = instrument.Quote
	}
	return instrument.Venue + ":perp:" + strings.ToUpper(settle)
}

func now() int64 { return time.Now().UnixMilli() }
