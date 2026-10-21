import * as Popover from "@radix-ui/react-popover";
import { useMemo, useState, type ReactNode } from "react";

import type { Account, Instrument } from "@/api/types";
import { usd, venueLabel } from "@/lib/format";
import { accountEquity, accountName, brokersReaching, tradingAccounts } from "@/lib/routes";
import { useWorkspace } from "@/state/workspace";
import { PrimaryButton, cx } from "@/ui/primitives";
import { useT } from "@/i18n";

/** Accounts that can trade instrument, and brokers that could once connected. */
export function useAccountChoices(instrument: Instrument | undefined) {
  const { accounts, routes, venues } = useWorkspace();
  return useMemo(() => {
    if (!instrument) return { candidates: [] as Account[], unconnectedBrokers: [] as string[] };
    const connected = new Set(accounts.map((account) => account.venue));
    return {
      candidates: tradingAccounts(accounts, instrument, routes),
      unconnectedBrokers: brokersReaching(venues, instrument).filter((broker) => !connected.has(broker)),
    };
  }, [accounts, routes, venues, instrument]);
}

/**
 * The menu of accounts that can trade a market, opened from any trigger:
 * one line per account (name, kind, what it has available, ✓ on the current
 * one), then brokers that could once connected.
 */
export function AccountMenu({ instrument, account, candidates, unconnectedBrokers, onChoose, ariaLabel, className, children }: {
  instrument: Instrument | undefined;
  account: Account | undefined;
  candidates: Account[];
  unconnectedBrokers: string[];
  onChoose: (id: string) => void;
  ariaLabel: string;
  className?: string;
  children: (open: boolean) => ReactNode;
}) {
  const t = useT();
  const { openConnect } = useWorkspace();
  const [open, setOpen] = useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger aria-label={ariaLabel} className={cx(className, open && "bg-raised")}>
        {children(open)}
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={4} className="z-40 w-[288px] rounded-[10px] border border-line bg-overlay py-1">
          <div className="px-3 pt-1.5 pb-1 text-muted">{t("accounts.tradeWith", { symbol: instrument?.symbol ?? "" })}</div>
          {candidates.map((item) => (
            <AccountOption key={item.id} account={item} selected={item.id === account?.id} onChoose={() => { onChoose(item.id); setOpen(false); }} />
          ))}
          {unconnectedBrokers.map((broker) => (
            <button
              key={broker}
              onClick={() => { setOpen(false); openConnect(broker); }}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-accent hover:bg-raised"
            >
              {t("accounts.connectBroker", { broker: venueLabel(broker) })}
              <span className="ml-auto text-muted">{t("accounts.onePool")}</span>
            </button>
          ))}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** Stream dot and name of an account. */
export function AccountLabel({ account }: { account: Account }) {
  const t = useT();
  const { live } = useWorkspace();
  const status = live[account.id]?.status;
  return (
    <>
      <span className={cx("size-1.5 shrink-0 rounded-full", status ? (status.connected ? "bg-up" : "bg-down") : "bg-muted")} title={status ? (status.connected ? t("accounts.streamLive") : t("accounts.streamDown")) : undefined} />
      <span className="truncate">{accountName(account)}</span>
    </>
  );
}

export function AccountOption({ account, selected, onChoose }: { account: Account; selected: boolean; onChoose: () => void }) {
  const t = useT();
  const { live } = useWorkspace();
  const equity = accountEquity(account, live[account.id]);
  return (
    <button onClick={onChoose} className={cx("flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-raised", selected && "text-accent")}>
      <span className="w-3 shrink-0">{selected ? "✓" : ""}</span>
      <span className="flex min-w-0 flex-col">
        <span className="truncate">{accountName(account)}</span>
        <span className="text-muted">{account.kind === "broker" ? t("common.crossExchangeMargin") : t("common.native")}</span>
      </span>
      <span className="ml-auto shrink-0 text-right">
        <span className="block">{equity ? usd(equity.available) : "—"}</span>
        <span className="block text-muted">{t("common.available")}</span>
      </span>
    </button>
  );
}

/**
 * No connected account trades this order book. Say so in one sentence and
 * offer the accounts that would: the venue itself, and any broker that
 * reaches it.
 */
export function NoAccount({ instrument, unconnectedBrokers, compact }: { instrument: Instrument; unconnectedBrokers: string[]; compact?: boolean }) {
  const t = useT();
  const { venues, openConnect } = useWorkspace();
  const where = t(instrument.product === "perp" ? "accounts.wherePerps" : "accounts.whereSpot", { venue: venueLabel(instrument.venue) });
  const native = venues.some((item) => item.venue === instrument.venue && item.kind === "native");
  if (compact) {
    return (
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-muted">{t("accounts.noneTradesShort", { where })}</span>
        {native && <button onClick={() => openConnect(instrument.venue)} className="shrink-0 text-accent hover:underline">{t("accounts.connectVenue", { venue: venueLabel(instrument.venue) })}</button>}
        {unconnectedBrokers.map((broker) => (
          <button key={broker} onClick={() => openConnect(broker)} className="shrink-0 text-accent hover:underline">{venueLabel(broker)}</button>
        ))}
      </span>
    );
  }
  return (
    <>
      <div className="flex items-baseline justify-between">
        <span className="text-md">{instrument.symbol}</span>
        <span className="text-muted">{venueLabel(instrument.venue)}</span>
      </div>
      <p className="text-text-2">{t("accounts.noneTrades", { where })}</p>
      {native && <PrimaryButton onClick={() => openConnect(instrument.venue)}>{t("accounts.connectVenue", { venue: venueLabel(instrument.venue) })}</PrimaryButton>}
      {unconnectedBrokers.map((broker) => (
        <button key={broker} onClick={() => openConnect(broker)} className="h-10 rounded-lg border border-line text-text hover:bg-raised">
          {t("accounts.connectVenue", { venue: venueLabel(broker) })}
          <span className="ml-1.5 text-muted">{t("accounts.brokerReaches", { venue: venueLabel(instrument.venue) })}</span>
        </button>
      ))}
      <p className="text-muted">{t("accounts.marketDataLive")}</p>
    </>
  );
}
