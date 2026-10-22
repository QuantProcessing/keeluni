import type { Margin } from "@/api/types";
import { usd, venueLabel } from "@/lib/format";
import { accountName, marginLevel, type MarginLevel } from "@/lib/routes";
import { useTradingAccount } from "@/state/workspace";
import { Row, cx } from "@/ui/primitives";
import { useT } from "@/i18n";

/**
 * Below the ticket: what the account adds to the rest of the page. A native
 * account adds nothing (equity is in the top bar, available in the ticket,
 * PnL in the positions, the stream in the account chip's dot). A broker's
 * margin pool adds its risk: the rates that decide liquidation and how the
 * pool is spread across venues.
 */
export function AccountSummary() {
  const t = useT();
  const { account, live } = useTradingAccount();
  if (!account || account.kind !== "broker") return null;
  return (
    <div className="flex-1 bg-surface p-3">
      <div className="mb-2 flex items-baseline justify-between">
        <span className="text-md">{t("pool.title")}</span>
        <span className="text-muted">{accountName(account)}</span>
      </div>
      <MarginPool margin={live.margin} risk />
    </div>
  );
}

const WARNING = { caution: "pool.caution", restricted: "pool.restricted", liquidation: "pool.liquidation" } as const satisfies Record<Exclude<MarginLevel, "ok">, string>;

/** One margin pool backing positions on every venue the broker reaches. */
export function MarginPool({ margin, positionsPnl, risk }: { margin: Margin | undefined; positionsPnl?: number; risk?: boolean }) {
  const t = useT();
  if (!margin) return <div className="py-1 text-muted">{t("pool.loading")}</div>;
  const level = marginLevel(margin.initialMarginRate, margin.maintenanceMarginRate);
  // The positions' own PnL when known: it is what the positions table sums to.
  const pnl = positionsPnl ?? Number(margin.unrealizedPnl);
  const shares = (margin.byVenue ?? []).filter((share) => Number(share.equity) !== 0 || Number(share.initialMargin) !== 0);
  return (
    <>
      {/* risk: the trade page shows balance, available and PnL elsewhere. */}
      {!risk && (
        <>
          <Row label={t("pool.balance")} hint={t("pool.balanceHint")}>{usd(margin.equity)}</Row>
          <Row label={t("pool.available")} hint={t("pool.availableHint")}>{usd(margin.available)}</Row>
          <Row label={t("summary.unrealized")}><span className={pnl >= 0 ? "text-up" : "text-down"}>{usd(pnl)}</span></Row>
        </>
      )}
      {Number(margin.initialMargin) > 0 && <Row label={t("pool.initial")}>{usd(margin.initialMargin)}</Row>}
      <Row label={t("pool.initialRate")} hint={t("pool.initialRateHint")}>
        <Rate value={margin.initialMarginRate} warn={level === "caution" || level === "restricted"} />
      </Row>
      <Row label={t("pool.maintenanceRate")} hint={t("pool.maintenanceRateHint")}>
        <Rate value={margin.maintenanceMarginRate} warn={level === "liquidation"} />
      </Row>
      {level !== "ok" && (
        <div role="alert" className="my-2 rounded-lg border border-down/60 bg-warn-bg px-3 py-2 text-down">{t(WARNING[level])}</div>
      )}
      {shares.length > 0 && (
        <div className="mt-2 border-t border-line pt-2">
          <div className="grid grid-cols-[1fr_auto_auto] gap-x-3 py-1 text-muted">
            <span>{t("pool.venue")}</span><span className="text-right">{t("pool.equity")}</span><span className="text-right">{t("pool.inUse")}</span>
          </div>
          {shares.map((share) => (
            <div key={share.venue + share.asset} className="grid grid-cols-[1fr_auto_auto] gap-x-3 py-0.5 text-text-2">
              <span>{venueLabel(share.venue)} <span className="text-muted">{share.asset}</span></span>
              <span className="text-right">{usd(share.equity)}</span>
              <span className="text-right">{share.initialMargin ? usd(share.initialMargin) : "—"}</span>
            </div>
          ))}
        </div>
      )}
    </>
  );
}

function Rate({ value, warn }: { value: string | undefined; warn: boolean }) {
  const n = Number(value);
  if (!value || !Number.isFinite(n) || n <= 0) return <span className="text-muted">—</span>;
  return <span className={cx(warn && "text-down")}>{(n * 100).toFixed(1)}%</span>;
}
