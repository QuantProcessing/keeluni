// Pure hedge helpers shared by the ticket, the leg panels and the book.

import type { Account, FundingPoint, Instrument, Position, Route } from "@/api/types";
import { marketOf, tradingAccounts } from "@/lib/routes";
import type { AccountLive } from "@/state/hooks";
import { translate } from "@/i18n";

export type Side = "buy" | "sell";

export interface LegChoice {
  inst: string;
  accountId: string;
  side: Side;
}

export type Pair = [LegChoice, LegChoice];

export const DEFAULT_PAIR: Pair = [
  { inst: "okx:perp:BTC-USDT-SWAP", accountId: "", side: "buy" },
  { inst: "hyperliquid:perp:BTC-USDC", accountId: "", side: "sell" },
];

/** +1 for a long leg, -1 for a short one. */
export const direction = (side: Side) => (side === "buy" ? 1 : -1);

/**
 * Net funding a hedge earns per year, as a fraction of one side's notional:
 * a long leg pays its APR, a short leg receives it. Spot legs pay nothing.
 * Returns undefined when a perp leg's APR is unknown.
 */
export function netFundingApr(legs: Array<{ side: Side; apr?: number; spot?: boolean }>): number | undefined {
  let total = 0;
  for (const leg of legs) {
    if (leg.spot) continue;
    if (leg.apr === undefined || !Number.isFinite(leg.apr)) return undefined;
    total += -direction(leg.side) * leg.apr;
  }
  return total;
}

/** Mean APR of the settled points since `since` (ms); undefined without data. */
export function averageApr(points: FundingPoint[] | undefined, since: number): number | undefined {
  const values = (points ?? []).filter((point) => point.t >= since && point.apr !== undefined).map((point) => Number(point.apr));
  if (!values.length) return undefined;
  return values.reduce((sum, value) => sum + value, 0) / values.length;
}

/** Spread of leg 2 over leg 1 in basis points of leg 1, ratio-adjusted. */
export function spreadBps(price0: number, price1: number, ratio: number): number | undefined {
  if (!(price0 > 0) || !(price1 > 0) || !(ratio > 0)) return undefined;
  return ((price1 / ratio - price0) / price0) * 10_000;
}

/**
 * Days for net funding to repay an entry cost (both in basis points of one
 * side's notional); undefined when funding does not pay.
 */
export function breakevenDays(costBps: number, aprFraction: number | undefined): number | undefined {
  if (aprFraction === undefined || aprFraction <= 0 || !(costBps >= 0)) return undefined;
  const perDayBps = (aprFraction * 10_000) / 365;
  return costBps / perDayBps;
}

/**
 * What a leg can spend on its account: a broker's shared pool (in USD), or
 * the native account's margin asset. Undefined until the account streams.
 */
export function legAvailable(account: Account | undefined, instrument: Instrument | undefined, live: AccountLive | undefined, side: Side): { amount: number; asset: string } | undefined {
  if (!account || !instrument || !live) return undefined;
  if (account.kind === "broker") {
    return live.margin ? { amount: Number(live.margin.available) || 0, asset: "USD" } : undefined;
  }
  if (!live.balances) return undefined;
  const perp = instrument.product === "perp";
  const asset = perp ? instrument.settle || instrument.quote : side === "buy" ? instrument.quote : instrument.base;
  const market = marketOf(instrument);
  const row = live.balances.find((item) => item.market.toUpperCase() === market.toUpperCase() && item.asset.toUpperCase() === asset.toUpperCase())
    ?? live.balances.find((item) => item.asset.toUpperCase() === asset.toUpperCase());
  return { amount: row ? Number(row.available) : 0, asset };
}

/**
 * The account a leg trades on: the one the user picked, while it can still
 * trade the leg's instrument. There is no default — a hedge leg's account is
 * always the user's choice.
 */
export function legAccount(accounts: Account[], routes: Record<string, Route[] | undefined>, instrument: Instrument | undefined, accountId: string): Account | undefined {
  if (!instrument || !accountId) return undefined;
  return tradingAccounts(accounts, instrument, routes).find((account) => account.id === accountId);
}

export function legPosition(positions: Position[] | undefined, inst: string): Position | undefined {
  return positions?.find((item) => item.inst === inst);
}

