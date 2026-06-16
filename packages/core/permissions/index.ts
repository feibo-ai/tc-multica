/**
 * Public API for the permissions module.
 *
 * Exports only what the views currently consume. The full pure-rule set lives
 * in `./rules` and is available to tests and future surfaces directly. Adding
 * a new rule to the public API should follow the same minimum-surface pattern
 * — only export when there's a caller.
 */
export type {
  Decision,
  DecisionReason,
  PermissionContext,
} from "./types";

export { canAssignAgentToIssue, canEditAgent } from "./rules";

export {
  useAgentPermissions,
  useSkillPermissions,
} from "./use-resource-permissions";

// Surfaced for the runtimes fleet-update DRI gate (TEA-113): the view hides the
// one-click update action for non-owner/admin members. The hide is UX only —
// the server's owner/admin role check is the authority (INV-3).
export { useCurrentMember } from "./use-current-member";
