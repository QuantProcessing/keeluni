import * as Popover from "@radix-ui/react-popover";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState, type ReactNode } from "react";

import { api } from "@/api/client";
import { useHedgeHoldings, type HedgeHolding } from "@/api/hedge";
import { navigate } from "@/state/route";
import type { Account, Balance, Order, Position, Trigger } from "@/api/types";
import { instrumentName, marketLabel, num, price, snapToStep, time, usd, venueLabel } from "@/lib/format";
import { accountName, rulesFor } from "@/lib/routes";
import { useBook, useInstrument, useInstruments, useRoutes } from "@/state/hooks";
import { originLabel, useWorkspace, type AccountFill, type AccountOrder, type AccountTrigger } from "@/state/workspace";
import { Dialog, PrimaryButton, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { submitOrder } from "./submit";
import { TriggerDialog, TriggerSummary, triggersOf } from "./triggers";
import { translate, useT } from "@/i18n";

type Tab = "positions" | "orders" | "fills" | "balances";

interface AccountPosition extends Position { account: string }
interface AccountBalance extends Balance { account: string }

/**
 * Positions, orders, fills and balances of every connected account in one
 * table each. An Account column says whose they are; the filter narrows to
 * one account. With a single account the column is omitted.
 */
export function Blotter() {
  const t = useT();
  const { accounts, live, openOrders, triggers: allTriggers, fills, accountFilter, openConnect } = useWorkspace();
  const [tab, setTab] = useState<Tab>("positions");
  const shown = accountFilter ? accounts.filter((account) => account.id === accountFilter) : accounts;
  const ids = new Set(shown.map((account) => account.id));
  const loading = shown.some((account) => live[account.id]?.positions === undefined);

  const positions = useMemo<AccountPosition[]>(() => shown.flatMap((account) => (live[account.id]?.positions ?? []).map((position) => ({ ...position, account: account.id }))),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [shown.map((account) => account.id).join(), live]);
  const balances = useMemo<AccountBalance[]>(() => shown.flatMap((account) => (live[account.id]?.balances ?? []).map((balance) => ({ ...balance, account: account.id }))),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [shown.map((account) => account.id).join(), live]);
  const orders = openOrders.filter((order) => ids.has(order.account));
  const triggers = allTriggers.filter((trigger) => ids.has(trigger.account));
  const trades = fills.filter((fill) => ids.has(fill.account));
  const many = accounts.length > 1;

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex shrink-0 items-stretch border-b border-line">
        <Tabs
          value={tab}
          onChange={setTab}
          className="border-b-0"
          options={[
            { value: "balances", label: t("blotter.balances") },
            { value: "positions", label: `${t("blotter.positions")}${positions.length ? ` (${positions.length})` : ""}` },
            { value: "orders", label: `${t("blotter.openOrders")}${orders.length + triggers.length ? ` (${orders.length + triggers.length})` : ""}` },
            { value: "fills", label: t("blotter.tradeHistory") },
          ]}
        />
        {many && <AccountFilter />}
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {accounts.length === 0 ? (
          <Empty>
            {t("blotter.noAccounts")} <button className="text-accent hover:underline" onClick={() => openConnect()}>{t("blotter.connectExchange")}</button> {t("blotter.noAccountsTail")}
          </Empty>
        ) : tab === "positions" ? (
          <Positions positions={positions} triggers={triggers} loading={loading} many={many} />
        ) : tab === "orders" ? (
          <OpenOrders orders={orders} triggers={triggers} many={many} />
        ) : tab === "fills" ? (
          <Fills fills={trades} many={many} />
        ) : (
          <Balances balances={balances} loading={shown.some((account) => live[account.id]?.balances === undefined)} many={many} />
        )}
      </div>
    </div>
  );
}

