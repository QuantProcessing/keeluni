package optioner

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/QuantProcessing/keeluni/optionmath"
	"github.com/shopspring/decimal"
)

var (
	at     = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	expiry = time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	fwd    = 82000.0
)

func optionInstrument(kind string, strike int, inverse bool) gwclient.Instrument {
	premium, quote, prefix := "USDC", "USDC", "deribit:option:BTC-USDC-20261127-"
	if inverse {
		premium, quote, prefix = "BTC", "USD", "deribit:option:BTC-USD-20261127-"
	}
	letter := "C"
	if kind == "put" {
		letter = "P"
	}
	id := prefix + strconv.Itoa(strike) + "-" + letter
	instrument := gwclient.Instrument{ID: id, Venue: "deribit", Product: "option", Symbol: id[len("deribit:option:"):], Base: "BTC", Quote: quote,
		Settle: premium, TickSize: "0.0001", StepSize: "0.1", MinQty: "0.1", CoarseTickFrom: "0.005", CoarseTick: "0.0005",
		Option: &gwclient.OptionTerms{Underlying: "BTC", Kind: kind, Strike: strconv.Itoa(strike), Expiry: expiry.UnixMilli(), PremiumAsset: premium, Inverse: inverse, European: true}}
	if !inverse {
		instrument.TickSize, instrument.CoarseTickFrom, instrument.CoarseTick, instrument.StepSize, instrument.MinQty = "5", "1000", "20", "0.01", "0.01"
	}
	return instrument
}

// fixture: one BTC inverse chain at a flat 45% vol, a perp, two accounts.
func fixture(t *testing.T) *state {
	t.Helper()
	s := newState()
	s.connected = true
	years := optionmath.Years(at, expiry)
	chain := gwclient.OptionChain{Series: "deribit:BTC:BTC", Venue: "deribit", Underlying: "BTC", PremiumAsset: "BTC", StrikeAsset: "USD", Inverse: true, Index: 81500,
		Expiries: []gwclient.OptionExpiry{{Expiry: expiry.UnixMilli(), Forward: fwd, ATMIV: 0.45}}}
	var instruments []gwclient.Instrument
	for _, strike := range []int{70000, 80000, 90000} {
		for _, kind := range []string{"call", "put"} {
			instrument := optionInstrument(kind, strike, true)
			instruments = append(instruments, instrument)
			usd := optionmath.Price(kind == "call", fwd, float64(strike), years, 0.45)
			mark := usd / fwd
			chain.Rows = append(chain.Rows, gwclient.OptionRow{Instrument: instrument.ID, Kind: kind, Strike: strconv.Itoa(strike), Expiry: expiry.UnixMilli(),
				Bid: strconv.FormatFloat(mark*0.98, 'f', 4, 64), Ask: strconv.FormatFloat(mark*1.02, 'f', 4, 64), Mark: strconv.FormatFloat(mark, 'f', 8, 64), MarkIV: 0.45})
		}
	}
	instruments = append(instruments, gwclient.Instrument{ID: "deribit:perp:BTC-USDC", Venue: "deribit", Product: "perp", Symbol: "BTC-USDC", Base: "BTC", Quote: "USDC", Settle: "USDC", TickSize: "1", StepSize: "0.0001", MinQty: "0.0001"},
		gwclient.Instrument{ID: "binance:perp:ETH-USDT", Venue: "binance", Product: "perp", Symbol: "ETH-USDT", Base: "ETH", Quote: "USDT", TickSize: "0.01", StepSize: "0.001", MinQty: "0.001"})
	s.setInstruments(instruments)
	s.setChain(chain)
	s.setAccounts([]gwclient.Account{
		{ID: "acc_d", Venue: "deribit", Kind: "native", Label: "deribit", Markets: []string{"deribit:perp:USDC", "deribit:spot", "deribit:option"}, Active: true},
		{ID: "acc_b", Venue: "binance", Kind: "native", Label: "binance", Markets: []string{"binance:perp:USDT"}, Active: true},
	})
	return s
}

