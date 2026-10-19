import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query";
import * as Popover from "@radix-ui/react-popover";
import { useEffect, useState, type FormEvent, type KeyboardEvent } from "react";

import { ApiError, api, setUnauthenticatedHandler } from "@/api/client";
import { realtime } from "@/api/realtime";
import { Blotter } from "@/features/blotter";
import { ChartPanel } from "@/features/chart";
import { AccountsMenu, ConnectDialog } from "@/features/connect";
import { MarketHeader } from "@/features/market-header";
import { BookPanel } from "@/features/order-book";
import { OrderTicket } from "@/features/order-ticket";
import { AccountSummary } from "@/features/account-summary";
import { HedgeApp } from "@/features/hedge/hedge-app";
import { navigate, usePath } from "@/state/route";
import { useConnection } from "@/state/hooks";
import { WorkspaceProvider } from "@/state/workspace";
import { Mark, Wordmark } from "@/ui/logo";
import { PrimaryButton, cx } from "@/ui/primitives";
import { ToastProvider } from "@/ui/toast";
import { I18nProvider, Interpolate, LANGS, useI18n, useT } from "@/i18n";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 5_000 } },
});

export function App() {
  return (
    <I18nProvider>
      <QueryClientProvider client={queryClient}>
        <ToastProvider>
          <Gate />
        </ToastProvider>
      </QueryClientProvider>
    </I18nProvider>
  );
}

function Gate() {
  const client = useQueryClient();
  const session = useQuery({ queryKey: ["session"], queryFn: api.session, staleTime: Infinity });
  useEffect(() => {
    setUnauthenticatedHandler(() => client.setQueryData(["session"], { authenticated: false }));
  }, [client]);
  const authenticated = session.data?.authenticated;
  useEffect(() => {
    if (authenticated) realtime.start();
    else realtime.stop();
  }, [authenticated]);

  if (session.isLoading) return null;
  if (!authenticated) return <Login />;
  return (
    <WorkspaceProvider>
      <Shell />
      <ConnectDialog />
    </WorkspaceProvider>
  );
}

function Shell() {
  const t = useT();
  const path = usePath();
  const apps = useQuery({ queryKey: ["apps"], queryFn: api.apps, staleTime: Infinity, retry: false });
  const hedge = path.startsWith("/hedge");
  if (hedge && apps.data?.hedge) {
    return (
      <div className="flex h-full flex-col gap-[3px]">
        <TopBar hedge />
        <HedgeApp />
        <StatusBar />
      </div>
    );
  }
  if (hedge && apps.data && !apps.data.hedge) {
    return (
      <div className="flex h-full flex-col gap-[3px]">
        <TopBar hedge={false} />
        <main className="flex flex-1 items-center justify-center bg-surface text-muted">{t("hedge.notDeployed")}</main>
      </div>
    );
  }
  return <Terminal hedgeEnabled={!!apps.data?.hedge} />;
}

