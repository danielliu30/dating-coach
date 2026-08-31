import { API_BASE_URL, API_PREFIX } from '../config';
import type {
  AnalysisResult,
  AuthSession,
  AvailabilityWindow,
  ChatMessage,
  ChatThread,
  Coach,
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

/**
 * Outcome of trading the stored refresh token for a new access token:
 * 'renewed' when a fresh one is in place, 'invalid' when the refresh token is
 * gone or rejected and the session is over, 'unavailable' when the exchange
 * could not be completed (offline, 5xx) and the session may still be good.
 */
export type RenewalOutcome = 'renewed' | 'invalid' | 'unavailable';

type SessionRenewer = () => Promise<RenewalOutcome>;

/**
 * What a request does with a 401 it cannot use its own token to avoid:
 * 'renew' trades the refresh token and retries once, 'sign-out' ends the
 * session immediately, and 'defer' leaves the decision to the caller — the
 * renewal exchange itself uses that, since its 401 is the input to the
 * decision rather than a second one.
 */
type UnauthorizedPolicy = 'renew' | 'sign-out' | 'defer';

/** Typed client for the Go API. One instance per app, token injected lazily. */
export class ApiClient {
  private token: TokenProvider = () => null;
  private onUnauthorized: () => void = () => undefined;
  private renewSession: SessionRenewer = async () => 'invalid';
  // Counts completed renewals, so a request that started before one can tell a
  // token replaced by renewal from one replaced by a different sign-in.
  private renewals = 0;

  useToken(provider: TokenProvider): void {
    this.token = provider;
  }

  /** Called once per rejected authenticated request so the app can sign out. */
  onSessionRejected(handler: () => void): void {
    this.onUnauthorized = handler;
  }

  /**
   * Registers the renewal handler used when an access token is rejected.
   * Access tokens are short-lived, so a 401 usually means "expired", not
   * "signed out": the request is retried once after a successful renewal, and
   * only an 'invalid' outcome signs the user out — a renewal that merely could
   * not be completed fails the request and leaves the session in place.
   */
  onAccessTokenExpired(handler: SessionRenewer): void {
    this.renewSession = async () => {
      const outcome = await handler();
      if (outcome === 'renewed') this.renewals += 1;
      return outcome;
    };
  }

  /**
   * Performs one API call, retrying it once with a renewed access token when
   * the first attempt is rejected — whether this request triggered the renewal
   * or merely raced one another request had already started. unauthorized
   * decides what a 401 means here; the retry uses 'sign-out' so the recursion
   * stops after one renewal.
   */
  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    unauthorized: UnauthorizedPolicy = 'renew',
  ): Promise<T> {
    const token = this.token();
    const renewals = this.renewals;
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
      if (response.status === 401 && unauthorized !== 'defer' && token) {
        if (token === this.token()) {
          const outcome = unauthorized === 'renew' ? await this.renewSession() : 'invalid';
          if (outcome === 'renewed') return this.request<T>(method, path, body, 'sign-out');
          if (outcome === 'invalid') this.onUnauthorized();
        } else if (unauthorized === 'renew' && this.renewals !== renewals && this.token()) {
          // A concurrent renewal replaced the token this request was sent with:
          // the rejection is stale, not a verdict on the session. Any other
          // change of token is a different session, which must not be replayed
          // into.
          return this.request<T>(method, path, body, 'sign-out');
        }
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
   * Exchanges a refresh token for a new session, rotating the refresh token in
   * the process: the value passed in is dead once this resolves, so callers
   * must store the returned one. Throws ApiError(401) when it was already
   * spent, revoked or expired, which is unrecoverable without signing in.
   */
  refresh(refreshToken: string) {
    return this.request<AuthSession>('POST', '/auth/refresh', { refresh_token: refreshToken }, 'defer');
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
