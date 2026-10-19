import { useQuery } from "@tanstack/react-query";
import { BaselineSeries, LineSeries, createChart, type IChartApi, type ISeriesApi, type UTCTimestamp } from "lightweight-charts";
import { useEffect, useMemo, useRef, useState } from "react";

import { api } from "@/api/client";
import type { Candle } from "@/api/types";
import { useCandleStream, useInstrument } from "@/state/hooks";
import { cx } from "@/ui/primitives";
import { aprText, bpsText, netFundingSeries, seriesStats, spreadSeries, tone, type Pair, type SpreadPoint } from "./model";

const INTERVALS = ["1m", "5m", "15m", "1h", "4h", "1d"] as const;
const WINDOWS = [["7d", 7], ["30d", 30]] as const;
type View = "spread" | "funding";

/**
 * The pair over time. Spread: leg 2 over leg 1 per bar, from both legs'
 * candles (bps through the ratio for one underlying, else the price ratio).
 * Funding: the net APR the pair earned at each settlement, and each leg's.
 */
export function SpreadChart({ pair, matching }: { pair: Pair; matching: { match: "base" | "notional"; ratio: number } | undefined }) {
  const [view, setView] = useState<View>("spread");
  const [interval, setIntervalValue] = useState<string>(() => localStorage.getItem("bolter.hedge.interval") ?? "15m");
  const [days, setDays] = useState<number>(7);
  const inst0 = useInstrument(pair[0].inst);
  const inst1 = useInstrument(pair[1].inst);

  // Candles: one REST page each, then the live bar from the stream.
  const candles0 = useQuery({ queryKey: ["candles", pair[0].inst, interval], queryFn: ({ signal }) => api.candles(pair[0].inst, interval, undefined, signal), staleTime: 60_000 });
  const candles1 = useQuery({ queryKey: ["candles", pair[1].inst, interval], queryFn: ({ signal }) => api.candles(pair[1].inst, interval, undefined, signal), staleTime: 60_000 });
  const [live, setLive] = useState<[Candle | undefined, Candle | undefined]>([undefined, undefined]);
  useEffect(() => setLive([undefined, undefined]), [pair[0].inst, pair[1].inst, interval]);
  useCandleStream(pair[0].inst, interval, useMemo(() => (candle: Candle) => setLive((current) => [candle, current[1]]), []));
  useCandleStream(pair[1].inst, interval, useMemo(() => (candle: Candle) => setLive((current) => [current[0], candle]), []));

  const spread = useMemo(() => {
    if (!matching || !candles0.data || !candles1.data) return [];
    const merge = (rows: Candle[], last: Candle | undefined) => (last ? [...rows.filter((row) => row.t !== last.t), last] : rows);
    return spreadSeries(merge(candles0.data, live[0]), merge(candles1.data, live[1]), matching.match, matching.ratio);
  }, [candles0.data, candles1.data, live, matching]);

  // Funding: settled history of each perp leg over the window.
  const since = Math.floor((Date.now() - days * 86_400_000) / 3_600_000) * 3_600_000;
  const perp = [inst0?.product === "perp", inst1?.product === "perp"] as const;
  const history0 = useQuery({ queryKey: ["fundingHistory", pair[0].inst, since], queryFn: ({ signal }) => api.fundingHistory(pair[0].inst, since, signal), enabled: view === "funding" && perp[0], staleTime: 10 * 60_000, retry: false });
  const history1 = useQuery({ queryKey: ["fundingHistory", pair[1].inst, since], queryFn: ({ signal }) => api.fundingHistory(pair[1].inst, since, signal), enabled: view === "funding" && perp[1], staleTime: 10 * 60_000, retry: false });
  const funding = useMemo(() => {
    const legs = [0, 1].map((i) => ({ side: pair[i]!.side, spot: !perp[i], points: (i === 0 ? history0.data : history1.data) ?? [] }));
    const aprs = (points: Array<{ t: number; apr?: string }>): SpreadPoint[] => points.filter((point) => point.apr !== undefined).map((point) => ({ t: point.t, value: Number(point.apr) }));
    return { net: netFundingSeries(legs), leg0: aprs(legs[0]!.points), leg1: aprs(legs[1]!.points) };
  }, [history0.data, history1.data, pair, perp[0], perp[1]]);

  const loading = view === "spread" ? candles0.isLoading || candles1.isLoading : history0.isLoading || history1.isLoading;
  const failed = view === "spread" ? candles0.error || candles1.error : null;
  const stats = view === "spread" ? seriesStats(spread) : seriesStats(funding.net);
  const bps = matching?.match === "base";
  const format = (value: number | undefined) => (view === "funding" ? aprText(value) : bps ? bpsText(value, 1) : value === undefined ? "—" : value.toPrecision(6));

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-stretch gap-1 border-b border-line pr-3">
        {(["spread", "funding"] as const).map((item) => (
          <button key={item} onClick={() => setView(item)} className={cx("-mb-px border-b px-3 whitespace-nowrap", view === item ? "border-accent text-text" : "border-transparent text-muted hover:text-text")}>
            {item === "spread" ? (bps ? "Spread" : "Price ratio") : "Net funding"}
          </button>
        ))}
        <div className="ml-auto flex items-center">
          <span className="flex whitespace-nowrap">
            {view === "spread"
              ? INTERVALS.map((item) => (
                <button key={item} onClick={() => { setIntervalValue(item); localStorage.setItem("bolter.hedge.interval", item); }} className={cx("rounded px-1.5 py-0.5", interval === item ? "text-text" : "text-muted hover:text-text")}>{item}</button>
              ))
              : WINDOWS.map(([label, value]) => (
                <button key={label} onClick={() => setDays(value)} className={cx("rounded px-1.5 py-0.5", days === value ? "text-text" : "text-muted hover:text-text")}>{label}</button>
              ))}
          </span>
        </div>
      </div>
      <div className="relative min-h-0 flex-1">
        <Plot view={view} spread={spread} funding={funding} bps={bps} />
        {stats && (
          <div className="pointer-events-none absolute top-2 left-3 z-10 flex gap-3 whitespace-nowrap text-muted">
            <span>Now <span className={cx("text-text", tone(stats.last - (bps || view === "funding" ? 0 : 1)))}>{format(stats.last)}</span></span>
            <span>Avg <span className="text-text">{format(stats.mean)}</span></span>
            <span>Low <span className="text-text">{format(stats.low)}</span></span>
            <span>High <span className="text-text">{format(stats.high)}</span></span>
          </div>
        )}
        {(loading || failed || (view === "spread" && !loading && !spread.length && matching)) && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center text-muted">
            {loading ? "Loading…" : failed ? "Candles unavailable for this pair" : "No overlapping bars"}
          </div>
        )}
        {view === "funding" && !perp[0] && !perp[1] && <div className="absolute inset-0 flex items-center justify-center text-muted">Spot legs pay no funding</div>}
      </div>
      {view === "funding" && (
        <div className="flex h-6 shrink-0 items-center gap-4 border-t border-line px-3 text-muted">
          <Legend color="#50d2c1" label="Net (short receives, long pays)" />
          {perp[0] && <Legend color="#949e9c" label={`Leg 1 ${inst0?.symbol ?? ""}`} />}
          {perp[1] && <Legend color="#575e62" label={`Leg 2 ${inst1?.symbol ?? ""}`} dashed />}
        </div>
      )}
    </div>
  );
}