function Login() {
  const t = useT();
  const client = useQueryClient();
  const [password, setPassword] = useState("");
  const [visible, setVisible] = useState(false);
  const [capsLock, setCapsLock] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(password);
      // Drop data cached under a previous session, but keep the session query
      // itself observed so Gate re-renders.
      client.removeQueries({ predicate: (query) => query.queryKey[0] !== "session" });
      client.setQueryData(["session"], { authenticated: true });
    } catch (e) {
      const status = e instanceof ApiError ? e.status : 0;
      setError(status === 401 ? t("login.incorrect") : status === 429 ? t("login.throttled") : (e as Error).message);
      setPassword("");
    } finally {
      setBusy(false);
    }
  };
  const keys = (event: KeyboardEvent<HTMLInputElement>) => setCapsLock(event.getModifierState("CapsLock"));
  return (
    <div className="flex h-full flex-col bg-surface">
      <main className="flex flex-1 items-center justify-center px-4">
        <form onSubmit={submit} className="w-full max-w-[320px]">
          <Mark size={40} />
          <h1 className="mt-5 text-lg leading-tight">{t("login.title")}</h1>
          <p className="mt-1.5 text-muted">{t("brand.tagline")}</p>

          <label htmlFor="password" className="mt-7 block text-muted">{t("login.password")}</label>
          <div className={cx("mt-1.5 flex h-10 items-center rounded-lg border bg-transparent pr-1 transition-colors", error ? "border-down/70" : "border-line focus-within:border-accent")}>
            <input
              id="password"
              type={visible ? "text" : "password"}
              autoFocus
              autoComplete="current-password"
              spellCheck={false}
              value={password}
              onChange={(e) => { setPassword(e.target.value); setError(""); }}
              onKeyDown={keys}
              onKeyUp={keys}
              aria-invalid={!!error}
              aria-describedby={error ? "login-error" : undefined}
              className="h-full min-w-0 flex-1 bg-transparent px-3 text-md outline-none focus-visible:outline-none"
            />
            <button
              type="button"
              onClick={() => setVisible(!visible)}
              aria-label={visible ? t("login.hide") : t("login.show")}
              className="flex size-8 items-center justify-center rounded-md text-muted hover:text-text"
            >
              <EyeGlyph open={!visible} />
            </button>
          </div>
          <div className="mt-2 min-h-4" aria-live="polite">
            {error ? (
              <span id="login-error" className="text-down">{error}</span>
            ) : capsLock ? (
              <span className="text-text-2">{t("login.capsLock")}</span>
            ) : null}
          </div>

          <PrimaryButton type="submit" className="mt-3" disabled={busy || !password}>{busy ? t("login.submitting") : t("login.submit")}</PrimaryButton>
        </form>
      </main>
      <footer className="px-4 pb-6 text-center text-muted">
        <Interpolate text={t("login.footer")} values={{ command: <code className="text-text-2">trader passwd</code> }} />
      </footer>
    </div>
  );
}

function EyeGlyph({ open }: { open: boolean }) {
  return (
    <svg viewBox="0 0 20 20" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" aria-hidden>
      <path d="M2.5 10s2.8-5 7.5-5 7.5 5 7.5 5-2.8 5-7.5 5-7.5-5-7.5-5z" />
      <circle cx="10" cy="10" r="2.2" />
      {!open && <path d="M3.5 16.5 16.5 3.5" />}
    </svg>
  );
}

/** The documentation site (GitHub Pages, built from docs/ by .github/workflows/docs.yml). */
const DOCS = "https://quantprocessing.github.io/keeluni/";
const MORE_LINKS = [
  { key: "nav.docs", href: DOCS },
  { key: "nav.api", href: DOCS + "api/" },
] as const;

/** Panels are flat, 5px-rounded tiles; the canvas shows through 3px gaps. */
const PANEL = "shrink-0 overflow-hidden rounded-md min-[1001px]:shrink";

function Terminal({ hedgeEnabled }: { hedgeEnabled: boolean }) {
  return (
    <div className="flex h-full flex-col gap-[3px]">
      <TopBar hedge={false} hedgeEnabled={hedgeEnabled} />

      {/* Desktop: chart+book | ticket on top, blotter below chart+book. */}
      <main className="grid min-h-0 flex-1 gap-[3px] px-[3px] max-[1000px]:flex max-[1000px]:flex-col max-[1000px]:overflow-y-auto min-[1001px]:grid-cols-[minmax(0,1fr)_300px_320px] min-[1001px]:grid-rows-[56px_minmax(0,1fr)_minmax(220px,35%)]">
        <div className={cx(PANEL, "min-[1001px]:col-span-2")}><MarketHeader /></div>
        <div className={cx(PANEL, "flex min-h-[420px] min-[1001px]:col-start-1 min-[1001px]:row-start-2")}><ChartPanel /></div>
        <div className={cx(PANEL, "flex min-h-[420px] max-[1000px]:h-[420px] min-[1001px]:col-start-2 min-[1001px]:row-start-2")}><BookPanel /></div>
        <aside className="flex flex-col gap-[3px] overflow-y-auto min-[1001px]:col-start-3 min-[1001px]:row-span-3 min-[1001px]:row-start-1 [&>*]:rounded-md">
          <OrderTicket />
          <AccountSummary />
        </aside>
        <div className={cx(PANEL, "flex min-h-[260px] min-[1001px]:col-span-2 min-[1001px]:row-start-3")}><Blotter /></div>
      </main>

      <StatusBar />
    </div>
  );
}

function NavLink({ href, active, children }: { href: string; active: boolean; children: string }) {
  return (
    <a
      href={href}
      aria-current={active ? "page" : undefined}
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
        event.preventDefault();
        navigate(href);
      }}
      className={cx("px-2.5 py-2 transition-colors hover:text-accent-hover", active ? "text-accent" : "text-text")}
    >
      {children}
    </a>
  );
}

