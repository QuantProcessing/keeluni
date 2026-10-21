package pairexec

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/shopspring/decimal"
)

// fakeVenue simulates the gateway and two venues: market orders fill at the
// top of book; limit orders rest until the test fills them; outcomes can be
// scripted per instrument.
type fakeVenue struct {
	mu     sync.Mutex
	engine *Engine
	orders map[string]*gwclient.Order // by client id
	fills  map[string][]gwclient.Fill // by instrument
	nextID int
	price  map[string]decimal.Decimal
	script map[string][]string // instrument -> outcomes: "fill" | "reject" | "unknown-filled" | "unknown-missing" | "partial:<qty>" | "pending"
	// requests are every placement as sent.
	requests []gwclient.PlaceRequest
	// forgetful makes Lookup answer "unconfirmed" for orders that are no
	// longer open (Kraken, Deribit labels, CrossEx text lookups).
	forgetful bool
	places    map[string]int // client id -> placements (must be 1)
	cancels   int
}

func newFakeVenue() *fakeVenue {
	return &fakeVenue{
		orders: map[string]*gwclient.Order{}, fills: map[string][]gwclient.Fill{}, places: map[string]int{},
		price:  map[string]decimal.Decimal{"binance:perp:BTC-USDT": d("65000"), "okx:perp:BTC-USDT-SWAP": d("65010")},
		script: map[string][]string{},
	}
}

func (venue *fakeVenue) Instruments(context.Context) ([]gwclient.Instrument, error) {
	return []gwclient.Instrument{
		{ID: "binance:perp:BTC-USDT", Venue: "binance", Product: "perp", Base: "BTC", Quote: "USDT", Settle: "USDT", TickSize: "0.1", StepSize: "0.001", MinQty: "0.001", MinNotional: "5"},
		{ID: "okx:perp:BTC-USDT-SWAP", Venue: "okx", Product: "perp", Base: "BTC", Quote: "USDT", Settle: "USDT", TickSize: "0.1", StepSize: "0.0001", MinQty: "0.0001"},
	}, nil
}

func (venue *fakeVenue) Accounts(context.Context) ([]gwclient.Account, error) {
	return []gwclient.Account{
		{ID: "acc_a", Venue: "binance", Kind: gwclient.AccountNative, Label: "a", Active: true, Markets: []string{"binance:perp:USDT"}},
		{ID: "acc_b", Venue: "okx", Kind: gwclient.AccountNative, Label: "b", Active: true, Markets: []string{"okx:perp:USDT"}},
		{ID: "acc_cx", Venue: "crossex", Kind: gwclient.AccountBroker, Label: "cx", Active: true, Markets: []string{"binance:perp:USDT", "okx:perp:USDT"}},
	}, nil
}

// Routes: native accounts trade on the venue's rules; the broker on its own
// (finer step on OKX, a minimum value on Binance).
func (venue *fakeVenue) Routes(_ context.Context, accountID string) ([]gwclient.Route, error) {
	switch accountID {
	case "acc_a":
		return []gwclient.Route{{Instrument: "binance:perp:BTC-USDT", TickSize: "0.1", StepSize: "0.001", MinQty: "0.001", MinNotional: "5"}}, nil
	case "acc_b":
		return []gwclient.Route{{Instrument: "okx:perp:BTC-USDT-SWAP", TickSize: "0.1", StepSize: "0.0001", MinQty: "0.0001"}}, nil
	case "acc_cx":
		return []gwclient.Route{
			{Instrument: "binance:perp:BTC-USDT", TickSize: "0.1", StepSize: "0.001", MinQty: "0.001", MinNotional: "50"},
			{Instrument: "okx:perp:BTC-USDT-SWAP", TickSize: "0.1", StepSize: "0.00001", MinQty: "0.00001"},
		}, nil
	}
	return nil, nil
}

func (venue *fakeVenue) Margin(context.Context, string) (*gwclient.Margin, error) { return nil, nil }

func (venue *fakeVenue) OpenOrders(_ context.Context, _ string, instrument string) ([]gwclient.Order, error) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	var out []gwclient.Order
	for _, order := range venue.orders {
		if order.Instrument == instrument && !order.Terminal() {
			out = append(out, *order)
		}
	}
	return out, nil
}

func (venue *fakeVenue) Positions(context.Context, string) ([]gwclient.Position, error) {
	return nil, nil
}

