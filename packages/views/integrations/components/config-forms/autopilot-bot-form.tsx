"use client";

import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../../i18n";

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
  const { t } = useT("integrations");
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
      <Field
        label={t(($) => $.forms.autopilot.bot_name_label)}
        hint={t(($) => $.forms.autopilot.bot_name_hint)}
      >
        <Input
          value={cfg.bot_name ?? ""}
          placeholder="weekly-debrief-bot"
          onChange={(e) => setField("bot_name", e.target.value)}
        />
      </Field>

      <Field label={t(($) => $.forms.autopilot.enabled_label)}>
        <select
          value={cfg.enabled === false ? "false" : "true"}
          onChange={(e) => setField("enabled", e.target.value === "true")}
          className="w-full rounded border bg-background p-2 text-sm"
        >
          <option value="true">{t(($) => $.forms.autopilot.enabled)}</option>
          <option value="false">{t(($) => $.forms.autopilot.disabled)}</option>
        </select>
      </Field>

      <Field
        label={t(($) => $.forms.autopilot.schedule_label)}
        hint={t(($) => $.forms.autopilot.schedule_hint)}
      >
        <Input
          value={cfg.schedule ?? ""}
          placeholder="0 9 * * 1"
          onChange={(e) => setField("schedule", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field
        label={t(($) => $.forms.autopilot.target_workspace_label)}
        hint={t(($) => $.forms.autopilot.target_workspace_hint)}
      >
        <Input
          value={cfg.target_workspace_slug ?? ""}
          placeholder="acme"
          onChange={(e) => setField("target_workspace_slug", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field
        label={t(($) => $.forms.autopilot.description_label)}
        hint={t(($) => $.forms.autopilot.description_hint)}
      >
        <Input
          value={cfg.description ?? ""}
          placeholder="Posts Friday demo summary to feishu team chat."
          onChange={(e) => setField("description", e.target.value)}
        />
      </Field>

      <Field
        label={t(($) => $.forms.autopilot.health_check_label)}
        hint={t(($) => $.forms.autopilot.health_check_hint)}
      >
        <Input
          value={cfg.health_check_url ?? ""}
          placeholder="https://bot.example.com/health"
          onChange={(e) => setField("health_check_url", e.target.value)}
        />
      </Field>

      <div className="rounded-md border bg-muted/40 p-3 text-xs text-muted-foreground">
        {t(($) => $.forms.autopilot.secrets_hint)}
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
