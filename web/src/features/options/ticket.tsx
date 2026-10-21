import { useEffect, useMemo, useState } from "react";

import { optionsApi, type LegRequest, type OptionChain, type OptionRow } from "@/api/options";
import type { Account } from "@/api/types";
import { num, snapToStep, usd, venueLabel } from "@/lib/format";
import { useWorkspace } from "@/state/workspace";
import { accountName } from "@/lib/routes";
import { PrimaryButton, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { useT } from "@/i18n";
import {
  STRATEGIES, dteLabel, expiryLabel, mid, premiumUsd, summarize, touch, years, type PackageLeg, type PackageSummary, type Quoted, type Side, type Strategy,
} from "./model";

export type PriceMode = "mid" | "touch" | "mark";

/**
 * The combo ticket — the advanced mode, entered from the header toggle or
 * "Add to combo" on the single ticket. A package is any set of legs in one
 * underlying, picked from the board (bid sells, ask buys) or built from a
 * template, traded as one action of independent limit orders (no venue in
 * scope has an atomic combo book). The payoff is drawn beside the board.
 *
 *   template ▾ · long/short the structure
 *   legs: side, ratio, contract, live price and IV
 *   size (units of the underlying per 1×) · price at mid / touch / mark
 *   net premium, package greeks
 *   account · execution · send
 */
export function PackageTicket({ chain, steps, legs, setLegs, size, setSize, forward, now, accounts, account, setAccount, onPreset, onSummary, onExit }: {
  chain: OptionChain | undefined;
  /** Each option's quantity step (from the instrument catalog). */
  steps: Map<string, string>;
  legs: PackageLeg[];
  setLegs: (legs: PackageLeg[]) => void;
  size: string;
  setSize: (size: string) => void;
  forward: number;
  now: number;
  accounts: Account[];
  account: string;
  setAccount: (id: string) => void;
  onPreset: (strategy: Strategy, side: Side) => void;
  /** The package's valuation, for the payoff beside the board. */
  onSummary: (summary: PackageSummary | undefined) => void;
  /** Back to the single-option ticket. */
  onExit: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const { openConnect } = useWorkspace();
  const [priceMode, setPriceMode] = useState<PriceMode>("mid");
  const [tif, setTif] = useState<"gtc" | "post_only" | "ioc">("gtc");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [presetSide, setPresetSide] = useState<Side>("buy");
  const inverse = !!chain?.inverse;
  const rows = useMemo(() => new Map((chain?.rows ?? []).map((row) => [row.inst, row])), [chain]);
  const forwards = useMemo(() => new Map((chain?.expiries ?? []).map((item) => [item.expiry, item.forward ?? forward])), [chain, forward]);
  // The package size must give every leg a whole number of steps: the
  // coarsest step of the legs (Deribit BTC options 0.1, ETH 1, altcoins up
  // to 10 000).
  const step = useMemo(() => legs.map((item) => steps.get(item.inst) ?? "0.1").reduce((a, b) => (Number(b) > Number(a) ? b : a), "0"), [legs, steps]);
  const units = Number(size) || 0;

  const legPrice = (item: PackageLeg, row: OptionRow | undefined) => {
    if (!row) return 0;
    if (priceMode === "mark") return Number(row.mark) || 0;
    if (priceMode === "touch") return touch(row, item.side);
    return mid(row);
  };
  const quoted: Quoted[] = legs.map((item) => {
    const row = rows.get(item.inst);
    const legForward = forwards.get(item.expiry) ?? forward;
    return { leg: item, row, forward: legForward, entryUsd: premiumUsd(String(legPrice(item, row)), inverse, legForward) };
  });
  const summary = useMemo(() => (legs.length && units > 0 ? summarize(quoted, units, now, payoffSpan(quoted, now)) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [legs, units, priceMode, chain?.updatedAt, now]);
  const missing = quoted.some((item) => !(item.entryUsd > 0));
  useEffect(() => onSummary(summary), [summary, onSummary]);

  const premiumText = (value: number) => {
    const money = usd(Math.abs(value));
    const coin = inverse && forward > 0 ? ` · ${(Math.abs(value) / forward).toFixed(4)} ${chain?.premiumAsset}` : "";
    return money + coin;
  };

  const send = async () => {
    if (!account || !legs.length || units <= 0) return;
    const requests: LegRequest[] = legs.map((item) => ({
      account, inst: item.inst, side: item.side, qty: snapToStep(String(item.ratio * units), step) || "0",
      priceBy: priceMode === "touch" ? (item.side === "buy" ? "ask" : "bid") : priceMode, tif,
    }));
    if (requests.some((item) => Number(item.qty) <= 0)) {
      toast.push({ kind: "error", title: t("options.sizeTooSmall"), detail: t("options.stepIs", { step }) });
      return;
    }
    setBusy(true);
    try {
      const execution = await optionsApi.submit(label, requests);
      const rejected = execution.legs.filter((item) => item.state === "rejected");
      if (rejected.length === execution.legs.length) {
        toast.push({ kind: "error", title: t("options.packageRejected"), detail: rejected[0]?.message });
      } else if (rejected.length) {
        toast.push({ kind: "error", title: t("options.packagePartial", { n: rejected.length }), detail: rejected.map((item) => item.message).filter(Boolean).join(" · ") });
      } else {
        toast.push({ kind: "info", title: t("options.packageSent", { n: execution.legs.length }), detail: label || undefined });
        setLegs([]);
      }
    } catch (error) {
      toast.push({ kind: "error", title: t("options.packageFailed"), detail: (error as Error).message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto bg-surface p-3 [&>*]:shrink-0">
      <div className="flex items-center gap-2">
        <span className="text-md">{t("options.combo")}</span>
        <button onClick={onExit} className="ml-auto text-muted hover:text-text">{t("options.backToSingle")}</button>
      </div>
      <div className="flex items-center gap-2">
        <select value="" onChange={(e) => { if (e.target.value) onPreset(e.target.value as Strategy, presetSide); }} aria-label={t("options.template")}
          className="h-8 min-w-0 flex-1 rounded-lg bg-raised px-2 text-text-2 outline-none">
          <option value="">{t("options.template")}…</option>
          {STRATEGIES.filter((strategy) => strategy !== "call" && strategy !== "put").map((strategy) => (
            <option key={strategy} value={strategy}>{t(`options.strategy.${strategy}` as Parameters<typeof t>[0])} · {t(`options.strategy.${strategy}.hint` as Parameters<typeof t>[0])}</option>
          ))}
        </select>
        <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5" title={t("options.structureSideHint")}>
          {(["buy", "sell"] as const).map((side) => (
            <button key={side} onClick={() => setPresetSide(side)} className={cx("rounded-md px-2", presetSide === side ? (side === "buy" ? "bg-up text-on-accent" : "bg-down text-on-accent") : "text-muted")}>
              {t(side === "buy" ? "options.long" : "options.short")}
            </button>
          ))}
        </div>
      </div>

      {legs.length === 0 ? (
        <div className="rounded-lg border border-dashed border-line px-3 py-4 text-center text-muted">{t("options.emptyPackage")}</div>
      ) : (
        <div className="flex flex-col">
          {quoted.map((item, index) => (
            <LegLine key={item.leg.inst} item={item} inverse={inverse} priceOf={legPrice}
              onFlip={() => setLegs(legs.map((leg, i) => (i === index ? { ...leg, side: leg.side === "buy" ? "sell" : "buy" } : leg)))}
              onRatio={(ratio) => setLegs(legs.map((leg, i) => (i === index ? { ...leg, ratio } : leg)))}
              onRemove={() => setLegs(legs.filter((_, i) => i !== index))} />
          ))}
          <button onClick={() => setLegs([])} className="mt-1 self-end text-muted hover:text-text">{t("options.clear")}</button>
        </div>
      )}

      <label className="flex h-9 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
        <span className="text-muted">{t("options.size")}</span>
        <input inputMode="decimal" value={size} onChange={(e) => setSize(e.target.value.replace(/[^0-9.]/g, ""))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
        <span className="text-muted">{chain?.underlying ?? ""}</span>
      </label>
      {legs.length > 0 && Number(step) > 0 && <div className="-mt-2 text-right text-xs text-muted">{t("options.stepIs", { step })}</div>}
      <div className="flex items-center gap-2">
        <span className="text-muted">{t("options.priceAt")}</span>
        <div className="ml-auto flex rounded-lg bg-raised p-0.5">
          {(["mid", "touch", "mark"] as const).map((mode) => (
            <button key={mode} onClick={() => setPriceMode(mode)} title={t(`options.price.${mode}.hint` as Parameters<typeof t>[0])}
              className={cx("rounded-md px-2 leading-6", priceMode === mode ? "bg-line-strong/60 text-text" : "text-muted hover:text-text")}>
              {t(`options.price.${mode}` as Parameters<typeof t>[0])}
            </button>
          ))}
        </div>
      </div>

      {summary && (
        <div className="flex flex-col gap-1 border-t border-line pt-2">
          <SummaryRow label={summary.premiumUsd >= 0 ? t("options.netDebit") : t("options.netCredit")} hint={t("options.netHint")}>
            <span className={summary.premiumUsd >= 0 ? "text-down" : "text-up"}>{premiumText(summary.premiumUsd)}</span>
          </SummaryRow>
          <div className="grid grid-cols-4 gap-1 py-1 text-center">
            <Greek label="Δ" value={num(summary.greeks.delta, 3)} hint={t("options.greek.delta")} />
            <Greek label="Γ 1%" value={usd(summary.greeks.gamma * forward * forward * 0.01, 0)} hint={t("options.greek.gamma")} />
            <Greek label="Vega" value={usd(summary.greeks.vega, 0)} hint={t("options.greek.vega")} />
            <Greek label="Θ/d" value={usd(summary.greeks.theta, 0)} hint={t("options.greek.theta")} />
          </div>
        </div>
      )}

      {accounts.length === 0 && chain && (
        <button onClick={() => openConnect(chain.venue)} className="h-9 rounded-lg border border-line text-accent hover:bg-raised">
          {t("options.connectVenue", { venue: venueLabel(chain.venue) })}
        </button>
      )}
      <div className={cx("flex items-center gap-2 border-t border-line pt-2", accounts.length === 0 && "hidden")}>
        <span className="text-muted">{t("common.account")}</span>
        <select value={account} onChange={(e) => setAccount(e.target.value)} className="ml-auto h-7 max-w-[180px] rounded-md bg-raised px-2 outline-none">
          {accounts.length === 0 && <option value="">{t("options.noAccount")}</option>}
          {accounts.map((item) => <option key={item.id} value={item.id}>{accountName(item)}</option>)}
        </select>
      </div>
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
      <input value={label} maxLength={40} onChange={(e) => setLabel(e.target.value)} placeholder={t("options.labelPlaceholder")}
        className="h-8 rounded-lg border border-line bg-transparent px-3 outline-none placeholder:text-faint focus:border-line-strong" />
      <PrimaryButton disabled={busy || !account || !legs.length || units <= 0 || missing} onClick={send}>
        {busy ? t("options.sending") : legs.length ? t("options.send", { n: legs.length }) : t("options.send0")}
      </PrimaryButton>
      {missing && legs.length > 0 && <div className="text-down">{t("options.noPrice")}</div>}
      <p className="text-muted">{t("options.legNote")}</p>
    </div>
  );
}

/**
 * The payoff's x range: three standard deviations of the move to the first
 * expiry (at the legs' highest IV), between 4% and 60% of the forward — a
 * two-day straddle and a six-month risk reversal both fill the chart.
 */
function payoffSpan(quoted: Quoted[], now: number): number {
  const first = Math.min(...quoted.map((item) => item.leg.expiry));
  const iv = Math.max(0.2, ...quoted.map((item) => item.row?.markIv ?? 0));
  return Math.min(0.6, Math.max(0.04, 3 * iv * Math.sqrt(years(first, now))));
}

function LegLine({ item, inverse, priceOf, onFlip, onRatio, onRemove }: {
  item: Quoted;
  inverse: boolean;
  priceOf: (leg: PackageLeg, row: OptionRow | undefined) => number;
  onFlip: () => void;
  onRatio: (ratio: number) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const price = priceOf(item.leg, item.row);
  return (
    <div className="group flex h-7 items-center gap-2">
      <button onClick={onFlip} title={t("options.flip")} className={cx("w-7 shrink-0 rounded px-1 text-center", item.leg.side === "buy" ? "bg-long/25 text-up" : "bg-short-deep/60 text-down")}>
        {t(item.leg.side === "buy" ? "options.b" : "options.s")}
      </button>
      <input value={item.leg.ratio} onChange={(e) => onRatio(Math.max(1, Math.round(Number(e.target.value) || 1)))} className="w-5 shrink-0 bg-transparent text-right text-text-2 outline-none" aria-label={t("options.ratio")} />
      <span className="shrink-0 text-muted">×</span>
      <span className="min-w-0 flex-1 truncate">
        <span className="text-text">{num(item.leg.strike)}</span> <span className={item.leg.kind === "call" ? "text-up" : "text-down"}>{item.leg.kind === "call" ? "C" : "P"}</span> <span className="text-muted">{expiryLabel(item.leg.expiry).slice(0, -2)}</span>
      </span>
      <span className="shrink-0 text-right tabular-nums">{price > 0 ? (inverse ? price.toFixed(4) : num(price)) : "—"}</span>
      <span className="w-10 shrink-0 text-right text-muted tabular-nums">{item.row?.markIv ? (item.row.markIv * 100).toFixed(1) + "%" : ""}</span>
      <button onClick={onRemove} aria-label={t("options.remove")} className="w-4 shrink-0 text-muted opacity-0 group-hover:opacity-100 hover:text-down">✕</button>
    </div>
  );
}

function SummaryRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between py-0.5">
      <span className={cx("text-muted", hint && "dotted")} title={hint}>{label}</span>
      <span className="tabular-nums">{children}</span>
    </div>
  );
}

function Greek({ label, value, hint }: { label: string; value: string; hint: string }) {
  return (
    <div className="rounded-md bg-raised py-1" title={hint}>
      <div className="text-xs text-muted">{label}</div>
      <div className="tabular-nums">{value}</div>
    </div>
  );
}

export { dteLabel };
