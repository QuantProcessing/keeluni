import { useEffect, useMemo, useState } from "react";

import { useHedgeGroups } from "@/api/hedge";
import { useAccountStream } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { GroupsPanel } from "./groups";
import { LegPanel } from "./leg-panel";
import { PairBar } from "./pair-bar";
import { DEFAULT_PAIR, type LegChoice, type Pair } from "./model";
import { HedgeTicket } from "./ticket";

/**
 * The hedge workspace. A hedge trader reads it top-down:
 *   pair bar   — the two legs and the numbers that decide whether to trade
 *                them (basis spread, net funding now and over 7 days)
 *   leg panels — each leg's book, funding clock, account, position
 *   groups     — every hedge as one row, expandable to its legs and history
 * with the ticket on the right, always in reach.
 */
export function HedgeApp() {
  const { accounts } = useWorkspace();
  const [pair, setPair] = useState<Pair>(() => {
    try {
      const saved = JSON.parse(localStorage.getItem("bolter.hedge.pair") ?? "null") as Pair | null;
      if (saved?.length === 2 && saved.every((leg) => leg.inst && leg.side)) return saved;
    } catch {
      // ignore a corrupt saved pair
    }
    return DEFAULT_PAIR;
  });
  const savePair = (next: Pair) => {
    setPair(next);
    localStorage.setItem("bolter.hedge.pair", JSON.stringify(next));
  };
  const setLeg = (index: 0 | 1, change: Partial<LegChoice>) => {
    const next: Pair = [{ ...pair[0] }, { ...pair[1] }];
    next[index] = { ...next[index], ...change };
    savePair(next);
  };

  // Default each leg's account to the first account on its venue.
  useEffect(() => {
    let changed = false;
    const next: Pair = [{ ...pair[0] }, { ...pair[1] }];
    for (const index of [0, 1] as const) {
      const venue = next[index].inst.split(":")[0];
      const valid = accounts.some((account) => account.id === next[index].accountId && account.venue === venue);
      if (!valid) {
        const fallback = accounts.find((account) => account.venue === venue && account.active)?.id ?? "";
        if (fallback !== next[index].accountId) {
          next[index].accountId = fallback;
          changed = true;
        }
      }
    }
    if (changed) savePair(next);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accounts, pair[0].inst, pair[1].inst]);

  const live0 = useAccountStream(pair[0].accountId);
  const live1 = useAccountStream(pair[1].accountId === pair[0].accountId ? "" : pair[1].accountId);
  const lives = useMemo(() => [live0, pair[1].accountId === pair[0].accountId ? live0 : live1] as const, [live0, live1, pair]);
  const { groups, state } = useHedgeGroups();

  return (
    <main className="grid min-h-0 flex-1 gap-[3px] px-[3px] max-[1000px]:flex max-[1000px]:flex-col max-[1000px]:overflow-y-auto min-[1001px]:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_340px] min-[1001px]:grid-rows-[auto_minmax(300px,1fr)_minmax(240px,40%)]">
      <div className="overflow-hidden rounded-md min-[1001px]:col-span-2">
        <PairBar pair={pair} onSwap={() => savePair([{ ...pair[1] }, { ...pair[0] }])} onFlip={() => savePair([{ ...pair[0], side: pair[1].side }, { ...pair[1], side: pair[0].side }])} />
      </div>
      {([0, 1] as const).map((index) => (
        <div key={index} className={cx("flex min-h-[300px] overflow-hidden rounded-md", index === 0 ? "min-[1001px]:col-start-1" : "min-[1001px]:col-start-2", "min-[1001px]:row-start-2")}>
          <LegPanel index={index} leg={pair[index]} live={lives[index]} onChange={(change) => setLeg(index, change)} />
        </div>
      ))}
      <aside className="overflow-y-auto rounded-md min-[1001px]:col-start-3 min-[1001px]:row-span-3 min-[1001px]:row-start-1">
        <HedgeTicket pair={pair} lives={lives} />
      </aside>
      <div className="flex min-h-[240px] overflow-hidden rounded-md min-[1001px]:col-span-2 min-[1001px]:row-start-3">
        <GroupsPanel groups={groups} state={state} />
      </div>
    </main>
  );
}
