import { useMemo } from "react";

import type { Margin } from "@/api/types";
import { usd, venueLabel } from "@/lib/format";
import { accountName, marginLevel, type MarginLevel } from "@/lib/routes";
import { useTradingAccount } from "@/state/workspace";
import { Row, cx } from "@/ui/primitives";
import { useT } from "@/i18n";

const STABLES = new Set(["USDT", "USDC", "USD", "FDUSD", "USDE", "USDT0"]);

/** The trading account of the selected market: its funds and stream. */
export function AccountSummary() {
  const t = useT();
  const { account, live } = useTradingAccount();
  const summary = useMemo(() => {
    const balances = live.balances ?? [];
    const positions = live.positions ?? [];
    const stable = balances.filter((item) => STABLES.has(item.asset.toUpperCase()));
    const total = stable.reduce((sum, item) => sum + Number(item.total), 0);
    const available = stable.reduce((sum, item) => sum + Number(item.available), 0);
    const pnl = positions.reduce((sum, item) => sum + Number(item.unrealizedPnl), 0);
    const margin = positions.reduce((sum, item) => sum + (Number(item.margin) || 0), 0);
    return { total, available, pnl, margin };
  }, [live.balances, live.positions]);

  if (!account) return null;
  const stream = (
    <Row label={t("summary.stream")}>
      <span className={live.status?.connected ? "text-up" : "text-down"}>{live.status ? (live.status.connected ? t("summary.live") : t("status.reconnecting")) : t("status.connecting")}</span>
    </Row>
  );
  if (account.kind === "broker") {
    return (
      <div className="flex-1 bg-surface p-3">
        <div className="mb-2 text-md">{accountName(account)}</div>
        <MarginPool margin={live.margin} />
        {stream}
      </div>
    );
  }
  return (
    <div className="flex-1 bg-surface p-3">
      <div className="mb-2 text-md">{accountName(account)}</div>
      <Row label={t("summary.stable")} hint={t("summary.stableHint")}>{usd(summary.total)}</Row>
      <Row label={t("summary.available")}>{usd(summary.available)}</Row>
      <Row label={t("summary.unrealized")}><span className={summary.pnl >= 0 ? "text-up" : "text-down"}>{usd(summary.pnl)}</span></Row>
      {summary.margin > 0 && <Row label={t("summary.positionMargin")}>{usd(summary.margin)}</Row>}
      {stream}
    </div>
  );
}

const WARNING = { caution: "pool.caution", restricted: "pool.restricted", liquidation: "pool.liquidation" } as const satisfies Record<Exclude<MarginLevel, "ok">, string>;

/** One margin pool backing positions on every venue the broker reaches. */
export function MarginPool({ margin }: { margin: Margin | undefined }) {
  const t = useT();
  if (!margin) return <div className="py-1 text-muted">{t("pool.loading")}</div>;
  const level = marginLevel(margin.initialMarginRate, margin.maintenanceMarginRate);
  const pnl = Number(margin.unrealizedPnl);
  const shares = (margin.byVenue ?? []).filter((share) => Number(share.equity) !== 0 || Number(share.initialMargin) !== 0);
  return (
    <>
      <Row label={t("pool.balance")} hint={t("pool.balanceHint")}>{usd(margin.equity)}</Row>
      <Row label={t("pool.available")} hint={t("pool.availableHint")}>{usd(margin.available)}</Row>
      <Row label={t("summary.unrealized")}><span className={pnl >= 0 ? "text-up" : "text-down"}>{usd(margin.unrealizedPnl)}</span></Row>
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
