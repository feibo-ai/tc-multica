// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { TeamOverviewMember } from "@multica/core/team";

import enCommon from "../locales/en/common.json";
import enMembers from "../locales/en/members.json";
import enTeam from "../locales/en/team.json";
import { NavigationProvider, type NavigationAdapter } from "../navigation";

const useQueryMock = vi.fn();
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => useQueryMock(),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ projects: () => "/ws-1/projects" }),
}));
vi.mock("@multica/core/team", () => ({ teamOverviewOptions: () => ({}) }));

import { TeamOverviewPage } from "./team-overview-page";

const TEST_RESOURCES = {
  en: { common: enCommon, team: enTeam, members: enMembers },
};
const nav: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/",
  searchParams: new URLSearchParams(),
  getShareableUrl: (p: string) => p,
};

function member(over: Partial<TeamOverviewMember>): TeamOverviewMember {
  return {
    member_id: "m1",
    user_id: "u1",
    name: "Alice",
    email: "",
    avatar_url: "",
    role: "owner",
    is_self: true,
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
    ...over,
  };
}

function renderPage() {
  return render(
    <I18nProvider resources={TEST_RESOURCES} locale="en">
      <NavigationProvider value={nav}>
        <TeamOverviewPage />
      </NavigationProvider>
    </I18nProvider>,
  );
}

describe("TeamOverviewPage", () => {
  afterEach(() => {
    cleanup();
    useQueryMock.mockReset();
  });

  it("shows the team tab and the empty state when there are no members", () => {
    useQueryMock.mockReturnValue({
      data: { viewer_member_id: "", members: [] },
      isLoading: false,
    });
    renderPage();
    expect(screen.getByText("Team")).toBeInTheDocument();
    expect(screen.getByText("No team members yet")).toBeInTheDocument();
  });

  it("renders a card per member", () => {
    useQueryMock.mockReturnValue({
      data: { viewer_member_id: "m1", members: [member({ name: "Alice" })] },
      isLoading: false,
    });
    renderPage();
    expect(screen.getByText("Alice")).toBeInTheDocument();
  });
});
