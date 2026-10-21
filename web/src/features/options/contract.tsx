import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import type { OptionChain, OptionQuote, OptionRow } from "@/api/options";
import type { Account, Instrument, Level, Order } from "@/api/types";
import { decimals, num, snapToStep, usd, venueLabel } from "@/lib/format";
import { accountName } from "@/lib/routes";
import { useBook } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { PrimaryButton, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { useT } from "@/i18n";
import { submitOrder } from "../submit";
import { PayoffChart } from "./charts";
import { dteLabel, expiryLabel, impliedVolOf, priceAt, years, type PackageSummary, type Side } from "./model";

export type PriceBy = "price" | "iv";
export type Tif = "gtc" | "post_only" | "ioc";

/** A chain row with its live quote laid over the 10 s snapshot. */
export function liveRow(row: OptionRow, quote: OptionQuote | undefined): OptionRow & { forward?: number } {
  if (!quote) return row;
  return {
    ...row,
    bid: quote.bid ?? row.bid, ask: quote.ask ?? row.ask, mark: quote.mark ?? row.mark,
    bidIv: quote.bidIv ?? row.bidIv, askIv: quote.askIv ?? row.askIv, markIv: quote.markIv ?? row.markIv,
    delta: quote.delta ?? row.delta, gamma: quote.gamma ?? row.gamma, vega: quote.vega ?? row.vega, theta: quote.theta ?? row.theta,
    forward: Number(quote.underlying) || undefined,
  };
}

export function contractName(chain: OptionChain, row: OptionRow): string {
  return `${chain.underlying} ${expiryLabel(row.expiry)} ${num(Number(row.strike))} ${row.kind === "call" ? "C" : "P"}`;
}

function priceText(value: string | undefined, inverse: boolean): string {
  const n = Number(value);
  if (!(n > 0)) return "—";
  return inverse ? n.toFixed(4) : num(n, n >= 100 ? 1 : n >= 1 ? 2 : 4);
}

function ivText(value: number | undefined): string {
  return value && value > 0 ? `${(value * 100).toFixed(1)}%` : "";
}

/**
 * The selected option, beside the board: its live quote and greeks, its
 * book with each level's IV, and the payoff — of the order being written
 * in single mode, of the package in combo mode.
 */
export function ContractInfo({ chain, row, forward, discount, now, instrument, summary, payoffLabel }: {
  chain: OptionChain;
  row: OptionRow;
  forward: number;
  discount: number;
  now: number;
  instrument: Instrument | undefined;
  summary: PackageSummary | undefined;
  payoffLabel: string;
}) {
  const t = useT();
  const call = row.kind === "call";
  const inverse = !!chain.inverse;
  const strike = Number(row.strike);
  const cell = (label: string, value: string, sub: string, tone?: string, hint?: string) => (
    <div className="flex items-baseline justify-between gap-2" title={hint}>
      <span className={cx("text-muted", hint && "dotted")}>{label}</span>
      <span className="flex items-baseline gap-1.5 tabular-nums">
        <span className={tone ?? "text-text"}>{value}</span>
        {sub && <span className="text-xs text-muted">{sub}</span>}
      </span>
    </div>
  );
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-line px-3">
        <span className="text-text">{chain.underlying} {expiryLabel(row.expiry)} {num(strike)}</span>
        <span className={call ? "text-up" : "text-down"}>{call ? "Call" : "Put"}</span>
        <span className="ml-auto text-muted">{dteLabel(row.expiry, now)}</span>
      </div>
      <div className="grid shrink-0 grid-cols-2 gap-x-4 gap-y-1 border-b border-line px-3 py-2">
        {cell(t("options.bid"), priceText(row.bid, inverse), ivText(row.bidIv), "text-up")}
        {cell(t("options.ask"), priceText(row.ask, inverse), ivText(row.askIv), "text-down")}
        {cell(t("options.mark"), priceText(row.mark, inverse), ivText(row.markIv))}
        {cell(t("options.forward"), forward ? num(forward, forward >= 100 ? 0 : 2) : "—", "")}
        {cell("Δ", num(row.delta, 3), "", undefined, t("options.greek.delta"))}
        {cell("Γ 1%", usd(row.gamma * forward * forward * 0.01, 2), "", undefined, t("options.greek.gamma"))}
        {cell("Vega", usd(row.vega, 2), "", undefined, t("options.greek.vega"))}
        {cell("Θ/d", usd(row.theta, 2), "", undefined, t("options.greek.theta"))}
      </div>
      <ContractBook inst={row.inst} inverse={inverse} tick={instrument?.tickSize} base={chain.underlying}
        ivOf={(value) => ivText(impliedVolOf(call, forward, strike, years(row.expiry, now), inverse ? value * forward : value, discount))} />
      <div className="shrink-0 border-t border-line px-3 pt-2 pb-1">
        <div className="flex items-center justify-between">
          <span className="dotted text-muted" title={t("options.payoffHint")}>{t("options.payoff")}</span>
          <span className="truncate text-xs text-muted">{payoffLabel}</span>
        </div>
        <PayoffChart summary={summary} forward={forward} height={150} />
        {summary && (
          <div className="grid grid-cols-3 gap-1 pb-1 text-xs tabular-nums">
            <span className="text-muted">{t("options.maxGain")} <span className="text-up">{summary.unboundedGain ? "∞" : usd(summary.maxGain, 0)}</span></span>
            <span className="text-center text-muted">{t("options.maxLoss")} <span className="text-down">{summary.unboundedLoss ? "∞" : usd(summary.maxLoss, 0)}</span></span>
            <span className="text-right text-muted">{t("options.breakevenShort")} <span className="text-text">{summary.breakevens.length ? summary.breakevens.map((value) => num(value, 0)).join("/") : "—"}</span></span>
          </div>
        )}
      </div>
    </div>
  );
}

