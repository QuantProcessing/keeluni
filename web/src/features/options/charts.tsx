// Small SVG charts for the options page: the smile of one expiry, the term
// structure of ATM IV, and a package's payoff. lightweight-charts plots
// against time only; these plot against strike and spot.

import { useLayoutEffect, useRef, useState, type ReactNode } from "react";

import type { OptionExpiry } from "@/api/options";
import { num } from "@/lib/format";
import { dteLabel, expiryLabel, type PackageSummary, type StrikeRow } from "./model";

function useSize<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [size, setSize] = useState({ width: 0, height: 0 });
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const measure = () => setSize({ width: element.clientWidth, height: element.clientHeight });
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return [ref, size] as const;
}

const PAD = { left: 44, right: 10, top: 10, bottom: 20 };

function scale(domain: [number, number], range: [number, number]) {
  const [d0, d1] = domain;
  const [r0, r1] = range;
  return (value: number) => (d1 === d0 ? (r0 + r1) / 2 : r0 + ((value - d0) / (d1 - d0)) * (r1 - r0));
}

function ticks(min: number, max: number, count: number): number[] {
  if (!(max > min)) return [min];
  const raw = (max - min) / count;
  const power = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * power).find((value) => value >= raw) ?? raw;
  const out: number[] = [];
  for (let value = Math.ceil(min / step) * step; value <= max + step * 1e-9; value += step) out.push(Number(value.toPrecision(12)));
  return out;
}

function Frame({ width, height, children }: { width: number; height: number; children: ReactNode }) {
  return (
    <svg width={width} height={height} className="block select-none" role="img">
      {children}
    </svg>
  );
}

const AXIS = "var(--color-muted)";
const GRID = "var(--color-line)";

/**
 * The smile of one expiry: out-of-the-money mark IV against strike (puts
 * below the forward, calls above, the way the surface is quoted), with each
 * strike's bid–ask IV range as a bar, and the forward marked.
 */
export function SmileChart({ rows, forward }: { rows: StrikeRow[]; forward: number }) {
  const [ref, size] = useSize<HTMLDivElement>();
  const points = rows.flatMap((row) => {
    const option = row.strike < forward ? row.put : row.call;
    if (!option?.markIv) return [];
    return [{ strike: row.strike, mark: option.markIv, bid: option.bidIv, ask: option.askIv }];
  });
  const width = size.width;
  const height = size.height;
  let body: ReactNode = null;
  if (points.length >= 2 && width > 60 && height > 60) {
    // Clip the y range to the visible mark IVs so a far wing's quote does not
    // flatten the smile.
    const marks = points.map((p) => p.mark);
    const low = Math.min(...marks);
    const high = Math.max(...marks);
    const pad = Math.max((high - low) * 0.15, 0.01);
    const y = scale([low - pad, high + pad], [height - PAD.bottom, PAD.top]);
    const x = scale([points[0]!.strike, points[points.length - 1]!.strike], [PAD.left, width - PAD.right]);
    const clamp = (value: number) => Math.max(PAD.top, Math.min(height - PAD.bottom, value));
    const path = points.map((p, i) => `${i ? "L" : "M"}${x(p.strike).toFixed(1)},${y(p.mark).toFixed(1)}`).join("");
    body = (
      <>
        {ticks(low - pad, high + pad, 4).map((value) => (
          <g key={value}>
            <line x1={PAD.left} x2={width - PAD.right} y1={y(value)} y2={y(value)} stroke={GRID} />
            <text x={PAD.left - 6} y={y(value) + 3} textAnchor="end" fontSize="10" fill={AXIS}>{(value * 100).toFixed(0)}%</text>
          </g>
        ))}
        {ticks(points[0]!.strike, points[points.length - 1]!.strike, 5).map((value) => (
          <text key={value} x={x(value)} y={height - 6} textAnchor="middle" fontSize="10" fill={AXIS}>{num(value, 0)}</text>
        ))}
        {forward > 0 && <line x1={x(forward)} x2={x(forward)} y1={PAD.top} y2={height - PAD.bottom} stroke="var(--color-line-strong)" strokeDasharray="3 3" />}
        {points.map((p) => p.bid && p.ask ? (
          <line key={p.strike} x1={x(p.strike)} x2={x(p.strike)} y1={clamp(y(p.bid))} y2={clamp(y(p.ask))} stroke="var(--color-text-2)" strokeOpacity="0.35" strokeWidth="3" />
        ) : null)}
        <path d={path} fill="none" stroke="var(--color-accent)" strokeWidth="1.5" />
        {points.map((p) => <circle key={p.strike} cx={x(p.strike)} cy={y(p.mark)} r="1.8" fill="var(--color-accent)" />)}
      </>
    );
  }
  return <div ref={ref} className="min-h-0 flex-1">{width > 0 && <Frame width={width} height={height}>{body}</Frame>}</div>;
}

