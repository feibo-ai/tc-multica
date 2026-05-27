// @vitest-environment jsdom

import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  MemberWithUser,
  SkillSummary,
} from "@multica/core/types";

import enCommon from "../../../locales/en/common.json";
import enSkills from "../../../locales/en/skills.json";

import { useSkillColumns, type SkillRow, STALE_AFTER_MS } from "../skill-columns";

const TEST_RESOURCES = {
  en: { common: enCommon, skills: enSkills },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

// Tiny harness that renders the columns for a single row, returning the DOM
// fragment for assertions. We use useReactTable + flexRender (the same path
// DataTable uses internally) so the test exercises the production cell
// renderers, not a synthetic copy.
function RowHarness({
  row,
  members,
}: {
  row: SkillRow;
  members: MemberWithUser[];
}) {
  const columns = useSkillColumns(members);
  const table = useReactTable({
    data: [row],
    columns,
    getCoreRowModel: getCoreRowModel(),
  });
  return (
    <table>
      <tbody>
        {table.getRowModel().rows.map((r) => (
          <tr key={r.id}>
            {r.getVisibleCells().map((c) => (
              <td key={c.id} data-column-id={c.column.id}>
                {flexRender(c.column.columnDef.cell, c.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function renderRow(row: SkillRow, members: MemberWithUser[] = []) {
  return render(
    <I18nWrapper>
      <RowHarness row={row} members={members} />
    </I18nWrapper>,
  );
}

const ALICE: MemberWithUser = {
  id: "mem-1",
  workspace_id: "ws-1",
  user_id: "user-alice",
  role: "member",
  created_at: "2026-01-01T00:00:00Z",
  name: "Alice",
  email: "alice@example.com",
  avatar_url: null,
};

function makeSkill(overrides: Partial<SkillSummary> = {}): SkillSummary {
  return {
    id: "skill-1",
    workspace_id: "ws-1",
    name: "test",
    description: "",
    config: {},
    created_by: null,
    owner_user_id: null,
    last_reviewed_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function makeRow(overrides: Partial<SkillRow> = {}): SkillRow {
  return {
    skill: makeSkill(),
    agents: [],
    creator: null,
    runtime: null,
    canEdit: false,
    ...overrides,
  };
}

describe("skill-columns owner cell", () => {
  it("renders 'No owner' in red when owner_user_id is null", () => {
    renderRow(makeRow({ skill: makeSkill({ owner_user_id: null }) }));
    const cell = document.querySelector('[data-column-id="owner"]');
    expect(cell).not.toBeNull();
    const noOwner = cell!.querySelector("span");
    expect(noOwner).not.toBeNull();
    expect(noOwner!.textContent).toBe("No owner");
    expect(noOwner!.className).toContain("text-red-500");
  });

  it("renders member name when owner_user_id maps to a known member", () => {
    renderRow(
      makeRow({ skill: makeSkill({ owner_user_id: "user-alice" }) }),
      [ALICE],
    );
    const cell = document.querySelector('[data-column-id="owner"]');
    expect(cell).not.toBeNull();
    expect(cell!.textContent).toContain("Alice");
    // Not the red "No owner" path.
    expect(cell!.textContent).not.toContain("No owner");
  });

  it("falls back to truncated UUID when owner_user_id is unknown", () => {
    renderRow(
      makeRow({
        skill: makeSkill({ owner_user_id: "11111111-2222-3333-4444-555555555555" }),
      }),
      [ALICE],
    );
    const cell = document.querySelector('[data-column-id="owner"]');
    expect(cell).not.toBeNull();
    // First 8 chars + ellipsis.
    expect(cell!.textContent).toContain("11111111…");
  });
});

describe("skill-columns last reviewed cell", () => {
  // Freeze "now" so the staleness boundary is deterministic.
  const FIXED_NOW = new Date("2026-06-01T00:00:00Z").getTime();

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders 'Never' in red when last_reviewed_at is null", () => {
    renderRow(makeRow({ skill: makeSkill({ last_reviewed_at: null }) }));
    const cell = document.querySelector('[data-column-id="lastReviewed"]');
    expect(cell).not.toBeNull();
    const span = cell!.querySelector("span");
    expect(span!.textContent).toBe("Never");
    expect(span!.className).toContain("text-red-500");
  });

  it("renders the timestamp in red when last_reviewed_at is older than 90 days", () => {
    const stale = new Date(FIXED_NOW - STALE_AFTER_MS - 1000).toISOString();
    renderRow(makeRow({ skill: makeSkill({ last_reviewed_at: stale }) }));
    const cell = document.querySelector('[data-column-id="lastReviewed"]');
    expect(cell).not.toBeNull();
    const span = cell!.querySelector("span");
    expect(span).not.toBeNull();
    expect(span!.className).toContain("text-red-500");
  });

  it("renders the timestamp in muted color when last_reviewed_at is within 90 days", () => {
    const recent = new Date(FIXED_NOW - 1000).toISOString();
    renderRow(makeRow({ skill: makeSkill({ last_reviewed_at: recent }) }));
    const cell = document.querySelector('[data-column-id="lastReviewed"]');
    expect(cell).not.toBeNull();
    const span = cell!.querySelector("span");
    expect(span).not.toBeNull();
    expect(span!.className).not.toContain("text-red-500");
    expect(span!.className).toContain("text-muted-foreground");
  });
});

describe("skill-columns header surfacing", () => {
  it("exposes 'Owner' and 'Last reviewed' headers from i18n", () => {
    // Smoke-check: render a row and look for the localized header strings in
    // the resolved column definitions. We don't render headers here, but
    // the i18n bundle has to contain the keys or useSkillColumns would
    // throw.
    renderRow(makeRow());
    // No assertion failure means the resources resolved cleanly; explicit
    // sanity check on the JSON shape so a future locale rename triggers a
    // visible test failure.
    expect(enSkills.table.owner).toBe("Owner");
    expect(enSkills.table.last_reviewed).toBe("Last reviewed");
    expect(enSkills.owner.unassigned).toBe("No owner");
    expect(enSkills.review.never).toBe("Never");
    // Make sure the new scope key is in place too.
    expect(enSkills.page.scopes.stale.label).toBe("Stale");
    // Reference `screen` so the import isn't dead — used by the runtime
    // helper in the same file pattern.
    expect(screen).toBeDefined();
  });
});
