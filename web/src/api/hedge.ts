// Client of the hedge app (hedger), served by the trader at /api/hedge.
// Wire types mirror hedger/*.go; numbers are decimal strings.

import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { ApiError, api } from "./client";
import type { FundingQuote } from "./types";
import { currentLang } from "@/i18n";
import { hedgerText, type HedgerText } from "@/i18n/hedger";

export type { HedgerText };

export type GroupStatus = "opening" | "open" | "closing" | "closed" | "failed" | "broken";
export type ActionKind = "open" | "close" | "unwind" | "rehedge";
export type ExecMode = "market" | "limit";

export interface HedgeLeg {
  leg: 0 | 1;
  accountId: string;
  /** The account is a broker (Gate CrossEx): its rules and shared pool. */
  broker?: boolean;
  inst: string;
  venue: string;
  product: "spot" | "perp";
  base: string;
  quote: string;
  side: "buy" | "sell";
  stepSize: string;
  tickSize: string;
  minQty: string;
  minNotional: string;
}

export interface HedgeAction {
  id: string;
  groupId: string;
  kind: ActionKind;
  mode: ExecMode;
  makerLeg?: 0 | 1;
  limitPrice: string;
  tif?: string;
  target: [string, string];
  status: "working" | "done" | "canceled" | "failed";
  error?: HedgerText;
  createdAt: string;
  updatedAt: string;
}

export interface LegView extends HedgeLeg {
  held: string;
  avgEntry: string;
  opened: string;
  closed: string;
  avgOpen: string;
  avgClose: string;
  realizedPnl: string;
  fees: string;
  feesOther?: Record<string, string>;
  mark: string;
  bid: string;
  ask: string;
  notional: string;
  unrealizedPnl: string;
  fundingRate: string;
  fundingHours?: number;
  fundingApr: string;
  nextFunding?: number;
  nextFundingCash: string;
  fundingEarned: string;
  fundingStale?: boolean;
  venueSize: string;
  venueSide?: "long" | "short";
  liquidationPrice: string;
  liquidationPct: string;
  leverage?: string;
  accountLive: boolean;
}

export interface GroupView {
  id: string;
  label: string;
  note: string;
  status: GroupStatus;
  policy: "unwind" | "hold";
  match: "base" | "notional";
  ratio: string;
  /** Why a group is unhedged or failed; empty when there is none. */
  problem?: HedgerText;
  legs: [HedgeLeg, HedgeLeg];
  createdAt: string;
  updatedAt: string;
  closedAt?: string;
  working?: HedgeAction;
  legView: [LegView, LegView];
  exposure: string;
  exposureValue: string;
  imbalance: string;
  balanced: boolean;
  entrySpreadBps: string;
  spreadBps: string;
  exitSpreadBps: string;
  netFundingApr: string;
  fundingEarned: string;
  unrealizedPnl: string;
  realizedPnl: string;
  fees: string;
  netPnl: string;
  notional: string;
  estimated?: boolean;
}

export interface HedgeOrder {
  clientOrderId: string;
  actionId: string;
  leg: 0 | 1;
  role: "maker" | "taker" | "hedge";
  inst: string;
  side: "buy" | "sell";
  type: "market" | "limit";
  qty: string;
  price: string;
  refPrice: string;
  state: string;
  orderId?: string;
  filled: string;
  avgPrice: string;
  error?: HedgerText;
  createdAt: string;
}

export interface HedgeFill {
  clientOrderId: string;
  fillId: string;
  price: string;
  qty: string;
  fee: string;
  feeAsset?: string;
  liquidity?: string;
  ts: string;
}

export interface HedgeEvent {
  id: number;
  at: string;
  level: "info" | "warn" | "error";
  message: HedgerText;
}

export interface FundingAccrual {
  leg: 0 | 1;
  settledAt: string;
  rate: string;
  qty: string;
  price: string;
  amount: string;
}

export interface GroupDetail extends GroupView {
  actions: HedgeAction[];
  orders: HedgeOrder[];
  fills: HedgeFill[];
  events: HedgeEvent[];
  funding: FundingAccrual[];
}

export interface LegInput {
  accountId: string;
  inst: string;
  side: "buy" | "sell";
}

export interface OpenRequest {
  legs: [LegInput, LegInput];
  size: string;
  unit: "base" | "quote";
  mode: ExecMode;
  makerLeg: 0 | 1;
  limitPrice?: string;
  tif?: "gtc" | "post_only";
  policy: "unwind" | "hold";
  label?: string;
  note?: string;
}

export interface CloseRequest {
  fraction: string;
  mode: ExecMode;
  makerLeg: 0 | 1;
  limitPrice?: string;
  tif?: "gtc" | "post_only";
}

export interface Sweep {
  filled: string;
  avgPrice: string;
  best: string;
  slippageBps: string;
  short: boolean;
}

export interface Preview {
  qty: [string, string];
  notional: [string, string];
  ratio: string;
  mismatch: string;
  problems?: HedgerText[];
  price: [string, string];
  sweep: [Sweep, Sweep];
  spreadBps: string;
  funding: [FundingQuote | null, FundingQuote | null];
  netFundingApr: string;
  /** How the legs are sized against each other; the hedger decides. */
  match: "base" | "notional";
  /** The legs as they would open: the rules of each leg's account. */
  legs: [HedgeLeg, HedgeLeg];
  /** Both legs trade from one broker account and share its pool. */
  sharedPool?: boolean;
}

