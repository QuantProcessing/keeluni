// Options math and package building for the options page. Pure functions:
// Black-76 on the expiry's forward with zero discounting (how the gateway
// and the venues value options), strategy presets, and payoff curves.
//
// Units: prices and greeks are in the strike currency (USD) per one unit of
// the underlying unless a function says otherwise. Vega per vol point,
// theta per day, IV a fraction.

import type { OptionChain, OptionRow } from "@/api/options";

const YEAR_MS = 365 * 24 * 3600 * 1000;

export function years(expiry: number, now: number): number {
  return Math.max(0, (expiry - now) / YEAR_MS);
}

function cdf(x: number): number {
  // Abramowitz-Stegun 7.1.26 through erf; |error| < 1.5e-7.
  const sign = x < 0 ? -1 : 1;
  const z = Math.abs(x) / Math.SQRT2;
  const t = 1 / (1 + 0.3275911 * z);
  const y = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-z * z);
  return 0.5 * (1 + sign * y);
}

function pdf(x: number): number {
  return Math.exp(-x * x / 2) / Math.sqrt(2 * Math.PI);
}

export function intrinsic(call: boolean, forward: number, strike: number): number {
  return call ? Math.max(forward - strike, 0) : Math.max(strike - forward, 0);
}

export function price(call: boolean, forward: number, strike: number, t: number, vol: number): number {
  if (!(forward > 0) || !(strike > 0)) return 0;
  if (t <= 0 || vol <= 0) return intrinsic(call, forward, strike);
  const root = vol * Math.sqrt(t);
  const d1 = (Math.log(forward / strike) + (vol * vol * t) / 2) / root;
  const d2 = d1 - root;
  return call ? forward * cdf(d1) - strike * cdf(d2) : strike * cdf(-d2) - forward * cdf(-d1);
}

export interface Greeks {
  delta: number;
  gamma: number;
  vega: number;
  theta: number;
}

export function greeks(call: boolean, forward: number, strike: number, t: number, vol: number): Greeks {
  if (!(forward > 0) || !(strike > 0)) return { delta: 0, gamma: 0, vega: 0, theta: 0 };
  if (t <= 0 || vol <= 0) {
    const delta = call ? (forward > strike ? 1 : 0) : forward < strike ? -1 : 0;
    return { delta, gamma: 0, vega: 0, theta: 0 };
  }
  const root = Math.sqrt(t);
  const d1 = (Math.log(forward / strike) + (vol * vol * t) / 2) / (vol * root);
  const density = pdf(d1);
  return {
    delta: call ? cdf(d1) : cdf(d1) - 1,
    gamma: density / (forward * vol * root),
    vega: (forward * density * root) / 100,
    theta: -(forward * density * vol) / (2 * root) / 365,
  };
}

/**
 * The premium (strike currency per unit) of an option at an IV on the
 * expiry's model: discount × Black-76(forward). The inverse of
 * impliedVolOf.
 */
export function priceAt(call: boolean, forward: number, strike: number, t: number, vol: number, discount = 1): number {
  return (discount > 0 ? discount : 1) * price(call, forward, strike, t, vol);
}

/** The IV of a premium (strike currency per unit) on the expiry's model. */
export function impliedVolOf(call: boolean, forward: number, strike: number, t: number, value: number, discount = 1): number | undefined {
  return impliedVol(call, forward, strike, t, value / (discount > 0 ? discount : 1));
}

/** Bisection on Black-76; undefined outside the no-arbitrage band. */
export function impliedVol(call: boolean, forward: number, strike: number, t: number, value: number): number | undefined {
  if (t <= 0 || !(value > intrinsic(call, forward, strike)) || value >= (call ? forward : strike)) return undefined;
  let low = 1e-4;
  let high = 10;
  for (let i = 0; i < 100; i++) {
    const mid = (low + high) / 2;
    if (price(call, forward, strike, t, mid) > value) high = mid;
    else low = mid;
    if (high - low < 1e-7) break;
  }
  return (low + high) / 2;
}

// ---------------------------------------------------------------------------
// Chain shape
// ---------------------------------------------------------------------------

export interface StrikeRow {
  strike: number;
  call?: OptionRow;
  put?: OptionRow;
}

