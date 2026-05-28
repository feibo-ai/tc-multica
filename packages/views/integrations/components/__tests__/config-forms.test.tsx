// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { hasFormForKind, KindConfigForm } from "../config-forms";

describe("hasFormForKind type guard", () => {
  it("returns true for the three supported kinds", () => {
    expect(hasFormForKind("mcp-server")).toBe(true);
    expect(hasFormForKind("feishu")).toBe(true);
    expect(hasFormForKind("autopilot-bot")).toBe(true);
  });

  it("returns false for unknown kinds", () => {
    expect(hasFormForKind("not-a-real-kind")).toBe(false);
    expect(hasFormForKind("")).toBe(false);
  });
});

describe("KindConfigForm dispatcher", () => {
  it("renders the MCP server form for kind=mcp-server", () => {
    render(<KindConfigForm kind="mcp-server" value={{}} onChange={vi.fn()} />);
    expect(screen.queryByText("Transport")).toBeTruthy();
    expect(screen.queryByText("Command")).toBeTruthy();
  });

  it("renders the feishu form for kind=feishu", () => {
    render(<KindConfigForm kind="feishu" value={{}} onChange={vi.fn()} />);
    expect(screen.queryByText("App ID")).toBeTruthy();
    expect(screen.queryByText(/Team chat ID/i)).toBeTruthy();
  });

  it("renders the autopilot-bot form for kind=autopilot-bot", () => {
    render(<KindConfigForm kind="autopilot-bot" value={{}} onChange={vi.fn()} />);
    expect(screen.queryByText("Bot name")).toBeTruthy();
    expect(screen.queryByText("Schedule")).toBeTruthy();
  });
});

describe("MCP server form behavior", () => {
  it("emits onChange when a field is edited", () => {
    const onChange = vi.fn();
    render(<KindConfigForm kind="mcp-server" value={{}} onChange={onChange} />);

    const commandInput = screen.getByPlaceholderText("node") as HTMLInputElement;
    fireEvent.change(commandInput, { target: { value: "python" } });

    expect(onChange).toHaveBeenCalled();
    const lastCall = onChange.mock.calls[onChange.mock.calls.length - 1];
    expect(lastCall?.[0]?.command).toBe("python");
  });

  it("hides command/args/env when transport switches to non-stdio", () => {
    const Wrapper = () => {
      const [val, setVal] = (require("react") as typeof import("react")).useState<
        Record<string, unknown>
      >({ transport: "stdio", command: "node" });
      return <KindConfigForm kind="mcp-server" value={val} onChange={setVal} />;
    };
    render(<Wrapper />);
    expect(screen.queryByText("Command")).toBeTruthy();

    fireEvent.change(screen.getByRole("combobox"), { target: { value: "sse" } });
    expect(screen.queryByText("Command")).toBeNull();
    expect(screen.queryByText("URL")).toBeTruthy();
  });
});
