// Mirrors the JSON emitted by the Go backend.

export type Role = 'user' | 'coach' | 'admin';

export interface Profile {
  id: string;
  email: string;
  display_name: string;
  role: Role;
  email_verified: boolean;
}

/** Scope bound to a token when it was issued: sign-up yields 'verify', sign-in 'session'. */
export type TokenScope = 'verify' | 'session';

export interface AuthSession {
  token: string;
  expires_at: string;
  scope: TokenScope;
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

export type SessionStatus = 'scheduled' | 'completed' | 'cancelled' | 'no_show';

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

export interface SubmitMessage {
  sender: 'self' | 'match';
  body: string;
  sent_at?: string;
}

export type Outcome = 'ghosted' | 'kept_talking' | 'number_exchanged' | 'date_set';