/** All accounts, or one. Lives at the right end of the tab row. */
function AccountFilter() {
  const t = useT();
  const { accounts, accountFilter, setAccountFilter, live } = useWorkspace();
  const [open, setOpen] = useState(false);
  const current = accounts.find((account) => account.id === accountFilter);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className={cx("ml-auto flex items-center gap-1.5 px-3 whitespace-nowrap hover:text-text", current ? "text-text" : "text-muted")}>
        {current ? accountName(current) : t("blotter.allAccounts")}
        <span className="text-muted">{open ? "▴" : "▾"}</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={2} className="z-40 min-w-[220px] rounded-[10px] border border-line bg-overlay py-1">
          <FilterOption selected={!current} onChoose={() => { setAccountFilter(""); setOpen(false); }}>{t("blotter.allAccounts")}</FilterOption>
          {accounts.map((account) => (
            <FilterOption key={account.id} selected={account.id === accountFilter} onChoose={() => { setAccountFilter(account.id); setOpen(false); }}>
              <span className={cx("size-1.5 rounded-full", live[account.id]?.status?.connected ? "bg-up" : "bg-down")} />
              {accountName(account)}
            </FilterOption>
          ))}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function FilterOption({ selected, onChoose, children }: { selected: boolean; onChoose: () => void; children: ReactNode }) {
  return (
    <button onClick={onChoose} className={cx("flex w-full items-center gap-2 px-3 py-[5px] text-left leading-6 hover:bg-raised", selected ? "text-accent" : "text-text")}>
      <span className="w-3">{selected ? "✓" : ""}</span>
      {children}
    </button>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <div className="px-3 py-2.5 text-text">{children}</div>;
}

function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <table className="w-full min-w-[760px] border-collapse">
      <thead className="sticky top-0 bg-surface">
        <tr>{head.map((h, i) => <th key={h + i} className={cx("h-8 px-3 font-normal text-muted", i === 0 ? "text-left" : "text-right")}>{h}</th>)}</tr>
      </thead>
      <tbody className="text-text-2">{children}</tbody>
    </table>
  );
}

const td = "h-7 px-3 text-right whitespace-nowrap";
const tdFirst = "h-7 px-3 text-left whitespace-nowrap";

function useTick() {
  const { data } = useInstruments();
  return (id: string) => data?.find((item) => item.id === id)?.tickSize;
}

function useAccount() {
  const { accounts } = useWorkspace();
  return (id: string): Account | undefined => accounts.find((account) => account.id === id);
}

/** The account cell: name, and for a broker the venue it routed to. */
function AccountCell({ id, inst }: { id: string; inst?: string }) {
  const t = useT();
  const account = useAccount()(id);
  if (!account) return <td className={td}>{id}</td>;
  const routed = account.kind === "broker" && inst ? venueLabel(inst.split(":")[0] ?? "") : undefined;
  return (
    <td className={td} title={account.kind === "broker" ? t("blotter.brokerAccount") : undefined}>
      {accountName(account)}
      {routed && <span className="ml-1.5 text-muted">→ {routed}</span>}
    </td>
  );
}

/** "BTC-USDT" plus its venue: rows of several venues share a table. */
function Coin({ inst, className, onClick }: { inst: string; className?: string; onClick?: () => void }) {
  const venue = venueLabel(inst.split(":")[0] ?? "");
  return (
    <>
      {onClick ? <button className={cx(className, "hover:underline")} onClick={onClick}>{instrumentName(inst)}</button> : <span className={className}>{instrumentName(inst)}</span>}
      <span className="ml-1.5 text-muted">{venue}</span>
    </>
  );
}

