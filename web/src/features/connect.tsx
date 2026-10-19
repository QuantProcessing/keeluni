import * as Popover from "@radix-ui/react-popover";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { ApiError, api } from "@/api/client";
import type { Account, VenueInfo } from "@/api/types";
import { usd } from "@/lib/format";
import { accountEquity, accountName } from "@/lib/routes";
import { useWorkspace } from "@/state/workspace";
import { Dialog, PrimaryButton, cx } from "@/ui/primitives";
import { venueLabel } from "./market-header";

const HINTS: Record<string, string> = {
  binance: "Create an API key with Futures + Spot trading enabled. Do not enable withdrawals.",
  okx: "Create an API key with Trade permission. Do not enable withdrawals.",
  bybit: "Create an API key with Contract + Spot trade permission. Do not enable withdrawals.",
  bitget: "Create an API key with trade permission. Do not enable withdrawals.",
  gate: "Create an API key with futures and spot trade permission. Do not enable withdrawals.",
  hyperliquid: "Use an API wallet private key (app.hyperliquid.xyz/API). Set Account address to your main wallet.",
  lighter: "Use an API key private key and its key index; account index is shown in your Lighter account.",
  kraken: "Create a Kraken Futures API key with General and Trading access (futures.kraken.com, Settings › API Keys). Paste the private key as the API secret. Do not enable withdrawals.",
  deribit: "Create a Deribit API key with trade:read_write and account:read scopes. The Client ID is the API key and the Client Secret the API secret.",
  crossex: "One Gate API key trades Binance, OKX, Bybit, Gate, Hyperliquid, Lighter, Kraken and Deribit perps from a shared margin pool. Enable CrossEx trade permission, not withdrawals. The account must be in cross-exchange mode with one-way positions.",
};

/** "Perp USDT · Spot" for a native venue; the venues a broker reaches. */
function marketsSummary(item: VenueInfo): string {
  if (item.kind === "broker") {
    const venues = [...new Set(item.markets.map((market) => market.split(":")[0] ?? ""))];
    return venues.map(venueLabel).join(" · ");
  }
  return item.markets.map((m) => (m.includes(":perp") ? "Perp" : "Spot") + (m.split(":")[2] ? " " + m.split(":")[2] : "")).join(" · ");
}

/** The only place credentials are entered. Secrets live only in this form's state until submit. */
export function ConnectDialog() {
  const { connectOpen, setConnectOpen, connectVenue, accountCreated } = useWorkspace();
  const queryClient = useQueryClient();
  const venues = useQuery({ queryKey: ["venues"], queryFn: api.venues, staleTime: Infinity });
  const [venue, setVenue] = useState<VenueInfo | null>(null);
  const [label, setLabel] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ text: string; network: boolean } | null>(null);

  useEffect(() => {
    if (!connectOpen) {
      setVenue(null);
      setValues({});
      setLabel("");
      setError(null);
    } else if (connectVenue) {
      // Opened for one venue (from the ticket): go straight to its form.
      setVenue(venues.data?.find((item) => item.venue === connectVenue) ?? null);
    }
  }, [connectOpen, connectVenue, venues.data]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!venue) return;
    setBusy(true);
    setError(null);
    try {
      const account = await api.createAccount(venue.venue, label, values);
      setValues({});
      await queryClient.invalidateQueries({ queryKey: ["accounts"] });
      accountCreated(account);
      setConnectOpen(false);
    } catch (e) {
      // A network failure says nothing about the key: keep the form filled
      // so the user can simply retry.
      const network = e instanceof ApiError && (e.category === "network" || e.category === "venue_error" || e.category === "rate_limited");
      setError({ text: (e as Error).message, network });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={connectOpen}
      onOpenChange={setConnectOpen}
      title={venue ? `Connect ${venueLabel(venue.venue)}` : "Connect Exchange"}
      description={venue ? HINTS[venue.venue] : "Choose the exchange to trade on. Keys are verified against the exchange, then stored encrypted on this machine."}
    >
      {!venue ? (
        <div className="grid grid-cols-2 gap-2">
          {(venues.data ?? []).map((item) => (
            <button
              key={item.venue}
              onClick={() => setVenue(item)}
              className={cx("flex min-h-12 flex-col items-start justify-center rounded-lg bg-raised px-3 py-2 text-left hover:bg-line-strong/40", item.kind === "broker" && "col-span-2")}
            >
              <span className="text-md">{venueLabel(item.venue)}{item.kind === "broker" && <span className="ml-2 text-xs text-accent">Cross-exchange</span>}</span>
              <span className="text-muted">{marketsSummary(item)}</span>
            </button>
          ))}
          {venues.isLoading && <div className="col-span-2 py-6 text-center text-muted">Loading…</div>}
          {venues.error && <div className="col-span-2 py-6 text-center text-down">{(venues.error as Error).message}</div>}
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-2.5" autoComplete="off">
          {venue.fields.map((field) => (
            <label key={field.key} className="flex flex-col gap-1">
              <span className="text-muted">{field.label}{field.required ? "" : " (optional)"}</span>
              <input
                type={field.metadata || field.valueType === "integer" ? "text" : "password"}
                inputMode={field.valueType === "integer" ? "numeric" : undefined}
                autoComplete="off"
                spellCheck={false}
                required={field.required}
                value={values[field.key] ?? ""}
                onChange={(e) => setValues((current) => ({ ...current, [field.key]: e.target.value.trim() }))}
                className="h-9 rounded-lg border border-line bg-transparent px-3 outline-none focus:border-line-strong"
              />
            </label>
          ))}
          <label className="flex flex-col gap-1">
            <span className="text-muted">Label (optional)</span>
            <input value={label} maxLength={64} onChange={(e) => setLabel(e.target.value)} placeholder={venueLabel(venue.venue)} className="h-9 rounded-lg border border-line bg-transparent px-3 outline-none placeholder:text-faint focus:border-line-strong" />
          </label>
          <PrimaryButton type="submit" disabled={busy} className="mt-3">
            {busy ? "Verifying with exchange…" : error?.network ? "Retry" : "Connect"}
          </PrimaryButton>
          {error && <div className={cx("text-center", error.network ? "text-text-2" : "text-down")}>{error.text}</div>}
          <button type="button" onClick={() => { setVenue(null); setValues({}); setError(null); }} className="text-muted hover:text-text">← Choose another exchange</button>
        </form>
      )}
    </Dialog>
  );
}

