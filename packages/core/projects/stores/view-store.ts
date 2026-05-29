"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

export type ProjectViewMode = "compact" | "comfortable";

export interface ProjectViewState {
  viewMode: ProjectViewMode;
  setViewMode: (mode: ProjectViewMode) => void;
}

export const useProjectViewStore = create<ProjectViewState>()(
  persist(
    (set) => ({
      // The merged project tab opens on the enriched card grid by default
      // (unified-project-tab completion criterion #2). Users who prefer the
      // compact table still have their choice persisted per workspace.
      viewMode: "comfortable",
      setViewMode: (mode) => set({ viewMode: mode }),
    }),
    {
      name: "multica_projects_view",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({ viewMode: state.viewMode }),
      merge: (persisted, current) => {
        if (!persisted) return { ...current, viewMode: "comfortable" };
        return { ...current, ...(persisted as Partial<ProjectViewState>) };
      },
    }
  )
);

registerForWorkspaceRehydration(() => useProjectViewStore.persist.rehydrate());