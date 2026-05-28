"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { IntegrationKind } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../../navigation";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";
import { KindConfigForm, hasFormForKind } from "./config-forms";
import { SecretManagerSection } from "./secret-manager-section";
import { IntegrationActions } from "./integration-actions";
import { DeploymentTimeline } from "./deployment-timeline";

// Detail page for one integration (Plan 4 / PR D, Tasks D-12/D-13/D-14
// combined into a single screen to keep the v1 web surface compact).

export function IntegrationDetailPage({ id }: { id: string }) {
  const workspaceId = useWorkspaceId();
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const { t } = useT("integrations");

  const integrationQuery = useQuery({
    queryKey: [workspaceId, "integrations", id],
    queryFn: () => api.getIntegration(id),
    enabled: !!workspaceId && !!id,
  });
  const statusQuery = useQuery({
    queryKey: [workspaceId, "integrations", id, "status"],
    queryFn: () => api.getIntegrationStatus(id),
    enabled: !!workspaceId && !!id,
    refetchInterval: 10000,
  });

  if (integrationQuery.isLoading) {
    return (
      <div className="p-6">
        <Skeleton className="h-8 w-1/3" />
        <Skeleton className="mt-4 h-32" />
      </div>
    );
  }
  if (integrationQuery.isError || !integrationQuery.data) {
    return (
      <div className="p-6">
        <p className="text-sm text-destructive">{t(($) => $.detail.load_failed)}</p>
        <Button variant="link" onClick={() => navigation.push(paths.integrations())}>
          {t(($) => $.detail.back)}
        </Button>
      </div>
    );
  }

  const integration = integrationQuery.data;
  const status = statusQuery.data;

  return (
    <div className="flex h-full flex-col">
      <PageHeader className="justify-between px-5">
        <div>
          <h2 className="text-base font-semibold">{integration.name}</h2>
          <p className="text-xs text-muted-foreground">
            {integration.kind} · v{integration.version}
          </p>
        </div>
        <IntegrationActions integrationId={id} />
      </PageHeader>

      <div className="space-y-8 p-6">
        <section>
          <h3 className="mb-2 text-sm font-semibold uppercase text-muted-foreground">
            {t(($) => $.detail.status_heading)}
          </h3>
          <div className="rounded-md border bg-card p-3 text-sm">
            <p>
              {t(($) => $.detail.status_label)}:{" "}
              <code>{status?.integration_status ?? integration.status}</code>
            </p>
            <p>
              {t(($) => $.detail.config_version_label)}: v
              {status?.config_version ?? integration.version}
            </p>
            {status?.active_deployment ? (
              <p>
                {t(($) => $.detail.active_deployment_label)}:{" "}
                <code>{status.active_deployment.image_or_commit}</code> ·{" "}
                {status.active_deployment.status} · {t(($) => $.detail.last_heartbeat)}{" "}
                {status.active_deployment.last_heartbeat ?? "—"}
              </p>
            ) : (
              <p className="text-muted-foreground">{t(($) => $.detail.no_active_deployment)}</p>
            )}
          </div>
        </section>

        <ConfigEditor
          integrationId={id}
          kind={integration.kind}
          initialConfig={integration.config}
        />

        <SecretManagerSection integrationId={id} />

        <section>
          <h3 className="mb-2 text-sm font-semibold uppercase text-muted-foreground">
            {t(($) => $.detail.deployment_history)}
          </h3>
          <DeploymentTimeline integrationId={id} />
        </section>
      </div>
    </div>
  );
}

// --- config editor: per-kind form OR raw JSON textarea ---
//
// Two modes share one source of truth: the parsed config object held in local
// state. Switching modes serializes / deserializes; if the textarea contains
// invalid JSON when switching back to Form, the toggle stays in JSON mode
// with an inline error so user input is never silently dropped.

type EditorMode = "form" | "json";