/** The price a market order of side starts at (ask for a buy, bid for a sell). */
export function takePrice(book: { bids: Array<[string, string]>; asks: Array<[string, string]> } | undefined, side: Side): number | undefined {
  const level = side === "buy" ? book?.asks[0] : book?.bids[0];
  const value = Number(level?.[0]);
  return value > 0 ? value : undefined;
}

/** The passive price for a resting order of side (bid for a buy, ask for a sell). */
export function makePrice(book: { bids: Array<[string, string]>; asks: Array<[string, string]> } | undefined, side: Side): number | undefined {
  return takePrice(book, side === "buy" ? "sell" : "buy");
}

export function bpsText(value: number | string | undefined, digits = 2): string {
  const n = Number(value);
  if (value === undefined || value === "" || !Number.isFinite(n)) return "—";
  return (n > 0 ? "+" : n < 0 ? "−" : "") + Math.abs(n).toFixed(digits) + " bps";
}

export function aprText(value: number | string | undefined, digits = 2): string {
  const n = Number(value);
  if (value === undefined || value === "" || !Number.isFinite(n)) return "—";
  return (n > 0 ? "+" : n < 0 ? "−" : "") + Math.abs(n * 100).toFixed(digits) + "%";
}

/** Funding per settlement as a percentage with enough digits for hourly venues. */
export function rateText(value: string | undefined): string {
  if (!value) return "—";
  const percent = Number(value) * 100;
  if (!Number.isFinite(percent)) return "—";
  return (Math.abs(percent) < 0.001 && percent !== 0 ? percent.toFixed(6) : percent.toFixed(4)) + "%";
}

export const tone = (value: number | string | undefined) => {
  const n = Number(value);
  return !Number.isFinite(n) || n === 0 ? "" : n > 0 ? "text-up" : "text-down";
};

// ---------------------------------------------------------------------------
// The pair: always one long leg and one short leg
// ---------------------------------------------------------------------------

const opposite = (side: Side): Side => (side === "buy" ? "sell" : "buy");

/**
 * A pair as a hedge: leg 2 trades the opposite side of leg 1, and a spot leg
 * (which cannot be shorted here) is the long one when the other leg can be
 * short.
 */
export function normalizePair(pair: Pair, spot: [boolean, boolean] = [false, false]): Pair {
  let side0: Side = pair[0].side;
  if (spot[0] && !spot[1]) side0 = "buy";
  else if (spot[1] && !spot[0]) side0 = "sell";
  return [{ ...pair[0], side: side0 }, { ...pair[1], side: opposite(side0) }];
}

/** Swaps which leg is long; undefined when a spot leg would become short. */
export function flipPair(pair: Pair, spot: [boolean, boolean] = [false, false]): Pair | undefined {
  const side0 = opposite(pair[0].side);
  if ((spot[0] && side0 === "sell") || (spot[1] && side0 === "buy")) return undefined;
  return [{ ...pair[0], side: side0 }, { ...pair[1], side: opposite(side0) }];
}

// ---------------------------------------------------------------------------
// Spread history
// ---------------------------------------------------------------------------

export interface SpreadPoint { t: number; value: number }

/**
 * The spread of leg 2 over leg 1 per bar, from the two legs' closes at the
 * same bar time: in bps of leg 1 through the ratio for the same underlying
 * ((c2 / ratio − c1) / c1), else the plain price ratio c2 / c1. Bars only one
 * leg has are skipped.
 */
export function spreadSeries(leg0: Array<{ t: number; c: string }>, leg1: Array<{ t: number; c: string }>, match: "base" | "notional", ratio: number): SpreadPoint[] {
  const closes = new Map(leg0.map((bar) => [bar.t, Number(bar.c)]));
  const out: SpreadPoint[] = [];
  for (const bar of leg1) {
    const c0 = closes.get(bar.t);
    const c1 = Number(bar.c);
    if (!(c0 && c0 > 0) || !(c1 > 0)) continue;
    const value = match === "base" ? (ratio > 0 ? ((c1 / ratio - c0) / c0) * 10_000 : NaN) : c1 / c0;
    if (Number.isFinite(value)) out.push({ t: bar.t, value });
  }
  return out.sort((a, b) => a.t - b.t);
}

