// Option chains (gateway /v1/options) and the options app (optioner, served
// by the trader at /api/options). Wire types mirror gateway/optionchain and
// optioner/*.go.

import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { ApiError } from "./client";
import type { Instrument } from "./types";
import { realtime } from "./realtime";

export interface OptionSeries {
  /** "deribit:BTC:BTC": venue, underlying, premium asset. */
  series: string;
  venue: string;
  market: string;
  underlying: string;
  premiumAsset: string;
  /** The currency of strikes and greeks. */
  strikeAsset: string;
  inverse?: boolean;
  index?: number;
  expiries: number;
  contracts: number;
  frontAtmIv?: number;
  stale: boolean;
}

/** One option: prices in the premium asset, IVs as fractions, greeks per unit in USD. */
export interface OptionRow {
  inst: string;
  kind: "call" | "put";
  strike: string;
  expiry: number;
  bid?: string;
  ask?: string;
  mark?: string;
  last?: string;
  markIv?: number;
  bidIv?: number;
  askIv?: number;
  delta: number;
  gamma: number;
  vega: number;
  theta: number;
  oi?: string;
  volume?: string;
}

export interface OptionExpiry {
  expiry: number;
  forward?: number;
  /**
   * The expiry's discount factor: an option is worth discount × Black-76 on
   * the forward. 1 (or absent) where the venue publishes a forward (Deribit,
   * OKX); e^(−rT) where it values on spot with a rate (Binance).
   */
  discount?: number;
  atmIv?: number;
  rr25?: number;
  bf25?: number;
  strikes: number;
}

export interface OptionChain {
  series: string;
  venue: string;
  underlying: string;
  premiumAsset: string;
  strikeAsset: string;
  inverse?: boolean;
  index?: number;
  updatedAt: number;
  stale: boolean;
  expiries: OptionExpiry[];
  rows: OptionRow[];
}

/** The live quote of one option (gateway "option" channel). */
export interface OptionQuote {
  bid?: string;
  ask?: string;
  bidSize?: string;
  askSize?: string;
  mark?: string;
  markIv?: number;
  bidIv?: number;
  askIv?: number;
  underlying?: string;
  index?: string;
  delta?: number;
  gamma?: number;
  vega?: number;
  theta?: number;
  oi?: string;
  ts: number;
}

// ---- optioner

export interface Greeks {
  delta: number;
  deltaUsd: number;
  gamma1pct: number;
  vega: number;
  theta: number;
  value: number;
  pnl: number;
}

export interface PositionRisk {
  account: string;
  inst: string;
  product: "option" | "perp" | "spot";
  kind?: "call" | "put";
  strike?: string;
  expiry?: number;
  size: number;
  entry: string;
  mark: string;
  markIv?: number;
  premiumAsset?: string;
  greeks: Greeks;
}

export interface Scenario {
  spotMoves: number[];
  volMoves: number[];
  pnl: number[][];
  days: number;
}

export interface UnderlyingRisk {
  underlying: string;
  spot: number;
  greeks: Greeks;
  hedgeDelta: number;
  expiries: Array<{ expiry: number; dte: number; greeks: Greeks }>;
  positions: PositionRisk[];
  scenario: Scenario;
}

export interface Risk {
  underlyings: UnderlyingRisk[];
  updatedAt: number;
  stale: boolean;
}

export interface LegRequest {
  account: string;
  inst: string;
  side: "buy" | "sell";
  qty: string;
  price?: string;
  priceBy?: "mark" | "mid" | "bid" | "ask" | "iv";
  iv?: number;
  type?: "limit" | "market";
  tif?: "gtc" | "ioc" | "post_only";
  reduceOnly?: boolean;
}

export interface ExecLeg {
  account: string;
  inst: string;
  side: "buy" | "sell";
  qty: string;
  price?: string;
  type: "limit" | "market";
  tif?: string;
  reduceOnly?: boolean;
  pricedAt?: string;
  clientOrderId: string;
  orderId?: string;
  state: "pending" | "sent" | "open" | "partially_filled" | "filled" | "canceled" | "rejected" | "unknown";
  filled: string;
  avgPrice?: string;
  message?: string;
}

export interface Execution {
  id: string;
  label?: string;
  status: "working" | "done" | "partial" | "failed" | "canceled";
  legs: ExecLeg[];
  createdAt: number;
  updatedAt: number;
  problem?: string;
}

