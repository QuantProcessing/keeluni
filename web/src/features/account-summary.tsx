import { useMemo } from "react";

import type { Margin } from "@/api/types";
import { usd, venueLabel } from "@/lib/format";
import { accountName, marginLevel, type MarginLevel } from "@/lib/routes";
import { useTradingAccount } from "@/state/workspace";
import { Row, cx } from "@/ui/primitives";

const STABLES = new Set(["USDT", "USDC", "USD", "FDUSD", "USDE", "USDT0"]);

/** The trading account of the selected market: its funds and stream. */
export function AccountSummary() {
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
    <Row label="Private Stream">
      <span className={live.status?.connected ? "text-up" : "text-down"}>{live.status ? (live.status.connected ? "Live" : "Reconnecting") : "Connecting"}</span>
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
      <Row label="Stablecoin Balance" hint="Sum of USDT/USDC-like balances across markets">{usd(summary.total)}</Row>
      <Row label="Available">{usd(summary.available)}</Row>
      <Row label="Unrealized PNL"><span className={summary.pnl >= 0 ? "text-up" : "text-down"}>{usd(summary.pnl)}</span></Row>
      {summary.margin > 0 && <Row label="Position Margin">{usd(summary.margin)}</Row>}
      {stream}
    </div>
  );
}

const WARNING: Record<Exclude<MarginLevel, "ok">, string> = {
  caution: "Initial margin rate is below 110%. Margin can no longer be transferred out.",
  restricted: "Initial margin rate is below 100%. Open orders are cancelled and only closing is allowed.",
  liquidation: "Maintenance margin rate has reached 100%. The account is being liquidated.",
};

/** One margin pool backing positions on every venue the broker reaches. */
export function MarginPool({ margin }: { margin: Margin | undefined }) {
  if (!margin) return <div className="py-1 text-muted">Loading margin…</div>;
  const level = marginLevel(margin.initialMarginRate, margin.maintenanceMarginRate);
  const pnl = Number(margin.unrealizedPnl);
  const shares = (margin.byVenue ?? []).filter((share) => Number(share.equity) !== 0 || Number(share.initialMargin) !== 0);
  return (
    <>
      <Row label="Margin Balance" hint="Equity of the shared pool across every venue">{usd(margin.equity)}</Row>
      <Row label="Available Margin" hint="Usable for new orders on any venue">{usd(margin.available)}</Row>
      <Row label="Unrealized PNL"><span className={pnl >= 0 ? "text-up" : "text-down"}>{usd(margin.unrealizedPnl)}</span></Row>
      {Number(margin.initialMargin) > 0 && <Row label="Initial Margin">{usd(margin.initialMargin)}</Row>}
      <Row label="Initial Margin Rate" hint="Margin balance over initial margin. Below 100% only closing is allowed">
        <Rate value={margin.initialMarginRate} warn={level === "caution" || level === "restricted"} />
      </Row>
      <Row label="Maintenance Margin Rate" hint="Margin balance over maintenance margin. At 100% the account is liquidated">
        <Rate value={margin.maintenanceMarginRate} warn={level === "liquidation"} />
      </Row>
      {level !== "ok" && (
        <div role="alert" className="my-2 rounded-lg border border-down/60 bg-warn-bg px-3 py-2 text-down">{WARNING[level]}</div>
      )}
      {shares.length > 0 && (
        <div className="mt-2 border-t border-line pt-2">
          <div className="grid grid-cols-[1fr_auto_auto] gap-x-3 py-1 text-muted">
            <span>Venue</span><span className="text-right">Equity</span><span className="text-right">In Use</span>
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
