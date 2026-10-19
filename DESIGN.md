---
name: Bolter
description: Single-operator multi-venue trading terminal. Visual language and interaction model follow app.hyperliquid.xyz/trade.
source: extracted 2026-10-15 from app.hyperliquid.xyz/trade (computed styles + screenshots)
colors:
  bg: "#1b2429"            # page canvas, shows through the gaps between panels
  surface: "#0f1a1f"       # panels (top bar, chart, book, ticket, blotter), sign-in page
  overlay: "#1b2429"       # menus and popovers floating over panels
  surface-raised: "#273035" # inputs, segmented controls, hover rows, dialogs' controls
  border: "#273035"        # 1px panel dividers
  border-strong: "#575e62" # focused / emphasized input borders
  text: "#f6fefd"          # primary values
  text-secondary: "#d2dad7" # table cells
  text-muted: "#949e9c"    # labels, column headers, inactive tabs
  text-faint: "#9aa3a48c"
  accent: "#50d2c1"        # primary action, active tab underline, links, slider
  accent-hover: "#97fce4"
  accent-text: "#04060c"   # text on accent buttons
  accent-soft: "#0e3333"   # disabled primary / subtle accent fill
  long: "#1fa67d"          # buy / positive change (fills)
  long-text: "#50d2c1"     # positive numbers in text
  long-soft: "rgba(31,166,125,0.20)"  # bid depth bars
  short: "#ed7088"         # sell / negative change
  short-soft: "rgba(237,112,136,0.20)" # ask depth bars
  short-deep: "#732a36"    # sell-tab active fill
  warning-bg: "#2e0c18"
typography:
  family: "Inter Variable, system-ui, -apple-system, Segoe UI, Roboto, sans-serif"  # HL uses proprietary 'OurFont'; Inter is the closest licensed match
  numeric: "tabular-nums on every number"
  base: { fontSize: "12px", lineHeight: "1.25", fontWeight: 400 }   # nearly everything
  emphasis: { fontSize: "14px", fontWeight: 500 }
  instrument: { fontSize: "20px", lineHeight: "27px", fontWeight: 400 }  # market name in header
  dialog-title: { fontSize: "20px", fontWeight: 400 }
  tiny: { fontSize: "10px" }
rounded:
  sm: "4px"
  md: "5px"   # panels, small chips (leverage badge)
  lg: "8px"   # buttons, inputs, segmented controls
  xl: "12px"  # dialogs
  full: "9999px"
spacing:
  unit: "4px"
  panel-gap: "3px"     # canvas shows between panels
  panel-padding-x: "10px"
  row-height: "24px"   # book & table rows
  control-height: "32px" # inputs, segmented, tabs
  button-height: "40px"  # primary action
breakpoints:
  compact: "1000px"
  mobile: "480px"
---

# Bolter trading terminal

## Overview

A dark, dense, single-screen trading terminal. Everything a decision needs —
price, book, size, position, open orders — is visible without scrolling on a
desktop. Colour carries meaning only: teal = action / long / positive,
pink = short / negative / danger. Everything else is grey on near-black teal.
No cards, shadows, gradients or illustrations.

## Colors

- Panels (`surface`) are the darkest layer. The slightly lighter canvas
  (`bg`) shows through 3px gaps; panels have a 5px radius and no border.
  Menus and popovers use `overlay` with a 1px `border` and 10px radius.
- `accent` teal is reserved for: the primary action button, the active tab
  underline/text, the slider thumb, links and positive numbers.
- Long/short colours apply to side buttons, PnL, price change and book rows.
  Book depth bars use the `*-soft` fills; prices in the book use full colour.
- Disabled primary buttons use `accent-soft` fill with `text-muted` text.

## Typography

12px for virtually everything, including table cells, labels and buttons.
Numbers always use tabular figures and are right-aligned in tables. The
instrument name in the market header is the only large text (20px).
Labels are `text-muted`; values are `text` or `text-secondary`. Labels that
explain a concept are dotted-underlined (tooltip affordance).