/** One expiry's rows as strike lines, calls and puts side by side. */
export function strikeRows(chain: OptionChain | undefined, expiry: number): StrikeRow[] {
  if (!chain) return [];
  const byStrike = new Map<number, StrikeRow>();
  for (const row of chain.rows) {
    if (row.expiry !== expiry) continue;
    const strike = Number(row.strike);
    const line = byStrike.get(strike) ?? { strike };
    if (row.kind === "call") line.call = row;
    else line.put = row;
    byStrike.set(strike, line);
  }
  return [...byStrike.values()].sort((a, b) => a.strike - b.strike);
}

/** Index of the strike nearest the forward. */
export function atmIndex(rows: StrikeRow[], forward: number): number {
  let best = 0;
  rows.forEach((row, index) => {
    if (Math.abs(row.strike - forward) < Math.abs(rows[best]!.strike - forward)) best = index;
  });
  return best;
}

/** Strikes within ±window of the ATM strike; all when window <= 0. */
export function visibleRows(rows: StrikeRow[], forward: number, window: number): StrikeRow[] {
  if (window <= 0 || rows.length <= window * 2 + 1) return rows;
  const atm = atmIndex(rows, forward);
  return rows.slice(Math.max(0, atm - window), atm + window + 1);
}

/** "27NOV26" — the venue's way of naming an expiry. */
export function expiryLabel(ms: number): string {
  const d = new Date(ms);
  const months = ["JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"];
  return `${d.getUTCDate()}${months[d.getUTCMonth()]}${String(d.getUTCFullYear()).slice(2)}`;
}

/** Days to expiry, "4h" under a day. */
export function dteLabel(ms: number, now: number): string {
  const hours = (ms - now) / 3_600_000;
  if (hours <= 0) return "0h";
  if (hours < 24) return `${Math.ceil(hours)}h`;
  return `${Math.round(hours / 24)}d`;
}

// ---------------------------------------------------------------------------
// Packages
// ---------------------------------------------------------------------------

export type Side = "buy" | "sell";

/** One leg of a package being built. */
export interface PackageLeg {
  inst: string;
  kind: "call" | "put" | "perp";
  strike: number;
  expiry: number;
  side: Side;
  /** Units of the underlying per one package. */
  ratio: number;
}

export type Strategy =
  | "call" | "put" | "straddle" | "strangle" | "callSpread" | "putSpread" | "riskReversal" | "butterfly" | "ironCondor" | "calendar";

export const STRATEGIES: Strategy[] = ["call", "put", "straddle", "strangle", "callSpread", "putSpread", "riskReversal", "butterfly", "ironCondor", "calendar"];

/** Finds the strike line whose call (or put) delta is nearest target. */
function byDelta(rows: StrikeRow[], kind: "call" | "put", target: number): StrikeRow | undefined {
  let best: StrikeRow | undefined;
  for (const row of rows) {
    const option = kind === "call" ? row.call : row.put;
    if (!option || option.delta === 0) continue;
    if (!best || Math.abs(option.delta - target) < Math.abs((kind === "call" ? best.call! : best.put!).delta - target)) best = row;
  }
  return best;
}

function leg(row: StrikeRow | undefined, kind: "call" | "put", side: Side, ratio = 1): PackageLeg | undefined {
  const option = kind === "call" ? row?.call : row?.put;
  if (!row || !option) return undefined;
  return { inst: option.inst, kind, strike: row.strike, expiry: option.expiry, side, ratio };
}

/**
 * A strategy's legs, long by default (side "sell" flips every leg). Wings
 * are chosen by delta — 25-delta for strangles, risk reversals and condor
 * shorts, 10-delta for condor wings, 40/25 for vertical spreads — the way
 * desks quote them, so the shape is the same on any underlying.
 */
