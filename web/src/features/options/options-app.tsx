import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";

import { api } from "@/api/client";
import { optionsApi, useOptionChain, useOptionQuote, useOptionSeries, useOptionerStream, type OptionChain, type OptionRow } from "@/api/options";
import type { Order } from "@/api/types";
import { num, time } from "@/lib/format";
import { useNow } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { useT } from "@/i18n";
import { ChainBoard, type Holdings, type QuoteMode } from "./chain";
import { SmileChart, TermChart } from "./charts";
import { ContractInfo, ContractTicket, contractName, liveRow } from "./contract";
import { addLeg, atmIndex, buildStrategy, dteLabel, expiryLabel, premiumUsd, strikeRows, summarize, visibleRows, years, type PackageLeg, type PackageSummary, type Side, type Strategy } from "./model";
import { RiskPanel, type ExtraTab } from "./risk";
import { SeriesPicker } from "./series-picker";
import { PackageTicket } from "./ticket";

function stored<T>(key: string, fallback: T): T {
  try {
    const value = localStorage.getItem(key);
    return value === null ? fallback : (JSON.parse(value) as T);
  } catch {
    return fallback;
  }
}

function save(key: string, value: unknown) {
  localStorage.setItem(key, JSON.stringify(value));
}

type Mode = "single" | "combo";

/**
 * The options page, laid out as the trade page is — market left, book in
 * the middle, ticket right — because trading one option is the core action
 * and every options venue is built that way:
 *
 *   header      market picker (venue → underlying · settlement), the
 *               expiry's index, forward, ATM IV, 25Δ RR / BF, display
 *               toggles, and Single | Combo
 *   expiries    every expiry with DTE and ATM IV
 *   board       the T-board; clicking an option selects it in place
 *   contract    the selected option: quote and greeks, its book with each
 *               level's IV, and the payoff of the order being written
 *               (of the whole package in combo mode)
 *   ticket      single: one option by price or IV · combo: template, legs,
 *               net premium and greeks, one send
 *   bottom      open orders, fills, positions and book risk, executions,
 *               and the smile and term structure on demand
 */