## Layout (desktop ≥ 1000px)

```
┌ top bar 56px: logo · nav ─────────────────────── account / Connect · settings ┐
├ market header 56px (instrument selector + stats) ┬ book/trades ┬ ticket 320px ─┤
├ chart (tabs: Chart)                               │  ~300px     │               │
│                                                   │             │               │
├ blotter tabs: Balances · Positions · Open Orders · Trade History ┤ account     │
│ table                                                            │ summary     │
└ status bar 24px: connection dot ─────────────────────────────────────────────┘
```

- Chart + book share the upper region; the blotter spans chart+book width
  and takes ~35% of height. The order ticket runs full height on the right;
  account summary (equity, margin, deposit/withdraw placeholders absent)
  sits below it.
- Below 1000px: ticket moves under the chart; book/trades become a tab next
  to Chart. Below 480px: single column, ticket first after header.

## Components

**Mark** — a teal (`accent`) rounded tile (28/32, radius 8) split by a bolt
cut out of it (`web/src/ui/logo.tsx`, `web/public/favicon.svg`). Wordmark:
mark + "Bolter" 17px semibold, `text`. No other brand colours.

**Top bar** — 56px, `surface`, flush with the panels below. Wordmark left,
then nav items (12px, padding 8×10px): active = `accent`, hover =
`accent-hover`, no underline or background. **More** has a 14px chevron that
flips when open; its menu (`overlay`, 10px radius, items 12px/24px, padding
5×10px, hover = `accent-hover` text) holds the project links.
Right: the **Accounts** summary (a status dot — pink when any account's
stream is down — then "3 Accounts" or the single account's name, then total
equity in `muted`) or a teal **Connect** button when no account exists, then
a 32px icon button (1px `border`, radius lg, `muted` icon; hover/open =
`overlay` fill and `text` icon) for the session menu. The Accounts popover
(360px, `overlay`) is an overview, not a selector: a table Account · Equity ·
Available with a dot and "Native" / "Cross-exchange" under each name, a
Total row, and "+ Connect exchange". Clicking an account filters the
blotter to it; ✕ (on row hover) removes it after an inline confirm.

**Sign-in page** — `surface` page, a 320px left-aligned column centred on
screen: mark 40px, "Sign in to Bolter" 20px, one muted line, a labelled
password field with show/hide toggle and Caps Lock hint, full-width primary
button. Errors replace the hint under the field and turn its border pink.
The footer tells where the first-run password is printed.

**Market header** — instrument selector (icon-less name + chevron, 20px) with a
leverage chip (`accent-soft` bg, `accent` text, radius md). Stats are label
(muted, dotted underline) over value (white). Change uses long/short colour.
Funding shows the rate per settlement with its period (`0.0012% /1h`), the
annualized rate, and a countdown; rate and APR use `long-text`/`short`. Rates
from venues with different periods are only comparable through the APR.

**Instrument selector** — popover anchored under the name, 800px wide: search
input (radius lg, `surface-raised`), category tabs (All · Tradable · Perps ·
Spot, plus one tab per venue; Tradable appears once an account exists),
table: Market · Venue · Type. Keyboard: ↑/↓ navigate, Enter select, Esc
close, ⌘K open. Every market is listed and fully live — market data always
comes from the venue itself, whatever account trades it. A small `long` dot
after the venue marks markets a connected account can trade (tooltip names
the accounts); they rank slightly ahead. Non-crypto markets carry a muted
asset-class word after the symbol (Stock, Commodity, FX, Pre-IPO).

**Order book** — tabs Order Book · Trades (underline style). Header row:
tick selector, unit selector. Columns Price · Size · Total. Asks above (pink
price), spread row centred (`Spread 0.1 0.004%`), bids below (teal price).
Cumulative depth bars drawn from the right in soft colours. Clicking a price
fills the ticket's limit price. The book fills its panel: rows per side are
computed from the panel height (asks take the whole rows of the top half,
bids the rest). When a venue streams fewer levels than fit, blank rows go on
the outer edges so the spread stays centred.

