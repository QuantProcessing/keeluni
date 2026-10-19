// Pure hedge helpers shared by the ticket, the leg panels and the book.

import type { Account, FundingPoint, Instrument, Position, Route } from "@/api/types";
import { marketOf, tradingAccounts } from "@/lib/routes";
import type { AccountLive } from "@/state/hooks";

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
