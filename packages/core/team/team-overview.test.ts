import { describe, expect, it } from "vitest";

import { parseWithFallback } from "../api/schema";
import { EMPTY_TEAM_OVERVIEW, TeamOverviewSchema } from "../api/schemas";

const endpoint = "GET /api/team/overview";

describe("TeamOverviewSchema drift-defense", () => {
  it("parses a valid response and fills missing member fields with defaults", () => {
    const parsed = parseWithFallback(
      {
        viewer_member_id: "m1",
        members: [
          {
            member_id: "m1",
            user_id: "u1",
            name: "Alice",
            role: "owner",
            is_self: true,
            issues_total: 3,
            issues_by_status: { blocked: 1, in_progress: 2 },
          },
        ],
      },
      TeamOverviewSchema,
      EMPTY_TEAM_OVERVIEW,
      { endpoint },
    );
    expect(parsed.viewer_member_id).toBe("m1");
    expect(parsed.members).toHaveLength(1);
    const m = parsed.members[0]!;
    expect(m.name).toBe("Alice");
    // Absent numeric fields default to 0 rather than undefined.
    expect(m.agents_running).toBe(0);
    expect(m.tokens_week).toBe(0);
    expect(m.issues_by_status.blocked).toBe(1);
    // Delegated-task fields the member omitted default safely (drift contract).
    expect(m.agent_issues_total).toBe(0);
    expect(m.agent_issues_by_status).toEqual({});
  });

  it("degrades to the fallback when members is the wrong type", () => {
    const parsed = parseWithFallback(
      { viewer_member_id: "m1", members: 42 },
      TeamOverviewSchema,
      EMPTY_TEAM_OVERVIEW,
      { endpoint },
    );
    expect(parsed).toEqual(EMPTY_TEAM_OVERVIEW);
  });

  it("does not throw on a null body", () => {
    const parsed = parseWithFallback(null, TeamOverviewSchema, EMPTY_TEAM_OVERVIEW, {
      endpoint,
    });
    expect(parsed.members).toEqual([]);
  });
});