func (venue *fakeVenue) Book(context.Context, string, int) (gwclient.Book, error) {
	return gwclient.Book{}, nil
}

func (venue *fakeVenue) next(instrument string) string {
	if steps := venue.script[instrument]; len(steps) > 0 {
		venue.script[instrument] = steps[1:]
		return steps[0]
	}
	return "fill"
}

func (venue *fakeVenue) Place(_ context.Context, accountID string, request gwclient.PlaceRequest) (gwclient.PlaceResult, error) {
	venue.mu.Lock()
	venue.places[request.ClientOrderID]++
	venue.requests = append(venue.requests, request)
	outcome := "rest"
	if request.Type == "market" {
		outcome = venue.next(request.Instrument)
	}
	venue.nextID++
	order := &gwclient.Order{Instrument: request.Instrument, OrderID: "v" + strconv.Itoa(venue.nextID), ClientOrderID: request.ClientOrderID,
		Side: request.Side, Type: request.Type, Price: request.Price, Quantity: request.Quantity, Filled: "0", Status: "open"}
	var fill *gwclient.Fill
	fillAll := func(qty string) {
		order.Filled, order.AvgPrice = qty, venue.price[request.Instrument].String()
		if qty == request.Quantity {
			order.Status = "filled"
		} else {
			order.Status = "canceled" // IOC remainder
		}
		fill = &gwclient.Fill{Instrument: request.Instrument, FillID: "f" + order.OrderID, OrderID: order.OrderID, ClientOrderID: request.ClientOrderID,
			Side: request.Side, Price: order.AvgPrice, Quantity: qty, Fee: "0.1", FeeAsset: "USDT", Time: time.Now().UnixMilli()}
	}
	result := gwclient.PlaceResult{Status: gwclient.OutcomeAccepted}
	var err error
	switch {
	case outcome == "fill":
		fillAll(request.Quantity)
	case outcome == "reject":
		venue.mu.Unlock()
		return gwclient.PlaceResult{Status: gwclient.OutcomeRejected, Message: "Margin is insufficient.", VenueCode: "-2019"}, nil
	case outcome == "unknown-filled":
		fillAll(request.Quantity)
		err = errors.New("timeout")
	case outcome == "unknown-missing":
		venue.mu.Unlock()
		return gwclient.PlaceResult{}, errors.New("connection reset")
	case len(outcome) > 8 && outcome[:8] == "partial:":
		fillAll(outcome[8:])
	case outcome == "pending":
		// A broker accepted it; the exchange's answer comes later.
		order.Status = "pending"
	}
	venue.orders[request.ClientOrderID] = order
	if fill != nil {
		venue.fills[request.Instrument] = append(venue.fills[request.Instrument], *fill)
	}
	copied := *order
	venue.mu.Unlock()
	if err != nil {
		return gwclient.PlaceResult{}, err
	}
	result.Order = &copied
	return result, nil
}

func (venue *fakeVenue) Lookup(_ context.Context, _ string, _ string, clientOrderID string) (gwclient.LookupResult, error) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	if order, ok := venue.orders[clientOrderID]; ok {
		if venue.forgetful && order.Terminal() {
			return gwclient.LookupResult{Status: gwclient.LookupUnconfirmed}, nil
		}
		copied := *order
		return gwclient.LookupResult{Status: "found", Order: &copied}, nil
	}
	if venue.forgetful {
		return gwclient.LookupResult{Status: gwclient.LookupUnconfirmed}, nil
	}
	return gwclient.LookupResult{Status: "not_found"}, nil
}

func (venue *fakeVenue) Cancel(_ context.Context, accountID, instrument, orderID string) error {
	venue.mu.Lock()
	var update gwclient.Order
	for _, order := range venue.orders {
		if order.OrderID == orderID {
			order.Status = "canceled"
			update = *order
		}
	}
	venue.cancels++
	venue.mu.Unlock()
	venue.engine.OnOrder(accountID, update)
	return nil
}

func (venue *fakeVenue) Fills(_ context.Context, _ string, instrument string, _ time.Time) ([]gwclient.Fill, error) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	return append([]gwclient.Fill(nil), venue.fills[instrument]...), nil
}

func (venue *fakeVenue) Funding(context.Context, string) (gwclient.FundingQuote, error) {
	return gwclient.FundingQuote{}, errors.New("unused")
}

