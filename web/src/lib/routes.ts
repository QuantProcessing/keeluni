// Which accounts can trade an instrument, and with which rules.
//
// An instrument is an order book (binance:perp:BTC-USDT). An account is whose
// money and which channel. A native account trades its venue's markets; a
// broker account (Gate CrossEx) trades many venues' markets from one pool,
// with its own rules. The rules an order follows are the selected account's.

import type { Account, Instrument, Route } from "@/api/types";

/** The market of an instrument as the gateway names it: venue:product[:settle]. */
export function marketOf(instrument: Pick<Instrument, "venue" | "product" | "settle" | "quote">): string {
  if (instrument.product === "spot") return `${instrument.venue}:spot`;
  return `${instrument.venue}:perp:${(instrument.settle || instrument.quote).toUpperCase()}`;
}

/**
 * Whether account can trade instrument. Brokered instruments (source set)
 * exist only through a broker, so native accounts never trade them.
 */
export function canTrade(account: Account, instrument: Instrument): boolean {
  if (instrument.source && account.kind !== "broker") return false;
  return account.markets.includes(marketOf(instrument));
}

/** Accounts other than current that can trade instrument, for the "trade with" hint. */
export function alternativeAccounts(accounts: Account[], current: Account | undefined, instrument: Instrument): Account[] {
  return accounts.filter((account) => account.id !== current?.id && account.active && canTrade(account, instrument));
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
