import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { hedgeApi, type OpenRequest, type Preview } from "@/api/hedge";
import { num, price, snapToStep, usd } from "@/lib/format";
import { useBook, useInstrument, type AccountLive } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { PrimaryButton, Row, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { aprText, bpsText, breakevenDays, inferMatch, legAvailable, legPosition, makePrice, takePrice, tone, type Pair } from "./model";

type Mode = "market" | "limit";
type Unit = "base" | "quote";

/**
 * The open ticket. Market: both legs at market at once. Limit: one leg rests
 * at the limit price; every fill is hedged at market on the other leg, so the
 * position is never more than one fill away from flat.
 */
export function HedgeTicket({ pair, lives }: { pair: Pair; lives: readonly [AccountLive, AccountLive] }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const { setConnectOpen } = useWorkspace();
  const inst0 = useInstrument(pair[0].inst);
  const inst1 = useInstrument(pair[1].inst);
  const book0 = useBook(pair[0].inst);
  const book1 = useBook(pair[1].inst);
  const [mode, setMode] = useState<Mode>("market");
  const [unit, setUnit] = useState<Unit>("quote");
  const [size, setSize] = useState("");
  const [maker, setMaker] = useState<0 | 1>(0);
  const [limitPrice, setLimitPrice] = useState("");
  const [postOnly, setPostOnly] = useState(true);
  const [policy, setPolicy] = useState<"unwind" | "hold">("unwind");
  const [label, setLabel] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewError, setPreviewError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  // Memoized on the bases: the books re-render this ticket every 100 ms, and
  // a fresh object here would restart the preview debounce forever.
  const base0 = inst0?.base;
  const base1 = inst1?.base;
  const matching = useMemo(() => (base0 && base1 ? inferMatch(base0, base1) : undefined), [base0, base1]);
  const makerInst = maker === 0 ? inst0 : inst1;
  const makerBook = maker === 0 ? book0 : book1;
  const ready = !!inst0 && !!inst1 && !!pair[0].accountId && !!pair[1].accountId;
  const sameSide = pair[0].side === pair[1].side && matching?.match === "base";

  useEffect(() => { setLimitPrice(""); }, [maker, pair[0].inst, pair[1].inst]);

  // The preview needs only the instruments; opening also needs both accounts.
  const legKey = `${pair[0].inst}|${pair[0].accountId}|${pair[0].side}|${pair[1].inst}|${pair[1].accountId}|${pair[1].side}`;
  const draft = useMemo<OpenRequest | null>(() => {
    if (!base0 || !base1 || !matching || !(Number(size) > 0)) return null;
    if (mode === "limit" && !(Number(limitPrice) > 0)) return null;
    return {
      legs: [
        { accountId: pair[0].accountId, inst: pair[0].inst, side: pair[0].side },
        { accountId: pair[1].accountId, inst: pair[1].inst, side: pair[1].side },
      ],
      size, unit, match: matching.match, ratio: matching.match === "base" ? String(matching.ratio) : undefined,
      mode, makerLeg: maker, limitPrice: mode === "limit" ? limitPrice : undefined,
      tif: mode === "limit" ? (postOnly ? "post_only" : "gtc") : undefined,
      policy, label: label.trim() || undefined,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base0, base1, matching, size, unit, mode, maker, limitPrice, postOnly, policy, label, legKey]);
  const request = ready ? draft : null;

  // Preview against the live books, debounced, then refreshed while the
  // books move.
  useEffect(() => {
    setPreviewError("");
    if (!draft) {
      setPreview(null);
      return;
    }
    const load = () => hedgeApi.preview(draft).then((value) => { setPreview(value); setPreviewError(""); }).catch((error) => {
      setPreview(null);
      setPreviewError((error as Error).message);
    });
    const first = setTimeout(load, 300);
    const refresh = setInterval(load, 3000);
    return () => { clearTimeout(first); clearInterval(refresh); };
  }, [draft]);

  const available = [legAvailable(inst0, lives[0].balances, pair[0].side), legAvailable(inst1, lives[1].balances, pair[1].side)];
  // Venues net positions per account and instrument: a hedge leg on an
  // instrument the account already holds shares that position.
  const shared = ([0, 1] as const).filter((i) => !!legPosition(lives[i].positions, pair[i].inst));
  const problems = preview?.problems ?? [];
  const costBps = useMemo(() => {
    if (!preview) return undefined;
    // Crossing cost of each taker leg relative to its touch, averaged over
    // the two legs' notional (fees excluded: they depend on the account tier).
    const legs = [0, 1].filter((i) => !(mode === "limit" && i === maker));
    const total = legs.reduce((sum, i) => sum + Number(preview.sweep[i]?.slippageBps ?? 0) * Number(preview.notional[i]), 0);
    const notional = legs.reduce((sum, i) => sum + Number(preview.notional[i]), 0);
    return notional > 0 ? total / notional : undefined;
  }, [preview, mode, maker]);
  const breakeven = costBps === undefined ? undefined : breakevenDays(costBps, Number(preview?.netFundingApr));

  const submit = async () => {
    if (!request) return;
    setSubmitting(true);
    try {
      const group = await hedgeApi.open(request);
      toast.push({ kind: "info", title: `Hedge ${mode === "market" ? "opening" : "working"}`, detail: group.label });
      setSize("");
      void queryClient.invalidateQueries({ queryKey: ["hedgeGroup"] });
    } catch (error) {
      toast.push({ kind: "error", title: "Hedge not opened", detail: (error as Error).message });
    } finally {
      setSubmitting(false);
    }
  };

  const setPriceAt = (kind: "join" | "touch" | "mid") => {
    if (!makerInst) return;
    const side = pair[maker].side;
    const join = makePrice(makerBook, side);
    const touch = takePrice(makerBook, side);
    const value = kind === "join" ? join : kind === "touch" ? touch : join && touch ? (join + touch) / 2 : undefined;
    if (value) setLimitPrice(snapToStep(value.toFixed(12), makerInst.tickSize));
  };

  const units = { base: inst0?.base ?? "Base", quote: inst0?.quote ?? "Quote" };

  return (
    <div className="flex flex-col gap-3 bg-surface p-3">
      <div className="text-md">Open hedge</div>
      <Tabs value={mode} onChange={setMode} options={[{ value: "market", label: "Market both" }, { value: "limit", label: "Limit + hedge" }]} className="[&>button]:flex-1" />

      {mode === "limit" && (
        <>
          <div>
            <div className="mb-1 text-muted">Resting leg</div>
            <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5">
              {([0, 1] as const).map((leg) => (
                <button key={leg} onClick={() => setMaker(leg)} className={cx("truncate rounded-md px-2", maker === leg ? "bg-line-strong/60 text-text" : "text-muted")}>
                  Leg {leg + 1} · {(leg === 0 ? inst0 : inst1)?.venue ?? ""}
                </button>
              ))}
            </div>
            <p className="mt-1 text-muted">Rests on leg {maker + 1}; each fill is hedged at market on leg {2 - maker}.</p>
          </div>
          <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
            <span className="text-muted">Price</span>
            <input inputMode="decimal" value={limitPrice} onChange={(event) => setLimitPrice(sanitize(event.target.value))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
            <span className="text-muted">{makerInst?.quote}</span>
          </label>
          <div className="-mt-1 flex gap-1">
            {(["join", "mid", "touch"] as const).map((kind) => (
              <button key={kind} onClick={() => setPriceAt(kind)} className="h-6 flex-1 rounded-md bg-raised text-text-2 hover:text-text" title={{ join: "Best price on this leg's side of the book (rests)", mid: "Middle of the book", touch: "Best opposite price (fills immediately unless Post only)" }[kind]}>
                {{ join: pair[maker].side === "buy" ? "Bid" : "Ask", mid: "Mid", touch: pair[maker].side === "buy" ? "Ask" : "Bid" }[kind]}
              </button>
            ))}
          </div>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={postOnly} onChange={(event) => setPostOnly(event.target.checked)} className="accent-[#50d2c1]" />
            Post only <span className="text-muted">(never takes; maker fee)</span>
          </label>
        </>
      )}

      <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
        <span className="text-muted">Size</span>
        <input inputMode="decimal" value={size} onChange={(event) => setSize(sanitize(event.target.value))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
        <select value={unit} onChange={(event) => { setUnit(event.target.value as Unit); setSize(""); }} className="bg-transparent text-text outline-none" aria-label="Size unit">
          <option value="quote">{units.quote}</option>
          <option value="base">{units.base}</option>
        </select>
      </label>
      <p className="-mt-2 text-muted">{unit === "quote" ? `Notional per leg, in ${units.quote}` : `Quantity of leg 1, in ${units.base}`}{matching?.match === "notional" ? "; legs are matched by notional" : ""}</p>

      <div>
        <Row label="Leg 1 available">{available[0] ? `${num(available[0].amount)} ${available[0].asset}` : "—"}</Row>
        <Row label="Leg 2 available">{available[1] ? `${num(available[1].amount)} ${available[1].asset}` : "—"}</Row>
      </div>

      <label className="flex items-center justify-between gap-2">
        <span className="dotted text-muted" title="If one leg fills and the other cannot, either reverse the filled leg at market (Unwind) or keep it and mark the hedge broken for you to decide (Hold).">If a leg fails</span>
        <select value={policy} onChange={(event) => setPolicy(event.target.value as "unwind" | "hold")} className="bg-transparent text-right outline-none">
          <option value="unwind">Unwind the other leg</option>
          <option value="hold">Hold and alert</option>
        </select>
      </label>
      <label className="flex h-9 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
        <span className="text-muted">Label</span>
        <input value={label} maxLength={80} onChange={(event) => setLabel(event.target.value)} placeholder="optional" className="min-w-0 flex-1 bg-transparent text-right outline-none placeholder:text-faint" />
      </label>

      <PrimaryButton onClick={ready ? submit : () => setConnectOpen(true)} disabled={ready && (!request || submitting || problems.length > 0 || !!previewError || sameSide)}>
        {!ready ? "Connect both venues" : submitting ? "Opening…" : sameSide ? "Legs must be opposite sides" : mode === "market" ? "Open at market" : "Place limit + hedge"}
      </PrimaryButton>
      {shared.length > 0 && (
        <div className="rounded-md border border-line px-3 py-2 text-text-2">
          {shared.map((i) => `Leg ${i + 1}`).join(" and ")} already {shared.length > 1 ? "have" : "has"} a position on {shared.length > 1 ? "their accounts" : "its account"}. The venue nets it with this hedge; closing the hedge only reduces by the hedge's own size.
        </div>
      )}
      {previewError && <div className="text-down">{previewError}</div>}
      {problems.map((problem) => <div key={problem} className="text-down">{problem}</div>)}

      {preview && (
        <div className="border-t border-line pt-2">
          {([0, 1] as const).map((i) => {
            const instrument = i === 0 ? inst0 : inst1;
            const isMaker = mode === "limit" && i === maker;
            const sweep = preview.sweep[i];
            return (
              <div key={i} className="mb-1.5">
                <div className="flex justify-between">
                  <span className={pair[i].side === "buy" ? "text-up" : "text-down"}>{pair[i].side === "buy" ? "Long" : "Short"} {num(preview.qty[i])} {instrument?.base}</span>
                  <span>{usd(preview.notional[i])}</span>
                </div>
                <div className="flex justify-between text-muted">
                  <span>{instrument?.venue}</span>
                  <span>
                    {isMaker ? `rests @ ${price(limitPrice, instrument?.tickSize)}` : sweep?.avgPrice && Number(sweep.avgPrice) > 0 ? `≈ ${price(sweep.avgPrice, instrument?.tickSize)} · ${bpsText(sweep.slippageBps)}` : "—"}
                    {sweep?.short && !isMaker ? <span className="text-down"> · book too thin</span> : null}
                  </span>
                </div>
              </div>
            );
          })}
          {matching?.match === "base" && <Row label="Entry spread" hint="Leg 2 over leg 1 at the entry prices">{bpsText(preview.spreadBps)}</Row>}
          <Row label="Size mismatch" hint="Leg 2 notional minus leg 1 notional after rounding both legs to their venue steps: the directional exposure the hedge keeps">{usd(preview.mismatch)}</Row>
          <Row label="Crossing cost" hint="Slippage from the touch for the taker legs, weighted by notional (fees not included)">{bpsText(costBps)}</Row>
          <Row label="Net funding" hint="Per year at current rates; positive earns">
            <span className={tone(preview.netFundingApr)}>{aprText(preview.netFundingApr)}</span>
          </Row>
          <Row label="Breakeven" hint="Days of current net funding to repay the crossing cost">{breakeven === undefined ? "—" : breakeven < 1 ? "< 1 day" : `${breakeven.toFixed(1)} days`}</Row>
        </div>
      )}
    </div>
  );
}

function sanitize(value: string): string {
  const cleaned = value.replace(/,/g, ".").replace(/[^\d.]/g, "");
  const [whole, ...rest] = cleaned.split(".");
  return rest.length ? `${whole}.${rest.join("")}` : whole ?? "";
}
