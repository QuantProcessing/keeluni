import { useMemo, useState } from "react";

import { compact, num } from "@/lib/format";
import { useBook, useInstrument } from "@/state/hooks";
import { cx } from "@/ui/primitives";
import { useT } from "@/i18n";
import { bpsText, breakevenDays, spreadLadder, type Pair } from "./model";

const SIZES = [1_000, 10_000, 50_000, 100_000, 500_000];

/**
 * The pair's synthetic book, the screen a carry trader reads before sizing:
 * for each amount per leg, the spread a market entry would trade at (buy the
 * long leg's asks, sell the short leg's bids) and the spread a market exit
 * would trade at, in bps of the long leg. The round trip — what crossing both
 * books twice costs — is what funding has to earn back, and the days column
 * says how long that takes at the current net rate.
 *
 * The two venue books stay one click away for a trader who wants the levels.
 */
export function SpreadLadder({ pair, matching, netApr, onSize }: {
  pair: Pair;
  matching: { match: "base" | "notional"; ratio: number } | undefined;
  /** Net funding a year (fraction), for the payback column. */
  netApr: number | undefined;
  /** Fills the ticket's amount. */
  onSize: (notional: number) => void;
}) {
  const t = useT();
  const [showBooks, setShowBooks] = useState(false);
  const long = pair[0].side === "buy" ? 0 : 1;
  const short = 1 - long;
  const longInst = useInstrument(pair[long]!.inst);
  const shortInst = useInstrument(pair[short]!.inst);
  const longBook = useBook(pair[long]!.inst);
  const shortBook = useBook(pair[short]!.inst);
  // The ratio converts leg 2's price to leg 1's units; flip it when leg 2 is the long one.
  const ratio = matching?.match === "base" ? (long === 0 ? matching.ratio : 1 / matching.ratio) : 1;
  const rows = useMemo(() => spreadLadder(longBook, shortBook, SIZES, ratio), [longBook, shortBook, ratio]);
  const quote = longInst?.quote ?? "USD";

  if (matching?.match === "notional") {
    return <div className="flex flex-1 items-center justify-center bg-surface px-6 text-center text-muted">{t("ladder.notionalOnly")}</div>;
  }
  const th = "h-7 px-2 text-right font-normal text-muted whitespace-nowrap first:pl-3 first:text-left last:pr-3";
  const td = "h-8 px-2 text-right tabular-nums whitespace-nowrap first:pl-3 first:text-left last:pr-3";
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-line px-3">
        <span className="dotted text-text" title={t("ladder.hint")}>{t("ladder.title")}</span>
        <span className="truncate text-muted">{t("ladder.legs", { long: longInst?.symbol ?? "", short: shortInst?.symbol ?? "" })}</span>
        <button onClick={() => setShowBooks((value) => !value)} className="ml-auto shrink-0 text-muted hover:text-text">
          {showBooks ? t("ladder.hideBooks") : t("ladder.showBooks")}
        </button>
      </div>
      {showBooks ? (
        <div className="grid min-h-0 flex-1 grid-cols-2 divide-x divide-line">
          <MiniBook inst={pair[long]!.inst} label={t("common.long")} />
          <MiniBook inst={pair[short]!.inst} label={t("common.short")} />
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <table className="w-full border-collapse">
            <thead>
              <tr>
                <th className={th}>{t("ladder.amount", { quote })}</th>
                <th className={cx(th, "dotted")} title={t("ladder.openHint")}>{t("ladder.open")}</th>
                <th className={cx(th, "dotted")} title={t("ladder.closeHint")}>{t("ladder.close")}</th>
                <th className={cx(th, "dotted")} title={t("ladder.roundTripHint")}>{t("ladder.roundTrip")}</th>
                <th className={cx(th, "dotted")} title={t("ladder.paybackHint")}>{t("ladder.payback")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                // Opening earns the spread when the short leg sells above the
                // long leg's buy; closing gives it back. The round trip is
                // what both crossings cost together.
                const cost = row.open !== undefined && row.close !== undefined ? row.close - row.open : undefined;
                const days = cost === undefined ? undefined : breakevenDays(Math.max(0, cost), netApr);
                return (
                  <tr key={row.notional} onClick={() => onSize(row.notional)} title={t("ladder.useAmount")} className="cursor-pointer hover:bg-raised">
                    <td className={td}>{compact(row.notional)}</td>
                    <td className={td}>{bpsText(row.open, 1).replace(" bps", "")}</td>
                    <td className={td}>{bpsText(row.close, 1).replace(" bps", "")}</td>
                    <td className={td}>{cost === undefined ? "—" : Math.max(0, cost).toFixed(1)}</td>
                    <td className={cx(td, !row.full && "text-muted")}>
                      {!row.full ? t("ladder.thin") : days === undefined ? "—" : days < 1 ? t("hticket.lessThanDay") : t("hticket.days", { n: days.toFixed(1) })}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <p className="px-3 py-2 text-xs text-muted">{t("ladder.note")}</p>
        </div>
      )}
    </div>
  );
}

/** One leg's book, a few levels a side, for the trader who wants them. */
function MiniBook({ inst, label }: { inst: string; label: string }) {
  const t = useT();
  const instrument = useInstrument(inst);
  const book = useBook(inst);
  const rows = 8;
  const asks = (book?.asks ?? []).slice(0, rows);
  const bids = (book?.bids ?? []).slice(0, rows);
  const line = (level: [string, string], ask: boolean) => (
    <div key={(ask ? "a" : "b") + level[0]} className="grid h-5 grid-cols-2 px-3 tabular-nums">
      <span className={ask ? "text-down" : "text-up"}>{num(level[0])}</span>
      <span className="text-right text-text-2">{num(level[1])}</span>
    </div>
  );
  const blank = (count: number) => Array.from({ length: Math.max(0, count) }, (_, index) => <div key={index} className="h-5" />);
  return (
    <div className="flex min-h-0 flex-col overflow-hidden">
      <div className="flex h-7 items-center justify-between px-3 text-muted">
        <span>{label} · {instrument?.symbol}</span>
        <span>{instrument?.base}</span>
      </div>
      {!book ? <div className="p-3 text-muted">{t("book.loading")}</div> : (
        <div className="flex flex-col">
          {blank(rows - asks.length)}
          <div className="flex flex-col-reverse">{asks.map((level) => line(level, true))}</div>
          <div className="my-0.5 h-px bg-line" />
          {bids.map((level) => line(level, false))}
          {blank(rows - bids.length)}
        </div>
      )}
    </div>
  );
}