// fillMaker fills qty of the resting limit order on instrument and pushes the
// update through the engine, as the account stream would.
func (venue *fakeVenue) fillMaker(t *testing.T, instrument, accountID, qty string) {
	t.Helper()
	venue.mu.Lock()
	var update gwclient.Order
	var fill gwclient.Fill
	found := false
	for _, order := range venue.orders {
		if order.Instrument == instrument && order.Type == "limit" && order.Status != "filled" && order.Status != "canceled" {
			filled := d(order.Filled).Add(d(qty))
			order.Filled, order.AvgPrice = filled.String(), order.Price
			order.Status = "partially_filled"
			if filled.Equal(d(order.Quantity)) {
				order.Status = "filled"
			}
			update = *order
			venue.nextID++
			fill = gwclient.Fill{Instrument: instrument, FillID: "m" + strconv.Itoa(venue.nextID), OrderID: order.OrderID, ClientOrderID: order.ClientOrderID,
				Side: order.Side, Price: order.Price, Quantity: qty, Fee: "-0.01", FeeAsset: "USDT", Time: time.Now().UnixMilli()}
			venue.fills[instrument] = append(venue.fills[instrument], fill)
			found = true
		}
	}
	venue.mu.Unlock()
	if !found {
		t.Fatal("no resting maker order")
	}
	venue.engine.OnOrder(accountID, update)
	venue.engine.OnFill(accountID, fill)
}

func (venue *fakeVenue) maxPlacements() int {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	most := 0
	for _, count := range venue.places {
		most = max(most, count)
	}
	return most
}

type testRig struct {
	venue  *fakeVenue
	engine *Engine
	store  *Store
	market *Market
	ctx    context.Context
}

func newRig(t *testing.T) *testRig {
	t.Helper()
	store := testStore(t)
	venue := newFakeVenue()
	market := NewMarket(nil)
	// Seed books so sizing has prices.
	for instrument, price := range venue.price {
		market.books[instrument] = bookState{
			bids: BookSide{{price.Sub(d("0.5")), d("100")}}, asks: BookSide{{price.Add(d("0.5")), d("100")}}, at: time.Now(),
		}
	}
	engine := NewEngine(venue, store, market)
	engine.lookupDelays = []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	engine.pollEvery = 50 * time.Millisecond
	venue.engine = engine
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return &testRig{venue: venue, engine: engine, store: store, market: market, ctx: ctx}
}

