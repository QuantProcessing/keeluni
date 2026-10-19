package hedger

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned for unknown groups.
var ErrNotFound = errors.New("hedge group not found")

// ErrBusy means the group already has a working action.
var ErrBusy = errors.New("the group already has an action working")

// Store is the hedger's PostgreSQL state.
type Store struct {
	pool *pgxpool.Pool
}

// OpenStore connects and applies pending migrations. Concurrent hedger
// processes serialize migrations on an advisory lock.
func OpenStore(ctx context.Context, url string) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database URL is invalid: %w", err)
	}
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	store := &Store{pool: pool}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Close() { store.pool.Close() }

// Ping checks the database is reachable.
func (store *Store) Ping(ctx context.Context) error { return store.pool.Ping(ctx) }

const migrationLock = 7_356_210_441 // arbitrary, hedger-specific

func (store *Store) migrate(ctx context.Context) error {
	conn, err := store.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLock); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", migrationLock) //nolint:errcheck
	if _, err := conn.Exec(ctx, `create table if not exists hedger_migrations (version int primary key, applied_at timestamptz not null default now())`); err != nil {
		return err
	}
	applied := map[int]bool{}
	rows, err := conn.Query(ctx, "select version from hedger_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		applied[version] = true
	}
	rows.Close()
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s has no numeric prefix", entry.Name())
		}
		if applied[version] {
			continue
		}
		script, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(script)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, "insert into hedger_migrations (version) values ($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// numeric columns are scanned as text and parsed with decimal, so no value
// passes through float64.
func dec(text *string) decimal.Decimal {
	if text == nil || *text == "" {
		return decimal.Zero
	}
	value, err := decimal.NewFromString(*text)
	if err != nil {
		return decimal.Zero
	}
	return value
}

func nullDec(value decimal.Decimal) any {
	if value.IsZero() {
		return nil
	}
	return value.String()
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

// CreateGroup stores a new group, its legs and its opening action in one
// transaction.
func (store *Store) CreateGroup(ctx context.Context, group Group, action Action) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into hedge_groups (id, label, note, status, policy, match, ratio, created_at, updated_at)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
			group.ID, group.Label, group.Note, group.Status, group.Policy, group.Match, group.Ratio.String(), group.CreatedAt); err != nil {
			return err
		}
		for _, leg := range group.Legs {
			if _, err := tx.Exec(ctx, `insert into hedge_legs (group_id, leg, account_id, instrument, venue, product, base, quote, side, step_size, tick_size, min_qty, min_notional)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
				group.ID, leg.Index, leg.AccountID, leg.Instrument, leg.Venue, leg.Product, leg.Base, leg.Quote, leg.Side,
				leg.StepSize.String(), leg.TickSize.String(), leg.MinQty.String(), leg.MinNotional.String()); err != nil {
				return err
			}
		}
		return insertAction(ctx, tx, action)
	})
}

func insertAction(ctx context.Context, tx pgx.Tx, action Action) error {
	var maker any
	if action.MakerLeg != nil {
		maker = *action.MakerLeg
	}
	_, err := tx.Exec(ctx, `insert into hedge_actions (id, group_id, kind, mode, maker_leg, limit_price, tif, target_0, target_1, status, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
		action.ID, action.GroupID, action.Kind, action.Mode, maker, nullDec(action.LimitPrice), action.TIF,
		action.Target[0].String(), action.Target[1].String(), action.Status, action.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "one_working") {
		return ErrBusy
	}
	return err
}

// StartAction stores a new action on an existing group and sets the group's
// status, unless another action is already working.
func (store *Store) StartAction(ctx context.Context, action Action, status string) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if err := insertAction(ctx, tx, action); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update hedge_groups set status=$2, problem='', updated_at=now() where id=$1`, action.GroupID, status)
		return err
	})
}

// SetGroupStatus updates a group's status and problem text.
func (store *Store) SetGroupStatus(ctx context.Context, groupID, status, problem string) error {
	closed := "closed_at"
	if status == StatusClosed || status == StatusFailed {
		closed = "coalesce(closed_at, now())"
	}
	_, err := store.pool.Exec(ctx, `update hedge_groups set status=$2, problem=$3, updated_at=now(), closed_at=`+closed+` where id=$1`, groupID, status, problem)
	return err
}

// UpdateGroupText edits a group's label and note.
func (store *Store) UpdateGroupText(ctx context.Context, groupID, label, note string) error {
	tag, err := store.pool.Exec(ctx, `update hedge_groups set label=$2, note=$3, updated_at=now() where id=$1`, groupID, label, note)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

const groupColumns = `g.id, g.label, g.note, g.status, g.policy, g.match, g.ratio::text, g.problem, g.created_at, g.updated_at, g.closed_at`

func scanGroup(row pgx.Row) (Group, error) {
	var group Group
	var ratio string
	if err := row.Scan(&group.ID, &group.Label, &group.Note, &group.Status, &group.Policy, &group.Match, &ratio, &group.Problem, &group.CreatedAt, &group.UpdatedAt, &group.ClosedAt); err != nil {
		return Group{}, err
	}
	group.Ratio = dec(&ratio)
	return group, nil
}

// Groups lists active groups, plus finished ones closed after since (zero:
// none).
func (store *Store) Groups(ctx context.Context, finishedSince time.Time) ([]Group, error) {
	rows, err := store.pool.Query(ctx, `select `+groupColumns+` from hedge_groups g
		where g.status not in ('closed','failed') or g.closed_at >= $1
		order by g.created_at desc`, finishedSince)
	if err != nil {
		return nil, err
	}
	var groups []Group
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, group)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groups, store.attachLegs(ctx, groups)
}

// Group loads one group with its legs.
func (store *Store) Group(ctx context.Context, id string) (Group, error) {
	group, err := scanGroup(store.pool.QueryRow(ctx, `select `+groupColumns+` from hedge_groups g where g.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, err
	}
	groups := []Group{group}
	if err := store.attachLegs(ctx, groups); err != nil {
		return Group{}, err
	}
	return groups[0], nil
}

