import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import { api } from "@/api/client";
import { compact, countdown, venueLabel } from "@/lib/format";
import { useNow } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { useT } from "@/i18n";
import { aprText, bpsText, opportunities, type Opportunity } from "./model";

const DEPTHS = [0, 1_000_000, 10_000_000, 100_000_000] as const;

/**
 * Where to carry: the funding table read as pairs. Every underlying listed
 * on two venues or more, with the venue paying the most shorted and the one
 * paying the least bought, ranked by the net rate. A market without depth is
 * not an opportunity, so rows are filtered by the thinner leg's open
 * interest; "My venues" keeps the pairs a connected account can trade.
 * Picking a row loads it into the workspace.
 */
export function OpportunityList({ current, onPick }: { current: [string, string]; onPick: (item: Opportunity) => void }) {
  const t = useT();
  const now = useNow(30_000);
  const { accounts } = useWorkspace();
  const table = useQuery({ queryKey: ["fundingTable"], queryFn: ({ signal }) => api.fundingTable(signal), refetchInterval: 30_000, staleTime: 15_000 });
  const [minOi, setMinOi] = useState<number>(() => Number(localStorage.getItem("keeluni.hedge.minOi") ?? 10_000_000));
  const [mine, setMine] = useState<boolean>(() => localStorage.getItem("keeluni.hedge.mine") === "1");
  const [search, setSearch] = useState("");
  const connected = useMemo(() => [...new Set(accounts.filter((item) => item.active).map((item) => item.venue))], [accounts]);
  const list = useMemo(() => {
    const all = opportunities(table.data?.quotes ?? [], { minOi, venues: mine && connected.length ? connected : undefined });
    const term = search.trim().toUpperCase();
    return (term ? all.filter((item) => item.base.toUpperCase().startsWith(term)) : all).slice(0, 200);
  }, [table.data, minOi, mine, connected, search]);
  const th = "sticky top-0 z-10 h-7 bg-surface px-3 text-right font-normal text-muted whitespace-nowrap first:text-left";
  const td = "h-7 px-3 text-right tabular-nums whitespace-nowrap first:text-left";

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-center gap-3 border-b border-line px-3">
        <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("opps.search")}
          className="h-7 w-28 rounded-md border border-line bg-transparent px-2 outline-none placeholder:text-faint focus:border-line-strong" />
        <span className="dotted text-muted" title={t("opps.depthHint")}>{t("opps.depth")}</span>
        <span className="flex rounded-md bg-raised p-0.5">
          {DEPTHS.map((value) => (
            <button key={value} onClick={() => { setMinOi(value); localStorage.setItem("keeluni.hedge.minOi", String(value)); }}
              className={cx("rounded px-2 leading-5", minOi === value ? "bg-line-strong/60 text-text" : "text-muted hover:text-text")}>
              {value === 0 ? t("common.all") : `≥${compact(value)}`}
            </button>
          ))}
        </span>
        {connected.length > 0 && (
          <label className="flex items-center gap-1.5 text-muted" title={t("opps.mineHint")}>
            <input type="checkbox" checked={mine} onChange={(e) => { setMine(e.target.checked); localStorage.setItem("keeluni.hedge.mine", e.target.checked ? "1" : "0"); }} className="accent-[var(--color-accent)]" />
            {t("opps.mine")}
          </label>
        )}
        <span className="ml-auto text-muted">{t("opps.count", { n: list.length })}</span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full min-w-[720px] border-collapse">
          <thead>
            <tr>
              <th className={th}>{t("opps.coin")}</th>
              <th className={cx(th, "!text-left")}>{t("opps.long")}</th>
              <th className={cx(th, "!text-left")}>{t("opps.short")}</th>
              <th className={cx(th, "dotted")} title={t("opps.netHint")}>{t("opps.net")}</th>
              <th className={cx(th, "dotted")} title={t("opps.basisHint")}>{t("opps.basis")}</th>
              <th className={cx(th, "dotted")} title={t("opps.depthColHint")}>{t("opps.depthCol")}</th>
              <th className={cx(th, "dotted")} title={t("opps.nextHint")}>{t("opps.next")}</th>
            </tr>
          </thead>
          <tbody>
            {list.map((item) => {
              const selected = (item.long.inst === current[0] && item.short.inst === current[1]) || (item.long.inst === current[1] && item.short.inst === current[0]);
              const next = Math.min(...[item.long.nextFunding, item.short.nextFunding].filter((value): value is number => !!value));
              return (
                <tr key={item.base} onClick={() => onPick(item)} className={cx("cursor-pointer hover:bg-raised", selected && "bg-accent-soft")}>
                  <td className={cx(td, "text-text")}>{item.base}<span className="ml-1.5 text-xs text-muted">{item.venues}</span></td>
                  <td className={cx(td, "!text-left")}>{venueLabel(item.long.venue)} <span className="text-muted">{aprText(item.long.apr, 1)}</span></td>
                  <td className={cx(td, "!text-left")}>{venueLabel(item.short.venue)} <span className="text-muted">{aprText(item.short.apr, 1)}</span></td>
                  <td className={cx(td, "text-up")}>{aprText(item.net, 1)}</td>
                  <td className={cx(td, "text-text-2")}>{bpsText(item.basisBps, 1)}</td>
                  <td className={cx(td, "text-text-2")}>{item.depth === undefined ? "—" : compact(item.depth)}</td>
                  <td className={cx(td, "text-muted")}>{Number.isFinite(next) ? countdown(next, now) : "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {table.isLoading && <div className="p-3 text-muted">{t("common.loading")}</div>}
        {!table.isLoading && list.length === 0 && <div className="p-3 text-muted">{t("opps.none")}</div>}
        {list.length > 0 && <p className="px-3 py-2 text-xs text-muted">{t("opps.note")}</p>}
      </div>
    </div>
  );
}