export function buildStrategy(strategy: Strategy, rows: StrikeRow[], forward: number, side: Side, nextRows?: StrikeRow[]): PackageLeg[] {
  if (rows.length === 0) return [];
  const atm = rows[atmIndex(rows, forward)];
  const flip = (item: PackageLeg | undefined): PackageLeg | undefined => item && (side === "sell" ? { ...item, side: item.side === "buy" ? "sell" : "buy" } : item);
  let legs: Array<PackageLeg | undefined>;
  switch (strategy) {
    case "call":
      legs = [leg(atm, "call", "buy")];
      break;
    case "put":
      legs = [leg(atm, "put", "buy")];
      break;
    case "straddle":
      legs = [leg(atm, "call", "buy"), leg(atm, "put", "buy")];
      break;
    case "strangle":
      legs = [leg(byDelta(rows, "call", 0.25), "call", "buy"), leg(byDelta(rows, "put", -0.25), "put", "buy")];
      break;
    case "callSpread":
      legs = [leg(byDelta(rows, "call", 0.4), "call", "buy"), leg(byDelta(rows, "call", 0.25), "call", "sell")];
      break;
    case "putSpread":
      legs = [leg(byDelta(rows, "put", -0.4), "put", "buy"), leg(byDelta(rows, "put", -0.25), "put", "sell")];
      break;
    case "riskReversal":
      legs = [leg(byDelta(rows, "call", 0.25), "call", "buy"), leg(byDelta(rows, "put", -0.25), "put", "sell")];
      break;
    case "butterfly": {
      const index = rows.indexOf(atm!);
      const wing = Math.max(1, Math.round(rows.length / 12));
      legs = [leg(rows[Math.max(0, index - wing)], "call", "buy"), leg(atm, "call", "sell", 2), leg(rows[Math.min(rows.length - 1, index + wing)], "call", "buy")];
      break;
    }
    case "ironCondor":
      // Long the condor: short the 25-delta strangle, long the 10-delta wings.
      legs = [leg(byDelta(rows, "put", -0.1), "put", "buy"), leg(byDelta(rows, "put", -0.25), "put", "sell"),
        leg(byDelta(rows, "call", 0.25), "call", "sell"), leg(byDelta(rows, "call", 0.1), "call", "buy")];
      break;
    case "calendar": {
      // Long the calendar: sell the front ATM call, buy the next expiry's.
      const next = nextRows && nextRows.length ? nextRows[atmIndex(nextRows, forward)] : undefined;
      const back = next && next.strike === atm!.strike ? next : nextRows?.find((row) => row.strike === atm!.strike);
      legs = [leg(atm, "call", "sell"), leg(back, "call", "buy")];
      break;
    }
  }
  const out = legs.map(flip).filter((item): item is PackageLeg => !!item);
  // A preset whose strikes collapsed (thin chain) is not that strategy.
  const keys = new Set(out.map((item) => item.inst));
  return keys.size === out.length ? out : [];
}

/** Adds a leg, netting it against the same instrument already in the package. */
export function addLeg(legs: PackageLeg[], next: PackageLeg): PackageLeg[] {
  const existing = legs.find((item) => item.inst === next.inst);
  if (!existing) return [...legs, next];
  const signed = (item: PackageLeg) => (item.side === "buy" ? item.ratio : -item.ratio);
  const total = signed(existing) + signed(next);
  if (total === 0) return legs.filter((item) => item.inst !== next.inst);
  return legs.map((item) => (item.inst === next.inst ? { ...item, side: total > 0 ? "buy" : "sell", ratio: Math.abs(total) } : item));
}

// ---------------------------------------------------------------------------
// Package valuation
// ---------------------------------------------------------------------------

/** What a leg needs to be valued: the chain's quote and the expiry's forward. */
export interface Quoted {
  leg: PackageLeg;
  row?: OptionRow;
  forward: number;
  /** Price paid or received per unit, in the strike currency. */
  entryUsd: number;
}

export interface PackageSummary {
  /** Premium per package in USD: positive is paid, negative received. */
  premiumUsd: number;
  greeks: Greeks;
  /** Payoff points: spot, value at expiry and value today, in USD. */
  curve: Array<{ spot: number; expiry: number; today: number }>;
  breakevens: number[];
  maxGain: number;
  maxLoss: number;
  /** Unbounded on the upside or downside within the curve's model. */
  unboundedGain: boolean;
  unboundedLoss: boolean;
}

/**
 * The package's net premium, greeks and payoff for size units of the
 * underlying. The curve spans ±span of the forward; "expiry" is the value
 * when the first leg expires (later legs valued at their IV then), "today"
 * at current IVs.
 */
