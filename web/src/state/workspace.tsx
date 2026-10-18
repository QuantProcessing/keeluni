import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { api } from "@/api/client";
import type { Account, Fill, Instrument, Order, Route, VenueInfo } from "@/api/types";
import { useAccountStreams, useInstrument, useInstruments, type AccountLive } from "./hooks";
import { useToast } from "@/ui/toast";
import { instrumentName, num, venueLabel } from "@/lib/format";
import { accountName, brokersReaching, chooseAccount, marketOf, tradingAccounts } from "@/lib/routes";

const DEFAULT_INSTRUMENT = "binance:perp:BTC-USDT";

/** Rows of the blotter: every account's, each tagged with its account. */
export interface AccountOrder extends Order { account: string }
export interface AccountFill extends Fill { account: string }

interface Workspace {
  instrumentId: string;
  setInstrumentId: (id: string) => void;
  accounts: Account[];
  /** Live state per account id. */
  live: Record<string, AccountLive>;
  /** Order rules per account id, once loaded. */
  routes: Record<string, Route[] | undefined>;
  venues: VenueInfo[];

  /** Accounts that can trade the selected instrument, native first. */
  candidates: Account[];
  /** The account the order ticket uses for the selected instrument. */
  tradingAccount: Account | undefined;
  /** Chooses the account for the selected instrument's venue (remembered). */
  chooseTradingAccount: (id: string) => void;
  /** Brokers not yet connected that could trade the selected instrument. */
  unconnectedBrokers: string[];

  /** Blotter filter: one account id, or "" for all. */
  accountFilter: string;
  setAccountFilter: (id: string) => void;

  /** Remembers an order this terminal placed, to report a later rejection. */
  trackOrder: (accountId: string, order: Order) => void;
  openOrders: AccountOrder[];
  fills: AccountFill[];
  limitPriceHint: { price: string; at: number } | null;
  hintLimitPrice: (price: string) => void;
  /** Opens the connect dialog, optionally on one venue's form. */
  connectOpen: boolean;
  connectVenue: string | undefined;
  openConnect: (venue?: string) => void;
  setConnectOpen: (open: boolean) => void;
  /** Called by the connect dialog with a newly created account. */
  accountCreated: (account: Account) => void;
}

const WorkspaceContext = createContext<Workspace | null>(null);