**Chart drawing tools** — a 40px vertical toolbar left of the chart (32px
icon buttons, `text-secondary`, hover `surface-raised`, active `accent`):
Cross · Trend line · Ray · Horizontal line · Vertical line · Rectangle ·
Fib retracement · Measure, then Magnet (snap to OHLC), Hide, Remove all.
Two-point tools are placed by click-click or by dragging; the tool returns to
Cross afterwards. Drawings are grey lines (`text-secondary`), selected ones
show teal handles and can be dragged or reshaped; Delete removes the
selected one, Esc cancels. Horizontal lines label their price on the axis.
Measure is transient (teal up, pink down) and shows change, %, bars and
time. Drawings are stored per instrument in localStorage and kept across
intervals.

**Order ticket** —
0. Account row: muted "Account" left; right, a dot (stream state), the
   account name, and "· Cross-exchange" for a broker. The account follows
   the selected market's venue: the account last chosen for that venue, else
   the native account, else a broker that reaches it. When more than one
   account (or an unconnected broker) could trade it, the row is a menu:
   "Trade BTC-USD with", one line per account (name, Native /
   Cross-exchange margin, available on the right, ✓ on the current one),
   then "+ Connect Gate CrossEx · one pool, many venues". The choice is
   remembered per venue. Nothing is switched without the user picking it.
1. Top row of three equal segmented buttons: margin mode (Cross), leverage
   (`20x`, opens Adjust Leverage dialog), and a third slot.
2. Underline tabs: Market · Limit.
3. Side toggle: one `surface-raised` pill containing Buy / Long and
   Sell / Short; the selected half fills with `long` (teal text dark) or
   `short-deep`.
4. Info rows: Available to Trade, Current Position (label muted left, value
   right).
5. Price input (limit only) with "Mid" shortcut; Size input with unit
   dropdown (base / quote) inside the field, right-aligned.
6. Percentage slider (teal track, 5 detents at 0/25/50/75/100) plus a small
   numeric `%` box.
7. Checkboxes: Reduce Only; for limit, TIF select (GTC / IOC / Post Only).
8. Full-width primary button 40px: "Place Order" (teal) — becomes
   "Connect" when no account, disabled while submitting.
9. Summary rows: Liquidation Price, Order Value, Margin Required, Fees.

Size steps, minimums and tick come from the trading account's route
(`/accounts/{id}/instruments`), not the market: Gate CrossEx can allow
finer sizes than the venue. On a broker account Available to Trade is the
pool's available margin and a Min Size row shows the broker's minimums.
When no connected account trades the market, the ticket is the symbol and
venue, one sentence ("No connected account trades Kraken perps."), a
primary "Connect Kraken" that opens the connect dialog straight on that
venue's form, a bordered "Connect Gate CrossEx · trades Kraken from one
pool" when a broker reaches it, and a muted note that the chart, book and
trades stay live. An order a broker accepted shows "accepted · Waiting for
the exchange"; a later rejection arrives as an error toast with the venue's
own text and the account's name.

**Blotter** — every connected account in one set of tables. Underline tabs
with counts (`Positions (2)`); at the right end of the tab row an account
filter ("All Accounts ▾", a menu of accounts with status dots; the choice is
remembered). Tables: 12px, header `text-muted`, rows 24px, no zebra, hover
`surface-raised`. Coin cells show the symbol and its venue in muted text.
With more than one account an Account column follows Coin; for a broker it
adds "→ Binance", the venue it routed to. Positions columns: Coin
(coloured by side, with leverage) · Account · Size · Position Value · Entry
Price · Mark Price · PNL (ROE %) · Liq. Price · Margin · Close (Market link
in `accent`; it always closes on the account holding the position, and the
dialog names it). Empty state: plain muted text "No open positions yet".
Open orders mark "Pending" (accepted, not yet on the exchange) and
risk-engine orders (Liquidation, Auto-deleverage, Delisting) in pink.
Balances show the market ("Binance USD-M", "Cross-exchange pool").