function Legend({ color, label, dashed }: { color: string; label: string; dashed?: boolean }) {
  return (
    <span className="flex items-center gap-1.5">
      <span className="inline-block w-3 border-t" style={{ borderColor: color, borderStyle: dashed ? "dashed" : "solid" }} />
      {label}
    </span>
  );
}

const time = (t: number) => Math.floor(t / 1000) as UTCTimestamp;
const line = (points: SpreadPoint[]) => points.map((point) => ({ time: time(point.t), value: point.value }));

/** One lightweight chart; series are swapped with the view. */
function Plot({ view, spread, funding, bps }: { view: View; spread: SpreadPoint[]; funding: { net: SpreadPoint[]; leg0: SpreadPoint[]; leg1: SpreadPoint[] }; bps: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const chart = useRef<IChartApi | null>(null);
  const series = useRef<{ spread?: ISeriesApi<"Baseline">; net?: ISeriesApi<"Baseline">; leg0?: ISeriesApi<"Line">; leg1?: ISeriesApi<"Line"> }>({});
  const fitted = useRef("");

  useEffect(() => {
    if (!host.current) return;
    const instance = createChart(host.current, {
      autoSize: true,
      layout: { background: { color: "#0f1a1f" }, textColor: "#949e9c", fontSize: 11, fontFamily: "Inter Variable, system-ui", attributionLogo: false },
      grid: { vertLines: { color: "#1c262c" }, horzLines: { color: "#1c262c" } },
      rightPriceScale: { borderColor: "#273035", scaleMargins: { top: 0.12, bottom: 0.08 } },
      timeScale: { borderColor: "#273035", timeVisible: true, secondsVisible: false },
      crosshair: { mode: 0 },
    });
    chart.current = instance;
    return () => {
      instance.remove();
      chart.current = null;
      series.current = {};
    };
  }, []);

  useEffect(() => {
    const instance = chart.current;
    if (!instance) return;
    for (const item of Object.values(series.current)) if (item) instance.removeSeries(item);
    series.current = {};
    const baseline = { baseValue: { type: "price" as const, price: 0 }, topLineColor: "#50d2c1", topFillColor1: "rgba(80,210,193,0.18)", topFillColor2: "rgba(80,210,193,0.02)", bottomLineColor: "#ed7088", bottomFillColor1: "rgba(237,112,136,0.02)", bottomFillColor2: "rgba(237,112,136,0.18)", lineWidth: 2 as const, priceLineVisible: false };
    if (view === "spread") {
      series.current.spread = instance.addSeries(BaselineSeries, {
        ...baseline,
        // A price ratio has no zero line: plain teal.
        ...(bps ? {} : { baseValue: { type: "price" as const, price: 1 }, bottomLineColor: "#50d2c1", bottomFillColor1: "rgba(80,210,193,0.02)", bottomFillColor2: "rgba(80,210,193,0.02)" }),
        priceFormat: { type: "custom", formatter: (value: number) => (bps ? `${value.toFixed(1)} bps` : value.toPrecision(6)), minMove: bps ? 0.1 : 0.000001 },
      });
    } else {
      const percent = { type: "custom" as const, formatter: (value: number) => `${(value * 100).toFixed(2)}%`, minMove: 0.0001 };
      series.current.leg0 = instance.addSeries(LineSeries, { color: "#949e9c", lineWidth: 1, priceFormat: percent, priceLineVisible: false, lastValueVisible: false, lineType: 1 });
      series.current.leg1 = instance.addSeries(LineSeries, { color: "#575e62", lineWidth: 1, lineStyle: 2, priceFormat: percent, priceLineVisible: false, lastValueVisible: false, lineType: 1 });
      series.current.net = instance.addSeries(BaselineSeries, { ...baseline, priceFormat: percent });
    }
    fitted.current = "";
  }, [view, bps]);

  useEffect(() => {
    const instance = chart.current;
    if (!instance) return;
    if (view === "spread") {
      series.current.spread?.setData(line(spread));
    } else {
      series.current.net?.setData(line(funding.net));
      series.current.leg0?.setData(line(funding.leg0));
      series.current.leg1?.setData(line(funding.leg1));
    }
    // Fit once per view and data set; live updates keep the user's zoom.
    const key = `${view}|${view === "spread" ? spread[0]?.t : funding.net[0]?.t}`;
    if (key !== fitted.current && (view === "spread" ? spread.length : funding.net.length)) {
      fitted.current = key;
      instance.timeScale().fitContent();
    }
  }, [view, spread, funding]);

  return <div ref={host} className="absolute inset-0" />;
}
