import * as Popover from "@radix-ui/react-popover";
import { useMemo, useState } from "react";

import type { Account, Instrument } from "@/api/types";
import { usd, venueLabel } from "@/lib/format";
import { accountEquity, accountName, brokersReaching, tradingAccounts } from "@/lib/routes";
import { useWorkspace } from "@/state/workspace";
import { PrimaryButton, cx } from "@/ui/primitives";

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
 * The account an order goes to, as one row: its name, stream state and kind;
 * a menu of every account that trades this order book (with what each has
 * available) when there is a choice, plus brokers that could. Nothing is
 * chosen behind the user's back: with no selection the row asks for one.
 */
export function AccountPicker({ instrument, account, candidates, unconnectedBrokers, onChoose, label = "Account", ariaLabel = "Trading account" }: {
  instrument: Instrument | undefined;
  account: Account | undefined;
  candidates: Account[];
  unconnectedBrokers: string[];
  onChoose: (id: string) => void;
  label?: string;
  ariaLabel?: string;
}) {
  const { live, openConnect } = useWorkspace();
  const [open, setOpen] = useState(false);
  const choosable = candidates.length > 1 || unconnectedBrokers.length > 0 || (!account && candidates.length > 0);
  const status = account ? live[account.id]?.status : undefined;
  const row = account ? (
    <>
      <span className={cx("size-1.5 shrink-0 rounded-full", status ? (status.connected ? "bg-up" : "bg-down") : "bg-muted")} />
      <span className="truncate">{accountName(account)}</span>
      {account.kind === "broker" && <span className="shrink-0 text-muted">· Cross-exchange</span>}
    </>
  ) : (
    <span className="truncate text-accent">Choose account</span>
  );
  return (
    <div className="flex h-8 items-center gap-2">
      <span className="shrink-0 text-muted">{label}</span>
      {choosable ? (
        <Popover.Root open={open} onOpenChange={setOpen}>
          <Popover.Trigger
            aria-label={ariaLabel}
            className={cx("ml-auto flex h-8 min-w-0 items-center gap-2 rounded-lg px-2.5 hover:bg-raised", open && "bg-raised")}
          >
            {row}
            <span className="shrink-0 text-muted">{open ? "▴" : "▾"}</span>
          </Popover.Trigger>
          <Popover.Portal>
            <Popover.Content align="end" sideOffset={4} className="z-40 w-[288px] rounded-[10px] border border-line bg-overlay py-1">
              <div className="px-3 pt-1.5 pb-1 text-muted">Trade {instrument?.symbol} with</div>
              {candidates.map((item) => (
                <AccountOption key={item.id} account={item} selected={item.id === account?.id} onChoose={() => { onChoose(item.id); setOpen(false); }} />
              ))}
              {unconnectedBrokers.map((broker) => (
                <button
                  key={broker}
                  onClick={() => { setOpen(false); openConnect(broker); }}
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-accent hover:bg-raised"
                >
                  + Connect {venueLabel(broker)}
                  <span className="ml-auto text-muted">one pool, many venues</span>
                </button>
              ))}
            </Popover.Content>
          </Popover.Portal>
        </Popover.Root>
      ) : (
        <span className="ml-auto flex min-w-0 items-center gap-2 px-2.5">{row}</span>
      )}
    </div>
  );
}

function AccountOption({ account, selected, onChoose }: { account: Account; selected: boolean; onChoose: () => void }) {
  const { live } = useWorkspace();
  const equity = accountEquity(account, live[account.id]);
  return (
    <button onClick={onChoose} className={cx("flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-raised", selected && "text-accent")}>
      <span className="w-3 shrink-0">{selected ? "✓" : ""}</span>
      <span className="flex min-w-0 flex-col">
        <span className="truncate">{accountName(account)}</span>
        <span className="text-muted">{account.kind === "broker" ? "Cross-exchange margin" : "Native"}</span>
      </span>
      <span className="ml-auto shrink-0 text-right">
        <span className="block">{equity ? usd(equity.available) : "—"}</span>
        <span className="block text-muted">available</span>
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
  const { venues, openConnect } = useWorkspace();
  const where = `${venueLabel(instrument.venue)} ${instrument.product === "perp" ? "perps" : "spot"}`;
  const native = venues.some((item) => item.venue === instrument.venue && item.kind === "native");
  if (compact) {
    return (
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-muted">No account trades {where}</span>
        {native && <button onClick={() => openConnect(instrument.venue)} className="shrink-0 text-accent hover:underline">Connect {venueLabel(instrument.venue)}</button>}
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
      <p className="text-text-2">No connected account trades {where}.</p>
      {native && <PrimaryButton onClick={() => openConnect(instrument.venue)}>Connect {venueLabel(instrument.venue)}</PrimaryButton>}
      {unconnectedBrokers.map((broker) => (
        <button key={broker} onClick={() => openConnect(broker)} className="h-10 rounded-lg border border-line text-text hover:bg-raised">
          Connect {venueLabel(broker)}
          <span className="ml-1.5 text-muted">· trades {venueLabel(instrument.venue)} from one pool</span>
        </button>
      ))}
      <p className="text-muted">The chart, book and trades stay live without an account.</p>
    </>
  );
}