/** ATM IV per expiry against days to expiry; the selected expiry ringed. */
export function TermChart({ expiries, selected, now }: { expiries: OptionExpiry[]; selected: number; now: number }) {
  const [ref, size] = useSize<HTMLDivElement>();
  const points = expiries.filter((item) => item.atmIv && item.expiry > now).map((item) => ({ expiry: item.expiry, days: (item.expiry - now) / 86_400_000, iv: item.atmIv! }));
  const { width, height } = size;
  let body: ReactNode = null;
  if (points.length >= 2 && width > 60 && height > 60) {
    const ivs = points.map((p) => p.iv);
    const pad = Math.max((Math.max(...ivs) - Math.min(...ivs)) * 0.2, 0.01);
    const y = scale([Math.min(...ivs) - pad, Math.max(...ivs) + pad], [height - PAD.bottom, PAD.top]);
    // Square-root time spreads the front, where most expiries are.
    const x = scale([Math.sqrt(points[0]!.days), Math.sqrt(points[points.length - 1]!.days)], [PAD.left, width - PAD.right]);
    const at = (days: number) => x(Math.sqrt(days));
    body = (
      <>
        {ticks(Math.min(...ivs) - pad, Math.max(...ivs) + pad, 4).map((value) => (
          <g key={value}>
            <line x1={PAD.left} x2={width - PAD.right} y1={y(value)} y2={y(value)} stroke={GRID} />
            <text x={PAD.left - 6} y={y(value) + 3} textAnchor="end" fontSize="10" fill={AXIS}>{(value * 100).toFixed(0)}%</text>
          </g>
        ))}
        <path d={points.map((p, i) => `${i ? "L" : "M"}${at(p.days).toFixed(1)},${y(p.iv).toFixed(1)}`).join("")} fill="none" stroke="var(--color-accent)" strokeWidth="1.5" />
        {points.map((p) => (
          <circle key={p.expiry} cx={at(p.days)} cy={y(p.iv)} r={p.expiry === selected ? 4 : 2} fill={p.expiry === selected ? "none" : "var(--color-accent)"} stroke="var(--color-accent)" />
        ))}
        {labelled(points.map((p) => ({ key: p.expiry, x: at(p.days), selected: p.expiry === selected })), 44).map((label) => (
          <text key={label.key} x={label.x} y={height - 6} textAnchor="middle" fontSize="10" fill={label.selected ? "var(--color-text)" : AXIS}>
            {expiryLabel(label.key).slice(0, -2)}
          </text>
        ))}
      </>
    );
  }
  return <div ref={ref} className="min-h-0 flex-1">{width > 0 && <Frame width={width} height={height}>{body}</Frame>}</div>;
}

/** Axis labels at least gap px apart; the selected one always wins its place. */
function labelled<T extends { x: number; selected: boolean }>(items: T[], gap: number): T[] {
  const chosen = items.filter((item) => item.selected);
  for (const item of items) {
    if (!item.selected && chosen.every((other) => Math.abs(other.x - item.x) >= gap)) chosen.push(item);
  }
  return chosen;
}

/**
 * A package's value against the underlying: at the first expiry (solid)
 * and today at current IVs (dashed); profit above zero in teal, loss pink.
 */
export function PayoffChart({ summary, forward, height = 150 }: { summary: PackageSummary | undefined; forward: number; height?: number }) {
  const [ref, size] = useSize<HTMLDivElement>();
  const width = size.width;
  const curve = summary?.curve ?? [];
  let body: ReactNode = null;
  if (curve.length > 2 && width > 60) {
    const values = curve.flatMap((p) => [p.expiry, p.today]);
    const low = Math.min(0, ...values);
    const high = Math.max(0, ...values);
    const pad = Math.max((high - low) * 0.08, 1);
    const y = scale([low - pad, high + pad], [height - PAD.bottom, PAD.top]);
    const x = scale([curve[0]!.spot, curve[curve.length - 1]!.spot], [PAD.left, width - PAD.right]);
    const line = (pick: (p: (typeof curve)[number]) => number) => curve.map((p, i) => `${i ? "L" : "M"}${x(p.spot).toFixed(1)},${y(pick(p)).toFixed(1)}`).join("");
    const zero = y(0);
    const area = `${line((p) => p.expiry)}L${x(curve[curve.length - 1]!.spot)},${zero}L${x(curve[0]!.spot)},${zero}Z`;
    body = (
      <>
        <defs>
          <clipPath id="payoff-up"><rect x={PAD.left} y={PAD.top} width={width} height={Math.max(0, zero - PAD.top)} /></clipPath>
          <clipPath id="payoff-down"><rect x={PAD.left} y={zero} width={width} height={Math.max(0, height - PAD.bottom - zero)} /></clipPath>
        </defs>
        {ticks(low - pad, high + pad, 3).map((value) => (
          <text key={value} x={PAD.left - 6} y={y(value) + 3} textAnchor="end" fontSize="10" fill={AXIS}>{Math.abs(value) >= 1000 ? `${(value / 1000).toFixed(1)}k` : value.toFixed(0)}</text>
        ))}
        <path d={area} fill="var(--color-up)" fillOpacity="0.12" clipPath="url(#payoff-up)" />
        <path d={area} fill="var(--color-down)" fillOpacity="0.12" clipPath="url(#payoff-down)" />
        <line x1={PAD.left} x2={width - PAD.right} y1={zero} y2={zero} stroke="var(--color-line-strong)" />
        <line x1={x(forward)} x2={x(forward)} y1={PAD.top} y2={height - PAD.bottom} stroke="var(--color-line-strong)" strokeDasharray="3 3" />
        <path d={line((p) => p.today)} fill="none" stroke="var(--color-text-2)" strokeWidth="1" strokeDasharray="4 3" />
        <path d={line((p) => p.expiry)} fill="none" stroke="var(--color-accent)" strokeWidth="1.5" />
        {(summary?.breakevens ?? []).map((value) => <circle key={value} cx={x(value)} cy={zero} r="2.5" fill="var(--color-text)" />)}
        {labelled((summary?.breakevens ?? []).map((value) => ({ key: value, x: x(value), selected: false })), 48).map((label) => (
          <text key={label.key} x={label.x} y={height - 6} textAnchor="middle" fontSize="10" fill="var(--color-text-2)">{num(label.key, 0)}</text>
        ))}
      </>
    );
  }
  return <div ref={ref} style={{ height }}>{width > 0 && <Frame width={width} height={height}>{body}</Frame>}</div>;
}

export { dteLabel };
