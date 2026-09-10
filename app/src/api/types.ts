// Mirrors the JSON emitted by the Go backend.

export type Role = 'user' | 'coach' | 'admin';

export interface Profile {
  id: string;
  email: string;
  display_name: string;
  role: Role;
  email_verified: boolean;
  dating_styles: DatingStyle[];
  phases_strong: DatingPhase[];
  phases_working_on: DatingPhase[];
  /** Free text: what the user is looking for. The analyzer tailors its hints to it. */
  dating_preferences: string;
}

// Vocabularies mirror auth.DatingStyles / auth.DatingPhases in the backend.
export const DATING_STYLES = [
  'in_person',
  'tinder',
  'hinge',
  'bumble',
  'coffee_meets_bagel',
  'match',
  'okcupid',
  'feeld',
  'speed_dating',
  'friends_intro',
] as const;
export type DatingStyle = (typeof DATING_STYLES)[number];

export const DATING_PHASES = [
  'opening',
  'first_messages',
  'building_rapport',
  'flirting',
  'asking_out',
  'first_date',
  'follow_up',
  'defining_relationship',
] as const;
export type DatingPhase = (typeof DATING_PHASES)[number];

export interface DatingProfileInput {
  dating_styles: DatingStyle[];
  phases_strong: DatingPhase[];
  phases_working_on: DatingPhase[];
  /** Trimmed server-side and capped at MAX_DATING_PREFERENCES_LEN characters. */
  dating_preferences: string;
}

// Mirrors auth.MaxDatingPreferencesLen in the backend.
export const MAX_DATING_PREFERENCES_LEN = 2000;

export interface AuthSession {
  token: string;
  /** Absent on responses that do not open a full session, e.g. sign-up. */
  refresh_token?: string;
  expires_at: string;
  user: Profile;
}

export interface Coach {
  id: string;
  display_name: string;
  headline: string;
  bio: string;
  specialties: string[];
  hourly_rate_cents: number;
  timezone: string;
  years_experience: number;
  accepting_clients: boolean;
}

export interface AvailabilityWindow {
  weekday: number;
  start_minute: number;
  end_minute: number;
}

export interface Slot {
  start: string;
  duration_minutes: number;
}

export type SessionStatus =
  | 'pending_payment'
  | 'pending'
  | 'scheduled'
  | 'declined'
  | 'expired'
  | 'completed'
  | 'cancelled'
  | 'no_show';

export type PaymentStatus =
  | 'not_required'
  | 'pending'
  | 'expiring'
  | 'authorized'
  | 'paid'
  | 'releasing'
  | 'released'
  | 'refund_due'
  | 'refunded'
  | 'failed';

/** Server-side feature switches the app must follow rather than decide itself. */
export interface CoachingConfig {
  payments_enabled: boolean;
}

export interface CoachingSession {
  id: string;
  user_id: string;
  coach_id: string;
  counterpart_name?: string;
  scheduled_time: string;
  duration_minutes: number;
  status: SessionStatus;
  topic: string;
  coach_notes?: string;
  payment_status: PaymentStatus;
  amount_cents: number;
  currency: string;
  hold_expires_at?: string;
  /** Only present on a booking response that must be paid for. */
  checkout_url?: string;
}

export interface ChatThread {
  id: string;
  user_id: string;
  coach_id: string;
  session_id?: string;
  counterpart_name?: string;
  status: 'active' | 'closed';
  last_message_at: string;
  counterpart_online: boolean;
}

export interface ChatMessage {
  id: string;
  thread_id: string;
  sender_id: string;
  body: string;
  created_at: string;
}

export type ChatEventType = 'message' | 'typing' | 'presence' | 'history' | 'error';

export interface ChatEvent {
  type: ChatEventType;
  thread_id?: string;
  message_id?: string;
  sender_id?: string;
  body?: string;
  typing?: boolean;
  online?: boolean;
  messages?: ChatMessage[];
  created_at?: string;
}

export interface Conversation {
  id: string;
  title: string;
  platform: string;
  match_name: string;
  created_at: string;
}

export type SegmentLabel = 'engaging' | 'neutral' | 'flat';

export interface AnalysisSegment {
  start_position: number;
  end_position: number;
  engagement_score: number;
  label: SegmentLabel;
  comment: string;
}

export interface AnalysisOverall {
  engagement_score: number;
  summary: string;
  strengths: string[];
  improvements: string[];
}

export type AnalysisStatus = 'pending' | 'running' | 'succeeded' | 'failed';

export interface AnalysisResult {
  id: string;
  conversation_id: string;
  status: AnalysisStatus;
  model_version: string;
  segments: AnalysisSegment[] | null;
  overall: AnalysisOverall | null;
  error?: string;
  created_at: string;
  completed_at?: string;
}

/** Media types the image track accepts. */
export type ImageMediaType = 'image/jpeg' | 'image/png' | 'image/webp' | 'image/gif';

/**
 * One profile photo for POST /analysis/images. Exactly one of url/base64 must
 * be set; base64 is the raw payload with no data: prefix, at most 5 MiB decoded.
 */
export interface ImageRef {
  url?: string;
  base64?: string;
  /** Defaults to image/jpeg server-side. */
  media_type?: ImageMediaType;
}

/** Body of POST /analysis/images: 1-10 photos. Preferences come from the profile, not the request. */
export interface ImageAnalyzeInput {
  images: ImageRef[];
}

export const MAX_ANALYSIS_IMAGES = 10;

/** Verdict for one photo, in request order (index). Scores are 0-1. */
export interface ImageAssessment {
  index: number;
  clarity_score: number;
  is_clear: boolean;
  subject_focus_score: number;
  is_customer_focal_point: boolean;
  feedback: string;
}

export interface ImageOverall {
  summary: string;
  strengths: string[];
  improvements: string[];
}

/** Synchronous response of POST /analysis/images; nothing is stored server-side. */
export interface ImageAnalysis {
  model_version: string;
  images: ImageAssessment[];
  overall: ImageOverall;
}

export interface SubmitMessage {
  sender: 'self' | 'match';
  body: string;
  sent_at?: string;
}

export type Outcome = 'ghosted' | 'kept_talking' | 'number_exchanged' | 'date_set';