func (rig *testRig) waitStatus(t *testing.T, groupID string, want ...string) Group {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		group, err := rig.store.Group(rig.ctx, groupID)
		if err != nil {
			t.Fatal(err)
		}
		_, working := rig.engine.Working(groupID)
		for _, status := range want {
			if group.Status == status && !working {
				return group
			}
		}
		if time.Now().After(deadline) {
			events, _ := rig.store.Events(rig.ctx, groupID, 50)
			for i := len(events) - 1; i >= 0; i-- {
				t.Logf("  %s %s", events[i].Level, events[i].Message)
			}
			t.Fatalf("group status = %s (working=%v), want %v", group.Status, working, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (rig *testRig) books(t *testing.T, groupID string) [2]LegBook {
	t.Helper()
	_, books, err := rig.engine.snapshot(rig.ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	return books
}

func openRequest(mode string) OpenRequest {
	return OpenRequest{
		Legs: [2]LegInput{{AccountID: "acc_a", Instrument: "binance:perp:BTC-USDT", Side: "buy"}, {AccountID: "acc_b", Instrument: "okx:perp:BTC-USDT-SWAP", Side: "sell"}},
		Size: d("0.01"), Unit: UnitBase, Mode: mode, Policy: PolicyUnwind,
	}
}

func TestMarketOpenAndFullClose(t *testing.T) {
	rig := newRig(t)
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.01")) || !books[1].Held.Equal(d("0.01")) || !books[0].Fees.Equal(d("0.1")) {
		t.Fatalf("books after open = %+v", books)
	}
	if _, err := rig.engine.Close(rig.ctx, group.ID, CloseRequest{Mode: ModeMarket}); err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusClosed)
	books = rig.books(t, group.ID)
	if !books[0].Held.IsZero() || !books[1].Held.IsZero() {
		t.Fatalf("books after close = %+v", books)
	}
	if rig.venue.maxPlacements() != 1 {
		t.Fatal("an order was placed twice")
	}
}

func TestMarketOpenWithOneLegRejectedIsUnwound(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"reject"}
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	// Open fails with leg 1 exposed; policy unwind closes it automatically.
	final := rig.waitStatus(t, group.ID, StatusClosed)
	books := rig.books(t, group.ID)
	if !books[0].Held.IsZero() || !books[0].Opened.Equal(d("0.01")) || !books[0].Closed.Equal(d("0.01")) {
		t.Fatalf("leg 1 not unwound: %+v (status %s)", books[0], final.Status)
	}
	actions, _ := rig.store.Actions(rig.ctx, group.ID)
	if len(actions) != 2 || actions[0].Status != ActionFailed || actions[1].Kind != KindUnwind || actions[1].Status != ActionDone {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestBothLegsRejectedFailsCleanly(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"reject"}
	rig.venue.script["binance:perp:BTC-USDT"] = []string{"reject"}
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	failed := rig.waitStatus(t, group.ID, StatusFailed)
	if failed.Problem.IsZero() {
		t.Fatal("failed group has no reason")
	}
}

func TestPartialFillUnderUnwindPolicyTrimsToTheMatchedSize(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"partial:0.0042"}
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	// Leg 2 filled 0.0042 of 0.01; leg 1 (step 0.001) is trimmed to 0.004 and
	// leg 2 to the matching 0.004. The hedge stays open, smaller.
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.004")) || !books[1].Held.Equal(d("0.004")) {
		t.Fatalf("books after trim = %+v", books)
	}
	actions, _ := rig.store.Actions(rig.ctx, group.ID)
	if len(actions) != 2 || actions[1].Kind != KindUnwind || actions[1].Status != ActionDone {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestHoldPolicyLeavesTheGroupBrokenAndRehedgeRepairsIt(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"partial:0.004"}
	request := openRequest(ModeMarket)
	request.Policy = PolicyHold
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	broken := rig.waitStatus(t, group.ID, StatusBroken)
	if broken.Problem.IsZero() {
		t.Fatal("broken group has no problem text")
	}
	if _, err := rig.engine.Rehedge(rig.ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[1].Held.Equal(d("0.01")) {
		t.Fatalf("leg 2 after rehedge = %s", books[1].Held)
	}
}

func TestUnknownOutcomeIsLookedUpNeverReplaced(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"unknown-filled"}
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	if rig.venue.maxPlacements() != 1 {
		t.Fatal("unknown outcome led to a second placement")
	}
	books := rig.books(t, group.ID)
	if !books[1].Held.Equal(d("0.01")) {
		t.Fatalf("filled-but-unknown order not recovered: %+v", books[1])
	}
}

func TestUnknownOutcomeThatNeverReachedTheVenue(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"unknown-missing"}
	now := time.Now()
	var clock sync.Mutex
	rig.engine.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }
	request := openRequest(ModeMarket)
	request.Policy = PolicyHold
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	// "not_found" is only trusted once the order is old enough.
	time.Sleep(300 * time.Millisecond)
	if _, working := rig.engine.Working(group.ID); !working {
		t.Fatal("unknown order resolved as missing too early")
	}
	clock.Lock()
	now = now.Add(notFoundAfter + time.Second)
	clock.Unlock()
	rig.waitStatus(t, group.ID, StatusBroken)
	if rig.venue.maxPlacements() != 1 {
		t.Fatal("missing order was placed again")
	}
}

func TestLimitOpenHedgesEveryMakerFill(t *testing.T) {
	rig := newRig(t)
	request := openRequest(ModeLimit)
	request.Maker, request.Price, request.TIF = 1, d("65100"), "post_only"
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rig.venue.restingCount() == 1 })
	rig.venue.fillMaker(t, "okx:perp:BTC-USDT-SWAP", "acc_b", "0.004")
	waitFor(t, func() bool { return rig.books(t, group.ID)[0].Held.Equal(d("0.004")) })
	rig.venue.fillMaker(t, "okx:perp:BTC-USDT-SWAP", "acc_b", "0.006")
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.01")) || !books[1].Held.Equal(d("0.01")) || !books[1].AvgEntry.Equal(d("65100")) {
		t.Fatalf("books = %+v", books)
	}
	// Maker rebate recorded as a negative fee.
	if !books[1].Fees.Equal(d("-0.02")) {
		t.Fatalf("maker fees = %s", books[1].Fees)
	}
	// Post only is the resting order's alone: the hedges are market orders
	// with no time in force.
	rig.venue.mu.Lock()
	defer rig.venue.mu.Unlock()
	for _, sent := range rig.venue.requests {
		resting := sent.Instrument == "okx:perp:BTC-USDT-SWAP"
		if resting && (sent.Type != "limit" || sent.TimeInForce != "post_only") || !resting && (sent.Type != "market" || sent.TimeInForce != "") {
			t.Fatalf("sent %+v", sent)
		}
	}
}

