import { useEffect, useMemo, useState } from "react";

import { useHedgeGroups, useMatching } from "@/api/hedge";
import { canTrade } from "@/lib/routes";
import { useInstruments } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { GroupsPanel } from "./groups";
import { LegPanel } from "./leg-panel";
import { DEFAULT_PAIR, flipPair, normalizePair, type Pair } from "./model";
import { PairBar } from "./pair-bar";
import { SpreadChart } from "./spread-chart";
import { HedgeTicket } from "./ticket";

function savedPair(): Pair {
  try {
    const saved = JSON.parse(localStorage.getItem("bolter.hedge.pair") ?? "null") as Pair | null;
    if (saved?.length === 2 && saved.every((leg) => leg.inst && leg.side)) return normalizePair(saved);
  } catch {
    // ignore a corrupt saved pair
  }
  return DEFAULT_PAIR;
}

/**
 * The hedge workspace, laid out like the trade page around a pair:
 *   pair bar — both markets (one long, one short) and the numbers that decide
 *              whether to trade them
 *   spread   — the pair over time (spread, or net funding), with both legs'
 *              books beside it
 *   hedges   — every hedge as one row, expandable to its legs and history
 * with the ticket (accounts, size, preview, the money behind it) on the right.
 */
export function HedgeApp() {
  const { accounts, routes } = useWorkspace();
  const { data: instruments } = useInstruments();
  const [pair, setPair] = useState<Pair>(savedPair);
  const spot = useMemo(() => pair.map((leg) => instruments?.find((item) => item.id === leg.inst)?.product === "spot") as [boolean, boolean], [instruments, pair]);
  const savePair = (next: Pair) => {
    const normal = normalizePair(next, spot);
    setPair(normal);
    localStorage.setItem("bolter.hedge.pair", JSON.stringify(normal));
  };
  const setInstrument = (index: 0 | 1, inst: string) => {
    const next: Pair = [{ ...pair[0] }, { ...pair[1] }];
    next[index] = { ...next[index], inst };
    const nextSpot: [boolean, boolean] = [...spot];
    nextSpot[index] = instruments?.find((item) => item.id === inst)?.product === "spot";
    const normal = normalizePair(next, nextSpot);
    setPair(normal);
    localStorage.setItem("bolter.hedge.pair", JSON.stringify(normal));
  };
  const setAccount = (index: 0 | 1, accountId: string) => {
    const next: Pair = [{ ...pair[0] }, { ...pair[1] }];
    next[index] = { ...next[index], accountId };
    savePair(next);
  };
  const flipped = flipPair(pair, spot);

  // A leg's account is always the user's choice: there is no default, and a
  // choice that can no longer trade the leg's instrument (another market
  // chosen, the account removed, a broker route gone) is cleared, never
  // swapped for another account.
  useEffect(() => {
    if (!instruments || !accounts.length) return;
    let changed = false;
    const next: Pair = [{ ...pair[0] }, { ...pair[1] }];
    for (const leg of next) {
      if (!leg.accountId) continue;
      const instrument = instruments.find((item) => item.id === leg.inst);
      const account = accounts.find((item) => item.id === leg.accountId);
      if (!instrument) continue;
      if (!account || !canTrade(account, instrument, routes[account.id])) {
        leg.accountId = "";
        changed = true;
      }
    }
    if (changed) savePair(next);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accounts, routes, instruments, pair[0].inst, pair[1].inst, pair[0].accountId, pair[1].accountId]);

  const matching = useMatching(pair[0].inst, pair[1].inst);
  const { groups, state } = useHedgeGroups();

  return (
    <main className="grid min-h-0 flex-1 gap-[3px] px-[3px] max-[1000px]:flex max-[1000px]:flex-col max-[1000px]:overflow-y-auto min-[1001px]:grid-cols-[minmax(0,1fr)_minmax(220px,260px)_minmax(220px,260px)_340px] min-[1001px]:grid-rows-[auto_minmax(320px,1fr)_minmax(220px,36%)]">
      <div className="overflow-hidden rounded-md min-[1001px]:col-span-3">
        <PairBar pair={pair} matching={matching} onInstrument={setInstrument} onFlip={() => flipped && savePair(flipped)} canFlip={!!flipped} />
      </div>
      <div className="flex min-h-[320px] overflow-hidden rounded-md min-[1001px]:col-start-1 min-[1001px]:row-start-2">
        <SpreadChart pair={pair} matching={matching} />
      </div>
      {([0, 1] as const).map((index) => (
        <div key={index} className="flex min-h-[320px] overflow-hidden rounded-md min-[1001px]:row-start-2" style={{ gridColumnStart: index + 2 }}>
          <LegPanel index={index} leg={pair[index]} />
        </div>
      ))}
      <aside className="overflow-y-auto rounded-md bg-surface min-[1001px]:col-start-4 min-[1001px]:row-span-3 min-[1001px]:row-start-1">
        <HedgeTicket pair={pair} matching={matching} onAccount={setAccount} />
      </aside>
      <div className="flex min-h-[220px] overflow-hidden rounded-md min-[1001px]:col-span-3 min-[1001px]:row-start-3">
        <GroupsPanel groups={groups} state={state} />
      </div>
    </main>
  );
}
