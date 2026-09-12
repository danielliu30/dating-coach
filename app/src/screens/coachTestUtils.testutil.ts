import type { Coach, CoachingSession, ChatThread, Slot } from '../api/types';

/** Coach fixture with every field populated; override to shape a scenario. */
export const coachFixture = (overrides: Partial<Coach> = {}): Coach => ({
  id: 'c1',
  display_name: 'Casey Coach',
  headline: 'Openers that land',
  bio: 'Ten years of listening.',
  specialties: ['openers', 'texting'],
  hourly_rate_cents: 12000,
  timezone: 'America/New_York',
  years_experience: 10,
  accepting_clients: true,
  ...overrides,
});

/** Scheduled-session fixture; override `status` to exercise non-actionable rows. */
export const sessionFixture = (overrides: Partial<CoachingSession> = {}): CoachingSession => ({
  id: 's1',
  user_id: 'u1',
  coach_id: 'c1',
  counterpart_name: 'Casey Coach',
  scheduled_time: '2030-01-07T18:00:00Z',
  duration_minutes: 45,
  status: 'scheduled',
  topic: 'Opening lines',
  payment_status: 'not_required',
  amount_cents: 9000,
  currency: 'usd',
  ...overrides,
});

/** Active chat thread fixture. */
export const threadFixture = (overrides: Partial<ChatThread> = {}): ChatThread => ({
  id: 't1',
  user_id: 'u1',
  coach_id: 'c1',
  counterpart_name: 'Riley Client',
  status: 'active',
  last_message_at: '2030-01-06T12:00:00Z',
  counterpart_online: true,
  ...overrides,
});

/** Open slot fixture. */
export const slotFixture = (start: string, duration_minutes = 45): Slot => ({ start, duration_minutes });
