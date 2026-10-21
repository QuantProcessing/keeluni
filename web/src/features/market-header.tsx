import { compact, countdown, pct, price, signed } from "@/lib/format";
import { useInstrument, useLastPrice, useNow, useStats, useTicker } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { useT } from "@/i18n";

import { InstrumentPicker } from "./instrument-picker";

export { venueLabel } from "@/lib/format";
export { InstrumentPicker } from "./instrument-picker";

export function MarketHeader() {
  const t = useT();
  const { instrumentId, setInstrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const perp = instrument?.product === "perp";
  const stats = useStats(instrumentId).data;
  const ticker = useTicker(instrumentId, perp);
  const last = useLastPrice(instrumentId);
  const now = useNow();

  const open = Number(stats?.open24h);
  const change = last && open ? Number(last) - open : undefined;
  const funding = ticker?.funding ?? stats?.funding;
  const fundingHours = ticker?.fundingIntervalHours ?? stats?.fundingIntervalHours;
  const fundingApr = ticker?.fundingApr ?? stats?.fundingApr;
  const nextFunding = ticker?.nextFunding ?? stats?.nextFunding;
  const tick = instrument?.tickSize;

  return (
    <div className="flex h-14 shrink-0 items-center gap-6 overflow-x-auto bg-surface px-3">
      <InstrumentPicker value={instrumentId} onChange={setInstrumentId} hotkey />
      <Stat label={t("market.price")} value={price(last, tick)} />
      {perp && <Stat label={t("market.mark")} hint={t("market.markHint")} value={price(ticker?.mark ?? stats?.mark, tick)} />}
      {perp && ticker?.index && <Stat label={t("market.index")} hint={t("market.indexHint")} value={price(ticker.index, tick)} />}
      <Stat
        label={t("market.change24h")}
        value={change === undefined ? "—" : `${signed(change, tick ? Math.min(8, tick.split(".")[1]?.length ?? 0) : 2)} / ${pct(change / open)}`}
        tone={change === undefined ? undefined : change >= 0 ? "up" : "down"}
      />
      <Stat label={t("market.volume24h")} value={stats ? compact(Number(stats.volume24h) * Number(last ?? 0)) + " " + (instrument?.quote ?? "") : "—"} />
      {perp && ticker?.oi && <Stat label={t("market.openInterest")} value={compact(ticker.oi) + " " + (instrument?.base ?? "")} />}
      {perp && (
        <div className="shrink-0">
          <div className="dotted text-muted" title={t("market.fundingHint")}>{t("market.funding")}</div>
          <div className="mt-1 flex gap-3">
            <span className={Number(funding) >= 0 ? "text-up" : "text-down"}>
              {fundingRate(funding)}{fundingHours ? <span className="text-muted"> /{fundingHours}h</span> : null}
            </span>
            <span className={Number(fundingApr) >= 0 ? "text-up" : "text-down"}>{fundingApr ? pct(Number(fundingApr)) : "—"}</span>
            <span>{countdown(nextFunding, now)}</span>
          </div>
        </div>
      )}
    </div>
  );
}

/** Funding per settlement, as a percentage with enough digits for hourly venues. */
export function fundingRate(value: string | undefined): string {
  if (!value) return "—";
  const percent = Number(value) * 100;
  return (Math.abs(percent) < 0.001 && percent !== 0 ? percent.toFixed(6) : percent.toFixed(4)) + "%";
}

function Stat({ label, value, tone, hint }: { label: string; value: string; tone?: "up" | "down"; hint?: string }) {
  return (
    <div className="shrink-0">
      <div className={cx("text-muted", hint && "dotted")} title={hint}>{label}</div>
      <div className={cx("mt-1", tone === "up" && "text-up", tone === "down" && "text-down")}>{value}</div>
    </div>
  );
}
