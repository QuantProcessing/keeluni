import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import { useBook, useFunding, useInstrument } from "@/state/hooks";
import { averageApr, aprText, bpsText, inferMatch, netFundingApr, spreadBps, takePrice, tone, type Pair } from "./model";

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
 * The numbers that decide whether a pair is worth trading, side by side:
 * the basis spread at which an entry would execute now, net funding now,
 * and net funding averaged over the last 7 days (a one-period spike is not
 * a carry).
 */
export function PairBar({ pair, onSwap, onFlip }: { pair: Pair; onSwap: () => void; onFlip: () => void }) {
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

  const matching = leg0 && leg1 ? inferMatch(leg0.base, leg1.base) : undefined;
  const entry = matching?.match === "base"
    ? spreadBps(takePrice(book0, pair[0].side) ?? 0, takePrice(book1, pair[1].side) ?? 0, matching.ratio)
    : undefined;
  const sameSide = pair[0].side === pair[1].side;
  const netNow = netFundingApr([
    { side: pair[0].side, apr: funding0?.apr !== undefined ? Number(funding0.apr) : undefined, spot: !perp0 },
    { side: pair[1].side, apr: funding1?.apr !== undefined ? Number(funding1.apr) : undefined, spot: !perp1 },
  ]);
  const netWeek = netFundingApr([
    { side: pair[0].side, apr: week0, spot: !perp0 },
    { side: pair[1].side, apr: week1, spot: !perp1 },
  ]);
  const label = (side: string) => (side === "buy" ? "Long" : "Short");

  return (
    <div className="flex min-h-14 flex-wrap items-center gap-x-6 gap-y-2 bg-surface px-3 py-2">
      <div className="flex items-center gap-2 text-md">
        <span className={pair[0].side === "buy" ? "text-up" : "text-down"}>{label(pair[0].side)}</span>
        <span>{leg0?.symbol ?? pair[0].inst.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{leg0?.venue}</span>
        <span className="text-muted">/</span>
        <span className={pair[1].side === "buy" ? "text-up" : "text-down"}>{label(pair[1].side)}</span>
        <span>{leg1?.symbol ?? pair[1].inst.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{leg1?.venue}</span>
      </div>
      <div className="flex gap-1">
        <button onClick={onFlip} title="Swap which leg is long and which is short" className="h-7 rounded-md bg-raised px-2 text-text-2 hover:text-text">Flip sides</button>
        <button onClick={onSwap} title="Swap leg 1 and leg 2" className="h-7 rounded-md bg-raised px-2 text-text-2 hover:text-text">Swap legs</button>
      </div>
      {sameSide && matching?.match === "base" && <span className="rounded-md bg-warn-bg px-2 py-1 text-down">Both legs are {label(pair[0].side).toLowerCase()} — not a hedge</span>}
      <Metric label="Entry spread" hint="Price of leg 2 over leg 1 at the prices a market entry would trade at (ask for the long leg, bid for the short leg), in basis points of leg 1. Same underlying only." value={bpsText(entry)} />
      <Metric label="Net funding" hint="What the pair earns per year from funding at the current rates: the short leg receives its rate, the long leg pays it. Annualized so hourly and 8-hourly venues compare." value={aprText(netNow)} className={tone(netNow)} />
      <Metric label="Net funding 7d" hint="The same, using each leg's average settled rate over the last 7 days." value={aprText(netWeek)} className={tone(netWeek)} />
      {matching?.match === "base" && matching.ratio !== 1 && <Metric label="Ratio" hint="Leg-2 contracts per leg-1 unit (e.g. 1000PEPE against PEPE)" value={String(matching.ratio)} />}
      {matching?.match === "notional" && <Metric label="Matching" hint="Different underlyings are sized to equal notional at entry" value="By notional" />}
    </div>
  );
}

function Metric({ label, value, hint, className }: { label: string; value: string; hint: string; className?: string }) {
  return (
    <div className="shrink-0">
      <div className="dotted text-muted" title={hint}>{label}</div>
      <div className={"mt-1 " + (className ?? "")}>{value}</div>
    </div>
  );
}