/** How two instruments are matched: by quantity through ratio, or by notional. */
export interface Matching {
  match: "base" | "notional";
  ratio: string;
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-KeelUni": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch("/api/hedge/v1" + path, {
    method, headers, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body),
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = payload.error ?? {};
    // A refused request carries a coded text: render it in the reader's language.
    const message = error.text ? hedgerText(error.text as HedgerText, currentLang()) : error.message;
    throw new ApiError(response.status, error.code ?? "HTTP_" + response.status, message ?? response.statusText);
  }
  return payload as T;
}

export const hedgeApi = {
  groups: (days = 7) => call<GroupView[]>("GET", `/groups?days=${days}`),
  group: (id: string) => call<GroupDetail>("GET", `/groups/${encodeURIComponent(id)}`),
  preview: (request: OpenRequest) => call<Preview>("POST", "/preview", request),
  match: (inst0: string, inst1: string) => call<Matching>("GET", `/match?inst=${encodeURIComponent(inst0)}&inst=${encodeURIComponent(inst1)}`),
  open: (request: OpenRequest) => call<GroupView>("POST", "/groups", request),
  edit: (id: string, label: string, note: string) => call<{ ok: boolean }>("PATCH", `/groups/${encodeURIComponent(id)}`, { label, note }),
  close: (id: string, request: CloseRequest) => call<HedgeAction>("POST", `/groups/${encodeURIComponent(id)}/close`, request),
  cancel: (id: string) => call<{ ok: boolean }>("POST", `/groups/${encodeURIComponent(id)}/cancel`, {}),
  /** Top up the smaller leg so the hedge is back to full size. */
  rehedge: (id: string) => call<HedgeAction>("POST", `/groups/${encodeURIComponent(id)}/rehedge`, {}),
  /** Reduce the larger leg to the smaller one's size. */
  trim: (id: string) => call<HedgeAction>("POST", `/groups/${encodeURIComponent(id)}/trim`, {}),
  unwind: (id: string) => call<HedgeAction>("POST", `/groups/${encodeURIComponent(id)}/unwind`, {}),
};

export type HedgeConnection = "connecting" | "online" | "offline";

/**
 * Live list of hedge groups: the hedger pushes the full list on every change
 * (and every 2 s while groups are active). Reconnects with backoff.
 */
export function useHedgeGroups(): { groups: GroupView[] | undefined; state: HedgeConnection } {
  const [groups, setGroups] = useState<GroupView[] | undefined>(undefined);
  const [state, setState] = useState<HedgeConnection>("connecting");
  useEffect(() => {
    let socket: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let backoff = 500;
    let stopped = false;
    const connect = () => {
      if (stopped) return;
      setState("connecting");
      const current = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/hedge/ws`);
      socket = current;
      current.onopen = () => {
        backoff = 500;
        setState("online");
      };
      current.onmessage = (event) => {
        try {
          const message = JSON.parse(event.data as string);
          if (message.ch === "groups") setGroups(message.data as GroupView[]);
        } catch {
          // ignore malformed frames
        }
      };
      current.onclose = () => {
        if (socket !== current) return;
        setState("offline");
        if (stopped) return;
        timer = setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, 15_000);
      };
    };
    connect();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      socket?.close();
    };
  }, []);
  return { groups, state };
}

/**
 * How the hedger sizes two instruments against each other. The ratio is the
 * hedger's (from the contract multipliers it knows), never guessed here.
 */
export function useMatching(inst0: string, inst1: string): { match: "base" | "notional"; ratio: number } | undefined {
  const query = useQuery({
    queryKey: ["hedgeMatch", inst0, inst1],
    queryFn: () => hedgeApi.match(inst0, inst1),
    enabled: !!inst0 && !!inst1 && inst0 !== inst1,
    staleTime: Infinity,
    retry: false,
  });
  return query.data ? { match: query.data.match, ratio: Number(query.data.ratio) } : undefined;
}

/** A hedge leg holding part of an account's position. */
export interface HedgeHolding {
  groupId: string;
  label: string;
  leg: 0 | 1;
  status: GroupStatus;
  held: string;
}

/**
 * Hedge legs per account and instrument ("account|inst"), for the Trade
 * page's blotter. Read only when the hedge app is deployed; polled, because
 * the blotter only needs to know which positions belong to a hedge.
 */
export function useHedgeHoldings(): Map<string, HedgeHolding[]> {
  const apps = useQuery({ queryKey: ["apps"], queryFn: api.apps, staleTime: Infinity, retry: false });
  const groups = useQuery({
    queryKey: ["hedgeGroups", "holdings"],
    queryFn: () => hedgeApi.groups(1),
    enabled: !!apps.data?.hedge,
    refetchInterval: 15_000,
    retry: false,
  });
  return useMemo(() => {
    const out = new Map<string, HedgeHolding[]>();
    for (const group of groups.data ?? []) {
      if (group.status === "closed" || group.status === "failed") continue;
      for (const leg of group.legView) {
        if (!(Number(leg.held) > 0)) continue;
        const key = `${leg.accountId}|${leg.inst}`;
        out.set(key, [...(out.get(key) ?? []), { groupId: group.id, label: group.label, leg: leg.leg, status: group.status, held: leg.held }]);
      }
    }
    return out;
  }, [groups.data]);
}
