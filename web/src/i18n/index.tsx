// Interface language. Chinese is the default; English is the other choice.
// Strings live in two dictionaries with the same keys (en.ts defines them,
// zh.ts must provide every one: a missing translation fails the type check).
// Values are plain strings with {name} placeholders, or functions for text
// that needs logic (plurals, word order).
//
// The language is a module-level value as well as React state so formatting
// helpers outside components (lib/format.ts) follow it.

import { Fragment, createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { en, type Messages } from "./en";
import { hedgerText, type HedgerText } from "./hedger";
import { zh } from "./zh";

export type Lang = "zh" | "en";
export const LANGS: Array<{ value: Lang; label: string }> = [
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
];

const DICTIONARIES: Record<Lang, Messages> = { zh, en };
const KEY = "keeluni.lang";

function initial(): Lang {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === "zh" || saved === "en") return saved;
  } catch {
    // storage unavailable
  }
  return "zh";
}

let current: Lang = initial();

/** The interface language right now, for code outside React. */
export function currentLang(): Lang {
  return current;
}

/** BCP 47 tag of the language, for Intl and date formatting. */
export function locale(lang: Lang = current): string {
  return lang === "zh" ? "zh-CN" : "en-US";
}

type Params = Record<string, string | number>;
type Key = keyof Messages;

function format(template: string, params?: Params): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) => (name in params ? String(params[name]) : match));
}

/** Translates key in lang; for code outside React (it does not re-render). */
export function translate<K extends Key>(key: K, params?: Params, lang: Lang = current): string {
  const value = DICTIONARIES[lang][key] ?? en[key];
  return typeof value === "function" ? (value as (p: Params) => string)(params ?? {}) : format(value as string, params);
}

export type T = <K extends Key>(key: K, params?: Params) => string;

interface I18n {
  lang: Lang;
  setLang: (lang: Lang) => void;
  t: T;
}

const Context = createContext<I18n | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(current);
  const setLang = useCallback((next: Lang) => {
    current = next;
    setLangState(next);
    try {
      localStorage.setItem(KEY, next);
    } catch {
      // storage unavailable
    }
  }, []);
  useEffect(() => {
    document.documentElement.lang = locale(lang);
  }, [lang]);
  const value = useMemo<I18n>(() => ({ lang, setLang, t: (key, params) => translate(key, params, lang) }), [lang, setLang]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}

export function useI18n(): I18n {
  const value = useContext(Context);
  if (!value) throw new Error("I18nProvider missing");
  return value;
}

/** The translate function of the current language. */
export function useT(): T {
  return useI18n().t;
}

/**
 * Renders a translated template whose {name} placeholders are elements
 * (a link, a code span) rather than text.
 */
export function Interpolate({ text, values }: { text: string; values: Record<string, ReactNode> }) {
  const parts = text.split(/(\{\w+\})/);
  return (
    <>
      {parts.map((part, i) => {
        const name = /^\{(\w+)\}$/.exec(part)?.[1];
        return <Fragment key={i}>{name && name in values ? values[name] : part}</Fragment>;
      })}
    </>
  );
}

/** Renders a hedger text (code + params, or a venue's words) in the current language. */
export function useHedgerText(): (text: HedgerText | string | undefined | null) => string {
  const { lang } = useI18n();
  return (text) => hedgerText(text, lang);
}
