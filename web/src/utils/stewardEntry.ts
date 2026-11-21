import type { RemoteProjectSummary, RemoteThreadSummary } from '../types/contract';
import { pickableProjects } from './builtinProjects';

export function resolveStewardOpenProjectID(
  selectedProjectID: string | null,
  projects: readonly Pick<RemoteProjectSummary, 'id' | 'kind'>[],
  threads: readonly Pick<RemoteThreadSummary, 'isSteward' | 'projectID'>[]
): string | null {
  if (selectedProjectID) return selectedProjectID;

  const stewardThread = threads.find((thread) => Boolean(thread.isSteward && thread.projectID));
  if (stewardThread?.projectID) return stewardThread.projectID;

  const pickableID = pickableProjects(projects).find((project) => Boolean(project.id))?.id;
  if (pickableID) return pickableID;

  return projects.find((project) => Boolean(project.id))?.id ?? null;
}

export function shouldShowStewardRow(opts: {
  openProjectID: string | null;
  supportsOpen: boolean;
  isConnected: boolean;
  filter: string;
  messageNeedle: string;
}): boolean {
  return (
    Boolean(opts.openProjectID) &&
    opts.supportsOpen &&
    opts.isConnected &&
    opts.filter !== 'archived' &&
    !opts.messageNeedle
  );
}
