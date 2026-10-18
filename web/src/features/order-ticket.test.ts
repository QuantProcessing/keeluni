import { describe, expect, it } from "vitest";

import { validate } from "./order-ticket";

describe("ticket validation follows the account's rules", () => {
  const native = { tickSize: "0.1", stepSize: "0.01", minQty: "0.01" };
  const crossex = { tickSize: "0.1", stepSize: "0.001", minQty: "0.001", minNotional: "50" };

  it("accepts a size only the broker allows", () => {
    expect(validate(native, "0.005", "market", "", 300)).toBe("Min size 0.01");
    expect(validate(crossex, "0.005", "market", "", 300)).toBeNull();
  });

  it("enforces the broker's minimum value", () => {
    expect(validate(crossex, "0.001", "limit", "40000", 40)).toBe("Min value 50");
  });

  it("waits for rules", () => {
    expect(validate(undefined, "1", "market", "", 1)).toBe("Loading market");
  });
});
