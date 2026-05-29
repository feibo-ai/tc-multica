"use client";

import { useEffect, useState } from "react";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Trash2 } from "lucide-react";
import { useT } from "../../../i18n";

// MCP-server config shape (Anthropic Model Context Protocol).
// All fields land at the top level of the integration's `config` JSON. Secrets
// (API tokens, etc.) go through the separate Secrets manager, not here.

interface McpServerConfig {
  transport?: "stdio" | "sse" | "http";
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  working_dir?: string;
  url?: string;
}

interface Props {
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

export function McpServerForm({ value, onChange }: Props) {
  const { t } = useT("integrations");
  const cfg = value as McpServerConfig;
  const transport = (cfg.transport ?? "stdio") as "stdio" | "sse" | "http";
  const isStdio = transport === "stdio";

  // Local state for the args list to keep input handling responsive; the
  // parent gets the full record on every keystroke via setField.
  const setField = <K extends keyof McpServerConfig>(key: K, next: McpServerConfig[K]) => {
    const merged: McpServerConfig = { ...cfg, [key]: next };
    // Drop empty optionals so the JSON stays clean.
    if (next === "" || next === undefined || (Array.isArray(next) && next.length === 0)) {
      delete merged[key];
    }
    onChange(merged as Record<string, unknown>);
  };

  return (
    <div className="space-y-4">
      <Field label="Transport">
        <select
          value={transport}
          onChange={(e) => setField("transport", e.target.value as McpServerConfig["transport"])}
          className="w-full rounded border bg-background p-2 text-sm"
        >
          <option value="stdio">{t(($) => $.forms.mcp.transport_stdio)}</option>
          <option value="sse">{t(($) => $.forms.mcp.transport_sse)}</option>
          <option value="http">{t(($) => $.forms.mcp.transport_http)}</option>
        </select>
      </Field>

      {isStdio ? (
        <>
          <Field label="Command" hint="The binary to spawn, e.g. node / python / path to executable">
            <Input
              value={cfg.command ?? ""}
              placeholder="node"
              onChange={(e) => setField("command", e.target.value)}
            />
          </Field>
          <ArgsEditor value={cfg.args ?? []} onChange={(args) => setField("args", args)} />
          <EnvEditor value={cfg.env ?? {}} onChange={(env) => setField("env", env)} />
          <Field label="Working directory" hint="Optional. Defaults to the runtime process cwd.">
            <Input
              value={cfg.working_dir ?? ""}
              placeholder="/path/to/dir"
              onChange={(e) => setField("working_dir", e.target.value)}
            />
          </Field>
        </>
      ) : (
        <Field label="URL" hint={`${transport.toUpperCase()} endpoint`}>
          <Input
            value={cfg.url ?? ""}
            placeholder="https://example.com/mcp"
            onChange={(e) => setField("url", e.target.value)}
          />
        </Field>
      )}
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="mb-1 block text-xs font-medium text-muted-foreground">{label}</label>
      {children}
      {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

function ArgsEditor({ value, onChange }: { value: string[]; onChange: (next: string[]) => void }) {
  const { t } = useT("integrations");
  return (
    <Field
      label="Arguments"
      hint="One CLI argument per row. e.g. ./mcp-server.js · --port · 9000"
    >
      <div className="space-y-1">
        {value.map((arg, idx) => (
          <div key={idx} className="flex gap-1">
            <Input
              value={arg}
              onChange={(e) => {
                const next = [...value];
                next[idx] = e.target.value;
                onChange(next);
              }}
              className="font-mono text-xs"
            />
            <Button
              variant="ghost"
              size="icon"
              onClick={() => onChange(value.filter((_, i) => i !== idx))}
              title="Remove"
            >
              <Trash2 className="size-3" />
            </Button>
          </div>
        ))}
        <Button variant="outline" size="sm" onClick={() => onChange([...value, ""])}>
          {t(($) => $.forms.mcp.add_argument)}
        </Button>
      </div>
    </Field>
  );
}

function EnvEditor({
  value,
  onChange,
}: {
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}) {
  const { t } = useT("integrations");
  // Local mirror so KEY rename doesn't lose focus mid-keystroke.
  const [rows, setRows] = useState<Array<[string, string]>>(() => Object.entries(value));
  useEffect(() => {
    setRows(Object.entries(value));
  }, [value]);

  const flush = (next: Array<[string, string]>) => {
    const obj: Record<string, string> = {};
    for (const [k, v] of next) {
      if (k.trim()) obj[k] = v;
    }
    onChange(obj);
  };

  return (
    <Field
      label="Environment variables"
      hint="Non-secret env. Secret API tokens go to the Secrets section below."
    >
      <div className="space-y-1">
        {rows.map(([k, v], idx) => (
          <div key={idx} className="flex gap-1">
            <Input
              value={k}
              placeholder="KEY"
              onChange={(e) => {
                const next = [...rows];
                next[idx] = [e.target.value, v];
                setRows(next);
              }}
              onBlur={() => flush(rows)}
              className="w-1/3 font-mono text-xs uppercase"
            />
            <Input
              value={v}
              placeholder="value"
              onChange={(e) => {
                const next = [...rows];
                next[idx] = [k, e.target.value];
                setRows(next);
                flush(next);
              }}
              className="flex-1 font-mono text-xs"
            />
            <Button
              variant="ghost"
              size="icon"
              onClick={() => flush(rows.filter((_, i) => i !== idx))}
              title="Remove"
            >
              <Trash2 className="size-3" />
            </Button>
          </div>
        ))}
        <Button variant="outline" size="sm" onClick={() => setRows([...rows, ["", ""]])}>
          {t(($) => $.forms.mcp.add_variable)}
        </Button>
      </div>
    </Field>
  );
}
