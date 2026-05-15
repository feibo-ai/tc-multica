import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render as rtlRender, screen, type RenderOptions } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSquads from "../../locales/en/squads.json";

// AlertDialog (Base UI) renders into a portal that is awkward to target with
// RTL — strip it down to passthrough wrappers so we can assert on the body
// content directly. The component logic under test (count fallback, pending
// state, button wiring) lives in the dialog body, not in Base UI.
vi.mock("@multica/ui/components/ui/alert-dialog", () => ({
  AlertDialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div role="alertdialog">{children}</div> : null,
  AlertDialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AlertDialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AlertDialogTitle: ({ children }: { children: ReactNode }) => <h1>{children}</h1>,
  AlertDialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
  AlertDialogFooter: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AlertDialogCancel: ({
    children,
    disabled,
  }: {
    children: ReactNode;
    disabled?: boolean;
  }) => (
    <button type="button" disabled={disabled}>
      {children}
    </button>
  ),
  AlertDialogAction: ({
    children,
    disabled,
    onClick,
  }: {
    children: ReactNode;
    disabled?: boolean;
    variant?: string;
    onClick?: () => void;
  }) => (
    <button type="button" disabled={disabled} onClick={onClick}>
      {children}
    </button>
  ),
}));

const TEST_RESOURCES = {
  en: { common: enCommon, squads: enSquads },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function render(ui: React.ReactElement, options?: RenderOptions) {
  return rtlRender(ui, { wrapper: I18nWrapper, ...options });
}

import { ArchiveSquadConfirmDialog } from "./archive-squad-confirm-dialog";

describe("ArchiveSquadConfirmDialog", () => {
  it("shows leader name and count when count is provided", () => {
    render(
      <ArchiveSquadConfirmDialog
        open
        squadName="Squirtle"
        leaderName="Squirtle-Leader"
        issueCount={3}
        onCancel={() => {}}
        onConfirm={async () => {}}
        pending={false}
      />,
    );
    expect(screen.getByText(/Squirtle-Leader/)).toBeInTheDocument();
    expect(screen.getByText(/3 issues/)).toBeInTheDocument();
  });

  it("falls back to no-count copy when issueCount is null", () => {
    render(
      <ArchiveSquadConfirmDialog
        open
        squadName="S"
        leaderName="L"
        issueCount={null}
        onCancel={() => {}}
        onConfirm={async () => {}}
        pending={false}
      />,
    );
    expect(screen.getByText(/all issues currently assigned/i)).toBeInTheDocument();
  });

  it("disables Cancel and shows the archiving label while pending", () => {
    render(
      <ArchiveSquadConfirmDialog
        open
        squadName="S"
        leaderName="L"
        issueCount={1}
        onCancel={() => {}}
        onConfirm={async () => {}}
        pending
      />,
    );
    expect(screen.getByRole("button", { name: /archiving/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /cancel/i })).toBeDisabled();
  });

  it("calls onConfirm when the Archive button is clicked", async () => {
    const onConfirm = vi.fn().mockResolvedValue(undefined);
    render(
      <ArchiveSquadConfirmDialog
        open
        squadName="S"
        leaderName="L"
        issueCount={1}
        onCancel={() => {}}
        onConfirm={onConfirm}
        pending={false}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /^archive$/i }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("uses the singular copy variant when count is 1", () => {
    render(
      <ArchiveSquadConfirmDialog
        open
        squadName="S"
        leaderName="L"
        issueCount={1}
        onCancel={() => {}}
        onConfirm={async () => {}}
        pending={false}
      />,
    );
    expect(screen.getByText(/take over 1 issue\b/)).toBeInTheDocument();
  });
});
