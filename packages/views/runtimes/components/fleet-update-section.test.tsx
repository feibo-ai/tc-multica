// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  AgentRuntime,
  FleetAuditRow,
  FleetSelfCheckResult,
  MemberRole,
} from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import enAgents from "../../locales/en/agents.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes, agents: enAgents },
};

// ---------------------------------------------------------------------------
// Mock surface. The component pulls role, lagging ids, runtimes, members,
// latest version, audit rows and the nudge mutation from @multica/core. We
// drive each through a mutable fixture so individual tests can set the scenario.
// ---------------------------------------------------------------------------
const state = vi.hoisted(() => ({
  role: "owner" as MemberRole | null,
  laggingIds: new Set<string>(),
  runtimes: [] as AgentRuntime[],
  members: [] as Array<{ user_id: string; name: string }>,
  latestVersion: "v1.2.0" as string | null,
  auditRows: [] as FleetAuditRow[],
  nudgeData: null as FleetSelfCheckResult | null,
  nudgePending: false,
  // captures the body passed to the nudge mutation so the INV-1 (no version)
  // assertion can inspect it.
  nudgeCalls: [] as Array<{ force?: boolean }>,
  // when set, the mutation invokes onError with this error.
  nudgeError: null as Error | null,
}));

const FleetRateLimitError = vi.hoisted(
  () =>
    class FleetRateLimitError extends Error {
      retryAfterSeconds: number | null;
      constructor(message: string, retryAfterSeconds: number | null) {
        super(message);
        this.name = "FleetRateLimitError";
        this.retryAfterSeconds = retryAfterSeconds;
      }
    },
);

vi.mock("@multica/core/api", () => ({
  FleetRateLimitError,
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: state.role }),
}));

vi.mock("@multica/core/runtimes/hooks", () => ({
  useUpdatableRuntimeIds: () => state.laggingIds,
}));

vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"], queryFn: vi.fn() }),
  latestCliVersionOptions: () => ({ queryKey: ["latest"], queryFn: vi.fn() }),
  fleetAuditOptions: () => ({ queryKey: ["audit"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/runtimes/mutations", () => ({
  useNudgeFleet: () => ({
    isPending: state.nudgePending,
    data: state.nudgeData,
    mutate: (
      body: { force?: boolean },
      opts?: { onError?: (err: Error) => void },
    ) => {
      state.nudgeCalls.push(body);
      if (state.nudgeError) opts?.onError?.(state.nudgeError);
    },
  }),
}));

// Route each mocked queryOptions to the matching fixture slice by its key.
vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>(
      "@tanstack/react-query",
    );
  return {
    ...actual,
    useQuery: (opts: { queryKey: string[] }) => {
      const key = opts.queryKey?.[0];
      if (key === "runtimes") return { data: state.runtimes };
      if (key === "members") return { data: state.members };
      if (key === "latest") return { data: state.latestVersion };
      if (key === "audit") return { data: { window_seconds: 0, rows: state.auditRows } };
      return { data: undefined };
    },
  };
});

import { FleetUpdateSection, aggregateProgress } from "./fleet-update-section";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Box",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host",
    metadata: { cli_version: "v1.0.0" },
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: "2026-06-16T00:00:00Z",
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
    ...overrides,
  };
}

function auditRow(overrides: Partial<FleetAuditRow>): FleetAuditRow {
  return {
    update_id: "u-1",
    runtime_id: "rt-1",
    user_id: "user-1",
    target_version: "v1.2.0",
    force: false,
    triggered_at: "2026-06-16T00:00:00Z",
    report_status: null,
    report_source: null,
    reported_at: null,
    ...overrides,
  };
}

function triggerResult(
  overrides: Partial<FleetSelfCheckResult>,
): FleetSelfCheckResult {
  return {
    target_version: "v1.2.0",
    force: false,
    triggered: [],
    skipped: [],
    failed: [],
    unreachable: [],
    ...overrides,
  };
}

function renderSection() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <FleetUpdateSection wsId="ws-1" />
    </I18nProvider>,
  );
}

