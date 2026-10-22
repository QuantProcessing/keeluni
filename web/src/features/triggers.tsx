import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "@/api/client";
import type { Position, Trigger, TriggerKind, TriggerPriceType } from "@/api/types";
import { useT } from "@/i18n";
import { instrumentName, num, price, snapToStep, usd } from "@/lib/format";
import { accountName, rulesFor } from "@/lib/routes";
import { useInstrument, useLastPrice, useRoutes } from "@/state/hooks";
import { useWorkspace, type AccountTrigger } from "@/state/workspace";
import { Dialog, PrimaryButton, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";

import { submitTrigger } from "./submit";

/** The resting triggers that close this position (its closing side). */
export function triggersOf(triggers: AccountTrigger[], position: Position & { account: string }): AccountTrigger[] {
  const closing = position.side === "long" ? "sell" : "buy";
  return triggers.filter((item) => item.account === position.account && item.inst === position.inst && item.side === closing);
}

/** "2,600 / 2,300" in the positions table: take-profit / stop-loss. */
export function TriggerSummary({ triggers, tick }: { triggers: Trigger[]; tick?: string }) {
  const t = useT();
  const take = triggers.filter((item) => item.kind === "take_profit");
  const stop = triggers.filter((item) => item.kind === "stop_loss");
  const cell = (items: Trigger[]) =>
    items.length ? <>{price(items[0]!.triggerPrice, tick)}{items.length > 1 && <span className="text-muted"> +{items.length - 1}</span>}</> : <span className="text-muted">—</span>;
  return (
    <span className="tabular-nums" title={t("tpsl.summaryHint")}>
      {cell(take)}<span className="text-muted"> / </span>{cell(stop)}
    </span>
  );
}

/** Where a trigger price must sit: a take-profit beyond the price in the position's favour, a stop the other way. */
export function triggerSideProblem(kind: TriggerKind, long: boolean, level: number, reference: number): "above" | "below" | undefined {
  if (!(level > 0) || !(reference > 0)) return undefined;
  const above = kind === "take_profit" ? long : !long;
  if (above && level <= reference) return "above";
  if (!above && level >= reference) return "below";
  return undefined;
}

/**
 * Take-profit and stop-loss of one position: the resting ones (cancellable)
 * and new ones. Each is a reduce-only market order the venue sends when its
 * trigger price is crossed, for a share of the position. Both are checked
 * against the current price so a trigger never fires on placement.
 */
export function TriggerDialog({ position, account: accountId, triggers, onDone }: { position: Position; account: string; triggers: Trigger[]; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { accounts } = useWorkspace();
  const account = accounts.find((item) => item.id === accountId);
  const instrument = useInstrument(position.inst);
  const routes = useRoutes(accountId).data;
  const route = routes?.find((item) => item.inst === position.inst);
  const rules = instrument ? rulesFor(instrument, routes) : undefined;
  const priceTypes: TriggerPriceType[] = route?.triggers?.priceTypes?.length ? route.triggers.priceTypes : ["last"];
  const last = useLastPrice(position.inst);
  const [priceType, setPriceType] = useState<TriggerPriceType>(priceTypes[0]!);
  const [take, setTake] = useState("");
  const [stop, setStop] = useState("");
  const [share, setShare] = useState(100);
  const [busy, setBusy] = useState(false);
  const [cancelling, setCancelling] = useState<string | null>(null);

  const long = position.side === "long";
  const side = long ? "sell" : "buy";
  const tick = rules?.tickSize ?? instrument?.tickSize;
  const reference = Number(priceType === "mark" ? position.markPrice ?? last : last ?? position.markPrice);
  const entry = Number(position.entryPrice);
  const qty = share >= 100 ? position.size : snapToStep(String((Number(position.size) * share) / 100), rules?.stepSize ?? "0.00000001");
  const tooSmall = !(Number(qty) > 0) || (rules?.minQty !== undefined && Number(qty) < Number(rules.minQty));

  const problem = (kind: TriggerKind, value: string): string | undefined => {
    if (!value) return undefined;
    const level = Number(value);
    if (!(level > 0)) return t("tpsl.invalidPrice");
    const wrong = triggerSideProblem(kind, long, level, reference);
    if (wrong === "above") return t("tpsl.mustBeAbove", { price: price(reference, tick) });
    if (wrong === "below") return t("tpsl.mustBeBelow", { price: price(reference, tick) });
    return undefined;
  };
  const pnlAt = (value: string) => {
    const level = Number(value);
    if (!(level > 0) || !(entry > 0)) return undefined;
    return (level - entry) * Number(qty) * (long ? 1 : -1);
  };
  const takeProblem = problem("take_profit", take);
  const stopProblem = problem("stop_loss", stop);
  const ready = Boolean((take || stop) && !takeProblem && !stopProblem && !tooSmall && account);

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["triggers", accountId] });
  const submit = async () => {
    if (!account) return;
    setBusy(true);
    const wanted: Array<[TriggerKind, string]> = [];
    if (take) wanted.push(["take_profit", take]);
    if (stop) wanted.push(["stop_loss", stop]);
    let failed = false;
    for (const [kind, value] of wanted) {
      const triggerPrice = snapToStep(value, tick ?? "0.00000001");
      const outcome = await submitTrigger(account.id, { inst: position.inst, side, kind, triggerPrice, priceType, qty });
      const label = t(kind === "take_profit" ? "tpsl.takeProfit" : "tpsl.stopLoss");
      if (outcome.kind === "accepted") {
        toast.push({ kind: "info", title: t("tpsl.placed", { kind: label, price: price(triggerPrice, tick), qty: num(qty), name: instrumentName(position.inst) }), detail: accountName(account) });
        if (kind === "take_profit") setTake(""); else setStop("");
      } else {
        failed = true;
        toast.push({ kind: outcome.kind === "unknown" ? "info" : "error", title: t("tpsl.failed", { kind: label }), detail: outcome.message });
      }
    }
    setBusy(false);
    await refresh();
    if (!failed) onDone();
  };
  const cancel = async (trigger: Trigger) => {
    setCancelling(trigger.orderId);
    try {
      await api.cancelTrigger(accountId, trigger.inst, trigger.orderId);
      queryClient.setQueryData<Trigger[]>(["triggers", accountId], (current = []) => current.filter((item) => item.orderId !== trigger.orderId));
    } catch (error) {
      toast.push({ kind: "error", title: t("blotter.cancelFailed"), detail: (error as Error).message });
    } finally {
      setCancelling(null);
    }
  };

  const field = "flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong";
  const section = (kind: TriggerKind, value: string, setValue: (value: string) => void, issue: string | undefined) => {
    const resting = triggers.filter((item) => item.kind === kind);
    const pnl = pnlAt(value);
    return (
      <div className="flex flex-col gap-1.5">
        <div className="flex items-baseline justify-between">
          <span>{t(kind === "take_profit" ? "tpsl.takeProfit" : "tpsl.stopLoss")}</span>
          {pnl !== undefined && !issue && (
            <span className="text-muted">{t("tpsl.estimate")} <span className={pnl >= 0 ? "text-up" : "text-down"}>{usd(pnl)}</span></span>
          )}
        </div>
        {resting.map((item) => (
          <div key={item.orderId} className="flex items-center gap-2 rounded-md bg-raised px-2.5 py-1.5 text-text-2">
            <span className="text-muted">{t(item.priceType === "mark" ? "tpsl.markTrigger" : "tpsl.lastTrigger")}</span>
            <span className="tabular-nums text-text">{price(item.triggerPrice, tick)}</span>
            <span className="text-muted">·</span>
            <span className="tabular-nums">{item.closePosition ? t("tpsl.wholePosition") : `${num(item.qty)} ${instrument?.base ?? ""}`}</span>
            <button className="ml-auto text-accent hover:underline disabled:text-muted" disabled={cancelling === item.orderId} onClick={() => cancel(item)}>
              {cancelling === item.orderId ? t("common.cancelling") : t("common.cancel")}
            </button>
          </div>
        ))}
        <label className={cx(field, issue && "border-down/70")}>
          <span className="text-muted">{t(resting.length ? "tpsl.addTrigger" : "tpsl.triggerPrice")}</span>
          <input inputMode="decimal" value={value} onChange={(event) => setValue(event.target.value.replace(/[^\d.]/g, ""))} placeholder="—"
            aria-label={t(kind === "take_profit" ? "tpsl.takeProfit" : "tpsl.stopLoss")}
            className="min-w-0 flex-1 bg-transparent text-right tabular-nums outline-none placeholder:text-faint" />
          <span className="text-muted">{instrument?.quote}</span>
        </label>
        {issue && <div className="text-down">{issue}</div>}
      </div>
    );
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onDone()} title={t("tpsl.title")} width={440}
      description={t("tpsl.description", { side: t(long ? "side.long" : "side.short"), qty: num(position.size), name: instrumentName(position.inst), account: account ? accountName(account) : accountId })}>
      <div className="flex flex-col gap-4">
        <div className="grid grid-cols-3 gap-2 text-text-2">
          <div><div className="text-muted">{t("blotter.entryPrice")}</div><div className="tabular-nums">{price(position.entryPrice, tick)}</div></div>
          <div><div className="text-muted">{t("market.price")}</div><div className="tabular-nums">{price(last, tick)}</div></div>
          <div><div className="text-muted">{t("blotter.markPrice")}</div><div className="tabular-nums">{price(position.markPrice, tick)}</div></div>
        </div>
        {priceTypes.length > 1 && (
          <div className="flex items-center gap-2">
            <span className="dotted text-muted" title={t("tpsl.priceTypeHint")}>{t("tpsl.priceType")}</span>
            <div className="ml-auto flex gap-1">
              {priceTypes.map((item) => (
                <button key={item} onClick={() => setPriceType(item)} className={cx("h-7 rounded-md px-2.5", priceType === item ? "bg-line-strong/60 text-text" : "bg-raised text-muted hover:text-text")}>
                  {t(item === "mark" ? "blotter.markPrice" : "market.price")}
                </button>
              ))}
            </div>
          </div>
        )}
        {section("take_profit", take, setTake, takeProblem)}
        {section("stop_loss", stop, setStop, stopProblem)}
        <div className="flex flex-col gap-1.5">
          <div className="flex items-center">
            <span className="text-muted">{t("common.size")}</span>
            <span className="ml-auto tabular-nums">{num(qty)} {instrument?.base}</span>
          </div>
          <div className="grid grid-cols-4 gap-1">
            {[25, 50, 75, 100].map((value) => (
              <button key={value} onClick={() => setShare(value)} className={cx("h-7 rounded-md", share === value ? "bg-line-strong/60 text-text" : "bg-raised text-muted hover:text-text")}>{value}%</button>
            ))}
          </div>
          {tooSmall && rules && <div className="text-down">{t("ticket.minSizeIs", { min: rules.minQty })}</div>}
        </div>
        <PrimaryButton onClick={submit} disabled={busy || !ready}>{busy ? t("tpsl.placing") : t("tpsl.confirm")}</PrimaryButton>
        <div className="text-center text-muted">{t("tpsl.footnote")}</div>
      </div>
    </Dialog>
  );
}
