import * as Popover from "@radix-ui/react-popover";
import { useLayoutEffect, useMemo, useRef, useState } from "react";

import type { Instrument } from "@/api/types";
import { countdown, num, price, venueLabel } from "@/lib/format";
import { useBook, useFunding, useInstrument, useInstruments, useNow, type AccountLive } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { aprText, legAvailable, legPosition, rateText, tone, type LegChoice } from "./model";

const ROW = 20;

/** Rows per book side that fit a body `height` px tall around a spread row. */
export function bookRows(height: number): number {
  return Math.max(1, Math.min(20, Math.floor((height - 24) / 2 / ROW)));
}

export function LegPanel({ index, leg, live, onChange }: {
  index: 0 | 1;
  leg: LegChoice;
  live: AccountLive;
  onChange: (change: Partial<LegChoice>) => void;
}) {
  const instrument = useInstrument(leg.inst);
  const perp = instrument?.product === "perp";
  const book = useBook(leg.inst);
  const funding = useFunding(leg.inst, perp);
  const now = useNow();
  const { accounts } = useWorkspace();
  const venueAccounts = accounts.filter((account) => account.venue === instrument?.venue);
  const available = legAvailable(instrument, live.balances, leg.side);
  const position = legPosition(live.positions, leg.inst);
  const tick = instrument?.tickSize;
  const body = useRef<HTMLDivElement>(null);
  const [rows, setRows] = useState(6);
  useLayoutEffect(() => {
    const element = body.current;
    if (!element) return;
    const measure = () => setRows(bookRows(element.clientHeight));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const asks = (book?.asks ?? []).slice(0, rows);
  const bids = (book?.bids ?? []).slice(0, rows);
  const bestAsk = Number(asks[0]?.[0]);
  const bestBid = Number(bids[0]?.[0]);
  const spread = bestAsk && bestBid ? bestAsk - bestBid : undefined;
  const maxSize = Math.max(...asks.map((level) => Number(level[1])), ...bids.map((level) => Number(level[1])), 1);

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <span className="text-muted">Leg {index + 1}</span>
        <InstrumentSelect value={leg.inst} onChange={(inst) => onChange({ inst })} />
        <div className="ml-auto grid h-7 grid-cols-2 rounded-md bg-raised p-0.5">
          <button onClick={() => onChange({ side: "buy" })} className={cx("rounded px-2.5", leg.side === "buy" ? "bg-up text-on-accent" : "text-text-2")}>Long</button>
          <button
            onClick={() => onChange({ side: "sell" })}
            disabled={instrument?.product === "spot"}
            title={instrument?.product === "spot" ? "A spot leg can only be long" : undefined}
            className={cx("rounded px-2.5 disabled:cursor-not-allowed disabled:opacity-40", leg.side === "sell" ? "bg-down text-on-accent" : "text-text-2")}
          >
            Short
          </button>
        </div>
      </div>

      <div className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-1 border-b border-line px-3 py-2">
        <Line label="Account">
          {venueAccounts.length === 0 ? (
            <span className="text-down">No {venueLabel(instrument?.venue ?? "")} account</span>
          ) : (
            <select value={leg.accountId} onChange={(event) => onChange({ accountId: event.target.value })} className="max-w-[180px] bg-transparent text-right outline-none">
              {venueAccounts.map((account) => <option key={account.id} value={account.id}>{account.label} · {account.id}</option>)}
            </select>
          )}
        </Line>
        <Line label="Available">{available ? `${num(available.amount)} ${available.asset}` : "—"}</Line>
        {perp ? (
          <>
            <Line label="Funding" hint="Rate at the next settlement / settlement period">
              <span className={tone(funding?.rate)}>{rateText(funding?.rate)}</span>
              {funding?.intervalHours ? <span className="text-muted"> /{funding.intervalHours}h</span> : null}
              {funding?.stale && <span className="ml-1 text-down" title="Funding collection for this venue is behind">stale</span>}
            </Line>
            <Line label="APR · Next" hint="Annualized (simple) · time to the next settlement">
              <span className={tone(funding?.apr)}>{aprText(funding?.apr)}</span>
              <span className="text-muted"> · </span>
              {countdown(funding?.nextFunding, now)}
            </Line>
          </>
        ) : (
          <Line label="Funding">Spot: none</Line>
        )}
        <Line label="Venue position" hint="This account's whole position in the instrument, including trades outside hedges">
          {position ? (
            <span className={position.side === "long" ? "text-up" : "text-down"}>
              {position.side === "short" ? "-" : ""}{num(position.size)} @ {price(position.entryPrice, tick)}
              {position.liquidationPrice ? <span className="text-muted"> · liq {price(position.liquidationPrice, tick)}</span> : null}
            </span>
          ) : "—"}
        </Line>
      </div>

      <div className="grid h-6 shrink-0 grid-cols-2 items-center px-3 text-muted">
        <span>Price ({instrument?.quote})</span>
        <span className="text-right">Size ({instrument?.base})</span>
      </div>
      <div ref={body} className="flex min-h-0 flex-1 flex-col justify-center overflow-hidden">
        {!book ? (
          <div className="py-6 text-center text-muted">Loading book…</div>
        ) : (
          <>
            <div className="flex flex-col-reverse">{asks.map(([p, q]) => <Level key={"a" + p} priceText={price(p, tick)} size={q} width={Number(q) / maxSize} ask />)}</div>
            <div className="flex h-6 items-center justify-center gap-4 bg-raised">
              <span className="text-muted">Spread</span>
              <span>{spread === undefined ? "—" : price(spread, tick)}</span>
              <span className="text-muted">{spread && bestAsk ? ((spread / bestAsk) * 10_000).toFixed(1) + " bps" : ""}</span>
            </div>
            <div>{bids.map(([p, q]) => <Level key={"b" + p} priceText={price(p, tick)} size={q} width={Number(q) / maxSize} />)}</div>
          </>
        )}
      </div>
    </div>
  );
}

function Line({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <>
      <span className={cx("text-muted", hint && "dotted")} title={hint}>{label}</span>
      <span className="text-right whitespace-nowrap">{children}</span>
    </>
  );
}

function Level({ priceText, size, width, ask }: { priceText: string; size: string; width: number; ask?: boolean }) {
  return (
    <div className="relative grid h-5 grid-cols-2 items-center px-3">
      <span className={cx("absolute inset-y-0 right-0", ask ? "bg-down/15" : "bg-long/15")} style={{ width: `${Math.min(1, width) * 100}%` }} />
      <span className={cx("relative", ask ? "text-down" : "text-up")}>{priceText}</span>
      <span className="relative text-right">{num(size)}</span>
    </div>
  );
}

/** Searchable instrument picker over every venue's perps and spot. */
function InstrumentSelect({ value, onChange }: { value: string; onChange: (inst: string) => void }) {
  const { data: instruments = [] } = useInstruments();
  const current = instruments.find((item) => item.id === value);
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [cursor, setCursor] = useState(0);
  const list = useRef<HTMLDivElement>(null);
  const filtered = useMemo(() => {
    const term = search.trim().toUpperCase().replace(/[\s/]+/g, "-");
    return instruments
      .filter((item) => !term || item.symbol.includes(term) || item.base.toUpperCase().startsWith(term) || item.venue.toUpperCase().startsWith(term))
      .sort((a, b) => rank(a, term) - rank(b, term) || a.venue.localeCompare(b.venue) || a.symbol.localeCompare(b.symbol))
      .slice(0, 150);
  }, [instruments, search]);
  const choose = (item: Instrument | undefined) => {
    if (!item) return;
    onChange(item.id);
    setOpen(false);
    setSearch("");
  };
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className="flex items-center gap-2">
        <span className="text-md">{current?.symbol ?? value.split(":").slice(2).join(":")}</span>
        <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-accent">{venueLabel(current?.venue ?? value.split(":")[0] ?? "")} · {current?.product === "spot" ? "Spot" : "Perp"}</span>
        <span className="text-muted">▾</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={6} className="z-40 w-[min(520px,calc(100vw-16px))] rounded-[10px] border border-line bg-overlay shadow-2xl">
          <div className="p-2">
            <input
              autoFocus
              value={search}
              placeholder="Search coin or venue"
              onChange={(event) => { setSearch(event.target.value); setCursor(0); }}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") { event.preventDefault(); setCursor((c) => Math.min(c + 1, filtered.length - 1)); }
                if (event.key === "ArrowUp") { event.preventDefault(); setCursor((c) => Math.max(c - 1, 0)); }
                if (event.key === "Enter") choose(filtered[cursor]);
              }}
              className="h-8 w-full rounded-lg border border-line-strong bg-transparent px-3 outline-none placeholder:text-muted"
            />
          </div>
          <div ref={list} className="max-h-[50vh] overflow-y-auto pb-2">
            {filtered.map((item, i) => (
              <button
                key={item.id}
                onMouseEnter={() => setCursor(i)}
                onClick={() => choose(item)}
                className={cx("grid w-full grid-cols-[1fr_110px_50px] gap-2 px-3 py-1 text-left", i === cursor && "bg-raised", item.id === value && "text-accent")}
              >
                <span className="truncate">{item.symbol}</span>
                <span className="text-text-2">{venueLabel(item.venue)}</span>
                <span className="text-right text-muted">{item.product === "perp" ? "Perp" : "Spot"}</span>
              </button>
            ))}
            {filtered.length === 0 && <div className="px-3 py-4 text-center text-muted">No markets match</div>}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function rank(item: Instrument, term: string): number {
  let score = 0;
  if (term && item.base.toUpperCase() === term) score -= 100;
  if (item.product === "perp") score -= 10;
  if (item.symbol.includes(":")) score += 5;
  return score;
}
