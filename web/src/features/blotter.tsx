import * as Popover from "@radix-ui/react-popover";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";

import { api } from "@/api/client";
import type { Account, Balance, Order, Position } from "@/api/types";
import { instrumentName, marketLabel, num, price, time, usd, venueLabel } from "@/lib/format";
import { accountName } from "@/lib/routes";
import { useInstruments } from "@/state/hooks";
import { originLabel, useWorkspace, type AccountFill, type AccountOrder } from "@/state/workspace";
import { Dialog, PrimaryButton, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { submitOrder } from "./submit";

type Tab = "positions" | "orders" | "fills" | "balances";

interface AccountPosition extends Position { account: string }
interface AccountBalance extends Balance { account: string }

/**
 * Positions, orders, fills and balances of every connected account in one
 * table each. An Account column says whose they are; the filter narrows to
 * one account. With a single account the column is omitted.
 */
export function Blotter() {
  const { accounts, live, openOrders, fills, accountFilter, openConnect } = useWorkspace();
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
            { value: "balances", label: "Balances" },
            { value: "positions", label: `Positions${positions.length ? ` (${positions.length})` : ""}` },
            { value: "orders", label: `Open Orders${orders.length ? ` (${orders.length})` : ""}` },
            { value: "fills", label: "Trade History" },
          ]}
        />
        {many && <AccountFilter />}
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {accounts.length === 0 ? (
          <Empty>
            No accounts connected. <button className="text-accent hover:underline" onClick={() => openConnect()}>Connect an exchange</button> to see positions and orders.
          </Empty>
        ) : tab === "positions" ? (
          <Positions positions={positions} loading={loading} many={many} />
        ) : tab === "orders" ? (
          <OpenOrders orders={orders} many={many} />
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
  const { accounts, accountFilter, setAccountFilter, live } = useWorkspace();
  const [open, setOpen] = useState(false);
  const current = accounts.find((account) => account.id === accountFilter);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className={cx("ml-auto flex items-center gap-1.5 px-3 whitespace-nowrap hover:text-text", current ? "text-text" : "text-muted")}>
        {current ? accountName(current) : "All Accounts"}
        <span className="text-muted">{open ? "▴" : "▾"}</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={2} className="z-40 min-w-[220px] rounded-[10px] border border-line bg-overlay py-1">
          <FilterOption selected={!current} onChoose={() => { setAccountFilter(""); setOpen(false); }}>All Accounts</FilterOption>
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
  const account = useAccount()(id);
  if (!account) return <td className={td}>{id}</td>;
  const routed = account.kind === "broker" && inst ? venueLabel(inst.split(":")[0] ?? "") : undefined;
  return (
    <td className={td} title={account.kind === "broker" ? "Cross-exchange margin account" : undefined}>
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

function Positions({ positions, loading, many }: { positions: AccountPosition[]; loading: boolean; many: boolean }) {
  const tick = useTick();
  const { setInstrumentId } = useWorkspace();
  const [closing, setClosing] = useState<AccountPosition | null>(null);
  if (loading && !positions.length) return <Empty><span className="text-muted">Loading positions…</span></Empty>;
  if (!positions.length) return <Empty>No open positions yet</Empty>;
  return (
    <>
      <Table head={["Coin", ...(many ? ["Account"] : []), "Size", "Position Value", "Entry Price", "Mark Price", "PNL (ROE %)", "Liq. Price", "Margin", "Close"]}>
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
              </td>
              {many && <AccountCell id={position.account} />}
              <td className={cx(td, color)}>{(position.side === "short" ? "-" : "") + num(position.size)}</td>
              <td className={td}>{usd(value)}</td>
              <td className={td}>{price(position.entryPrice, tick(position.inst))}</td>
              <td className={td}>{price(position.markPrice, tick(position.inst))}</td>
              <td className={cx(td, pnl >= 0 ? "text-up" : "text-down")}>
                {usd(pnl)}{roe !== undefined && ` (${(roe * 100).toFixed(1)}%)`}
              </td>
              <td className={td}>{price(position.liquidationPrice, tick(position.inst))}</td>
              <td className={td}>{position.margin ? usd(position.margin) : "—"}</td>
              <td className={td}>
                <button className="text-accent hover:underline" onClick={() => setClosing(position)}>Market</button>
              </td>
            </tr>
          );
        })}
      </Table>
      {closing && <ClosePosition position={closing} onDone={() => setClosing(null)} />}
    </>
  );
}

