import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useEffect, useState, type ReactNode } from "react";

import { hedgeApi, type GroupDetail, type GroupView, type HedgeConnection, type LegView } from "@/api/hedge";
import { countdown, num, price, time, usd } from "@/lib/format";
import { useNow } from "@/state/hooks";
import { Dialog, PrimaryButton, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { aprText, bpsText, makePrice, rateText, takePrice, tone } from "./model";
import { useBook } from "@/state/hooks";

type Filter = "active" | "history";

const STATUS: Record<GroupView["status"], { text: string; className: string }> = {
  opening: { text: "Opening", className: "text-accent" },
  open: { text: "Open", className: "text-text" },
  closing: { text: "Closing", className: "text-accent" },
  closed: { text: "Closed", className: "text-muted" },
  failed: { text: "Failed", className: "text-muted" },
  broken: { text: "Unhedged", className: "text-down" },
};

const active = (group: GroupView) => group.status !== "closed" && group.status !== "failed";

/** Every hedge as one row: the pair, its exposure, spread, funding and PnL. */
export function GroupsPanel({ groups, state }: { groups: GroupView[] | undefined; state: HedgeConnection }) {
  const [filter, setFilter] = useState<Filter>("active");
  const [expanded, setExpanded] = useState<string | null>(null);
  const [closing, setClosing] = useState<GroupView | null>(null);
  const list = (groups ?? []).filter((group) => (filter === "active" ? active(group) : !active(group)));
  const activeCount = (groups ?? []).filter(active).length;
  const broken = (groups ?? []).filter((group) => group.status === "broken").length;
  const totals = (groups ?? []).filter(active).reduce(
    (sum, group) => ({ pnl: sum.pnl + Number(group.netPnl), notional: sum.notional + Number(group.notional) / 2, funding: sum.funding + Number(group.fundingEarned) }),
    { pnl: 0, notional: 0, funding: 0 },
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex items-center border-b border-line pr-3">
        <Tabs
          value={filter}
          onChange={setFilter}
          options={[
            { value: "active", label: `Hedges${activeCount ? ` (${activeCount})` : ""}` },
            { value: "history", label: "History (7d)" },
          ]}
          className="border-b-0"
        />
        {broken > 0 && <span className="ml-3 rounded-md bg-warn-bg px-2 py-0.5 text-down">{broken} unhedged</span>}
        <div className="ml-auto flex items-center gap-5">
          {activeCount > 0 && (
            <>
              <span className="text-muted">Capital <span className="text-text">{usd(totals.notional, 0)}</span></span>
              <span className="text-muted">Funding <span className={tone(totals.funding)}>{usd(totals.funding)}</span></span>
              <span className="text-muted">Net PnL <span className={tone(totals.pnl)}>{usd(totals.pnl)}</span></span>
            </>
          )}
          <span className={cx("flex items-center gap-1.5", state === "online" ? "text-up" : "text-down")}>
            <span className={cx("size-1.5 rounded-full", state === "online" ? "bg-up" : "bg-down")} />
            {state === "online" ? "Hedger live" : state === "connecting" ? "Connecting" : "Hedger offline"}
          </span>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {groups === undefined ? (
          <div className="px-3 py-2.5 text-muted">Loading hedges…</div>
        ) : list.length === 0 ? (
          <div className="px-3 py-2.5">{filter === "active" ? "No open hedges" : "No hedges closed in the last 7 days"}</div>
        ) : (
          <table className="w-full min-w-[980px] border-collapse">
            <thead className="sticky top-0 z-10 bg-surface">
              <tr className="text-muted">
                {["Hedge", "Status", "Size", "Net exposure", "Spread entry → now", "Net funding", "Next funding", "Funding earned", "Fees", "Net PnL", ""].map((head, i) => (
                  <th key={head + i} className={cx("h-8 px-3 font-normal", i === 0 ? "text-left" : "text-right")}>{head}</th>
                ))}
              </tr>
            </thead>
            <tbody className="text-text-2">
              {list.map((group) => (
                <Fragment key={group.id}>
                  <GroupRow group={group} expanded={expanded === group.id} onToggle={() => setExpanded(expanded === group.id ? null : group.id)} onClose={() => setClosing(group)} />
                  {expanded === group.id && (
                    <tr>
                      <td colSpan={11} className="border-y border-line bg-bg/40 px-3 py-2">
                        <GroupDetailView group={group} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {closing && <CloseDialog group={closing} onDone={() => setClosing(null)} />}
    </div>
  );
}

const td = "h-8 px-3 text-right whitespace-nowrap";

function GroupRow({ group, expanded, onToggle, onClose }: { group: GroupView; expanded: boolean; onToggle: () => void; onClose: () => void }) {
  const toast = useToast();
  const now = useNow();
  const [busy, setBusy] = useState("");
  const [leg0, leg1] = group.legView;
  const status = STATUS[group.status];
  const nextFunding = [leg0, leg1].filter((leg) => leg.nextFunding && Number(leg.held) > 0).sort((a, b) => (a.nextFunding ?? 0) - (b.nextFunding ?? 0))[0];
  const nextCash = [leg0, leg1].reduce((sum, leg) => (leg.nextFunding === nextFunding?.nextFunding ? sum + Number(leg.nextFundingCash) : sum), 0);
  const run = async (name: string, action: () => Promise<unknown>) => {
    setBusy(name);
    try {
      await action();
    } catch (error) {
      toast.push({ kind: "error", title: `${name} failed`, detail: (error as Error).message });
    } finally {
      setBusy("");
    }
  };
  const working = group.working;
  const exposure = Number(group.exposure);

  return (
    <tr className={cx("cursor-pointer hover:bg-raised", group.status === "broken" && "bg-warn-bg/40")} onClick={onToggle}>
      <td className="h-8 px-3 text-left whitespace-nowrap">
        <span className="mr-1.5 text-muted">{expanded ? "▾" : "▸"}</span>
        <span className="text-text">{group.label}</span>
        <div className="ml-4 text-muted">
          <span className={leg0.side === "buy" ? "text-up" : "text-down"}>{leg0.side === "buy" ? "L" : "S"}</span> {leg0.inst.split(":").slice(2).join(":")} {leg0.venue}
          {" / "}
          <span className={leg1.side === "buy" ? "text-up" : "text-down"}>{leg1.side === "buy" ? "L" : "S"}</span> {leg1.inst.split(":").slice(2).join(":")} {leg1.venue}
        </div>
      </td>
      <td className={cx(td, status.className)} title={group.problem}>
        {status.text}
        {working && <div className="text-muted">{working.kind}{working.mode === "limit" ? " · limit" : ""}</div>}
      </td>
      <td className={td}>
        {num(leg0.held)} {leg0.base}
        <div className="text-muted">{usd(Number(group.notional) / 2, 0)}</div>
      </td>
      <td className={cx(td, Math.abs(exposure) > 0 && !group.balanced ? "text-down" : "")} title="Directional exposure left after both legs, in leg 1 units">
        {group.balanced ? "Hedged" : `${exposure > 0 ? "+" : ""}${num(group.exposure)} ${leg0.base}`}
        {!group.balanced && <div className="text-muted">{usd(group.exposureValue)}</div>}
      </td>
      <td className={td} title="Leg 2 over leg 1 in bps: average entry → mark now. For a long-leg-1 hedge the spread narrowing is profit.">
        {group.match === "base" ? <>{bpsText(group.entrySpreadBps, 1)} → {bpsText(group.spreadBps, 1)}</> : "—"}
      </td>
      <td className={cx(td, tone(group.netFundingApr))}>{aprText(group.netFundingApr)}</td>
      <td className={td}>
        {nextFunding ? <>{countdown(nextFunding.nextFunding, now)}<div className={tone(nextCash)}>{usd(nextCash, 4)}</div></> : "—"}
      </td>
      <td className={cx(td, tone(group.fundingEarned))} title="Estimated from the quoted rate at each settlement">{usd(group.fundingEarned)}</td>
      <td className={td}>{usd(Number(group.fees) * -1)}</td>
      <td className={cx(td, tone(group.netPnl))} title="Unrealized + realized − fees + funding">
        {usd(group.netPnl)}
        {group.estimated && <span className="text-muted" title="Some fill prices are estimates"> *</span>}
      </td>
      <td className={td} onClick={(event) => event.stopPropagation()}>
        <div className="flex justify-end gap-3">
          {working?.mode === "limit" && <LinkButton onClick={() => run("Cancel", () => hedgeApi.cancel(group.id))} busy={busy === "Cancel"}>Cancel</LinkButton>}
          {group.status === "broken" && !working && (
            <>
              <LinkButton title="Top up the smaller leg at market: back to full size" onClick={() => run("Rehedge", () => hedgeApi.rehedge(group.id))} busy={busy === "Rehedge"}>Rehedge</LinkButton>
              <LinkButton title="Reduce the larger leg at market to match the smaller one" onClick={() => run("Trim", () => hedgeApi.trim(group.id))} busy={busy === "Trim"}>Trim</LinkButton>
            </>
          )}
          {(group.status === "broken" || group.status === "open") && !working && (
            <LinkButton title="Close both legs completely at market" onClick={() => run("Unwind", () => hedgeApi.unwind(group.id))} busy={busy === "Unwind"} danger>Unwind</LinkButton>
          )}
          {group.status === "open" && !working && <LinkButton onClick={onClose}>Close</LinkButton>}
        </div>
      </td>
    </tr>
  );
}

function LinkButton({ onClick, busy, danger, title, children }: { onClick: () => void; busy?: boolean; danger?: boolean; title?: string; children: ReactNode }) {
  return (
    <button disabled={busy} onClick={onClick} title={title} className={cx("hover:underline disabled:text-muted", danger ? "text-down" : "text-accent")}>
      {busy ? "…" : children}
    </button>
  );
}

/** The two legs side by side, then orders, events and funding. */
function GroupDetailView({ group }: { group: GroupView }) {
  const [tab, setTab] = useState<"orders" | "events" | "funding" | "note">("orders");
  const detail = useQuery({
    queryKey: ["hedgeGroup", group.id, group.updatedAt, group.working?.id, group.legView[0].held, group.legView[1].held],
    queryFn: () => hedgeApi.group(group.id),
    staleTime: 2_000,
  });
  return (
    <div className="flex flex-col gap-2">
      {group.problem && <div className="rounded-md border border-down/60 bg-warn-bg px-3 py-2 text-down">{group.problem}</div>}
      <div className="grid grid-cols-2 gap-[3px]">
        {group.legView.map((leg) => <LegCard key={leg.leg} leg={leg} />)}
      </div>
      <Tabs value={tab} onChange={setTab} options={[
        { value: "orders", label: `Orders${detail.data ? ` (${detail.data.orders.length})` : ""}` },
        { value: "events", label: "Activity" },
        { value: "funding", label: "Funding" },
        { value: "note", label: "Note" },
      ]} />
      {!detail.data ? <div className="text-muted">Loading…</div> : tab === "orders" ? <Orders detail={detail.data} /> : tab === "events" ? <Events detail={detail.data} /> : tab === "funding" ? <FundingLog detail={detail.data} /> : <Note group={group} />}
    </div>
  );
}

function LegCard({ leg }: { leg: LegView }) {
  const now = useNow();
  const tick = leg.tickSize;
  const mismatch = Number(leg.venueSize) > 0 && Number(leg.held) > 0 && (leg.venueSide === "long" ? "buy" : "sell") !== leg.side;
  return (
    <div className="rounded-md bg-surface p-2.5">
      <div className="mb-1.5 flex items-center gap-2">
        <span className={leg.side === "buy" ? "text-up" : "text-down"}>{leg.side === "buy" ? "Long" : "Short"}</span>
        <span className="text-text">{leg.inst.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{leg.venue} · {leg.accountId}</span>
        <span className={cx("ml-auto size-1.5 rounded-full", leg.accountLive ? "bg-up" : "bg-down")} title={leg.accountLive ? "Account stream live" : "Account stream offline"} />
      </div>
      <div className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5">
        <K>Held</K><V>{num(leg.held)} {leg.base} <span className="text-muted">({usd(leg.notional)})</span></V>
        <K>Entry · Mark</K><V>{price(leg.avgEntry, tick)} · {price(leg.mark, tick)}</V>
        <K>Unrealized</K><V className={tone(leg.unrealizedPnl)}>{usd(leg.unrealizedPnl)}</V>
        {Number(leg.closed) > 0 && <><K>Closed · Realized</K><V>{num(leg.closed)} @ {price(leg.avgClose, tick)} · <span className={tone(leg.realizedPnl)}>{usd(leg.realizedPnl)}</span></V></>}
        <K>Fees</K><V>{usd(leg.fees)}{leg.feesOther ? Object.entries(leg.feesOther).map(([asset, value]) => ` + ${num(value)} ${asset}`).join("") : ""}</V>
        {leg.product === "perp" && (
          <>
            <K>Funding</K>
            <V>
              <span className={tone(leg.fundingRate)}>{rateText(leg.fundingRate)}</span>{leg.fundingHours ? <span className="text-muted">/{leg.fundingHours}h</span> : null}
              {" · "}<span className={tone(leg.fundingApr)}>{aprText(leg.fundingApr)}</span>
              {" · next "}{countdown(leg.nextFunding, now)} <span className={tone(leg.nextFundingCash)}>{usd(leg.nextFundingCash, 4)}</span>
              {leg.fundingStale && <span className="text-down"> stale</span>}
            </V>
            <K>Earned</K><V className={tone(leg.fundingEarned)}>{usd(leg.fundingEarned)}</V>
          </>
        )}
        <K>Liquidation</K>
        <V className={Number(leg.liquidationPct) > 0 && Number(leg.liquidationPct) < 15 ? "text-down" : ""}>
          {Number(leg.liquidationPrice) > 0 ? `${price(leg.liquidationPrice, tick)} · ${num(leg.liquidationPct, 1)}% away` : "—"}
          {leg.leverage ? <span className="text-muted"> · {num(leg.leverage, 0)}x</span> : null}
        </V>
        <K>Venue position</K>
        <V className={mismatch ? "text-down" : ""} >
          {Number(leg.venueSize) > 0 ? `${leg.venueSide === "short" ? "-" : ""}${num(leg.venueSize)}` : "flat"}
          {Number(leg.venueSize) > 0 && Number(leg.venueSize) !== Number(leg.held) && <span className="text-muted"> (includes trades outside this hedge)</span>}
        </V>
      </div>
    </div>
  );
}

const K = ({ children }: { children: ReactNode }) => <span className="text-muted">{children}</span>;
const V = ({ children, className }: { children: ReactNode; className?: string }) => <span className={cx("text-right", className)}>{children}</span>;

function Orders({ detail }: { detail: GroupDetail }) {
  const actions = new Map(detail.actions.map((action) => [action.id, action]));
  if (!detail.orders.length) return <div className="text-muted">No orders</div>;
  return (
    <table className="w-full border-collapse">
      <thead><tr className="text-muted">{["Time", "Action", "Leg", "Role", "Side", "Type", "Qty", "Filled", "Avg price", "Slippage", "State"].map((head, i) => <th key={head} className={cx("h-6 px-2 font-normal", i === 0 ? "text-left" : "text-right")}>{head}</th>)}</tr></thead>
      <tbody>
        {[...detail.orders].reverse().map((order) => {
          const ref = Number(order.refPrice);
          const avg = Number(order.avgPrice);
          const slip = order.type === "market" && ref > 0 && avg > 0 ? ((avg - ref) / ref) * 10_000 * (order.side === "buy" ? 1 : -1) : undefined;
          return (
            <tr key={order.clientOrderId} className="hover:bg-raised" title={order.error || `client ${order.clientOrderId}${order.orderId ? ` · venue ${order.orderId}` : ""}`}>
              <td className="h-6 px-2 text-left">{time(Date.parse(order.createdAt))}</td>
              <td className="px-2 text-right">{actions.get(order.actionId)?.kind ?? ""}</td>
              <td className="px-2 text-right">{order.leg + 1}</td>
              <td className="px-2 text-right text-muted">{order.role}</td>
              <td className={cx("px-2 text-right", order.side === "buy" ? "text-up" : "text-down")}>{order.side}</td>
              <td className="px-2 text-right">{order.type}{Number(order.price) > 0 ? ` @ ${num(order.price)}` : ""}</td>
              <td className="px-2 text-right">{num(order.qty)}</td>
              <td className="px-2 text-right">{num(order.filled)}</td>
              <td className="px-2 text-right">{avg > 0 ? num(order.avgPrice) : "—"}</td>
              <td className={cx("px-2 text-right", slip !== undefined && slip > 5 ? "text-down" : "")}>{slip === undefined ? "—" : bpsText(slip, 1)}</td>
              <td className={cx("px-2 text-right", order.state === "rejected" || order.state === "unknown" ? "text-down" : "")}>{order.state}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function Events({ detail }: { detail: GroupDetail }) {
  return (
    <div className="max-h-56 overflow-y-auto">
      {detail.events.map((event) => (
        <div key={event.id} className="flex gap-3 py-0.5">
          <span className="shrink-0 text-muted">{time(Date.parse(event.at))}</span>
          <span className={event.level === "error" ? "text-down" : event.level === "warn" ? "text-text" : "text-text-2"}>{event.message}</span>
        </div>
      ))}
    </div>
  );
}

function FundingLog({ detail }: { detail: GroupDetail }) {
  if (!detail.funding.length) return <div className="text-muted">No settlements yet. Funding is estimated from the quoted rate at each settlement.</div>;
  return (
    <table className="w-full border-collapse">
      <thead><tr className="text-muted">{["Settled", "Leg", "Rate", "Size", "Price", "Amount"].map((head, i) => <th key={head} className={cx("h-6 px-2 font-normal", i === 0 ? "text-left" : "text-right")}>{head}</th>)}</tr></thead>
      <tbody>
        {detail.funding.map((row) => (
          <tr key={row.settledAt + row.leg}>
            <td className="h-6 px-2 text-left">{time(Date.parse(row.settledAt))}</td>
            <td className="px-2 text-right">{row.leg + 1}</td>
            <td className="px-2 text-right">{rateText(row.rate)}</td>
            <td className="px-2 text-right">{num(row.qty)}</td>
            <td className="px-2 text-right">{num(row.price)}</td>
            <td className={cx("px-2 text-right", tone(row.amount))}>{usd(row.amount, 4)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Note({ group }: { group: GroupView }) {
  const toast = useToast();
  const [label, setLabel] = useState(group.label);
  const [note, setNote] = useState(group.note);
  const [saving, setSaving] = useState(false);
  useEffect(() => { setLabel(group.label); setNote(group.note); }, [group.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const save = async () => {
    setSaving(true);
    try {
      await hedgeApi.edit(group.id, label, note);
    } catch (error) {
      toast.push({ kind: "error", title: "Not saved", detail: (error as Error).message });
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="flex max-w-xl flex-col gap-2">
      <input value={label} maxLength={80} onChange={(event) => setLabel(event.target.value)} className="h-8 rounded-lg border border-line bg-transparent px-3 outline-none focus:border-line-strong" />
      <textarea value={note} maxLength={2000} rows={3} onChange={(event) => setNote(event.target.value)} placeholder="Why this hedge, exit plan…" className="rounded-lg border border-line bg-transparent px-3 py-2 outline-none placeholder:text-faint focus:border-line-strong" />
      <button onClick={save} disabled={saving} className="h-8 w-24 rounded-lg bg-raised hover:bg-line-strong/40 disabled:opacity-50">{saving ? "Saving…" : "Save"}</button>
    </div>
  );
}

/** Close a fraction of the hedge at market, or rest one leg and hedge fills. */
function CloseDialog({ group, onDone }: { group: GroupView; onDone: () => void }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const [percent, setPercent] = useState(100);
  const [mode, setMode] = useState<"market" | "limit">("market");
  const [maker, setMaker] = useState<0 | 1>(0);
  const [limitPrice, setLimitPrice] = useState("");
  const [postOnly, setPostOnly] = useState(true);
  const [busy, setBusy] = useState(false);
  const makerLeg = group.legView[maker];
  const book = useBook(makerLeg.inst);
  const closeSide = makerLeg.side === "buy" ? "sell" : "buy";
  const exit0 = Number(group.legView[0].held) * (percent / 100);
  const submit = async () => {
    setBusy(true);
    try {
      await hedgeApi.close(group.id, {
        fraction: String(percent / 100), mode, makerLeg: maker,
        limitPrice: mode === "limit" ? limitPrice : undefined, tif: mode === "limit" ? (postOnly ? "post_only" : "gtc") : undefined,
      });
      void queryClient.invalidateQueries({ queryKey: ["hedgeGroup"] });
      onDone();
    } catch (error) {
      toast.push({ kind: "error", title: "Close not started", detail: (error as Error).message });
    } finally {
      setBusy(false);
    }
  };
  const fill = (kind: "join" | "touch") => {
    const value = kind === "join" ? makePrice(book, closeSide) : takePrice(book, closeSide);
    if (value) setLimitPrice(String(value));
  };
  return (
    <Dialog open onOpenChange={(open) => !open && onDone()} title="Close hedge" description={group.label}>
      <div className="flex flex-col gap-3 text-left">
        <div className="flex items-center gap-3">
          <input type="range" min={1} max={100} value={percent} onChange={(event) => setPercent(Number(event.target.value))} className="range flex-1" style={{ ["--fill" as string]: `${percent}%` }} />
          <span className="w-12 text-right">{percent}%</span>
        </div>
        <div className="flex gap-1">{[25, 50, 75, 100].map((value) => <button key={value} onClick={() => setPercent(value)} className={cx("h-7 flex-1 rounded-md", percent === value ? "bg-line-strong/60" : "bg-raised")}>{value}%</button>)}</div>
        <div className="text-muted">
          Closes ≈ {num(exit0)} {group.legView[0].base} on leg 1 and the matching amount on leg 2.
          {group.match === "base" && <> Exit spread now {bpsText(group.exitSpreadBps, 1)} (entry {bpsText(group.entrySpreadBps, 1)}).</>}
        </div>
        <Tabs value={mode} onChange={setMode} options={[{ value: "market", label: "Market both" }, { value: "limit", label: "Limit + hedge" }]} className="[&>button]:flex-1" />
        {mode === "limit" && (
          <>
            <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5">
              {([0, 1] as const).map((leg) => (
                <button key={leg} onClick={() => setMaker(leg)} className={cx("rounded-md", maker === leg ? "bg-line-strong/60 text-text" : "text-muted")}>Rest leg {leg + 1} · {group.legView[leg].venue}</button>
              ))}
            </div>
            <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3">
              <span className="text-muted">{closeSide === "buy" ? "Buy" : "Sell"} price</span>
              <input inputMode="decimal" value={limitPrice} onChange={(event) => setLimitPrice(event.target.value.replace(/[^\d.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
              <button onClick={() => fill("join")} className="text-accent">{closeSide === "buy" ? "Bid" : "Ask"}</button>
              <button onClick={() => fill("touch")} className="text-accent">{closeSide === "buy" ? "Ask" : "Bid"}</button>
            </label>
            <label className="flex items-center gap-2"><input type="checkbox" checked={postOnly} onChange={(event) => setPostOnly(event.target.checked)} className="accent-[#50d2c1]" />Post only</label>
          </>
        )}
        <PrimaryButton onClick={submit} disabled={busy || (mode === "limit" && !(Number(limitPrice) > 0))}>{busy ? "Starting…" : mode === "market" ? `Close ${percent}% at market` : `Place close ${percent}%`}</PrimaryButton>
      </div>
    </Dialog>
  );
}
