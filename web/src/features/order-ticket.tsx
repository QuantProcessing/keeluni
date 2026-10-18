import * as Popover from "@radix-ui/react-popover";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { api } from "@/api/client";
import type { Account, Instrument, Order } from "@/api/types";
import { instrumentName, num, price, snapToStep, usd, venueLabel } from "@/lib/format";
import { accountEquity, accountName, rulesFor, type Rules } from "@/lib/routes";
import { useInstrument, useLastPrice, useStats, useTicker } from "@/state/hooks";
import { useTradingAccount, useWorkspace } from "@/state/workspace";
import { Dialog, PrimaryButton, Row, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { submitOrder } from "./submit";

type OrderType = "market" | "limit";
type Tif = "gtc" | "ioc" | "post_only";
type Unit = "base" | "quote";

export function OrderTicket() {
  const { instrumentId, trackOrder, limitPriceHint } = useWorkspace();
  const { account, live, routes } = useTradingAccount();
  const instrument = useInstrument(instrumentId);
  // Orders follow the trading account's rules for this order book.
  const rules = instrument ? rulesFor(instrument, routes) : undefined;
  const broker = account?.kind === "broker";
  const perp = instrument?.product === "perp";
  const last = useLastPrice(instrumentId);
  const ticker = useTicker(instrumentId, perp);
  const stats = useStats(instrumentId).data;
  const toast = useToast();
  const queryClient = useQueryClient();

  const [type, setType] = useState<OrderType>("market");
  const [side, setSide] = useState<"buy" | "sell">("buy");
  const [size, setSize] = useState("");
  const [unit, setUnit] = useState<Unit>("base");
  const [limitPrice, setLimitPrice] = useState("");
  const [tif, setTif] = useState<Tif>("gtc");
  const [reduceOnly, setReduceOnly] = useState(false);
  const [percent, setPercent] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const [leverage, setLeverage] = useState<number | undefined>(undefined);
  const [leverageOpen, setLeverageOpen] = useState(false);

  useEffect(() => {
    setSize("");
    setPercent(0);
    setLimitPrice("");
    setReduceOnly(false);
    setLeverage(undefined);
  }, [instrumentId]);

  useEffect(() => {
    if (limitPriceHint) {
      setType("limit");
      setLimitPrice(limitPriceHint.price);
    }
  }, [limitPriceHint]);

  const markOrLast = Number(ticker?.mark ?? stats?.mark ?? last ?? 0);
  const refPrice = type === "limit" && Number(limitPrice) > 0 ? Number(limitPrice) : markOrLast;

  const position = live.positions?.find((item) => item.inst === instrumentId);
  const positionLeverage = position?.leverage ? Math.round(Number(position.leverage)) : undefined;
  const effectiveLeverage = leverage ?? positionLeverage;

  const quoteAsset = instrument?.settle || instrument?.quote || "";
  const available = useMemo(() => {
    // A broker account trades every venue from one pool.
    if (broker && live.margin?.available) return { amount: Number(live.margin.available), asset: "USD" };
    if (!instrument || !live.balances) return undefined;
    const market = perp ? `${instrument.venue}:perp:${instrument.settle || instrument.quote}` : `${instrument.venue}:spot`;
    const asset = perp || side === "buy" ? quoteAsset : instrument.base;
    // Binance USDT and USDC perps are one account: its balances carry the
    // USDT market's name. Prefer the exact market, then the venue's product.
    const same = (item: { asset: string }) => item.asset.toUpperCase() === asset.toUpperCase();
    const product = market.split(":").slice(0, 2).join(":").toUpperCase();
    const row = live.balances.find((item) => item.market.toUpperCase() === market.toUpperCase() && same(item))
      ?? live.balances.find((item) => item.market.toUpperCase().startsWith(product) && same(item))
      ?? live.balances.find(same);
    return row ? { amount: Number(row.available), asset } : { amount: 0, asset };
  }, [instrument, live.balances, live.margin, broker, perp, side, quoteAsset]);

  // Size in base units, snapped to the instrument step.
  const baseQty = useMemo(() => {
    if (!rules || !size) return "";
    const raw = unit === "base" ? size : refPrice > 0 ? String(Number(size) / refPrice) : "";
    if (!raw || !Number.isFinite(Number(raw))) return "";
    return snapToStep(Number(raw).toFixed(12).replace(/\.?0+$/, ""), rules.stepSize);
  }, [rules, size, unit, refPrice]);

  const notional = Number(baseQty) * refPrice;
  const margin = perp && effectiveLeverage ? notional / effectiveLeverage : undefined;

  const applyPercent = (value: number) => {
    setPercent(value);
    if (!available || !rules || refPrice <= 0) return;
    let maxBase: number;
    if (perp) maxBase = (available.amount * (effectiveLeverage ?? 1)) / refPrice;
    else maxBase = side === "buy" ? available.amount / refPrice : available.amount;
    const target = (maxBase * value) / 100;
    if (unit === "base") setSize(snapToStep(target.toFixed(12), rules.stepSize));
    else setSize((target * refPrice).toFixed(2));
  };

  const validation = validate(rules, baseQty, type, limitPrice, notional);

  const place = async () => {
    if (!account || !instrument || !rules || validation) return;
    setSubmitting(true);
    try {
      const outcome = await submitOrder(account.id, {
        inst: instrument.id, side, type, qty: baseQty,
        ...(type === "limit" ? { price: snapToStep(limitPrice, rules.tickSize), tif } : {}),
        ...(perp && reduceOnly ? { reduceOnly: true } : {}),
      });
      if (outcome.kind === "accepted") {
        trackOrder(account.id, outcome.order);
        announce(outcome.order);
        setSize("");
        setPercent(0);
        void queryClient.invalidateQueries({ queryKey: ["orders", account.id] });
      } else {
        toast.push({ kind: "error", title: outcome.kind === "rejected" ? "Order rejected" : "Order outcome unknown", detail: outcome.message });
      }
    } catch (error) {
      toast.push({ kind: "error", title: "Order failed", detail: (error as Error).message });
    } finally {
      setSubmitting(false);
    }
  };

  const announce = (order: Order) => {
    const verb = order.side === "buy" ? "Buy" : "Sell";
    const name = instrumentName(order.inst);
    if (order.status === "filled") {
      toast.push({ kind: order.side === "buy" ? "long" : "short", title: `${verb} ${num(order.filled)} ${name} filled`, detail: order.avgPrice ? `Average price ${num(order.avgPrice)}` : undefined });
    } else if (order.status === "open" || order.status === "partially_filled") {
      toast.push({ kind: "info", title: `${verb} ${num(order.qty)} ${name} placed`, detail: order.price ? `Limit ${num(order.price)}` : undefined });
    } else if (order.status === "pending") {
      // Accepted by a broker, not yet on the exchange: it can still be refused.
      toast.push({ kind: "info", title: `${verb} ${num(order.qty)} ${name} accepted`, detail: "Waiting for the exchange" });
    } else {
      toast.push({ kind: "info", title: `${verb} ${num(order.qty)} ${name} submitted`, detail: `Status: ${order.status}` });
    }
  };

  if (!account) {
    return (
      <div className="flex flex-col gap-3 bg-surface p-3">
        {instrument && <NoAccount instrument={instrument} />}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3 bg-surface p-3">
      <AccountRow account={account} />
      {perp && (
        <div className="grid grid-cols-2 gap-2">
          <div className="flex h-8 items-center justify-center rounded-lg bg-raised text-muted" title="Margin mode is managed on the venue">Cross</div>
          <button
            disabled={!account}
            onClick={() => setLeverageOpen(true)}
            className="h-8 rounded-lg bg-raised hover:bg-line-strong/40 disabled:text-muted"
          >
            {effectiveLeverage ? `${effectiveLeverage}x` : "Leverage"}
          </button>
        </div>
      )}

      <Tabs value={type} onChange={setType} options={[{ value: "market", label: "Market" }, { value: "limit", label: "Limit" }]} className="[&>button]:flex-1" />

      <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5">
        <button onClick={() => setSide("buy")} className={cx("rounded-md", side === "buy" ? "bg-up text-on-accent" : "text-text")}>
          {perp ? "Buy / Long" : "Buy"}
        </button>
        <button onClick={() => setSide("sell")} className={cx("rounded-md", side === "sell" ? "bg-down text-on-accent" : "text-text")}>
          {perp ? "Sell / Short" : "Sell"}
        </button>
      </div>

      <div>
        <Row label="Available to Trade">{available ? `${num(available.amount)} ${available.asset}` : "—"}</Row>
        {perp && <Row label="Current Position">{position ? <span className={position.side === "long" ? "text-up" : "text-down"}>{(position.side === "short" ? "-" : "") + num(position.size)} {instrument?.base}</span> : `0 ${instrument?.base ?? ""}`}</Row>}
      </div>

      {type === "limit" && (
        <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
          <span className="text-muted">Price ({instrument?.quote})</span>
          <input inputMode="decimal" value={limitPrice} onChange={(e) => setLimitPrice(sanitize(e.target.value))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
          <button className="text-accent" onClick={() => { const mid = midPrice(); if (mid) setLimitPrice(mid); }}>Mid</button>
        </label>
      )}

      <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
        <span className="text-muted">Size</span>
        <input inputMode="decimal" value={size} onChange={(e) => { setSize(sanitize(e.target.value)); setPercent(0); }} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
        <select
          value={unit}
          onChange={(e) => { setUnit(e.target.value as Unit); setSize(""); setPercent(0); }}
          className="bg-transparent text-text outline-none"
          aria-label="Size unit"
        >
          <option value="base">{instrument?.base ?? "Base"}</option>
          <option value="quote">{instrument?.quote ?? "Quote"}</option>
        </select>
      </label>

      <div className="flex items-center gap-3">
        <input
          type="range" min={0} max={100} step={1} value={percent}
          onChange={(e) => applyPercent(Number(e.target.value))}
          className="range flex-1" style={{ ["--fill" as string]: `${percent}%` }}
          disabled={!available || available.amount <= 0}
          aria-label="Size as percent of available"
        />
        <div className="flex h-8 w-16 items-center justify-end gap-1 rounded-lg border border-line px-2">
          <span>{percent}</span><span className="text-muted">%</span>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        {perp && (
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={reduceOnly} onChange={(e) => setReduceOnly(e.target.checked)} className="accent-[#50d2c1]" />
            Reduce Only
          </label>
        )}
        {type === "limit" && (
          <label className="ml-auto flex items-center gap-2 text-muted">
            TIF
            <select value={tif} onChange={(e) => setTif(e.target.value as Tif)} className="bg-transparent text-text outline-none">
              <option value="gtc">GTC</option>
              <option value="ioc">IOC</option>
              <option value="post_only">Post Only</option>
            </select>
          </label>
        )}
      </div>

      <PrimaryButton
        onClick={place}
        disabled={submitting || !!validation || !instrument}
        className={side === "sell" ? "bg-down hover:bg-down/80" : undefined}
      >
        {submitting ? "Placing…" : validation && size ? validation : type === "market" ? `Market ${side === "buy" ? "Buy" : "Sell"}` : `Place ${side === "buy" ? "Buy" : "Sell"} Order`}
      </PrimaryButton>

      <div className="border-t border-line pt-2">
        <Row label="Order Value">{notional > 0 ? usd(notional) : "N/A"}</Row>
        {perp && <Row label="Margin Required" hint="Order value divided by leverage">{margin ? usd(margin) : "N/A"}</Row>}
        {perp && position?.liquidationPrice && <Row label="Liquidation Price">{price(position.liquidationPrice, rules?.tickSize)}</Row>}
        {type === "market" && <Row label="Est. Price" hint="Mark or last trade price; the fill may differ">{refPrice ? price(refPrice, rules?.tickSize) : "—"}</Row>}
        {broker && rules && <Row label="Min Size" hint="Gate CrossEx's own minimum for this market">{rules.minQty}{rules.minNotional ? ` · ${usd(rules.minNotional, 0)}` : ""}</Row>}
      </div>

      {instrument && perp && (
        <LeverageDialog
          open={leverageOpen}
          onOpenChange={setLeverageOpen}
          instrument={instrument}
          accountId={account.id}
          current={effectiveLeverage ?? 10}
          onApplied={setLeverage}
        />
      )}
    </div>
  );

  function midPrice(): string | undefined {
    if (!rules || !markOrLast) return undefined;
    return snapToStep(String(markOrLast), rules.tickSize);
  }
}

function sanitize(value: string): string {
  const cleaned = value.replace(/,/g, ".").replace(/[^\d.]/g, "");
  const [whole, ...rest] = cleaned.split(".");
  return rest.length ? `${whole}.${rest.join("")}` : whole ?? "";
}

export function validate(rules: Rules | undefined, baseQty: string, type: OrderType, limitPrice: string, notional: number): string | null {
  if (!rules) return "Loading market";
  if (!baseQty || Number(baseQty) <= 0) return "Enter a size";
  if (Number(baseQty) < Number(rules.minQty)) return `Min size ${rules.minQty}`;
  if (type === "limit" && !(Number(limitPrice) > 0)) return "Enter a price";
  if (rules.minNotional && notional > 0 && notional < Number(rules.minNotional)) return `Min value ${rules.minNotional}`;
  return null;
}

/**
 * The account an order goes to. It follows the instrument's venue: the
 * account last chosen there, else the native account, else a broker. When
 * several accounts trade this order book a menu lists them all; nothing is
 * switched without the user picking it.
 */
function AccountRow({ account }: { account: Account }) {
  const { candidates, chooseTradingAccount, live, unconnectedBrokers, openConnect, instrumentId } = useWorkspace();
  const [open, setOpen] = useState(false);
  const instrument = useInstrument(instrumentId);
  const choosable = candidates.length > 1 || unconnectedBrokers.length > 0;
  const status = live[account.id]?.status;
  const row = (
    <>
      <span className={cx("size-1.5 shrink-0 rounded-full", status ? (status.connected ? "bg-up" : "bg-down") : "bg-muted")} />
      <span className="truncate">{accountName(account)}</span>
      {account.kind === "broker" && <span className="shrink-0 text-muted">· Cross-exchange</span>}
    </>
  );
  return (
    <div className="flex h-8 items-center gap-2">
      <span className="shrink-0 text-muted">Account</span>
      {choosable ? (
        <Popover.Root open={open} onOpenChange={setOpen}>
          <Popover.Trigger
            aria-label="Trading account"
            className={cx("ml-auto flex h-8 min-w-0 items-center gap-2 rounded-lg px-2.5 hover:bg-raised", open && "bg-raised")}
          >
            {row}
            <span className="shrink-0 text-muted">{open ? "▴" : "▾"}</span>
          </Popover.Trigger>
          <Popover.Portal>
            <Popover.Content align="end" sideOffset={4} className="z-40 w-[288px] rounded-[10px] border border-line bg-overlay py-1">
              <div className="px-3 pt-1.5 pb-1 text-muted">Trade {instrument?.symbol} with</div>
              {candidates.map((item) => (
                <AccountOption key={item.id} account={item} selected={item.id === account.id} onChoose={() => { chooseTradingAccount(item.id); setOpen(false); }} />
              ))}
              {unconnectedBrokers.map((broker) => (
                <button
                  key={broker}
                  onClick={() => { setOpen(false); openConnect(broker); }}
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-accent hover:bg-raised"
                >
                  + Connect {venueLabel(broker)}
                  <span className="ml-auto text-muted">one pool, many venues</span>
                </button>
              ))}
            </Popover.Content>
          </Popover.Portal>
        </Popover.Root>
      ) : (
        <span className="ml-auto flex min-w-0 items-center gap-2 px-2.5">{row}</span>
      )}
    </div>
  );
}

function AccountOption({ account, selected, onChoose }: { account: Account; selected: boolean; onChoose: () => void }) {
  const { live } = useWorkspace();
  const equity = accountEquity(account, live[account.id]);
  return (
    <button onClick={onChoose} className={cx("flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-raised", selected && "text-accent")}>
      <span className="w-3 shrink-0">{selected ? "✓" : ""}</span>
      <span className="flex min-w-0 flex-col">
        <span className="truncate">{accountName(account)}</span>
        <span className="text-muted">{account.kind === "broker" ? "Cross-exchange margin" : "Native"}</span>
      </span>
      <span className="ml-auto shrink-0 text-right">
        <span className="block">{equity ? usd(equity.available) : "—"}</span>
        <span className="block text-muted">available</span>
      </span>
    </button>
  );
}

/**
 * No connected account trades this order book. Say so in one sentence and
 * offer the accounts that would: the venue itself, and any broker that
 * reaches it.
 */
function NoAccount({ instrument }: { instrument: Instrument }) {
  const { venues, unconnectedBrokers, openConnect } = useWorkspace();
  const where = `${venueLabel(instrument.venue)} ${instrument.product === "perp" ? "perps" : "spot"}`;
  const native = venues.some((item) => item.venue === instrument.venue && item.kind === "native");
  return (
    <>
      <div className="flex items-baseline justify-between">
        <span className="text-md">{instrument.symbol}</span>
        <span className="text-muted">{venueLabel(instrument.venue)}</span>
      </div>
      <p className="text-text-2">No connected account trades {where}.</p>
      {native && <PrimaryButton onClick={() => openConnect(instrument.venue)}>Connect {venueLabel(instrument.venue)}</PrimaryButton>}
      {unconnectedBrokers.map((broker) => (
        <button key={broker} onClick={() => openConnect(broker)} className="h-10 rounded-lg border border-line text-text hover:bg-raised">
          Connect {venueLabel(broker)}
          <span className="ml-1.5 text-muted">· trades {venueLabel(instrument.venue)} from one pool</span>
        </button>
      ))}
      <p className="text-muted">The chart, book and trades stay live without an account.</p>
    </>
  );
}

function LeverageDialog({ open, onOpenChange, instrument, accountId, current, onApplied }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  instrument: Instrument;
  accountId: string;
  current: number;
  onApplied: (value: number) => void;
}) {
  const [value, setValue] = useState(current);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { if (open) { setValue(current); setError(""); } }, [open, current]);
  const max = 125;

  const apply = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await api.setLeverage(accountId, instrument.id, value);
      onApplied(result.leverage || value);
      onOpenChange(false);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Adjust Leverage" description={`Control the leverage used for ${instrument.base} positions on ${instrument.venue}. The venue enforces its own maximum.`}>
      <div className="flex items-center gap-4">
        <input type="range" min={1} max={max} value={value} onChange={(e) => setValue(Number(e.target.value))} className="range flex-1" style={{ ["--fill" as string]: `${((value - 1) / (max - 1)) * 100}%` }} />
        <div className="flex h-9 w-20 items-center gap-1 rounded-lg border border-line px-2">
          <input inputMode="numeric" value={value} onChange={(e) => setValue(Math.max(1, Math.min(max, Number(e.target.value.replace(/\D/g, "")) || 1)))} className="w-full bg-transparent text-right outline-none" />
          <span className="text-muted">x</span>
        </div>
      </div>
      <PrimaryButton className="mt-6" onClick={apply} disabled={busy}>{busy ? "Applying…" : "Confirm"}</PrimaryButton>
      {error && <div className="mt-3 text-center text-down">{error}</div>}
      <div className="mt-4 rounded-lg border border-down/60 bg-warn-bg px-3 py-2.5 text-center text-down">
        Note that setting a higher leverage increases the risk of liquidation.
      </div>
    </Dialog>
  );
}
