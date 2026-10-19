import { describe, expect, it } from "vitest";

import type { Account, Instrument } from "@/api/types";
import { legVenueHeld } from "./groups";
import { aprText, averageApr, breakevenDays, bpsText, legAccount, legAvailable, netFundingApr, spreadBps } from "./model";

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