beforeEach(() => {
  state.role = "owner";
  state.laggingIds = new Set<string>();
  state.runtimes = [];
  state.members = [{ user_id: "user-1", name: "Alice" }];
  state.latestVersion = "v1.2.0";
  state.auditRows = [];
  state.nudgeData = null;
  state.nudgePending = false;
  state.nudgeCalls = [];
  state.nudgeError = null;
});

describe("FleetUpdateSection — DRI gating (INV-3)", () => {
  it("renders the fleet button for an owner when machines are lagging", () => {
    state.role = "owner";
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    renderSection();
    expect(screen.getByText("Update fleet")).toBeTruthy();
  });

  it("renders for an admin", () => {
    state.role = "admin";
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    renderSection();
    expect(screen.getByText("Update fleet")).toBeTruthy();
  });

  it("renders nothing for a non-DRI member (hide is UX only)", () => {
    state.role = "member";
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    const { container } = renderSection();
    expect(screen.queryByText("Update fleet")).toBeNull();
    expect(container.textContent).toBe("");
  });

  it("renders nothing when no machine is lagging", () => {
    state.role = "owner";
    state.laggingIds = new Set();
    renderSection();
    expect(screen.queryByText("Update fleet")).toBeNull();
  });
});

describe("FleetUpdateSection — lagging list is pure CLI (§8)", () => {
  it("shows owner + current cli_version → target, never skill copy", () => {
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [
      makeRuntime({
        id: "rt-1",
        owner_id: "user-1",
        metadata: { cli_version: "v1.0.0" },
      }),
    ];
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));

    expect(screen.getByText("Alice")).toBeTruthy();
    expect(screen.getByText("v1.0.0")).toBeTruthy();
    // target version appears (in the list row and the target line).
    expect(screen.getAllByText("v1.2.0").length).toBeGreaterThan(0);

    // No skill-lagging copy anywhere in the panel (pure CLI dimension).
    expect(document.body.textContent?.toLowerCase()).not.toContain("skill");
  });
});

describe("FleetUpdateSection — request body carries no version (INV-1)", () => {
  it("nudge sends { force: false } and no version field", () => {
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));
    fireEvent.click(screen.getByText("Nudge self-check"));

    expect(state.nudgeCalls.length).toBe(1);
    const body = state.nudgeCalls[0]!;
    expect(body).toEqual({ force: false });
    expect("version" in body).toBe(false);
    expect("target_version" in body).toBe(false);
  });

  it("force sends { force: true } and no version field", () => {
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));
    fireEvent.click(screen.getByText("Force"));

    expect(state.nudgeCalls[0]).toEqual({ force: true });
    expect("version" in state.nudgeCalls[0]!).toBe(false);
  });
});

describe("FleetUpdateSection — 429 disables the button (INV-12)", () => {
  it("disables the action buttons after a FleetRateLimitError", () => {
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    state.nudgeError = new FleetRateLimitError("rate limited", 30);
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));

    const nudgeBtn = screen.getByText("Nudge self-check")
      .closest("button") as HTMLButtonElement;
    fireEvent.click(nudgeBtn);

    expect(nudgeBtn.disabled).toBe(true);
    const forceBtn = screen.getByText("Force").closest("button") as HTMLButtonElement;
    expect(forceBtn.disabled).toBe(true);
    expect(screen.getByText("Just triggered — try again shortly.")).toBeTruthy();
  });
});

