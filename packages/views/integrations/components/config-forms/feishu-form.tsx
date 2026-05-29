"use client";

import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../../i18n";

// Feishu / Lark config shape (feishu open platform).
// Secret credentials (FEISHU_APP_SECRET / FEISHU_VERIFICATION_TOKEN /
// FEISHU_ENCRYPT_KEY) are NOT here — they live in the Secrets manager
// section of the detail page. This form is for the non-secret IDs only.

interface FeishuConfig {
  app_id?: string;
  domain?: "feishu" | "lark";
  base_url?: string;
  team_chat_id?: string;
  tenant_key?: string;
  webhook_url?: string;
}

interface Props {
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

export function FeishuForm({ value, onChange }: Props) {
  const { t } = useT("integrations");
  const cfg = value as FeishuConfig;
  const setField = <K extends keyof FeishuConfig>(key: K, next: FeishuConfig[K]) => {
    const merged: FeishuConfig = { ...cfg, [key]: next };
    if (next === "" || next === undefined) {
      delete merged[key];
    }
    onChange(merged as Record<string, unknown>);
  };

  const domain = cfg.domain ?? "feishu";

  return (
    <div className="space-y-4">
      <Field label="App ID" hint="From feishu.cn / lark.com developer console. e.g. cli_xxxxxxxxxxxxxxx">
        <Input
          value={cfg.app_id ?? ""}
          placeholder="cli_xxxxxxxxxxxxxxx"
          onChange={(e) => setField("app_id", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field label="Domain">
        <select
          value={domain}
          onChange={(e) => setField("domain", e.target.value as FeishuConfig["domain"])}
          className="w-full rounded border bg-background p-2 text-sm"
        >
          <option value="feishu">{t(($) => $.forms.feishu.domain_feishu)}</option>
          <option value="lark">{t(($) => $.forms.feishu.domain_lark)}</option>
        </select>
      </Field>

      <Field
        label="Base URL"
        hint="Override the API host. Leave empty to use the default for the selected domain."
      >
        <Input
          value={cfg.base_url ?? ""}
          placeholder={domain === "feishu" ? "https://open.feishu.cn" : "https://open.larksuite.com"}
          onChange={(e) => setField("base_url", e.target.value)}
        />
      </Field>

      <Field
        label="Team chat ID"
        hint="The OAuth-resolved chat the bot belongs to. e.g. oc_xxxxxxxxxxxxxx"
      >
        <Input
          value={cfg.team_chat_id ?? ""}
          placeholder="oc_xxxxxxxxxxxxxx"
          onChange={(e) => setField("team_chat_id", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field label="Tenant key" hint="Optional. Set when integrating a multi-tenant store app.">
        <Input
          value={cfg.tenant_key ?? ""}
          placeholder="t_xxxxxxxxxxxxxx"
          onChange={(e) => setField("tenant_key", e.target.value)}
          className="font-mono"
        />
      </Field>

      <Field
        label="Webhook URL"
        hint="Incoming webhook URL for posting messages from automation. (Custom Bot URL.)"
      >
        <Input
          value={cfg.webhook_url ?? ""}
          placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxx"
          onChange={(e) => setField("webhook_url", e.target.value)}
        />
      </Field>

      <div className="rounded-md border bg-muted/40 p-3 text-xs text-muted-foreground">
        {t(($) => $.forms.feishu.secrets_hint)}
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
