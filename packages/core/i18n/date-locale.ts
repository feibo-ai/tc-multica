import type { SupportedLocale } from "./types";

// Map a SupportedLocale to a BCP-47 tag suitable for Intl.DateTimeFormat /
// Date.prototype.toLocaleDateString. Pure function — no React / DOM / date-fns
// imports, so it stays shareable with mobile.
const BCP47_BY_LOCALE: Record<SupportedLocale, string> = {
  en: "en-US",
  "zh-Hans": "zh-CN",
  ja: "ja-JP",
  ko: "ko-KR",
};

/** Resolve a SupportedLocale to the BCP-47 tag used for date formatting. */
export function toBcp47(locale: SupportedLocale): string {
  return BCP47_BY_LOCALE[locale];
}
