"use client";

import { Loader2 } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

export interface ArchiveSquadConfirmDialogProps {
  open: boolean;
  squadName: string;
  leaderName: string;
  /**
   * Total issues currently assigned to the squad. Pass `null` (older server
   * or transient count error) to render the "no count" copy variant — the
   * action is still safe, the user just sees a less specific number-free
   * description.
   */
  issueCount: number | null;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => Promise<void> | void;
}

export function ArchiveSquadConfirmDialog({
  open,
  squadName,
  leaderName,
  issueCount,
  pending,
  onCancel,
  onConfirm,
}: ArchiveSquadConfirmDialogProps) {
  const { t } = useT("squads");

  const description =
    issueCount == null
      ? t(($) => $.archive_dialog.description_no_count, { leader: leaderName })
      : t(($) => $.archive_dialog.description_with_count, {
          leader: leaderName,
          count: issueCount,
        });

  return (
    <AlertDialog
      open={open}
      onOpenChange={(v) => {
        if (!v && !pending) onCancel();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t(($) => $.archive_dialog.title, { name: squadName })}
          </AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>
            {t(($) => $.archive_dialog.cancel)}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={() => void onConfirm()}
          >
            {pending ? (
              <>
                <Loader2 className="size-3.5 mr-1 animate-spin" />
                {t(($) => $.archive_dialog.archiving)}
              </>
            ) : (
              t(($) => $.archive_dialog.confirm)
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
