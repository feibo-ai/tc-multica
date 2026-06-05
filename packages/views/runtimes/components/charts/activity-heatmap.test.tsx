// @vitest-environment jsdom

import type { ReactNode } from "react";
import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enRuntimes from "../../../locales/en/runtimes.json";
import { ActivityHeatmap, type HeatmapUsage } from "./activity-heatmap";

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };

// estimateCost reads the custom-pricing store; an empty store falls through to
// the built-in MODEL_PRICING table, so claude-sonnet-4-6 still prices. The
// store hook must be both callable and expose getState() (real Zustand shape).
vi.mock("@multica/core/runtimes/custom-pricing-store", () => {
  const state = { pricings: {} as Record<string, unknown> };
  const useCustomPricingStore = Object.assign(
    (sel?: (s: typeof state) => unknown) => (sel ? sel(state) : state),
    { getState: () => state },
  );
  return { useCustomPricingStore, getCustomPricing: () => undefined };
});

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function titlesOf(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll("title")).map(
    (t) => t.textContent ?? "",
  );
}

describe("ActivityHeatmap metric parametrization (R1 regression guard)", () => {
  // Seed on "today" in UTC so the row lands on the last rendered cell — the
  // grid spans the trailing 26 weeks up to today in the viewer's tz.
  const today = new Date().toISOString().slice(0, 10);
  const rows: HeatmapUsage[] = [
    {
      date: today,
      model: "claude-sonnet-4-6",
      input_tokens: 1_000_000,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
    },
  ];

  it("default metric colours by cost and renders money ($) tooltips", () => {
    const { container } = render(
      <Wrapper>
        <ActivityHeatmap usage={rows} tz="UTC" />
      </Wrapper>,
    );
    // 1M input tokens × $3/M (claude-sonnet-4-6) = $3.00.
    expect(titlesOf(container).some((s) => s.includes("$3.00"))).toBe(true);
    expect(container.textContent).toContain("Last 26 weeks");
  });

  it("tokens metric colours by token count and never renders a $ tooltip", () => {
    const { container } = render(
      <Wrapper>
        <ActivityHeatmap usage={rows} tz="UTC" metric="tokens" />
      </Wrapper>,
    );
    const titles = titlesOf(container);
    // 1,000,000 tokens → compact "1M", and crucially NO money string — the SVG
    // <title> is the spot the cost "$" most easily survives a half-done rename.
    expect(titles.some((s) => s.includes("1M"))).toBe(true);
    expect(titles.every((s) => !s.includes("$"))).toBe(true);
  });
});
