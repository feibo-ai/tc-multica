// @vitest-environment jsdom

import { describe, it, expect, beforeEach, vi } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

// Capture every queryKey so we can read which owner/agent the heatmap query was
// wired with (= the current selection). queryOptions() runs for real so the
// keys are production keys; the queryFn never runs (useQuery is mocked).
const queryKeys = vi.hoisted(() => [] as unknown[][]);
vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: (opts: { queryKey: unknown[] }) => {
      queryKeys.push(opts.queryKey);
      return { data: undefined, isLoading: false };
    },
  };
});

// The panel renders each person's name in its own span; the avatar pulls in
// workspace-path / actor-name hooks we don't exercise here, so stub it out.
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

// estimateCost (inside aggregateOwnerAmbient + the heatmap) reads the
// custom-pricing store; an empty store falls through to the built-in table.
vi.mock("@multica/core/runtimes/custom-pricing-store", () => {
  const state = { pricings: {} as Record<string, unknown> };
  const useCustomPricingStore = Object.assign(
    (sel?: (s: typeof state) => unknown) => (sel ? sel(state) : state),
    { getState: () => state },
  );
  return { useCustomPricingStore, getCustomPricing: () => undefined };
});

import { UserUsagePanel, AgentUsagePanel } from "./dashboard-page";

// Heatmap keys: [..., "ambient-daily" | "agent-daily", id, days, tz]. Return the
// id (the element right after the tag) of the most recent matching key.
function lastSelectedId(tag: string): unknown {
  const keys = queryKeys.filter((k) => k.includes(tag));
  const k = keys[keys.length - 1];
  return k ? k[k.indexOf(tag) + 1] : undefined;
}

// u-2 (opus, 1M tokens ≈ $5) outspends u-1 (haiku, tiny) and the unattributed
// opus row, so u-2 ranks first and is the default selection.
const AMBIENT_ROWS = [
  { owner_id: "u-1", model: "claude-haiku-4-5", input_tokens: 1000, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
  { owner_id: "u-2", model: "claude-opus-4-7", input_tokens: 1_000_000, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
  { owner_id: "", model: "claude-opus-4-7", input_tokens: 500, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
];
const MEMBERS = [
  { user_id: "u-1", name: "Alice" },
  { user_id: "u-2", name: "Bob" },
];

describe("UserUsagePanel", () => {
  beforeEach(() => {
    queryKeys.length = 0;
    cleanup();
  });

  it("renders the ambient leaderboard including an unattributed row", () => {
    renderWithI18n(
      <UserUsagePanel wsId="ws-1" viewTZ="UTC" rows={AMBIENT_ROWS} members={MEMBERS} />,
    );
    expect(screen.getByText("Bob")).toBeTruthy();
    expect(screen.getByText("Alice")).toBeTruthy();
    expect(screen.getByText("Unattributed")).toBeTruthy();
  });

  it("defaults the heatmap to the top spender (Bob = u-2)", () => {
    renderWithI18n(
      <UserUsagePanel wsId="ws-1" viewTZ="UTC" rows={AMBIENT_ROWS} members={MEMBERS} />,
    );
    expect(lastSelectedId("ambient-daily")).toBe("u-2");
  });

  it("re-fires the heatmap with owner_id \"\" when the unattributed row is clicked", () => {
    renderWithI18n(
      <UserUsagePanel wsId="ws-1" viewTZ="UTC" rows={AMBIENT_ROWS} members={MEMBERS} />,
    );
    fireEvent.click(screen.getByText("Unattributed"));
    // "" is a real selection (the !!-guard dead-end): the query must run for it.
    expect(lastSelectedId("ambient-daily")).toBe("");
  });

  it("shows the empty state and the heatmap hint when there are no rows", () => {
    renderWithI18n(
      <UserUsagePanel wsId="ws-1" viewTZ="UTC" rows={[]} members={MEMBERS} />,
    );
    // Empty list → no_data on the left, and the heatmap shows its select hint
    // (effective owner is null → the daily query stays disabled).
    expect(screen.getByText("No local CLI usage in this window.")).toBeTruthy();
    expect(
      screen.getByText("Select a row to see its 26-week activity"),
    ).toBeTruthy();
  });

  it("renders a per-tool breakdown for the selected owner, summing to 100%", () => {
    // u-mix is the top spender (claude-opus 1M ≈ $5 + gpt-5.5 1M ≈ $5 = ~$10),
    // so it's the default selection. Equal token volume across the two
    // families → Claude Code 50% / Codex 50%.
    const rows = [
      { owner_id: "u-mix", model: "claude-opus-4-7", input_tokens: 1_000_000, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
      { owner_id: "u-mix", model: "gpt-5.5", input_tokens: 1_000_000, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
      { owner_id: "u-1", model: "claude-haiku-4-5", input_tokens: 1000, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
    ];
    renderWithI18n(
      <UserUsagePanel
        wsId="ws-1"
        viewTZ="UTC"
        rows={rows}
        members={[{ user_id: "u-mix", name: "Mix" }, { user_id: "u-1", name: "Alice" }]}
      />,
    );
    // Both tool rows render with their label …
    expect(screen.getByText("Claude Code")).toBeTruthy();
    expect(screen.getByText("Codex")).toBeTruthy();
    // … and the two 50% cells appear (one per tool, summing to 100).
    const pcts = screen.getAllByText("50%");
    expect(pcts).toHaveLength(2);
    // A model family the owner didn't use must not show up.
    expect(screen.queryByText("Other")).toBeNull();
  });

  it("renders a single tool at 100% when the owner used only one family", () => {
    const rows = [
      { owner_id: "u-solo", model: "claude-opus-4-7", input_tokens: 1_000_000, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
    ];
    renderWithI18n(
      <UserUsagePanel
        wsId="ws-1"
        viewTZ="UTC"
        rows={rows}
        members={[{ user_id: "u-solo", name: "Solo" }]}
      />,
    );
    expect(screen.getByText("Claude Code")).toBeTruthy();
    expect(screen.getByText("100%")).toBeTruthy();
    expect(screen.queryByText("Codex")).toBeNull();
  });
});

const AGENT_ROWS = [
  { agentId: "a-1", tokens: 100, cost: 5, seconds: 0, taskCount: 1 },
  { agentId: "a-2", tokens: 50, cost: 1, seconds: 0, taskCount: 1 },
];
const AGENTS = [
  { id: "a-1", name: "Agent One" },
  { id: "a-2", name: "Agent Two" },
];

describe("AgentUsagePanel", () => {
  beforeEach(() => {
    queryKeys.length = 0;
    cleanup();
  });

  it("renders the agent leaderboard and defaults the heatmap to the first row", () => {
    renderWithI18n(
      <AgentUsagePanel
        wsId="ws-1"
        viewTZ="UTC"
        rows={AGENT_ROWS}
        agents={AGENTS}
        lessThanMinuteLabel="<1m"
      />,
    );
    expect(screen.getByText("Agent One")).toBeTruthy();
    expect(lastSelectedId("agent-daily")).toBe("a-1");
  });

  it("switches the heatmap selection when another agent row is clicked", () => {
    renderWithI18n(
      <AgentUsagePanel
        wsId="ws-1"
        viewTZ="UTC"
        rows={AGENT_ROWS}
        agents={AGENTS}
        lessThanMinuteLabel="<1m"
      />,
    );
    fireEvent.click(screen.getByText("Agent Two"));
    expect(lastSelectedId("agent-daily")).toBe("a-2");
  });
});
