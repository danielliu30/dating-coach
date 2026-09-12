import type { DatingPhase } from '../api/types';

/** Human-readable label for each dating-cycle phase, shared by every screen that renders phases. */
export const PHASE_LABELS: Record<DatingPhase, string> = {
  opening: 'Opening line',
  first_messages: 'First messages',
  building_rapport: 'Building rapport',
  flirting: 'Flirting',
  asking_out: 'Asking them out',
  first_date: 'First date',
  follow_up: 'Following up after a date',
  defining_relationship: 'Defining the relationship',
};

/**
 * Toggles `value` in `list` and returns a new array ordered like `vocabulary`,
 * so multi-select state stays canonical regardless of tap order. Values not in
 * `vocabulary` are dropped.
 */
export function toggle<T extends string>(list: readonly T[], value: T, vocabulary: readonly T[]): T[] {
  const next = new Set(list);
  if (next.has(value)) next.delete(value);
  else next.add(value);
  return vocabulary.filter((v) => next.has(v));
}
