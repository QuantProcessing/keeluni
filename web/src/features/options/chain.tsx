import { useLayoutEffect, useMemo, useRef } from "react";

import type { OptionChain, OptionRow } from "@/api/options";
import { num } from "@/lib/format";
import { cx } from "@/ui/primitives";
import { useT } from "@/i18n";
import type { Side, StrikeRow } from "./model";

export type QuoteMode = "price" | "iv";

/** Held size per instrument (signed, units of the underlying). */
export type Holdings = Record<string, number>;

const iv = (value: number | undefined) => (value ? (value * 100).toFixed(1) : "—");

/**
 * The T-board: calls left, strikes centre, puts right, centred on the
 * forward (a marker line sits between the strikes it falls between).
 * In-the-money halves are shaded. Clicking a half selects that option (its
 * book and payoff show beside the board, the page does not change); the bid
 * and ask cells also choose the side — the bid sells, the ask buys — which
 * in combo mode adds a leg instead.
 */
export function ChainBoard({ chain, rows, forward, mode, usdUnit, holdings, picked, selected, onPick, onSelect }: {
  chain: OptionChain | undefined;
  rows: StrikeRow[];
  forward: number;
  mode: QuoteMode;
  /** Show inverse premiums in USD instead of the coin. */
  usdUnit: boolean;
  holdings: Holdings;
  /** Instruments in the package, by side. */
  picked: Record<string, Side>;
  /** The option shown beside the board. */
  selected: string;
  onPick: (option: OptionRow, side: Side) => void;
  onSelect: (option: OptionRow) => void;
}) {
  const t = useT();
  const scroller = useRef<HTMLDivElement>(null);
  const forwardRow = useRef<HTMLTableRowElement>(null);
  const inverse = !!chain?.inverse;
  const convert = (value: string | undefined) => {
    const n = Number(value);
    if (!(n > 0)) return undefined;
    return inverse && usdUnit ? n * forward : n;
  };
  const priceText = (value: string | undefined) => {
    const n = convert(value);
    if (n === undefined) return "—";
    if (inverse && !usdUnit) return n.toFixed(4);
    return num(n, n >= 100 ? 0 : n >= 1 ? 2 : 4);
  };
  const cell = (option: OptionRow | undefined, field: "bid" | "mark" | "ask") => {
    if (!option) return "";
    if (mode === "iv") return iv(field === "bid" ? option.bidIv : field === "ask" ? option.askIv : option.markIv);
    return priceText(option[field]);
  };
  // Where the forward line goes: before the first strike above it.
  const forwardAt = useMemo(() => rows.findIndex((row) => row.strike >= forward), [rows, forward]);

  // Keep the forward in view when the series or expiry changes.
  const key = `${chain?.series}|${rows[0]?.call?.expiry ?? rows[0]?.put?.expiry}`;
  useLayoutEffect(() => {
    const container = scroller.current;
    const line = forwardRow.current;
    if (container && line) container.scrollTop = line.offsetTop - container.clientHeight / 2;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const th = "bg-surface px-2 py-1.5 font-normal text-muted whitespace-nowrap";
  const td = "px-2 py-0 h-6 text-right tabular-nums whitespace-nowrap";
  const tradeCell = (option: OptionRow | undefined, side: Side, itm: boolean) => {
    const field = side === "sell" ? "bid" : "ask";
    const active = option && picked[option.inst] === side;
    const empty = !option || !(Number(option[field]) > 0);
    return (
      <td className={cx(td, "p-0", itm && "bg-raised/40", option && option.inst === selected && "!bg-accent-soft")}>
        <button
          disabled={empty}
          onClick={() => option && onPick(option, side)}
          title={option ? t(side === "sell" ? "options.sellHint" : "options.buyHint") : undefined}
          className={cx(
            "h-6 w-full px-2 text-right tabular-nums transition-colors disabled:cursor-default",
            side === "sell" ? "text-up hover:bg-long/25" : "text-down hover:bg-short-deep/50",
            active && (side === "sell" ? "bg-long/30" : "bg-short-deep/60"),
            empty && "text-faint",
          )}
        >
          {cell(option, field)}
        </button>
      </td>
    );
  };
  // Any non-trade cell of a half selects that option.
  const info = (option: OptionRow | undefined, itm: boolean, className: string, content: React.ReactNode) => (
    <td onClick={() => option && onSelect(option)} className={cx(td, className, itm && "bg-raised/40", option && "cursor-pointer", option?.inst === selected && "!bg-accent-soft")}>
      {content}
    </td>
  );
  const position = (option: OptionRow | undefined, itm: boolean) => {
    const held = option ? holdings[option.inst] : undefined;
    return info(option, itm, held ? (held > 0 ? "text-up" : "text-down") : "", held ? (held > 0 ? "+" : "") + num(held) : "");
  };

  return (
    <div ref={scroller} className="min-h-0 flex-1 overflow-auto">
      <table className="w-full border-separate border-spacing-0">
        <thead className="sticky top-0 z-10 bg-surface">
          <tr>
            <th colSpan={6} className={cx(th, "border-b border-line text-center text-text-2")}>{t("options.calls")}</th>
            <th className={cx(th, "border-b border-line")} />
            <th colSpan={6} className={cx(th, "border-b border-line text-center text-text-2")}>{t("options.puts")}</th>
          </tr>
          <tr className="text-right">
            <th className={th}>{t("options.pos")}</th>
            <th className={th}>{t("options.oi")}</th>
            <th className={th}>Δ</th>
            <th className={th}>{mode === "iv" ? t("options.bidIv") : t("options.bid")}</th>
            <th className={th}>{mode === "iv" ? t("options.markIv") : t("options.mark")}</th>
            <th className={th}>{mode === "iv" ? t("options.askIv") : t("options.ask")}</th>
            <th className={cx(th, "text-center")}>{t("options.strike")}</th>
            <th className={th}>{mode === "iv" ? t("options.bidIv") : t("options.bid")}</th>
            <th className={th}>{mode === "iv" ? t("options.markIv") : t("options.mark")}</th>
            <th className={th}>{mode === "iv" ? t("options.askIv") : t("options.ask")}</th>
            <th className={th}>Δ</th>
            <th className={th}>{t("options.oi")}</th>
            <th className={th}>{t("options.pos")}</th>
          </tr>
          <tr><th colSpan={13} className="h-px bg-line p-0" /></tr>
        </thead>
        <tbody>
          {rows.map((row, index) => {
            const callItm = row.strike < forward;
            const putItm = row.strike > forward;
            return [
              index === forwardAt && forward > 0 ? (
                <tr key="forward" ref={forwardRow}>
                  <td colSpan={13} className="relative h-4 p-0">
                    <div className="absolute inset-x-0 top-1/2 border-t border-accent/50" />
                    <span className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 rounded bg-accent-soft px-1.5 text-xs leading-4 text-accent">
                      {t("options.forward")} {num(forward, 0)}
                    </span>
                  </td>
                </tr>
              ) : null,
              <tr key={row.strike} className="hover:bg-raised/60">
                {position(row.call, callItm)}
                {info(row.call, callItm, "text-muted", Number(row.call?.oi) > 0 ? num(row.call!.oi, 1) : "")}
                {info(row.call, callItm, "text-text-2", row.call ? row.call.delta.toFixed(2) : "")}
                {tradeCell(row.call, "sell", callItm)}
                {info(row.call, callItm, "text-text-2", cell(row.call, "mark"))}
                {tradeCell(row.call, "buy", callItm)}
                <td className={cx(td, "border-x border-line bg-surface !text-center text-text")}>{num(row.strike)}</td>
                {tradeCell(row.put, "sell", putItm)}
                {info(row.put, putItm, "text-text-2", cell(row.put, "mark"))}
                {tradeCell(row.put, "buy", putItm)}
                {info(row.put, putItm, "text-text-2", row.put ? row.put.delta.toFixed(2) : "")}
                {info(row.put, putItm, "text-muted", Number(row.put?.oi) > 0 ? num(row.put!.oi, 1) : "")}
                {position(row.put, putItm)}
              </tr>,
            ];
          })}
        </tbody>
      </table>
      {rows.length === 0 && <div className="flex h-32 items-center justify-center text-muted">{chain ? t("options.noStrikes") : t("common.loading")}</div>}
    </div>
  );
}