function Positions({ positions, triggers, loading, many }: { positions: AccountPosition[]; triggers: AccountTrigger[]; loading: boolean; many: boolean }) {
  const t = useT();
  const tick = useTick();
  const { setInstrumentId, routes } = useWorkspace();
  const [closing, setClosing] = useState<{ position: AccountPosition; type: "market" | "limit" } | null>(null);
  const [protecting, setProtecting] = useState<AccountPosition | null>(null);
  // Take-profit / stop-loss where the account's route has trigger orders;
  // elsewhere a position closes by a limit (or market) order only.
  const supports = (position: AccountPosition) => Boolean(routes[position.account]?.find((route) => route.inst === position.inst)?.triggers);
  const anyTriggers = positions.some(supports);
  const holdings = useHedgeHoldings();
  if (loading && !positions.length) return <Empty><span className="text-muted">{t("blotter.loadingPositions")}</span></Empty>;
  if (!positions.length) return <Empty>{t("blotter.noPositions")}</Empty>;
  return (
    <>
      <Table head={[t("common.coin"), ...(many ? [t("common.account")] : []), t("common.size"), t("blotter.positionValue"), t("blotter.entryPrice"), t("blotter.markPrice"), t("blotter.pnl"), t("blotter.liqPrice"), t("blotter.margin"), ...(anyTriggers ? [t("tpsl.column")] : []), t("blotter.close")]}>
        {positions.map((position) => {
          const value = Number(position.size) * Number(position.markPrice ?? position.entryPrice);
          const pnl = Number(position.unrealizedPnl);
          const margin = Number(position.margin);
          const roe = margin > 0 ? pnl / margin : undefined;
          const color = position.side === "long" ? "text-up" : "text-down";
          return (
            <tr key={position.account + position.inst + position.side} className="hover:bg-raised">
              <td className={tdFirst}>
                <Coin inst={position.inst} className={color} onClick={() => setInstrumentId(position.inst)} />
                {position.leverage && <span className="ml-1.5 text-muted">{num(position.leverage, 0)}x</span>}
                <HedgeMark holdings={holdings.get(`${position.account}|${position.inst}`)} />
              </td>
              {many && <AccountCell id={position.account} />}
              <td className={cx(td, color)}>{(position.side === "short" ? "-" : "") + num(position.size)}</td>
              <td className={td}>{usd(value)}</td>
              <td className={td}>{price(position.entryPrice, tick(position.inst))}</td>
              <td className={td}>{price(position.markPrice, tick(position.inst))}</td>
              <td className={cx(td, pnl >= 0 ? "text-up" : "text-down")}>
                {usd(pnl)}{roe !== undefined && ` (${(roe * 100).toFixed(1)}%)`}
              </td>
              <td className={td}><LiquidationCell position={position} /></td>
              <td className={td}>{position.margin ? usd(position.margin) : "—"}</td>
              {anyTriggers && (
                <td className={td}>
                  {supports(position) ? (
                    <button className="group inline-flex items-center gap-1.5" onClick={() => setProtecting(position)} title={t("tpsl.edit")}>
                      <TriggerSummary triggers={triggersOf(triggers, position)} tick={tick(position.inst)} />
                      <span className="text-accent group-hover:underline">{t("tpsl.set")}</span>
                    </button>
                  ) : (
                    <span className="text-muted" title={t("tpsl.unsupported")}>—</span>
                  )}
                </td>
              )}
              <td className={td}>
                <span className="inline-flex gap-3">
                  <button className="text-accent hover:underline" onClick={() => setClosing({ position, type: "market" })}>{t("blotter.marketClose")}</button>
                  <button className="text-accent hover:underline" onClick={() => setClosing({ position, type: "limit" })}>{t("blotter.limitClose")}</button>
                </span>
              </td>
            </tr>
          );
        })}
      </Table>
      {protecting && <TriggerDialog position={protecting} account={protecting.account} triggers={triggersOf(triggers, protecting)} onDone={() => setProtecting(null)} />}
      {closing && <ClosePosition position={closing.position} type={closing.type} hedges={holdings.get(`${closing.position.account}|${closing.position.inst}`)} onDone={() => setClosing(null)} />}
    </>
  );
}

/**
 * The liquidation price, or for a cross-margin pool (Gate CrossEx) the
 * account-level risk: the pool is liquidated as a whole when its maintenance
 * margin rate falls to 100%, so no single position has a price of its own.
 */
function LiquidationCell({ position }: { position: AccountPosition }) {
  const t = useT();
  const tick = useTick();
  const account = useAccount()(position.account);
  const { live } = useWorkspace();
  if (position.liquidationPrice) return <>{price(position.liquidationPrice, tick(position.inst))}</>;
  if (account?.kind !== "broker") return <>—</>;
  const rate = Number(live[position.account]?.margin?.maintenanceMarginRate);
  const shown = Number.isFinite(rate) && rate > 0 ? `${(rate * 100).toFixed(0)}%` : "—";
  return (
    <span className="dotted text-muted" title={t("blotter.poolLiqHint", { rate: shown })}>
      {t("blotter.poolLiq", { rate: shown })}
    </span>
  );
}