func TestCancelingALimitOpenKeepsTheFilledPartHedged(t *testing.T) {
	rig := newRig(t)
	request := openRequest(ModeLimit)
	request.Maker, request.Price = 0, d("64900")
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rig.venue.restingCount() == 1 })
	rig.venue.fillMaker(t, "binance:perp:BTC-USDT", "acc_a", "0.003")
	waitFor(t, func() bool { return rig.books(t, group.ID)[1].Held.Equal(d("0.003")) })
	if err := rig.engine.Cancel(group.ID); err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.003")) || !books[1].Held.Equal(d("0.003")) || rig.venue.cancels != 1 {
		t.Fatalf("books = %+v cancels = %d", books, rig.venue.cancels)
	}
	actions, _ := rig.store.Actions(rig.ctx, group.ID)
	if actions[0].Status != ActionCanceled {
		t.Fatalf("action = %+v", actions[0])
	}
}

func TestRejectedHedgeStopsTheMakerAndBreaksTheGroup(t *testing.T) {
	rig := newRig(t)
	rig.venue.script["binance:perp:BTC-USDT"] = []string{"reject"}
	request := openRequest(ModeLimit)
	request.Maker, request.Price, request.Policy = 1, d("65100"), PolicyHold
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rig.venue.restingCount() == 1 })
	rig.venue.fillMaker(t, "okx:perp:BTC-USDT-SWAP", "acc_b", "0.005")
	rig.waitStatus(t, group.ID, StatusBroken)
	if rig.venue.cancels != 1 {
		t.Fatalf("maker not canceled after a rejected hedge (cancels=%d)", rig.venue.cancels)
	}
}

func TestPartialCloseKeepsTheRatio(t *testing.T) {
	rig := newRig(t)
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	if _, err := rig.engine.Close(rig.ctx, group.ID, CloseRequest{Fraction: d("0.5"), Mode: ModeMarket}); err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.005")) || !books[1].Held.Equal(d("0.005")) {
		t.Fatalf("books = %+v", books)
	}
	if _, err := rig.engine.Close(rig.ctx, group.ID, CloseRequest{Fraction: d("2"), Mode: ModeMarket}); err == nil || !IsInvalid(err) {
		t.Fatalf("fraction 2 err = %v", err)
	}
}

func TestRestartResumesAWorkingActionWithoutReplacing(t *testing.T) {
	rig := newRig(t)
	request := openRequest(ModeLimit)
	request.Maker, request.Price = 1, d("65100")
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rig.venue.restingCount() == 1 })
	// The maker fills while the hedger is down.
	rig.venue.mu.Lock()
	for _, order := range rig.venue.orders {
		if order.Type == "limit" {
			order.Filled, order.AvgPrice, order.Status = order.Quantity, order.Price, "filled"
		}
	}
	rig.venue.mu.Unlock()
	// A second engine on the same database takes over (the first is stopped).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := NewEngine(rig.venue, rig.store, rig.market)
	second.lookupDelays, second.pollEvery = rig.engine.lookupDelays, rig.engine.pollEvery
	rig.engine.Stop()
	rig.venue.engine = second
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	rig.engine = second
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.01")) || !books[1].Held.Equal(d("0.01")) || rig.venue.maxPlacements() != 1 {
		t.Fatalf("books after restart = %+v", books)
	}
}

