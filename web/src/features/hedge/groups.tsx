import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useEffect, useState, type ReactNode } from "react";

import { hedgeApi, type GroupDetail, type GroupView, type HedgeConnection, type LegView } from "@/api/hedge";
import { countdown, num, price, time, usd, venueLabel } from "@/lib/format";
import { accountName } from "@/lib/routes";
import { useNow } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { Dialog, PrimaryButton, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { aprText, bpsText, makePrice, rateText, takePrice, tone } from "./model";
import { useBook } from "@/state/hooks";
import { useHedgerText, useT, type T } from "@/i18n";

type Filter = "active" | "history";

const STATUS_CLASS: Record<GroupView["status"], string> = {
  opening: "text-accent", open: "text-text", closing: "text-accent", closed: "text-muted", failed: "text-muted", broken: "text-down",
};

/** Column heads, one line each; the hint explains the shorter ones. */
const HEADS: Array<[Parameters<T>[0] | "", Parameters<T>[0]?]> = [
  ["groups.head.hedge"],
  ["common.status"],
  ["groups.head.size", "groups.head.sizeHint"],
  ["groups.head.exposure", "groups.head.exposureHint"],
  ["groups.head.spread", "groups.head.spreadHint"],
  ["groups.head.funding", "groups.head.fundingHint"],
  ["groups.head.next", "groups.head.nextHint"],
  ["groups.head.earned", "groups.head.earnedHint"],
  ["common.fees"],
  ["groups.head.netPnl", "groups.head.netPnlHint"],
  [""],
];

const active = (group: GroupView) => group.status !== "closed" && group.status !== "failed";

/** Every hedge as one row: the pair, its exposure, spread, funding and PnL. */
export function GroupsPanel({ groups, state }: { groups: GroupView[] | undefined; state: HedgeConnection }) {
  const t = useT();
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
            { value: "active", label: `${t("groups.hedges")}${activeCount ? ` (${activeCount})` : ""}` },
            { value: "history", label: t("groups.history") },
          ]}
          className="border-b-0"
        />
        {broken > 0 && <span className="ml-3 rounded-md bg-warn-bg px-2 py-0.5 text-down">{t("groups.unhedgedBadge", { n: broken })}</span>}
        <div className="ml-auto flex shrink-0 items-center gap-5">
          <span className={cx("flex items-center gap-1.5 whitespace-nowrap", state === "online" ? "text-up" : "text-down")}>
            <span className={cx("size-1.5 rounded-full", state === "online" ? "bg-up" : "bg-down")} />
            {state === "online" ? t("groups.live") : state === "connecting" ? t("status.connecting") : t("groups.offline")}
          </span>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {groups === undefined ? (
          <div className="px-3 py-2.5 text-muted">{t("groups.loading")}</div>
        ) : list.length === 0 ? (
          <div className="px-3 py-2.5">{filter === "active" ? t("groups.noneOpen") : t("groups.noneClosed")}</div>
        ) : (
          <table className="w-full min-w-[1040px] border-collapse">
            <thead className="sticky top-0 z-20 bg-surface">
              <tr className="text-muted">
                {HEADS.map(([head, hint], i) => (
                  <th
                    key={head + i}
                    title={hint ? t(hint) : undefined}
                    className={cx("h-8 px-3 font-normal whitespace-nowrap", i === 0 ? "text-left" : "text-right", hint && "dotted", i === HEADS.length - 1 && "sticky right-0 bg-surface")}
                  >
                    {head ? t(head) : ""}
                  </th>
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
            {filter === "active" && activeCount > 1 && (
              <tfoot className="sticky bottom-0 z-10 bg-surface text-text">
                <tr className="border-t border-line">
                  <td className="h-8 px-3 text-muted">{t("groups.totalRow", { n: activeCount })}</td>
                  <td className={td} />
                  <td className={td}>{usd(totals.notional, 0)}</td>
                  <td className={td} colSpan={4} />
                  <td className={cx(td, tone(totals.funding))}>{usd(totals.funding)}</td>
                  <td className={td} />
                  <td className={cx(td, tone(totals.pnl))}>{usd(totals.pnl)}</td>
                  <td className="sticky right-0 bg-surface" />
                </tr>
              </tfoot>
            )}
          </table>
        )}
      </div>
      {closing && <CloseDialog group={closing} onDone={() => setClosing(null)} />}
    </div>
  );
}

const td = "h-8 px-3 text-right whitespace-nowrap";

/**
 * Where a leg trades, as the operator reads it: "Binance · main" for a
 * venue's own account, "OKX via Gate CrossEx" through a broker, the account
 * id while accounts load or after it was removed.
 */
function useLegPlace() {
  const t = useT();
  const { accounts } = useWorkspace();
  return (leg: { venue: string; accountId: string }) => {
    const account = accounts.find((item) => item.id === leg.accountId);
    if (!account) return `${venueLabel(leg.venue)} · ${leg.accountId}`;
    return account.venue === leg.venue ? accountName(account) : t("groups.via", { venue: venueLabel(leg.venue), account: accountName(account) });
  };
}

function GroupRow({ group, expanded, onToggle, onClose }: { group: GroupView; expanded: boolean; onToggle: () => void; onClose: () => void }) {
  const t = useT();
  const text = useHedgerText();
  const toast = useToast();
  const place = useLegPlace();
  const now = useNow();
  const [busy, setBusy] = useState("");
  const [leg0, leg1] = group.legView;
  const statusClass = STATUS_CLASS[group.status];
  const nextFunding = [leg0, leg1].filter((leg) => leg.nextFunding && Number(leg.held) > 0).sort((a, b) => (a.nextFunding ?? 0) - (b.nextFunding ?? 0))[0];
  const nextCash = [leg0, leg1].reduce((sum, leg) => (leg.nextFunding === nextFunding?.nextFunding ? sum + Number(leg.nextFundingCash) : sum), 0);
  const run = async (name: string, action: () => Promise<unknown>) => {
    setBusy(name);
    try {
      await action();
    } catch (error) {
      toast.push({ kind: "error", title: t("groups.actionFailed", { action: name }), detail: (error as Error).message });
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
          <span className={leg0.side === "buy" ? "text-up" : "text-down"}>{t(leg0.side === "buy" ? "common.long" : "common.short")}</span> {leg0.inst.split(":").slice(2).join(":")} {place(leg0)}
          {" / "}
          <span className={leg1.side === "buy" ? "text-up" : "text-down"}>{t(leg1.side === "buy" ? "common.long" : "common.short")}</span> {leg1.inst.split(":").slice(2).join(":")} {place(leg1)}
        </div>
      </td>
      <td className={cx(td, statusClass)} title={text(group.problem) || undefined}>
        {t(`groups.status.${group.status}`)}
        {working && <div className="text-muted">{t(`groups.action.${working.kind}`)}{working.mode === "limit" ? t("groups.limitSuffix") : ""}</div>}
      </td>
      <td className={td}>
        {num(leg0.held)} {leg0.base}
        <div className="text-muted">{usd(Number(group.notional) / 2, 0)}</div>
      </td>
      <td className={cx(td, Math.abs(exposure) > 0 && !group.balanced ? "text-down" : "")} title={t("groups.exposureHint")}>
        {group.balanced ? t("groups.hedged") : `${exposure > 0 ? "+" : ""}${num(group.exposure)} ${leg0.base}`}
        {!group.balanced && <div className="text-muted">{usd(group.exposureValue)}</div>}
      </td>
      <td className={td} title={t("groups.spreadHint")}>
        {group.match === "base" ? <>{bpsText(group.entrySpreadBps, 1)} → {bpsText(group.spreadBps, 1)}</> : "—"}
      </td>
      <td className={cx(td, tone(group.netFundingApr))}>{aprText(group.netFundingApr)}</td>
      <td className={td}>
        {nextFunding ? <>{countdown(nextFunding.nextFunding, now)}<div className={tone(nextCash)}>{usd(nextCash, 4)}</div></> : "—"}
      </td>
      <td className={cx(td, tone(group.fundingEarned))} title={t("groups.earnedHint")}>{usd(group.fundingEarned)}</td>
      <td className={td}>{usd(Number(group.fees) * -1)}</td>
      <td className={cx(td, tone(group.netPnl))} title={t("groups.head.netPnlHint")}>
        {usd(group.netPnl)}
        {group.estimated && <span className="text-muted" title={t("groups.estimated")}> *</span>}
      </td>
      <td className={cx(td, "sticky right-0", group.status === "broken" ? "bg-[#1f1219]" : "bg-surface")} onClick={(event) => event.stopPropagation()}>
        <div className="flex justify-end gap-3">
          {working?.mode === "limit" && <LinkButton onClick={() => run(t("groups.cancel"), () => hedgeApi.cancel(group.id))} busy={busy === t("groups.cancel")}>{t("groups.cancel")}</LinkButton>}
          {group.status === "broken" && !working && (
            <>
              <LinkButton title={t("groups.rehedgeHint")} onClick={() => run(t("groups.rehedge"), () => hedgeApi.rehedge(group.id))} busy={busy === t("groups.rehedge")}>{t("groups.rehedge")}</LinkButton>
              <LinkButton title={t("groups.trimHint")} onClick={() => run(t("groups.trim"), () => hedgeApi.trim(group.id))} busy={busy === t("groups.trim")}>{t("groups.trim")}</LinkButton>
            </>
          )}
          {(group.status === "broken" || group.status === "open") && !working && (
            <LinkButton title={t("groups.unwindHint")} onClick={() => run(t("groups.unwind"), () => hedgeApi.unwind(group.id))} busy={busy === t("groups.unwind")} danger>{t("groups.unwind")}</LinkButton>
          )}
          {group.status === "open" && !working && <LinkButton onClick={onClose}>{t("groups.close")}</LinkButton>}
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
  const t = useT();
  const text = useHedgerText();
  const [tab, setTab] = useState<"orders" | "events" | "funding" | "note">("orders");
  const detail = useQuery({
    queryKey: ["hedgeGroup", group.id, group.updatedAt, group.working?.id, group.legView[0].held, group.legView[1].held],
    queryFn: () => hedgeApi.group(group.id),
    staleTime: 2_000,
  });
  return (
    <div className="flex flex-col gap-2">
      {text(group.problem) && <div className="rounded-md border border-down/60 bg-warn-bg px-3 py-2 text-down">{text(group.problem)}</div>}
      <div className="grid grid-cols-2 gap-[3px]">
        {group.legView.map((leg) => <LegCard key={leg.leg} leg={leg} />)}
      </div>
      <Tabs value={tab} onChange={setTab} options={[
        { value: "orders", label: `${t("groups.tab.orders")}${detail.data ? ` (${detail.data.orders.length})` : ""}` },
        { value: "events", label: t("groups.tab.activity") },
        { value: "funding", label: t("groups.tab.funding") },
        { value: "note", label: t("groups.tab.note") },
      ]} />
      {!detail.data ? <div className="text-muted">{t("common.loading")}</div> : tab === "orders" ? <Orders detail={detail.data} /> : tab === "events" ? <Events detail={detail.data} /> : tab === "funding" ? <FundingLog detail={detail.data} /> : <Note group={group} />}
    </div>
  );
}

function LegCard({ leg }: { leg: LegView }) {
  const t = useT();
  const now = useNow();
  const place = useLegPlace();
  const continuous = leg.venue === "deribit";
  const tick = leg.tickSize;
  const venue = legVenueHeld(leg);
  // The venue holding less of the leg than the hedge accounts for is the
  // hedger's Unhedged check; holding more is other trades in the account.
  const short = venue !== undefined && Number(leg.held) > 0 && venue + Number(leg.stepSize) < Number(leg.held);
  return (
    <div className="rounded-md bg-surface p-2.5">
      <div className="mb-1.5 flex items-center gap-2">
        <span className={leg.side === "buy" ? "text-up" : "text-down"}>{t(leg.side === "buy" ? "common.long" : "common.short")}</span>
        <span className="text-text">{leg.inst.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{place(leg)}</span>
        <span className={cx("ml-auto size-1.5 rounded-full", leg.accountLive ? "bg-up" : "bg-down")} title={leg.accountLive ? t("accounts.streamLive") : t("accounts.streamDown")} />
      </div>
      <div className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5">
        <K>{t("groups.held")}</K><V>{num(leg.held)} {leg.base} <span className="text-muted">({usd(leg.notional)})</span></V>
        <K>{t("groups.entryMark")}</K><V>{price(leg.avgEntry, tick)} · {price(leg.mark, tick)}</V>
        <K>{t("groups.unrealized")}</K><V className={tone(leg.unrealizedPnl)}>{usd(leg.unrealizedPnl)}</V>
        {Number(leg.closed) > 0 && <><K>{t("groups.closedRealized")}</K><V>{num(leg.closed)} @ {price(leg.avgClose, tick)} · <span className={tone(leg.realizedPnl)}>{usd(leg.realizedPnl)}</span></V></>}
        <K>{t("common.fees")}</K><V>{usd(leg.fees)}{leg.feesOther ? Object.entries(leg.feesOther).map(([asset, value]) => ` + ${num(value)} ${asset}`).join("") : ""}</V>
        {leg.product === "perp" && (
          <>
            <K>{t("common.funding")}</K>
            <V>
              <span className={tone(leg.fundingRate)}>{rateText(leg.fundingRate)}</span>{leg.fundingHours ? <span className="text-muted">/{leg.fundingHours}h</span> : null}
              {" · "}<span className={tone(leg.fundingApr)}>{aprText(leg.fundingApr)}</span>
              {continuous
                ? <span className="text-muted" title={t("groups.continuousHint")}> · {t("leg.continuous")}</span>
                : <>{t("groups.next")}{countdown(leg.nextFunding, now)} <span className={tone(leg.nextFundingCash)}>{usd(leg.nextFundingCash, 4)}</span></>}
              {leg.fundingStale && <span className="text-down"> {t("leg.stale")}</span>}
            </V>
            <K>{t("groups.earned")}</K><V className={tone(leg.fundingEarned)}>{usd(leg.fundingEarned)}</V>
          </>
        )}
        <K>{t("groups.liquidation")}</K>
        <V className={Number(leg.liquidationPct) > 0 && Number(leg.liquidationPct) < 15 ? "text-down" : ""}>
          {Number(leg.liquidationPrice) > 0 ? `${price(leg.liquidationPrice, tick)} · ${t("groups.away", { pct: num(leg.liquidationPct, 1) })}` : "—"}
          {leg.leverage ? <span className="text-muted"> · {num(leg.leverage, 0)}x</span> : null}
        </V>
        <K>{t("groups.venuePosition")}</K>
        <V className={short ? "text-down" : ""} >
          {Number(leg.venueSize) > 0 ? `${leg.venueSide === "short" ? "-" : ""}${num(leg.venueSize)}` : t("groups.flat")}
          {short && <span>{t("groups.lessThanHedge")}</span>}
          {!short && venue !== undefined && venue > Number(leg.held) && <span className="text-muted">{t("groups.outsideHedge")}</span>}
        </V>
      </div>
    </div>
  );
}

const K = ({ children }: { children: ReactNode }) => <span className="text-muted">{children}</span>;
const V = ({ children, className }: { children: ReactNode; className?: string }) => <span className={cx("text-right", className)}>{children}</span>;

function Orders({ detail }: { detail: GroupDetail }) {
  const t = useT();
  const text = useHedgerText();
  const actions = new Map(detail.actions.map((action) => [action.id, action]));
  if (!detail.orders.length) return <div className="text-muted">{t("groups.noOrders")}</div>;
  return (
    <table className="w-full border-collapse">
      <thead><tr className="text-muted">{(["groups.orders.time", "groups.orders.action", "groups.orders.leg", "groups.orders.role", "common.side", "common.type", "groups.orders.qty", "groups.orders.filled", "groups.orders.avg", "groups.orders.slippage", "groups.orders.state"] as const).map((key) => t(key)).map((head, i) => <th key={head + i} className={cx("h-6 px-2 font-normal", i === 0 ? "text-left" : "text-right")}>{head}</th>)}</tr></thead>
      <tbody>
        {[...detail.orders].reverse().map((order) => {
          const ref = Number(order.refPrice);
          const avg = Number(order.avgPrice);
          const slip = order.type === "market" && ref > 0 && avg > 0 ? ((avg - ref) / ref) * 10_000 * (order.side === "buy" ? 1 : -1) : undefined;
          return (
            <tr key={order.clientOrderId} className="hover:bg-raised" title={text(order.error) || t("groups.orders.client", { id: order.clientOrderId }) + (order.orderId ? t("groups.orders.venue", { id: order.orderId }) : "")}>
              <td className="h-6 px-2 text-left">{time(Date.parse(order.createdAt))}</td>
              <td className="px-2 text-right">{(() => { const kind = actions.get(order.actionId)?.kind; return kind ? t(`groups.action.${kind}`) : ""; })()}</td>
              <td className="px-2 text-right">{order.leg + 1}</td>
              <td className="px-2 text-right text-muted">{t(`role.${order.role}`)}</td>
              <td className={cx("px-2 text-right", order.side === "buy" ? "text-up" : "text-down")}>{t(order.side === "buy" ? "common.buy" : "common.sell")}</td>
              <td className="px-2 text-right">{t(order.type === "limit" ? "common.limit" : "common.market")}{Number(order.price) > 0 ? ` @ ${num(order.price)}` : ""}</td>
              <td className="px-2 text-right">{num(order.qty)}</td>
              <td className="px-2 text-right">{num(order.filled)}</td>
              <td className="px-2 text-right">{avg > 0 ? num(order.avgPrice) : "—"}</td>
              <td className={cx("px-2 text-right", slip !== undefined && slip > 5 ? "text-down" : "")}>{slip === undefined ? "—" : bpsText(slip, 1)}</td>
              <td className={cx("px-2 text-right", order.state === "rejected" || order.state === "unknown" || order.state === "unresolved" ? "text-down" : "")}>{stateLabel(t, order.state)}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function Events({ detail }: { detail: GroupDetail }) {
  const text = useHedgerText();
  return (
    <div className="max-h-56 overflow-y-auto">
      {detail.events.map((event) => (
        <div key={event.id} className="flex gap-3 py-0.5">
          <span className="shrink-0 text-muted">{time(Date.parse(event.at))}</span>
          <span className={event.level === "error" ? "text-down" : event.level === "warn" ? "text-text" : "text-text-2"}>{text(event.message)}</span>
        </div>
      ))}
    </div>
  );
}

function FundingLog({ detail }: { detail: GroupDetail }) {
  const t = useT();
  if (!detail.funding.length) return <div className="text-muted">{t("groups.noSettlements")}</div>;
  return (
    <table className="w-full border-collapse">
      <thead><tr className="text-muted">{(["groups.funding.settled", "groups.orders.leg", "groups.funding.rate", "common.size", "common.price", "groups.funding.amount"] as const).map((key) => t(key)).map((head, i) => <th key={head + i} className={cx("h-6 px-2 font-normal", i === 0 ? "text-left" : "text-right")}>{head}</th>)}</tr></thead>
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
  const t = useT();
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
      toast.push({ kind: "error", title: t("groups.notSaved"), detail: (error as Error).message });
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="flex max-w-xl flex-col gap-2">
      <input value={label} maxLength={80} onChange={(event) => setLabel(event.target.value)} className="h-8 rounded-lg border border-line bg-transparent px-3 outline-none focus:border-line-strong" />
      <textarea value={note} maxLength={2000} rows={3} onChange={(event) => setNote(event.target.value)} placeholder={t("groups.notePlaceholder")} className="rounded-lg border border-line bg-transparent px-3 py-2 outline-none placeholder:text-faint focus:border-line-strong" />
      <button onClick={save} disabled={saving} className="h-8 w-24 rounded-lg bg-raised hover:bg-line-strong/40 disabled:opacity-50">{saving ? t("common.saving") : t("common.save")}</button>
    </div>
  );
}

/** Close a fraction of the hedge at market, or rest one leg and hedge fills. */
function CloseDialog({ group, onDone }: { group: GroupView; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [percent, setPercent] = useState(100);
  const [mode, setMode] = useState<"market" | "limit">("market");
  const [maker, setMaker] = useState<0 | 1>(0);
  const [limitPrice, setLimitPrice] = useState("");
  // Post only applies to the resting order, chosen per resting leg.
  const [postOnlyByLeg, setPostOnlyByLeg] = useState<[boolean, boolean]>([true, true]);
  const postOnly = postOnlyByLeg[maker];
  const setPostOnly = (value: boolean) => setPostOnlyByLeg((current) => (maker === 0 ? [value, current[1]] : [current[0], value]));
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
      toast.push({ kind: "error", title: t("closeHedge.notStarted"), detail: (error as Error).message });
    } finally {
      setBusy(false);
    }
  };
  const fill = (kind: "join" | "touch") => {
    const value = kind === "join" ? makePrice(book, closeSide) : takePrice(book, closeSide);
    if (value) setLimitPrice(String(value));
  };
  return (
    <Dialog open onOpenChange={(open) => !open && onDone()} title={t("closeHedge.title")} description={group.label}>
      <div className="flex flex-col gap-3 text-left">
        <div className="flex items-center gap-3">
          <input type="range" min={1} max={100} value={percent} onChange={(event) => setPercent(Number(event.target.value))} className="range flex-1" style={{ ["--fill" as string]: `${percent}%` }} />
          <span className="w-12 text-right">{percent}%</span>
        </div>
        <div className="flex gap-1">{[25, 50, 75, 100].map((value) => <button key={value} onClick={() => setPercent(value)} className={cx("h-7 flex-1 rounded-md", percent === value ? "bg-line-strong/60" : "bg-raised")}>{value}%</button>)}</div>
        <div className="text-muted">
          {t("closeHedge.explain", { qty: num(exit0), base: group.legView[0].base })}
          {group.match === "base" && t("closeHedge.exitSpread", { exit: bpsText(group.exitSpreadBps, 1), entry: bpsText(group.entrySpreadBps, 1) })}
        </div>
        <Tabs value={mode} onChange={setMode} options={[{ value: "market", label: t("hticket.marketBoth") }, { value: "limit", label: t("hticket.limitHedge") }]} className="[&>button]:flex-1" />
        {mode === "limit" && (
          <>
            <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5">
              {([0, 1] as const).map((leg) => (
                <button key={leg} onClick={() => setMaker(leg)} className={cx("rounded-md", maker === leg ? "bg-line-strong/60 text-text" : "text-muted")}>{t("hticket.restLeg", { n: leg + 1, venue: venueLabel(group.legView[leg].venue) })}</button>
              ))}
            </div>
            <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3">
              <span className="text-muted">{t("closeHedge.price", { side: t(closeSide === "buy" ? "common.buy" : "common.sell") })}</span>
              <input inputMode="decimal" value={limitPrice} onChange={(event) => setLimitPrice(event.target.value.replace(/[^\d.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
              <button onClick={() => fill("join")} className="text-accent">{t(closeSide === "buy" ? "hticket.join.buy" : "hticket.join.sell")}</button>
              <button onClick={() => fill("touch")} className="text-accent">{t(closeSide === "buy" ? "hticket.join.sell" : "hticket.join.buy")}</button>
            </label>
            <label className="flex items-center gap-2" title={t("hticket.postOnlyHint")}>
              <input type="checkbox" checked={postOnly} onChange={(event) => setPostOnly(event.target.checked)} className="accent-[#50d2c1]" />
              {t("hticket.postOnly")}
              <span className="text-muted">{t("hticket.postOnlyScope", { venue: venueLabel(makerLeg.venue) })}</span>
            </label>
          </>
        )}
        <PrimaryButton onClick={submit} disabled={busy || (mode === "limit" && !(Number(limitPrice) > 0))}>{busy ? t("closeHedge.starting") : t(mode === "market" ? "closeHedge.closeMarket" : "closeHedge.placeClose", { pct: percent })}</PrimaryButton>
      </div>
    </Dialog>
  );
}

/**
 * The venue's position in the leg's direction (negative when it is the other
 * way); undefined for spot legs and while the account stream is down.
 */
export function legVenueHeld(leg: Pick<LegView, "product" | "accountLive" | "venueSize" | "venueSide" | "side">): number | undefined {
  if (leg.product !== "perp" || !leg.accountLive) return undefined;
  const size = Number(leg.venueSize) || 0;
  if (size === 0) return 0;
  return (leg.venueSide === "long") === (leg.side === "buy") ? size : -size;
}

/** An order state in the interface language; unknown states pass through. */
function stateLabel(t: T, state: string): string {
  const key = `state.${state}` as Parameters<T>[0];
  const text = t(key);
  return text === key ? state : text;
}
