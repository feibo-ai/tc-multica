"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, Trash2 } from "lucide-react";
import type { SecretValue } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

// Secret manager UI for one integration. Lists keys (no values), supports
// click-to-reveal with 30s auto-hide, add, and delete.
//
// Auto-hide intent: keep plaintext on screen long enough to copy into another
// app, but not so long it lingers if the user wanders off. 30s matches what
// 1Password's clipboard auto-clear does.

const REVEAL_TIMEOUT_MS = 30_000;

interface Props {
  integrationId: string;
}

export function SecretManagerSection({ integrationId }: Props) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const { t } = useT("integrations");
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
      window.setTimeout(() => {
        setRevealed(null);
        setRevealing(null);
      }, REVEAL_TIMEOUT_MS);
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
    <section data-testid="secret-manager-section">
      <div className="mb-2 flex items-center justify-between">
        <h3 className="text-sm font-semibold uppercase text-muted-foreground">
          {t(($) => $.secrets.heading)}
        </h3>
        <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
          {t(($) => $.secrets.add_button)}
        </Button>
      </div>
      {keysQuery.isLoading ? (
        <Skeleton className="h-12" />
      ) : keysQuery.data && keysQuery.data.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="secrets-empty">
          {t(($) => $.secrets.empty)}
        </p>
      ) : (
        <ul className="divide-y rounded-md border bg-card text-sm">
          {keysQuery.data?.map((k) => (
            <li
              key={k.key}
              className="flex items-center justify-between px-3 py-2"
              data-testid={`secret-row-${k.key}`}
            >
              <div className="flex flex-1 items-center gap-3 font-mono text-xs">
                <span>{k.key}</span>
                <span className="text-muted-foreground">v{k.version}</span>
                {revealing === k.key && revealed?.key === k.key && (
                  <span className="text-emerald-600" data-testid="revealed-value">
                    {revealed.value}
                  </span>
                )}
              </div>
              <div className="flex gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  data-testid={`reveal-${k.key}`}
                  onClick={() => {
                    if (revealing === k.key) {
                      setRevealing(null);
                      setRevealed(null);
                    } else {
                      setRevealing(k.key);
                      reveal.mutate(k.key);
                    }
                  }}
                  title={t(($) => $.secrets.reveal_tooltip)}
                >
                  {revealing === k.key ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  data-testid={`delete-${k.key}`}
                  onClick={() => {
                    if (window.confirm(t(($) => $.secrets.delete_confirm, { key: k.key }))) {
                      remove.mutate(k.key);
                    }
                  }}
                  title={t(($) => $.secrets.delete_tooltip)}
                >
                  <Trash2 className="size-4 text-destructive" />
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {adding && (
        <AddSecretForm integrationId={integrationId} onClose={() => setAdding(false)} />
      )}
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
  const { t } = useT("integrations");
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
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      data-testid="add-secret-dialog"
    >
      <div className="w-96 rounded-md bg-background p-6 shadow-lg">
        <h3 className="mb-3 text-lg font-semibold">{t(($) => $.secrets.add_title)}</h3>
        <label className="block text-xs text-muted-foreground">
          {t(($) => $.secrets.key_label)}
        </label>
        <Input
          className="mb-3 font-mono"
          value={key}
          placeholder={t(($) => $.secrets.key_placeholder)}
          onChange={(e) => setKey(e.target.value.toUpperCase())}
        />
        <label className="block text-xs text-muted-foreground">
          {t(($) => $.secrets.value_label)}
        </label>
        <textarea
          className="mb-3 h-24 w-full rounded border bg-background p-2 font-mono text-xs"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        {error && (
          <p className="mb-3 text-xs text-destructive" data-testid="add-secret-error">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t(($) => $.secrets.cancel)}
          </Button>
          <Button
            size="sm"
            data-testid="add-secret-submit"
            onClick={() => {
              if (!/^[A-Z][A-Z0-9_]{0,127}$/.test(key)) {
                setError(t(($) => $.secrets.key_pattern_error));
                return;
              }
              if (!value) {
                setError(t(($) => $.secrets.value_required));
                return;
              }
              mut.mutate();
            }}
            disabled={mut.isPending}
          >
            {mut.isPending ? t(($) => $.secrets.saving) : t(($) => $.secrets.save)}
          </Button>
        </div>
      </div>
    </div>
  );
}