/**
 * Top-bar summary of every connected account: total equity, and per account
 * its connection, equity and available margin. The order ticket picks the
 * account per market, so this is an overview, not a selector. Accounts are
 * added and removed here.
 */
export function AccountsMenu() {
  const { accounts, live, openConnect, setAccountFilter } = useWorkspace();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  if (!accounts.length) {
    return <button onClick={() => openConnect()} className="h-8 rounded-lg bg-accent px-5 text-on-accent hover:bg-accent-hover">Connect</button>;
  }

  const equities = accounts.map((account) => accountEquity(account, live[account.id]));
  const known = equities.filter((value) => value !== undefined);
  const total = known.reduce((sum, value) => sum + value!.total, 0);
  const down = accounts.filter((account) => live[account.id]?.status && !live[account.id]!.status!.connected).length;

  const remove = async (id: string) => {
    await api.deleteAccount(id);
    setConfirming(null);
    await queryClient.invalidateQueries({ queryKey: ["accounts"] });
  };

  return (
    <Popover.Root open={open} onOpenChange={(value) => { setOpen(value); if (!value) setConfirming(null); }}>
      <Popover.Trigger className={cx("flex h-8 items-center gap-2 rounded-lg border border-line px-3 hover:bg-raised", open && "bg-raised")}>
        <span className={cx("size-1.5 rounded-full", down ? "bg-down" : "bg-up")} />
        <span>{accounts.length === 1 ? accountName(accounts[0]!) : `${accounts.length} Accounts`}</span>
        <span className="text-muted">{known.length ? usd(total) : ""}</span>
        <span className="text-muted">▾</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} className="z-40 w-[360px] rounded-[10px] border border-line bg-overlay py-1">
          <div className="grid grid-cols-[1fr_84px_84px_20px] gap-x-2 px-3 py-1.5 text-muted">
            <span>Account</span><span className="text-right">Equity</span><span className="text-right">Available</span><span />
          </div>
          {accounts.map((account, index) => (
            <AccountLine
              key={account.id}
              account={account}
              equity={equities[index]}
              connected={live[account.id]?.status?.connected}
              confirming={confirming === account.id}
              onShow={() => { setAccountFilter(account.id); setOpen(false); }}
              onRemove={() => setConfirming(account.id)}
              onConfirm={() => remove(account.id)}
              onKeep={() => setConfirming(null)}
            />
          ))}
          {accounts.length > 1 && (
            <div className="grid grid-cols-[1fr_84px_84px_20px] gap-x-2 border-t border-line px-3 py-1.5">
              <span className="text-muted">Total</span>
              <span className="text-right">{known.length ? usd(total) : "—"}</span>
              <span className="text-right">{known.length ? usd(known.reduce((sum, value) => sum + value!.available, 0)) : "—"}</span>
              <span />
            </div>
          )}
          <button className="w-full border-t border-line px-3 py-2 text-left text-accent hover:bg-raised" onClick={() => { setOpen(false); openConnect(); }}>
            + Connect exchange
          </button>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function AccountLine({ account, equity, connected, confirming, onShow, onRemove, onConfirm, onKeep }: {
  account: Account;
  equity: ReturnType<typeof accountEquity>;
  connected: boolean | undefined;
  confirming: boolean;
  onShow: () => void;
  onRemove: () => void;
  onConfirm: () => void;
  onKeep: () => void;
}) {
  if (confirming) {
    return (
      <div className="flex items-center gap-2 bg-raised px-3 py-2">
        <span className="flex-1 text-text-2">Remove {accountName(account)}? Its key is deleted from this machine.</span>
        <button className="text-down" onClick={onConfirm}>Remove</button>
        <button className="text-muted" onClick={onKeep}>Keep</button>
      </div>
    );
  }
  return (
    <div className="group grid grid-cols-[1fr_84px_84px_20px] items-center gap-x-2 px-3 py-1.5 hover:bg-raised">
      <button className="flex min-w-0 flex-col items-start text-left" onClick={onShow} title="Show this account's positions and orders">
        <span className="flex max-w-full items-center gap-1.5">
          <span className={cx("size-1.5 shrink-0 rounded-full", connected === undefined ? "bg-muted" : connected ? "bg-up" : "bg-down")} />
          <span className="truncate">{accountName(account)}</span>
        </span>
        <span className="pl-3 text-muted">
          {account.kind === "broker" ? "Cross-exchange" : "Native"}
          {!account.active ? " · inactive" : connected === false ? " · reconnecting" : ""}
        </span>
      </button>
      <span className="text-right">{equity ? usd(equity.total) : "—"}</span>
      <span className="text-right text-text-2">{equity ? usd(equity.available) : "—"}</span>
      <button className="text-muted opacity-0 group-hover:opacity-100 hover:text-down focus-visible:opacity-100" aria-label={`Remove ${accountName(account)}`} onClick={onRemove}>✕</button>
    </div>
  );
}
