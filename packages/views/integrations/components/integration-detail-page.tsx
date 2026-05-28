"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, RefreshCw, Trash2 } from "lucide-react";
import type { SecretValue } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../../navigation";
import { PageHeader } from "../../layout/page-header";

// Detail page for one integration (Plan 4 / PR D, Tasks D-12/D-13/D-14
// combined into a single screen to keep the v1 web surface compact).

export function IntegrationDetailPage({ id }: { id: string }) {
  const workspaceId = useWorkspaceId();
  const navigation = useNavigation();

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
        <p className="text-sm text-destructive">Failed to load integration.</p>
        <Button variant="link" onClick={() => navigation.push("/integrations")}>
          Back to integrations
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
        <HeaderActions integrationId={id} />
      </PageHeader>

      <div className="space-y-8 p-6">
        <section>
          <h3 className="mb-2 text-sm font-semibold uppercase text-muted-foreground">Status</h3>
          <div className="rounded-md border bg-card p-3 text-sm">
            <p>
              Integration status: <code>{status?.integration_status ?? integration.status}</code>
            </p>
            <p>Config version: v{status?.config_version ?? integration.version}</p>
            {status?.active_deployment ? (
              <p>
                Active deployment: <code>{status.active_deployment.image_or_commit}</code> ·{" "}
                {status.active_deployment.status} · last heartbeat{" "}
                {status.active_deployment.last_heartbeat ?? "—"}
              </p>
            ) : (
              <p className="text-muted-foreground">No active deployment.</p>
            )}
          </div>
        </section>

        <ConfigEditor integrationId={id} initialConfig={integration.config} />

        <SecretsSection integrationId={id} />
      </div>
    </div>
  );
}

// --- header actions: restart / redeploy / delete ---

function HeaderActions({ integrationId }: { integrationId: string }) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const navigation = useNavigation();

  const restart = useMutation({
    mutationFn: () => api.restartIntegration(integrationId),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: [workspaceId, "integrations", integrationId] }),
  });
  const redeploy = useMutation({
    mutationFn: () => api.redeployIntegration(integrationId),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: [workspaceId, "integrations", integrationId] }),
  });
  const del = useMutation({
    mutationFn: () => api.deleteIntegration(integrationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [workspaceId, "integrations"] });
      navigation.push("/integrations");
    },
  });

  return (
    <div className="flex gap-2">
      <Button variant="outline" size="sm" onClick={() => restart.mutate()} disabled={restart.isPending}>
        <RefreshCw className="mr-1 size-4" />
        Restart
      </Button>
      <Button variant="outline" size="sm" onClick={() => redeploy.mutate()} disabled={redeploy.isPending}>
        Redeploy
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => {
          if (window.confirm("Delete this integration and all its secrets?")) {
            del.mutate();
          }
        }}
      >
        <Trash2 className="size-4 text-destructive" />
      </Button>
    </div>
  );
}

// --- config editor: textarea + save (no Monaco — keeps bundle small) ---

function ConfigEditor({
  integrationId,
  initialConfig,
}: {
  integrationId: string;
  initialConfig: Record<string, unknown>;
}) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const initialText = useMemo(() => JSON.stringify(initialConfig, null, 2), [initialConfig]);
  const [text, setText] = useState(initialText);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setText(initialText);
  }, [initialText]);

  const save = useMutation({
    mutationFn: (cfg: Record<string, unknown>) => api.updateIntegrationConfig(integrationId, cfg),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [workspaceId, "integrations", integrationId] });
      setError(null);
    },
    onError: (e: Error) => setError(e.message),
  });

  return (
    <section>
      <h3 className="mb-2 text-sm font-semibold uppercase text-muted-foreground">Config</h3>
      <textarea
        className="h-64 w-full rounded-md border bg-card p-3 font-mono text-xs"
        value={text}
        spellCheck={false}
        onChange={(e) => setText(e.target.value)}
      />
      {error && <p className="mt-2 text-xs text-destructive">{error}</p>}
      <div className="mt-2 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={() => setText(initialText)}>
          Reset
        </Button>
        <Button
          size="sm"
          onClick={() => {
            try {
              const parsed = JSON.parse(text);
              if (typeof parsed !== "object" || parsed === null) {
                setError("Config must be a JSON object");
                return;
              }
              save.mutate(parsed);
            } catch (e) {
              setError(`Invalid JSON: ${(e as Error).message}`);
            }
          }}
          disabled={save.isPending}
        >
          {save.isPending ? "Saving…" : "Save config"}
        </Button>
      </div>
    </section>
  );
}