func TestOpenValidation(t *testing.T) {
	rig := newRig(t)
	cases := map[string]func(*OpenRequest){
		"same side":       func(r *OpenRequest) { r.Legs[1].Side = "buy" },
		"wrong account":   func(r *OpenRequest) { r.Legs[0].AccountID = "acc_b" },
		"unknown inst":    func(r *OpenRequest) { r.Legs[0].Instrument = "binance:perp:NOPE-USDT" },
		"below minimum":   func(r *OpenRequest) { r.Size = d("0.0001") },
		"limit no price":  func(r *OpenRequest) { r.Mode = ModeLimit },
		"bad unit":        func(r *OpenRequest) { r.Unit = "lots" },
		"same instrument": func(r *OpenRequest) { r.Legs[1] = r.Legs[0]; r.Legs[1].Side = "sell" },
	}
	for name, mutate := range cases {
		request := openRequest(ModeMarket)
		mutate(&request)
		if _, err := rig.engine.Open(rig.ctx, rig.market, request); err == nil || !IsInvalid(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func (venue *fakeVenue) restingCount() int {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	count := 0
	for _, order := range venue.orders {
		if order.Type == "limit" && (order.Status == "open" || order.Status == "partially_filled") {
			count++
		}
	}
	return count
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fakeFundingHistory map[string][]gwclient.FundingPoint

func (history fakeFundingHistory) FundingHistory(_ context.Context, instrument string, since, until time.Time) ([]gwclient.FundingPoint, error) {
	var points []gwclient.FundingPoint
	for _, point := range history[instrument] {
		at := time.UnixMilli(point.Time)
		if !at.Before(since) && !at.After(until) {
			points = append(points, point)
		}
	}
	return points, nil
}

func TestFundingAccruesFromSettledRatesOnlyWhileHeld(t *testing.T) {
	rig := newRig(t)
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	orders, _ := rig.store.Orders(rig.ctx, group.ID)
	opened := orders[0].CreatedAt
	before := opened.Add(-time.Hour).UnixMilli()
	after := opened.Add(time.Second).UnixMilli()
	// Pretend the settlements are old so the entry price is used (no mark).
	rig.engine.now = func() time.Time { return opened.Add(3 * time.Hour) }
	history := fakeFundingHistory{
		"binance:perp:BTC-USDT":  {{Time: before, Rate: "0.01"}, {Time: after, Rate: "0.0001"}},
		"okx:perp:BTC-USDT-SWAP": {{Time: after, Rate: "0.0002"}},
	}
	rig.engine.accrueFunding(rig.ctx, history)
	rig.engine.accrueFunding(rig.ctx, history) // idempotent
	log, _ := rig.store.FundingLog(rig.ctx, group.ID)
	if len(log) != 2 {
		t.Fatalf("accruals = %+v", log)
	}
	totals, _ := rig.store.FundingTotals(rig.ctx, []string{group.ID})
	// Long 0.01 BTC entered at 65000 pays 0.0001; short 0.01 entered at 65010
	// receives 0.0002. The settlement before the open accrues nothing.
	if !totals[group.ID][0].Equal(d("-0.065")) || !totals[group.ID][1].Equal(d("0.13002")) {
		t.Fatalf("totals = %v", totals[group.ID])
	}
}

func TestALateFillOnAFinishedActionRechecksTheGroup(t *testing.T) {
	rig := newRig(t)
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	orders, _ := rig.store.Orders(rig.ctx, group.ID)
	// A venue reports more filled on leg 1 after the action finished.
	late := gwclient.Order{ClientOrderID: orders[0].ClientOrderID, OrderID: orders[0].VenueOrderID, Filled: "0.02", AvgPrice: "65000.5", Status: "filled"}
	rig.engine.OnOrder(orders[0].AccountID, late)
	broken := rig.waitStatus(t, group.ID, StatusBroken)
	if broken.Problem.IsZero() {
		t.Fatal("no problem text")
	}
}

// ---------------------------------------------------------------------------
// The account model and order lifecycle of the current gateway
// ---------------------------------------------------------------------------

// Both legs on one Gate CrossEx account: legal, sized with the broker's own
// rules (OKX at 0.00001 rather than the venue's 0.0001), flagged broker.
func TestBrokerAccountTradesBothLegsWithItsOwnRules(t *testing.T) {
	rig := newRig(t)
	request := openRequest(ModeMarket)
	request.Legs[0].AccountID, request.Legs[1].AccountID = "acc_cx", "acc_cx"
	request.Size = d("0.01234")
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	group = rig.waitStatus(t, group.ID, StatusOpen)
	if !group.Legs[0].Broker || !group.Legs[1].StepSize.Equal(d("0.00001")) || !group.Legs[0].MinNotional.Equal(d("50")) {
		t.Fatalf("legs = %+v", group.Legs)
	}
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.012")) || !books[1].Held.Equal(d("0.012")) {
		t.Fatalf("books = %+v", books)
	}
}

// A native account of another venue, or a broker that does not list the
// instrument, cannot carry the leg.
func TestLegAccountMustTradeTheInstrument(t *testing.T) {
	rig := newRig(t)
	request := openRequest(ModeMarket)
	request.Legs[1].AccountID = "acc_a" // Binance account on an OKX leg
	if _, err := rig.engine.Open(rig.ctx, rig.market, request); err == nil || !IsInvalid(err) {
		t.Fatalf("foreign venue account: %v", err)
	}
}

// The ratio is the backend's: PEPE against 1000PEPE is 0.001, BTC against
// ETH is matched by notional, 1INCH is a name and not a multiple.
func TestMatchLegsFromInstruments(t *testing.T) {
	leg := func(venue, base string) Leg { return Leg{Venue: venue, Base: base} }
	cases := []struct {
		a, b  Leg
		match string
		ratio string
	}{
		{leg("binance", "PEPE"), leg("binance", "1000PEPE"), MatchBase, "0.001"},
		{leg("hyperliquid", "kPEPE"), leg("bybit", "1000PEPE"), MatchBase, "1"},
		{leg("bitget", "1MCHEEMS"), leg("binance", "1000CHEEMS"), MatchBase, "1000"},
		{leg("okx", "BTC"), leg("hyperliquid", "BTC"), MatchBase, "1"},
		{leg("binance", "1INCH"), leg("okx", "1INCH"), MatchBase, "1"},
		{leg("binance", "kAITO"), leg("okx", "AITO"), MatchNotional, "0"}, // k is a multiple on Hyperliquid only
		{leg("okx", "BTC"), leg("okx", "ETH"), MatchNotional, "0"},
	}
	for _, c := range cases {
		match, ratio := MatchLegs([2]Leg{c.a, c.b})
		if match != c.match || !ratio.Equal(d(c.ratio)) {
			t.Errorf("%s/%s = %s %s, want %s %s", c.a.Base, c.b.Base, match, ratio, c.match, c.ratio)
		}
	}
}

// A broker accepts the hedge and rejects it later on the stream. The action
// must stop on that rejection — not send the hedge again — and the group is
// repaired by the policy.
func TestAsynchronousHedgeRejectionStopsTheAction(t *testing.T) {
	rig := newRig(t)
	request := openRequest(ModeLimit)
	request.Maker, request.Price, request.TIF = 0, d("64000"), "gtc"
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"pending"}
	group, err := rig.engine.Open(rig.ctx, rig.market, request)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rig.venue.restingCount() == 1 })
	rig.venue.fillMaker(t, "binance:perp:BTC-USDT", "acc_a", "0.004")
	// The hedge is accepted (pending); the exchange then rejects it.
	var hedge gwclient.Order
	waitFor(t, func() bool {
		rig.venue.mu.Lock()
		defer rig.venue.mu.Unlock()
		for _, order := range rig.venue.orders {
			if order.Instrument == "okx:perp:BTC-USDT-SWAP" && order.Status == "pending" {
				order.Status, order.RejectMessage, order.RejectCode = "rejected", "Insufficient availableMargin", "TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR"
				hedge = *order
				return true
			}
		}
		return false
	})
	rig.engine.OnOrder("acc_b", hedge)
	group = rig.waitStatus(t, group.ID, StatusClosed, StatusBroken, StatusOpen)
	hedges := 0
	rig.venue.mu.Lock()
	for _, order := range rig.venue.orders {
		if order.Instrument == "okx:perp:BTC-USDT-SWAP" && order.Side == "sell" {
			hedges++
		}
	}
	rig.venue.mu.Unlock()
	if hedges != 1 || rig.venue.restingCount() != 0 {
		t.Fatalf("hedges sent = %d, resting = %d: the rejection did not stop the action", hedges, rig.venue.restingCount())
	}
	orders, _ := rig.store.Orders(rig.ctx, group.ID)
	for _, order := range orders {
		if order.Role == RoleHedge && (order.State != OrderRejected || order.Error.IsZero()) {
			t.Fatalf("hedge order = %+v", order)
		}
	}
}