function ConfigEditor({
  integrationId,
  kind,
  initialConfig,
}: {
  integrationId: string;
  kind: IntegrationKind;
  initialConfig: Record<string, unknown>;
}) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const { t } = useT("integrations");
  const hasForm = hasFormForKind(kind);

  // Single source of truth = the parsed JS object.
  const [config, setConfig] = useState<Record<string, unknown>>(() => initialConfig);
  // Mirror for the textarea so the user can keep typing freely without our
  // JSON.stringify reformatting between keystrokes.
  const [text, setText] = useState(() => JSON.stringify(initialConfig, null, 2));
  const [mode, setMode] = useState<EditorMode>(hasForm ? "form" : "json");
  const [error, setError] = useState<string | null>(null);

  // When the server returns a new config (e.g. after save or via WS event),
  // resync both states.
  const initialText = useMemo(() => JSON.stringify(initialConfig, null, 2), [initialConfig]);
  useEffect(() => {
    setConfig(initialConfig);
    setText(initialText);
    setError(null);
  }, [initialConfig, initialText]);

  const save = useMutation({
    mutationFn: (cfg: Record<string, unknown>) => api.updateIntegrationConfig(integrationId, cfg),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [workspaceId, "integrations", integrationId] });
      setError(null);
    },
    onError: (e: Error) => setError(e.message),
  });

  // Switching mode: form → json is always safe (stringify the object). json →
  // form requires the textarea to parse cleanly; otherwise we surface the
  // error and stay in JSON mode.
  const switchTo = (next: EditorMode) => {
    if (next === mode) return;
    if (next === "form") {
      try {
        const parsed = JSON.parse(text);
        if (typeof parsed !== "object" || parsed === null) {
          setError(t(($) => $.config.switch_form_needs_object));
          return;
        }
        setConfig(parsed as Record<string, unknown>);
        setError(null);
        setMode("form");
      } catch (e) {
        setError(t(($) => $.config.switch_form_invalid_json, { message: (e as Error).message }));
      }
    } else {
      setText(JSON.stringify(config, null, 2));
      setError(null);
      setMode("json");
    }
  };

  return (
    <section>
      <div className="mb-2 flex items-center justify-between">
        <h3 className="text-sm font-semibold uppercase text-muted-foreground">
          {t(($) => $.config.heading)}
        </h3>
        {hasForm && (
          <div className="flex gap-1 rounded-md border bg-card p-0.5">
            <Button
              size="sm"
              variant={mode === "form" ? "default" : "ghost"}
              className="h-7 px-3 text-xs"
              onClick={() => switchTo("form")}
            >
              {t(($) => $.config.mode_form)}
            </Button>
            <Button
              size="sm"
              variant={mode === "json" ? "default" : "ghost"}
              className="h-7 px-3 text-xs"
              onClick={() => switchTo("json")}
            >
              {t(($) => $.config.mode_json)}
            </Button>
          </div>
        )}
      </div>

      {mode === "form" && hasForm ? (
        <div className="rounded-md border bg-card p-4">
          <KindConfigForm kind={kind} value={config} onChange={setConfig} />
        </div>
      ) : (
        <textarea
          className="h-64 w-full rounded-md border bg-card p-3 font-mono text-xs"
          value={text}
          spellCheck={false}
          onChange={(e) => setText(e.target.value)}
        />
      )}

      {error && <p className="mt-2 text-xs text-destructive">{error}</p>}

      <div className="mt-2 flex justify-end gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            setConfig(initialConfig);
            setText(initialText);
            setError(null);
          }}
        >
          {t(($) => $.config.reset)}
        </Button>
        <Button
          size="sm"
          onClick={() => {
            if (mode === "json") {
              try {
                const parsed = JSON.parse(text);
                if (typeof parsed !== "object" || parsed === null) {
                  setError(t(($) => $.config.must_be_object));
                  return;
                }
                save.mutate(parsed as Record<string, unknown>);
              } catch (e) {
                setError(t(($) => $.config.invalid_json, { message: (e as Error).message }));
              }
            } else {
              save.mutate(config);
            }
          }}
          disabled={save.isPending}
        >
          {save.isPending ? t(($) => $.config.saving) : t(($) => $.config.save)}
        </Button>
      </div>
    </section>
  );
}

// SecretsSection / AddSecretForm extracted to secret-manager-section.tsx
// IntegrationActions extracted to integration-actions.tsx
// DeploymentTimeline added in deployment-timeline.tsx
// ConfigEditor remains here — heavily coupled to the page's local mode state.