export function OptionsApp() {
  const t = useT();
  const now = useNow(5_000);
  const { accounts } = useWorkspace();
  const series = useOptionSeries();
  const [seriesId, setSeriesId] = useState<string>(() => stored("keeluni.options.series", "deribit:BTC:BTC"));
  const list = series.data ?? [];
  const current = list.find((item) => item.series === seriesId) ?? list[0];
  const chain = useOptionChain(current?.series);
  const [expiry, setExpiry] = useState<number>(0);
  const [quoteMode, setQuoteMode] = useState<QuoteMode>(() => stored("keeluni.options.mode", "price"));
  const [usdUnit, setUsdUnit] = useState<boolean>(() => stored("keeluni.options.usd", false));
  const [window, setWindow] = useState<number>(() => stored("keeluni.options.window", 12));
  const [mode, setMode] = useState<Mode>("single");
  const [legs, setLegs] = useState<PackageLeg[]>([]);
  const [comboSize, setComboSize] = useState("1");
  const [hedges, setHedges] = useState<boolean>(() => stored("keeluni.options.hedges", true));
  const [days, setDays] = useState(0);
  // The selected option and the single ticket's order (for its payoff).
  const [selectedId, setSelectedId] = useState<string>("");
  const [side, setSide] = useState<Side>("buy");
  const [size, setSize] = useState("");
  const [limit, setLimit] = useState("");
  const [comboSummary, setComboSummary] = useState<PackageSummary | undefined>(undefined);

  const optionAccounts = useMemo(() => accounts.filter((account) => account.active && account.markets.some((market) => current && market === current.market)), [accounts, current]);
  const [accountChoice, setAccountChoice] = useState<string>(() => stored("keeluni.options.account", ""));
  const account = optionAccounts.find((item) => item.id === accountChoice)?.id ?? optionAccounts[0]?.id ?? "";
  const chooseAccount = (id: string) => {
    setAccountChoice(id);
    save("keeluni.options.account", id);
  };
  const stream = useOptionerStream([], hedges, days);

  // Option rules from the catalog (chains carry quotes, not rules).
  const optionInstruments = useQuery({ queryKey: ["instruments", "option"], queryFn: optionsApi.instruments, staleTime: 10 * 60_000 });
  const instruments = useMemo(() => new Map((optionInstruments.data ?? []).map((item) => [item.id, item])), [optionInstruments.data]);
  const steps = useMemo(() => new Map([...instruments].map(([id, item]) => [id, item.stepSize])), [instruments]);

  // Expiries still trading, nearest first; keep the choice while it lives.
  const expiries = useMemo(() => (chain?.expiries ?? []).filter((item) => item.expiry > now), [chain, now]);
  useEffect(() => {
    if (!expiries.length) return;
    if (!expiries.some((item) => item.expiry === expiry)) {
      // Default to the first expiry beyond a day: dailies are for scalpers.
      setExpiry((expiries.find((item) => item.expiry - now > 86_400_000) ?? expiries[0]!).expiry);
    }
  }, [expiries, expiry, now]);
  useEffect(() => {
    setLegs([]);
    setSelectedId("");
  }, [current?.series]);

  const selected = expiries.find((item) => item.expiry === expiry);
  const forward = selected?.forward ?? current?.index ?? 0;
  const allRows = useMemo(() => strikeRows(chain, expiry), [chain, expiry]);
  const rows = useMemo(() => visibleRows(allRows, forward, window), [allRows, forward, window]);
  const nextExpiry = expiries[expiries.findIndex((item) => item.expiry === expiry) + 1];
  const nextRows = useMemo(() => strikeRows(chain, nextExpiry?.expiry ?? 0), [chain, nextExpiry]);

  // The selected option: the user's pick while it is in the chain, else the
  // ATM call of the expiry, so the right side is never empty.
  const pickedRow = selectedId ? chain?.rows.find((item) => item.inst === selectedId) : undefined;
  const atmRow = allRows[atmIndex(allRows, forward)];
  const baseRow = pickedRow ?? atmRow?.call ?? atmRow?.put;
  const quote = useOptionQuote(baseRow?.inst);
  const row = baseRow ? liveRow(baseRow, quote) : undefined;
  const rowExpiry = row ? expiries.find((item) => item.expiry === row.expiry) : undefined;
  const rowForward = row?.forward ?? rowExpiry?.forward ?? forward;
  const rowDiscount = rowExpiry?.discount ?? 1;

  const holdings = useMemo<Holdings>(() => {
    const out: Holdings = {};
    for (const item of stream.risk?.underlyings ?? []) for (const position of item.positions) out[position.inst] = (out[position.inst] ?? 0) + position.size;
    return out;
  }, [stream.risk]);
  const picked = useMemo(() => {
    if (mode === "combo") return Object.fromEntries(legs.map((item) => [item.inst, item.side])) as Record<string, Side>;
    return row ? { [row.inst]: side } : {};
  }, [mode, legs, row, side]);

  const select = (option: OptionRow) => {
    setSelectedId(option.inst);
    if (option.expiry !== expiry) setExpiry(option.expiry);
  };
  const pick = (option: OptionRow, chosen: Side) => {
    select(option);
    if (mode === "combo") setLegs((current) => addLeg(current, { inst: option.inst, kind: option.kind, strike: Number(option.strike), expiry: option.expiry, side: chosen, ratio: 1 }));
    else setSide(chosen);
  };
  const enterCombo = () => {
    if (row && !legs.some((item) => item.inst === row.inst)) {
      setLegs((current) => addLeg(current, { inst: row.inst, kind: row.kind, strike: Number(row.strike), expiry: row.expiry, side, ratio: 1 }));
      if (Number(size) > 0) setComboSize(size);
    }
    setMode("combo");
  };
  const preset = (strategy: Strategy, structureSide: Side) => setLegs(buildStrategy(strategy, allRows, forward, structureSide, nextRows));
  const closePosition = (inst: string, held: number) => {
    const option = chain?.rows.find((item) => item.inst === inst);
    if (!option) return;
    setMode("single");
    select(option);
    setSide(held > 0 ? "sell" : "buy");
    setSize(String(Math.abs(held)));
  };

  // The single order's payoff: a one-leg package at the ticket's price.
  const singleSummary = useMemo(() => {
    if (!row || !chain || !(rowForward > 0)) return undefined;
    const units = Number(size) > 0 ? Number(size) : 1;
    const price = Number(limit) > 0 ? limit : side === "buy" ? row.ask ?? row.mark : row.bid ?? row.mark;
    const leg: PackageLeg = { inst: row.inst, kind: row.kind, strike: Number(row.strike), expiry: row.expiry, side, ratio: 1 };
    const entryUsd = premiumUsd(price, !!chain.inverse, rowForward);
    if (!(entryUsd > 0)) return undefined;
    const span = Math.min(0.6, Math.max(0.04, 3 * Math.max(0.2, row.markIv ?? 0) * Math.sqrt(years(row.expiry, now))));
    return summarize([{ leg, row, forward: rowForward, entryUsd }], units, now, span);
  }, [row, chain, rowForward, size, limit, side, now]);
  const onPrice = useCallback((value: string) => setLimit(value), []);
  const onSummary = useCallback((value: PackageSummary | undefined) => setComboSummary(value), []);

  const extraTabs: ExtraTab[] = [
    { value: "orders", label: t("blotter.openOrders"), first: true, render: () => <OptionOrders chain={chain} kind="orders" onSelect={(inst) => { const option = chain?.rows.find((item) => item.inst === inst); if (option) select(option); }} /> },
    { value: "fills", label: t("blotter.tradeHistory"), first: true, render: () => <OptionOrders chain={chain} kind="fills" onSelect={(inst) => { const option = chain?.rows.find((item) => item.inst === inst); if (option) select(option); }} /> },
    { value: "vol", label: t("options.tab.vol"), render: () => (
      <div className="grid h-full min-h-[200px] grid-cols-2 gap-[3px] max-[900px]:grid-cols-1">
        <div className="flex flex-col"><div className="px-3 py-1.5 text-muted" title={t("options.smileHint")}>{t("options.smile")} · {selected ? expiryLabel(selected.expiry) : ""}</div><SmileChart rows={allRows} forward={forward} /></div>
        <div className="flex flex-col"><div className="px-3 py-1.5 text-muted" title={t("options.termHint")}>{t("options.term")}</div><TermChart expiries={expiries} selected={expiry} now={now} /></div>
      </div>
    ) },
  ];

  if (series.isSuccess && list.length === 0) {
    return <main className="flex flex-1 items-center justify-center bg-surface text-muted">{t("options.noSeries")}</main>;
  }

  return (
    <main className="grid min-h-0 flex-1 gap-[3px] px-[3px] max-[1100px]:flex max-[1100px]:flex-col max-[1100px]:overflow-y-auto min-[1101px]:grid-cols-[minmax(0,1fr)_300px_320px] min-[1101px]:grid-rows-[auto_auto_minmax(0,1fr)_minmax(200px,30%)]">
      {/* Header */}
      <div className="flex min-h-14 flex-wrap items-center gap-x-6 gap-y-1 rounded-md bg-surface px-4 py-2 min-[1101px]:col-span-3">
        <SeriesPicker list={list} current={current} onChange={(id) => { setSeriesId(id); save("keeluni.options.series", id); }} />
        <Stat label={t("options.index")} value={current?.index ? num(current.index, 2) : "—"} hint={t("options.indexHint")} />
        <Stat label={t("options.forward")} value={forward ? num(forward, 2) : "—"} hint={t("options.forwardHint")} />
        <Stat label={t("options.atmIv")} value={selected?.atmIv ? `${(selected.atmIv * 100).toFixed(1)}%` : "—"} hint={t("options.atmIvHint")} />
        <Stat label="25Δ RR" value={selected?.rr25 ? signedVol(selected.rr25) : "—"} hint={t("options.rrHint")} tone={selected?.rr25} />
        <Stat label="25Δ BF" value={selected?.bf25 ? signedVol(selected.bf25) : "—"} hint={t("options.bfHint")} />
        {chain?.stale && <span className="text-down" title={t("options.staleHint")}>{t("options.stale")}</span>}
        <div className="ml-auto flex items-center gap-2">
          <Toggle value={quoteMode} options={[["price", t("options.showPrice")], ["iv", t("options.showIv")]]} onChange={(value) => { setQuoteMode(value); save("keeluni.options.mode", value); }} />
          {chain?.inverse && (
            <Toggle value={usdUnit ? "usd" : "coin"} options={[["coin", chain.premiumAsset], ["usd", "USD"]]} onChange={(value) => { setUsdUnit(value === "usd"); save("keeluni.options.usd", value === "usd"); }} />
          )}
          <span className="mx-1 h-5 w-px bg-line" />
          <Toggle value={mode} options={[["single", t("options.single")], ["combo", legs.length ? `${t("options.combo")} ${legs.length}` : t("options.combo")]]}
            onChange={(value) => (value === "combo" ? enterCombo() : setMode("single"))} />
        </div>
      </div>

      {/* Expiry strip */}
      <div className="flex gap-1 overflow-x-auto rounded-md bg-surface px-2 py-1.5 min-[1101px]:col-span-3">
        {expiries.map((item) => (
          <button key={item.expiry} onClick={() => setExpiry(item.expiry)}
            className={cx("flex shrink-0 flex-col items-center rounded-md px-2.5 py-0.5", item.expiry === expiry ? "bg-raised text-text" : "text-muted hover:text-text")}>
            <span>{expiryLabel(item.expiry)}</span>
            <span className="text-xs text-muted">{dteLabel(item.expiry, now)}{item.atmIv ? ` · ${(item.atmIv * 100).toFixed(0)}%` : ""}</span>
          </button>
        ))}
      </div>

      {/* Board */}
      <div className="flex min-h-[440px] min-w-0 flex-col rounded-md bg-surface min-[1101px]:col-start-1 min-[1101px]:row-start-3">
        <div className="flex h-9 shrink-0 items-center gap-3 border-b border-line px-3">
          <span className="text-text">{selected ? expiryLabel(selected.expiry) : ""}</span>
          <span className="text-muted">{selected ? t("options.strikesCount", { n: selected.strikes }) : ""}</span>
          <span className="text-muted">{mode === "combo" ? t("options.boardComboHint") : t("options.boardHint")}</span>
          <span className="ml-auto text-muted">{t("options.strikesShown")}</span>
          <Toggle value={String(window)} options={[["8", "±8"], ["12", "±12"], ["0", t("common.all")]]} onChange={(value) => { setWindow(Number(value)); save("keeluni.options.window", Number(value)); }} />
        </div>
        <ChainBoard chain={chain} rows={rows} forward={forward} mode={quoteMode} usdUnit={usdUnit} holdings={holdings} picked={picked}
          selected={row?.inst ?? ""} onPick={pick} onSelect={select} />
      </div>

      {/* Selected option: quote, book, payoff */}
      <div className="flex min-h-[440px] flex-col overflow-hidden rounded-md min-[1101px]:col-start-2 min-[1101px]:row-start-3">
        {chain && row ? (
          <ContractInfo chain={chain} row={row} forward={rowForward} discount={rowDiscount} now={now} instrument={instruments.get(row.inst)}
            summary={mode === "combo" ? comboSummary : singleSummary}
            payoffLabel={mode === "combo" ? t("options.payoffCombo", { n: legs.length }) : `${t(side === "buy" ? "common.buy" : "common.sell")} ${Number(size) > 0 ? size : 1} ${chain.underlying}${Number(limit) > 0 ? ` @ ${limit}` : ""}`} />
        ) : (
          <div className="flex flex-1 items-center justify-center bg-surface text-muted">{t("common.loading")}</div>
        )}
      </div>

      {/* Ticket */}
      <aside className="flex min-h-0 flex-col overflow-hidden rounded-md min-[1101px]:col-start-3 min-[1101px]:row-span-2 min-[1101px]:row-start-3">
        {mode === "combo" ? (
          <PackageTicket chain={chain} steps={steps} legs={legs} setLegs={setLegs} size={comboSize} setSize={setComboSize} forward={forward} now={now}
            accounts={optionAccounts} account={account} setAccount={chooseAccount} onPreset={preset} onSummary={onSummary} onExit={() => setMode("single")} />
        ) : chain && row ? (
          <ContractTicket chain={chain} row={row} instrument={instruments.get(row.inst)} forward={rowForward} discount={rowDiscount} now={now}
            side={side} setSide={setSide} size={size} setSize={setSize} onPrice={onPrice}
            accounts={optionAccounts} account={account} setAccount={chooseAccount} onCombo={enterCombo} />
        ) : (
          <div className="flex-1 bg-surface" />
        )}
      </aside>

      {/* Orders, fills, risk, volatility */}
      <div className="flex min-h-[240px] overflow-hidden rounded-md min-[1101px]:col-span-2 min-[1101px]:col-start-1 min-[1101px]:row-start-4">
        <RiskPanel risk={stream.risk} executions={stream.executions} online={stream.online} accounts={accounts}
          hedges={hedges} setHedges={(value) => { setHedges(value); save("keeluni.options.hedges", value); }} days={days} setDays={setDays}
          onClosePosition={closePosition} extra={extraTabs} />
      </div>
    </main>
  );
}