/** The option's book, a few levels a side, with each level's IV. */
function ContractBook({ inst, inverse, tick, base, ivOf }: {
  inst: string;
  inverse: boolean;
  tick: string | undefined;
  base: string;
  ivOf: (price: number) => string;
}) {
  const t = useT();
  const { hintLimitPrice } = useWorkspace();
  const book = useBook(inst);
  const digits = tick ? decimals(tick) : inverse ? 4 : 2;
  const asks = (book?.asks ?? []).slice(0, 6);
  const bids = (book?.bids ?? []).slice(0, 6);
  const max = Math.max(1, ...[...asks, ...bids].map((level) => Number(level[1])));
  const row = (level: Level, side: "bid" | "ask") => (
    <button key={side + level[0]} onClick={() => hintLimitPrice(level[0])} title={t("options.bookClickHint")}
      className="relative grid h-[22px] w-full shrink-0 grid-cols-3 items-center px-3 text-right hover:bg-raised">
      <span className={cx("absolute inset-y-0 right-0", side === "ask" ? "bg-down/15" : "bg-long/15")} style={{ width: `${(Number(level[1]) / max) * 100}%` }} />
      <span className={cx("relative text-left tabular-nums", side === "bid" ? "text-up" : "text-down")}>{num(level[0], digits)}</span>
      <span className="relative text-muted tabular-nums">{ivOf(Number(level[0]))}</span>
      <span className="relative tabular-nums">{num(level[1])}</span>
    </button>
  );
  const blank = (count: number) => Array.from({ length: Math.max(0, count) }, (_, index) => <div key={index} className="h-[22px] shrink-0" aria-hidden />);
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="grid h-7 shrink-0 grid-cols-3 items-center px-3 text-right text-xs text-muted">
        <span className="text-left">{t("common.price")}</span>
        <span>IV</span>
        <span>{t("book.sizeIn", { base })}</span>
      </div>
      {!book ? (
        <div className="flex flex-1 items-center justify-center text-muted">{t("book.loading")}</div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col justify-center overflow-hidden">
          {blank(6 - asks.length)}
          <div className="flex shrink-0 flex-col-reverse">{asks.map((level) => row(level, "ask"))}</div>
          <div className="h-px shrink-0 bg-line" />
          <div className="flex shrink-0 flex-col">{bids.map((level) => row(level, "bid"))}</div>
          {blank(6 - bids.length)}
        </div>
      )}
    </div>
  );
}

/**
 * The single-option ticket — the default, as on every options venue: side,
 * price by premium or by IV, size in the underlying, execution, send. The
 * order goes through the gateway like a spot or perp order (one
 * clientOrderId, looked up and never resent when the outcome is unknown).
 * "Add to combo" carries this leg into a package.
 */
