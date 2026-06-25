"use client";

import { CalendarDays } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import {
  dateOnlyToLocalDate,
  formatDateOnly,
  isPastDateOnly,
} from "@multica/core/issues/date";
import { CalendarDatePicker } from "../../../common/calendar-date-picker";
import { useDateLocale, useT } from "../../../i18n";

export function DueDatePicker({
  dueDate,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  align = "start",
  defaultOpen = false,
}: {
  dueDate: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  align?: "start" | "center" | "end";
  /** Open the popover on first mount. Used by progressive-disclosure
   *  sidebars so a newly-added field immediately enters edit state. */
  defaultOpen?: boolean;
}) {
  const { t } = useT("issues");
  const { locale } = useDateLocale();
  const date = dateOnlyToLocalDate(dueDate);
  const isOverdue = isPastDateOnly(dueDate);

  return (
    <CalendarDatePicker
      value={dueDate}
      onChange={(v) => onUpdate({ due_date: v })}
      triggerRender={triggerRender}
      align={align}
      defaultOpen={defaultOpen}
      clearLabel={t(($) => $.pickers.due_date.clear_action)}
      trigger={
        customTrigger ?? (
          <>
            <CalendarDays className="h-3.5 w-3.5 text-muted-foreground" />
            {date ? (
              <span className={isOverdue ? "text-destructive" : ""}>
                {formatDateOnly(dueDate, { month: "short", day: "numeric" }, locale)}
              </span>
            ) : (
              <span className="text-muted-foreground">{t(($) => $.pickers.due_date.trigger_label)}</span>
            )}
          </>
        )
      }
    />
  );
}
