"use client";

import { useState, type ReactElement } from "react";
import type { Issue } from "@multica/core/types";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
} from "@multica/ui/components/ui/dropdown-menu";
import { useIssueActions } from "./use-issue-actions";
import {
  IssueActionsMenuItems,
  dropdownPrimitives,
} from "./issue-actions-menu-items";
import { AssigneePicker } from "../components/pickers";
import { CalendarDatePicker } from "../../common/calendar-date-picker";
import { useT } from "../../i18n";

interface IssueActionsDropdownProps {
  issue: Issue;
  /** A single React element cloned by Base UI as the trigger (via `render` prop). */
  trigger: ReactElement;
  align?: "start" | "end" | "center";
  /** If set, navigate here after the issue is deleted. */
  onDeletedNavigateTo?: string;
}

export function IssueActionsDropdown({
  issue,
  trigger,
  align = "end",
  onDeletedNavigateTo,
}: IssueActionsDropdownProps) {
  const { t } = useT("issues");
  const actions = useIssueActions(issue);
  const [assigneeOpen, setAssigneeOpen] = useState(false);
  const [startDateOpen, setStartDateOpen] = useState(false);
  const [dueDateOpen, setDueDateOpen] = useState(false);

  // The outer `relative inline-flex` is the picker's anchor box: the
  // absolute, pointer-events-none span inside `triggerRender` fills it, so
  // the popover positions itself relative to the dropdown's 3-dot button
  // without us having to thread a ref through Base UI's anchor API.
  return (
    <span className="relative inline-flex">
      <DropdownMenu>
        <DropdownMenuTrigger render={trigger} />
        <DropdownMenuContent align={align} className="w-auto">
          <IssueActionsMenuItems
            issue={issue}
            actions={actions}
            primitives={dropdownPrimitives}
            onOpenAssignee={() => setAssigneeOpen(true)}
            onOpenStartDate={() => setStartDateOpen(true)}
            onOpenDueDate={() => setDueDateOpen(true)}
            onDeletedNavigateTo={onDeletedNavigateTo}
          />
        </DropdownMenuContent>
      </DropdownMenu>
      {/* Mount the picker only once the user actually opens it. Otherwise
          every row in a list/board would subscribe to members/agents/squads
          /frequency queries on mount, multiplying memory + render cost. */}
      {assigneeOpen && (
        <AssigneePicker
          assigneeType={issue.assignee_type}
          assigneeId={issue.assignee_id}
          onUpdate={actions.updateField}
          open={assigneeOpen}
          onOpenChange={setAssigneeOpen}
          triggerRender={
            <span
              aria-hidden
              className="pointer-events-none absolute inset-0"
            />
          }
          trigger={<span />}
          align={align}
        />
      )}
      {/* Calendar pickers handed off from the date submenus' "Pick a date…"
          item, anchored to the 3-dot button, mounted only while open. */}
      {startDateOpen && (
        <CalendarDatePicker
          value={issue.start_date}
          onChange={(v) => actions.updateField({ start_date: v })}
          open={startDateOpen}
          onOpenChange={setStartDateOpen}
          clearLabel={t(($) => $.pickers.start_date.clear_action)}
          triggerRender={
            <span aria-hidden className="pointer-events-none absolute inset-0" />
          }
          trigger={<span />}
          align={align}
        />
      )}
      {dueDateOpen && (
        <CalendarDatePicker
          value={issue.due_date}
          onChange={(v) => actions.updateField({ due_date: v })}
          open={dueDateOpen}
          onOpenChange={setDueDateOpen}
          clearLabel={t(($) => $.pickers.due_date.clear_action)}
          triggerRender={
            <span aria-hidden className="pointer-events-none absolute inset-0" />
          }
          trigger={<span />}
          align={align}
        />
      )}
    </span>
  );
}