func near(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %v, want %v ± %v", name, got, want, tolerance)
	}
}

// A short inverse straddle: option deltas net to roughly zero at the money,
// but the premium received is BTC — the book is long that coin. Risk counts
// it, so a "flat" inverse book is flat in dollars only after hedging it.
func TestInverseRiskCountsThePremiumCoin(t *testing.T) {
	s := fixture(t)
	s.setPositions("acc_d", []gwclient.Position{
		{Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "short", Size: "2", EntryPrice: "0.06"},
		{Instrument: "deribit:option:BTC-USD-20261127-80000-P", Side: "short", Size: "2", EntryPrice: "0.04"},
	})
	report := s.risk(RiskOptions{IncludeHedges: true}, at)
	if len(report.Underlyings) != 1 || report.Stale {
		t.Fatalf("report = %+v", report)
	}
	btc := report.Underlyings[0]
	years := optionmath.Years(at, expiry)
	call := optionmath.GreeksOf(true, fwd, 80000, years, 0.45)
	put := optionmath.GreeksOf(false, fwd, 80000, years, 0.45)
	optionDelta := -2*call.Delta - 2*put.Delta
	premiumCoin := 2 * (optionmath.Price(true, fwd, 80000, years, 0.45) + optionmath.Price(false, fwd, 80000, years, 0.45)) / fwd
	near(t, "delta", btc.Greeks.Delta, optionDelta+premiumCoin, 1e-4)
	if btc.Greeks.Vega >= 0 || btc.Greeks.Theta <= 0 || btc.Greeks.Gamma1Pct >= 0 {
		t.Fatalf("a short straddle is short vega and gamma, long theta: %+v", btc.Greeks)
	}
	near(t, "vega", btc.Greeks.Vega, -2*(call.Vega+put.Vega), 0.02)
	if len(btc.Expiries) != 1 || len(btc.Positions) != 2 || btc.Positions[0].Kind != "call" {
		t.Fatalf("buckets = %+v positions = %+v", btc.Expiries, btc.Positions)
	}
	// The zero cell of the grid is zero, and a short straddle loses on both
	// big moves and on a vol rise.
	grid := btc.Scenario
	zeroSpot, zeroVol := index(grid.SpotMoves, 0), index(grid.VolMoves, 0)
	if grid.PnL[zeroSpot][zeroVol] != 0 {
		t.Fatalf("grid center = %v", grid.PnL[zeroSpot][zeroVol])
	}
	if grid.PnL[0][zeroVol] >= 0 || grid.PnL[len(grid.SpotMoves)-1][zeroVol] >= 0 || grid.PnL[zeroSpot][len(grid.VolMoves)-1] >= 0 {
		t.Fatalf("short straddle grid = %v", grid.PnL)
	}
}

func index(values []float64, target float64) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

// A perp hedge on the same underlying is netted into delta and reported; a
// perp on another underlying is not shown with these options.
func TestPerpHedgesNetIntoDelta(t *testing.T) {
	s := fixture(t)
	s.setPositions("acc_d", []gwclient.Position{
		{Instrument: "deribit:option:BTC-USD-20261127-90000-C", Side: "long", Size: "10", EntryPrice: "0.02"},
		{Instrument: "deribit:perp:BTC-USDC", Side: "short", Size: "1.5", EntryPrice: "81000", MarkPrice: "82000"},
	})
	s.setPositions("acc_b", []gwclient.Position{{Instrument: "binance:perp:ETH-USDT", Side: "long", Size: "3", EntryPrice: "2400", MarkPrice: "2380"}})
	with := s.risk(RiskOptions{IncludeHedges: true}, at).Underlyings
	without := s.risk(RiskOptions{IncludeHedges: false}, at).Underlyings
	if len(with) != 1 || with[0].HedgeDelta != -1.5 || len(with[0].Positions) != 2 {
		t.Fatalf("with hedges = %+v", with)
	}
	near(t, "netted delta", with[0].Greeks.Delta, without[0].Greeks.Delta-1.5, 1e-6)
	// The perp adds its PnL (-1.5 × (82000 - 81000)) to value, not its notional.
	near(t, "perp value", with[0].Greeks.Value-without[0].Greeks.Value, -1500, 0.01)
	only := s.risk(RiskOptions{IncludeHedges: true, Accounts: map[string]bool{"acc_b": true}}, at)
	if len(only.Underlyings) != 0 {
		t.Fatalf("an ETH perp alone is not an options book: %+v", only.Underlyings)
	}
}

