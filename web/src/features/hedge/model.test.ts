import { describe, expect, it } from "vitest";

import { aprText, averageApr, breakevenDays, bpsText, inferMatch, netFundingApr, spreadBps, underlying } from "./model";

describe("underlying", () => {
  it("splits venue contract multipliers off the base", () => {
    expect(underlying("1000PEPE")).toEqual({ root: "PEPE", multiplier: 1000 });
    expect(underlying("kPEPE")).toEqual({ root: "PEPE", multiplier: 1000 });
    expect(underlying("1000000MOG")).toEqual({ root: "MOG", multiplier: 1_000_000 });
    expect(underlying("ETH")).toEqual({ root: "ETH", multiplier: 1 });
  });
});

describe("inferMatch", () => {
  it("matches the same underlying by quantity through the multiplier", () => {
    expect(inferMatch("ETH", "ETH")).toEqual({ match: "base", ratio: 1 });
    // 1 PEPE on leg 1 is hedged by 0.001 of 1000PEPE on leg 2.
    expect(inferMatch("PEPE", "1000PEPE")).toEqual({ match: "base", ratio: 0.001 });
    expect(inferMatch("1000PEPE", "kPEPE")).toEqual({ match: "base", ratio: 1 });
  });
  it("matches different underlyings by notional", () => {
    expect(inferMatch("BTC", "ETH")).toEqual({ match: "notional", ratio: 1 });
  });
});

describe("netFundingApr", () => {
  it("charges the long leg and pays the short leg", () => {
    // Long OKX at 10.43%, short Hyperliquid at 10.95%: earns 0.52%.
    expect(netFundingApr([{ side: "buy", apr: 0.1043 }, { side: "sell", apr: 0.1095 }])).toBeCloseTo(0.0052, 10);
    // Negative funding on the long leg is income.
    expect(netFundingApr([{ side: "buy", apr: -0.05 }, { side: "sell", apr: 0.02 }])).toBeCloseTo(0.07, 10);
  });
  it("ignores spot legs and refuses unknown periods", () => {
    expect(netFundingApr([{ side: "buy", spot: true }, { side: "sell", apr: 0.1 }])).toBeCloseTo(0.1, 10);
    expect(netFundingApr([{ side: "buy", apr: undefined }, { side: "sell", apr: 0.1 }])).toBeUndefined();
  });
});

describe("averages and costs", () => {
  it("averages settled APRs inside the window", () => {
    const points = [{ t: 1, rate: "0", apr: "0.1" }, { t: 10, rate: "0", apr: "0.2" }, { t: 20, rate: "0", apr: "0.4" }, { t: 30, rate: "0" }];
    expect(averageApr(points, 10)).toBeCloseTo(0.3, 10);
    expect(averageApr([], 0)).toBeUndefined();
  });
  it("computes spread and breakeven", () => {
    expect(spreadBps(100, 100.25, 1)).toBeCloseTo(25, 6);
    expect(spreadBps(0.00001, 0.01, 1000)).toBeCloseTo(0, 6);
    // 10 bps cost at 36.5% APR (10 bps/day) repays in one day.
    expect(breakevenDays(10, 0.365)).toBeCloseTo(1, 10);
    expect(breakevenDays(10, -0.1)).toBeUndefined();
  });
  it("formats signed values", () => {
    expect(bpsText(2.234)).toBe("+2.23 bps");
    expect(aprText(-0.1052)).toBe("−10.52%");
    expect(aprText(undefined)).toBe("—");
  });
});
