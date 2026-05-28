// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const listIntegrations = vi.fn();
const createIntegration = vi.fn();
const navPush = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: {
    listIntegrations: (...args: unknown[]) => listIntegrations(...args),
    createIntegration: (...args: unknown[]) => createIntegration(...args),
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("../../../navigation", () => ({
  useNavigation: () => ({ push: navPush, replace: vi.fn(), back: vi.fn() }),
}));

vi.mock("../../../layout/page-header", () => ({
  PageHeader: ({ children }: { children: React.ReactNode }) => <header>{children}</header>,
}));

import { IntegrationsPage } from "../integrations-page";

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <IntegrationsPage />
    </QueryClientProvider>,
  );
}

const ROWS = [
  {
    id: "i-1",
    workspace_id: "ws-1",
    kind: "mcp-server",
    name: "alpha-mcp",
    config: {},
    version: 1,
    status: "running",
    deployment_webhook_url: null,
    config_schema_ref: null,
    created_at: "",
    updated_at: "",
  },
  {
    id: "i-2",
    workspace_id: "ws-1",
    kind: "feishu",
    name: "feishu-bot",
    config: {},
    version: 1,
    status: "pending",
    deployment_webhook_url: null,
    config_schema_ref: null,
    created_at: "",
    updated_at: "",
  },
] as const;

describe("IntegrationsPage", () => {
  beforeEach(() => {
    listIntegrations.mockReset();
    createIntegration.mockReset();
    navPush.mockReset();
  });

  it("renders all integration rows by default", async () => {
    listIntegrations.mockResolvedValue([...ROWS]);
    renderPage();

    await waitFor(() => {
      expect(screen.queryByText("alpha-mcp")).toBeTruthy();
      expect(screen.queryByText("feishu-bot")).toBeTruthy();
    });
  });

  it("filters by kind when a filter button is clicked", async () => {
    listIntegrations.mockResolvedValue([...ROWS]);
    renderPage();
    await waitFor(() => expect(screen.queryByText("alpha-mcp")).toBeTruthy());

    // Click 'feishu' filter — only feishu rows should remain.
    fireEvent.click(screen.getByRole("button", { name: "feishu" }));
    expect(screen.queryByText("alpha-mcp")).toBeNull();
    expect(screen.queryByText("feishu-bot")).toBeTruthy();
  });

  it("navigates to detail on row click", async () => {
    listIntegrations.mockResolvedValue([...ROWS]);
    renderPage();
    await waitFor(() => expect(screen.queryByText("alpha-mcp")).toBeTruthy());

    fireEvent.click(screen.getByText("alpha-mcp"));
    expect(navPush).toHaveBeenCalledWith("/integrations/i-1");
  });

  it("shows empty state when no integrations exist", async () => {
    listIntegrations.mockResolvedValue([]);
    renderPage();
    await waitFor(() =>
      expect(screen.queryByText(/No integrations yet/i)).toBeTruthy(),
    );
  });
});
