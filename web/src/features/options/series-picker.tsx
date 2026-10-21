import * as Popover from "@radix-ui/react-popover";
import { useMemo, useState } from "react";

import type { OptionSeries } from "@/api/options";
import { num, venueLabel } from "@/lib/format";
import { cx } from "@/ui/primitives";
import { useT } from "@/i18n";

/**
 * The options market picker: venue first, then the venue's underlyings with
 * the settlement each is listed in (coin-margined or USD-margined). Venues
 * differ in index, settlement and margin, so a series never mixes venues and
 * the venue is the first choice, not a suffix.
 */
export function SeriesPicker({ list, current, onChange }: {
  list: OptionSeries[];
  current: OptionSeries | undefined;
  onChange: (series: string) => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const venues = useMemo(() => [...new Set(list.map((item) => item.venue))], [list]);
  const [venueChoice, setVenue] = useState<string>("");
  const venue = venueChoice || current?.venue || venues[0] || "";
  // The venue's underlyings, each with its coin- and USD-margined series.
  const underlyings = useMemo(() => {
    const out = new Map<string, OptionSeries[]>();
    for (const item of list) if (item.venue === venue) out.set(item.underlying, [...(out.get(item.underlying) ?? []), item]);
    return [...out];
  }, [list, venue]);
  const settle = (item: OptionSeries) => (item.inverse ? t("options.coinMargined") : t("options.usdMargined", { asset: item.premiumAsset }));

  return (
    <Popover.Root open={open} onOpenChange={(next) => { setOpen(next); if (next) setVenue(""); }}>
      <Popover.Trigger className="flex shrink-0 items-center gap-2 text-left">
        <span className="text-lg">{current ? `${current.underlying} ${t("options.title")}` : t("options.title")}</span>
        <span className="text-muted">{open ? "▴" : "▾"}</span>
        {current && <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-accent">{venueLabel(current.venue)} · {settle(current)}</span>}
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={8} className="z-40 flex w-[min(520px,calc(100vw-16px))] rounded-[10px] border border-line bg-overlay shadow-2xl">
          <div className="flex w-36 shrink-0 flex-col gap-0.5 border-r border-line p-2">
            <div className="px-2 pb-1 text-xs text-muted">{t("picker.venue")}</div>
            {venues.map((item) => (
              <button key={item} onMouseEnter={() => setVenue(item)} onClick={() => setVenue(item)}
                className={cx("rounded-md px-2 py-1.5 text-left", item === venue ? "bg-raised text-text" : "text-text-2 hover:text-text")}>
                {venueLabel(item)}
                <span className="ml-1.5 text-xs text-muted">{list.filter((series) => series.venue === item).length}</span>
              </button>
            ))}
          </div>
          <div className="min-w-0 flex-1 p-2">
            <div className="grid grid-cols-[64px_1fr_72px_64px] gap-2 px-2 pb-1 text-xs text-muted">
              <span>{t("options.underlying")}</span><span>{t("options.settlement")}</span><span className="text-right">{t("options.index")}</span><span className="text-right">{t("options.atmIv")}</span>
            </div>
            <div className="max-h-[50vh] overflow-y-auto">
              {underlyings.map(([underlying, series]) => series.map((item, index) => (
                <button key={item.series} onClick={() => { onChange(item.series); setOpen(false); }}
                  className={cx("grid w-full grid-cols-[64px_1fr_72px_64px] items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-raised",
                    item.series === current?.series && "text-accent")}>
                  <span className={index ? "text-transparent" : ""}>{underlying}</span>
                  <span className="text-text-2">{settle(item)} <span className="text-xs text-muted">{t("options.contractsCount", { n: item.contracts })}</span></span>
                  <span className="text-right tabular-nums text-text-2">{item.index ? num(item.index, item.index >= 100 ? 0 : 2) : "—"}</span>
                  <span className="text-right tabular-nums">{item.frontAtmIv ? `${(item.frontAtmIv * 100).toFixed(1)}%` : "—"}</span>
                </button>
              )))}
            </div>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
