// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type {
  MemberWithUser,
  Project,
  ProjectPriority,
  ProjectStatus,
} from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";

// The DRI section calls `api.assignProjectDRI` on submit. Mock the api
// module before importing the component so the import sees the stub.
vi.mock("@multica/core/api", () => ({
  api: {
    assignProjectDRI: vi.fn(),
  },
}));

import { ProjectDriSection } from "../project-dri-section";

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

function makeProject(overrides: Partial<Project> = {}): Project {
  return {
    id: "proj-1",
    workspace_id: "ws-1",
    title: "Test Project",
    description: null,
    icon: null,
    status: "in_progress" as ProjectStatus,
    priority: "medium" as ProjectPriority,
    lead_type: null,
    lead_id: null,
    dri_user_id: null,
    start_date: null,
    due_date: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    ...overrides,
  };
}

function renderDriSection(project: Project, members: MemberWithUser[] = []) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return renderWithI18n(
    <Wrapper>
      <ProjectDriSection project={project} members={members} wsId="ws-1" />
    </Wrapper>,
  );
}

describe("ProjectDriSection", () => {
  it("renders the red 'Unassigned' warning when dri_user_id is null", () => {
    const { container } = renderDriSection(
      makeProject({ dri_user_id: null }),
    );
    // The trigger button surfaces the unassigned warning in red so the
    // SOP P-5 risk is visible at a glance from the sidebar.
    const warning = container.querySelector("span.text-red-500");
    expect(warning).not.toBeNull();
    expect(warning!.textContent).toBe("Unassigned (SOP P-5 risk)");
  });

  it("renders the member name when dri_user_id maps to a known member", () => {
    const { container } = renderDriSection(
      makeProject({ dri_user_id: "user-alice" }),
      [ALICE],
    );
    // Member name is present in the trigger.
    expect(container.textContent).toContain("Alice");
    // The red-warning path should be absent when the DRI is set.
    expect(container.textContent).not.toContain("Unassigned");
    // Avatar (a div with `data-slot="avatar"`) renders alongside the name.
    expect(container.querySelector('[data-slot="avatar"]')).not.toBeNull();
  });

  it("falls back to a truncated UUID when dri_user_id is unknown", () => {
    // DRI is set on the server but the user is no longer a workspace member
    // (deleted user). Surface the truncated id so we don't appear to have
    // silently lost the assignment.
    const { container } = renderDriSection(
      makeProject({
        dri_user_id: "11111111-2222-3333-4444-555555555555",
      }),
      [ALICE],
    );
    // First 8 chars + ellipsis appear in the trigger.
    expect(container.textContent).toContain("11111111…");
    // Neither the red "Unassigned" nor the known member name should appear.
    expect(container.textContent).not.toContain("Unassigned");
    expect(container.textContent).not.toContain("Alice");
  });
});

describe("projects locale bundle exposes DRI keys", () => {
  it("registers the en/zh-Hans strings the section uses", async () => {
    // Locale assertions stay in this file (rather than a separate i18n
    // smoke test) so a future locale rename or accidental drop on the DRI
    // keys produces a visible failure in the DRI test suite. The static
    // import keeps the keys narrowly typed (JSON inference) so a deleted
    // key would be a typecheck failure, not a runtime undefined.
    const en = (await import("../../../locales/en/projects.json")).default;
    const zh = (await import("../../../locales/zh-Hans/projects.json")).default;

    expect(en.filters.without_dri).toBe("Without DRI");
    expect(en.detail.dri_label).toBe("DRI");
    expect(en.detail.dri_unassigned).toBe("Unassigned (SOP P-5 risk)");
    expect(en.detail.dri_assign).toBe("Assign DRI");
    expect(en.detail.dri_change).toBe("Change DRI");

    expect(zh.filters.without_dri).toBe("无 DRI");
    expect(zh.detail.dri_label).toBe("DRI");
    expect(zh.detail.dri_unassigned).toBe("未指定（SOP P-5 风险）");
    expect(zh.detail.dri_assign).toBe("指定 DRI");
    expect(zh.detail.dri_change).toBe("更换 DRI");
  });
});