func (store *Store) attachLegs(ctx context.Context, groups []Group) error {
	if len(groups) == 0 {
		return nil
	}
	ids := make([]string, len(groups))
	index := make(map[string]int, len(groups))
	for i, group := range groups {
		ids[i] = group.ID
		index[group.ID] = i
	}
	rows, err := store.pool.Query(ctx, `select group_id, leg, account_id, instrument, venue, product, base, quote, side,
		step_size::text, tick_size::text, min_qty::text, min_notional::text from hedge_legs where group_id = any($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID string
		var leg Leg
		var step, tick, minQty, minNotional string
		if err := rows.Scan(&groupID, &leg.Index, &leg.AccountID, &leg.Instrument, &leg.Venue, &leg.Product, &leg.Base, &leg.Quote, &leg.Side,
			&step, &tick, &minQty, &minNotional); err != nil {
			return err
		}
		leg.StepSize, leg.TickSize, leg.MinQty, leg.MinNotional = dec(&step), dec(&tick), dec(&minQty), dec(&minNotional)
		groups[index[groupID]].Legs[leg.Index] = leg
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

const actionColumns = `id, group_id, kind, mode, maker_leg, coalesce(limit_price::text,''), tif, target_0::text, target_1::text, status, error, created_at, updated_at`

func scanAction(row pgx.Row) (Action, error) {
	var action Action
	var maker *int16
	var limit, t0, t1 string
	if err := row.Scan(&action.ID, &action.GroupID, &action.Kind, &action.Mode, &maker, &limit, &action.TIF, &t0, &t1, &action.Status, &action.Error, &action.CreatedAt, &action.UpdatedAt); err != nil {
		return Action{}, err
	}
	if maker != nil {
		leg := int(*maker)
		action.MakerLeg = &leg
	}
	action.LimitPrice, action.Target[0], action.Target[1] = dec(&limit), dec(&t0), dec(&t1)
	return action, nil
}

// WorkingActions lists every action still working, oldest first.
func (store *Store) WorkingActions(ctx context.Context) ([]Action, error) {
	return store.queryActions(ctx, `select `+actionColumns+` from hedge_actions where status='working' order by created_at`)
}

// Actions lists a group's actions, oldest first.
func (store *Store) Actions(ctx context.Context, groupID string) ([]Action, error) {
	return store.queryActions(ctx, `select `+actionColumns+` from hedge_actions where group_id=$1 order by created_at`, groupID)
}

func (store *Store) queryActions(ctx context.Context, query string, args ...any) ([]Action, error) {
	rows, err := store.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []Action
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

// FinishAction sets a working action's final status.
func (store *Store) FinishAction(ctx context.Context, actionID, status, message string) error {
	_, err := store.pool.Exec(ctx, `update hedge_actions set status=$2, error=$3, updated_at=now() where id=$1 and status='working'`, actionID, status, message)
	return err
}

// ---------------------------------------------------------------------------
// Orders and fills
// ---------------------------------------------------------------------------

// InsertOrder records an order before it is sent.
func (store *Store) InsertOrder(ctx context.Context, order Order) error {
	_, err := store.pool.Exec(ctx, `insert into hedge_orders (client_order_id, group_id, action_id, leg, role, account_id, instrument, side, type, qty, price, ref_price, tif, reduce_only, state, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)`,
		order.ClientOrderID, order.GroupID, order.ActionID, order.Leg, order.Role, order.AccountID, order.Instrument, order.Side, order.Type,
		order.Qty.String(), nullDec(order.Price), nullDec(order.RefPrice), order.TIF, order.ReduceOnly, order.State, order.CreatedAt)
	return err
}

// UpdateOrder stores an order's latest venue state. Filled never decreases:
// a stale report cannot erase a fill already seen.
func (store *Store) UpdateOrder(ctx context.Context, order Order) error {
	_, err := store.pool.Exec(ctx, `update hedge_orders set state=$2, venue_order_id=case when $3='' then venue_order_id else $3 end,
		filled=greatest(filled, $4::numeric), avg_price=coalesce($5::numeric, avg_price), error=$6, updated_at=now()
		where client_order_id=$1`,
		order.ClientOrderID, order.State, order.VenueOrderID, order.Filled.String(), nullDec(order.AvgPrice), order.Error)
	return err
}

const orderColumns = `client_order_id, group_id, action_id, leg, role, account_id, instrument, side, type, qty::text, coalesce(price::text,''), coalesce(ref_price::text,''),
	tif, reduce_only, state, venue_order_id, filled::text, coalesce(avg_price::text,''), error, created_at, updated_at`

func scanOrder(row pgx.Row) (Order, error) {
	var order Order
	var qty, price, ref, filled, avg string
	var leg int16
	if err := row.Scan(&order.ClientOrderID, &order.GroupID, &order.ActionID, &leg, &order.Role, &order.AccountID, &order.Instrument, &order.Side, &order.Type,
		&qty, &price, &ref, &order.TIF, &order.ReduceOnly, &order.State, &order.VenueOrderID, &filled, &avg, &order.Error, &order.CreatedAt, &order.UpdatedAt); err != nil {
		return Order{}, err
	}
	order.Leg = int(leg)
	order.Qty, order.Price, order.RefPrice, order.Filled, order.AvgPrice = dec(&qty), dec(&price), dec(&ref), dec(&filled), dec(&avg)
	return order, nil
}

// Orders lists a group's orders, oldest first.
func (store *Store) Orders(ctx context.Context, groupID string) ([]Order, error) {
	return store.queryOrders(ctx, `select `+orderColumns+` from hedge_orders where group_id=$1 order by created_at, client_order_id`, groupID)
}

// ActionOrders lists one action's orders, oldest first.
func (store *Store) ActionOrders(ctx context.Context, actionID string) ([]Order, error) {
	return store.queryOrders(ctx, `select `+orderColumns+` from hedge_orders where action_id=$1 order by created_at, client_order_id`, actionID)
}

// OrderByClientID loads one order.
func (store *Store) OrderByClientID(ctx context.Context, clientOrderID string) (Order, bool, error) {
	order, err := scanOrder(store.pool.QueryRow(ctx, `select `+orderColumns+` from hedge_orders where client_order_id=$1`, clientOrderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	return order, err == nil, err
}

// OrderByVenueID finds an order by its venue order id within an account.
func (store *Store) OrderByVenueID(ctx context.Context, accountID, venueOrderID string) (Order, bool, error) {
	order, err := scanOrder(store.pool.QueryRow(ctx, `select `+orderColumns+` from hedge_orders where account_id=$1 and venue_order_id=$2`, accountID, venueOrderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	return order, err == nil, err
}

func (store *Store) queryOrders(ctx context.Context, query string, args ...any) ([]Order, error) {
	rows, err := store.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []Order
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

// AddFill stores a fill once; it reports whether it was new.
func (store *Store) AddFill(ctx context.Context, fill Fill) (bool, error) {
	tag, err := store.pool.Exec(ctx, `insert into hedge_fills (client_order_id, fill_id, price, qty, fee, fee_asset, liquidity, ts)
		values ($1,$2,$3,$4,$5,$6,$7,$8) on conflict do nothing`,
		fill.ClientOrderID, fill.FillID, fill.Price.String(), fill.Qty.String(), fill.Fee.String(), fill.FeeAsset, fill.Liquidity, fill.Time)
	return err == nil && tag.RowsAffected() == 1, err
}

// Fills lists a group's fills by order.
func (store *Store) Fills(ctx context.Context, groupID string) ([]Fill, error) {
	rows, err := store.pool.Query(ctx, `select f.client_order_id, f.fill_id, f.price::text, f.qty::text, f.fee::text, f.fee_asset, f.liquidity, f.ts
		from hedge_fills f join hedge_orders o using (client_order_id) where o.group_id=$1 order by f.ts, f.fill_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fills []Fill
	for rows.Next() {
		var fill Fill
		var price, qty, fee string
		if err := rows.Scan(&fill.ClientOrderID, &fill.FillID, &price, &qty, &fee, &fill.FeeAsset, &fill.Liquidity, &fill.Time); err != nil {
			return nil, err
		}
		fill.Price, fill.Qty, fill.Fee = dec(&price), dec(&qty), dec(&fee)
		fills = append(fills, fill)
	}
	return fills, rows.Err()
}

// ---------------------------------------------------------------------------
// Events and funding
// ---------------------------------------------------------------------------

// AddEvent appends to a group's audit trail.
func (store *Store) AddEvent(ctx context.Context, groupID, level, message string) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := store.pool.Exec(ctx, `insert into hedge_events (group_id, level, message) values ($1,$2,$3)`, groupID, level, message)
	return err
}

