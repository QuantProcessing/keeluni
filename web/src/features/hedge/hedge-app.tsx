import { useEffect, useMemo, useState } from "react";

import { useHedgeGroups, useMatching } from "@/api/hedge";
import { canTrade } from "@/lib/routes";
import { useFunding, useInstruments } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { useT } from "@/i18n";
import { GroupsPanel } from "./groups";
import { DEFAULT_PAIR, flipPair, netFundingApr, normalizePair, type Opportunity, type Pair } from "./model";
import { OpportunityList } from "./opportunities";
import { PairBar } from "./pair-bar";
import { SpreadChart } from "./spread-chart";
import { SpreadLadder } from "./spread-ladder";
import { HedgeTicket } from "./ticket";

function savedPair(): Pair {
  try {
    const saved = JSON.parse(localStorage.getItem("keeluni.hedge.pair") ?? "null") as Pair | null;
    if (saved?.length === 2 && saved.every((leg) => leg.inst && leg.side)) return normalizePair(saved);
  } catch {
    // ignore a corrupt saved pair
  }
  return DEFAULT_PAIR;
}

/**
 * The hedge workspace, built for funding carry: find a pair, judge it, open
 * it, manage it — on one page, in that order.
 *   pair bar      both markets (one long, one short) and the numbers that
 *                 decide whether to carry them
 *   chart         the pair over time: spread, or net funding
 *   spread depth  the pair as one market: entry and exit spread at each
 *                 amount, the round trip, days for funding to repay it
 *                 (the two venue books are one click away)
 *   bottom        Opportunities (every underlying's best funding pair; a
 *                 row loads it here) · Hedges · History
 * with the ticket on the right.
 */
export function HedgeApp() {
  const { accounts, routes } = useWorkspace();
  const { data: instruments } = useInstruments();
  const [pair, setPair] = useState<Pair>(savedPair);
  const spot = useMemo(() => pair.map((leg) => instruments?.find((item) => item.id === leg.inst)?.product === "spot") as [boolean, boolean], [instruments, pair]);
  const savePair = (next: Pair) => {
    const normal = normalizePair(next, spot);
    setPair(normal);
    localStorage.setItem("keeluni.hedge.pair", JSON.stringify(normal));
  };
  const setInstrument = (index: 0 | 1, inst: string) => {
    const next: Pair = [{ ...pair[0] }, { ...pair[1] }];
    next[index] = { ...next[index], inst };
    const nextSpot: [boolean, boolean] = [...spot];
    nextSpot[index] = instruments?.find((item) => item.id === inst)?.product === "spot";
    const normal = normalizePair(next, nextSpot);
    setPair(normal);
    localStorage.setItem("keeluni.hedge.pair", JSON.stringify(normal));
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

  const t = useT();
  const matching = useMatching(pair[0].inst, pair[1].inst);
  const { groups, state } = useHedgeGroups();
  const [amount, setAmount] = useState<{ value: number; at: number } | undefined>(undefined);
  const perp = (index: 0 | 1) => instruments?.find((item) => item.id === pair[index].inst)?.product === "perp";
  const funding0 = useFunding(pair[0].inst, perp(0));
  const funding1 = useFunding(pair[1].inst, perp(1));
  const netApr = netFundingApr([
    { side: pair[0].side, apr: funding0?.apr !== undefined ? Number(funding0.apr) : undefined, spot: !perp(0) },
    { side: pair[1].side, apr: funding1?.apr !== undefined ? Number(funding1.apr) : undefined, spot: !perp(1) },
  ]);
  // An opportunity keeps the leg accounts already chosen for the same market.
  const pick = (item: Opportunity) => {
    const keep = (inst: string) => pair.find((leg) => leg.inst === inst)?.accountId ?? "";
    savePair([{ inst: item.long.inst, accountId: keep(item.long.inst), side: "buy" }, { inst: item.short.inst, accountId: keep(item.short.inst), side: "sell" }]);
  };

  return (
    <main className="grid min-h-0 flex-1 gap-[3px] px-[3px] max-[1100px]:flex max-[1100px]:flex-col max-[1100px]:overflow-y-auto min-[1101px]:grid-cols-[minmax(0,1fr)_minmax(320px,400px)_340px] min-[1101px]:grid-rows-[auto_minmax(300px,1fr)_minmax(240px,40%)]">
      <div className="overflow-hidden rounded-md min-[1101px]:col-span-2">
        <PairBar pair={pair} matching={matching} onInstrument={setInstrument} onFlip={() => flipped && savePair(flipped)} canFlip={!!flipped} />
      </div>
      <div className="flex min-h-[300px] overflow-hidden rounded-md min-[1101px]:col-start-1 min-[1101px]:row-start-2">
        <SpreadChart pair={pair} matching={matching} />
      </div>
      <div className="flex min-h-[300px] overflow-hidden rounded-md min-[1101px]:col-start-2 min-[1101px]:row-start-2">
        <SpreadLadder pair={pair} matching={matching} netApr={netApr} onSize={(value) => setAmount({ value, at: Date.now() })} />
      </div>
      <aside className="overflow-y-auto rounded-md bg-surface min-[1101px]:col-start-3 min-[1101px]:row-span-3 min-[1101px]:row-start-1">
        <HedgeTicket pair={pair} matching={matching} onAccount={setAccount} amount={amount} netApr={netApr} />
      </aside>
      <div className="flex min-h-[240px] overflow-hidden rounded-md min-[1101px]:col-span-2 min-[1101px]:row-start-3">
        <GroupsPanel groups={groups} state={state}
          lead={{ value: "opportunities", label: t("hedge.tab.opportunities"), render: () => <OpportunityList current={[pair[0].inst, pair[1].inst]} onPick={pick} /> }} />
      </div>
    </main>
  );
}