// --- secrets section: list + add + click-to-reveal + delete ---

function SecretsSection({ integrationId }: { integrationId: string }) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const [adding, setAdding] = useState(false);
  const [revealing, setRevealing] = useState<string | null>(null);
  const [revealed, setRevealed] = useState<SecretValue | null>(null);

  const keysQuery = useQuery({
    queryKey: [workspaceId, "integrations", integrationId, "secrets"],
    queryFn: () => api.listIntegrationSecretKeys(integrationId),
    enabled: !!workspaceId && !!integrationId,
  });

  const reveal = useMutation({
    mutationFn: (key: string) => api.getIntegrationSecret(integrationId, key),
    onSuccess: (data) => {
      setRevealed(data);
      // Auto-hide after 30s so the plaintext doesn't linger on screen.
      window.setTimeout(() => {
        setRevealed(null);
        setRevealing(null);
      }, 30_000);
    },
  });

  const remove = useMutation({
    mutationFn: (key: string) => api.deleteIntegrationSecret(integrationId, key),
    onSuccess: () =>
      qc.invalidateQueries({
        queryKey: [workspaceId, "integrations", integrationId, "secrets"],
      }),
  });

  return (
    <section>
      <div className="mb-2 flex items-center justify-between">
        <h3 className="text-sm font-semibold uppercase text-muted-foreground">Secrets</h3>
        <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
          + Add secret
        </Button>
      </div>
      {keysQuery.isLoading ? (
        <Skeleton className="h-12" />
      ) : keysQuery.data && keysQuery.data.length === 0 ? (
        <p className="text-sm text-muted-foreground">No secrets yet.</p>
      ) : (
        <ul className="divide-y rounded-md border bg-card text-sm">
          {keysQuery.data?.map((k) => (
            <li key={k.key} className="flex items-center justify-between px-3 py-2">
              <div className="flex flex-1 items-center gap-3 font-mono text-xs">
                <span>{k.key}</span>
                <span className="text-muted-foreground">v{k.version}</span>
                {revealing === k.key && revealed?.key === k.key && (
                  <span className="text-emerald-600">{revealed.value}</span>
                )}
              </div>
              <div className="flex gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => {
                    if (revealing === k.key) {
                      setRevealing(null);
                      setRevealed(null);
                    } else {
                      setRevealing(k.key);
                      reveal.mutate(k.key);
                    }
                  }}
                  title="Reveal (auto-hides after 30s)"
                >
                  {revealing === k.key ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => {
                    if (window.confirm(`Delete secret ${k.key}?`)) remove.mutate(k.key);
                  }}
                  title="Delete"
                >
                  <Trash2 className="size-4 text-destructive" />
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {adding && <AddSecretForm integrationId={integrationId} onClose={() => setAdding(false)} />}
    </section>
  );
}

function AddSecretForm({
  integrationId,
  onClose,
}: {
  integrationId: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const mut = useMutation({
    mutationFn: () => api.upsertIntegrationSecret(integrationId, key, value),
    onSuccess: () => {
      qc.invalidateQueries({
        queryKey: [workspaceId, "integrations", integrationId, "secrets"],
      });
      onClose();
    },
    onError: (e: Error) => setError(e.message),
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-96 rounded-md bg-background p-6 shadow-lg">
        <h3 className="mb-3 text-lg font-semibold">Add secret</h3>
        <label className="block text-xs text-muted-foreground">Key (UPPER_SNAKE_CASE)</label>
        <Input
          className="mb-3 font-mono"
          value={key}
          placeholder="FEISHU_APP_SECRET"
          onChange={(e) => setKey(e.target.value.toUpperCase())}
        />
        <label className="block text-xs text-muted-foreground">Value</label>
        <textarea
          className="mb-3 h-24 w-full rounded border bg-background p-2 font-mono text-xs"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        {error && <p className="mb-3 text-xs text-destructive">{error}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => {
              if (!/^[A-Z][A-Z0-9_]{0,127}$/.test(key)) {
                setError("Key must match ^[A-Z][A-Z0-9_]+$");
                return;
              }
              if (!value) {
                setError("Value is required");
                return;
              }
              mut.mutate();
            }}
            disabled={mut.isPending}
          >
            {mut.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>
    </div>
  );
}