func TestStaleWhenAPositionHasNoMarket(t *testing.T) {
	s := fixture(t)
	s.setInstruments([]gwclient.Instrument{optionInstrument("call", 100000, true)})
	s.setPositions("acc_d", []gwclient.Position{{Instrument: "deribit:option:BTC-USD-20261127-100000-C", Side: "long", Size: "1", EntryPrice: "0.01"}})
	if !s.risk(RiskOptions{}, at).Stale {
		t.Fatal("a position missing from the chain must mark the report stale")
	}
}

// fakeGateway records placements and answers with scripted outcomes.
type fakeGateway struct {
	mu       sync.Mutex
	placed   []gwclient.PlaceRequest
	outcome  func(gwclient.PlaceRequest) (gwclient.PlaceResult, error)
	lookup   gwclient.LookupResult
	canceled []string
}

func (g *fakeGateway) Instruments(context.Context) ([]gwclient.Instrument, error) { return nil, nil }
func (g *fakeGateway) InstrumentsOf(context.Context, string) ([]gwclient.Instrument, error) {
	return nil, nil
}
func (g *fakeGateway) Accounts(context.Context) ([]gwclient.Account, error)          { return nil, nil }
func (g *fakeGateway) OptionSeries(context.Context) ([]gwclient.OptionSeries, error) { return nil, nil }
func (g *fakeGateway) OptionChain(context.Context, string) (gwclient.OptionChain, error) {
	return gwclient.OptionChain{}, nil
}
func (g *fakeGateway) Positions(context.Context, string) ([]gwclient.Position, error) {
	return nil, nil
}
func (g *fakeGateway) Place(_ context.Context, _ string, request gwclient.PlaceRequest) (gwclient.PlaceResult, error) {
	g.mu.Lock()
	g.placed = append(g.placed, request)
	g.mu.Unlock()
	return g.outcome(request)
}
func (g *fakeGateway) Lookup(context.Context, string, string, string) (gwclient.LookupResult, error) {
	return g.lookup, nil
}
func (g *fakeGateway) Cancel(_ context.Context, _, _, orderID string) error {
	g.mu.Lock()
	g.canceled = append(g.canceled, orderID)
	g.mu.Unlock()
	return nil
}

func TestPricingSnapsToTheTickAndCoarseTick(t *testing.T) {
	s := fixture(t)
	executions := newExecutions(&fakeGateway{}, s)
	legs, err := executions.Prepare(ExecRequest{Legs: []LegRequest{
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "buy", Quantity: "0.5", PriceBy: "mid"},
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "sell", Quantity: "0.5", PriceBy: "mid"},
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-90000-C", Side: "buy", Quantity: "0.1", Price: "0.00423"},
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "buy", Quantity: "0.1", PriceBy: "iv", IV: 0.45, TimeInForce: "ioc"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	buy, sell := parse(legs[0].Price), parse(legs[1].Price)
	onCoarse := func(text string) bool {
		return decimal.RequireFromString(text).Mod(decimal.RequireFromString("0.0005")).IsZero()
	}
	if buy >= sell || !onCoarse(legs[0].Price) || !onCoarse(legs[1].Price) {
		t.Fatalf("mid above the coarse boundary: buy %v (down) sell %v (up) on a 0.0005 tick", buy, sell)
	}
	if legs[2].Price != "0.0042" {
		t.Fatalf("fine tick buy rounds down: %s", legs[2].Price)
	}
	if legs[3].PricedAt != "iv:0.4500" || legs[3].Price == "" {
		t.Fatalf("iv priced leg = %+v", legs[3])
	}
	for _, bad := range []LegRequest{
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "buy", Quantity: "0.15", PriceBy: "mid"},
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "buy", Quantity: "1", Type: "market"},
		{Account: "acc_b", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "buy", Quantity: "1", PriceBy: "mid"},
	} {
		if _, err := executions.Prepare(ExecRequest{Legs: []LegRequest{bad}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: err = %v", bad, err)
		}
	}
}