/** Last, mean, low and high of a series; undefined when empty. */
export function seriesStats(points: SpreadPoint[]): { last: number; mean: number; low: number; high: number } | undefined {
  if (!points.length) return undefined;
  let sum = 0, low = Infinity, high = -Infinity;
  for (const point of points) {
    sum += point.value;
    low = Math.min(low, point.value);
    high = Math.max(high, point.value);
  }
  return { last: points[points.length - 1]!.value, mean: sum / points.length, low, high };
}

/**
 * Net funding APR over time from each leg's settled APRs: each leg's last
 * known APR holds until its next settlement (venues settle on different
 * clocks). The short leg receives, the long leg pays; spot legs pay nothing.
 */
export function netFundingSeries(legs: Array<{ side: Side; spot?: boolean; points: Array<{ t: number; apr?: string }> }>): SpreadPoint[] {
  const series = legs.map((leg) => leg.points.filter((point) => point.apr !== undefined && Number.isFinite(Number(point.apr))).sort((a, b) => a.t - b.t));
  const times = [...new Set(series.flat().map((point) => point.t))].sort((a, b) => a - b);
  const index = legs.map(() => -1);
  const out: SpreadPoint[] = [];
  for (const t of times) {
    let total = 0;
    let known = true;
    legs.forEach((leg, i) => {
      if (leg.spot) return;
      const points = series[i]!;
      while (index[i]! + 1 < points.length && points[index[i]! + 1]!.t <= t) index[i]!++;
      const point = points[index[i]!];
      if (!point) known = false;
      else total += -direction(leg.side) * Number(point.apr);
    });
    if (known) out.push({ t, value: total });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Leverage and margin
// ---------------------------------------------------------------------------

/**
 * The leverage a leg's margin is estimated with: one the user just set on
 * the venue, else the account's position in the instrument, else 1x (the
 * venue's own setting is unknown without a position). Spot is always 1x.
 */
export function legLeverage(spot: boolean, position: Position | undefined, chosen: number | undefined): { value: number; source: "set" | "position" | "default" } {
  if (spot) return { value: 1, source: "default" };
  if (chosen && chosen > 0) return { value: chosen, source: "set" };
  const fromPosition = Number(position?.leverage);
  if (fromPosition > 0) return { value: fromPosition, source: "position" };
  return { value: 1, source: "default" };
}

/**
 * Margin each leg needs against what its account has. Legs on one broker
 * account draw on one pool: their needs add up against it once.
 */
export function marginCheck(legs: Array<{ accountId: string; shared: boolean; notional: number; leverage: number; available: number | undefined }>): { need: number[]; short: string | undefined } {
  const need = legs.map((leg) => (leg.leverage > 0 ? leg.notional / leg.leverage : leg.notional));
  if (legs.length === 2 && legs[0]!.shared && legs[0]!.accountId === legs[1]!.accountId) {
    const total = need[0]! + need[1]!;
    const available = legs[0]!.available;
    return { need, short: available !== undefined && total > available ? translate("margin.poolShort", { need: money(total), available: money(available) }) : undefined };
  }
  for (let i = 0; i < legs.length; i++) {
    const available = legs[i]!.available;
    if (available !== undefined && need[i]! > available) return { need, short: translate("margin.legShort", { n: i + 1, need: money(need[i]!), available: money(available) }) };
  }
  return { need, short: undefined };
}

const money = (value: number) => "$" + value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

// ---------------------------------------------------------------------------
// Opportunities: the funding table read as pairs
// ---------------------------------------------------------------------------

export interface FundingRow {
  inst: string;
  venue: string;
  base: string;
  apr?: string;
  intervalHours?: number;
  nextFunding?: number;
  mark?: string;
  oiUsd?: string;
  stale: boolean;
}

export interface Opportunity {
  base: string;
  /** The leg that pays the least funding: bought. */
  long: FundingRow;
  /** The leg that pays the most: sold, so it receives. */
  short: FundingRow;
  /** Net funding a year, short APR − long APR (a fraction). */
  net: number;
  /** The smaller leg's open interest in USD: how much the pair can carry. */
  depth?: number;
  /** Mark of the short leg over the long leg, in bps: the basis now. */
  basisBps?: number;
  /** Perps of this base the table lists. */
  venues: number;
}

/**
 * The best funding pair of every underlying: the venue paying the most is
 * shorted, the one paying the least is bought. Rows without an APR and stale
 * rows are left out. With minOi, a pair needs depth: a leg below minOi (USD
 * open interest) is dropped, and a leg whose venue does not publish open
 * interest (Binance, Hyperliquid) is kept only when paired with one that does
 * and clears the bar — a 400% APR on a market nobody trades is not an
 * opportunity, and two unknowns prove nothing.
 */
export function opportunities(rows: FundingRow[], options: { minOi?: number; venues?: string[] } = {}): Opportunity[] {
  const byBase = new Map<string, FundingRow[]>();
  for (const row of rows) {
    if (row.stale || row.apr === undefined || !Number.isFinite(Number(row.apr))) continue;
    if (options.venues?.length && !options.venues.includes(row.venue)) continue;
    if (options.minOi && row.oiUsd !== undefined && !(Number(row.oiUsd) >= options.minOi)) continue;
    byBase.set(row.base, [...(byBase.get(row.base) ?? []), row]);
  }
  const out: Opportunity[] = [];
  for (const [base, list] of byBase) {
    if (list.length < 2) continue;
    let long = list[0]!, short = list[0]!;
    for (const row of list) {
      if (Number(row.apr) < Number(long.apr)) long = row;
      if (Number(row.apr) > Number(short.apr)) short = row;
    }
    if (long.inst === short.inst) continue;
    const ois = [long.oiUsd, short.oiUsd].filter((value) => value !== undefined).map(Number);
    if (options.minOi && ois.length === 0) continue;
    const marks = [Number(long.mark), Number(short.mark)];
    out.push({
      base, long, short, net: Number(short.apr) - Number(long.apr), venues: list.length,
      depth: ois.length ? Math.min(...ois) : undefined,
      basisBps: marks[0]! > 0 && marks[1]! > 0 ? ((marks[1]! - marks[0]!) / marks[0]!) * 10_000 : undefined,
    });
  }
  return out.sort((a, b) => b.net - a.net);
}

// ---------------------------------------------------------------------------
// Spread ladder: both books read as one market for the pair
// ---------------------------------------------------------------------------

type Levels = Array<[string, string]>;

/** Average price and filled base to spend notional (quote) walking levels; undefined when empty. */
export function sweepNotional(levels: Levels, notional: number): { avg: number; base: number; full: boolean } | undefined {
  let spent = 0, base = 0;
  for (const [priceText, sizeText] of levels) {
    const price = Number(priceText), size = Number(sizeText);
    if (!(price > 0) || !(size > 0)) continue;
    const take = Math.min(size, (notional - spent) / price);
    spent += take * price;
    base += take;
    if (spent >= notional * (1 - 1e-9)) break;
  }
  return base > 0 ? { avg: spent / base, base, full: spent >= notional * (1 - 1e-9) } : undefined;
}

export interface LadderRow {
  /** Notional per leg, in the quote currency. */
  notional: number;
  /** Opening: buy the long leg's asks, sell the short leg's bids. bps of the long leg. */
  open?: number;
  /** Closing: sell the long leg's bids, buy back the short leg's asks. */
  close?: number;
  /** Both books hold the size on both sides. */
  full: boolean;
}

/**
 * The pair's synthetic book: for each size, the spread a market entry would
 * trade at (short leg's average sell over the long leg's average buy, in bps
 * of the long leg) and the spread a market exit would trade at. open − close
 * is the round trip a hedge pays for crossing both books twice; funding has
 * to earn it back.
 */
export function spreadLadder(
  longBook: { bids: Levels; asks: Levels } | undefined,
  shortBook: { bids: Levels; asks: Levels } | undefined,
  sizes: number[],
  ratio = 1,
): LadderRow[] {
  return sizes.map((notional) => {
    if (!longBook || !shortBook) return { notional, full: false };
    const buyLong = sweepNotional(longBook.asks, notional);
    const sellShort = sweepNotional(shortBook.bids, notional);
    const sellLong = sweepNotional(longBook.bids, notional);
    const buyShort = sweepNotional(shortBook.asks, notional);
    const spread = (longPrice: number | undefined, shortPrice: number | undefined) =>
      longPrice && shortPrice ? ((shortPrice / ratio - longPrice) / longPrice) * 10_000 : undefined;
    return {
      notional,
      open: spread(buyLong?.avg, sellShort?.avg),
      close: spread(sellLong?.avg, buyShort?.avg),
      full: !!(buyLong?.full && sellShort?.full && sellLong?.full && buyShort?.full),
    };
  });
}