/** "Hedge · label": the position is (partly) a leg of an open hedge. */
function HedgeMark({ holdings }: { holdings: HedgeHolding[] | undefined }) {
  const t = useT();
  if (!holdings?.length) return null;
  const names = holdings.map((item) => item.label).join(", ");
  return (
    <button
      onClick={(event) => { event.stopPropagation(); navigate("/hedge"); }}
      title={t("blotter.hedgeMarkHint", { names, detail: holdings.map((item) => t("blotter.hedgeHeld", { qty: num(item.held), label: item.label })).join(", ") })}
      className={cx("ml-1.5 rounded px-1 text-muted hover:text-text", holdings.some((item) => item.status === "broken") && "text-down")}
    >
      {t("blotter.hedgeMark", { names })}
    </button>
  );
}

/**
 * Closes on the account that holds the position, never another one, with a
 * reduce-only order: at market, or a limit at a price (prefilled from the
 * other side of the book) for a share of the position. A position that is a
 * hedge leg can still be closed here, after a warning: the hedge is then left
 * one-sided and the hedger marks it Unhedged.
 */
function ClosePosition({ position, type, hedges, onDone }: { position: AccountPosition; type: "market" | "limit"; hedges?: HedgeHolding[]; onDone: () => void }) {
  const t = useT();
  const account = useAccount()(position.account);
  const instrument = useInstrument(position.inst);
  const routes = useRoutes(position.account).data;
  const rules = instrument ? rulesFor(instrument, routes) : undefined;
  const book = useBook(type === "limit" ? position.inst : "");
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [share, setShare] = useState(100);
  const [limit, setLimit] = useState("");
  const side = position.side === "long" ? "sell" : "buy";
  // A long closes by selling: the resting price joins the asks; a short the bids.
  const join = side === "sell" ? book?.asks[0]?.[0] : book?.bids[0]?.[0];
  useEffect(() => {
    if (type === "limit" && !limit && join) setLimit(join);
  }, [type, join, limit]);
  const qty = share >= 100 ? position.size : snapToStep(String((Number(position.size) * share) / 100), rules?.stepSize ?? "0.00000001");
  const tooSmall = !(Number(qty) > 0) || (rules?.minQty !== undefined && Number(qty) < Number(rules.minQty));
  const close = async () => {
    if (!account) return;
    setBusy(true);
    const outcome = await submitOrder(account.id, type === "market"
      ? { inst: position.inst, side, type: "market", qty, reduceOnly: true }
      : { inst: position.inst, side, type: "limit", qty, price: snapToStep(limit, rules?.tickSize ?? "0.00000001"), tif: "gtc", reduceOnly: true });
    setBusy(false);
    if (outcome.kind === "accepted") {
      toast.push({
        kind: "info", title: t(type === "market" ? "close.closingTitle" : "close.limitPlaced", { qty: num(qty), name: instrumentName(position.inst), price: limit }),
        detail: outcome.order.avgPrice ? t("order.averagePrice", { price: num(outcome.order.avgPrice) }) : accountName(account),
      });
      onDone();
    } else {
      toast.push({ kind: "error", title: t("close.failed"), detail: outcome.message });
    }
  };
  const field = "flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong";
  return (
    <Dialog open onOpenChange={(open) => !open && onDone()} title={t(type === "market" ? "close.title" : "close.limitTitle")}
      description={t("close.description", { side: t(position.side === "long" ? "side.long" : "side.short"), qty: num(position.size), name: instrumentName(position.inst), account: account ? accountName(account) : position.account })}>
      {hedges?.length ? (
        <div role="alert" className="mb-3 rounded-lg border border-down/60 bg-warn-bg px-3 py-2 text-down">
          {t("close.hedgeWarning", { names: hedges.map((item) => item.label).join(", ") })}
          <button onClick={() => { onDone(); navigate("/hedge"); }} className="mt-1 block text-accent hover:underline">{t("close.openHedge")}</button>
        </div>
      ) : null}
      <div className="flex flex-col gap-3">
        {type === "limit" && (
          <label className={field}>
            <span className="text-muted">{t("common.price")}</span>
            <input inputMode="decimal" autoFocus value={limit} onChange={(event) => setLimit(event.target.value.replace(/[^\d.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
            <span className="text-muted">{instrument?.quote}</span>
          </label>
        )}
        <div className="flex items-center gap-2">
          <span className="text-muted">{t("common.size")}</span>
          <span className="ml-auto tabular-nums">{num(qty)} {instrument?.base}</span>
        </div>
        <div className="grid grid-cols-4 gap-1">
          {[25, 50, 75, 100].map((value) => (
            <button key={value} onClick={() => setShare(value)} className={cx("h-7 rounded-md", share === value ? "bg-line-strong/60 text-text" : "bg-raised text-muted hover:text-text")}>{value}%</button>
          ))}
        </div>
        <PrimaryButton onClick={close} disabled={busy || !account || tooSmall || (type === "limit" && !(Number(limit) > 0))}>
          {busy ? t("close.closing") : t(type === "market" ? "close.confirmMarket" : "close.confirmLimit")}
        </PrimaryButton>
        {tooSmall && rules && <div className="text-center text-muted">{t("ticket.minSizeIs", { min: rules.minQty })}</div>}
      </div>
    </Dialog>
  );
}

function OpenOrders({ orders, triggers, many }: { orders: AccountOrder[]; triggers: AccountTrigger[]; many: boolean }) {
  const t = useT();
  const tick = useTick();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { setInstrumentId } = useWorkspace();
  const [cancelling, setCancelling] = useState<string | null>(null);
  if (!orders.length && !triggers.length) return <Empty>{t("blotter.noOrders")}</Empty>;
  const cancelTrigger = async (trigger: AccountTrigger) => {
    setCancelling(trigger.account + "/t/" + trigger.orderId);
    try {
      await api.cancelTrigger(trigger.account, trigger.inst, trigger.orderId);
      queryClient.setQueryData<Trigger[]>(["triggers", trigger.account], (current = []) => current.filter((item) => item.orderId !== trigger.orderId));
    } catch (error) {
      toast.push({ kind: "error", title: t("blotter.cancelFailed"), detail: (error as Error).message });
    } finally {
      setCancelling(null);
    }
  };
  const cancel = async (order: AccountOrder) => {
    setCancelling(order.account + order.orderId);
    try {
      await api.cancel(order.account, order.inst, order.orderId);
      queryClient.setQueryData<Order[]>(["orders", order.account], (current = []) => current.filter((item) => item.orderId !== order.orderId));
    } catch (error) {
      toast.push({ kind: "error", title: t("blotter.cancelFailed"), detail: (error as Error).message });
    } finally {
      setCancelling(null);
    }
  };
  return (
    <Table head={[t("common.time"), t("common.coin"), ...(many ? [t("common.account")] : []), t("common.type"), t("common.side"), t("common.price"), t("common.size"), t("blotter.filled"), t("blotter.reduceOnly"), ""]}>
      {orders.map((order) => (
        <tr key={order.account + order.orderId} className="hover:bg-raised">
          <td className={tdFirst}>{time(order.createdAt ?? order.updatedAt ?? 0)}</td>
          <td className={td}><Coin inst={order.inst} onClick={() => setInstrumentId(order.inst)} /></td>
          {many && <AccountCell id={order.account} inst={order.inst} />}
          <td className={td}>
            {t(order.type === "limit" ? "common.limit" : "common.market")}{order.tif && order.tif !== "gtc" ? ` · ${order.tif === "post_only" ? t("ticket.postOnly") : order.tif.toUpperCase()}` : ""}
            {order.status === "pending" && <span className="ml-1.5 text-muted" title={t("blotter.pendingHint")}>{t("blotter.pending")}</span>}
            {order.origin && <span className="ml-1.5 text-down">{originLabel(order.origin)}</span>}
          </td>
          <td className={cx(td, order.side === "buy" ? "text-up" : "text-down")}>{t(order.side === "buy" ? "common.buy" : "common.sell")}</td>
          <td className={td}>{price(order.price, tick(order.inst))}</td>
          <td className={td}>{num(order.qty)}</td>
          <td className={td}>{num(order.filled)}</td>
          <td className={td}>{t(order.reduceOnly ? "common.yes" : "common.no")}</td>
          <td className={td}>
            <button className="text-accent hover:underline disabled:text-muted" disabled={cancelling === order.account + order.orderId} onClick={() => cancel(order)}>
              {cancelling === order.account + order.orderId ? t("common.cancelling") : t("common.cancel")}
            </button>
          </td>
        </tr>
      ))}
      {triggers.map((trigger) => {
        const key = trigger.account + "/t/" + trigger.orderId;
        return (
          <tr key={key} className="hover:bg-raised">
            <td className={tdFirst}>{trigger.createdAt ? time(trigger.createdAt) : "—"}</td>
            <td className={td}><Coin inst={trigger.inst} onClick={() => setInstrumentId(trigger.inst)} /></td>
            {many && <AccountCell id={trigger.account} inst={trigger.inst} />}
            <td className={td} title={t("tpsl.typeHint")}>{t(trigger.kind === "take_profit" ? "tpsl.takeProfit" : "tpsl.stopLoss")} · {t("common.market")}</td>
            <td className={cx(td, trigger.side === "buy" ? "text-up" : "text-down")}>{t(trigger.side === "buy" ? "common.buy" : "common.sell")}</td>
            <td className={td}>
              <span className="text-muted">{t(trigger.priceType === "mark" ? "tpsl.markTrigger" : "tpsl.lastTrigger")} </span>
              {price(trigger.triggerPrice, tick(trigger.inst))}
            </td>
            <td className={td}>{trigger.closePosition ? t("tpsl.wholePosition") : num(trigger.qty)}</td>
            <td className={td}>—</td>
            <td className={td}>{t("common.yes")}</td>
            <td className={td}>
              <button className="text-accent hover:underline disabled:text-muted" disabled={cancelling === key} onClick={() => cancelTrigger(trigger)}>
                {cancelling === key ? t("common.cancelling") : t("common.cancel")}
              </button>
            </td>
          </tr>
        );
      })}
    </Table>
  );
}

function Fills({ fills, many }: { fills: AccountFill[]; many: boolean }) {
  const t = useT();
  const tick = useTick();
  if (!fills.length) return <Empty>{t("blotter.noTrades")}</Empty>;
  return (
    <Table head={[t("common.time"), t("common.coin"), ...(many ? [t("common.account")] : []), t("common.side"), t("common.price"), t("common.size"), t("blotter.tradeValue"), t("common.fee")]}>
      {fills.map((fill) => (
        <tr key={fill.account + fill.fillId} className="hover:bg-raised">
          <td className={tdFirst}>{time(fill.ts)}</td>
          <td className={td}><Coin inst={fill.inst} /></td>
          {many && <AccountCell id={fill.account} inst={fill.inst} />}
          <td className={cx(td, fill.side === "buy" ? "text-up" : "text-down")}>{t(fill.side === "buy" ? "common.buy" : "common.sell")}</td>
          <td className={td}>{price(fill.price, tick(fill.inst))}</td>
          <td className={td}>{num(fill.qty)}</td>
          <td className={td}>{usd(Number(fill.price) * Number(fill.qty))}</td>
          <td className={td}>{num(fill.fee)} {fill.feeAsset}</td>
        </tr>
      ))}
    </Table>
  );
}

function Balances({ balances, loading, many }: { balances: AccountBalance[]; loading: boolean; many: boolean }) {
  const t = useT();
  if (loading && !balances.length) return <Empty><span className="text-muted">{t("blotter.loadingBalances")}</span></Empty>;
  if (!balances.length) return <Empty>{t("blotter.noBalances")}</Empty>;
  return (
    <Table head={[t("common.coin"), ...(many ? [t("common.account")] : []), t("blotter.market"), t("blotter.totalHead"), t("summary.available"), t("blotter.inUse")]}>
      {balances.map((balance) => (
        <tr key={balance.account + balance.market + balance.asset} className="hover:bg-raised">
          <td className={tdFirst}>{balance.asset}</td>
          {many && <AccountCell id={balance.account} />}
          <td className={td}>{balanceMarket(balance.market)}</td>
          <td className={td}>{num(balance.total)}</td>
          <td className={td}>{num(balance.available)}</td>
          <td className={td}>{num(balance.locked)}</td>
        </tr>
      ))}
    </Table>
  );
}

/**
 * A broker's single pool reads "Cross-exchange pool"; a venue whose products
 * share one wallet (Deribit, OKX) reads "<venue> unified account"; Binance
 * perps are one USD-M account.
 */
function balanceMarket(market: string): string {
  if (!market.includes(":")) return market === "crossex" ? translate("blotter.pool") : translate("blotter.unified", { venue: venueLabel(market) });
  if (market === "binance:perp:USDT") return translate("blotter.binanceUsdm");
  return marketLabel(market);
}
