"use client";

import type { IntegrationKind } from "@multica/core/types";
import { McpServerForm } from "./mcp-server-form";
import { FeishuForm } from "./feishu-form";
import { AutopilotBotForm } from "./autopilot-bot-form";

// Dispatcher: render the per-kind form. Unknown kinds (e.g. a future kind
// shipped by a newer server) fall back to null — the caller switches the UI
// to raw-JSON mode in that case.

interface Props {
  kind: IntegrationKind;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

export function KindConfigForm({ kind, value, onChange }: Props) {
  switch (kind) {
    case "mcp-server":
      return <McpServerForm value={value} onChange={onChange} />;
    case "feishu":
      return <FeishuForm value={value} onChange={onChange} />;
    case "autopilot-bot":
      return <AutopilotBotForm value={value} onChange={onChange} />;
    default:
      // New kind we don't recognize. Caller should fall back to raw-JSON mode.
      return null;
  }
}

// Helper for the caller to decide whether to show the toggle at all.
export function hasFormForKind(kind: string): kind is IntegrationKind {
  return kind === "mcp-server" || kind === "feishu" || kind === "autopilot-bot";
}
