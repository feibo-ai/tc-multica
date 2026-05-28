"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Trash2 } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

// Restart / Redeploy / Delete actions for one integration, surfaced as a
// header-level row on the detail page. Extracted from integration-detail-page
// so the buttons can be tested in isolation.

interface Props {
  integrationId: string;
}

export function IntegrationActions({ integrationId }: Props) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const { t } = useT("integrations");

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
      navigation.push(paths.integrations());
    },
  });

  return (
    <div className="flex gap-2">
      <Button
        variant="outline"
        size="sm"
        onClick={() => restart.mutate()}
        disabled={restart.isPending}
        data-testid="action-restart"
      >
        <RefreshCw className="mr-1 size-4" />
        {t(($) => $.actions.restart)}
      </Button>
      <Button
        variant="outline"
        size="sm"
        onClick={() => redeploy.mutate()}
        disabled={redeploy.isPending}
        data-testid="action-redeploy"
      >
        {t(($) => $.actions.redeploy)}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        data-testid="action-delete"
        onClick={() => {
          if (window.confirm(t(($) => $.actions.delete_confirm))) {
            del.mutate();
          }
        }}
      >
        <Trash2 className="size-4 text-destructive" />
      </Button>
    </div>
  );
}
