import { describe, expect, it } from "vitest";

import { en } from "./en";
import { translate } from "./index";
import { zh } from "./zh";

const placeholders = (text: string) => [...text.matchAll(/\{(\w+)\}/g)].map((match) => match[1]).sort();

// Values that read the same in both languages: names, codes, symbols.
const SAME = new Set(["nav.api", "asset.pre_ipo", "asset.rwa_other", "groups.orders.client",
  // Greek letters and order codes stay in Latin script in Chinese desks.
  "options.showIv", "options.tif.gtc", "options.tif.ioc", "options.risk.delta", "options.risk.gamma", "options.risk.vega", "options.risk.theta",
  "options.hedgeSent", "options.contractsCount"]);

describe("dictionaries", () => {
  it("translate every key with the same placeholders", () => {
    for (const key of Object.keys(en) as Array<keyof typeof en>) {
      const source = en[key];
      const target = zh[key];
      expect(typeof target, key).toBe(typeof source);
      if (typeof source === "string" && typeof target === "string") {
        expect(placeholders(target), key).toEqual(placeholders(source));
        if (!SAME.has(key)) expect(target, `${key} is not translated`).not.toBe(source);
      }
    }
  });

  it("fill placeholders and functions in either language", () => {
    expect(translate("accounts.count", { n: 3 }, "en")).toBe("3 Accounts");
    expect(translate("accounts.count", { n: 1 }, "en")).toBe("1 Account");
    expect(translate("accounts.count", { n: 3 }, "zh")).toBe("3 个账户");
    expect(translate("connect.titleVenue", { venue: "OKX" }, "zh")).toBe("连接 OKX");
    expect(translate("common.leg", { n: 2 }, "zh")).toBe("腿 2");
  });
});

import { HEDGER_CODES, hedgerText } from "./hedger";
// The hedger's own source, read as text at test time (Vite ?raw).
import hedgerSource from "../../../pairexec/text.go?raw";

describe("hedger texts", () => {
  // The hedger's own list of codes and English formats.
  const goFormats = Object.fromEntries(
    [...hedgerSource.matchAll(/^\t"([\w.]+)":\s+"((?:[^"\\]|\\.)*)",$/gm)].map((m) => [m[1]!, m[2]!]),
  );

  it("know every code the hedger sends, in both languages", () => {
    expect(Object.keys(goFormats).length).toBeGreaterThan(50);
    for (const [code, format] of Object.entries(goFormats)) {
      expect(HEDGER_CODES.en[code], code).toBe(format);
      expect(HEDGER_CODES.zh[code], code).toBeDefined();
      expect(placeholders(HEDGER_CODES.zh[code]!), code).toEqual(placeholders(format as string));
    }
  });

  it("render codes, reasons and venue words", () => {
    const text = {
      code: "failure.hedgeRejected", params: { leg: "2" }, message: "hedge on leg 2 rejected: Insufficient margin",
      reason: { message: "Insufficient margin" },
    };
    expect(hedgerText(text, "en")).toBe("hedge on leg 2 rejected: Insufficient margin");
    expect(hedgerText(text, "zh")).toBe("腿 2 的对冲单被拒：Insufficient margin");
    expect(hedgerText({ code: "event.orderState", params: { leg: "1", side: "sell", state: "rejected" }, message: "" }, "zh")).toBe("腿 1 卖出订单被拒");
    expect(hedgerText({ code: "event.late", params: { from: "open", to: "broken" }, message: "" }, "zh")).toBe("交易所延迟更新：状态 已对冲 → 未对冲");
    // Unknown code (a newer hedger) and old plain rows fall back to English.
    expect(hedgerText({ code: "event.future", message: "Something new" }, "zh")).toBe("Something new");
    expect(hedgerText("legs unbalanced", "zh")).toBe("legs unbalanced");
  });
});
