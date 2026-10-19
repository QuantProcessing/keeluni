import { useLayoutEffect, useRef, useState } from "react";

import { countdown, num, price, usd } from "@/lib/format";
import { rulesFor } from "@/lib/routes";
import { useBook, useFunding, useInstrument, useNow } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { AccountPicker, NoAccount, useAccountChoices } from "../account-picker";
import { InstrumentPicker } from "../market-header";
import { aprText, legAccount, legAvailable, legPosition, rateText, tone, type LegChoice } from "./model";

const ROW = 20;

/** Rows per book side that fit a body `height` px tall around a spread row. */
export function bookRows(height: number): number {
  return Math.max(1, Math.min(20, Math.floor((height - 24) / 2 / ROW)));
}

export function LegPanel({ index, leg, other, onChange }: {
  index: 0 | 1;
  leg: LegChoice;
  /** The other leg: a broker account shared by both legs is one pool. */
  other: LegChoice;
  onChange: (change: Partial<LegChoice>) => void;
}) {
  const instrument = useInstrument(leg.inst);
  const perp = instrument?.product === "perp";
  const book = useBook(leg.inst);
  const funding = useFunding(leg.inst, perp);
  const now = useNow();
  const { accounts, routes, live } = useWorkspace();
  const { candidates, unconnectedBrokers } = useAccountChoices(instrument);
  const account = legAccount(accounts, routes, instrument, leg.accountId);
  const state = account ? live[account.id] : undefined;
  const available = legAvailable(account, instrument, state, leg.side);
  const position = legPosition(state?.positions, leg.inst);
  const rules = instrument ? rulesFor(instrument, account ? routes[account.id] : undefined) : undefined;
  const sharedPool = account?.kind === "broker" && other.accountId === account.id;
  const tick = rules?.tickSize ?? instrument?.tickSize;
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
  // Deribit accrues funding continuously; the gateway quotes it per hour.
  const continuous = instrument?.venue === "deribit";

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <span className="text-muted">Leg {index + 1}</span>
        <InstrumentPicker value={leg.inst} onChange={(inst) => onChange({ inst })} small />
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

      <div className="border-b border-line px-3 py-1">
        {instrument && candidates.length === 0 ? (
          <div className="flex h-8 items-center gap-2">
            <span className="shrink-0 text-muted">Account</span>
            <span className="ml-auto min-w-0"><NoAccount instrument={instrument} unconnectedBrokers={unconnectedBrokers} compact /></span>
          </div>
        ) : (
          <AccountPicker instrument={instrument} account={account} candidates={candidates} unconnectedBrokers={unconnectedBrokers} onChoose={(accountId) => onChange({ accountId })} ariaLabel={`Leg ${index + 1} account`} />
        )}
      </div>

      <div className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-1 border-b border-line px-3 py-2">
        <Line label="Available" hint={sharedPool ? "Both legs draw on this one cross-exchange pool" : account?.kind === "broker" ? "The cross-exchange pool's available margin" : undefined}>
          {available ? `${num(available.amount)} ${available.asset}` : "—"}
          {sharedPool && <span className="text-muted"> · shared</span>}
        </Line>
        {account?.kind === "broker" && rules && (
          <Line label="Min size" hint="The broker's own rules for this market">{rules.minQty}{rules.minNotional ? ` · ${usd(rules.minNotional, 0)}` : ""}</Line>
        )}
        {perp ? (
          <>
            <Line label="Funding" hint={continuous ? "Deribit accrues funding continuously; shown per hour" : "Rate at the next settlement / settlement period"}>
              <span className={tone(funding?.rate)}>{rateText(funding?.rate)}</span>
              {funding?.intervalHours ? <span className="text-muted"> /{funding.intervalHours}h</span> : null}
              {funding?.stale && <span className="ml-1 text-down" title="Funding collection for this venue is behind">stale</span>}
            </Line>
            <Line label={continuous ? "APR" : "APR · Next"} hint={continuous ? "Annualized (simple)" : "Annualized (simple) · time to the next settlement"}>
              <span className={tone(funding?.apr)}>{aprText(funding?.apr)}</span>
              {continuous ? <span className="text-muted"> · continuous</span> : <><span className="text-muted"> · </span>{countdown(funding?.nextFunding, now)}</>}
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
            <div className="flex flex-col-reverse">{asks.map(([p, q]) => <Level key={"a" + p} priceText={price(p, instrument?.tickSize)} size={q} width={Number(q) / maxSize} ask />)}</div>
            <div className="flex h-6 items-center justify-center gap-4 bg-raised">
              <span className="text-muted">Spread</span>
              <span>{spread === undefined ? "—" : price(spread, instrument?.tickSize)}</span>
              <span className="text-muted">{spread && bestAsk ? ((spread / bestAsk) * 10_000).toFixed(1) + " bps" : ""}</span>
            </div>
            <div>{bids.map(([p, q]) => <Level key={"b" + p} priceText={price(p, instrument?.tickSize)} size={q} width={Number(q) / maxSize} />)}</div>
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