// Events lists a group's audit trail, newest first, at most limit lines.
func (store *Store) Events(ctx context.Context, groupID string, limit int) ([]Event, error) {
	rows, err := store.pool.Query(ctx, `select id, group_id, at, level, message from hedge_events where group_id=$1 order by id desc limit $2`, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.GroupID, &event.At, &event.Level, &event.Message); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AddFunding records one leg's funding at one settlement, once.
func (store *Store) AddFunding(ctx context.Context, accrual FundingAccrual) (bool, error) {
	tag, err := store.pool.Exec(ctx, `insert into hedge_funding (group_id, leg, settled_at, rate, qty, price, amount) values ($1,$2,$3,$4,$5,$6,$7) on conflict do nothing`,
		accrual.GroupID, accrual.Leg, accrual.SettledAt, accrual.Rate.String(), accrual.Qty.String(), accrual.Price.String(), accrual.Amount.String())
	return err == nil && tag.RowsAffected() == 1, err
}

// FundingTotals sums estimated funding per leg of each group.
func (store *Store) FundingTotals(ctx context.Context, groupIDs []string) (map[string][2]decimal.Decimal, error) {
	result := map[string][2]decimal.Decimal{}
	if len(groupIDs) == 0 {
		return result, nil
	}
	rows, err := store.pool.Query(ctx, `select group_id, leg, sum(amount)::text from hedge_funding where group_id = any($1) group by group_id, leg`, groupIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID, sum string
		var leg int16
		if err := rows.Scan(&groupID, &leg, &sum); err != nil {
			return nil, err
		}
		totals := result[groupID]
		totals[leg] = dec(&sum)
		result[groupID] = totals
	}
	return result, rows.Err()
}

// FundingLog lists a group's funding accruals, newest first.
func (store *Store) FundingLog(ctx context.Context, groupID string) ([]FundingAccrual, error) {
	rows, err := store.pool.Query(ctx, `select group_id, leg, settled_at, rate::text, qty::text, price::text, amount::text from hedge_funding where group_id=$1 order by settled_at desc, leg`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var log []FundingAccrual
	for rows.Next() {
		var accrual FundingAccrual
		var leg int16
		var rate, qty, price, amount string
		if err := rows.Scan(&accrual.GroupID, &leg, &accrual.SettledAt, &rate, &qty, &price, &amount); err != nil {
			return nil, err
		}
		accrual.Leg = int(leg)
		accrual.Rate, accrual.Qty, accrual.Price, accrual.Amount = dec(&rate), dec(&qty), dec(&price), dec(&amount)
		log = append(log, accrual)
	}
	return log, rows.Err()
}
