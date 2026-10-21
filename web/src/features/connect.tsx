import * as Popover from "@radix-ui/react-popover";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { ApiError, api } from "@/api/client";
import type { Account, VenueInfo } from "@/api/types";
import { usd } from "@/lib/format";
import { accountEquity, accountName } from "@/lib/routes";
import { useWorkspace } from "@/state/workspace";
import { Dialog, PrimaryButton, cx } from "@/ui/primitives";
import { useT, type T } from "@/i18n";
import { venueLabel } from "./market-header";

/** Where to create a key for each venue: connect.hint.<venue> in the dictionaries. */
function hint(t: T, venue: string): string | undefined {
  const key = `connect.hint.${venue}` as Parameters<T>[0];
  const text = t(key);
  return text === key ? undefined : text;
}

/** "Perp USDT · Spot" for a native venue; the venues a broker reaches. */
function marketsSummary(t: T, item: VenueInfo): string {
  if (item.kind === "broker") {
    const venues = [...new Set(item.markets.map((market) => market.split(":")[0] ?? ""))];
    return venues.map(venueLabel).join(" · ");
  }
  const name = (m: string) => t(m.includes(":perp") ? "common.perp" : m.includes(":option") ? "common.option" : "common.spot");
  return item.markets.map((m) => name(m) + (m.split(":")[2] ? " " + m.split(":")[2] : "")).join(" · ");
}

/** The only place credentials are entered. Secrets live only in this form's state until submit. */
export function ConnectDialog() {
  const t = useT();
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
      title={venue ? t("connect.titleVenue", { venue: venueLabel(venue.venue) }) : t("connect.title")}
      description={venue ? hint(t, venue.venue) : t("connect.choose")}
    >
      {!venue ? (
        <div className="grid grid-cols-2 gap-2">
          {(venues.data ?? []).map((item) => (
            <button
              key={item.venue}
              onClick={() => setVenue(item)}
              className={cx("flex min-h-12 flex-col items-start justify-center rounded-lg bg-raised px-3 py-2 text-left hover:bg-line-strong/40", item.kind === "broker" && "col-span-2")}
            >
              <span className="text-md">{venueLabel(item.venue)}{item.kind === "broker" && <span className="ml-2 text-xs text-accent">{t("common.crossExchange")}</span>}</span>
              <span className="text-muted">{marketsSummary(t, item)}</span>
            </button>
          ))}
          {venues.isLoading && <div className="col-span-2 py-6 text-center text-muted">{t("common.loading")}</div>}
          {venues.error && <div className="col-span-2 py-6 text-center text-down">{(venues.error as Error).message}</div>}
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-2.5" autoComplete="off">
          {venue.fields.map((field) => (
            <label key={field.key} className="flex flex-col gap-1">
              <span className="text-muted">{field.label}{field.required ? "" : t("connect.optionalSuffix")}</span>
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
            <span className="text-muted">{t("connect.labelOptional")}</span>
            <input value={label} maxLength={64} onChange={(e) => setLabel(e.target.value)} placeholder={venueLabel(venue.venue)} className="h-9 rounded-lg border border-line bg-transparent px-3 outline-none placeholder:text-faint focus:border-line-strong" />
          </label>
          <PrimaryButton type="submit" disabled={busy} className="mt-3">
            {busy ? t("connect.verifying") : error?.network ? t("common.retry") : t("connect.submit")}
          </PrimaryButton>
          {error && <div className={cx("text-center", error.network ? "text-text-2" : "text-down")}>{error.text}</div>}
          <button type="button" onClick={() => { setVenue(null); setValues({}); setError(null); }} className="text-muted hover:text-text">{t("connect.back")}</button>
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
  const t = useT();
  const { accounts, live, openConnect, setAccountFilter } = useWorkspace();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  if (!accounts.length) {
    return <button onClick={() => openConnect()} className="h-8 rounded-lg bg-accent px-5 text-on-accent hover:bg-accent-hover">{t("accounts.connect")}</button>;
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
        <span>{accounts.length === 1 ? accountName(accounts[0]!) : t("accounts.count", { n: accounts.length })}</span>
        <span className="text-muted">{known.length ? usd(total) : ""}</span>
        <span className="text-muted">▾</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} className="z-40 w-[360px] rounded-[10px] border border-line bg-overlay py-1">
          <div className="grid grid-cols-[1fr_84px_84px_20px] gap-x-2 px-3 py-1.5 text-muted">
            <span>{t("common.account")}</span><span className="text-right">{t("accounts.equity")}</span><span className="text-right">{t("accounts.availableHead")}</span><span />
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
              <span className="text-muted">{t("common.total")}</span>
              <span className="text-right">{known.length ? usd(total) : "—"}</span>
              <span className="text-right">{known.length ? usd(known.reduce((sum, value) => sum + value!.available, 0)) : "—"}</span>
              <span />
            </div>
          )}
          <button className="w-full border-t border-line px-3 py-2 text-left text-accent hover:bg-raised" onClick={() => { setOpen(false); openConnect(); }}>
            {t("accounts.connectExchange")}
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
  const t = useT();
  if (confirming) {
    return (
      <div className="flex items-center gap-2 bg-raised px-3 py-2">
        <span className="flex-1 text-text-2">{t("accounts.removeConfirm", { name: accountName(account) })}</span>
        <button className="text-down" onClick={onConfirm}>{t("accounts.remove")}</button>
        <button className="text-muted" onClick={onKeep}>{t("accounts.keep")}</button>
      </div>
    );
  }
  return (
    <div className="group grid grid-cols-[1fr_84px_84px_20px] items-center gap-x-2 px-3 py-1.5 hover:bg-raised">
      <button className="flex min-w-0 flex-col items-start text-left" onClick={onShow} title={t("accounts.showOnly")}>
        <span className="flex max-w-full items-center gap-1.5">
          <span className={cx("size-1.5 shrink-0 rounded-full", connected === undefined ? "bg-muted" : connected ? "bg-up" : "bg-down")} />
          <span className="truncate">{accountName(account)}</span>
        </span>
        <span className="pl-3 text-muted">
          {account.kind === "broker" ? t("common.crossExchange") : t("common.native")}
          {!account.active ? ` · ${t("accounts.inactive")}` : connected === false ? ` · ${t("accounts.reconnecting")}` : ""}
        </span>
      </button>
      <span className="text-right">{equity ? usd(equity.total) : "—"}</span>
      <span className="text-right text-text-2">{equity ? usd(equity.available) : "—"}</span>
      <button className="text-muted opacity-0 group-hover:opacity-100 hover:text-down focus-visible:opacity-100" aria-label={t("accounts.removeNamed", { name: accountName(account) })} onClick={onRemove}>✕</button>
    </div>
  );
}
