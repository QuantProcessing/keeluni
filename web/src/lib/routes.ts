// Which accounts can trade an instrument, and with which rules.
//
// An instrument is an order book (binance:perp:BTC-USDT); its market data
// always comes from that venue. An account is whose money and which channel.
// A native account trades its venue's markets; a broker account (Gate
// CrossEx) trades many venues' markets from one pool, with its own rules.
// The rules an order follows are the trading account's.

import type { Account, Balance, Instrument, Margin, Position, Route } from "@/api/types";
import { venueLabel } from "./format";

/** The market of an instrument as the gateway names it: venue:product[:settle]. */
export function marketOf(instrument: Pick<Instrument, "venue" | "product" | "settle" | "quote">): string {
  if (instrument.product === "spot") return `${instrument.venue}:spot`;
  return `${instrument.venue}:perp:${(instrument.settle || instrument.quote).toUpperCase()}`;
}

/**
 * Whether account can trade instrument. A broker reaches whole markets but
 * not every instrument in them; once its routes are known they decide.
 */
export function canTrade(account: Account, instrument: Instrument, routes?: Route[]): boolean {
  if (!account.active || !account.markets.includes(marketOf(instrument))) return false;
  return routes === undefined || routes.some((route) => route.inst === instrument.id);
}

/** Accounts that can trade instrument: native accounts first, then brokers, oldest first. */
export function tradingAccounts(accounts: Account[], instrument: Instrument, routes: Record<string, Route[] | undefined> = {}): Account[] {
  return accounts
    .filter((account) => canTrade(account, instrument, routes[account.id]))
    .sort((a, b) => (a.kind === b.kind ? a.createdAt.localeCompare(b.createdAt) : a.kind === "native" ? -1 : 1));
}

/**
 * The account an order on instrument goes to: the one last chosen for its
 * venue while it can still trade it, else the first trading account. The
 * choice is never changed behind the user's back to a different account
 * than one of these.
 */
export function chooseAccount(candidates: Account[], remembered: string | undefined): Account | undefined {
  return candidates.find((account) => account.id === remembered) ?? candidates[0];
}

/** Brokers (connected or not) whose markets include instrument's. */
export function brokersReaching(venues: { venue: string; kind: string; markets: string[] }[], instrument: Instrument): string[] {
  const market = marketOf(instrument);
  return venues.filter((item) => item.kind === "broker" && item.markets.includes(market)).map((item) => item.venue);
}

/** "Binance" or "Binance · main": the venue, and the label when it says more. */
export function accountName(account: Pick<Account, "venue" | "label">): string {
  const venue = venueLabel(account.venue);
  const label = account.label.trim();
  return label && label !== account.venue && label !== venue ? `${venue} · ${label}` : venue;
}

export interface Rules {
  tickSize: string;
  stepSize: string;
  minQty: string;
  minNotional?: string;
}

/**
 * The rules an order follows: the account's route when it has one, else the
 * instrument's own (a native account trades on the venue's rules).
 */
export function rulesFor(instrument: Instrument, routes: Route[] | undefined): Rules {
  const route = routes?.find((item) => item.inst === instrument.id);
  if (route) return { tickSize: route.tickSize, stepSize: route.stepSize, minQty: route.minQty, minNotional: route.minNotional };
  return { tickSize: instrument.tickSize, stepSize: instrument.stepSize, minQty: instrument.minQty, minNotional: instrument.minNotional };
}

const STABLES = new Set(["USDT", "USDC", "USD", "FDUSD", "USDE", "USDT0"]);

export interface Equity {
  /** Stablecoin balances, or the broker pool's margin balance. */
  total: number;
  available: number;
  unrealizedPnl: number;
}

/** A dollar summary of one account. Undefined until its stream has data. */
export function accountEquity(account: Account, live: { balances?: Balance[]; positions?: Position[]; margin?: Margin } | undefined): Equity | undefined {
  if (!live) return undefined;
  if (account.kind === "broker") {
    if (!live.margin) return undefined;
    return { total: Number(live.margin.equity) || 0, available: Number(live.margin.available) || 0, unrealizedPnl: Number(live.margin.unrealizedPnl) || 0 };
  }
  if (!live.balances) return undefined;
  const stable = live.balances.filter((item) => STABLES.has(item.asset.toUpperCase()));
  return {
    total: stable.reduce((sum, item) => sum + Number(item.total), 0),
    available: stable.reduce((sum, item) => sum + Number(item.available), 0),
    unrealizedPnl: (live.positions ?? []).reduce((sum, item) => sum + Number(item.unrealizedPnl), 0),
  };
}

export type MarginLevel = "ok" | "caution" | "restricted" | "liquidation";

/**
 * Gate CrossEx risk states (API v1.0.6, account assets):
 * - initial margin rate below 110%: margin coins cannot be transferred out
 * - initial margin rate below 100%: open orders are cancelled, only closing is allowed
 * - maintenance margin rate at or below 100%: the account is liquidated
 * Rates are ratios (1.1 = 110%). Empty or zero rates mean no margin in use.
 */
export function marginLevel(initialRate: string | undefined, maintenanceRate: string | undefined): MarginLevel {
  const imr = Number(initialRate);
  const mmr = Number(maintenanceRate);
  const inUse = (value: number) => Number.isFinite(value) && value > 0;
  if (inUse(mmr) && mmr <= 1) return "liquidation";
  if (inUse(imr) && imr < 1) return "restricted";
  if (inUse(imr) && imr < 1.1) return "caution";
  return "ok";
}