// A venue that forgets closed orders answers "unconfirmed". The order's
// fills establish it.
func TestUnconfirmedLookupIsResolvedFromFills(t *testing.T) {
	rig := newRig(t)
	rig.venue.forgetful = true
	rig.venue.script["binance:perp:BTC-USDT"] = []string{"unknown-filled"}
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	books := rig.books(t, group.ID)
	if !books[0].Held.Equal(d("0.01")) || rig.venue.maxPlacements() != 1 {
		t.Fatalf("books = %+v", books)
	}
}

// When neither the order nor a fill can be found, the order is unresolved
// after a bounded wait and the group is left for the operator: no automatic
// repair on a leg whose size is unknown.
func TestUnresolvableOrderLeavesTheGroupForTheOperator(t *testing.T) {
	rig := newRig(t)
	rig.venue.forgetful = true
	clock := time.Now()
	var clockMu sync.Mutex
	rig.engine.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	rig.venue.script["okx:perp:BTC-USDT-SWAP"] = []string{"unknown-missing"}
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	clockMu.Lock()
	clock = clock.Add(unresolvedAfter + time.Second)
	clockMu.Unlock()
	group = rig.waitStatus(t, group.ID, StatusBroken)
	orders, _ := rig.store.Orders(rig.ctx, group.ID)
	unresolved := 0
	for _, order := range orders {
		if order.State == OrderUnresolved {
			unresolved++
		}
		if order.Leg == 0 && order.Side == "sell" {
			t.Fatal("an automatic trim was sent although a leg's size is unknown")
		}
	}
	if unresolved != 1 {
		t.Fatalf("orders = %+v", orders)
	}
}

