import { describe, expect, it } from "vitest";

import type { Account, Instrument } from "@/api/types";
import { legVenueHeld } from "./groups";
import { aprText, averageApr, breakevenDays, bpsText, flipPair, legAccount, legAvailable, legLeverage, marginCheck, netFundingApr, netFundingSeries, normalizePair, seriesStats, spreadBps, spreadSeries, type Pair } from "./model";

describe("leg accounts", () => {
  const instrument = { id: "okx:perp:BTC-USDT-SWAP", venue: "okx", product: "perp", base: "BTC", quote: "USDT", settle: "USDT" } as Instrument;
  const okx = { id: "a1", venue: "okx", kind: "native", label: "main", active: true, markets: ["okx:perp:USDT", "okx:spot"], createdAt: "1" } as Account;
  const cx = { id: "a2", venue: "crossex", kind: "broker", label: "cx", active: true, markets: ["okx:perp:USDT", "binance:perp:USDT"], createdAt: "2" } as Account;
  const bn = { id: "a3", venue: "binance", kind: "native", label: "bn", active: true, markets: ["binance:perp:USDT"], createdAt: "3" } as Account;
  it("keeps only the user's choice, and only while it trades the instrument", () => {
    expect(legAccount([okx, cx, bn], {}, instrument, "")).toBeUndefined();
    expect(legAccount([okx, cx, bn], {}, instrument, "a2")?.id).toBe("a2");
    expect(legAccount([okx, cx, bn], {}, instrument, "a3")).toBeUndefined();
    // A broker whose routes do not list the instrument cannot carry it.
    expect(legAccount([okx, cx], { a2: [] }, instrument, "a2")).toBeUndefined();
  });
  it("reads a broker's pool and a native account's margin asset", () => {
    expect(legAvailable(cx, instrument, { margin: { available: "1200" } as never }, "buy")).toEqual({ amount: 1200, asset: "USD" });
    expect(legAvailable(okx, instrument, { balances: [{ market: "okx:perp:USDT", asset: "USDT", total: "9", available: "7" } as never] }, "sell")).toEqual({ amount: 7, asset: "USDT" });
    expect(legAvailable(okx, instrument, {}, "buy")).toBeUndefined();
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

describe("legVenueHeld", () => {
  const leg = { product: "perp", accountLive: true, venueSize: "0.01", venueSide: "short", side: "sell" } as const;
  it("signs the venue position by the leg's direction", () => {
    expect(legVenueHeld(leg)).toBe(0.01);
    expect(legVenueHeld({ ...leg, venueSide: "long" })).toBe(-0.01);
    expect(legVenueHeld({ ...leg, venueSize: "0" })).toBe(0);
  });
  it("knows nothing for spot legs or a stream that is down", () => {
    expect(legVenueHeld({ ...leg, product: "spot" })).toBeUndefined();
    expect(legVenueHeld({ ...leg, accountLive: false })).toBeUndefined();
  });
});

describe("pair", () => {
  const pair = [{ inst: "a", accountId: "", side: "buy" }, { inst: "b", accountId: "", side: "buy" }] as Pair;
  it("is always one long and one short leg", () => {
    expect(normalizePair(pair).map((leg) => leg.side)).toEqual(["buy", "sell"]);
    expect(normalizePair(pair, [false, true]).map((leg) => leg.side)).toEqual(["sell", "buy"]);
    expect(flipPair(normalizePair(pair))!.map((leg) => leg.side)).toEqual(["sell", "buy"]);
    // A spot leg cannot become the short one.
    expect(flipPair(normalizePair(pair, [true, false]), [true, false])).toBeUndefined();
  });
});

describe("spread history", () => {
  const bars = (closes: Array<[number, string]>) => closes.map(([t, c]) => ({ t, c }));
  it("joins the legs by bar time", () => {
    const series = spreadSeries(bars([[1, "100"], [2, "100"], [3, "100"]]), bars([[2, "100.5"], [3, "99"], [4, "1"]]), "base", 1);
    expect(series.map((point) => point.t)).toEqual([2, 3]);
    expect(series[0]!.value).toBeCloseTo(50, 8);
    expect(series[1]!.value).toBeCloseTo(-100, 8);
    // 1000PEPE against PEPE: leg 2 is quoted per 1000.
    expect(spreadSeries(bars([[1, "0.00001"]]), bars([[1, "0.01001"]]), "base", 1000)[0]!.value).toBeCloseTo(10, 6);
    expect(spreadSeries(bars([[1, "2"]]), bars([[1, "3"]]), "notional", 0)[0]!.value).toBe(1.5);
    expect(seriesStats([{ t: 1, value: -2 }, { t: 2, value: 4 }])).toEqual({ last: 4, mean: 1, low: -2, high: 4 });
  });
  it("carries each leg's last settled APR into the net", () => {
    const net = netFundingSeries([
      { side: "buy", points: [{ t: 0, apr: "0.1" }, { t: 8, apr: "0.2" }] },
      { side: "sell", points: [{ t: 0, apr: "0.3" }, { t: 4, apr: "0.5" }, { t: 8, apr: "0.4" }] },
    ]);
    expect(net.map((point) => point.t)).toEqual([0, 4, 8]);
    expect(net[0]!.value).toBeCloseTo(0.2, 10);
    expect(net[1]!.value).toBeCloseTo(0.4, 10);
    expect(net[2]!.value).toBeCloseTo(0.2, 10);
  });
});

describe("leverage and margin", () => {
  it("uses the set leverage, else the position's, else 1x", () => {
    expect(legLeverage(false, { leverage: "10" } as never, undefined)).toEqual({ value: 10, source: "position" });
    expect(legLeverage(false, { leverage: "10" } as never, 3)).toEqual({ value: 3, source: "set" });
    expect(legLeverage(false, undefined, undefined)).toEqual({ value: 1, source: "default" });
    expect(legLeverage(true, { leverage: "5" } as never, 5).value).toBe(1);
  });
  it("adds both legs up against a shared pool", () => {
    const shared = marginCheck([
      { accountId: "cx", shared: true, notional: 10_000, leverage: 5, available: 2_500 },
      { accountId: "cx", shared: true, notional: 10_000, leverage: 10, available: 2_500 },
    ]);
    expect(shared.need).toEqual([2000, 1000]);
    expect(shared.short).toContain("pool");
    const separate = marginCheck([
      { accountId: "a", shared: false, notional: 1000, leverage: 1, available: 2000 },
      { accountId: "b", shared: false, notional: 1000, leverage: 1, available: 500 },
    ]);
    expect(separate.short).toContain("Leg 2");
  });
});
