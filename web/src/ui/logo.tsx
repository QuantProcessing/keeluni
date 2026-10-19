import { useId } from "react";

import { cx } from "./primitives";

/**
 * KeelUni mark: a rounded teal tile with a U cut out of it — a hull in
 * cross-section whose bottom narrows into a keel. One figure for both halves
 * of the name: Uni (the U, every market in one place) and Keel (what keeps it
 * steady). The cut is a mask so the mark reads on any background, down to a
 * 16px favicon. Keep in sync with public/favicon.svg.
 */
export function Mark({ size = 24, className }: { size?: number; className?: string }) {
  const id = "keeluni-cut-" + useId().replace(/:/g, "");
  return (
    <svg viewBox="0 0 32 32" width={size} height={size} className={cx("shrink-0", className)} aria-hidden>
      <defs>
        <mask id={id}>
          <rect width="32" height="32" fill="#fff" />
          <path d="M10 8v8.5a6 6 0 0 0 12 0V8" fill="none" stroke="#000" strokeWidth="3" strokeLinecap="round" />
          <path d="M13.4 22.8h5.2L16 26.6z" fill="#000" stroke="#000" strokeWidth="1.6" strokeLinejoin="round" />
        </mask>
      </defs>
      <rect x="2" y="2" width="28" height="28" rx="8" fill="#50d2c1" mask={`url(#${id})`} />
    </svg>
  );
}

/** Mark + name, as used in the top bar and on the sign-in page: Keel, then Uni in accent. */
export function Wordmark({ size = 24, className }: { size?: number; className?: string }) {
  return (
    <span className={cx("flex items-center gap-2 text-text", className)}>
      <Mark size={size} />
      <span className="text-[17px] leading-none font-semibold tracking-[-0.01em]">
        Keel<span className="text-accent">Uni</span>
      </span>
    </span>
  );
}