export interface HedgeSuggestion {
  underlying: string;
  delta: number;
  leg: LegRequest;
  residual: number;
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-KeelUni": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(path, { method, headers, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body) });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = payload.error ?? {};
    throw new ApiError(response.status, error.code ?? "HTTP_" + response.status, error.message ?? response.statusText);
  }
  return payload as T;
}

const app = (path: string) => "/api/options/v1" + path;

export const optionsApi = {
  /** Option instruments (listed only on request: thousands of contracts). */
  instruments: () => call<Instrument[]>("GET", "/api/gw/v1/instruments?product=option"),
  series: () => call<OptionSeries[]>("GET", "/api/gw/v1/options"),
  chain: (series: string) => call<OptionChain>("GET", "/api/gw/v1/options/chain?series=" + encodeURIComponent(series)),
  preview: (legs: LegRequest[]) => call<ExecLeg[]>("POST", app("/preview"), { legs }),
  submit: (label: string, legs: LegRequest[]) => call<Execution>("POST", app("/executions"), { label, legs }),
  cancel: (id: string) => call<Execution>("POST", app(`/executions/${encodeURIComponent(id)}/cancel`)),
  hedge: (underlying: string, account: string, hedges: boolean) =>
    call<HedgeSuggestion>("GET", app(`/hedge?underlying=${encodeURIComponent(underlying)}&account=${encodeURIComponent(account)}&hedges=${hedges ? 1 : 0}`)),
};

export function useOptionSeries() {
  return useQuery({ queryKey: ["optionSeries"], queryFn: optionsApi.series, refetchInterval: 30_000 });
}

/** One series' chain: a REST read, then every rebuild from the gateway stream. */
export function useOptionChain(series: string | undefined): OptionChain | undefined {
  const initial = useQuery({ queryKey: ["optionChain", series], queryFn: () => optionsApi.chain(series!), enabled: !!series, staleTime: 5_000, retry: 1 });
  const [live, setLive] = useState<OptionChain | undefined>(undefined);
  useEffect(() => {
    setLive(undefined);
    if (!series) return;
    return realtime.subscribe({ ch: "options", inst: series }, (message) => {
      if (message.ch === "options") setLive(message.data as OptionChain);
    });
  }, [series]);
  return live && live.updatedAt >= (initial.data?.updatedAt ?? 0) ? live : initial.data;
}

/** The live quote of one option. */
export function useOptionQuote(inst: string | undefined): OptionQuote | undefined {
  const [quote, setQuote] = useState<OptionQuote | undefined>(undefined);
  useEffect(() => {
    setQuote(undefined);
    if (!inst) return;
    return realtime.subscribe({ ch: "option", inst }, (message) => {
      if (message.ch === "option") setQuote(message.data as OptionQuote);
    });
  }, [inst]);
  return quote;
}

/**
 * Risk and executions from the options app's socket. The risk options are
 * fixed per connection; changing them reconnects.
 */
export function useOptionerStream(accounts: string[], hedges: boolean, days: number) {
  const [risk, setRisk] = useState<Risk | undefined>(undefined);
  const [executions, setExecutions] = useState<Execution[]>([]);
  const [online, setOnline] = useState(false);
  const key = `${accounts.join(",")}|${hedges}|${days}`;
  useEffect(() => {
    let socket: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let closed = false;
    let backoff = 500;
    const query = new URLSearchParams({ hedges: hedges ? "1" : "0", days: String(days) });
    if (accounts.length) query.set("accounts", accounts.join(","));
    const connect = () => {
      socket = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/options/ws?${query}`);
      socket.onopen = () => {
        backoff = 500;
        setOnline(true);
      };
      socket.onmessage = (event) => {
        try {
          const message = JSON.parse(event.data as string) as { ch: string; data: unknown };
          if (message.ch === "risk") setRisk(message.data as Risk);
          if (message.ch === "executions") setExecutions(message.data as Execution[]);
        } catch {
          // ignore a malformed frame
        }
      };
      socket.onclose = () => {
        setOnline(false);
        if (closed) return;
        timer = setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, 15_000);
      };
    };
    connect();
    return () => {
      closed = true;
      if (timer) clearTimeout(timer);
      socket?.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return { risk, executions, online };
}
