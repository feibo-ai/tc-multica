"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import type { Integration, IntegrationKind } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../../navigation";
import { PageHeader } from "../../layout/page-header";

// Compact list view for the control plane (Plan 4 / PR D, Task D-12).
// Workspace-scoped — every row links into the detail page.

const KIND_FILTERS: ReadonlyArray<"all" | IntegrationKind> = [
  "all",
  "mcp-server",
  "feishu",
  "autopilot-bot",
];

export function IntegrationsPage() {
  const workspaceId = useWorkspaceId();
  const navigation = useNavigation();
  const [filter, setFilter] = useState<"all" | IntegrationKind>("all");
  const [creating, setCreating] = useState(false);

  const { data: integrations, isLoading, isError } = useQuery({
    queryKey: [workspaceId, "integrations"],
    queryFn: () => api.listIntegrations(),
    enabled: !!workspaceId,
  });

  const filtered = useMemo(() => {
    if (!integrations) return [];
    return filter === "all" ? integrations : integrations.filter((i) => i.kind === filter);
  }, [integrations, filter]);

  return (
    <div className="flex h-full flex-col">
      <PageHeader className="justify-between px-5">
        <div>
          <h2 className="text-base font-semibold">Integrations</h2>
          <p className="text-xs text-muted-foreground">
            Manage MCP servers, feishu credentials, and autopilot-bot deployments.
          </p>
        </div>
        <Button onClick={() => setCreating(true)} size="sm">
          <Plus className="mr-1 size-4" />
          New integration
        </Button>
      </PageHeader>

      <div className="flex items-center gap-2 px-6 py-3">
        {KIND_FILTERS.map((k) => (
          <Button
            key={k}
            variant={filter === k ? "default" : "outline"}
            size="sm"
            onClick={() => setFilter(k)}
          >
            {k === "all" ? "All" : k}
          </Button>
        ))}
      </div>

      {isLoading ? (
        <div className="flex flex-col gap-2 px-6">
          <Skeleton className="h-12" />
          <Skeleton className="h-12" />
          <Skeleton className="h-12" />
        </div>
      ) : isError ? (
        <p className="px-6 text-sm text-destructive">Failed to load integrations.</p>
      ) : filtered.length === 0 ? (
        <p className="px-6 py-12 text-center text-sm text-muted-foreground">
          No integrations yet. Click <em>New integration</em> to add one.
        </p>
      ) : (
        <div className="flex-1 overflow-auto px-6">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-xs uppercase text-muted-foreground">
                <th className="py-2 pr-4">Name</th>
                <th className="py-2 pr-4">Kind</th>
                <th className="py-2 pr-4">Status</th>
                <th className="py-2 pr-4">Version</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((i) => (
                <tr
                  key={i.id}
                  className="cursor-pointer border-b hover:bg-muted/50"
                  onClick={() => navigation.push(`/integrations/${i.id}`)}
                >
                  <td className="py-2 pr-4 font-medium">{i.name}</td>
                  <td className="py-2 pr-4 text-muted-foreground">{i.kind}</td>
                  <td className="py-2 pr-4">
                    <StatusDot status={i.status} />
                    <span className="ml-2 capitalize">{i.status}</span>
                  </td>
                  <td className="py-2 pr-4 text-muted-foreground">v{i.version}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && <CreateInlineForm onClose={() => setCreating(false)} />}
    </div>
  );
}

function StatusDot({ status }: { status: Integration["status"] }) {
  const color =
    status === "running"
      ? "bg-emerald-500"
      : status === "degraded"
        ? "bg-amber-500"
        : status === "stopped"
          ? "bg-rose-500"
          : "bg-zinc-400";
  return <span className={`inline-block size-2 rounded-full align-middle ${color}`} />;
}

// Inline create form — kept tiny on purpose. A richer modal can land later;
// this is enough to get owners through the bootstrap flow without expanding
// the v1 surface area.
function CreateInlineForm({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const workspaceId = useWorkspaceId();
  const [kind, setKind] = useState<IntegrationKind>("mcp-server");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const mut = useMutation({
    mutationFn: () => api.createIntegration({ kind, name }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [workspaceId, "integrations"] });
      onClose();
    },
    onError: (e: Error) => setError(e.message),
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-96 rounded-md bg-background p-6 shadow-lg">
        <h3 className="mb-3 text-lg font-semibold">New integration</h3>
        <label className="block text-xs text-muted-foreground">Kind</label>
        <select
          className="mb-3 w-full rounded border bg-background p-2 text-sm"
          value={kind}
          onChange={(e) => setKind(e.target.value as IntegrationKind)}
        >
          <option value="mcp-server">mcp-server</option>
          <option value="feishu">feishu</option>
          <option value="autopilot-bot">autopilot-bot</option>
        </select>
        <label className="block text-xs text-muted-foreground">Name</label>
        <Input
          className="mb-3"
          value={name}
          placeholder="e.g. tcmcp"
          onChange={(e) => setName(e.target.value)}
        />
        {error && <p className="mb-3 text-xs text-destructive">{error}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => {
              if (!name.trim()) {
                setError("name is required");
                return;
              }
              mut.mutate();
            }}
            disabled={mut.isPending}
          >
            {mut.isPending ? "Creating…" : "Create"}
          </Button>
        </div>
      </div>
    </div>
  );
}
