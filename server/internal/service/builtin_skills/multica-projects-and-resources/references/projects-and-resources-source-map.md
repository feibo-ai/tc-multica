# Projects and resources source map

- `server/cmd/multica/cmd_project.go` registers project `list`, `get`, `create`, `update`, `delete`, and `status`.
- The same file registers `project resource list/add/update/remove`.
- `project create --repo` attaches `github_repo` resources during project creation.
- `project create` and `project update` expose `--dri`, `--priority`, `--start-date`, and `--due-date`; they pass through as `dri_user_id` / `priority` / `start_date` / `due_date` in the request body, validated server-side in `server/internal/handler/project.go` (`CreateProject` / `UpdateProject`) — priority against the project-priority enum, dates via `util.ParseCalendarDate` (`YYYY-MM-DD`). An empty `start_date` / `due_date` on update clears the column.
- `project resource add` supports shortcuts for `github_repo` (`--url`, `--default-branch-hint`) and `local_directory` (`--local-path`, `--daemon-id`, `--ref-label`), or generic `--ref '<json>'`.
- `project resource update` merges shortcut edits with existing `resource_ref` so a partial edit does not clobber required fields.
- `server/cmd/server/router.go` exposes `/api/projects` plus `/api/projects/{projectId}/resources` routes.
- `server/pkg/db/queries/project_resource.sql` is the CRUD query surface for `project_resource` rows.
- Project resources are written into `.multica/project/resources.json` for agent workdirs.