describe("FleetUpdateSection — 429 re-enables after the window (INV-12)", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("consumes retryAfterSeconds and re-enables the buttons once it elapses", () => {
    vi.useFakeTimers();
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1" })];
    // 5s window so we can prove the timer is driven by retryAfterSeconds (a
    // hard-coded 30s fallback would not have fired after advancing 5s).
    state.nudgeError = new FleetRateLimitError("rate limited", 5);
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));

    const nudgeBtn = screen.getByText("Nudge self-check")
      .closest("button") as HTMLButtonElement;
    const forceBtn = screen.getByText("Force")
      .closest("button") as HTMLButtonElement;

    // Trigger the 429: both buttons disable, banner shows.
    fireEvent.click(nudgeBtn);
    expect(nudgeBtn.disabled).toBe(true);
    expect(forceBtn.disabled).toBe(true);
    expect(screen.getByText("Just triggered — try again shortly.")).toBeTruthy();

    // Just before the window closes — still disabled.
    act(() => {
      vi.advanceTimersByTime(4_999);
    });
    expect(nudgeBtn.disabled).toBe(true);

    // Window elapsed (retryAfterSeconds consumed, NOT dead code) — re-enabled.
    act(() => {
      vi.advanceTimersByTime(2);
    });
    expect(nudgeBtn.disabled).toBe(false);
    expect(forceBtn.disabled).toBe(false);
    expect(
      screen.queryByText("Just triggered — try again shortly."),
    ).toBeNull();
  });
});

describe("FleetUpdateSection — progress badges a11y (INV-15)", () => {
  it("status labels are neutral foreground with no semantic text colour, dots carry aria-hidden", () => {
    state.laggingIds = new Set(["rt-1", "rt-2"]);
    state.runtimes = [
      makeRuntime({ id: "rt-1", owner_id: "user-1" }),
      makeRuntime({ id: "rt-2", owner_id: "user-1" }),
    ];
    state.nudgeData = triggerResult({
      triggered: [
        { runtime_id: "rt-1", update_id: "u-1" },
        { runtime_id: "rt-2", update_id: "u-2" },
      ],
    });
    state.auditRows = [
      auditRow({ update_id: "u-1", runtime_id: "rt-1", report_status: "completed", report_source: "daemon-reported", reported_at: "2026-06-16T00:01:00Z" }),
      auditRow({ update_id: "u-2", runtime_id: "rt-2", report_status: "failed", report_source: "daemon-reported", reported_at: "2026-06-16T00:01:00Z" }),
    ];
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));

    // completed bucket label = "daemon self-reported updated" (not "safely
    // completed"); INV-4 trust distinction.
    const completedLabel = screen.getByText("Daemon self-reported updated");
    expect(completedLabel).toBeTruthy();
    const failedLabel = screen.getByText("Failed");
    expect(failedLabel).toBeTruthy();

    // The label SPANs must not carry semantic text colours.
    for (const label of [completedLabel, failedLabel]) {
      const cls = label.className;
      expect(cls).not.toMatch(/text-success/);
      expect(cls).not.toMatch(/text-destructive/);
      expect(cls).not.toMatch(/text-warning/);
      expect(cls).not.toMatch(/text-info/);
      // explicit neutral foreground.
      expect(cls).toContain("text-foreground");
    }

    // Each badge dot carries aria-hidden and a bg-* colour cue (NOT text-*).
    const dots = document.querySelectorAll('span[aria-hidden="true"].rounded-full');
    expect(dots.length).toBeGreaterThanOrEqual(2);
    dots.forEach((dot) => {
      expect(dot.className).toMatch(/bg-/);
      expect(dot.className).not.toMatch(/text-/);
    });
  });

  it("renders the trigger_failed bucket distinctly from skipped, neutral label", () => {
    state.laggingIds = new Set(["rt-1"]);
    state.runtimes = [makeRuntime({ id: "rt-1", owner_id: "user-1" })];
    state.nudgeData = triggerResult({
      // already updating -> skipped ("已在更新中").
      skipped: [{ runtime_id: "rt-2", reason: "update_in_progress" }],
      // Create faulted -> its own trigger_failed bucket ("触发出错").
      failed: [{ runtime_id: "rt-3", reason: "redis: connection refused" }],
    });
    renderSection();
    fireEvent.click(screen.getByText("Update fleet"));

    const triggerFailed = screen.getByText("Trigger errored");
    const skipped = screen.getByText("Still updating to a prior trigger");
    // Two distinct labels — failed is NOT disguised as skipped.
    expect(triggerFailed).toBeTruthy();
    expect(skipped).toBeTruthy();
    expect(triggerFailed).not.toBe(skipped);

    // INV-15: trigger_failed label is neutral foreground, never semantic colour
    // (the destructive cue lives only on the dot).
    expect(triggerFailed.className).not.toMatch(/text-destructive/);
    expect(triggerFailed.className).toContain("text-foreground");
  });
});

