import { describe, expect, it } from "vitest";

import type { OptionChain, OptionRow } from "@/api/options";
import { addLeg, buildStrategy, greeks, impliedVol, impliedVolOf, price, priceAt, strikeRows, summarize, visibleRows, years, type Quoted } from "./model";

const NOW = Date.UTC(2026, 9, 20);
const EXPIRY = Date.UTC(2026, 10, 27, 8);
const FORWARD = 82000;

function chain(): OptionChain {
  const t = years(EXPIRY, NOW);
  const rows: OptionRow[] = [];
  for (let strike = 60000; strike <= 104000; strike += 2000) {
    for (const kind of ["call", "put"] as const) {
      const value = price(kind === "call", FORWARD, strike, t, 0.45);
      const g = greeks(kind === "call", FORWARD, strike, t, 0.45);
      rows.push({ inst: `deribit:option:BTC-USD-20261127-${strike}-${kind === "call" ? "C" : "P"}`, kind, strike: String(strike), expiry: EXPIRY,
        bid: String(value * 0.98), ask: String(value * 1.02), mark: String(value), markIv: 0.45, ...g });
    }
  }
  return { series: "deribit:BTC:USDC", venue: "deribit", underlying: "BTC", premiumAsset: "USDC", strikeAsset: "USDC", updatedAt: NOW, stale: false,
    expiries: [{ expiry: EXPIRY, forward: FORWARD, atmIv: 0.45, strikes: 23 }], rows };
}

describe("black-76", () => {
  it("matches Deribit's published greeks for BTC-27NOV26-73000-P", () => {
    // Underlying 82032.7, mark IV 40.39%: delta -0.16925, vega 67.05, theta -35.36.
    const g = greeks(false, 82032.7, 73000, years(EXPIRY, 1792456815095), 0.4039);
    expect(g.delta).toBeCloseTo(-0.16925, 3);
    expect(g.vega).toBeCloseTo(67.05, 0);
    expect(g.theta).toBeCloseTo(-35.36, 0);
  });

  it("inverts its own prices and keeps put-call parity", () => {
    const t = 0.1;
    const value = price(true, 81000, 90000, t, 0.62);
    expect(impliedVol(true, 81000, 90000, t, value)).toBeCloseTo(0.62, 4);
    expect(price(true, 100, 95, t, 0.6) - price(false, 100, 95, t, 0.6)).toBeCloseTo(5, 4);
    expect(impliedVol(true, 100, 90, t, 9)).toBeUndefined();
  });
});

describe("chain and packages", () => {
  const rows = strikeRows(chain(), EXPIRY);

  it("lays strikes out with calls and puts side by side around the forward", () => {
    expect(rows).toHaveLength(23);
    expect(rows[0]?.call?.kind).toBe("call");
    const shown = visibleRows(rows, FORWARD, 3);
    expect(shown.map((row) => row.strike)).toEqual([76000, 78000, 80000, 82000, 84000, 86000, 88000]);
  });

  it("builds structures by delta, the way they are quoted", () => {
    const strangle = buildStrategy("strangle", rows, FORWARD, "buy");
    expect(strangle).toHaveLength(2);
    const call = strangle.find((leg) => leg.kind === "call")!;
    const put = strangle.find((leg) => leg.kind === "put")!;
    expect(call.strike).toBeGreaterThan(FORWARD);
    expect(put.strike).toBeLessThan(FORWARD);
    const condor = buildStrategy("ironCondor", rows, FORWARD, "buy");
    expect(condor.map((leg) => leg.side)).toEqual(["buy", "sell", "sell", "buy"]);
    const short = buildStrategy("straddle", rows, FORWARD, "sell");
    expect(short.every((leg) => leg.side === "sell" && leg.strike === 82000)).toBe(true);
  });

  it("nets a leg added twice and drops it when it cancels out", () => {
    const leg = { inst: "x", kind: "call" as const, strike: 1, expiry: EXPIRY, side: "buy" as const, ratio: 1 };
    expect(addLeg(addLeg([], leg), leg)[0]?.ratio).toBe(2);
    expect(addLeg([leg], { ...leg, side: "sell" })).toEqual([]);
  });

  it("values a call spread: capped both ways, one breakeven, debit paid", () => {
    const legs = buildStrategy("callSpread", rows, FORWARD, "buy");
    const byInst = new Map(chain().rows.map((row) => [row.inst, row]));
    const quoted: Quoted[] = legs.map((leg) => ({ leg, row: byInst.get(leg.inst), forward: FORWARD, entryUsd: Number(byInst.get(leg.inst)!.mark) }));
    const summary = summarize(quoted, 1, NOW);
    expect(summary.premiumUsd).toBeGreaterThan(0);
    expect(summary.unboundedGain).toBe(false);
    expect(summary.unboundedLoss).toBe(false);
    expect(summary.breakevens).toHaveLength(1);
    const width = legs[1]!.strike - legs[0]!.strike;
    expect(summary.maxGain).toBeCloseTo(width - summary.premiumUsd, 0);
    expect(summary.maxLoss).toBeCloseTo(-summary.premiumUsd, 0);
    expect(summary.greeks.delta).toBeGreaterThan(0);
  });

  it("flags a short straddle's unlimited loss", () => {
    const legs = buildStrategy("straddle", rows, FORWARD, "sell");
    const byInst = new Map(chain().rows.map((row) => [row.inst, row]));
    const quoted: Quoted[] = legs.map((leg) => ({ leg, row: byInst.get(leg.inst), forward: FORWARD, entryUsd: Number(byInst.get(leg.inst)!.mark) }));
    const summary = summarize(quoted, 1, NOW);
    expect(summary.premiumUsd).toBeLessThan(0);
    expect(summary.unboundedLoss).toBe(true);
    expect(summary.breakevens).toHaveLength(2);
    expect(summary.greeks.vega).toBeLessThan(0);
    expect(summary.greeks.theta).toBeGreaterThan(0);
  });
});

describe("pricing on the expiry's model", () => {
  it("round-trips a premium through its IV with and without a discount", () => {
    const t = 0.25;
    for (const discount of [1, Math.exp(-0.05 * t)]) {
      for (const [call, strike] of [[true, 90_000], [false, 75_000]] as const) {
        const value = priceAt(call, 82_000, strike, t, 0.42, discount);
        expect(impliedVolOf(call, 82_000, strike, t, value, discount)).toBeCloseTo(0.42, 5);
      }
    }
  });

  it("discounts the forward model", () => {
    const t = 0.5;
    const discount = Math.exp(-0.0531 * t);
    expect(priceAt(true, 82_000, 82_000, t, 0.4, discount)).toBeCloseTo(discount * priceAt(true, 82_000, 82_000, t, 0.4), 8);
  });
});
