"use client";

import { use } from "react";
import { IntegrationDetailPage } from "@multica/views/integrations";

export default function IntegrationDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <IntegrationDetailPage id={id} />;
}
