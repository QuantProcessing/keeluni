import { describe, expect, it } from "vitest";

import type { Account, Instrument } from "@/api/types";
import { accountEquity, accountName, brokersReaching, canTrade, chooseAccount, marginLevel, marketOf, rulesFor, tradingAccounts } from "./routes";

const instrument = (id: string, extra: Partial<Instrument> = {}): Instrument => {
  const [venue = "", product = "perp", symbol = ""] = id.split(":");
  const [base = "", quote = ""] = symbol.split("-");
  return { id, venue, product: product as Instrument["product"], symbol, base, quote, settle: product === "perp" ? quote : undefined, tickSize: "0.1", stepSize: "0.01", minQty: "0.01", ...extra };
};

const binance: Account = { id: "acc_bn", venue: "binance", kind: "native", label: "main", createdAt: "2026-10-01", active: true, markets: ["binance:perp:USDT", "binance:perp:USDC", "binance:spot"] };
const kraken: Account = { id: "acc_kr", venue: "kraken", kind: "native", label: "kraken", createdAt: "2026-10-03", active: true, markets: ["kraken:perp:USD"] };
const crossex: Account = {
  id: "acc_cx", venue: "crossex", kind: "broker", label: "cx", createdAt: "2026-09-01", active: true,
  markets: ["binance:perp:USDT", "binance:perp:USDC", "okx:perp:USDT", "kraken:perp:USD"],
};

describe("routes", () => {
  it("names markets the way the gateway does", () => {
    expect(marketOf(instrument("binance:perp:BTC-USDT"))).toBe("binance:perp:USDT");
    expect(marketOf(instrument("binance:spot:BTC-USDT"))).toBe("binance:spot");
    expect(marketOf(instrument("okx:perp:BTC-USDT-SWAP", { quote: "USDT", settle: "USDT" }))).toBe("okx:perp:USDT");
    expect(marketOf(instrument("kraken:perp:BTC-USD"))).toBe("kraken:perp:USD");
  });

  it("lets native and broker accounts trade the same order book", () => {
    const btc = instrument("binance:perp:BTC-USDT");
    expect(canTrade(binance, btc)).toBe(true);
    expect(canTrade(crossex, btc)).toBe(true);
    expect(canTrade(crossex, instrument("binance:spot:BTC-USDT"))).toBe(false);
    expect(canTrade({ ...binance, active: false }, btc)).toBe(false);
  });

  it("asks a broker's routes whether it lists the instrument", () => {
    const doge = instrument("kraken:perp:DOGE-USD");
    expect(canTrade(crossex, doge)).toBe(true);
    expect(canTrade(crossex, doge, [{ inst: "kraken:perp:BTC-USD", tickSize: "1", stepSize: "1", minQty: "1" }])).toBe(false);
  });

  it("puts native accounts before brokers and remembers the user's choice", () => {
    const btc = instrument("kraken:perp:BTC-USD");
    const candidates = tradingAccounts([crossex, kraken, binance], btc);
    expect(candidates.map((a) => a.id)).toEqual(["acc_kr", "acc_cx"]);
    expect(chooseAccount(candidates, undefined)?.id).toBe("acc_kr");
    expect(chooseAccount(candidates, "acc_cx")?.id).toBe("acc_cx");
    // A remembered account that cannot trade this market is not used.
    expect(chooseAccount(candidates, "acc_bn")?.id).toBe("acc_kr");
    expect(chooseAccount(tradingAccounts([binance], btc), undefined)).toBeUndefined();
  });

  it("finds brokers that reach a market", () => {
    const venues = [
      { venue: "kraken", kind: "native", markets: ["kraken:perp:USD"] },
      { venue: "crossex", kind: "broker", markets: crossex.markets },
    ];
    expect(brokersReaching(venues, instrument("kraken:perp:BTC-USD"))).toEqual(["crossex"]);
    expect(brokersReaching(venues, instrument("deribit:perp:BTC-USDC"))).toEqual([]);
  });

  it("names accounts briefly", () => {
    expect(accountName(binance)).toBe("Binance · main");
    expect(accountName(kraken)).toBe("Kraken");
    expect(accountName({ venue: "crossex", label: "Gate CrossEx" })).toBe("Gate CrossEx");
  });

  it("uses the account's rules when it has a route", () => {
    const btc = instrument("binance:perp:BTC-USDT");
    expect(rulesFor(btc, undefined).stepSize).toBe("0.01");
    expect(rulesFor(btc, [{ inst: btc.id, tickSize: "0.1", stepSize: "0.001", minQty: "0.001", minNotional: "50" }])).toEqual(
      { tickSize: "0.1", stepSize: "0.001", minQty: "0.001", minNotional: "50" });
  });

  it("summarizes equity of native and broker accounts", () => {
    expect(accountEquity(binance, undefined)).toBeUndefined();
    expect(accountEquity(binance, { balances: [
      { market: "binance:perp:USDT", asset: "USDT", total: "100", available: "60", locked: "40" },
      { market: "binance:perp:USDT", asset: "USDC", total: "50", available: "50", locked: "0" },
      { market: "binance:spot", asset: "BTC", total: "1", available: "1", locked: "0" },
    ], positions: [{ inst: "binance:perp:BTC-USDT", side: "long", size: "1", entryPrice: "1", unrealizedPnl: "-5" }] })).toEqual({ total: 150, available: 110, unrealizedPnl: -5 });
    expect(accountEquity(crossex, { margin: { equity: "900", available: "700", unrealizedPnl: "3", ts: 0 } })).toEqual({ total: 900, available: 700, unrealizedPnl: 3 });
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