/** Closes on the account that holds the position, never another one. */
function ClosePosition({ position, onDone }: { position: AccountPosition; onDone: () => void }) {
  const account = useAccount()(position.account);
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const close = async () => {
    if (!account) return;
    setBusy(true);
    const outcome = await submitOrder(account.id, {
      inst: position.inst, side: position.side === "long" ? "sell" : "buy", type: "market", qty: position.size, reduceOnly: true,
    });
    setBusy(false);
    if (outcome.kind === "accepted") {
      toast.push({ kind: "info", title: `Closing ${num(position.size)} ${instrumentName(position.inst)}`, detail: outcome.order.avgPrice ? `Average price ${num(outcome.order.avgPrice)}` : accountName(account) });
      onDone();
    } else {
      toast.push({ kind: "error", title: "Close failed", detail: outcome.message });
    }
  };
  return (
    <Dialog open onOpenChange={(open) => !open && onDone()} title="Market Close"
      description={`This will close your ${position.side} ${num(position.size)} ${instrumentName(position.inst)} on ${account ? accountName(account) : position.account} at market with a reduce-only order.`}>
      <PrimaryButton onClick={close} disabled={busy || !account}>{busy ? "Closing…" : "Market Close"}</PrimaryButton>
    </Dialog>
  );
}

function OpenOrders({ orders, many }: { orders: AccountOrder[]; many: boolean }) {
  const tick = useTick();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { setInstrumentId } = useWorkspace();
  const [cancelling, setCancelling] = useState<string | null>(null);
  if (!orders.length) return <Empty>No open orders</Empty>;
  const cancel = async (order: AccountOrder) => {
    setCancelling(order.account + order.orderId);
    try {
      await api.cancel(order.account, order.inst, order.orderId);
      queryClient.setQueryData<Order[]>(["orders", order.account], (current = []) => current.filter((item) => item.orderId !== order.orderId));
    } catch (error) {
      toast.push({ kind: "error", title: "Cancel failed", detail: (error as Error).message });
    } finally {
      setCancelling(null);
    }
  };
  return (
    <Table head={["Time", "Coin", ...(many ? ["Account"] : []), "Type", "Side", "Price", "Size", "Filled", "Reduce Only", ""]}>
      {orders.map((order) => (
        <tr key={order.account + order.orderId} className="hover:bg-raised">
          <td className={tdFirst}>{time(order.createdAt ?? order.updatedAt ?? 0)}</td>
          <td className={td}><Coin inst={order.inst} onClick={() => setInstrumentId(order.inst)} /></td>
          {many && <AccountCell id={order.account} inst={order.inst} />}
          <td className={td}>
            {order.type === "limit" ? "Limit" : "Market"}{order.tif && order.tif !== "gtc" ? ` · ${order.tif.toUpperCase()}` : ""}
            {order.status === "pending" && <span className="ml-1.5 text-muted" title="Accepted, not yet on the exchange">Pending</span>}
            {order.origin && <span className="ml-1.5 text-down">{originLabel(order.origin)}</span>}
          </td>
          <td className={cx(td, order.side === "buy" ? "text-up" : "text-down")}>{order.side === "buy" ? "Buy" : "Sell"}</td>
          <td className={td}>{price(order.price, tick(order.inst))}</td>
          <td className={td}>{num(order.qty)}</td>
          <td className={td}>{num(order.filled)}</td>
          <td className={td}>{order.reduceOnly ? "Yes" : "No"}</td>
          <td className={td}>
            <button className="text-accent hover:underline disabled:text-muted" disabled={cancelling === order.account + order.orderId} onClick={() => cancel(order)}>
              {cancelling === order.account + order.orderId ? "Cancelling…" : "Cancel"}
            </button>
          </td>
        </tr>
      ))}
    </Table>
  );
}

function Fills({ fills, many }: { fills: AccountFill[]; many: boolean }) {
  const tick = useTick();
  if (!fills.length) return <Empty>No trades yet</Empty>;
  return (
    <Table head={["Time", "Coin", ...(many ? ["Account"] : []), "Side", "Price", "Size", "Trade Value", "Fee"]}>
      {fills.map((fill) => (
        <tr key={fill.account + fill.fillId} className="hover:bg-raised">
          <td className={tdFirst}>{time(fill.ts)}</td>
          <td className={td}><Coin inst={fill.inst} /></td>
          {many && <AccountCell id={fill.account} inst={fill.inst} />}
          <td className={cx(td, fill.side === "buy" ? "text-up" : "text-down")}>{fill.side === "buy" ? "Buy" : "Sell"}</td>
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
  if (loading && !balances.length) return <Empty><span className="text-muted">Loading balances…</span></Empty>;
  if (!balances.length) return <Empty>No balances</Empty>;
  return (
    <Table head={["Coin", ...(many ? ["Account"] : []), "Market", "Total", "Available", "In Use"]}>
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

/** A broker's single pool reads "Cross-exchange pool"; Binance perps are one USD-M account. */
function balanceMarket(market: string): string {
  if (!market.includes(":")) return "Cross-exchange pool";
  if (market === "binance:perp:USDT") return "Binance USD-M";
  return marketLabel(market);
}