**Account summary** — below the ticket, for the ticket's account, titled
with its name. Native: stablecoin balance, available, unrealized PNL,
position margin, private stream state. Broker: Margin
Balance, Available Margin, Unrealized PNL, Initial Margin, Initial and
Maintenance Margin Rate (percent; pink when at risk), then a small table of
each venue's equity and margin in use. Below 110% / 100% initial rate or at
100% maintenance rate, a pink warning box states the consequence.

**Dialogs** — centred, 480px, `surface` bg, 1px `border`, radius xl, 24px
padding, title 20px centred, body 12px centred, close × top-right.
Backdrop blurs and darkens the page. Primary action full-width teal 40px.
Warnings use a pink-bordered box with pink text on `warning-bg`.

**Connect (credential) dialog** — the only place credentials are entered.
Step 1: venue grid (name buttons). Step 2: fields for that venue from
`/v1/venues` (password-type inputs for secrets, autocomplete off), optional
label, "Connect" button; errors inline under the button. Opened from the
ticket for one venue, it starts at step 2. Success closes the dialog; a new
account for the market on screen becomes its ticket account. Removing an
account lives in the Accounts popover with a confirm.

**Status bar** — 24px, left: pill `● Online` (teal dot) / `● Reconnecting`
(pink); right: gateway latency.

## Hedge page (`/hedge`)

A hedge is two legs traded as one position, so the page is organised around
the pair, not around an instrument:

```
┌ pair bar: Long X venue / Short Y venue · Flip · Swap · entry spread · net funding · 7d ┐ ticket
├ leg 1 panel                           ┬ leg 2 panel                         ┤ 340px
│ instrument · Long/Short · account      │ (same)                              │
│ available · funding /period · APR · next│                                     │
│ venue position · compact book          │                                     │
├ hedges table: one row per hedge, expandable to both legs + orders/activity/funding ┤
└ status bar ─────────────────────────────────────────────────────────────────────┘
```

- The pair bar carries only the numbers that decide whether to trade the
  pair: entry spread at executable prices (bps), net funding APR now and over
  7 days. Funding is always shown as rate per period plus APR; raw rates of
  different periods are never compared.
- The ticket: Market both / Limit + hedge tabs, size in quote (default) or
  base, a live preview (per-leg quantity and value, expected average price and
  slippage, entry spread, rounding mismatch, crossing cost, net funding,
  breakeven days), and the failure policy. The primary button names the
  action; problems from the preview appear under it in pink.
- Hedges table columns: Hedge (label + both legs, L/S coloured), Status,
  Size, Net exposure ("Hedged", or the residual in pink), Spread entry → now,
  Net funding, Next funding (countdown + amount), Funding earned, Fees, Net
  PnL, actions (Close / Unwind; Rehedge / Trim when unhedged; Cancel for a
  working limit). An unhedged row has the warning background; a badge in the
  tab bar counts them.
- Expanded row: two leg cards side by side (held, entry · mark, unrealized,
  fees, funding, liquidation distance, venue position, account stream dot),
  then tabs Orders (with slippage per market order) · Activity · Funding ·
  Note.
- The Hedge menu appears only when the trader serves a hedger.

## Interaction rules

- Market orders submit immediately on click (no confirmation dialog), like
  Hyperliquid. The button shows a spinner; the result arrives as a toast
  (bottom-left): "Filled 0.01 ETH at 2,354.1" / "Order rejected: <venue
  message>" / "Outcome unknown — checking…" followed by the lookup result.
- Positions/balances update only from the account stream's full snapshots;
  never optimistic.
- Keyboard: ⌘K instrument search; Esc closes dialogs/popovers.
- Numbers never shift layout: fixed column widths, tabular figures.
- Focus ring: 1px `accent` outline offset 1px.

## Do / Don't

- Do keep everything at 12px; resist larger type.
- Do use colour only for side/sign/action.
- Don't add cards, shadows, gradients, icons in tables, or explanatory copy.
- Don't use Hyperliquid's logo, name or proprietary font.
