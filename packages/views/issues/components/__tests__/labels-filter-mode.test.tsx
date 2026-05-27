import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enIssues from "../../../locales/en/issues.json";

// The component lives in `issues-header.tsx`. Its host file does some heavy
// pulls (workspace queries, full UI menu chrome) that aren't relevant to the
// behavior under test — but importing one named export only triggers those
// modules to resolve, not to run, so the lightweight test stays cheap.
import { LabelFilterModeToggle } from "../issues-header";

// IssuesHeader imports useWorkspaceId for the labels query (it's read at
// render time inside LabelSubContent, not LabelFilterModeToggle). The
// extracted toggle itself doesn't need it, but the module-level import
// must resolve — so we stub it once.
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// Same reason: issues-header imports queries from these modules. We don't
// invoke them in the toggle, but the imports need to resolve.
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["m"], queryFn: () => [] }),
  agentListOptions: () => ({ queryKey: ["a"], queryFn: () => [] }),
  squadListOptions: () => ({ queryKey: ["s"], queryFn: () => [] }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: () => ({ queryKey: ["p"], queryFn: () => [] }),
}));
vi.mock("@multica/core/labels/queries", () => ({
  labelListOptions: () => ({ queryKey: ["l"], queryFn: () => [] }),
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

function renderToggle(value: "any" | "all", onChange = vi.fn()) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <LabelFilterModeToggle value={value} onChange={onChange} />
    </I18nProvider>,
  );
}

describe("LabelFilterModeToggle", () => {
  it("renders both Any and All options", () => {
    renderToggle("any");
    expect(screen.getByRole("radio", { name: /any/i })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /all/i })).toBeInTheDocument();
  });

  it("marks Any as the active option by default", () => {
    renderToggle("any");
    expect(screen.getByRole("radio", { name: /any/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(screen.getByRole("radio", { name: /all/i })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("fires onChange with 'all' when the All option is clicked", () => {
    const onChange = vi.fn();
    renderToggle("any", onChange);
    fireEvent.click(screen.getByRole("radio", { name: /all/i }));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith("all");
  });

  it("does not fire onChange when clicking the already-active option", () => {
    const onChange = vi.fn();
    renderToggle("any", onChange);
    fireEvent.click(screen.getByRole("radio", { name: /any/i }));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("fires onChange with 'any' when switching back from All", () => {
    const onChange = vi.fn();
    renderToggle("all", onChange);
    fireEvent.click(screen.getByRole("radio", { name: /any/i }));
    expect(onChange).toHaveBeenCalledWith("any");
  });
});
