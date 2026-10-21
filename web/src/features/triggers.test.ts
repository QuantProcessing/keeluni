import { describe, expect, it } from "vitest";

import { triggerSideProblem, triggersOf } from "./triggers";

describe("trigger side check", () => {
  it("puts a long's take-profit above the price and its stop below", () => {
    expect(triggerSideProblem("take_profit", true, 110, 100)).toBeUndefined();
    expect(triggerSideProblem("take_profit", true, 95, 100)).toBe("above");
    expect(triggerSideProblem("stop_loss", true, 90, 100)).toBeUndefined();
    expect(triggerSideProblem("stop_loss", true, 100, 100)).toBe("below");
  });

  it("mirrors it for a short", () => {
    expect(triggerSideProblem("take_profit", false, 90, 100)).toBeUndefined();
    expect(triggerSideProblem("take_profit", false, 105, 100)).toBe("below");
    expect(triggerSideProblem("stop_loss", false, 110, 100)).toBeUndefined();
    expect(triggerSideProblem("stop_loss", false, 99, 100)).toBe("above");
  });

  it("does not judge without a reference price", () => {
    expect(triggerSideProblem("stop_loss", true, 90, NaN)).toBeUndefined();
  });

  it("matches a position's triggers by account, market and closing side", () => {
    const base = { inst: "binance:perp:BTC-USDT", orderId: "1", kind: "stop_loss" as const, triggerPrice: "1", priceType: "last" as const, status: "open" };
    const triggers = [
      { ...base, account: "a", side: "sell" as const },
      { ...base, account: "a", side: "buy" as const, orderId: "2" },
      { ...base, account: "b", side: "sell" as const, orderId: "3" },
    ];
    const long = { account: "a", inst: base.inst, side: "long" as const, size: "1", entryPrice: "1", unrealizedPnl: "0" };
    expect(triggersOf(triggers, long).map((item) => item.orderId)).toEqual(["1"]);
    expect(triggersOf(triggers, { ...long, side: "short" }).map((item) => item.orderId)).toEqual(["2"]);
  });
});
