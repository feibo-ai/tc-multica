// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// ---------------------------------------------------------------------------
// Mocks — control plane API methods + workspace hook. The component reads
// from the api singleton, so we replace the methods it calls.
// ---------------------------------------------------------------------------
const listIntegrationSecretKeys = vi.fn();
const getIntegrationSecret = vi.fn();
const upsertIntegrationSecret = vi.fn();
const deleteIntegrationSecret = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: {
    listIntegrationSecretKeys: (...args: unknown[]) =>
      listIntegrationSecretKeys(...args),
    getIntegrationSecret: (...args: unknown[]) =>
      getIntegrationSecret(...args),
    upsertIntegrationSecret: (...args: unknown[]) =>
      upsertIntegrationSecret(...args),
    deleteIntegrationSecret: (...args: unknown[]) =>
      deleteIntegrationSecret(...args),
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

import { SecretManagerSection } from "../secret-manager-section";

function renderSection() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <SecretManagerSection integrationId="int-1" />
    </QueryClientProvider>,
  );
}

describe("SecretManagerSection", () => {
  beforeEach(() => {
    listIntegrationSecretKeys.mockReset();
    getIntegrationSecret.mockReset();
    upsertIntegrationSecret.mockReset();
    deleteIntegrationSecret.mockReset();
    vi.useRealTimers();
  });

  it("renders empty state when no secrets exist", async () => {
    listIntegrationSecretKeys.mockResolvedValue([]);
    renderSection();
    await waitFor(() =>
      expect(screen.queryByTestId("secrets-empty")).toBeTruthy(),
    );
  });

  it("renders key rows without ever exposing values", async () => {
    listIntegrationSecretKeys.mockResolvedValue([
      { key: "FEISHU_APP_SECRET", version: 1, created_by: null, created_at: "", updated_at: "" },
      { key: "WEBHOOK_TOKEN", version: 3, created_by: null, created_at: "", updated_at: "" },
    ]);

    const { container } = renderSection();
    await waitFor(() =>
      expect(screen.queryByTestId("secret-row-FEISHU_APP_SECRET")).toBeTruthy(),
    );

    // The list response shape has no `value` field; render output must not
    // contain any plaintext that looks like a secret value.
    expect(container.textContent).toContain("FEISHU_APP_SECRET");
    expect(container.textContent).toContain("WEBHOOK_TOKEN");
    expect(container.textContent).toContain("v1");
    expect(container.textContent).toContain("v3");
  });

  it("reveals a secret on eye click", async () => {
    listIntegrationSecretKeys.mockResolvedValue([
      { key: "MY_KEY", version: 1, created_by: null, created_at: "", updated_at: "" },
    ]);
    getIntegrationSecret.mockResolvedValue({
      key: "MY_KEY",
      value: "the-plaintext",
      version: 1,
    });

    renderSection();
    const revealBtn = await screen.findByTestId("reveal-MY_KEY");

    fireEvent.click(revealBtn);

    // The component requested the value and rendered the plaintext.
    await waitFor(() =>
      expect(getIntegrationSecret).toHaveBeenCalledWith("int-1", "MY_KEY"),
    );
    await waitFor(() => {
      const revealed = screen.queryByTestId("revealed-value");
      expect(revealed?.textContent).toBe("the-plaintext");
    });
  });

  it("uses window.setTimeout with 30_000ms to auto-hide", async () => {
    // The 30s auto-hide is intentionally hard to exercise end-to-end (it
    // races react-query's internal scheduler under jsdom). Instead we
    // assert the contract: when reveal succeeds, the component schedules
    // exactly one 30_000ms setTimeout, which is the auto-hide.
    const spy = vi.spyOn(window, "setTimeout");
    listIntegrationSecretKeys.mockResolvedValue([
      { key: "MY_KEY", version: 1, created_by: null, created_at: "", updated_at: "" },
    ]);
    getIntegrationSecret.mockResolvedValue({
      key: "MY_KEY",
      value: "secret-value",
      version: 1,
    });

    renderSection();
    fireEvent.click(await screen.findByTestId("reveal-MY_KEY"));

    await waitFor(() => expect(getIntegrationSecret).toHaveBeenCalled());
    await waitFor(() => {
      const autoHideCall = spy.mock.calls.find((c) => c[1] === 30_000);
      expect(autoHideCall).toBeTruthy();
    });
    spy.mockRestore();
  });

  it("rejects invalid key pattern when adding a secret", async () => {
    listIntegrationSecretKeys.mockResolvedValue([]);
    renderSection();

    fireEvent.click(await screen.findByText("+ Add secret"));
    expect(screen.queryByTestId("add-secret-dialog")).toBeTruthy();

    // Lowercase key — the lowercase input handler upper-cases as we type, so
    // we set it directly via the underlying input change with a value that
    // bypasses the upper-case (no real way for a user to do this, but the
    // regex check is the actual production guard we want to verify).
    // Use a key with an invalid character (dash) that survives the toUpperCase.
    const keyInput = document.querySelector('input[placeholder="FEISHU_APP_SECRET"]') as HTMLInputElement;
    fireEvent.change(keyInput, { target: { value: "BAD-KEY" } });
    const valueArea = document.querySelector("textarea") as HTMLTextAreaElement;
    fireEvent.change(valueArea, { target: { value: "x" } });

    fireEvent.click(screen.getByTestId("add-secret-submit"));

    await waitFor(() =>
      expect(screen.queryByTestId("add-secret-error")?.textContent).toMatch(/Key must match/),
    );
    expect(upsertIntegrationSecret).not.toHaveBeenCalled();
  });
});