describe("aggregateProgress (INV-6 buckets)", () => {
  it("returns zero totals with no trigger receipt", () => {
    const p = aggregateProgress(null, []);
    expect(p.total).toBe(0);
    expect(p.done).toBe(0);
  });

  it("includes skipped + unknown buckets and counts them in N", () => {
    const trigger = triggerResult({
      // u-1 completed (audit), u-2 has NO audit row => unknown.
      triggered: [
        { runtime_id: "rt-1", update_id: "u-1" },
        { runtime_id: "rt-2", update_id: "u-2" },
      ],
      // a skipped machine: Create refused (still updating to a prior trigger).
      skipped: [{ runtime_id: "rt-3", reason: "update_in_progress" }],
      // desktop machine: excluded from x/N.
      unreachable: [{ runtime_id: "rt-4", reason: "desktop" }],
    });
    const rows = [
      auditRow({ update_id: "u-1", runtime_id: "rt-1", report_status: "completed", report_source: "daemon-reported" }),
    ];
    const p = aggregateProgress(trigger, rows);

    expect(p.counts.completed).toBe(1);
    // u-2 triggered but no audit row => unknown.
    expect(p.counts.unknown).toBe(1);
    expect(p.counts.skipped).toBe(1);

    // N = triggered (2) + skipped (1) = 3; unreachable excluded.
    expect(p.total).toBe(3);
    // done = completed + skipped + unknown = 3.
    expect(p.done).toBe(3);
    // unreachable surfaced separately, not in x/N.
    expect(p.notTriggered).toBe(1);
  });

  it("buckets a timeout (server-timeout sweep) distinctly", () => {
    const trigger = triggerResult({
      triggered: [{ runtime_id: "rt-1", update_id: "u-1" }],
    });
    const rows = [
      auditRow({ update_id: "u-1", runtime_id: "rt-1", report_status: "timeout", report_source: "server-timeout" }),
    ];
    const p = aggregateProgress(trigger, rows);
    expect(p.counts.timeout).toBe(1);
    expect(p.counts.completed).toBe(0);
    expect(p.total).toBe(1);
    expect(p.done).toBe(1);
  });

  it("buckets trigger-time failures separately from skipped, in N and done", () => {
    const trigger = triggerResult({
      triggered: [{ runtime_id: "rt-1", update_id: "u-1" }],
      // already updating -> skipped.
      skipped: [{ runtime_id: "rt-2", reason: "update_in_progress" }],
      // Create faulted (store/Redis) -> its own trigger_failed bucket.
      failed: [{ runtime_id: "rt-3", reason: "redis: connection refused" }],
    });
    const rows = [
      auditRow({ update_id: "u-1", runtime_id: "rt-1", report_status: "completed", report_source: "daemon-reported" }),
    ];
    const p = aggregateProgress(trigger, rows);

    expect(p.counts.trigger_failed).toBe(1);
    expect(p.counts.skipped).toBe(1);
    // trigger_failed must NOT be folded into skipped (or the audit `failed`).
    expect(p.counts.failed).toBe(0);
    // N = triggered (1) + skipped (1) + trigger_failed (1) = 3.
    expect(p.total).toBe(3);
    // done = completed (1) + skipped (1) + trigger_failed (1) = 3.
    expect(p.done).toBe(3);
  });

  it("treats a triggered row with a null report as pending, not unknown", () => {
    const trigger = triggerResult({
      triggered: [{ runtime_id: "rt-1", update_id: "u-1" }],
    });
    const rows = [auditRow({ update_id: "u-1", runtime_id: "rt-1", report_status: null })];
    const p = aggregateProgress(trigger, rows);
    expect(p.counts.pending).toBe(1);
    expect(p.counts.unknown).toBe(0);
  });
});