export function ContractTicket({ chain, row, instrument, forward, discount, now, side, setSide, size, setSize, onPrice, accounts, account, setAccount, onCombo }: {
  chain: OptionChain;
  row: OptionRow;
  instrument: Instrument | undefined;
  forward: number;
  discount: number;
  now: number;
  side: Side;
  setSide: (side: Side) => void;
  size: string;
  setSize: (size: string) => void;
  /** The limit the ticket would send, for the payoff beside the board. */
  onPrice: (price: string) => void;
  accounts: Account[];
  account: string;
  setAccount: (id: string) => void;
  onCombo: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { trackOrder, limitPriceHint, openConnect } = useWorkspace();
  const [by, setBy] = useState<PriceBy>("price");
  const [priceInput, setPriceInput] = useState("");
  const [ivInput, setIvInput] = useState("");
  const [tif, setTif] = useState<Tif>("gtc");
  const [busy, setBusy] = useState(false);
  const call = row.kind === "call";
  const strike = Number(row.strike);
  const inverse = !!chain.inverse;
  const t0 = years(row.expiry, now);
  const tick = instrument?.tickSize ?? (inverse ? "0.0001" : "0.01");
  const step = instrument?.stepSize ?? "0.01";
  const minQty = Number(instrument?.minQty) || 0;

  // The tick that applies at a price (Deribit and OKX coin options quote in
  // a coarser tick above a threshold).
  const tickAt = (value: number) => (instrument?.coarseTick && instrument.coarseTickFrom && value >= Number(instrument.coarseTickFrom) ? instrument.coarseTick : tick);
  // Snap towards the passive side: a buy rounds down, a sell up.
  const snap = (value: number) => {
    const at = tickAt(value);
    const units = value / Number(at);
    const snapped = (side === "buy" ? Math.floor(units + 1e-9) : Math.ceil(units - 1e-9)) * Number(at);
    return snapped > 0 ? snapped.toFixed(decimals(at)) : "";
  };
  const usdOf = (value: number) => (inverse ? value * forward : value);
  const premiumOf = (value: number) => (inverse ? value / forward : value);
  const ivOfPrice = (value: number) => (value > 0 && forward > 0 ? impliedVolOf(call, forward, strike, t0, usdOf(value), discount) : undefined);

  // The order's price: typed, or derived from the typed IV.
  const limit = useMemo(() => {
    if (by === "price") return snapToStep(priceInput, tickAt(Number(priceInput) || 0)) || "";
    const vol = Number(ivInput) / 100;
    if (!(vol > 0) || !(forward > 0)) return "";
    return snap(premiumOf(priceAt(call, forward, strike, t0, vol, discount)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [by, priceInput, ivInput, forward, discount, side, t0]);
  const limitIv = ivOfPrice(Number(limit));
  useEffect(() => onPrice(limit), [limit, onPrice]);

  // Start on the touch of the chosen side; a book click fills the price.
  useEffect(() => {
    const touch = side === "buy" ? row.ask : row.bid;
    const seed = Number(touch) > 0 ? touch! : Number(row.mark) > 0 ? row.mark! : "";
    setPriceInput(seed ? snap(Number(seed)) : "");
    const vol = ivOfPrice(Number(seed));
    setIvInput(vol ? (vol * 100).toFixed(2) : "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [row.inst, side]);
  useEffect(() => {
    if (!limitPriceHint) return;
    setBy("price");
    setPriceInput(limitPriceHint.price);
  }, [limitPriceHint]);

  const qty = snapToStep(size, step);
  const units = Number(qty) || 0;
  const premiumTotal = units * (Number(limit) || 0);
  const validation = !account ? t("options.noAccount")
    : !(Number(limit) > 0) ? t("options.needPrice")
    : !(units > 0) ? t("options.needSize")
    : units < minQty ? t("options.belowMin", { min: instrument?.minQty ?? "" })
    : "";

  const place = async () => {
    if (validation || !instrument) return;
    setBusy(true);
    try {
      const outcome = await submitOrder(account, { inst: row.inst, side, type: "limit", qty, price: limit, tif });
      if (outcome.kind === "accepted") {
        trackOrder(account, outcome.order);
        announce(outcome.order);
        void queryClient.invalidateQueries({ queryKey: ["orders", account] });
      } else {
        toast.push({ kind: "error", title: t(outcome.kind === "rejected" ? "order.rejected" : "order.unknown"), detail: outcome.message });
      }
    } catch (error) {
      toast.push({ kind: "error", title: t("order.failed"), detail: (error as Error).message });
    } finally {
      setBusy(false);
    }
  };
  const announce = (order: Order) => {
    const verb = t(order.side === "buy" ? "common.buy" : "common.sell");
    const name = contractName(chain, row);
    if (order.status === "filled") toast.push({ kind: order.side === "buy" ? "long" : "short", title: t("order.filled", { verb, qty: num(order.filled), name }) });
    else toast.push({ kind: "info", title: t("order.placed", { verb, qty: num(order.qty), name }), detail: t("order.limitAt", { price: limit }) });
  };

  const field = "flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong";
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto bg-surface p-3 [&>*]:shrink-0">
      {accounts.length === 0 ? (
        <button onClick={() => openConnect(chain.venue)} title={t("options.connectToTrade")} className="h-9 rounded-lg border border-line text-accent hover:bg-raised">
          {t("options.connectVenue", { venue: venueLabel(chain.venue) })}
        </button>
      ) : (
        <div className="flex items-center gap-2">
          <span className="text-muted">{t("common.account")}</span>
          <select value={account} onChange={(e) => setAccount(e.target.value)} className="ml-auto h-7 max-w-[200px] rounded-md bg-raised px-2 outline-none">
            {accounts.map((item) => <option key={item.id} value={item.id}>{accountName(item)}</option>)}
          </select>
        </div>
      )}
      <div className="flex items-baseline justify-between">
        <span className="text-md">{contractName(chain, row)}</span>
        <span className="text-muted">{venueLabel(chain.venue)}</span>
      </div>
      <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5">
        <button onClick={() => setSide("buy")} className={cx("rounded-md", side === "buy" ? "bg-up text-on-accent" : "text-text")}>{t("common.buy")}</button>
        <button onClick={() => setSide("sell")} className={cx("rounded-md", side === "sell" ? "bg-down text-on-accent" : "text-text")}>{t("common.sell")}</button>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-muted">{t("options.priceBy")}</span>
        <div className="ml-auto flex rounded-lg bg-raised p-0.5">
          {(["price", "iv"] as const).map((value) => (
            <button key={value} onClick={() => setBy(value)} className={cx("rounded-md px-2.5 leading-6", by === value ? "bg-line-strong/60 text-text" : "text-muted hover:text-text")}>
              {t(value === "price" ? "options.byPrice" : "options.byIv")}
            </button>
          ))}
        </div>
      </div>
      {by === "price" ? (
        <label className={field}>
          <span className="text-muted">{t("ticket.priceIn", { quote: chain.premiumAsset })}</span>
          <input inputMode="decimal" value={priceInput} onChange={(e) => setPriceInput(e.target.value.replace(/[^0-9.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
        </label>
      ) : (
        <label className={field}>
          <span className="text-muted">IV</span>
          <input inputMode="decimal" value={ivInput} onChange={(e) => setIvInput(e.target.value.replace(/[^0-9.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
          <span className="text-muted">%</span>
        </label>
      )}
      <div className="-mt-2 flex justify-between text-xs text-muted tabular-nums">
        <span>{by === "price" ? (limitIv ? `IV ${(limitIv * 100).toFixed(2)}%` : "") : limit ? `${t("options.limitIs")} ${limit} ${chain.premiumAsset}` : ""}</span>
        <span>{inverse && Number(limit) > 0 ? usd(Number(limit) * forward) : ""}</span>
      </div>
      <label className={field}>
        <span className="text-muted">{t("common.size")}</span>
        <input inputMode="decimal" value={size} onChange={(e) => setSize(e.target.value.replace(/[^0-9.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
        <span className="text-muted">{chain.underlying}</span>
      </label>
      <div className="-mt-2 text-right text-xs text-muted">{t("options.stepIs", { step })}{instrument?.minQty ? ` · ${t("options.minIs", { min: instrument.minQty })}` : ""}</div>
      <div className="flex items-center gap-2">
        <span className="text-muted" title={t("options.tifHint")}>{t("options.execution")}</span>
        <div className="ml-auto flex rounded-lg bg-raised p-0.5">
          {(["gtc", "post_only", "ioc"] as const).map((value) => (
            <button key={value} onClick={() => setTif(value)} className={cx("rounded-md px-2 leading-6", tif === value ? "bg-line-strong/60 text-text" : "text-muted hover:text-text")}>
              {t(`options.tif.${value}` as Parameters<typeof t>[0])}
            </button>
          ))}
        </div>
      </div>
      <PrimaryButton disabled={busy || !!validation} onClick={place} className={side === "sell" ? "bg-down hover:bg-down/80" : undefined}>
        {busy ? t("ticket.placing") : validation && size ? validation : t(side === "buy" ? "options.buyContract" : "options.sellContract")}
      </PrimaryButton>
      <div className="flex flex-col gap-0.5 border-t border-line pt-2">
        <div className="flex justify-between"><span className="text-muted">{side === "buy" ? t("options.premiumPaid") : t("options.premiumReceived")}</span>
          <span className="tabular-nums">{premiumTotal > 0 ? `${premiumTotal.toFixed(inverse ? 4 : 2)} ${chain.premiumAsset}${inverse ? ` · ${usd(premiumTotal * forward)}` : ""}` : "—"}</span></div>
        <div className="flex justify-between"><span className="text-muted">{t("options.positionDelta")}</span>
          <span className="tabular-nums">{units > 0 ? num((side === "buy" ? 1 : -1) * units * row.delta, 3) : "—"}</span></div>
        {side === "sell" && <p className="mt-1 text-xs text-muted">{t("options.sellNote")}</p>}
      </div>
      <button onClick={onCombo} title={t("options.addToComboHint")} className="h-8 rounded-lg border border-line text-text-2 hover:bg-raised hover:text-text">
        + {t("options.addToCombo")}
      </button>
    </div>
  );
}
