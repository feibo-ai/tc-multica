// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { TeamOverviewMember } from "@multica/core/team";

import enCommon from "../locales/en/common.json";
import enMembers from "../locales/en/members.json";
import enTeam from "../locales/en/team.json";
import { TeamMemberCard } from "./member-card";

const TEST_RESOURCES = {
  en: { common: enCommon, team: enTeam, members: enMembers },
};

function renderCard(member: Partial<TeamOverviewMember>) {
  const full: TeamOverviewMember = {
    member_id: "m1",
    user_id: "u1",
    name: "Alice",
    email: "alice@example.com",
    avatar_url: "",
    role: "owner",
    is_self: false,
    squad_name: "",
    projects_led: 0,
    projects_dri: 0,
    issues_by_status: {},
    issues_total: 0,
    issues_blocked: 0,
    agent_issues_by_status: {},
    agent_issues_total: 0,
    agents_total: 0,
    agents_running: 0,
    autopilots: 0,
    tokens_week: 0,
    tokens_month: 0,
    ...member,
  };
  return render(
    <I18nProvider resources={TEST_RESOURCES} locale="en">
      <TeamMemberCard member={full} />
    </I18nProvider>,
  );
}

describe("TeamMemberCard", () => {
  afterEach(cleanup);

  it("renders the member name and the role label", () => {
    renderCard({ name: "Alice", role: "owner" });
    expect(screen.getByText("Alice")).toBeInTheDocument();
    expect(screen.getByText("Owner")).toBeInTheDocument();
  });

  it("describes agent presence (running agents), not human online status", () => {
    renderCard({ agents_running: 2 });
    expect(screen.getByText("2 agents running")).toBeInTheDocument();
  });

  it("renders the task distribution with status labels and counts", () => {
    renderCard({ issues_total: 5, issues_by_status: { todo: 3, blocked: 2 } });
    expect(screen.getByText(/To do/)).toBeInTheDocument();
    expect(screen.getByText(/Stuck/)).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
  });

  it("folds agent-delegated tasks into the distribution total", () => {
    // An AI-native team's own-task counts are usually zero; the work is on the
    // agents. The total must reflect own + agent-delegated, not just own.
    renderCard({
      issues_total: 1,
      issues_by_status: { todo: 1 },
      agent_issues_total: 4,
      agent_issues_by_status: { in_progress: 4 },
    });
    // own (1) + agent-delegated (4) = 5; would read "1" if agent work were dropped.
    expect(screen.getByText("5")).toBeInTheDocument();
    // The agent-delegated status shows a non-zero count in the legend.
    const inProgress = screen
      .getAllByText(/In progress/)
      .find((el) => el.textContent?.includes("4"));
    expect(inProgress).toBeDefined();
  });
});
