# KeelUni

A self-hosted, one-stop trading stack for one operator: every connected
venue's perps and spot (options and commodities next) through one terminal.

```text
web        React terminal (Hyperliquid-style, see DESIGN.md): Trade + Hedge
  │  cookie session, same origin
trader     password login, session, proxy to apps, serves web      — no database
  │  bearer token, internal network only
hedger     hedge app: two legs traded as one position               — its own PostgreSQL database
  │  bearer token
gateway    venue-neutral REST + WebSocket API                       — one encrypted file
  │
exchange   normalized clients and models
  │
sdk        native venue APIs
```

- **sdk/** implements each venue's native API faithfully.
- **exchange/** normalizes venues into one typed client contract.
- **gateway/** exposes that contract over HTTP/WebSocket (see below).
- **trader/** is the manual-trading app. Other apps (hedging, arbitrage) talk to
  the gateway directly and own their own storage.
- **hedger/** is the hedge app: it opens, tracks and closes two legs on
  (possibly) different venues as one hedge, in PostgreSQL. It deploys on its
  own; the trader serves its UI when `HEDGER_URL` is set. See
  [docs/hedger.md](docs/hedger.md).
- **gwclient/** is the Go client of the gateway API for apps.

## What the gateway is

Three rules decide whether something belongs in the gateway or in an app:

1. **The only way out to a venue.** Every venue request — public or private,
   REST or WebSocket — leaves through the gateway. Rate limits, connections,
   proxies and credentials exist only there, so one budget per venue is shared
   and prioritised: orders and cancels first, interactive reads next, resident
   collection last (at most 25% of a venue's request budget). Apps never call a
   venue directly.
2. **Current venue facts only.** The gateway may hold facts in memory — the
   instrument catalog, position snapshots, market-wide datasets such as funding
   — as long as a restart can rebuild them from the venue. It accumulates no
   history the venue cannot return again, keeps no order ledger (idempotency is
   the venue's `clientOrderId`, and the venue is the source of truth), and needs
   no database: trading keeps working when an app's database is down.
3. **Normalisation, not business.** The gateway makes every venue speak the
   same identifiers, units and meanings (e.g. one funding rate convention, with
   its interval and annualised value). Anything relating two instruments or
   venues, any strategy, and any multi-order workflow belongs to an app.

Resident collection therefore splits by whether data must accumulate: current
market-wide facts are gateway datasets; time series kept beyond what a venue
retains, and anything derived across instruments, live in an app with its own
database.

Supported: Binance, OKX, Bybit, Bitget, Gate (spot + USDT perps; Binance/Bybit/Bitget
also USDC perps), Hyperliquid and Lighter (spot + perps), Kraken Futures (USD perps) and
Deribit (USDC perps). Gate CrossEx is a broker account: one margin pool trading Binance,
OKX, Bybit, Gate, Hyperliquid, Lighter, Kraken and Deribit perps
(design: [docs/crossex-design.md](docs/crossex-design.md)).

API and security contract: [docs/gateway-api.md](docs/gateway-api.md).
Exchange library: [docs/exchange/README.md](docs/exchange/README.md).

## Run

Locally (Go 1.26, Node 22):

```sh
scripts/dev.sh                                   # http://127.0.0.1:8080
HTTPS_PROXY=http://127.0.0.1:7892 scripts/dev.sh # if venues need a proxy
```

With Docker (gateway, trader, hedger and an internal PostgreSQL):

```sh
docker compose up -d --build --force-recreate
docker compose logs trader | grep password       # first-run password, shown once
```

Locally the hedge app needs a PostgreSQL once: `POSTGRES_ADMIN_URL=postgres://…
scripts/dev.sh` creates its role and database; later runs reuse them. Without
it, `scripts/dev.sh` runs gateway and trader only.

The first start prints a generated password once. Change it with
`go run ./cmd/trader passwd` (or `docker compose run --rm -it --no-deps trader passwd`).
Connect exchanges from the **Connect** button in the app.

## Safety

Every order reaches a real exchange. Use keys without withdrawal permission and
start small. Credentials are write-only: they are verified against the venue,
stored encrypted (`data/gateway/accounts.enc`, key in `data/gateway/master.key`),
and never returned by any API or written to logs. Back up `master.key`; without it
the stored accounts cannot be decrypted.

Only `127.0.0.1` is bound by default. For remote access use an SSH tunnel or a
VPN, or put a TLS reverse proxy in front and set `TRADER_SECURE_COOKIE=1`.

## Test

```sh
make test
HEDGER_TEST_DATABASE_URL=postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable \
  go test ./hedger/                              # hedger tests against a real PostgreSQL
```
