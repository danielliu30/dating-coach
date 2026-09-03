import { API_BASE_URL, API_PREFIX } from '../config';
import type {
  AnalysisResult,
  AuthSession,
  AvailabilityWindow,
  ChatMessage,
  ChatThread,
  Coach,
  CoachingConfig,
  CoachingSession,
  Conversation,
  Outcome,
  Profile,
  Role,
  Slot,
  SubmitMessage,
} from './types';

export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

type TokenProvider = () => string | null;

/** Typed client for the Go API. One instance per app, token injected lazily. */
export class ApiClient {
  private token: TokenProvider = () => null;
  private onUnauthorized: () => void = () => undefined;

  useToken(provider: TokenProvider): void {
    this.token = provider;
  }

  /** Called once per rejected authenticated request so the app can sign out. */
  onSessionRejected(handler: () => void): void {
    this.onUnauthorized = handler;
  }

  /**
   * Reports a session the backend refused somewhere other than a REST call,
   * running the same sign-out handler a 401 does. token is the one the caller
   * was using: a rejection arriving after the user signed in again is ignored
   * so it cannot drop the newer session.
   */
  rejectSession(token: string): void {
    if (token && token === this.token()) this.onUnauthorized();
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const token = this.token();
    const response = await fetch(`${API_BASE_URL}${API_PREFIX}${path}`, {
      method,
      headers: {
        Accept: 'application/json',
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });

    const text = await response.text();
    const payload = text ? (JSON.parse(text) as unknown) : null;

    if (!response.ok) {
      if (response.status === 401 && token && token === this.token()) {
        this.onUnauthorized();
      }
      const message =
        payload && typeof payload === 'object' && 'error' in payload
          ? String((payload as { error: unknown }).error)
          : `request failed with ${response.status}`;
      throw new ApiError(response.status, message);
    }
    return payload as T;
  }

  // --- auth -----------------------------------------------------------------

  signUp(input: { email: string; password: string; display_name: string; role: Role }) {
    return this.request<AuthSession>('POST', '/auth/signup', input);
  }

  signIn(input: { email: string; password: string }) {
    return this.request<AuthSession>('POST', '/auth/signin', input);
  }

  /**
   * Confirms an email address. The session's token is filled in when the
   * caller is authenticated as the account being verified, and empty otherwise
   * (e.g. verifying from a signed-out browser).
   */
  verifyEmail(token: string) {
    return this.request<AuthSession>('POST', '/auth/verify', { token });
  }

  resendVerification(email: string) {
    return this.request<{ status: string }>('POST', '/auth/resend-verification', { email });
  }

  me() {
    return this.request<Profile>('GET', '/auth/me');
  }

  /**
   * Deletes the authenticated account. Resolves on 202: the sessions are dead
   * once it returns, but the rows are removed by a worker afterwards, so the
   * caller must drop its token rather than read anything back. Repeating the
   * call with the now-revoked token is safe.
   */
  deleteAccount() {
    return this.request<{ status: string }>('DELETE', '/auth/me');
  }

  // --- coaching -------------------------------------------------------------

  async listCoaches(acceptingOnly = true): Promise<Coach[]> {
    const { coaches } = await this.request<{ coaches: Coach[] | null }>(
      'GET',
      `/coaching/coaches?accepting_only=${acceptingOnly}`,
    );
    return coaches ?? [];
  }

  getCoach(coachID: string) {
    return this.request<Coach>('GET', `/coaching/coaches/${coachID}`);
  }

  async coachAvailability(coachID: string): Promise<AvailabilityWindow[]> {
    const { availability } = await this.request<{ availability: AvailabilityWindow[] | null }>(
      'GET',
      `/coaching/coaches/${coachID}/availability`,
    );
    return availability ?? [];
  }

  async openSlots(coachID: string, durationMinutes = 45, excludeSessionID?: string): Promise<Slot[]> {
    const exclude = excludeSessionID ? `&exclude_session_id=${excludeSessionID}` : '';
    const { slots } = await this.request<{ slots: Slot[] | null }>(
      'GET',
      `/coaching/coaches/${coachID}/slots?duration_minutes=${durationMinutes}${exclude}`,
    );
    return slots ?? [];
  }

  coachingConfig() {
    return this.request<CoachingConfig>('GET', '/coaching/config');
  }

  bookSession(input: {
    coach_id: string;
    scheduled_time: string;
    duration_minutes: number;
    topic: string;
  }) {
    return this.request<CoachingSession>('POST', '/coaching/sessions', input);
  }

  async mySessions(status?: string): Promise<CoachingSession[]> {
    const query = status ? `?status=${encodeURIComponent(status)}` : '';
    const { sessions } = await this.request<{ sessions: CoachingSession[] | null }>(
      'GET',
      `/coaching/sessions${query}`,
    );
    return sessions ?? [];
  }

  cancelSession(sessionID: string) {
    return this.request<CoachingSession>('POST', `/coaching/sessions/${sessionID}/cancel`);
  }

  rescheduleSession(sessionID: string, scheduledTime: string) {
    return this.request<CoachingSession>('POST', `/coaching/sessions/${sessionID}/reschedule`, {
      scheduled_time: scheduledTime,
    });
  }

  // --- coach side -----------------------------------------------------------

  upsertCoachProfile(input: {
    headline: string;
    bio: string;
    specialties: string[];
    hourly_rate_cents: number;
    timezone: string;
    years_experience: number;
    accepting_clients: boolean;
  }) {
    return this.request<Coach>('PUT', '/coach/profile', input);
  }

  setAvailability(windows: AvailabilityWindow[]) {
    return this.request<{ availability: AvailabilityWindow[] }>('PUT', '/coach/availability', {
      availability: windows,
    });
  }

  async coachSessions(status?: string): Promise<CoachingSession[]> {
    const query = status ? `?status=${encodeURIComponent(status)}` : '';
    const { sessions } = await this.request<{ sessions: CoachingSession[] | null }>(
      'GET',
      `/coach/sessions${query}`,
    );
    return sessions ?? [];
  }

  setSessionStatus(sessionID: string, status: string) {
    return this.request<CoachingSession>('POST', `/coach/sessions/${sessionID}/status`, { status });
  }

  setSessionNotes(sessionID: string, notes: string) {
    return this.request<CoachingSession>('POST', `/coach/sessions/${sessionID}/notes`, {
      coach_notes: notes,
    });
  }

  async coachThreads(status = 'active'): Promise<ChatThread[]> {
    const { threads } = await this.request<{ threads: ChatThread[] | null }>(
      'GET',
      `/chat/coach/threads?status=${encodeURIComponent(status)}`,
    );
    return threads ?? [];
  }

  // --- live chat ------------------------------------------------------------

  startThread(coachID: string, sessionID?: string) {
    return this.request<ChatThread>('POST', '/chat/threads', {
      coach_id: coachID,
      session_id: sessionID ?? null,
    });
  }

  async threads(): Promise<ChatThread[]> {
    const { threads } = await this.request<{ threads: ChatThread[] | null }>('GET', '/chat/threads');
    return threads ?? [];
  }

  async threadMessages(threadID: string, limit = 50): Promise<ChatMessage[]> {
    const { messages } = await this.request<{ messages: ChatMessage[] | null }>(
      'GET',
      `/chat/threads/${threadID}/messages?limit=${limit}`,
    );
    return messages ?? [];
  }

  sendMessage(threadID: string, body: string) {
    return this.request<ChatMessage>('POST', `/chat/threads/${threadID}/messages`, { body });
  }

  closeThread(threadID: string) {
    return this.request<ChatThread>('POST', `/chat/threads/${threadID}/close`);
  }

  // --- conversation analysis ------------------------------------------------

  submitConversation(input: {
    title: string;
    platform: string;
    match_name: string;
    messages: SubmitMessage[];
  }) {
    return this.request<AnalysisResult>('POST', '/analysis/conversations', input);
  }

  async conversations(): Promise<Conversation[]> {
    const { conversations } = await this.request<{ conversations: Conversation[] | null }>(
      'GET',
      '/analysis/conversations',
    );
    return conversations ?? [];
  }

  analysisResult(analysisID: string) {
    return this.request<AnalysisResult>('GET', `/analysis/results/${analysisID}`);
  }

  latestResult(conversationID: string) {
    return this.request<AnalysisResult>(
      'GET',
      `/analysis/conversations/${conversationID}/result`,
    );
  }

  labelConversation(
    conversationID: string,
    input: { outcome?: Outcome; reply_received?: boolean; notes?: string; consented: boolean },
  ) {
    return this.request<unknown>('POST', `/analysis/conversations/${conversationID}/label`, {
      outcome: input.outcome ?? null,
      reply_received: input.reply_received ?? null,
      engagement_score: null,
      segment_labels: null,
      notes: input.notes ?? '',
      consented: input.consented,
      label_source: 'user',
    });
  }
}

export const api = new ApiClient();
