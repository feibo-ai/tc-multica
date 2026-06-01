import { describe, expect, it } from "vitest";
import type { Project } from "../types";
import { deriveProjectHealth } from "./config";

function makeProject(overrides: Partial<Project> = {}): Project {
  return {
    id: "p-1",
    workspace_id: "ws-1",
    title: "Test",
    description: null,
    icon: null,
    status: "in_progress",
    priority: "medium",
    lead_type: null,
    lead_id: null,
    dri_user_id: "user-1",
    created_at: "2026-05-29T00:00:00Z",
    updated_at: "2026-05-29T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    ...overrides,
  };
}

describe("deriveProjectHealth", () => {
  it("flags an active project with no DRI as at_risk (the P-5 signal)", () => {
    expect(deriveProjectHealth(makeProject({ status: "in_progress", dri_user_id: null }))).toBe("at_risk");
    expect(deriveProjectHealth(makeProject({ status: "planned", dri_user_id: null }))).toBe("at_risk");
    expect(deriveProjectHealth(makeProject({ status: "paused", dri_user_id: null }))).toBe("at_risk");
  });

  it("reports on_track for an active project with a DRI", () => {
    expect(deriveProjectHealth(makeProject({ status: "in_progress", dri_user_id: "user-1" }))).toBe("on_track");
  });

  it("returns null for terminal states (no ongoing health), even without a DRI", () => {
    expect(deriveProjectHealth(makeProject({ status: "completed", dri_user_id: null }))).toBeNull();
    expect(deriveProjectHealth(makeProject({ status: "cancelled", dri_user_id: null }))).toBeNull();
  });
});
