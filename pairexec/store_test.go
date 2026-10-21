package pairexec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// testStore opens a fresh database on the server named by
// HEDGER_TEST_DATABASE_URL (a superuser URL); tests skip without it.
func testStore(t *testing.T) *Store {
	t.Helper()
	return testStoreWith(t, HedgeSchema)
}

func testStoreWith(t *testing.T, schema Schema) *Store {
	t.Helper()
	admin := os.Getenv("HEDGER_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("set HEDGER_TEST_DATABASE_URL to run PostgreSQL tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	name := "hedger_test_" + hex.EncodeToString(raw)
	if _, err := conn.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "drop database if exists "+name+" with (force)")
		conn.Close(context.Background())
	})
	parsed, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	store, err := OpenStore(ctx, parsed.String(), schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func d(value string) decimal.Decimal { return decimal.RequireFromString(value) }

func sampleGroup(id string) (Group, Action) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	group := Group{
		ID: id, Label: "BTC basis", Status: StatusOpening, Policy: PolicyUnwind, Match: MatchBase, Ratio: d("1"), CreatedAt: now,
		Legs: [2]Leg{
			{Index: 0, AccountID: "acc_a", Instrument: "binance:perp:BTC-USDT", Venue: "binance", Product: "perp", Base: "BTC", Quote: "USDT", Side: "buy", StepSize: d("0.001"), TickSize: d("0.1"), MinQty: d("0.001"), MinNotional: d("5")},
			{Index: 1, AccountID: "acc_b", Instrument: "okx:perp:BTC-USDT-SWAP", Venue: "okx", Product: "perp", Base: "BTC", Quote: "USDT", Side: "sell", StepSize: d("0.0001"), TickSize: d("0.1"), MinQty: d("0.0001")},
		},
	}
	action := Action{ID: "act_" + id, GroupID: id, Kind: KindOpen, Mode: ModeMarket, Target: [2]decimal.Decimal{d("0.01"), d("0.01")}, Status: ActionWorking, CreatedAt: now}
	return group, action
}

