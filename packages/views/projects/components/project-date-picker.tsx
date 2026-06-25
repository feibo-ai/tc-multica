"use client";

import { CalendarClock, CalendarDays } from "lucide-react";
import {
  dateOnlyToLocalDate,
  formatDateOnly,
  isPastDateOnly,
} from "@multica/core/issues/date";
import { CalendarDatePicker } from "../../common/calendar-date-picker";
import { useDateLocale, useT } from "../../i18n";

// Project counterpart to the issue Start/Due date pickers. Wraps the generic
// CalendarDatePicker with a project-flavored default trigger (icon + localized
// date / placeholder). Unlike the issue pickers, `onChange` is a plain
// (v) => void callback — the caller threads it into the appropriate project
// mutation (e.g. handleUpdateField({ start_date: v })).
export function ProjectDatePicker({
  kind,
  value,
  onChange,
  trigger: customTrigger,
  triggerRender,
  align = "start",
  defaultOpen = false,
  open,
  onOpenChange,
}: {
  kind: "start" | "due";
  value: string | null;
  onChange: (v: string | null) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  align?: "start" | "center" | "end";
  /** Open the popover on first mount. */
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
}) {
  const { t } = useT("projects");
  const { locale } = useDateLocale();
  const date = dateOnlyToLocalDate(value);
  const isDue = kind === "due";
  const isOverdue = isDue && isPastDateOnly(value);
  const Icon = isDue ? CalendarDays : CalendarClock;
  const triggerKey = isDue ? "due_date" : "start_date";

  return (
    <CalendarDatePicker
      value={value}
      onChange={onChange}
      triggerRender={triggerRender}
      open={open}
      onOpenChange={onOpenChange}
      align={align}
      defaultOpen={defaultOpen}
      clearLabel={t(($) => $.pickers[triggerKey].clear_action)}
      trigger={
        customTrigger ?? (
          <>
            <Icon className="h-3.5 w-3.5 text-muted-foreground" />
            {date ? (
              <span className={isOverdue ? "text-destructive" : ""}>
                {formatDateOnly(value, { month: "short", day: "numeric" }, locale)}
              </span>
            ) : (
              <span className="text-muted-foreground">
                {t(($) => $.pickers[triggerKey].trigger_label)}
              </span>
            )}
          </>
        )
      }
    />
  );
}
