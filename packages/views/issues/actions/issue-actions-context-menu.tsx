"use client";

import { useRef, useState, type ReactElement } from "react";
import type { Issue } from "@multica/core/types";
import {
  ContextMenu,
  ContextMenuTrigger,
  ContextMenuContent,
} from "@multica/ui/components/ui/context-menu";
import { useIssueActions } from "./use-issue-actions";
import {
  IssueActionsMenuItems,
  contextPrimitives,
} from "./issue-actions-menu-items";
import { AssigneePicker } from "../components/pickers";
import { CalendarDatePicker } from "../../common/calendar-date-picker";
import { useT } from "../../i18n";

interface IssueActionsContextMenuProps {
  issue: Issue;
  /** A single React element cloned by Base UI as the trigger (via `render` prop). */
  children: ReactElement;
}

export function IssueActionsContextMenu({
  issue,
  children,
}: IssueActionsContextMenuProps) {
  const { t } = useT("issues");
  const actions = useIssueActions(issue);
  const [assigneeOpen, setAssigneeOpen] = useState(false);
  const [startDateOpen, setStartDateOpen] = useState(false);
  const [dueDateOpen, setDueDateOpen] = useState(false);
  // Right-click coordinates captured during contextmenu so the AssigneePicker
  // opens where the context menu just was, instead of jumping to the row's
  // top-left corner. Reset between opens; only consulted while the picker is
  // mounted-open.
  const clickPosRef = useRef<{ x: number; y: number }>({ x: 0, y: 0 });

  const handleContextMenu = (e: React.MouseEvent) => {
    clickPosRef.current = { x: e.clientX, y: e.clientY };
  };

  return (
    <>
      <ContextMenu>
        <ContextMenuTrigger
          render={children}
          onContextMenu={handleContextMenu}
        />
        <ContextMenuContent>
          <IssueActionsMenuItems
            issue={issue}
            actions={actions}
            primitives={contextPrimitives}
            onOpenAssignee={() => setAssigneeOpen(true)}
            onOpenStartDate={() => setStartDateOpen(true)}
            onOpenDueDate={() => setDueDateOpen(true)}
          />
        </ContextMenuContent>
      </ContextMenu>
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
              className="pointer-events-none fixed"
              style={{
                left: clickPosRef.current.x,
                top: clickPosRef.current.y,
                width: 0,
                height: 0,
              }}
            />
          }
          trigger={<span />}
          align="start"
        />
      )}
      {/* Calendar pickers handed off from the date submenus' "Pick a date…"
          item. Anchored at the right-click position like the assignee picker,
          mounted only while open. */}
      {startDateOpen && (
        <CalendarDatePicker
          value={issue.start_date}
          onChange={(v) => actions.updateField({ start_date: v })}
          open={startDateOpen}
          onOpenChange={setStartDateOpen}
          clearLabel={t(($) => $.pickers.start_date.clear_action)}
          triggerRender={
            <span
              aria-hidden
              className="pointer-events-none fixed"
              style={{
                left: clickPosRef.current.x,
                top: clickPosRef.current.y,
                width: 0,
                height: 0,
              }}
            />
          }
          trigger={<span />}
          align="start"
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
            <span
              aria-hidden
              className="pointer-events-none fixed"
              style={{
                left: clickPosRef.current.x,
                top: clickPosRef.current.y,
                width: 0,
                height: 0,
              }}
            />
          }
          trigger={<span />}
          align="start"
        />
      )}
    </>
  );
}