function TopBar({ hedge, hedgeEnabled = true }: { hedge: boolean; hedgeEnabled?: boolean }) {
  const t = useT();
  return (
    <header className="flex h-14 shrink-0 items-center bg-surface px-4">
      <a href="/" aria-label="KeelUni" onClick={(event) => { event.preventDefault(); navigate("/"); }} className="mr-[18px] flex items-center"><Wordmark /></a>
      <nav aria-label={t("nav.main")} className="flex items-center gap-2">
        <NavLink href="/" active={!hedge}>{t("nav.trade")}</NavLink>
        {(hedge || hedgeEnabled) && <NavLink href="/hedge" active={hedge}>{t("nav.hedge")}</NavLink>}
        <MoreMenu />
      </nav>
      <div className="ml-auto flex items-center gap-2">
        <AccountsMenu />
        <SessionMenu />
      </div>
    </header>
  );
}

function MoreMenu() {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className={cx("group flex items-center gap-2 px-2.5 py-2 transition-colors hover:text-accent-hover", open ? "text-accent-hover" : "text-text")}>
        {t("nav.more")}
        <svg viewBox="0 0 14 9" width="14" height="9" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden className={cx("text-muted transition-transform group-hover:text-accent-hover", open && "rotate-180 text-accent-hover")}>
          <path d="m1 1.5 6 6 6-6" />
        </svg>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={2} className="z-40 min-w-[140px] rounded-[10px] border border-line bg-overlay py-1">
          {MORE_LINKS.map((link) => (
            <a
              key={link.key}
              href={link.href}
              target="_blank"
              rel="noopener noreferrer"
              onClick={() => setOpen(false)}
              className="block px-2.5 py-[5px] leading-6 text-text transition-colors hover:text-accent-hover focus-visible:text-accent-hover"
            >
              {t(link.key)}
            </a>
          ))}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function SessionMenu() {
  const { t, lang, setLang } = useI18n();
  const [open, setOpen] = useState(false);
  const logout = async () => {
    setOpen(false);
    await api.logout();
    queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== "session" });
    queryClient.setQueryData(["session"], { authenticated: false });
  };
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        aria-label={t("session.menu")}
        className={cx("flex size-8 items-center justify-center rounded-lg border border-line transition-colors hover:bg-overlay hover:text-text", open ? "bg-overlay text-text" : "text-muted")}
      >
        <svg viewBox="0 0 20 20" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" aria-hidden>
          <circle cx="10" cy="7" r="3" />
          <path d="M4 16.5c.8-3 3.2-4.5 6-4.5s5.2 1.5 6 4.5" />
        </svg>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} className="z-40 w-52 rounded-[10px] border border-line bg-overlay py-1">
          <div className="px-2.5 py-[5px] leading-6 text-muted">{t("session.signedIn")}</div>
          <div className="mx-2.5 h-px bg-line" />
          <div className="flex items-center gap-2 px-2.5 py-[5px] leading-6">
            <span className="text-muted">{t("session.language")}</span>
            <span className="ml-auto flex rounded-md bg-raised p-0.5">
              {LANGS.map((item) => (
                <button
                  key={item.value}
                  onClick={() => setLang(item.value)}
                  aria-pressed={lang === item.value}
                  className={cx("rounded px-2 leading-5", lang === item.value ? "bg-line-strong/60 text-text" : "text-muted hover:text-text")}
                >
                  {item.label}
                </button>
              ))}
            </span>
          </div>
          <div className="mx-2.5 h-px bg-line" />
          <button onClick={logout} className="block w-full px-2.5 py-[5px] text-left leading-6 text-text transition-colors hover:text-down focus-visible:text-down">
            {t("session.signOut")}
          </button>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function StatusBar() {
  const t = useT();
  const state = useConnection();
  return (
    <footer className="flex h-6 shrink-0 items-center px-2">
      <span className={cx("flex items-center gap-1.5 rounded-md border px-2 text-xs", state === "online" ? "border-up/60 text-up" : "border-down/60 text-down")}>
        <span className={cx("size-1.5 rounded-full", state === "online" ? "bg-up" : "bg-down")} />
        {state === "online" ? t("status.online") : state === "connecting" ? t("status.connecting") : t("status.reconnecting")}
      </span>
    </footer>
  );
}