func TestExecutionReportsEachLegAndNeverResends(t *testing.T) {
	s := fixture(t)
	gateway := &fakeGateway{lookup: gwclient.LookupResult{Status: gwclient.LookupFound, Order: &gwclient.Order{OrderID: "o3", Status: "filled", Filled: "0.5"}}}
	gateway.outcome = func(request gwclient.PlaceRequest) (gwclient.PlaceResult, error) {
		switch request.Instrument {
		case "deribit:option:BTC-USD-20261127-80000-C":
			return gwclient.PlaceResult{Status: gwclient.OutcomeAccepted, Order: &gwclient.Order{OrderID: "o1", Status: "open", Filled: "0"}}, nil
		case "deribit:option:BTC-USD-20261127-80000-P":
			return gwclient.PlaceResult{Status: gwclient.OutcomeRejected, VenueMessage: "not_enough_funds"}, nil
		}
		return gwclient.PlaceResult{Status: gwclient.OutcomeUnknown}, nil
	}
	executions := newExecutions(gateway, s)
	execution, err := executions.Submit(context.Background(), ExecRequest{Label: "straddle + hedge", Legs: []LegRequest{
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-C", Side: "sell", Quantity: "0.5", PriceBy: "mark"},
		{Account: "acc_d", Instrument: "deribit:option:BTC-USD-20261127-80000-P", Side: "sell", Quantity: "0.5", PriceBy: "mark"},
		{Account: "acc_d", Instrument: "deribit:perp:BTC-USDC", Side: "buy", Quantity: "0.5", Type: "market"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.placed) != 3 {
		t.Fatalf("placed %d (an unknown outcome is looked up, never resent)", len(gateway.placed))
	}
	ids := map[string]bool{}
	for _, request := range gateway.placed {
		ids[request.ClientOrderID] = true
	}
	if len(ids) != 3 {
		t.Fatal("legs share a client order id")
	}
	if execution.Legs[0].State != "open" || execution.Legs[1].State != "rejected" || execution.Legs[1].Message != "not_enough_funds" ||
		execution.Legs[2].State != "filled" || execution.Status != "working" {
		t.Fatalf("execution = %+v", execution)
	}
	executions.OnOrder(gwclient.Order{ClientOrderID: execution.Legs[0].ClientOrderID, OrderID: "o1", Status: "filled", Filled: "0.5", AvgPrice: "0.06"})
	execution, _ = executions.Get(execution.ID)
	if execution.Status != "partial" || execution.Problem == "" {
		t.Fatalf("a package with a rejected leg is partial: %+v", execution)
	}
}

func TestSuggestHedgeFlattensDeltaOnAPerp(t *testing.T) {
	s := fixture(t)
	s.setPositions("acc_d", []gwclient.Position{{Instrument: "deribit:option:BTC-USD-20261127-90000-C", Side: "long", Size: "10", EntryPrice: "0.02"}})
	service := newService(&fakeGateway{}, nil)
	service.state = s
	suggestion, err := service.SuggestHedge("BTC", "acc_d", RiskOptions{IncludeHedges: true})
	if err != nil {
		t.Fatal(err)
	}
	if suggestion.Leg.Instrument != "deribit:perp:BTC-USDC" || suggestion.Leg.Side != "sell" || math.Abs(suggestion.Residual) > 0.0001 {
		t.Fatalf("suggestion = %+v", suggestion)
	}
	if _, err := service.SuggestHedge("BTC", "acc_b", RiskOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an account with no BTC perp: %v", err)
	}
}
