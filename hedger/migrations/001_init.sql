-- Hedger schema v1. Quantities are base units, prices quote per base.
-- A group's holdings are never stored: they are the sum of its orders' fills.

create table hedge_groups (
    id         text primary key,
    label      text not null default '',
    note       text not null default '',
    status     text not null check (status in ('opening', 'open', 'closing', 'closed', 'failed', 'broken')),
    policy     text not null check (policy in ('unwind', 'hold')),
    match      text not null check (match in ('base', 'notional')),
    -- ratio: base units of leg 1 per base unit of leg 0 (fixed at open).
    ratio      numeric not null check (ratio > 0),
    problem    text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    closed_at  timestamptz
);
create index hedge_groups_active on hedge_groups (created_at) where status not in ('closed', 'failed');
create index hedge_groups_finished on hedge_groups (closed_at desc) where status in ('closed', 'failed');

create table hedge_legs (
    group_id     text     not null references hedge_groups (id) on delete cascade,
    leg          smallint not null check (leg in (0, 1)),
    account_id   text     not null,
    -- broker: the account is a broker (Gate CrossEx) whose rules and margin
    -- pool the leg uses.
    broker       boolean  not null default false,
    instrument   text     not null,
    venue        text     not null,
    product      text     not null check (product in ('spot', 'perp')),
    base         text     not null,
    quote        text     not null,
    -- side opens the leg; the opposite side reduces it.
    side         text     not null check (side in ('buy', 'sell')),
    -- Rules are the trading account's route at open (a broker's can differ
    -- from the venue's).
    step_size    numeric  not null check (step_size > 0),
    tick_size    numeric  not null check (tick_size > 0),
    min_qty      numeric  not null default 0,
    min_notional numeric  not null default 0,
    primary key (group_id, leg)
);

create table hedge_actions (
    id          text primary key,
    group_id    text     not null references hedge_groups (id) on delete cascade,
    kind        text     not null check (kind in ('open', 'close', 'unwind', 'rehedge')),
    mode        text     not null check (mode in ('market', 'limit')),
    maker_leg   smallint check (maker_leg in (0, 1)),
    limit_price numeric,
    tif         text     not null default '',
    -- Signed base quantities: positive trades a leg's opening side.
    target_0    numeric  not null,
    target_1    numeric  not null,
    status      text     not null check (status in ('working', 'done', 'canceled', 'failed')),
    error       text     not null default '',
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);
create index hedge_actions_group on hedge_actions (group_id, created_at);
create unique index hedge_actions_one_working on hedge_actions (group_id) where status = 'working';

-- Every order is written here before it is sent: its client order id is the
-- only way to learn the outcome of a send that timed out.
create table hedge_orders (
    client_order_id text primary key,
    group_id        text     not null references hedge_groups (id) on delete cascade,
    action_id       text     not null references hedge_actions (id) on delete cascade,
    leg             smallint not null check (leg in (0, 1)),
    role            text     not null check (role in ('maker', 'taker', 'hedge')),
    account_id      text     not null,
    instrument      text     not null,
    side            text     not null check (side in ('buy', 'sell')),
    type            text     not null check (type in ('market', 'limit')),
    qty             numeric  not null check (qty > 0),
    price           numeric,
    ref_price       numeric,
    tif             text     not null default '',
    reduce_only     boolean  not null default false,
    state           text     not null,
    venue_order_id  text     not null default '',
    filled          numeric  not null default 0 check (filled >= 0),
    avg_price       numeric,
    error           text     not null default '',
    created_at      timestamptz not null default now(),
    updated_at      timestamptz not null default now()
);
create index hedge_orders_group on hedge_orders (group_id, created_at);
create index hedge_orders_venue on hedge_orders (account_id, venue_order_id) where venue_order_id <> '';

create table hedge_fills (
    client_order_id text    not null references hedge_orders (client_order_id) on delete cascade,
    fill_id         text    not null,
    price           numeric not null,
    qty             numeric not null,
    -- fee is a cost in fee_asset; negative is a rebate.
    fee             numeric not null default 0,
    fee_asset       text    not null default '',
    liquidity       text    not null default '',
    ts              timestamptz not null,
    primary key (client_order_id, fill_id)
);

create table hedge_events (
    id       bigserial primary key,
    group_id text not null references hedge_groups (id) on delete cascade,
    at       timestamptz not null default now(),
    level    text not null check (level in ('info', 'warn', 'error')),
    message  text not null
);
create index hedge_events_group on hedge_events (group_id, id);

-- Estimated funding per settlement: amount > 0 was received.
create table hedge_funding (
    group_id   text        not null references hedge_groups (id) on delete cascade,
    leg        smallint    not null check (leg in (0, 1)),
    settled_at timestamptz not null,
    rate       numeric     not null,
    qty        numeric     not null,
    price      numeric     not null,
    amount     numeric     not null,
    primary key (group_id, leg, settled_at)
);
