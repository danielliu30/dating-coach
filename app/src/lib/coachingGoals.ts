import type { CoachingGoal } from '../api/types';

/** Title and one-line explanation for each coaching-brain pillar a user can choose to improve on. */
export const COACHING_GOAL_LABELS: Record<CoachingGoal, { title: string; caption: string }> = {
  agency: { title: 'Agency', caption: 'Owning your choices: who you pursue, how you show up, what you accept.' },
  feedback: { title: 'Reading feedback', caption: 'Noticing how your messages land and what follows from them.' },
  support: { title: 'Support', caption: 'Being honest and direct while staying on your own side.' },
};
