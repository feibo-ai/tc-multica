"use client";

import { Input } from "@multica/ui/components/ui/input";

// Autopilot bot config: the metadata a multica autopilot needs to find its
// own running instance, the schedule, and the target workspace. Webhook
// signing secrets go to the Secrets section.

interface AutopilotBotConfig {
  bot_name?: string;
  enabled?: boolean;
  schedule?: string;
  target_workspace_slug?: string;
  description?: string;
  health_check_url?: string;
}

interface Props {
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

export function AutopilotBotForm({ value, onChange }: Props) {
  const cfg = value as AutopilotBotConfig;
  const setField = <K extends keyof AutopilotBotConfig>(key: K, next: AutopilotBotConfig[K]) => {
    const merged: AutopilotBotConfig = { ...cfg, [key]: next };
    if (next === "" || next === undefined) {
      delete merged[key];
    }
    onChange(merged as Record<string, unknown>);
  };

  return (
    <div className="space-y-4">
      <Field label="Bot name" hint="Display name shown in autopilot dashboards.">
        <Input
          value={cfg.bot_name ?? ""}
          placeholder="weekly-debrief-bot"
          onChange={(e) => setField("bot_name", e.target.value)}
        />
      </Field>

      <Field label="Enabled">
        <select
          value={cfg.enabled === false ? "false" : "true"}
          onChange={(e) => setField("enabled", e.target.value === "true")}
          className="w-full rounded border bg-background p-2 text-sm"
        >
          <option value="true">enabled</option>
          <option value="false">disabled (paused)</option>
        </select>
      </Field>

      <Field
        label="Schedule"
        hint='Cron expression in 5-field form (e.g. "0 9 * * 1" — every Monday 09:00) or "manual" for trigger-only.'
      >
        <Input
          value={cfg.schedule ?? ""}
          placeholder="0 9 * * 1"
          onChange={(e) => setField("schedule", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field
        label="Target workspace slug"
        hint="Workspace the bot acts on. Leave empty to default to the workspace owning this integration."
      >
        <Input
          value={cfg.target_workspace_slug ?? ""}
          placeholder="acme"
          onChange={(e) => setField("target_workspace_slug", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field label="Description" hint="One-line summary for the bot list.">
        <Input
          value={cfg.description ?? ""}
          placeholder="Posts Friday demo summary to feishu team chat."
          onChange={(e) => setField("description", e.target.value)}
        />
      </Field>

      <Field
        label="Health check URL"
        hint="Optional. multica pings this URL to confirm the bot's runtime is alive."
      >
        <Input
          value={cfg.health_check_url ?? ""}
          placeholder="https://bot.example.com/health"
          onChange={(e) => setField("health_check_url", e.target.value)}
        />
      </Field>

      <div className="rounded-md border bg-muted/40 p-3 text-xs text-muted-foreground">
        <strong className="text-foreground">Secrets:</strong> any signing secrets
        (e.g. <code className="mx-1 rounded bg-background px-1 py-0.5">WEBHOOK_SIGNING_SECRET</code>)
        or API tokens the bot needs go in the <em>Secrets</em> section below.
      </div>
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
