import { describe, it, expect } from "vitest";
import {
  addProjectToList,
  updateProjectInList,
  removeProjectFromList,
} from "./mutations";
import type { Project } from "../types";

function makeProject(id: string, overrides: Partial<Project> = {}): Project {
  return {
    id,
    workspace_id: "ws-1",
    title: `Project ${id}`,
    description: null,
    icon: null,
    status: "planned",
    priority: "none",
    lead_type: null,
    lead_id: null,
    dri_user_id: null,
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    ...overrides,
  };
}

// The default project list cache stores an unwrapped `Project[]` (see
// `projectListOptions` in ./queries, which unwraps the `{ projects, total }`
// envelope so every consumer reads a bare array). These helpers MUST operate on
// that bare array. Treating it as an envelope is what caused two production
// bugs: the create flow threw "Cannot read properties of undefined (reading
// 'some')" in onSuccess, and delete silently failed because the optimistic
// update threw inside onMutate, so React Query never called the delete
// mutationFn.
describe("project list cache helpers", () => {
  describe("addProjectToList", () => {
    it("appends a newly created project to the array", () => {
      const list = [makeProject("a"), makeProject("b")];
      const next = addProjectToList(list, makeProject("c"));
      expect(next?.map((p) => p.id)).toEqual(["a", "b", "c"]);
    });

    it("does not duplicate a project already in the list", () => {
      const list = [makeProject("a")];
      const next = addProjectToList(list, makeProject("a"));
      expect(next).toBe(list);
    });

    it("is a no-op when the cache is empty (undefined)", () => {
      expect(addProjectToList(undefined, makeProject("a"))).toBeUndefined();
    });

    it("operates on a bare array without throwing (regression for reading 'some')", () => {
      expect(() =>
        addProjectToList([makeProject("a")], makeProject("b")),
      ).not.toThrow();
    });
  });

  describe("updateProjectInList", () => {
    it("patches only the matching project", () => {
      const list = [
        makeProject("a", { title: "A" }),
        makeProject("b", { title: "B" }),
      ];
      const next = updateProjectInList(list, "a", { title: "A2" });
      expect(next?.find((p) => p.id === "a")?.title).toBe("A2");
      expect(next?.find((p) => p.id === "b")?.title).toBe("B");
    });

    it("is a no-op when the cache is empty (undefined)", () => {
      expect(updateProjectInList(undefined, "a", { title: "x" })).toBeUndefined();
    });
  });

  describe("removeProjectFromList", () => {
    it("removes the project by id", () => {
      const list = [makeProject("a"), makeProject("b")];
      const next = removeProjectFromList(list, "a");
      expect(next?.map((p) => p.id)).toEqual(["b"]);
    });

    it("is a no-op when the cache is empty (undefined)", () => {
      expect(removeProjectFromList(undefined, "a")).toBeUndefined();
    });

    it("operates on a bare array without throwing (regression for silent delete failure)", () => {
      expect(() => removeProjectFromList([makeProject("a")], "a")).not.toThrow();
    });
  });
});
