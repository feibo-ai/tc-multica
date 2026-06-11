import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { DraggableBoardCard } from "./board-card";
import { DraggableListRow } from "./list-row";
import { IssuePaneProvider, type IssuePaneController } from "./issue-pane";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useWorkspaceSlug: () => "acme",
    useRequiredWorkspaceSlug: () => "acme",
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

const mockActorNameResult = {
  getActorName: () => "Mock Actor",
  getActorInitials: () => "MA",
  getActorAvatarUrl: () => null,
  getMemberName: () => "Mock Member",
  getAgentName: () => "Mock Agent",
  getSquadName: () => "Mock Squad",
};
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => mockActorNameResult,
}));

const mockAuthUser = { id: "user-1", email: "test@test.com", name: "Test User" };
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: any) => {
      const state = { user: mockAuthUser, isAuthenticated: true };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ user: mockAuthUser, isAuthenticated: true }) },
  ),
  registerAuthStore: vi.fn(),
  createAuthStore: vi.fn(),
}));

// AppLink → <a> so we can assert the default (no-pane) navigation affordance.
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...props }: any) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), replace: vi.fn(), pathname: "/issues", searchParams: new URLSearchParams() }),
  NavigationProvider: ({ children }: { children: React.ReactNode }) => children,
}));

vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: (_wsId: string) => ({
    queryKey: ["projects", _wsId, "list"],
    queryFn: () => Promise.resolve([]),
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: (_wsId: string) => ({ queryKey: ["members", _wsId], queryFn: () => Promise.resolve([]) }),
  agentListOptions: (_wsId: string) => ({ queryKey: ["agents", _wsId], queryFn: () => Promise.resolve([]) }),
}));

const mockCardProperties: Record<string, boolean> = {
  priority: true, description: true, assignee: false, dueDate: false,
  startDate: false, project: false, childProgress: false, labels: false,
};
vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  ViewStoreProvider: ({ children }: { children: React.ReactNode }) => children,
  useViewStore: (selector?: any) => {
    const state = { cardProperties: mockCardProperties };
    return selector ? selector(state) : state;
  },
}));

vi.mock("@multica/core/issues/stores/selection-store", () => ({
  useIssueSelectionStore: (selector?: any) => {
    const state = { selectedIds: new Set<string>(), toggle: vi.fn() };
    return selector ? selector(state) : state;
  },
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useUpdateIssue: () => ({ mutate: vi.fn(), mutateAsync: vi.fn() }),
}));

const mockOpenModal = vi.fn();
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(
    () => ({ open: mockOpenModal }),
    { getState: () => ({ open: mockOpenModal }) },
  ),
}));

vi.mock("@dnd-kit/sortable", () => ({
  defaultAnimateLayoutChanges: () => false,
  useSortable: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: vi.fn(),
    transform: null,
    transition: null,
    isDragging: false,
  }),
}));

vi.mock("@dnd-kit/utilities", () => ({
  CSS: { Transform: { toString: () => undefined } },
}));

const mockIssue: Issue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "PROJ-1",
  title: "Test Issue",
  description: null,
  status: "todo",
  priority: "high",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 100,
  start_date: null,
  due_date: null,
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderWithProviders(ui: React.ReactNode, pane?: IssuePaneController) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={TEST_RESOURCES} locale="en">
        {pane ? <IssuePaneProvider value={pane}>{ui}</IssuePaneProvider> : ui}
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("issue card master-detail (useIssuePane)", () => {
  beforeEach(() => vi.clearAllMocks());

  describe("DraggableBoardCard", () => {
    it("renders a full-page AppLink when no pane controller is present", () => {
      renderWithProviders(<DraggableBoardCard issue={mockIssue} />);
      const link = screen.getByRole("link");
      expect(link).toHaveAttribute("href", expect.stringContaining("issue-1"));
      // The card itself is not a click-to-open control (inline pickers may
      // still expose their own buttons — those are named otherwise).
      expect(
        screen.queryByRole("button", { name: /Test Issue/ }),
      ).not.toBeInTheDocument();
    });

    it("renders a click-to-open control and fires openIssue when a pane controller is present", () => {
      const openIssue = vi.fn();
      renderWithProviders(<DraggableBoardCard issue={mockIssue} />, {
        activeIssueId: null,
        openIssue,
      });
      expect(screen.queryByRole("link")).not.toBeInTheDocument();
      const card = screen.getByRole("button", { name: /Test Issue/ });
      fireEvent.click(card);
      expect(openIssue).toHaveBeenCalledWith("issue-1");
    });

    it("marks the active card with aria-current", () => {
      renderWithProviders(<DraggableBoardCard issue={mockIssue} />, {
        activeIssueId: "issue-1",
        openIssue: vi.fn(),
      });
      expect(
        screen.getByRole("button", { name: /Test Issue/ }),
      ).toHaveAttribute("aria-current", "true");
    });
  });

  describe("DraggableListRow", () => {
    it("renders a full-page AppLink when no pane controller is present", () => {
      renderWithProviders(<DraggableListRow issue={mockIssue} />);
      const link = screen.getByRole("link");
      expect(link).toHaveAttribute("href", expect.stringContaining("issue-1"));
    });

    it("renders a click-to-open control and fires openIssue when a pane controller is present", () => {
      const openIssue = vi.fn();
      renderWithProviders(<DraggableListRow issue={mockIssue} />, {
        activeIssueId: null,
        openIssue,
      });
      const row = screen.getByRole("button", { name: /Test Issue/ });
      fireEvent.click(row);
      expect(openIssue).toHaveBeenCalledWith("issue-1");
    });
  });
});
