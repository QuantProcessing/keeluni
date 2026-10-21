import { describe, expect, it, vi } from "vitest";

import { ApiError } from "@/api/client";
import type { Order, Trigger } from "@/api/types";
import { submitOrder, submitTrigger, type SubmitDeps, type TriggerDeps } from "./submit";

const order = { inst: "binance:perp:BTC-USDT", side: "buy" as const, type: "market" as const, qty: "0.01" };
const filled: Order = { inst: order.inst, orderId: "1", side: "buy", type: "market", qty: "0.01", filled: "0.01", status: "filled" };

function deps(overrides: Partial<SubmitDeps>): SubmitDeps {
  return { place: vi.fn(), lookup: vi.fn(), sleep: async () => {}, ...overrides };
}

describe("submitOrder", () => {
  it("returns accepted orders directly", async () => {
    const d = deps({ place: vi.fn().mockResolvedValue({ status: "accepted", order: filled }) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "accepted", order: filled });
    expect(d.lookup).not.toHaveBeenCalled();
  });

  it("surfaces venue rejection text", async () => {
    const d = deps({ place: vi.fn().mockResolvedValue({ status: "rejected", category: "rejected", message: "Margin is insufficient.", venueMessage: "Margin is insufficient.", venueCode: "-2019" }) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "rejected", message: "Margin is insufficient. (-2019)" });
  });

  it("looks up an unknown outcome with the same client id and never re-places", async () => {
    const place = vi.fn().mockResolvedValue({ status: "unknown" });
    const lookup = vi.fn().mockResolvedValueOnce({ status: "unconfirmed" }).mockResolvedValueOnce({ status: "found", order: filled });
    const d = deps({ place, lookup });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "accepted", order: filled });
    expect(place).toHaveBeenCalledTimes(1);
    const clientOrderId = place.mock.calls[0]![1].clientOrderId;
    expect(lookup).toHaveBeenCalledWith("acc", order.inst, clientOrderId);
  });

  it("treats a network failure as unknown, not as rejected", async () => {
    const d = deps({ place: vi.fn().mockRejectedValue(new TypeError("Failed to fetch")), lookup: vi.fn().mockResolvedValue({ status: "unconfirmed" }) });
    const outcome = await submitOrder("acc", order, d);
    expect(outcome.kind).toBe("unknown");
    expect(d.lookup).toHaveBeenCalledTimes(3);
  });

  it("renders the live Binance -2019 body as the venue's text", async () => {
    // Exact body returned by the gateway for a real -2019 on 2026-10-16.
    const body = { status: "rejected", category: "rejected", venueCode: "-2019", venueMessage: "Margin is insufficient.", message: "Margin is insufficient." };
    const d = deps({ place: vi.fn().mockResolvedValue(body) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "rejected", message: "Margin is insufficient. (-2019)" });
  });

  it("treats a 4xx as definite", async () => {
    const d = deps({ place: vi.fn().mockRejectedValue(new ApiError(400, "INVALID_REQUEST", "qty must be positive")) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "rejected", message: "qty must be positive" });
    expect(d.lookup).not.toHaveBeenCalled();
  });
});

describe("submitTrigger", () => {
  const request = { inst: "binance:perp:BTC-USDT", side: "sell" as const, kind: "stop_loss" as const, triggerPrice: "60000", qty: "0.01" };
  const resting: Trigger = { inst: request.inst, orderId: "77", side: "sell", kind: "stop_loss", triggerPrice: "60000", priceType: "last", qty: "0.01", status: "open" };
  const triggerDeps = (overrides: Partial<TriggerDeps>): TriggerDeps => ({ place: vi.fn(), lookup: vi.fn(), sleep: async () => {}, ...overrides });

  it("returns an accepted trigger", async () => {
    const d = triggerDeps({ place: vi.fn().mockResolvedValue({ status: "accepted", trigger: resting }) });
    expect(await submitTrigger("acc", request, d)).toEqual({ kind: "accepted", trigger: resting });
  });

  it("looks an unknown outcome up by its client id and never re-places", async () => {
    const place = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    const lookup = vi.fn().mockRejectedValueOnce(new Error("busy")).mockResolvedValueOnce({ status: "found", trigger: resting });
    const d = triggerDeps({ place, lookup });
    expect(await submitTrigger("acc", request, d)).toEqual({ kind: "accepted", trigger: resting });
    expect(place).toHaveBeenCalledTimes(1);
    expect(lookup).toHaveBeenCalledWith("acc", request.inst, place.mock.calls[0]![1].clientOrderId);
  });

  it("reports not found as not placed, and a venue rejection with its code", async () => {
    const missing = triggerDeps({ place: vi.fn().mockResolvedValue({ status: "unknown" }), lookup: vi.fn().mockResolvedValue({ status: "not_found" }) });
    expect((await submitTrigger("acc", request, missing)).kind).toBe("rejected");
    const rejected = triggerDeps({ place: vi.fn().mockResolvedValue({ status: "rejected", message: "Order would immediately trigger.", venueCode: "-2021" }) });
    expect(await submitTrigger("acc", request, rejected)).toEqual({ kind: "rejected", message: "Order would immediately trigger. (-2021)" });
  });
});
