import { useState } from "react";

import { optionsApi, type Execution, type Risk, type Scenario, type UnderlyingRisk } from "@/api/options";
import type { Account } from "@/api/types";
import { instrumentName, num, time, usd } from "@/lib/format";
import { accountName } from "@/lib/routes";
import { Dialog, PrimaryButton, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { useT } from "@/i18n";
import { dteLabel, expiryLabel } from "./model";

type Tab = "positions" | "expiries" | "scenario" | "executions" | string;

/** A tab the page adds to the panel (open orders, fills, volatility). */
export interface ExtraTab {
  value: string;
  label: string;
  /** Shown before the risk tabs. */
  first?: boolean;
  render: () => React.ReactNode;
}

const tone = (value: number) => (value > 0 ? "text-up" : value < 0 ? "text-down" : "text-text-2");

/**
 * The book's risk, per underlying: the greeks strip first (what a desk
 * checks before anything), then tabs for positions, expiry buckets, the
 * spot × vol scenario grid and recent executions.
 */
export function RiskPanel({ risk, executions, online, accounts, hedges, setHedges, days, setDays, onClosePosition, extra = [] }: {
  risk: Risk | undefined;
  executions: Execution[];
  online: boolean;
  accounts: Account[];
  hedges: boolean;
  setHedges: (value: boolean) => void;
  days: number;
  setDays: (value: number) => void;
  onClosePosition: (inst: string, size: number) => void;
  extra?: ExtraTab[];
}) {
  const t = useT();
  const [tab, setTab] = useState<Tab>(() => (localStorage.getItem("keeluni.options.riskTab") as Tab) || "positions");
  const [underlying, setUnderlying] = useState<string>("");
  const items = risk?.underlyings ?? [];
  const current = items.find((item) => item.underlying === underlying) ?? items[0];
  const working = executions.filter((item) => item.status === "working").length;
  const choose = (value: Tab) => {
    setTab(value);
    localStorage.setItem("keeluni.options.riskTab", value);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex shrink-0 items-stretch border-b border-line">
        <Tabs value={tab} onChange={choose} className="border-b-0" options={[
          ...extra.filter((item) => item.first).map((item) => ({ value: item.value, label: item.label })),
          { value: "positions", label: t("options.tab.positions") + (current ? ` (${current.positions.length})` : "") },
          { value: "expiries", label: t("options.tab.expiries") },
          { value: "scenario", label: t("options.tab.scenario") },
          { value: "executions", label: t("options.tab.executions") + (working ? ` (${working})` : "") },
          ...extra.filter((item) => !item.first).map((item) => ({ value: item.value, label: item.label })),
        ]} />
        <div className="ml-auto flex items-center gap-3 px-3">
          {!online && <span className="text-down">{t("options.appOffline")}</span>}
          {risk?.stale && <span className="text-down" title={t("options.staleHint")}>{t("options.stale")}</span>}
          <label className="flex items-center gap-1.5 text-muted" title={t("options.includeHedgesHint")}>
            <input type="checkbox" checked={hedges} onChange={(e) => setHedges(e.target.checked)} className="accent-[var(--color-accent)]" />
            {t("options.includeHedges")}
          </label>
        </div>
      </div>
      {items.length > 0 && current && !extra.some((item) => item.value === tab) && (
        <GreeksStrip items={items} current={current} onSelect={setUnderlying} accounts={accounts} hedges={hedges} />
      )}
      <div className="min-h-0 flex-1 overflow-auto">
        {extra.find((item) => item.value === tab)?.render() ?? (tab === "executions" ? (
          <Executions items={executions} />
        ) : !current ? (
          <div className="flex h-full items-center justify-center text-muted">{risk ? t("options.noPositions") : t("common.loading")}</div>
        ) : tab === "positions" ? (
          <Positions item={current} accounts={accounts} onClose={onClosePosition} />
        ) : tab === "expiries" ? (
          <Expiries item={current} />
        ) : (
          <ScenarioGrid scenario={current.scenario} days={days} setDays={setDays} />
        ))}
      </div>
    </div>
  );
}

function GreeksStrip({ items, current, onSelect, accounts, hedges }: {
  items: UnderlyingRisk[];
  current: UnderlyingRisk;
  onSelect: (underlying: string) => void;
  accounts: Account[];
  hedges: boolean;
}) {
  const t = useT();
  const [hedging, setHedging] = useState(false);
  const g = current.greeks;
  const cell = (label: string, value: string, hint: string, className?: string) => (
    <div className="flex min-w-[88px] flex-col px-3 py-1.5" title={hint}>
      <span className="dotted text-muted">{label}</span>
      <span className={cx("tabular-nums text-md", className)}>{value}</span>
    </div>
  );
  return (
    <div className="flex shrink-0 flex-wrap items-center border-b border-line">
      <div className="flex items-center gap-1 px-2">
        {items.map((item) => (
          <button key={item.underlying} onClick={() => onSelect(item.underlying)}
            className={cx("rounded-md px-2 py-1", item.underlying === current.underlying ? "bg-raised text-text" : "text-muted hover:text-text")}>
            {item.underlying}
          </button>
        ))}
      </div>
      {cell(t("options.risk.delta"), `${num(g.delta, 3)} · ${usd(g.deltaUsd, 0)}`, t("options.risk.deltaHint"), tone(g.delta))}
      {cell(t("options.risk.gamma"), usd(g.gamma1pct, 0), t("options.risk.gammaHint"), tone(g.gamma1pct))}
      {cell(t("options.risk.vega"), usd(g.vega, 0), t("options.risk.vegaHint"), tone(g.vega))}
      {cell(t("options.risk.theta"), usd(g.theta, 0), t("options.risk.thetaHint"), tone(g.theta))}
      {cell(t("options.risk.value"), usd(g.value, 0), t("options.risk.valueHint"))}
      {cell(t("options.risk.pnl"), usd(g.pnl, 0), t("options.risk.pnlHint"), tone(g.pnl))}
      {current.hedgeDelta !== 0 && cell(t("options.risk.hedged"), num(current.hedgeDelta, 4), t("options.risk.hedgedHint"))}
      <button onClick={() => setHedging(true)} className="ml-auto mr-3 rounded-lg border border-line px-3 py-1 text-accent hover:bg-raised">
        {t("options.hedgeDelta")}
      </button>
      <HedgeDialog open={hedging} onOpenChange={setHedging} underlying={current.underlying} delta={g.delta} accounts={accounts} hedges={hedges} />
    </div>
  );
}

function HedgeDialog({ open, onOpenChange, underlying, delta, accounts, hedges }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  underlying: string;
  delta: number;
  accounts: Account[];
  hedges: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const perpAccounts = accounts.filter((account) => account.active && account.markets.some((market) => market.includes(":perp")));
  const [account, setAccount] = useState("");
  const [busy, setBusy] = useState(false);
  const [suggestion, setSuggestion] = useState<Awaited<ReturnType<typeof optionsApi.hedge>> | null>(null);
  const [error, setError] = useState("");
  const chosen = account || perpAccounts[0]?.id || "";
  const load = async (id: string) => {
    setError("");
    setSuggestion(null);
    if (!id) return;
    try {
      setSuggestion(await optionsApi.hedge(underlying, id, hedges));
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const send = async () => {
    if (!suggestion) return;
    setBusy(true);
    try {
      const execution = await optionsApi.submit(t("options.hedgeLabel", { underlying }), [suggestion.leg]);
      const leg = execution.legs[0];
      if (leg?.state === "rejected") toast.push({ kind: "error", title: t("options.hedgeRejected"), detail: leg.message });
      else toast.push({ kind: suggestion.leg.side === "buy" ? "long" : "short", title: t("options.hedgeSent", { side: t(suggestion.leg.side === "buy" ? "common.buy" : "common.sell"), qty: suggestion.leg.qty, name: instrumentName(suggestion.leg.inst) }) });
      onOpenChange(false);
    } catch (e) {
      toast.push({ kind: "error", title: t("options.hedgeRejected"), detail: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={(next) => { onOpenChange(next); if (next) void load(chosen); }} title={t("options.hedgeTitle", { underlying })}
      description={t("options.hedgeDescription", { delta: num(delta, 4), underlying })}>
      <div className="flex flex-col gap-3">
        <label className="flex items-center gap-2">
          <span className="text-muted">{t("options.hedgeOn")}</span>
          <select value={chosen} onChange={(e) => { setAccount(e.target.value); void load(e.target.value); }} className="ml-auto h-8 rounded-md bg-raised px-2 outline-none">
            {perpAccounts.map((item) => <option key={item.id} value={item.id}>{accountName(item)}</option>)}
          </select>
        </label>
        {suggestion && (
          <div className="rounded-lg bg-raised p-3">
            <div className={suggestion.leg.side === "buy" ? "text-up" : "text-down"}>
              {t(suggestion.leg.side === "buy" ? "common.buy" : "common.sell")} {suggestion.leg.qty} {instrumentName(suggestion.leg.inst)} · {t("common.market")}
            </div>
            <div className="mt-1 text-muted">{t("options.hedgeResidual", { residual: num(suggestion.residual, 4) })}</div>
          </div>
        )}
        {error && <div className="text-down">{error}</div>}
        {perpAccounts.length === 0 && <div className="text-muted">{t("options.hedgeNoAccount")}</div>}
        <PrimaryButton disabled={!suggestion || busy} onClick={send}>{busy ? t("options.sending") : t("options.hedgeSend")}</PrimaryButton>
      </div>
    </Dialog>
  );
}

const th = "sticky top-0 bg-surface px-3 py-1.5 text-right font-normal text-muted whitespace-nowrap first:text-left";
const td = "px-3 h-6 text-right tabular-nums whitespace-nowrap first:text-left";

function Positions({ item, accounts, onClose }: { item: UnderlyingRisk; accounts: Account[]; onClose: (inst: string, size: number) => void }) {
  const t = useT();
  const nameOf = (id: string) => {
    const account = accounts.find((value) => value.id === id);
    return account ? accountName(account) : id;
  };
  const many = new Set(item.positions.map((position) => position.account)).size > 1;
  const now = Date.now();
  return (
    <table className="w-full border-collapse">
      <thead>
        <tr>
          <th className={th}>{t("options.contract")}</th>
          {many && <th className={cx(th, "!text-left")}>{t("common.account")}</th>}
          <th className={th}>{t("options.size")}</th>
          <th className={th}>{t("options.entry")}</th>
          <th className={th}>{t("options.mark")}</th>
          <th className={th}>IV</th>
          <th className={th}>Δ</th>
          <th className={th}>{t("options.risk.gamma")}</th>
          <th className={th}>Vega</th>
          <th className={th}>Θ/d</th>
          <th className={th}>{t("options.risk.pnl")}</th>
          <th className={th} />
        </tr>
      </thead>
      <tbody>
        {item.positions.map((position) => {
          const option = position.product === "option";
          return (
            <tr key={position.account + position.inst} className="hover:bg-raised">
              <td className={td}>
                {option ? (
                  <span>
                    {expiryLabel(position.expiry!)} <span className="text-text">{num(position.strike)}</span>{" "}
                    <span className={position.kind === "call" ? "text-up" : "text-down"}>{position.kind === "call" ? "C" : "P"}</span>
                    <span className="ml-1.5 text-muted">{dteLabel(position.expiry!, now)}</span>
                  </span>
                ) : (
                  <span>{instrumentName(position.inst)} <span className="text-muted">{t(position.product === "perp" ? "common.perp" : "common.spot")}</span></span>
                )}
              </td>
              {many && <td className={cx(td, "!text-left text-text-2")}>{nameOf(position.account)}</td>}
              <td className={cx(td, tone(position.size))}>{(position.size > 0 ? "+" : "") + num(position.size)}</td>
              <td className={td}>{num(position.entry)}</td>
              <td className={td}>{num(position.mark)}</td>
              <td className={cx(td, "text-text-2")}>{position.markIv ? (position.markIv * 100).toFixed(1) + "%" : ""}</td>
              <td className={cx(td, tone(position.greeks.delta))}>{num(position.greeks.delta, 3)}</td>
              <td className={td}>{usd(position.greeks.gamma1pct, 0)}</td>
              <td className={td}>{usd(position.greeks.vega, 0)}</td>
              <td className={cx(td, tone(position.greeks.theta))}>{usd(position.greeks.theta, 0)}</td>
              <td className={cx(td, tone(position.greeks.pnl))}>{usd(position.greeks.pnl, 0)}</td>
              <td className={td}>
                {option && <button className="text-accent hover:underline" onClick={() => onClose(position.inst, position.size)}>{t("options.closeLeg")}</button>}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function Expiries({ item }: { item: UnderlyingRisk }) {
  const t = useT();
  return (
    <table className="w-full border-collapse">
      <thead>
        <tr>
          <th className={th}>{t("options.expiry")}</th>
          <th className={th}>DTE</th>
          <th className={th}>Δ</th>
          <th className={th}>{t("options.risk.gamma")}</th>
          <th className={th}>Vega</th>
          <th className={th}>Θ/d</th>
          <th className={th}>{t("options.risk.value")}</th>
          <th className={th}>{t("options.risk.pnl")}</th>
        </tr>
      </thead>
      <tbody>
        {item.expiries.map((bucket) => (
          <tr key={bucket.expiry} className="hover:bg-raised">
            <td className={td}>{expiryLabel(bucket.expiry)}</td>
            <td className={cx(td, "text-text-2")}>{bucket.dte.toFixed(1)}</td>
            <td className={cx(td, tone(bucket.greeks.delta))}>{num(bucket.greeks.delta, 3)}</td>
            <td className={td}>{usd(bucket.greeks.gamma1pct, 0)}</td>
            <td className={cx(td, tone(bucket.greeks.vega))}>{usd(bucket.greeks.vega, 0)}</td>
            <td className={cx(td, tone(bucket.greeks.theta))}>{usd(bucket.greeks.theta, 0)}</td>
            <td className={td}>{usd(bucket.greeks.value, 0)}</td>
            <td className={cx(td, tone(bucket.greeks.pnl))}>{usd(bucket.greeks.pnl, 0)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/**
 * Spot × vol PnL, full revaluation. Cells are shaded by sign and size; the
 * current state is the outlined centre. The horizon rolls time forward so
 * theta is in the grid too.
 */
function ScenarioGrid({ scenario, days, setDays }: { scenario: Scenario; days: number; setDays: (days: number) => void }) {
  const t = useT();
  const worst = Math.max(1, ...scenario.pnl.flat().map((value) => Math.abs(value)));
  return (
    <div className="p-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-muted">{t("options.horizon")}</span>
        {[0, 1, 7].map((value) => (
          <button key={value} onClick={() => setDays(value)} className={cx("rounded-md px-2 py-0.5", days === value ? "bg-raised text-text" : "text-muted hover:text-text")}>
            {value === 0 ? t("options.now") : `+${value}d`}
          </button>
        ))}
        <span className="ml-auto text-muted">{t("options.scenarioHint")}</span>
      </div>
      <table className="border-collapse">
        <thead>
          <tr>
            <th className="px-2 py-1 text-right font-normal text-muted">{t("options.spotVol")}</th>
            {scenario.volMoves.map((move) => (
              <th key={move} className="px-2 py-1 text-right font-normal text-muted">{move > 0 ? "+" : ""}{move}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {scenario.spotMoves.map((spot, i) => (
            <tr key={spot}>
              <td className="px-2 text-right text-muted">{spot > 0 ? "+" : ""}{(spot * 100).toFixed(0)}%</td>
              {scenario.volMoves.map((vol, j) => {
                const value = scenario.pnl[i]?.[j] ?? 0;
                const strength = Math.min(1, Math.abs(value) / worst);
                const center = spot === 0 && vol === 0;
                return (
                  <td key={vol} className={cx("h-6 min-w-[72px] px-2 text-right tabular-nums", center && "outline outline-1 outline-line-strong")}
                    style={{ background: value === 0 ? undefined : value > 0 ? `rgba(31,166,125,${0.08 + strength * 0.45})` : `rgba(237,112,136,${0.08 + strength * 0.45})` }}>
                    {Math.abs(value) >= 1000 ? `${(value / 1000).toFixed(1)}k` : value.toFixed(0)}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Executions({ items }: { items: Execution[] }) {
  const t = useT();
  const toast = useToast();
  if (!items.length) return <div className="flex h-full items-center justify-center text-muted">{t("options.noExecutions")}</div>;
  const cancel = async (id: string) => {
    try {
      await optionsApi.cancel(id);
    } catch (e) {
      toast.push({ kind: "error", title: t("options.cancelFailed"), detail: (e as Error).message });
    }
  };
  return (
    <table className="w-full border-collapse">
      <thead>
        <tr>
          <th className={th}>{t("common.time")}</th>
          <th className={cx(th, "!text-left")}>{t("options.package")}</th>
          <th className={cx(th, "!text-left w-full")}>{t("options.legs")}</th>
          <th className={th}>{t("common.status")}</th>
          <th className={th} />
        </tr>
      </thead>
      <tbody>
        {items.map((execution) => (
          <tr key={execution.id} className="align-top hover:bg-raised">
            <td className={cx(td, "pt-1")}>{time(execution.createdAt)}</td>
            <td className={cx(td, "pt-1 !text-left")}>{execution.label || "—"}</td>
            <td className="px-3 py-1">
              {execution.legs.map((leg) => (
                <div key={leg.clientOrderId} className="flex gap-2 whitespace-nowrap">
                  <span className={leg.side === "buy" ? "text-up" : "text-down"}>{t(leg.side === "buy" ? "common.buy" : "common.sell")}</span>
                  <span>{leg.qty}</span>
                  <span className="text-text-2">{instrumentName(leg.inst)}</span>
                  {leg.price && <span className="text-muted">@ {leg.price}</span>}
                  <span className={cx(leg.state === "filled" ? "text-up" : leg.state === "rejected" || leg.state === "unknown" ? "text-down" : "text-muted")}>
                    {t(`options.state.${leg.state}` as Parameters<typeof t>[0])}{Number(leg.filled) > 0 && leg.state !== "filled" ? ` ${leg.filled}` : ""}
                  </span>
                  {leg.message && <span className="text-down">{leg.message}</span>}
                </div>
              ))}
            </td>
            <td className={cx(td, "pt-1", execution.status === "done" ? "text-up" : execution.status === "partial" || execution.status === "failed" ? "text-down" : "text-text-2")}>
              {t(`options.status.${execution.status}` as Parameters<typeof t>[0])}
            </td>
            <td className={cx(td, "pt-1")}>
              {execution.status === "working" && <button className="text-accent hover:underline" onClick={() => cancel(execution.id)}>{t("common.cancel")}</button>}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