func TestStoreRoundTripsAGroupAndRejectsASecondWorkingAction(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	group, action := sampleGroup("hg_1")
	if err := store.CreateGroup(ctx, group, action); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Group(ctx, "hg_1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Label != "BTC basis" || !loaded.Ratio.Equal(d("1")) || loaded.Legs[1].Instrument != "okx:perp:BTC-USDT-SWAP" || !loaded.Legs[0].MinNotional.Equal(d("5")) {
		t.Fatalf("group = %+v", loaded)
	}
	second := Action{ID: "act_2", GroupID: "hg_1", Kind: KindClose, Mode: ModeMarket, Target: [2]decimal.Decimal{d("-0.01"), d("-0.01")}, Status: ActionWorking, CreatedAt: time.Now()}
	if err := store.StartAction(ctx, second, StatusClosing); !errors.Is(err, ErrBusy) {
		t.Fatalf("second working action err = %v", err)
	}
	if err := store.FinishAction(ctx, action.ID, ActionDone, Text{}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartAction(ctx, second, StatusClosing); err != nil {
		t.Fatal(err)
	}
	working, _ := store.WorkingActions(ctx)
	if len(working) != 1 || working[0].ID != "act_2" || !working[0].Target[0].Equal(d("-0.01")) {
		t.Fatalf("working = %+v", working)
	}
	if _, err := store.Group(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStoreOrdersNeverLoseFillsAndFillsAreIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	group, action := sampleGroup("hg_2")
	if err := store.CreateGroup(ctx, group, action); err != nil {
		t.Fatal(err)
	}
	order := Order{ClientOrderID: "1001", GroupID: "hg_2", ActionID: action.ID, Leg: 0, Role: RoleTaker, AccountID: "acc_a",
		Instrument: "binance:perp:BTC-USDT", Side: "buy", Type: "market", Qty: d("0.01"), RefPrice: d("65000"), State: OrderSending, CreatedAt: time.Now()}
	if err := store.InsertOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	order.State, order.VenueOrderID, order.Filled, order.AvgPrice = OrderFilled, "v77", d("0.01"), d("65010")
	if err := store.UpdateOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	// A stale report must not erase the fill already seen.
	stale := order
	stale.State, stale.Filled, stale.AvgPrice, stale.VenueOrderID = OrderOpen, decimal.Zero, decimal.Zero, ""
	if err := store.UpdateOrder(ctx, stale); err != nil {
		t.Fatal(err)
	}
	loaded, found, _ := store.OrderByVenueID(ctx, "acc_a", "v77")
	if !found || !loaded.Filled.Equal(d("0.01")) || !loaded.AvgPrice.Equal(d("65010")) || !loaded.RefPrice.Equal(d("65000")) {
		t.Fatalf("order = %+v", loaded)
	}
	fill := Fill{ClientOrderID: "1001", FillID: "f1", Price: d("65010"), Qty: d("0.01"), Fee: d("0.26"), FeeAsset: "USDT", Time: time.Now()}
	if added, err := store.AddFill(ctx, fill); !added || err != nil {
		t.Fatalf("first fill added=%v err=%v", added, err)
	}
	if added, _ := store.AddFill(ctx, fill); added {
		t.Fatal("duplicate fill added")
	}
	fills, _ := store.Fills(ctx, "hg_2")
	if len(fills) != 1 || !fills[0].Fee.Equal(d("0.26")) {
		t.Fatalf("fills = %+v", fills)
	}
}

func TestStoreFundingAndEvents(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	group, action := sampleGroup("hg_3")
	if err := store.CreateGroup(ctx, group, action); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	for _, accrual := range []FundingAccrual{
		{GroupID: "hg_3", Leg: 0, SettledAt: at, Rate: d("0.0001"), Qty: d("0.01"), Price: d("65000"), Amount: d("-0.065")},
		{GroupID: "hg_3", Leg: 1, SettledAt: at, Rate: d("0.0002"), Qty: d("0.01"), Price: d("65000"), Amount: d("0.13")},
		{GroupID: "hg_3", Leg: 1, SettledAt: at, Rate: d("0.0002"), Qty: d("0.01"), Price: d("65000"), Amount: d("0.13")},
	} {
		if _, err := store.AddFunding(ctx, accrual); err != nil {
			t.Fatal(err)
		}
	}
	totals, _ := store.FundingTotals(ctx, []string{"hg_3"})
	if !totals["hg_3"][0].Equal(d("-0.065")) || !totals["hg_3"][1].Equal(d("0.13")) {
		t.Fatalf("totals = %+v", totals)
	}
	_ = store.AddEvent(ctx, "hg_3", "info", textOf("event.cancelRequested"))
	_ = store.AddEvent(ctx, "hg_3", "warn", textOf("event.makerFilled", "leg", 1, "filled", "0.004", "target", "0.01"))
	events, _ := store.Events(ctx, "hg_3", 10)
	if len(events) != 2 || events[0].Message.Code != "event.makerFilled" || events[0].Message.Params["filled"] != "0.004" || events[0].Message.Message != "Maker leg 1 filled 0.004 of 0.01" {
		t.Fatalf("events = %+v", events)
	}
	if err := store.SetGroupStatus(ctx, "hg_3", StatusClosed, Text{}); err != nil {
		t.Fatal(err)
	}
	closed, _ := store.Group(ctx, "hg_3")
	if closed.ClosedAt == nil {
		t.Fatal("closed_at not set")
	}
	listed, _ := store.Groups(ctx, time.Now().Add(-time.Hour))
	if len(listed) != 1 {
		t.Fatalf("recently closed not listed: %d", len(listed))
	}
	listed, _ = store.Groups(ctx, time.Now().Add(time.Hour))
	if len(listed) != 0 {
		t.Fatalf("old closed listed: %d", len(listed))
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	store := testStore(t)
	if err := store.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Two apps can share one database: each schema's tables, indexes and
// migration record are its own.
func TestSchemasKeepTheirOwnTables(t *testing.T) {
	hedge := testStore(t)
	spread := NewStoreWithPool(hedge.Pool(), Schema{Prefix: "spread_", Migrations: "spreader_exec_migrations", Lock: 42})
	ctx := context.Background()
	if err := spread.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	group, action := sampleGroup("g_hedge")
	if err := hedge.CreateGroup(ctx, group, action); err != nil {
		t.Fatal(err)
	}
	other, otherAction := sampleGroup("g_spread")
	if err := spread.CreateGroup(ctx, other, otherAction); err != nil {
		t.Fatal(err)
	}
	for store, want := range map[*Store]string{hedge: "g_hedge", spread: "g_spread"} {
		groups, err := store.Groups(ctx, time.Time{})
		if err != nil || len(groups) != 1 || groups[0].ID != want {
			t.Fatalf("groups = %+v, %v; want only %s", groups, err, want)
		}
	}
	var tables int
	if err := hedge.Pool().QueryRow(ctx, `select count(*) from information_schema.tables where table_name like 'spread\_%'`).Scan(&tables); err != nil || tables != 7 {
		t.Fatalf("spread tables = %d, %v", tables, err)
	}
}
