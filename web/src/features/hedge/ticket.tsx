import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { hedgeApi, type OpenRequest, type Preview } from "@/api/hedge";
import type { Account, Instrument } from "@/api/types";
import { num, price, snapToStep, usd, venueLabel } from "@/lib/format";
import { accountName, rulesFor } from "@/lib/routes";
import { useBook, useInstrument, type AccountLive } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { PrimaryButton, Row, Tabs, cx } from "@/ui/primitives";
import { useToast } from "@/ui/toast";
import { AccountLabel, AccountMenu, NoAccount, useAccountChoices } from "../account-picker";
import { MarginPool } from "../account-summary";
import { LeverageDialog } from "../order-ticket";
import { useHedgerText, useT } from "@/i18n";
import { aprText, bpsText, breakevenDays, legAccount, legAvailable, legLeverage, legPosition, makePrice, marginCheck, takePrice, tone, type LegChoice, type Pair } from "./model";

type Mode = "market" | "limit";
type Unit = "base" | "quote";

/**
 * The open ticket, top to bottom: where each leg trades (account and the
 * leverage its margin is estimated with), how (market both, or rest one leg
 * and hedge every fill at market on the other), how much, what it would do,
 * and the money behind it.
 */
export function HedgeTicket({ pair, matching, onAccount }: {
  pair: Pair;
  matching: { match: "base" | "notional"; ratio: number } | undefined;
  onAccount: (index: 0 | 1, accountId: string) => void;
}) {
  const t = useT();
  const text = useHedgerText();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { accounts, routes, live } = useWorkspace();
  const inst0 = useInstrument(pair[0].inst);
  const inst1 = useInstrument(pair[1].inst);
  const instruments = [inst0, inst1] as const;
  const book0 = useBook(pair[0].inst);
  const book1 = useBook(pair[1].inst);
  const account0 = legAccount(accounts, routes, inst0, pair[0].accountId);
  const account1 = legAccount(accounts, routes, inst1, pair[1].accountId);
  const legAccounts = [account0, account1] as const;
  const lives: readonly [AccountLive, AccountLive] = [account0 ? live[account0.id] ?? {} : {}, account1 ? live[account1.id] ?? {} : {}];
  // Both legs on one broker account trade from one margin pool.
  const sharedPool = !!account0 && account0.id === account1?.id && account0.kind === "broker";

  // Leverage per leg: set here (applied on the venue), else the position's.
  const [chosen, setChosen] = useState<[number | undefined, number | undefined]>([undefined, undefined]);
  useEffect(() => setChosen([undefined, undefined]), [pair[0].inst, pair[1].inst, pair[0].accountId, pair[1].accountId]);
  const positions = [legPosition(lives[0].positions, pair[0].inst), legPosition(lives[1].positions, pair[1].inst)] as const;
  const leverage = ([0, 1] as const).map((i) => legLeverage(instruments[i]?.product === "spot", positions[i], chosen[i]));

  const [mode, setMode] = useState<Mode>("market");
  const [unit, setUnit] = useState<Unit>("quote");
  const [size, setSize] = useState("");
  const [maker, setMaker] = useState<0 | 1>(0);
  const [limitPrice, setLimitPrice] = useState("");
  // Post only belongs to the resting order, chosen per resting leg: picking
  // the other leg to rest does not carry the choice over.
  const [postOnlyByLeg, setPostOnlyByLeg] = useState<[boolean, boolean]>([true, true]);
  const postOnly = postOnlyByLeg[maker];
  const setPostOnly = (value: boolean) => setPostOnlyByLeg((current) => (maker === 0 ? [value, current[1]] : [current[0], value]));
  const [policy, setPolicy] = useState<"unwind" | "hold">("unwind");
  const [label, setLabel] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewError, setPreviewError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const base0 = inst0?.base;
  const base1 = inst1?.base;
  const makerInst = instruments[maker];
  const makerBook = maker === 0 ? book0 : book1;
  // The resting leg's prices follow its account's rules (a broker can be finer).
  const makerTick = makerInst ? rulesFor(makerInst, legAccounts[maker] ? routes[legAccounts[maker]!.id] : undefined).tickSize : undefined;
  const missing = ([0, 1] as const).filter((i) => !legAccounts[i]);
  const ready = !!inst0 && !!inst1 && missing.length === 0;

  useEffect(() => { setLimitPrice(""); }, [maker, pair[0].inst, pair[1].inst]);

  // Previews size with each leg's account rules once chosen.
  const id0 = account0?.id ?? "";
  const id1 = account1?.id ?? "";
  const legKey = `${pair[0].inst}|${id0}|${pair[0].side}|${pair[1].inst}|${id1}|${pair[1].side}`;
  const draft = useMemo<OpenRequest | null>(() => {
    if (!base0 || !base1 || !(Number(size) > 0)) return null;
    if (mode === "limit" && !(Number(limitPrice) > 0)) return null;
    return {
      legs: [
        { accountId: id0, inst: pair[0].inst, side: pair[0].side },
        { accountId: id1, inst: pair[1].inst, side: pair[1].side },
      ],
      size, unit, mode, makerLeg: maker, limitPrice: mode === "limit" ? limitPrice : undefined,
      tif: mode === "limit" ? (postOnly ? "post_only" : "gtc") : undefined,
      policy, label: label.trim() || undefined,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base0, base1, size, unit, mode, maker, limitPrice, postOnly, policy, label, legKey]);
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

  const available = [legAvailable(account0, inst0, lives[0], pair[0].side), legAvailable(account1, inst1, lives[1], pair[1].side)] as const;
  const margin = preview && ready
    ? marginCheck(([0, 1] as const).map((i) => ({
      accountId: legAccounts[i]!.id, shared: sharedPool, notional: Number(preview.notional[i]), leverage: leverage[i]!.value, available: available[i]?.amount,
    })))
    : undefined;
  // Venues net positions per account and instrument: a hedge leg on an
  // instrument the account already holds shares that position.
  const netted = ([0, 1] as const).filter((i) => !!positions[i]);
  const problems = [...(preview?.problems ?? []).map(text), ...(margin?.short ? [margin.short] : [])];
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
      toast.push({ kind: "info", title: t(mode === "market" ? "hticket.opened" : "hticket.working"), detail: group.label });
      setSize("");
      void queryClient.invalidateQueries({ queryKey: ["hedgeGroup"] });
    } catch (error) {
      toast.push({ kind: "error", title: t("hticket.notOpened"), detail: (error as Error).message });
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
    if (value) setLimitPrice(snapToStep(value.toFixed(12), makerTick ?? makerInst.tickSize));
  };

  const units = { base: inst0?.base ?? "Base", quote: inst0?.quote ?? "Quote" };

  return (
    <div className="flex flex-col bg-surface">
      <div className="flex flex-col gap-3 p-3">
        <Tabs value={mode} onChange={setMode} options={[{ value: "market", label: t("hticket.marketBoth") }, { value: "limit", label: t("hticket.limitHedge") }]} className="[&>button]:flex-1" />

        {mode === "limit" && (
          <>
            <div>
              <div className="grid h-8 grid-cols-2 rounded-lg bg-raised p-0.5">
                {([0, 1] as const).map((leg) => (
                  <button key={leg} onClick={() => setMaker(leg)} className={cx("truncate rounded-md px-2", maker === leg ? "bg-line-strong/60 text-text" : "text-muted")}>
                    {t("hticket.restLeg", { n: leg + 1, venue: venueLabel(instruments[leg]?.venue ?? "") })}
                  </button>
                ))}
              </div>
              <p className="mt-1 text-muted">
                {t("hticket.restExplain", { maker: venueLabel(instruments[maker]?.venue ?? ""), other: venueLabel(instruments[1 - maker]?.venue ?? "") })}
              </p>
            </div>
            <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
              <span className="text-muted">{t("common.price")}</span>
              <input inputMode="decimal" value={limitPrice} onChange={(event) => setLimitPrice(sanitize(event.target.value))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
              <span className="text-muted">{makerInst?.quote}</span>
            </label>
            <div className="-mt-1 flex gap-1">
              {(["join", "mid", "touch"] as const).map((kind) => (
                <button key={kind} onClick={() => setPriceAt(kind)} className="h-6 flex-1 rounded-md bg-raised text-text-2 hover:text-text" title={t(({ join: "hticket.joinHint", mid: "hticket.midHint", touch: "hticket.touchHint" } as const)[kind])}>
                  {kind === "mid" ? t("ticket.mid") : t((kind === "join") === (pair[maker].side === "buy") ? "hticket.join.buy" : "hticket.join.sell")}
                </button>
              ))}
            </div>
            <label className="flex items-center gap-2" title={t("hticket.postOnlyHint")}>
              <input type="checkbox" checked={postOnly} onChange={(event) => setPostOnly(event.target.checked)} className="accent-[#50d2c1]" />
              {t("hticket.postOnly")}
              <span className="text-muted">{t("hticket.postOnlyScope", { venue: venueLabel(instruments[maker]?.venue ?? "") })}</span>
            </label>
          </>
        )}

        <div>
          <label className="flex h-10 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
            <span className="text-muted">{t("common.size")}</span>
            <input inputMode="decimal" value={size} onChange={(event) => setSize(sanitize(event.target.value))} className="min-w-0 flex-1 bg-transparent text-right outline-none" />
            <select value={unit} onChange={(event) => { setUnit(event.target.value as Unit); setSize(""); }} className="bg-transparent text-text outline-none" aria-label={t("ticket.sizeUnit")}>
              <option value="quote">{units.quote}</option>
              <option value="base">{units.base}</option>
            </select>
          </label>
          <p className="mt-1 text-muted">{unit === "quote" ? t("hticket.sizeQuote", { quote: units.quote }) : t("hticket.sizeBase", { base: units.base })}{matching?.match === "notional" ? t("hticket.matchedByValue") : ""}</p>
        </div>

        {/* The legs of this order: where each trades, its leverage, and what it would do. One grid so both legs align. */}
        <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-1 gap-y-0.5">
          {([0, 1] as const).map((i) => {
            const instrument = instruments[i];
            const isMaker = mode === "limit" && i === maker;
            const sweep = preview?.sweep[i];
            return (
              <LegRow
                key={i}
                index={i}
                leg={pair[i]}
                instrument={instrument}
                account={legAccounts[i]}
                leverage={leverage[i]!}
                onAccount={(id) => onAccount(i, id)}
                onLeverage={(value) => setChosen((current) => (i === 0 ? [value, current[1]] : [current[0], value]))}
                outcome={preview ? {
                  qty: preview.qty[i],
                  notional: preview.notional[i],
                  detail: isMaker
                    ? t("hticket.restsAt", { price: price(limitPrice, preview.legs?.[i]?.tickSize || instrument?.tickSize) })
                    : sweep?.avgPrice && Number(sweep.avgPrice) > 0 ? `≈ ${price(sweep.avgPrice, instrument?.tickSize)} · ${bpsText(sweep.slippageBps)}` : "—",
                  thin: !!sweep?.short && !isMaker,
                } : undefined}
              />
            );
          })}
        </div>

        {preview && (
          <div>
            <div className="border-t border-line pt-1">
              {matching?.match === "base" && <Row label={t("pair.entrySpread")} hint={t("hticket.entrySpreadHint")}>{bpsText(preview.spreadBps)}</Row>}
              <Row label={t("hticket.mismatch")} hint={t("hticket.mismatchHint")}>{usd(preview.mismatch)}</Row>
              <Row label={t("hticket.crossing")} hint={t("hticket.crossingHint")}>{bpsText(costBps)}</Row>
              <Row label={t("pair.netFunding")} hint={t("hticket.netFundingHint")}><span className={tone(preview.netFundingApr)}>{aprText(preview.netFundingApr)}</span></Row>
              <Row label={t("hticket.breakeven")} hint={t("hticket.breakevenHint")}>{breakeven === undefined ? "—" : breakeven < 1 ? t("hticket.lessThanDay") : t("hticket.days", { n: breakeven.toFixed(1) })}</Row>
              {margin && (
                <Row label={t("hticket.marginRequired")} hint={t("hticket.marginHint")}>
                  <span className={margin.short ? "text-down" : ""}>{usd(margin.need[0]! + margin.need[1]!)}</span>
                  <span className="text-muted"> / {sharedPool ? usd(available[0]?.amount) : `${usd(available[0]?.amount)} · ${usd(available[1]?.amount)}`}</span>
                </Row>
              )}
            </div>
          </div>
        )}

        <PrimaryButton onClick={submit} disabled={!request || submitting || problems.length > 0 || !!previewError}>
          {!ready
            ? t("hticket.chooseAccountFor", { legs: missing.length === 2 ? t("hticket.legsAnd", { a: t("common.leg", { n: 1 }), b: t("common.leg", { n: 2 }) }) : t("common.leg", { n: missing[0]! + 1 }) })
            : submitting ? t("hticket.opening") : t(mode === "market" ? "hticket.openMarket" : "hticket.placeLimit")}
        </PrimaryButton>
        {previewError && <div className="text-down">{previewError}</div>}
        {problems.map((problem) => <div key={problem} className="text-down">{problem}</div>)}
        {netted.length > 0 && (
          <p className="text-text-2">
            {t("hticket.netted", { legs: netted.length === 2 ? t("hticket.legsAnd", { a: t("common.leg", { n: 1 }), b: t("common.leg", { n: 2 }) }) : t("common.leg", { n: netted[0]! + 1 }), count: netted.length })}
          </p>
        )}

        <div className="flex items-center justify-between gap-2">
          <span className="dotted text-muted" title={t("hticket.ifLegFailsHint")}>{t("hticket.ifLegFails")}</span>
          <select value={policy} onChange={(event) => setPolicy(event.target.value as "unwind" | "hold")} className="bg-transparent text-right outline-none">
            <option value="unwind">{t("hticket.policyUnwind")}</option>
            <option value="hold">{t("hticket.policyHold")}</option>
          </select>
        </div>
        <label className="flex h-9 items-center gap-2 rounded-lg border border-line px-3 focus-within:border-line-strong">
          <span className="text-muted">{t("common.label")}</span>
          <input value={label} maxLength={80} onChange={(event) => setLabel(event.target.value)} placeholder={t("common.optional")} className="min-w-0 flex-1 bg-transparent text-right outline-none placeholder:text-faint" />
        </label>
      </div>

      <Funds accounts={legAccounts} lives={lives} available={available} sharedPool={sharedPool} />
    </div>
  );
}

/** The account cell's box: identical for a menu, a fixed account and no account. */
const cell = "flex h-8 w-full min-w-0 items-center gap-2 rounded-lg px-2";

/**
 * One leg of the order, on the ticket's three-column grid so both legs line
 * up: side and leg number · the account it trades on (a menu of the accounts
 * that can) · the leverage its margin is estimated with. Once there is a
 * size, a second line shows what the leg would do: quantity, expected price
 * and slippage, value.
 */
function LegRow({ index, leg, instrument, account, leverage, onAccount, onLeverage, outcome }: {
  index: 0 | 1;
  leg: LegChoice;
  instrument: Instrument | undefined;
  account: Account | undefined;
  leverage: { value: number; source: "set" | "position" | "default" };
  onAccount: (id: string) => void;
  onLeverage: (value: number) => void;
  outcome: { qty: string; notional: string; detail: string; thin: boolean } | undefined;
}) {
  const t = useT();
  const { candidates, unconnectedBrokers } = useAccountChoices(instrument);
  const [open, setOpen] = useState(false);
  const perp = instrument?.product === "perp";
  const long = leg.side === "buy";
  return (
    <>
      <span className="flex h-8 w-[118px] min-w-0 items-center gap-1.5 whitespace-nowrap">
        <span className={cx("w-9 shrink-0", long ? "text-up" : "text-down")}>{t(long ? "common.long" : "common.short")}</span>
        <span className="truncate text-muted" title={t("common.leg", { n: index + 1 })}>{venueLabel(instrument?.venue ?? leg.inst.split(":")[0] ?? "")}</span>
      </span>
      {/* Every account cell has the same box (padding, chevron slot), menu or not, so both legs line up. */}
      {instrument && candidates.length === 0 ? (
        <span className={cell}><NoAccount instrument={instrument} unconnectedBrokers={unconnectedBrokers} compact /></span>
      ) : account && candidates.length === 1 && unconnectedBrokers.length === 0 ? (
        // The only account that trades this market: nothing to choose.
        <span className={cell} title={t("accounts.soleAccount")}>
          <AccountLabel account={account} />
          <span className="ml-auto w-3 shrink-0" />
        </span>
      ) : (
        <AccountMenu
          instrument={instrument} account={account} candidates={candidates} unconnectedBrokers={unconnectedBrokers} onChoose={onAccount}
          ariaLabel={t("hticket.legAccount", { n: index + 1 })}
          className={cx(cell, "text-left hover:bg-raised")}
        >
          {(menuOpen) => (
            <>
              {account ? <AccountLabel account={account} /> : <span className="truncate text-accent">{t("accounts.choose")}</span>}
              <span className="ml-auto w-3 shrink-0 text-right text-muted">{menuOpen ? "▴" : "▾"}</span>
            </>
          )}
        </AccountMenu>
      )}
      <span className="flex w-10 justify-end">
        {perp && account ? (
          <button
            onClick={() => setOpen(true)}
            aria-label={t("hticket.legLeverage", { n: index + 1 })}
            title={t(`hticket.leverage.${leverage.source}`)}
            className={cx("h-7 w-10 rounded-md bg-raised hover:bg-line-strong/40", leverage.source === "default" ? "text-muted" : "text-text")}
          >
            {num(leverage.value, 0)}x
          </button>
        ) : null}
      </span>
      {outcome && (
        <>
          <span className={cx("pl-[42px] whitespace-nowrap", long ? "text-up" : "text-down")}>{num(outcome.qty)} {instrument?.base}</span>
          <span className="truncate px-2 text-muted">
            {outcome.thin && <span className="text-down" title={t("hticket.thinHint")}>{t("hticket.thin")} · </span>}
            {outcome.detail}
          </span>
          <span className="text-right whitespace-nowrap">{usd(outcome.notional, 0)}</span>
        </>
      )}
      {perp && account && instrument && (
        <LeverageDialog open={open} onOpenChange={setOpen} instrument={instrument} accountId={account.id} current={Math.round(leverage.value)} onApplied={onLeverage} />
      )}
    </>
  );
}

/**
 * The money behind the legs: one shared pool (equity, margin rates and the
 * venues it backs) when both legs trade from one broker account, else each
 * leg's account available.
 */
function Funds({ accounts, lives, available, sharedPool }: {
  accounts: readonly [Account | undefined, Account | undefined];
  lives: readonly [AccountLive, AccountLive];
  available: readonly [{ amount: number; asset: string } | undefined, { amount: number; asset: string } | undefined];
  sharedPool: boolean;
}) {
  const t = useT();
  if (!accounts[0] && !accounts[1]) return null;
  return (
    <div className="border-t border-line p-3">
      {sharedPool ? (
        <>
          <div className="mb-1 text-text">{accountName(accounts[0]!)} <span className="text-muted">{t("hticket.onePool")}</span></div>
          <MarginPool margin={lives[0].margin} />
        </>
      ) : (
        ([0, 1] as const).map((i) => {
          const account = accounts[i];
          if (!account) return null;
          const shown = i === 1 && accounts[0]?.id === account.id;
          if (shown) return null;
          return (
            <Row key={i} label={accounts[0]?.id === accounts[1]?.id ? accountName(account) : t("hticket.legAccountLabel", { n: i + 1, account: accountName(account) })} hint={t(account.kind === "broker" ? "hticket.poolAvailableHint" : "hticket.nativeAvailableHint")}>
              {available[i] ? `${num(available[i]!.amount, 2)} ${available[i]!.asset}` : "—"}
            </Row>
          );
        })
      )}
    </div>
  );
}

function sanitize(value: string): string {
  const cleaned = value.replace(/,/g, ".").replace(/[^\d.]/g, "");
  const [whole, ...rest] = cleaned.split(".");
  return rest.length ? `${whole}.${rest.join("")}` : whole ?? "";
}