function stored(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

function storedMap(key: string): Record<string, string> {
  try {
    const value = JSON.parse(localStorage.getItem(key) ?? "{}");
    return value && typeof value === "object" ? value : {};
  } catch {
    return {};
  }
}

const VENUE_ORDER = ["binance", "okx", "bybit", "hyperliquid", "bitget", "gate", "lighter", "deribit", "kraken"];

/**
 * When the saved instrument is not in the catalog (its venue is unreachable
 * or the market was delisted), show the same base and product elsewhere,
 * else the first BTC perp. The saved choice is kept, so it comes back once
 * its venue does.
 */
export function fallbackInstrument(id: string, instruments: Instrument[]): Instrument | undefined {
  if (instruments.length === 0 || instruments.some((item) => item.id === id)) return undefined;
  const [, product = "perp", symbol = ""] = id.split(":");
  const base = symbol.split("-")[0]?.toUpperCase() ?? "";
  const rank = (item: Instrument) => {
    const venue = VENUE_ORDER.indexOf(item.venue);
    return (venue < 0 ? VENUE_ORDER.length : venue) + (item.quote.toUpperCase() === "USDT" ? 0 : 0.5);
  };
  const pick = (match: (item: Instrument) => boolean) =>
    instruments.filter(match).sort((a, b) => rank(a) - rank(b) || a.symbol.localeCompare(b.symbol))[0];
  return pick((item) => item.product === product && item.base.toUpperCase() === base && !item.symbol.includes(":"))
    ?? pick((item) => item.product === "perp" && item.base.toUpperCase() === "BTC")
    ?? instruments[0];
}

function terminal(status: Order["status"]) {
  return status === "filled" || status === "canceled" || status === "rejected";
}

/** Toast text for an order the venue rejected after accepting it. */
export function rejectionNotice(order: Order): { title: string; detail: string } {
  const verb = order.side === "buy" ? "Buy" : "Sell";
  const title = `${verb} ${num(order.qty)} ${instrumentName(order.inst)} rejected`;
  const detail = order.rejectMessage
    ? order.rejectCode && !order.rejectMessage.includes(order.rejectCode) ? `${order.rejectMessage} (${order.rejectCode})` : order.rejectMessage
    : order.rejectCode ?? "The exchange refused the order after it was accepted.";
  return { title, detail };
}

/** Label for an order the venue's risk engine placed. */
export function originLabel(origin: Order["origin"]): string | undefined {
  switch (origin) {
    case "liquidation": return "Liquidation";
    case "adl": return "Auto-deleverage";
    case "settlement": return "Delisting";
  }
  return undefined;
}

/** The remembered account per instrument venue ("kraken" -> acc_...). */
const CHOICE_KEY = "bolter.tradeAccount";

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const [savedInstrument, setInstrument] = useState(() => stored("bolter.instrument", DEFAULT_INSTRUMENT));
  const { data: instruments } = useInstruments();
  const fallback = useMemo(() => fallbackInstrument(savedInstrument, instruments ?? []), [savedInstrument, instruments]);
  const instrumentId = fallback?.id ?? savedInstrument;
  const instrument = useInstrument(instrumentId);
  const [choices, setChoices] = useState(() => storedMap(CHOICE_KEY));
  const [accountFilter, setFilter] = useState(() => stored("bolter.accountFilter", ""));
  const [limitPriceHint, setHint] = useState<{ price: string; at: number } | null>(null);
  const [connectOpen, setConnectOpenState] = useState(false);
  const [connectVenue, setConnectVenue] = useState<string | undefined>(undefined);

  const accounts = useQuery({ queryKey: ["accounts"], queryFn: api.accounts }).data ?? [];
  const venues = useQuery({ queryKey: ["venues"], queryFn: api.venues, staleTime: Infinity }).data ?? [];
  const ids = useMemo(() => accounts.map((account) => account.id), [accounts]);

  const setInstrumentId = useCallback((id: string) => {
    setInstrument(id);
    localStorage.setItem("bolter.instrument", id);
  }, []);
  const setAccountFilter = useCallback((id: string) => {
    setFilter(id);
    localStorage.setItem("bolter.accountFilter", id);
  }, []);

  // Order rules: per account, loaded once and kept.
  const routeQueries = useQueries({
    queries: accounts.map((account) => ({ queryKey: ["routes", account.id], queryFn: () => api.routes(account.id), staleTime: 5 * 60_000 })),
  });
  const routes = useMemo(() => Object.fromEntries(accounts.map((account, index) => [account.id, routeQueries[index]?.data])) as Record<string, Route[] | undefined>,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [accounts, ...routeQueries.map((query) => query.data)]);

  // Open orders and fills: one REST snapshot per account, then stream events.
  const orderQueries = useQueries({
    queries: accounts.map((account) => ({ queryKey: ["orders", account.id], queryFn: () => api.openOrders(account.id), refetchInterval: 60_000 })),
  });
  const fillQueries = useQueries({
    queries: accounts.map((account) => ({ queryKey: ["fills", account.id], queryFn: () => api.fills(account.id), refetchInterval: 120_000 })),
  });
  const openOrders = useMemo(() => accounts.flatMap((account, index) => (orderQueries[index]?.data ?? []).map((order) => ({ ...order, account: account.id })))
    .sort((a, b) => (b.createdAt ?? b.updatedAt ?? 0) - (a.createdAt ?? a.updatedAt ?? 0)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [accounts, ...orderQueries.map((query) => query.data)]);
  const fills = useMemo(() => accounts.flatMap((account, index) => (fillQueries[index]?.data ?? []).map((fill) => ({ ...fill, account: account.id })))
    .sort((a, b) => b.ts - a.ts),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [accounts, ...fillQueries.map((query) => query.data)]);

  // Orders placed here that are not yet final. An asynchronous venue can
  // accept an order and reject it moments later; that rejection must reach
  // the user even though the ticket already reported "accepted".
  const tracked = useRef(new Map<string, number>());
  const trackOrder = useCallback((accountId: string, order: Order) => {
    if (terminal(order.status)) return;
    tracked.current.set(accountId + "/" + order.orderId, Date.now());
    for (const [id, at] of tracked.current) if (Date.now() - at > 10 * 60_000) tracked.current.delete(id);
  }, []);

  const accountsRef = useRef(accounts);
  accountsRef.current = accounts;
  const nameOf = (accountId: string) => {
    const account = accountsRef.current.find((item) => item.id === accountId);
    return account ? accountName(account) : accountId;
  };

  const onOrder = useCallback((accountId: string, order: Order) => {
    queryClient.setQueryData<Order[]>(["orders", accountId], (current = []) => {
      const rest = current.filter((item) => item.orderId !== order.orderId);
      return terminal(order.status) ? rest : [order, ...rest];
    });
    const key = accountId + "/" + order.orderId;
    const ours = tracked.current.has(key);
    if (terminal(order.status)) tracked.current.delete(key);
    if (order.status === "rejected" && ours) {
      const notice = rejectionNotice(order);
      toast.push({ kind: "error", title: notice.title, detail: `${notice.detail} · ${nameOf(accountId)}` });
    } else if (order.origin && order.status !== "rejected") {
      toast.push({ kind: "error", title: `${originLabel(order.origin)} order on ${instrumentName(order.inst)}`, detail: `${order.side === "buy" ? "Buy" : "Sell"} ${num(order.qty)} placed by the exchange's risk engine · ${nameOf(accountId)}` });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryClient, toast]);

  const onFill = useCallback((accountId: string, fill: Fill) => {
    queryClient.setQueryData<Fill[]>(["fills", accountId], (current = []) =>
      current.some((item) => item.fillId === fill.fillId) ? current : [fill, ...current].slice(0, 200));
    toast.push({
      kind: fill.side === "buy" ? "long" : "short",
      title: `${fill.side === "buy" ? "Bought" : "Sold"} ${num(fill.qty)} ${instrumentName(fill.inst)}`,
      detail: `at ${num(fill.price)} · ${nameOf(accountId)}`,
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryClient, toast]);

  const live = useAccountStreams(ids, onOrder, onFill);

  // The ticket's account follows the instrument: the account last chosen for
  // that venue if it can still trade the market, else the first native one,
  // else a broker.
  const venue = instrument?.venue ?? instrumentId.split(":")[0] ?? "";
  const candidates = useMemo(() => (instrument ? tradingAccounts(accounts, instrument, routes) : []), [accounts, instrument, routes]);
  const tradingAccount = chooseAccount(candidates, choices[venue]);
  const chooseTradingAccount = useCallback((id: string) => {
    setChoices((current) => {
      const next = { ...current, [venue]: id };
      localStorage.setItem(CHOICE_KEY, JSON.stringify(next));
      return next;
    });
  }, [venue]);
  const unconnectedBrokers = useMemo(() => {
    if (!instrument) return [];
    const connected = new Set(accounts.map((account) => account.venue));
    return brokersReaching(venues, instrument).filter((broker) => !connected.has(broker));
  }, [instrument, venues, accounts]);

  useEffect(() => {
    if (fallback) toast.push({ kind: "info", title: `${instrumentName(savedInstrument)} is unavailable`, detail: `${venueLabel(savedInstrument.split(":")[0] ?? "")} cannot be reached from this network. Showing ${fallback.symbol} on ${venueLabel(fallback.venue)}.` });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fallback?.id, savedInstrument]);

  useEffect(() => {
    if (accountFilter && accounts.length && !accounts.some((item) => item.id === accountFilter)) setAccountFilter("");
  }, [accountFilter, accounts, setAccountFilter]);

  const openConnect = useCallback((target?: string) => {
    setConnectVenue(target);
    setConnectOpenState(true);
  }, []);
  const setConnectOpen = useCallback((open: boolean) => {
    setConnectOpenState(open);
    if (!open) setConnectVenue(undefined);
  }, []);

  // A new account for the venue being traded becomes the ticket's account.
  const accountCreated = useCallback((account: Account) => {
    if (instrument && account.markets.includes(marketOf(instrument))) {
      setChoices((current) => {
        const next = { ...current, [instrument.venue]: account.id };
        localStorage.setItem(CHOICE_KEY, JSON.stringify(next));
        return next;
      });
    }
  }, [instrument]);

  const value = useMemo<Workspace>(() => ({
    instrumentId, setInstrumentId, accounts, live, routes, venues,
    candidates, tradingAccount, chooseTradingAccount, unconnectedBrokers,
    accountFilter, setAccountFilter, trackOrder, openOrders, fills,
    limitPriceHint, hintLimitPrice: (price) => setHint({ price, at: Date.now() }),
    connectOpen, connectVenue, openConnect, setConnectOpen, accountCreated,
  }), [instrumentId, setInstrumentId, accounts, live, routes, venues, candidates, tradingAccount, chooseTradingAccount, unconnectedBrokers,
    accountFilter, setAccountFilter, trackOrder, openOrders, fills, limitPriceHint, connectOpen, connectVenue, openConnect, setConnectOpen, accountCreated]);

  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}

export function useWorkspace(): Workspace {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("WorkspaceProvider missing");
  return value;
}

/** The trading account's live state and rules for the selected instrument. */
export function useTradingAccount() {
  const workspace = useWorkspace();
  const account = workspace.tradingAccount;
  return {
    account,
    live: (account && workspace.live[account.id]) || {},
    routes: account ? workspace.routes[account.id] : undefined,
  };
}
