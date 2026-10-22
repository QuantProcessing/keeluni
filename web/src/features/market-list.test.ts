import { describe, expect, it } from "vitest";

import type { Instrument, MarketRow } from "@/api/types";
import { DEFAULT_FILTERS, classesOf, filterAndSort, parseSearch, quotesOf, type ListItem } from "./market-list";

const instrument = (venue: string, product: "perp" | "spot", base: string, quote: string, assetClass?: string): Instrument => ({
  id: `${venue}:${product}:${base}-${quote}`, venue, product, symbol: `${base}-${quote}`, base, quote, settle: product === "perp" ? quote : undefined,
  tickSize: "0.1", stepSize: "0.001", minQty: "0.001", assetClass,
});

const item = (inst: Instrument, row?: Partial<MarketRow>): ListItem => ({ instrument: inst, row: row ? { inst: inst.id, ...row } : undefined });

const venues = ["binance", "okx", "hyperliquid"];
const items: ListItem[] = [
  item(instrument("binance", "perp", "BTC", "USDT"), { volume24h: 11e9, change24h: 0.02, fundingApr: "0.11", oiUsd: 5e9 }),
  item(instrument("okx", "perp", "BTC", "USDT"), { volume24h: 6e9, change24h: 0.021, fundingApr: "0.03" }),
  item(instrument("hyperliquid", "perp", "BTCB", "USDC"), { volume24h: 1e6, change24h: -0.05 }),
  item(instrument("binance", "spot", "ETH", "BTC"), { volume24h: 3e6, change24h: 0.01 }),
  item(instrument("hyperliquid", "perp", "TSLA", "USDC", "equity"), { volume24h: 2e8 }),
  item(instrument("okx", "spot", "SOL", "USDT")),
];
const context = { favorites: new Set<string>(), tradable: new Set<string>(), venues };

describe("market list", () => {
  it("sorts by 24h value by default, rows without data last", () => {
    expect(filterAndSort(items, DEFAULT_FILTERS, context).map((x) => x.instrument.id)).toEqual([
      "binance:perp:BTC-USDT", "okx:perp:BTC-USDT", "hyperliquid:perp:TSLA-USDC", "binance:spot:ETH-BTC", "hyperliquid:perp:BTCB-USDC", "okx:spot:SOL-USDT",
    ]);
  });

  it("searches with venue and product words, exact base first", () => {
    expect(parseSearch("binance btc", venues)).toEqual({ venue: "binance", words: ["BTC"] });
    expect(parseSearch("btc 永续", venues)).toEqual({ product: "perp", words: ["BTC"] });
    const hits = filterAndSort(items, { ...DEFAULT_FILTERS, search: "btc" }, context).map((x) => x.instrument.id);
    // ETH-BTC matches by quote; BTCB by prefix after the exact BTC bases.
    expect(hits).toEqual(["binance:perp:BTC-USDT", "okx:perp:BTC-USDT", "hyperliquid:perp:BTCB-USDC", "binance:spot:ETH-BTC"]);
    expect(filterAndSort(items, { ...DEFAULT_FILTERS, search: "okx btc" }, context).map((x) => x.instrument.id)).toEqual(["okx:perp:BTC-USDT"]);
  });

  it("filters by category, venue, quote, class, favorites and tradable", () => {
    const ids = (filters: Partial<typeof DEFAULT_FILTERS>, ctx = context) => filterAndSort(items, { ...DEFAULT_FILTERS, ...filters }, ctx).map((x) => x.instrument.id);
    expect(ids({ category: "spot" })).toEqual(["binance:spot:ETH-BTC", "okx:spot:SOL-USDT"]);
    expect(ids({ venue: "hyperliquid" })).toEqual(["hyperliquid:perp:TSLA-USDC", "hyperliquid:perp:BTCB-USDC"]);
    expect(ids({ quote: "USDC" })).toHaveLength(2);
    expect(ids({ assetClass: "equity" })).toEqual(["hyperliquid:perp:TSLA-USDC"]);
    expect(ids({ assetClass: "crypto" })).toHaveLength(5);
    expect(ids({ category: "favorites" }, { ...context, favorites: new Set(["okx:spot:SOL-USDT"]) })).toEqual(["okx:spot:SOL-USDT"]);
    expect(ids({ tradable: true }, { ...context, tradable: new Set(["okx:perp:BTC-USDT"]) })).toEqual(["okx:perp:BTC-USDT"]);
  });

  it("sorts by change, funding and symbol either way", () => {
    const ids = (sort: typeof DEFAULT_FILTERS.sort, descending: boolean) => filterAndSort(items, { ...DEFAULT_FILTERS, sort, descending }, context).map((x) => x.instrument.base);
    expect(ids("change", true).slice(0, 3)).toEqual(["BTC", "BTC", "ETH"]);
    expect(ids("change", false)[0]).toBe("BTCB");
    expect(ids("funding", true).slice(0, 2)).toEqual(["BTC", "BTC"]);
    expect(ids("symbol", false)[0]).toBe("BTC");
  });

  it("lists quotes by frequency and classes", () => {
    const list = items.map((x) => x.instrument);
    expect(quotesOf(list)[0]).toBe("USDT");
    expect(classesOf(list)).toEqual(["crypto", "equity"]);
  });
});
