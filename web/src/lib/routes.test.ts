import { describe, expect, it } from "vitest";

import type { Account, Instrument } from "@/api/types";
import { alternativeAccounts, canTrade, marginLevel, marketOf, rulesFor } from "./routes";

const instrument = (id: string, extra: Partial<Instrument> = {}): Instrument => {
  const [venue = "", product = "perp", symbol = ""] = id.split(":");
  const [base = "", quote = ""] = symbol.split("-");
  return { id, venue, product: product as Instrument["product"], symbol, base, quote, settle: product === "perp" ? quote : undefined, tickSize: "0.1", stepSize: "0.01", minQty: "0.01", ...extra };
};

const binance: Account = { id: "acc_bn", venue: "binance", kind: "native", label: "main", createdAt: "", active: true, markets: ["binance:perp:USDT", "binance:spot"] };
const crossex: Account = {
  id: "acc_cx", venue: "crossex", kind: "broker", label: "cx", createdAt: "", active: true,
  markets: ["binance:perp:USDT", "binance:perp:USDC", "okx:perp:USDT", "kraken:perp:USD"],
};

describe("routes", () => {
  it("names markets the way the gateway does", () => {
    expect(marketOf(instrument("binance:perp:BTC-USDT"))).toBe("binance:perp:USDT");
    expect(marketOf(instrument("binance:spot:BTC-USDT"))).toBe("binance:spot");
    expect(marketOf(instrument("okx:perp:BTC-USDT-SWAP", { quote: "USDT", settle: "USDT" }))).toBe("okx:perp:USDT");
  });

  it("lets native and broker accounts trade the same order book", () => {
    const btc = instrument("binance:perp:BTC-USDT");
    expect(canTrade(binance, btc)).toBe(true);
    expect(canTrade(crossex, btc)).toBe(true);
    expect(canTrade(crossex, instrument("binance:spot:BTC-USDT"))).toBe(false);
  });

  it("keeps broker-only instruments away from native accounts", () => {
    const amzn = instrument("binance:perp:AMZN-USDT", { source: "crossex" });
    expect(canTrade(binance, amzn)).toBe(false);
    expect(canTrade(crossex, amzn)).toBe(true);
    expect(alternativeAccounts([binance, crossex], binance, amzn).map((a) => a.id)).toEqual(["acc_cx"]);
    expect(alternativeAccounts([binance, { ...crossex, active: false }], binance, amzn)).toEqual([]);
  });

  it("uses the account's rules when it has a route", () => {
    const btc = instrument("binance:perp:BTC-USDT");
    expect(rulesFor(btc, undefined).stepSize).toBe("0.01");
    expect(rulesFor(btc, [{ inst: btc.id, tickSize: "0.1", stepSize: "0.001", minQty: "0.001", minNotional: "50" }])).toEqual(
      { tickSize: "0.1", stepSize: "0.001", minQty: "0.001", minNotional: "50" });
  });

  it("classifies CrossEx margin rates", () => {
    expect(marginLevel(undefined, undefined)).toBe("ok");
    expect(marginLevel("0", "0")).toBe("ok");
    expect(marginLevel("2.4", "4.8")).toBe("ok");
    expect(marginLevel("1.05", "3")).toBe("caution");
    expect(marginLevel("0.95", "2")).toBe("restricted");
    expect(marginLevel("0.6", "1")).toBe("liquidation");
  });
});
