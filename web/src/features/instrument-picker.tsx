import * as Popover from "@radix-ui/react-popover";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import { api } from "@/api/client";
import type { Instrument, MarketRow } from "@/api/types";
import { useT, type T } from "@/i18n";
import { compact, pct, price, venueLabel } from "@/lib/format";
import { accountName, tradingAccounts } from "@/lib/routes";
import { useInstrument, useInstruments } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";

import { DEFAULT_FILTERS, classesOf, filterAndSort, loadFavorites, quotesOf, saveFavorites, type Category, type Filters, type ListItem, type SortKey } from "./market-list";

/** The market list refreshes with the gateway's 30 s reads. */
function useMarketRows(enabled: boolean) {
  return useQuery({ queryKey: ["markets"], queryFn: ({ signal }) => api.markets(signal), enabled, staleTime: 25_000, refetchInterval: enabled ? 30_000 : false });
}

const LIMIT = 300;
const COLUMNS = "grid-cols-[22px_minmax(150px,1fr)_96px_110px_80px_84px_84px_84px]";

/**
 * The market picker over every venue: search ("binance btc", "eth 永续"),
 * category tabs, venue / quote / class filters, sortable price, 24h change,
 * 24h value, funding APR and open interest columns, and favorites. The
 * Trade page opens it with ⌘K; the hedge legs reuse it.
 */
