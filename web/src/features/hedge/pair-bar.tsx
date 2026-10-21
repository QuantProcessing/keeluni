import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import { useBook, useFunding, useInstrument } from "@/state/hooks";
import { cx } from "@/ui/primitives";
import { InstrumentPicker } from "../market-header";
import { useT } from "@/i18n";
import { averageApr, aprText, bpsText, netFundingApr, spreadBps, takePrice, tone, type Pair } from "./model";

const WEEK = 7 * 24 * 3600 * 1000;

/** 7-day funding history of a perp leg (cached by react-query for 10 min). */
function useWeekApr(inst: string, perp: boolean): number | undefined {
  const since = Math.floor((Date.now() - WEEK) / 3_600_000) * 3_600_000;
  const query = useQuery({
    queryKey: ["fundingHistory", inst, since],
    queryFn: ({ signal }) => api.fundingHistory(inst, since, signal),
    enabled: perp && !!inst,
    staleTime: 10 * 60_000,
    retry: false,
  });
  return perp ? averageApr(query.data, since) : undefined;
}

/**
 * The pair and the numbers that decide whether to trade it. Each leg's
 * market is picked here; one leg is always long and the other short, and ⇄
 * swaps which. Then: the spread at which a market entry would trade now, net
 * funding now and averaged over 7 days (a one-period spike is not a carry),
 * and the size ratio when the legs' contracts differ.
 */
export function PairBar({ pair, matching, onInstrument, onFlip, canFlip }: {
  pair: Pair;
  matching: { match: "base" | "notional"; ratio: number } | undefined;
  onInstrument: (index: 0 | 1, inst: string) => void;
  onFlip: () => void;
  canFlip: boolean;
}) {
  const t = useT();
  const leg0 = useInstrument(pair[0].inst);
  const leg1 = useInstrument(pair[1].inst);
  const perp0 = leg0?.product === "perp";
  const perp1 = leg1?.product === "perp";
  const book0 = useBook(pair[0].inst);
  const book1 = useBook(pair[1].inst);
  const funding0 = useFunding(pair[0].inst, perp0);
  const funding1 = useFunding(pair[1].inst, perp1);
  const week0 = useWeekApr(pair[0].inst, perp0);
  const week1 = useWeekApr(pair[1].inst, perp1);

  const entry = matching?.match === "base"
    ? spreadBps(takePrice(book0, pair[0].side) ?? 0, takePrice(book1, pair[1].side) ?? 0, matching.ratio)
    : undefined;
  const netNow = netFundingApr([
    { side: pair[0].side, apr: funding0?.apr !== undefined ? Number(funding0.apr) : undefined, spot: !perp0 },
    { side: pair[1].side, apr: funding1?.apr !== undefined ? Number(funding1.apr) : undefined, spot: !perp1 },
  ]);
  const netWeek = netFundingApr([
    { side: pair[0].side, apr: week0, spot: !perp0 },
    { side: pair[1].side, apr: week1, spot: !perp1 },
  ]);

  return (
    <div className="flex min-h-14 flex-wrap items-center gap-x-6 gap-y-2 bg-surface px-3 py-2">
      <div className="flex items-center gap-3">
        <Side side={pair[0].side} />
        <InstrumentPicker value={pair[0].inst} onChange={(inst) => onInstrument(0, inst)} />
        <button
          onClick={onFlip}
          disabled={!canFlip}
          aria-label={t("pair.swap")}
          title={canFlip ? t("pair.swapHint") : t("pair.spotLongOnly")}
          className="flex size-8 items-center justify-center rounded-lg bg-raised text-text-2 hover:text-text disabled:cursor-not-allowed disabled:opacity-40"
        >
          ⇄
        </button>
        <Side side={pair[1].side} />
        <InstrumentPicker value={pair[1].inst} onChange={(inst) => onInstrument(1, inst)} />
      </div>
      <div className="ml-auto flex flex-wrap items-center gap-x-6 gap-y-2">
        {matching?.match === "base" && <Metric label={t("pair.entrySpread")} hint={t("pair.entrySpreadHint")} value={bpsText(entry)} />}
        {matching?.match === "base" && matching.ratio !== 1 && <Metric label={t("pair.ratio")} hint={t("pair.ratioHint")} value={String(matching.ratio)} />}
        {matching?.match === "notional" && <Metric label={t("pair.matching")} hint={t("pair.matchingHint")} value={t("pair.byNotional")} />}
        <Metric label={t("pair.netFunding")} hint={t("pair.netFundingHint")} value={aprText(netNow)} className={tone(netNow)} />
        <Metric label={t("pair.netFunding7d")} hint={t("pair.netFunding7dHint")} value={aprText(netWeek)} className={tone(netWeek)} />
      </div>
    </div>
  );
}

/** The leg's side: the one place on the page the direction is coloured. */
function Side({ side }: { side: "buy" | "sell" }) {
  const t = useT();
  return <span className={cx("shrink-0 rounded px-1.5 py-0.5 text-xs", side === "buy" ? "bg-long/20 text-up" : "bg-short-deep/50 text-down")}>{t(side === "buy" ? "common.long" : "common.short")}</span>;
}

function Metric({ label, value, hint, className }: { label: string; value: string; hint: string; className?: string }) {
  return (
    <div className="shrink-0">
      <div className="dotted text-muted" title={hint}>{label}</div>
      <div className={"mt-1 " + (className ?? "")}>{value}</div>
    </div>
  );
}
