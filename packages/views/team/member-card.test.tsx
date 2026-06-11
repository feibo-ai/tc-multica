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
});
