"use client";

import { CalendarClock } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import { dateOnlyToLocalDate, formatDateOnly } from "@multica/core/issues/date";
import { CalendarDatePicker } from "../../../common/calendar-date-picker";
import { useDateLocale, useT } from "../../../i18n";

export function StartDatePicker({
  startDate,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open,
  onOpenChange,
  align = "start",
  defaultOpen = false,
}: {
  startDate: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /** Open the popover on first mount. Used by progressive-disclosure
   *  sidebars so a newly-added field immediately enters edit state. */
  defaultOpen?: boolean;
}) {
  const { t } = useT("issues");
  const { locale } = useDateLocale();
  const date = dateOnlyToLocalDate(startDate);

  return (
    <CalendarDatePicker
      value={startDate}
      onChange={(v) => onUpdate({ start_date: v })}
      triggerRender={triggerRender}
      open={open}
      onOpenChange={onOpenChange}
      align={align}
      defaultOpen={defaultOpen}
      clearLabel={t(($) => $.pickers.start_date.clear_action)}
      trigger={
        customTrigger ?? (
          <>
            <CalendarClock className="h-3.5 w-3.5 text-muted-foreground" />
            {date ? (
              <span>
                {formatDateOnly(startDate, { month: "short", day: "numeric" }, locale)}
              </span>
            ) : (
              <span className="text-muted-foreground">{t(($) => $.pickers.start_date.trigger_label)}</span>
            )}
          </>
        )
      }
    />
  );
}
