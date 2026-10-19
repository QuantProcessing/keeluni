import * as Popover from "@radix-ui/react-popover";
import { useEffect, useMemo, useRef, useState } from "react";

import type { Instrument } from "@/api/types";
import { compact, countdown, pct, price, signed, venueLabel } from "@/lib/format";
import { accountName, tradingAccounts } from "@/lib/routes";
import { useInstrument, useInstruments, useLastPrice, useNow, useStats, useTicker } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { useT, type T } from "@/i18n";

export { venueLabel } from "@/lib/format";

export function MarketHeader() {
  const t = useT();
  const { instrumentId, setInstrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const perp = instrument?.product === "perp";
  const stats = useStats(instrumentId).data;
  const ticker = useTicker(instrumentId, perp);
  const last = useLastPrice(instrumentId);
  const now = useNow();

  const open = Number(stats?.open24h);
  const change = last && open ? Number(last) - open : undefined;
  const funding = ticker?.funding ?? stats?.funding;
  const fundingHours = ticker?.fundingIntervalHours ?? stats?.fundingIntervalHours;
  const fundingApr = ticker?.fundingApr ?? stats?.fundingApr;
  const nextFunding = ticker?.nextFunding ?? stats?.nextFunding;
  const tick = instrument?.tickSize;

  return (
    <div className="flex h-14 shrink-0 items-center gap-6 overflow-x-auto bg-surface px-3">
      <InstrumentPicker value={instrumentId} onChange={setInstrumentId} hotkey />
      <Stat label={t("market.price")} value={price(last, tick)} />
      {perp && <Stat label={t("market.mark")} hint={t("market.markHint")} value={price(ticker?.mark ?? stats?.mark, tick)} />}
      {perp && ticker?.index && <Stat label={t("market.index")} hint={t("market.indexHint")} value={price(ticker.index, tick)} />}
      <Stat
        label={t("market.change24h")}
        value={change === undefined ? "—" : `${signed(change, tick ? Math.min(8, tick.split(".")[1]?.length ?? 0) : 2)} / ${pct(change / open)}`}
        tone={change === undefined ? undefined : change >= 0 ? "up" : "down"}
      />
      <Stat label={t("market.volume24h")} value={stats ? compact(Number(stats.volume24h) * Number(last ?? 0)) + " " + (instrument?.quote ?? "") : "—"} />
      {perp && ticker?.oi && <Stat label={t("market.openInterest")} value={compact(ticker.oi) + " " + (instrument?.base ?? "")} />}
      {perp && (
        <div className="shrink-0">
          <div className="dotted text-muted" title={t("market.fundingHint")}>{t("market.funding")}</div>
          <div className="mt-1 flex gap-3">
            <span className={Number(funding) >= 0 ? "text-up" : "text-down"}>
              {fundingRate(funding)}{fundingHours ? <span className="text-muted"> /{fundingHours}h</span> : null}
            </span>
            <span className={Number(fundingApr) >= 0 ? "text-up" : "text-down"}>{fundingApr ? pct(Number(fundingApr)) : "—"}</span>
            <span>{countdown(nextFunding, now)}</span>
          </div>
        </div>
      )}
    </div>
  );
}

/** Funding per settlement, as a percentage with enough digits for hourly venues. */
export function fundingRate(value: string | undefined): string {
  if (!value) return "—";
  const percent = Number(value) * 100;
  return (Math.abs(percent) < 0.001 && percent !== 0 ? percent.toFixed(6) : percent.toFixed(4)) + "%";
}

function Stat({ label, value, tone, hint }: { label: string; value: string; tone?: "up" | "down"; hint?: string }) {
  return (
    <div className="shrink-0">
      <div className={cx("text-muted", hint && "dotted")} title={hint}>{label}</div>
      <div className={cx("mt-1", tone === "up" && "text-up", tone === "down" && "text-down")}>{value}</div>
    </div>
  );
}

type Category = "all" | "tradable" | "perp" | "spot" | string;

/**
 * Searchable picker over every venue's markets, marking those a connected
 * account trades. The Trade page opens it with ⌘K; the hedge legs reuse it.
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
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState<Category>("all");
  const [cursor, setCursor] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

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

  const venues = useMemo(() => [...new Set(instruments.map((item) => item.venue))], [instruments]);
  // Which connected accounts trade each market: shown, never used to hide one.
  const traders = useMemo(() => {
    const out = new Map<string, string[]>();
    if (!accounts.length) return out;
    for (const item of instruments) {
      const names = tradingAccounts(accounts, item, routes).map(accountName);
      if (names.length) out.set(item.id, names);
    }
    return out;
  }, [instruments, accounts, routes]);
  const filtered = useMemo(() => {
    const term = search.trim().toUpperCase().replace(/[\s/]+/g, "-");
    const result = instruments.filter((item) => {
      if (category === "tradable") {
        if (!traders.has(item.id)) return false;
      } else if (category === "perp" || category === "spot") {
        if (item.product !== category) return false;
      } else if (category !== "all" && item.venue !== category) return false;
      return !term || item.symbol.toUpperCase().includes(term) || item.base.toUpperCase().startsWith(term) || item.venue.toUpperCase().startsWith(term);
    });
    // Exact base matches, majors, perps, the current venue; tradable ones a little ahead.
    const currentVenue = instrument?.venue;
    const score = (item: Instrument) => rank(item, term, currentVenue) - (traders.has(item.id) ? 8 : 0);
    result.sort((a, b) => score(a) - score(b) || a.symbol.localeCompare(b.symbol));
    return result.slice(0, 200);
  }, [instruments, search, category, instrument?.venue, traders]);

  useEffect(() => setCursor(0), [search, category]);
  useEffect(() => {
    listRef.current?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }, [cursor]);

  const choose = (item: Instrument | undefined) => {
    if (!item) return;
    onChange(item.id);
    setOpen(false);
    setSearch("");
  };

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className="flex shrink-0 items-center gap-2 text-left">
        <span className={cx(small ? "text-md" : "text-lg", "whitespace-nowrap")}>{instrument?.symbol ?? instrumentId.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{open ? "▴" : "▾"}</span>
        <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-accent">{venueLabel(instrument?.venue ?? "")} · {t(instrument?.product === "spot" ? "common.spot" : "common.perp")}</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={8} className="z-40 w-[min(760px,calc(100vw-16px))] rounded-[10px] border border-line bg-overlay shadow-2xl">
          <div className="p-3">
            <input
              autoFocus
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") { event.preventDefault(); setCursor((c) => Math.min(c + 1, filtered.length - 1)); }
                if (event.key === "ArrowUp") { event.preventDefault(); setCursor((c) => Math.max(c - 1, 0)); }
                if (event.key === "Enter") choose(filtered[cursor]);
              }}
              placeholder={t("picker.search")}
              className="h-9 w-full rounded-lg border border-line-strong bg-transparent px-3 outline-none placeholder:text-muted"
            />
          </div>
          <div className="flex gap-1 overflow-x-auto px-3">
            {[["all", t("common.all")], ...(accounts.length ? [["tradable", t("picker.tradable")]] : []), ["perp", t("common.perps")], ["spot", t("common.spot")], ...venues.map((v) => [v, venueLabel(v)])].map(([value, label]) => (
              <button key={value} onClick={() => setCategory(value!)} className={cx("rounded-md px-2 py-1 whitespace-nowrap", category === value ? "bg-raised text-text" : "text-muted hover:text-text")}>
                {label}
              </button>
            ))}
          </div>
          <div className="mt-2 grid grid-cols-[1fr_120px_80px] gap-2 px-3 py-1.5 text-muted">
            <span>{t("picker.market")}</span><span>{t("picker.venue")}</span><span className="text-right">{t("picker.type")}</span>
          </div>
          <div ref={listRef} className="max-h-[50vh] overflow-y-auto pb-2">
            {filtered.map((item, index) => {
              const by = traders.get(item.id);
              return (
                <button
                  key={item.id}
                  data-index={index}
                  onMouseEnter={() => setCursor(index)}
                  onClick={() => choose(item)}
                  className={cx("grid w-full grid-cols-[1fr_120px_80px] gap-2 px-3 py-1.5 text-left", index === cursor && "bg-raised", item.id === instrumentId && "text-accent")}
                >
                  <span className="truncate">
                    {item.symbol}
                    {item.assetClass && item.assetClass !== "crypto" && <span className="ml-1.5 text-muted">{assetClass(t, item.assetClass)}</span>}
                  </span>
                  <span className="flex items-center gap-1.5 text-text-2">
                    {venueLabel(item.venue)}
                    {by && <span className="size-1.5 rounded-full bg-up" title={t("picker.tradableWith", { names: by.join(", ") })} aria-label={t("picker.tradable")} />}
                  </span>
                  <span className="text-right text-muted">{t(item.product === "perp" ? "common.perp" : "common.spot")}</span>
                </button>
              );
            })}
            {filtered.length === 0 && <div className="px-3 py-6 text-center text-muted">{t("picker.none")}</div>}
          </div>
          <div className="flex gap-4 border-t border-line px-3 py-2 text-muted">
            {hotkey && <span><kbd className="text-text">⌘K</kbd> {t("picker.open")}</span>}
            <span><kbd className="text-text">↑↓</kbd> {t("picker.navigate")}</span>
            <span><kbd className="text-text">Enter</kbd> {t("picker.select")}</span>
            <span><kbd className="text-text">Esc</kbd> {t("picker.escape")}</span>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** A non-crypto market's asset class ("Stock", "大宗商品"); unknown classes pass through. */
function assetClass(t: T, value: string): string {
  const key = `asset.${value}` as Parameters<T>[0];
  const text = t(key);
  return text === key ? value : text;
}

const MAJORS = ["BTC", "ETH", "SOL"];
function rank(item: Instrument, term: string, currentVenue?: string): number {
  let score = 0;
  // Stay on the venue being traded unless the user picks another.
  if (item.venue === currentVenue) score -= 50;
  if (term && item.base.toUpperCase() === term) score -= 100;
  if (term && item.quote.toUpperCase() === "USDT") score -= 1;
  const major = MAJORS.indexOf(item.base.toUpperCase());
  if (major >= 0) score -= 30 - major;
  if (item.product === "perp") score -= 10;
  if (item.symbol.includes(":")) score += 5;
  return score;
}

