-- Team overview aggregates. Each query is ONE GROUP BY over the whole workspace
-- (never a per-member loop): the handler buckets the returned rows by member so
-- the /api/team/overview endpoint runs a fixed number of queries regardless of
-- team size (TEA-104 criterion N2). All scoped by workspace_id (multi-tenancy).
--
-- ID reference note (the handler maps both keys per member):
--   issue.assignee_id (assignee_type='member')   → member.id
--   project.lead_id   (lead_type='member')        → member.id
--   project.dri_user_id                           → user.id
--   agent.owner_id                                → user.id
--   autopilot.created_by_id (created_by_type='member') → member.id
--   squad_member.member_id (member_type='member') → member.id
--   token usage owner_id (ListDashboardUsageByPerson) → user.id

-- name: CountIssuesByMemberStatus :many
-- Per-member issue counts grouped by status (issues assigned directly to the
-- member). Keyed by member.id. See CountAgentIssuesByOwnerStatus for the work
-- this member's agents are doing — the card sums both.
SELECT assignee_id, status, COUNT(*)::bigint AS count
FROM issue
WHERE workspace_id = $1
  AND assignee_type = 'member'
  AND assignee_id IS NOT NULL
GROUP BY assignee_id, status;

-- name: CountAgentIssuesByOwnerStatus :many
-- Issues assigned to AGENTS, grouped by the agent's owner (user.id) + status.
-- An AI-native team assigns issues to agents rather than to members, so the
-- member-assigned view above is empty — this captures the delegated work each
-- person's agents are doing, which the card folds into the task distribution.
SELECT a.owner_id, i.status, COUNT(*)::bigint AS count
FROM issue i
JOIN agent a ON a.id = i.assignee_id
WHERE i.workspace_id = $1
  AND a.workspace_id = $1
  AND i.assignee_type = 'agent'
  AND a.owner_id IS NOT NULL
  AND a.archived_at IS NULL
GROUP BY a.owner_id, i.status;

-- name: CountAgentsByOwner :many
-- Active (non-archived) agents per owner.
SELECT owner_id, COUNT(*)::bigint AS count
FROM agent
WHERE workspace_id = $1
  AND owner_id IS NOT NULL
  AND archived_at IS NULL
GROUP BY owner_id;

-- name: CountRunningAgentsByOwner :many
-- Agents per owner that currently have an in-flight task. Powers the
-- "智能体 N 运行中" signal — this is AGENT presence, not human presence
-- (there is deliberately no member-presence backbone; TEA-104 B-D1).
-- The active set matches the rest of the codebase (agent.sql / runtime.sql):
-- queued (claimed, awaiting pickup) + dispatched + running. The real
-- agent_task_queue.status domain is queued/dispatched/running/completed/
-- failed/cancelled — 'claimed'/'in_progress' are not queue states.
SELECT a.owner_id, COUNT(DISTINCT a.id)::bigint AS count
FROM agent a
JOIN agent_task_queue q ON q.agent_id = a.id
WHERE a.workspace_id = $1
  AND a.owner_id IS NOT NULL
  AND a.archived_at IS NULL
  AND q.status IN ('queued', 'dispatched', 'running')
GROUP BY a.owner_id;

-- name: CountAutopilotsByCreatorMember :many
SELECT created_by_id, COUNT(*)::bigint AS count
FROM autopilot
WHERE workspace_id = $1
  AND created_by_type = 'member'
  AND created_by_id IS NOT NULL
GROUP BY created_by_id;

-- name: CountProjectsByLeadMember :many
SELECT lead_id, COUNT(*)::bigint AS count
FROM project
WHERE workspace_id = $1
  AND lead_type = 'member'
  AND lead_id IS NOT NULL
GROUP BY lead_id;

-- name: CountProjectsByDriUser :many
-- DRI is a user reference; the handler maps user_id → member for display.
SELECT dri_user_id, COUNT(*)::bigint AS count
FROM project
WHERE workspace_id = $1
  AND dri_user_id IS NOT NULL
GROUP BY dri_user_id;

-- name: ListSquadMembershipByMember :many
-- (member_id, squad_id, squad_name) for member-type squad memberships, for the
-- "小队" chip. Scoped to the workspace via the squad join.
SELECT sm.member_id, s.id AS squad_id, s.name AS squad_name
FROM squad_member sm
JOIN squad s ON s.id = sm.squad_id
WHERE s.workspace_id = $1
  AND sm.member_type = 'member'
  AND s.archived_at IS NULL;