// A leg closed outside the hedge (here: the venue reports no position) marks
// the group unhedged once the disagreement persists.
func TestVenuePositionMismatchMarksTheGroupUnhedged(t *testing.T) {
	rig := newRig(t)
	group, err := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	if err != nil {
		t.Fatal(err)
	}
	rig.waitStatus(t, group.ID, StatusOpen)
	report := func(account string, positions string) {
		rig.market.Handle(gwclient.Message{Channel: "account.status", Account: account, Data: []byte(`{"connected":true}`)})
		rig.market.Handle(gwclient.Message{Channel: "account.positions", Account: account, Data: []byte(positions)})
	}
	report("acc_a", `[{"inst":"binance:perp:BTC-USDT","side":"long","size":"0.01","entryPrice":"65000","unrealizedPnl":"0"}]`)
	report("acc_b", `[]`) // the short leg is gone
	clock := time.Now()
	rig.engine.now = func() time.Time { return clock }
	rig.engine.ReconcileVenue(rig.ctx)
	if got, _ := rig.store.Group(rig.ctx, group.ID); got.Status != StatusOpen {
		t.Fatalf("marked at first sight: %s", got.Status)
	}
	clock = clock.Add(venueMismatchAfter + time.Second)
	rig.engine.ReconcileVenue(rig.ctx)
	got, _ := rig.store.Group(rig.ctx, group.ID)
	if got.Status != StatusBroken || !strings.Contains(got.Problem.Message, "leg 2") {
		t.Fatalf("group = %s %q", got.Status, got.Problem.Message)
	}
	// A venue holding more than the hedge (other trades) is not a mismatch.
	other, _ := rig.engine.Open(rig.ctx, rig.market, openRequest(ModeMarket))
	rig.waitStatus(t, other.ID, StatusOpen)
	report("acc_b", `[{"inst":"okx:perp:BTC-USDT-SWAP","side":"short","size":"5","entryPrice":"65000","unrealizedPnl":"0"}]`)
	report("acc_a", `[{"inst":"binance:perp:BTC-USDT","side":"long","size":"5","entryPrice":"65000","unrealizedPnl":"0"}]`)
	clock = clock.Add(time.Hour)
	rig.engine.ReconcileVenue(rig.ctx)
	clock = clock.Add(time.Hour)
	rig.engine.ReconcileVenue(rig.ctx)
	if got, _ := rig.store.Group(rig.ctx, other.ID); got.Status != StatusOpen {
		t.Fatalf("larger venue position flagged: %s %q", got.Status, got.Problem.Message)
	}
}
