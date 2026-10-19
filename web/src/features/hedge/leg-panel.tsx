import { useLayoutEffect, useRef, useState } from "react";

import { countdown, num, price } from "@/lib/format";
import { useBook, useFunding, useInstrument, useNow } from "@/state/hooks";
import { cx } from "@/ui/primitives";
import { aprText, rateText, tone, type LegChoice } from "./model";
import { useT } from "@/i18n";

const ROW = 20;

/** Rows per book side that fit a body `height` px tall around a spread row. */
export function bookRows(height: number): number {
  return Math.max(1, Math.min(20, Math.floor((height - 24) / 2 / ROW)));
}

/**
 * One leg's market: side, funding on one line, and its book. The leg's
 * instrument is chosen in the pair bar and its account in the ticket.
 */
export function LegPanel({ index, leg }: { index: 0 | 1; leg: LegChoice }) {
  const t = useT();
  const instrument = useInstrument(leg.inst);
  const perp = instrument?.product === "perp";
  const book = useBook(leg.inst);
  const funding = useFunding(leg.inst, perp);
  const now = useNow();
  // Deribit accrues funding continuously; the gateway quotes it per hour.
  const continuous = instrument?.venue === "deribit";
  const tick = instrument?.tickSize;
  const body = useRef<HTMLDivElement>(null);
  const [rows, setRows] = useState(8);
  useLayoutEffect(() => {
    const element = body.current;
    if (!element) return;
    const measure = () => setRows(bookRows(element.clientHeight));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const asks = (book?.asks ?? []).slice(0, rows);
  const bids = (book?.bids ?? []).slice(0, rows);
  const bestAsk = Number(asks[0]?.[0]);
  const bestBid = Number(bids[0]?.[0]);
  const spread = bestAsk && bestBid ? bestAsk - bestBid : undefined;
  const maxSize = Math.max(...asks.map((level) => Number(level[1])), ...bids.map((level) => Number(level[1])), 1);

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-line px-3">
        <span className={cx("shrink-0 whitespace-nowrap", leg.side === "buy" ? "text-up" : "text-down")}>{t(leg.side === "buy" ? "common.long" : "common.short")}</span>
        <span className="shrink-0 whitespace-nowrap text-muted">{t("common.leg", { n: index + 1 })}</span>
        {perp ? (
          <span className="ml-auto flex shrink-0 items-center gap-1.5 whitespace-nowrap" title={continuous ? t("leg.fundingContinuousHint") : t("leg.fundingHint")}>
            <span className={tone(funding?.rate)}>{rateText(funding?.rate)}</span>
            {funding?.intervalHours ? <span className="text-muted">/{funding.intervalHours}h</span> : null}
            <span className={tone(funding?.apr)}>{aprText(funding?.apr)}</span>
            <span className="text-muted">{continuous ? t("leg.continuous") : countdown(funding?.nextFunding, now)}</span>
            {funding?.stale && <span className="text-down" title={t("leg.staleHint")}>{t("leg.stale")}</span>}
          </span>
        ) : (
          <span className="ml-auto text-muted">{t("leg.spotNoFunding")}</span>
        )}
      </div>
      <div className="grid h-6 shrink-0 grid-cols-2 items-center px-3 text-muted">
        <span>{t("leg.priceIn", { quote: instrument?.quote ?? "" })}</span>
        <span className="text-right">{t("leg.sizeIn", { base: instrument?.base ?? "" })}</span>
      </div>
      <div ref={body} className="flex min-h-0 flex-1 flex-col justify-center overflow-hidden">
        {!book ? (
          <div className="py-6 text-center text-muted">{t("book.loading")}</div>
        ) : (
          <>
            {/* Fewer levels than fit: blank rows on the outside keep the spread row centred, level with the other leg's. */}
            {blank(rows - asks.length, "a")}
            <div className="flex flex-col-reverse">{asks.map(([p, q]) => <Level key={"a" + p} priceText={price(p, tick)} size={q} width={Number(q) / maxSize} ask />)}</div>
            <div className="flex h-6 items-center justify-center gap-4 bg-raised">
              <span className="text-muted">{t("common.spread")}</span>
              <span>{spread === undefined ? "—" : price(spread, tick)}</span>
              <span className="text-muted">{spread && bestAsk ? ((spread / bestAsk) * 10_000).toFixed(1) + " bps" : ""}</span>
            </div>
            <div>{bids.map(([p, q]) => <Level key={"b" + p} priceText={price(p, tick)} size={q} width={Number(q) / maxSize} />)}</div>
            {blank(rows - bids.length, "b")}
          </>
        )}
      </div>
    </div>
  );
}

const blank = (count: number, side: string) => Array.from({ length: Math.max(0, count) }, (_, index) => <div key={side + index} className="h-5 shrink-0" aria-hidden />);

function Level({ priceText, size, width, ask }: { priceText: string; size: string; width: number; ask?: boolean }) {
  return (
    <div className="relative grid h-5 grid-cols-2 items-center px-3">
      <span className={cx("absolute inset-y-0 right-0", ask ? "bg-down/15" : "bg-long/15")} style={{ width: `${Math.min(1, width) * 100}%` }} />
      <span className={cx("relative", ask ? "text-down" : "text-up")}>{priceText}</span>
      <span className="relative text-right">{num(size)}</span>
    </div>
  );
}
