import { describe, expect, it } from "vitest";

import { migrateStorage } from "./storage";

class Memory implements Storage {
  private data = new Map<string, string>();
  get length() { return this.data.size; }
  clear() { this.data.clear(); }
  getItem(key: string) { return this.data.get(key) ?? null; }
  key(index: number) { return [...this.data.keys()][index] ?? null; }
  removeItem(key: string) { this.data.delete(key); }
  setItem(key: string, value: string) { this.data.set(key, value); }
}

describe("migrateStorage", () => {
  it("moves bolter.* keys to keeluni.* once, keeping newer values", () => {
    const store = new Memory();
    store.setItem("bolter.instrument", "okx:perp:BTC-USDT-SWAP");
    store.setItem("bolter.drawings.binance:perp:BTC-USDT", "[1]");
    store.setItem("keeluni.interval", "1h");
    store.setItem("bolter.interval", "15m");
    store.setItem("other", "x");
    migrateStorage(store);
    expect(store.getItem("keeluni.instrument")).toBe("okx:perp:BTC-USDT-SWAP");
    expect(store.getItem("keeluni.drawings.binance:perp:BTC-USDT")).toBe("[1]");
    expect(store.getItem("keeluni.interval")).toBe("1h");
    expect(store.getItem("bolter.instrument")).toBeNull();
    expect(store.getItem("other")).toBe("x");
  });
});