/** Open option orders or recent option fills across accounts; a row selects its option. */
function OptionOrders({ chain, kind, onSelect }: { chain: OptionChain | undefined; kind: "orders" | "fills"; onSelect: (inst: string) => void }) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { openOrders, fills } = useWorkspace();
  const isOption = (inst: string) => inst.includes(":option:");
  const nameOf = (inst: string) => {
    const option = chain?.rows.find((item) => item.inst === inst);
    return chain && option ? contractName(chain, option) : inst.split(":").slice(2).join(":");
  };
  const cancel = async (accountId: string, inst: string, orderId: string) => {
    try {
      await api.cancel(accountId, inst, orderId);
      queryClient.setQueryData<Order[]>(["orders", accountId], (current = []) => current.filter((item) => item.orderId !== orderId));
    } catch (error) {
      toast.push({ kind: "error", title: t("blotter.cancelFailed"), detail: (error as Error).message });
    }
  };
  const th = "sticky top-0 bg-surface px-3 py-1.5 text-right font-normal text-muted whitespace-nowrap first:text-left";
  const td = "px-3 h-6 text-right tabular-nums whitespace-nowrap first:text-left";
  if (kind === "orders") {
    const orders = openOrders.filter((order) => isOption(order.inst));
    if (!orders.length) return <div className="flex h-full items-center justify-center text-muted">{t("blotter.noOrders")}</div>;
    return (
      <table className="w-full border-collapse">
        <thead><tr>
          <th className={th}>{t("common.time")}</th><th className={cx(th, "!text-left")}>{t("options.contract")}</th><th className={th}>{t("common.side")}</th>
          <th className={th}>{t("common.price")}</th><th className={th}>{t("blotter.filled")}</th><th className={th} />
        </tr></thead>
        <tbody>
          {orders.map((order) => (
            <tr key={order.account + order.orderId} className="cursor-pointer hover:bg-raised" onClick={() => onSelect(order.inst)}>
              <td className={td}>{time(order.createdAt ?? order.updatedAt ?? 0)}</td>
              <td className={cx(td, "!text-left")}>{nameOf(order.inst)}</td>
              <td className={cx(td, order.side === "buy" ? "text-up" : "text-down")}>{t(order.side === "buy" ? "common.buy" : "common.sell")}</td>
              <td className={td}>{num(order.price)}</td>
              <td className={td}>{num(order.filled)} / {num(order.qty)}</td>
              <td className={td}><button className="text-accent hover:underline" onClick={(event) => { event.stopPropagation(); void cancel(order.account, order.inst, order.orderId); }}>{t("common.cancel")}</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    );
  }
  const trades = fills.filter((fill) => isOption(fill.inst)).slice(0, 100);
  if (!trades.length) return <div className="flex h-full items-center justify-center text-muted">{t("blotter.noTrades")}</div>;
  return (
    <table className="w-full border-collapse">
      <thead><tr>
        <th className={th}>{t("common.time")}</th><th className={cx(th, "!text-left")}>{t("options.contract")}</th><th className={th}>{t("common.side")}</th>
        <th className={th}>{t("common.price")}</th><th className={th}>{t("common.size")}</th><th className={th}>{t("common.fee")}</th>
      </tr></thead>
      <tbody>
        {trades.map((fill) => (
          <tr key={fill.account + fill.fillId} className="cursor-pointer hover:bg-raised" onClick={() => onSelect(fill.inst)}>
            <td className={td}>{time(fill.ts)}</td>
            <td className={cx(td, "!text-left")}>{nameOf(fill.inst)}</td>
            <td className={cx(td, fill.side === "buy" ? "text-up" : "text-down")}>{t(fill.side === "buy" ? "common.buy" : "common.sell")}</td>
            <td className={td}>{num(fill.price)}</td>
            <td className={td}>{num(fill.qty)}</td>
            <td className={cx(td, "text-muted")}>{fill.fee} {fill.feeAsset}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function signedVol(value: number): string {
  return (value > 0 ? "+" : "") + (value * 100).toFixed(1);
}

function Stat({ label, value, hint, tone }: { label: string; value: string; hint: string; tone?: number }) {
  return (
    <div className="flex flex-col" title={hint}>
      <span className="dotted text-muted">{label}</span>
      <span className={cx("tabular-nums", tone === undefined ? "text-text" : tone > 0 ? "text-up" : tone < 0 ? "text-down" : "text-text")}>{value}</span>
    </div>
  );
}

function Toggle<T extends string>({ value, options, onChange }: { value: T; options: Array<[T, string]>; onChange: (value: T) => void }) {
  return (
    <span className="flex rounded-md bg-raised p-0.5">
      {options.map(([key, label]) => (
        <button key={key} onClick={() => onChange(key)} aria-pressed={value === key}
          className={cx("rounded px-2 leading-5", value === key ? "bg-line-strong/60 text-text" : "text-muted hover:text-text")}>
          {label}
        </button>
      ))}
    </span>
  );
}
