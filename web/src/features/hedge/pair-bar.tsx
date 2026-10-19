import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import { useBook, useFunding, useInstrument } from "@/state/hooks";
import { cx } from "@/ui/primitives";
import { InstrumentPicker } from "../market-header";
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
          aria-label="Swap long and short"
          title={canFlip ? "Swap which leg is long and which is short" : "A spot leg can only be long"}
          className="flex size-8 items-center justify-center rounded-lg bg-raised text-text-2 hover:text-text disabled:cursor-not-allowed disabled:opacity-40"
        >
          ⇄
        </button>
        <Side side={pair[1].side} />
        <InstrumentPicker value={pair[1].inst} onChange={(inst) => onInstrument(1, inst)} />
      </div>
      <div className="ml-auto flex flex-wrap items-center gap-x-6 gap-y-2">
        {matching?.match === "base" && <Metric label="Entry spread" hint="Leg 2 over leg 1 at the prices a market entry would trade at (ask for the long leg, bid for the short leg), in basis points of leg 1." value={bpsText(entry)} />}
        {matching?.match === "base" && matching.ratio !== 1 && <Metric label="Ratio" hint="Leg-2 contracts per leg-1 unit, from the contract multipliers (e.g. 1000PEPE against PEPE)" value={String(matching.ratio)} />}
        {matching?.match === "notional" && <Metric label="Matching" hint="Different underlyings are sized to equal notional at entry" value="By notional" />}
        <Metric label="Net funding" hint="What the pair earns per year at the current rates: the short leg receives its rate, the long leg pays it. Annualized so hourly and 8-hourly venues compare." value={aprText(netNow)} className={tone(netNow)} />
        <Metric label="Net funding 7d" hint="The same, using each leg's average settled rate over the last 7 days." value={aprText(netWeek)} className={tone(netWeek)} />
      </div>
    </div>
  );
}

function Side({ side }: { side: "buy" | "sell" }) {
  return <span className={cx("w-9 shrink-0", side === "buy" ? "text-up" : "text-down")}>{side === "buy" ? "Long" : "Short"}</span>;
}

function Metric({ label, value, hint, className }: { label: string; value: string; hint: string; className?: string }) {
  return (
    <div className="shrink-0">
      <div className="dotted text-muted" title={hint}>{label}</div>
      <div className={"mt-1 " + (className ?? "")}>{value}</div>
    </div>
  );
}
