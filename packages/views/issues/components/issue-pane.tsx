"use client";

import { createContext, useContext } from "react";

/**
 * Master-detail plumbing for issue cards. When a board / list is rendered
 * inside a container that shows the issue in a side pane (the project page),
 * that container provides this context so a card opens the pane instead of
 * navigating the whole window away. Outside such a container — the My Issues
 * page, the standalone issues page, the actor issues panel — the context is
 * `null` and cards keep their full-page `AppLink` navigation untouched.
 *
 * This mirrors `NavigationProvider`: cross-cutting click-routing plumbing
 * threaded down to the leaf cards, not view state. The active issue id is
 * owned by the project page (URL-synced via `?issue=`); this context only
 * carries the current id + the open handler down to the cards.
 */
export interface IssuePaneController {
  /** The issue currently shown in the detail pane, or `null` when none is. */
  activeIssueId: string | null;
  /** Open `issueId` in the detail pane (and reveal the pane if collapsed). */
  openIssue: (issueId: string) => void;
}

const IssuePaneContext = createContext<IssuePaneController | null>(null);

export const IssuePaneProvider = IssuePaneContext.Provider;

/**
 * Returns the active master-detail controller, or `null` when the board / list
 * is not inside a detail-pane container (cards then use `AppLink`).
 */
export function useIssuePane(): IssuePaneController | null {
  return useContext(IssuePaneContext);
}
