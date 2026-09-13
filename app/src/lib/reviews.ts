import type { Coach } from '../api/types';

/**
 * Answers a client can pick when reviewing a coach. Each maps to the private
 * 1-5 `rating` the backend ranks by; the app only ever shows whether the
 * answer counts as a recommendation (rating >= 4, mirroring the server).
 */
export const RECOMMEND_OPTIONS: ReadonlyArray<{ rating: number; label: string }> = [
  { rating: 5, label: 'Yes, highly' },
  { rating: 4, label: 'Yes' },
  { rating: 3, label: 'Not sure' },
  { rating: 2, label: 'Not really' },
  { rating: 1, label: 'No' },
];

/**
 * Badge text for a coach's recommendation standing, e.g. "Recommended by 4 of 5 clients"
 * or "Recommended by all 3 clients". Callers should only use it when `review_count > 0`.
 */
export const recommendLabel = (c: Pick<Coach, 'recommend_count' | 'review_count'>): string =>
  c.recommend_count === c.review_count
    ? `Recommended by all ${c.review_count} client${c.review_count === 1 ? '' : 's'}`
    : `Recommended by ${c.recommend_count} of ${c.review_count} clients`;
