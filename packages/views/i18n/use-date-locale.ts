"use client";

import { useMemo } from "react";
import type { Locale } from "date-fns";
import { enUS, zhCN, ja, ko } from "date-fns/locale";
import { matchLocale, toBcp47, type SupportedLocale } from "@multica/core/i18n";
import { useT } from "./use-t";

const DATE_FNS_LOCALE: Record<SupportedLocale, Locale> = {
  en: enUS,
  "zh-Hans": zhCN,
  ja,
  ko,
};

/**
 * Resolve the active i18next language to the two locale shapes date display
 * needs: a BCP-47 tag for `formatDateOnly` / Intl, and a date-fns `Locale`
 * object for the Calendar's month/weekday rendering.
 *
 * Memoized on `i18n.language` so the returned object (and the date-fns Locale
 * inside it) is referentially stable across renders — passing a fresh Locale
 * to <Calendar> on every render would force it to re-render needlessly.
 */
export function useDateLocale(): { locale: string; dateFnsLocale: Locale } {
  const { i18n } = useT();
  const language = i18n.language;
  return useMemo(() => {
    const norm = matchLocale([language]);
    return { locale: toBcp47(norm), dateFnsLocale: DATE_FNS_LOCALE[norm] };
  }, [language]);
}
