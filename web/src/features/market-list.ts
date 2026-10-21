// The market list behind the instrument picker: pure filtering, search and
// sorting, kept apart from the component so they can be tested.

import type { Instrument, MarketRow } from "@/api/types";

export type Category = "all" | "favorites" | "perp" | "spot";
export type SortKey = "volume" | "change" | "funding" | "oi" | "symbol";

export interface Filters {
  category: Category;
  /** Only markets a connected account can trade. */
  tradable: boolean;
  venue: string; // "" for all
  quote: string; // "" for all
  assetClass: string; // "" for all, "crypto" for no class
  search: string;
  sort: SortKey;
  descending: boolean;
}

export const DEFAULT_FILTERS: Filters = { category: "all", tradable: false, venue: "", quote: "", assetClass: "", search: "", sort: "volume", descending: true };

export interface ListItem {
  instrument: Instrument;
  row?: MarketRow;
}

/**
 * The search terms: words split on spaces and slashes. A word that names a
 * venue or a product ("binance", "perp", "现货") filters by it; the rest must
 * all match the symbol (base, quote or the whole symbol prefix).
 */
export function parseSearch(text: string, venues: string[]): { venue?: string; product?: string; words: string[] } {
  const out: { venue?: string; product?: string; words: string[] } = { words: [] };
  for (const raw of text.trim().toUpperCase().split(/[\s/]+/).filter(Boolean)) {
    const lower = raw.toLowerCase();
    if (venues.includes(lower)) out.venue = lower;
    else if (["PERP", "PERPS", "SWAP", "永续"].includes(raw)) out.product = "perp";
    else if (["SPOT", "现货"].includes(raw)) out.product = "spot";
    else out.words.push(raw);
  }
  return out;
}

function matches(instrument: Instrument, words: string[]): boolean {
  const symbol = instrument.symbol.toUpperCase();
  const base = instrument.base.toUpperCase();
  const quote = instrument.quote.toUpperCase();
  return words.every((word) => base.startsWith(word) || quote === word || symbol.startsWith(word) || symbol.includes(word.replace(/-/g, "")));
}

/** Rank of a search hit: exact base first, then base prefix, then the rest. */
function searchRank(instrument: Instrument, words: string[]): number {
  if (!words.length) return 0;
  const base = instrument.base.toUpperCase();
  if (base === words[0]) return 0;
  if (base.startsWith(words[0]!)) return 1;
  return 2;
}

export function filterAndSort(items: ListItem[], filters: Filters, context: { favorites: Set<string>; tradable: Set<string>; venues: string[] }): ListItem[] {
  const search = parseSearch(filters.search, context.venues);
  const venue = search.venue ?? filters.venue;
  const product = search.product ?? (filters.category === "perp" || filters.category === "spot" ? filters.category : "");
  const result = items.filter(({ instrument }) => {
    if (filters.category === "favorites" && !context.favorites.has(instrument.id)) return false;
    if (filters.tradable && !context.tradable.has(instrument.id)) return false;
    if (venue && instrument.venue !== venue) return false;
    if (product && instrument.product !== product) return false;
    if (filters.quote && (instrument.settle || instrument.quote).toUpperCase() !== filters.quote) return false;
    if (filters.assetClass) {
      const cls = instrument.assetClass && instrument.assetClass !== "crypto" ? instrument.assetClass : "crypto";
      if (cls !== filters.assetClass) return false;
    }
    return matches(instrument, search.words);
  });
  const sign = filters.descending ? -1 : 1;
  const value = (item: ListItem): number | string | undefined => {
    switch (filters.sort) {
      case "volume": return item.row?.volume24h;
      case "change": return item.row?.change24h;
      case "funding": return item.row?.fundingApr === undefined ? undefined : Number(item.row.fundingApr);
      case "oi": return item.row?.oiUsd;
      case "symbol": return item.instrument.symbol;
    }
  };
  return result.sort((a, b) => {
    // A search ranks exact base matches first, whatever the sort.
    const rank = searchRank(a.instrument, search.words) - searchRank(b.instrument, search.words);
    if (rank) return rank;
    const left = value(a), right = value(b);
    // Rows without the value go last in either direction.
    if (left === undefined && right === undefined) return a.instrument.symbol.localeCompare(b.instrument.symbol);
    if (left === undefined) return 1;
    if (right === undefined) return -1;
    if (typeof left === "string" || typeof right === "string") return sign * String(left).localeCompare(String(right));
    return sign * (left - right) || a.instrument.symbol.localeCompare(b.instrument.symbol);
  });
}

/** The quote / settlement assets present, most common first. */
export function quotesOf(instruments: Instrument[]): string[] {
  const counts = new Map<string, number>();
  for (const item of instruments) {
    const quote = (item.settle || item.quote).toUpperCase();
    counts.set(quote, (counts.get(quote) ?? 0) + 1);
  }
  return [...counts].sort((a, b) => b[1] - a[1]).map(([quote]) => quote);
}

/** The asset classes present ("crypto" for unclassified). */
export function classesOf(instruments: Instrument[]): string[] {
  const set = new Set<string>(["crypto"]);
  for (const item of instruments) if (item.assetClass && item.assetClass !== "crypto") set.add(item.assetClass);
  return [...set];
}

const FAVORITES_KEY = "keeluni.favorites";

export function loadFavorites(): Set<string> {
  try {
    const value = JSON.parse(localStorage.getItem(FAVORITES_KEY) ?? "[]");
    return new Set(Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []);
  } catch {
    return new Set();
  }
}

export function saveFavorites(favorites: Set<string>) {
  localStorage.setItem(FAVORITES_KEY, JSON.stringify([...favorites]));
}