export function summarize(quoted: Quoted[], size: number, now: number, span = 0.3, points = 121): PackageSummary {
  const sign = (side: Side) => (side === "buy" ? 1 : -1);
  const forward = quoted.find((item) => item.forward > 0)?.forward ?? 0;
  const first = Math.min(...quoted.map((item) => item.leg.expiry));
  const total: Greeks = { delta: 0, gamma: 0, vega: 0, theta: 0 };
  let premiumUsd = 0;
  for (const item of quoted) {
    const units = sign(item.leg.side) * item.leg.ratio * size;
    premiumUsd += units * item.entryUsd;
    if (item.leg.kind === "perp") {
      total.delta += units;
      continue;
    }
    const value = greeks(item.leg.kind === "call", item.forward, item.leg.strike, years(item.leg.expiry, now), item.row?.markIv ?? 0);
    total.delta += units * value.delta;
    total.gamma += units * value.gamma;
    total.vega += units * value.vega;
    total.theta += units * value.theta;
  }
  const value = (spot: number, at: number) => {
    let sum = 0;
    for (const item of quoted) {
      const units = sign(item.leg.side) * item.leg.ratio * size;
      if (item.leg.kind === "perp") {
        sum += units * (spot - item.entryUsd);
        continue;
      }
      // Forwards move with spot; the expiry's basis is kept.
      const legForward = forward > 0 ? (item.forward * spot) / forward : spot;
      const iv = item.row?.markIv ?? 0;
      sum += units * (price(item.leg.kind === "call", legForward, item.leg.strike, years(item.leg.expiry, at), iv) - item.entryUsd);
    }
    return sum;
  };
  const curve: PackageSummary["curve"] = [];
  if (forward > 0 && quoted.length) {
    for (let i = 0; i < points; i++) {
      const spot = forward * (1 - span + (2 * span * i) / (points - 1));
      curve.push({ spot, expiry: value(spot, first), today: value(spot, now) });
    }
  }
  const breakevens: number[] = [];
  for (let i = 1; i < curve.length; i++) {
    const a = curve[i - 1]!;
    const b = curve[i]!;
    if (a.expiry === 0 || Math.sign(a.expiry) !== Math.sign(b.expiry)) {
      if (a.expiry !== b.expiry) breakevens.push(a.spot + ((0 - a.expiry) / (b.expiry - a.expiry)) * (b.spot - a.spot));
    }
  }
  const values = curve.map((point) => point.expiry);
  const edgeSlope = (from: number, to: number) => (curve.length > 1 ? curve[to]!.expiry - curve[from]!.expiry : 0);
  const lowSlope = edgeSlope(1, 0);
  const highSlope = edgeSlope(curve.length - 2, curve.length - 1);
  const tolerance = Math.max(1e-6, Math.abs(premiumUsd) * 1e-6);
  return {
    premiumUsd, greeks: total, curve,
    breakevens: dedupe(breakevens, forward * 0.002),
    maxGain: values.length ? Math.max(...values) : 0,
    maxLoss: values.length ? Math.min(...values) : 0,
    unboundedGain: lowSlope > tolerance || highSlope > tolerance,
    unboundedLoss: lowSlope < -tolerance || highSlope < -tolerance,
  };
}

function dedupe(values: number[], within: number): number[] {
  const out: number[] = [];
  for (const value of values) if (!out.some((existing) => Math.abs(existing - value) < within)) out.push(value);
  return out;
}

/** A row's premium in USD per unit: inverse premiums are coin. */
export function premiumUsd(value: string | undefined, inverse: boolean, forward: number): number {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return 0;
  return inverse ? n * forward : n;
}

/** Mid of bid and ask, else the mark. */
export function mid(row: OptionRow | undefined): number {
  if (!row) return 0;
  const bid = Number(row.bid);
  const ask = Number(row.ask);
  if (bid > 0 && ask > 0) return (bid + ask) / 2;
  return Number(row.mark) || 0;
}

/** The touch a leg trades at: ask to buy, bid to sell. */
export function touch(row: OptionRow | undefined, side: Side): number {
  if (!row) return 0;
  return Number(side === "buy" ? row.ask : row.bid) || 0;
}