export function InstrumentPicker({ value: instrumentId, onChange, hotkey, small }: {
  value: string;
  onChange: (id: string) => void;
  hotkey?: boolean;
  small?: boolean;
}) {
  const t = useT();
  const { accounts, routes } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const { data: instruments = [] } = useInstruments();
  const [open, setOpen] = useState(false);
  const { data: rows = [] } = useMarketRows(open);
  const [filters, setFilters] = useState<Filters>(DEFAULT_FILTERS);
  const [favorites, setFavorites] = useState<Set<string>>(loadFavorites);
  const [cursor, setCursor] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const set = (patch: Partial<Filters>) => setFilters((current) => ({ ...current, ...patch }));

  useEffect(() => {
    if (!hotkey) return;
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [hotkey]);

  // Spot and perpetuals only: options have their own page.
  const markets = useMemo(() => instruments.filter((item) => item.product === "perp" || item.product === "spot"), [instruments]);
  const venues = useMemo(() => [...new Set(markets.map((item) => item.venue))], [markets]);
  const quotes = useMemo(() => quotesOf(markets).slice(0, 12), [markets]);
  const classes = useMemo(() => classesOf(markets), [markets]);
  const byId = useMemo(() => new Map(rows.map((row) => [row.inst, row])), [rows]);
  const items = useMemo<ListItem[]>(() => markets.map((item) => ({ instrument: item, row: byId.get(item.id) })), [markets, byId]);
  // Which connected accounts trade each market: a dot and the tradable tab.
  const traders = useMemo(() => {
    const out = new Map<string, string[]>();
    if (!accounts.length) return out;
    for (const item of markets) {
      const names = tradingAccounts(accounts, item, routes).map(accountName);
      if (names.length) out.set(item.id, names);
    }
    return out;
  }, [markets, accounts, routes]);
  const tradable = useMemo(() => new Set(traders.keys()), [traders]);
  const all = useMemo(() => (open ? filterAndSort(items, filters, { favorites, tradable, venues }) : []), [open, items, filters, favorites, tradable, venues]);
  const shown = all.slice(0, LIMIT);

  useEffect(() => setCursor(0), [filters]);
  useEffect(() => {
    listRef.current?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }, [cursor]);

  const choose = (item: Instrument | undefined) => {
    if (!item) return;
    onChange(item.id);
    setOpen(false);
    set({ search: "" });
  };
  const toggleFavorite = (id: string) => {
    setFavorites((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      saveFavorites(next);
      return next;
    });
  };
  const sortBy = (sort: SortKey) => set(filters.sort === sort ? { descending: !filters.descending } : { sort, descending: sort !== "symbol" });

  const tabs: Array<[Category | "tradable", string]> = [
    ["favorites", "☆ " + t("picker.favorites")],
    ["all", t("common.all")],
    ["perp", t("common.perps")],
    ["spot", t("common.spot")],
  ];

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className="flex shrink-0 items-center gap-2 text-left">
        <span className={cx(small ? "text-md" : "text-lg", "whitespace-nowrap")}>{instrument?.symbol ?? instrumentId.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{open ? "▴" : "▾"}</span>
        <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-accent">{venueLabel(instrument?.venue ?? "")} · {t(instrument?.product === "spot" ? "common.spot" : "common.perp")}</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={8} className="z-40 w-[min(860px,calc(100vw-16px))] rounded-[10px] border border-line bg-overlay shadow-2xl">
          <div className="p-3 pb-2">
            <input
              autoFocus
              value={filters.search}
              onChange={(event) => set({ search: event.target.value })}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") { event.preventDefault(); setCursor((c) => Math.min(c + 1, shown.length - 1)); }
                if (event.key === "ArrowUp") { event.preventDefault(); setCursor((c) => Math.max(c - 1, 0)); }
                if (event.key === "Enter") choose(shown[cursor]?.instrument);
              }}
              placeholder={t("picker.searchHint")}
              className="h-9 w-full rounded-lg border border-line-strong bg-transparent px-3 outline-none placeholder:text-muted"
            />
          </div>
          <div className="flex items-center gap-1 px-3">
            {tabs.map(([value, label]) => (
              <button key={value} onClick={() => set({ category: value as Category })} className={cx("rounded-md px-2 py-1 whitespace-nowrap", filters.category === value ? "bg-raised text-text" : "text-muted hover:text-text")}>
                {label}
              </button>
            ))}
            {accounts.length > 0 && (
              <label className="ml-2 flex cursor-pointer items-center gap-1.5 whitespace-nowrap text-muted hover:text-text">
                <input type="checkbox" checked={filters.tradable} onChange={(event) => set({ tradable: event.target.checked })} className="accent-[var(--color-accent)]" />
                {t("picker.tradableOnly")}
              </label>
            )}
            <div className="ml-auto flex gap-1.5">
              <FilterSelect label={t("picker.venue")} value={filters.venue} onChange={(venue) => set({ venue })} options={venues.map((v) => [v, venueLabel(v)])} />
              <FilterSelect label={t("picker.quote")} value={filters.quote} onChange={(quote) => set({ quote })} options={quotes.map((q) => [q, q])} />
              <FilterSelect label={t("picker.class")} value={filters.assetClass} onChange={(assetClass) => set({ assetClass })} options={classes.map((c) => [c, c === "crypto" ? t("picker.crypto") : assetClass(t, c)])} />
            </div>
          </div>
          <div className={cx("mt-2 grid gap-2 border-b border-line px-3 py-1.5 text-muted", COLUMNS)}>
            <span />
            <SortHeader label={t("picker.market")} sort="symbol" filters={filters} onSort={sortBy} />
            <span>{t("picker.venue")}</span>
            <span className="text-right">{t("market.price")}</span>
            <SortHeader label={t("picker.change")} sort="change" filters={filters} onSort={sortBy} right />
            <SortHeader label={t("picker.volume")} sort="volume" filters={filters} onSort={sortBy} right hint={t("picker.volumeHint")} />
            <SortHeader label={t("picker.fundingApr")} sort="funding" filters={filters} onSort={sortBy} right />
            <SortHeader label={t("picker.oi")} sort="oi" filters={filters} onSort={sortBy} right hint={t("picker.oiHint")} />
          </div>
          <div ref={listRef} className="h-[min(56vh,520px)] overflow-y-auto pb-1">
            {shown.map(({ instrument: item, row }, index) => (
              <PickerRow
                key={item.id}
                index={index}
                item={item}
                row={row}
                active={index === cursor}
                current={item.id === instrumentId}
                favorite={favorites.has(item.id)}
                traders={traders.get(item.id)}
                onHover={() => setCursor(index)}
                onChoose={() => choose(item)}
                onFavorite={() => toggleFavorite(item.id)}
                t={t}
              />
            ))}
            {shown.length === 0 && (
              <div className="px-3 py-6 text-center text-muted">{filters.category === "favorites" && favorites.size === 0 ? t("picker.noFavorites") : t("picker.none")}</div>
            )}
            {all.length > LIMIT && <div className="px-3 py-2 text-center text-muted">{t("picker.more", { shown: LIMIT, total: all.length })}</div>}
          </div>
          <div className="flex gap-4 border-t border-line px-3 py-2 text-muted">
            {hotkey && <span><kbd className="text-text">⌘K</kbd> {t("picker.open")}</span>}
            <span><kbd className="text-text">↑↓</kbd> {t("picker.navigate")}</span>
            <span><kbd className="text-text">Enter</kbd> {t("picker.select")}</span>
            <span><kbd className="text-text">Esc</kbd> {t("picker.escape")}</span>
            <span className="ml-auto">{t("picker.count", { count: all.length })}</span>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function PickerRow({ index, item, row, active, current, favorite, traders, onHover, onChoose, onFavorite, t }: {
  index: number;
  item: Instrument;
  row?: MarketRow;
  active: boolean;
  current: boolean;
  favorite: boolean;
  traders?: string[];
  onHover: () => void;
  onChoose: () => void;
  onFavorite: () => void;
  t: T;
}) {
  const change = row?.change24h;
  const apr = row?.fundingApr === undefined ? undefined : Number(row.fundingApr);
  return (
    <div
      role="option"
      aria-selected={active}
      data-index={index}
      onMouseEnter={onHover}
      onClick={onChoose}
      className={cx("grid cursor-pointer items-center gap-2 px-3 py-1.5 tabular-nums", COLUMNS, active && "bg-raised", current && "text-accent")}
    >
      <button
        onClick={(event) => { event.stopPropagation(); onFavorite(); }}
        aria-label={t(favorite ? "picker.unfavorite" : "picker.favorite")}
        title={t(favorite ? "picker.unfavorite" : "picker.favorite")}
        className={cx("text-center", favorite ? "text-accent" : "text-faint hover:text-text-2")}
      >
        {favorite ? "★" : "☆"}
      </button>
      <span className="truncate">
        {item.symbol}
        <span className="ml-1.5 text-muted">{t(item.product === "perp" ? "common.perp" : "common.spot")}</span>
        {item.assetClass && item.assetClass !== "crypto" && <span className="ml-1.5 text-muted">{assetClass(t, item.assetClass)}</span>}
      </span>
      <span className="flex items-center gap-1.5 text-text-2">
        {venueLabel(item.venue)}
        {traders && <span className="size-1.5 rounded-full bg-up" title={t("picker.tradableWith", { names: traders.join(", ") })} aria-label={t("picker.tradable")} />}
      </span>
      <span className="text-right">{row?.last ? price(row.last, item.tickSize) : "—"}</span>
      <span className={cx("text-right", change !== undefined && change > 0 && "text-up", change !== undefined && change < 0 && "text-down")}>
        {change === undefined ? (row?.last ? "0.00%" : "—") : pct(change)}
      </span>
      <span className="text-right text-text-2">{row?.volume24h ? compact(row.volume24h) : "—"}</span>
      <span className={cx("text-right", apr !== undefined && apr > 0 && "text-up", apr !== undefined && apr < 0 && "text-down")}>
        {apr === undefined ? "—" : pct(apr, 1)}
      </span>
      <span className="text-right text-text-2">{row?.oiUsd ? compact(row.oiUsd) : "—"}</span>
    </div>
  );
}

function SortHeader({ label, sort, filters, onSort, right, hint }: { label: string; sort: SortKey; filters: Filters; onSort: (sort: SortKey) => void; right?: boolean; hint?: string }) {
  const active = filters.sort === sort;
  return (
    <button onClick={() => onSort(sort)} title={hint} className={cx("whitespace-nowrap hover:text-text", right && "text-right", active && "text-text", hint && "dotted")}>
      {label}{active ? (filters.descending ? " ↓" : " ↑") : ""}
    </button>
  );
}

function FilterSelect({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: Array<[string, string]> }) {
  return (
    <select
      value={value}
      onChange={(event) => onChange(event.target.value)}
      aria-label={label}
      className={cx("h-7 rounded-md bg-raised px-2 outline-none", value ? "text-text" : "text-muted")}
    >
      <option value="">{label}</option>
      {options.map(([key, text]) => <option key={key} value={key}>{text}</option>)}
    </select>
  );
}

/** A non-crypto market's asset class ("Stock", "大宗商品"); unknown classes pass through. */
export function assetClass(t: T, value: string): string {
  const key = `asset.${value}` as Parameters<T>[0];
  const text = t(key);
  return text === key ? value : text;
}
