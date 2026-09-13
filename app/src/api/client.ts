import { API_BASE_URL, API_PREFIX } from '../config';
import type {
  AnalysisResult,
  AuthSession,
  AvailabilityWindow,
  ChatMessage,
  ChatThread,
  Coach,
  CoachReview,
  CoachingConfig,
  CoachingSession,
  Conversation,
  DatingPhase,
  DatingProfileInput,
  Outcome,
  Profile,
  ReviewSummary,
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
 * Outcome of a renewal attempt. `unavailable` covers everything that is not a
 * verdict on the refresh token — a network failure, or a renewal superseded by
 * a sign-out or a different sign-in — and leaves the session alone; only
 * `rejected` means the refresh token itself is no good.
 */
export type RenewalResult = 'renewed' | 'rejected' | 'unavailable';

/**
 * Why a session ended without the user asking. `revoked` is claimed only where
 * the API said so; a refused renewal on its own cannot tell a token that aged
 * out from one that was withdrawn, and is reported as `expired`.
 */
export type SessionEndReason = 'expired' | 'revoked';

type SessionRenewer = () => Promise<RenewalResult>;

/** Typed client for the Go API. One instance per app, token injected lazily. */
export class ApiClient {
  private token: TokenProvider = () => null;
  private onUnauthorized: (reason: SessionEndReason) => void = () => undefined;
  private renew: SessionRenewer = async () => 'rejected';
  private renewal: Promise<{ result: RenewalResult; reason: SessionEndReason }> | null = null;
  // Strongest reason any caller of the renewal in flight gave for a refusal.
  private pendingReason: SessionEndReason = 'expired';
  private principal: () => number = () => 0;

  useToken(provider: TokenProvider): void {
    this.token = provider;
  }

  /**
   * Registers a counter identifying who the app is acting for. It must change
   * when a session starts, ends or changes hands and stay put when a renewal
   * swaps the access token, which is what lets a rejected request tell "my
   * token was renewed" from "someone else is signed in now". Without it every
   * token change looks like a renewal.
   */
  usePrincipal(provider: () => number): void {
    this.principal = provider;
  }

  /**
   * Registers how to trade the refresh token in for a new access token. It is
   * called at most once per rejected request, and concurrent requests share the
   * one renewal in flight.
   */
  useRenewal(renew: SessionRenewer): void {
    this.renew = renew;
  }

  /**
   * Called once per rejected authenticated request so the app can sign out.
   * The handler is told why the session ended so a screen can say so.
   */
  onSessionRejected(handler: (reason: SessionEndReason) => void): void {
    this.onUnauthorized = handler;
  }

  /**
   * Trades the refresh token in for a new access token, collapsing concurrent
   * callers onto the one renewal in flight. A `rejected` refresh token is
   * unrecoverable, so the app is signed out before the caller sees the answer;
   * an `unavailable` one leaves the session in place to be retried. Callers
   * outside the HTTP path use this too: the chat socket has no 401 to react to.
   *
   * reason is what the app is told when the renewal is refused. Callers that
   * already know the session was withdrawn pass `revoked`; the default suits
   * everyone else, for whom a refusal is indistinguishable from an expiry. It
   * applies to the shared renewal rather than to the caller, so one caller that
   * knows better speaks for all of them, whoever started the renewal.
   */
  async renewSession(reason: SessionEndReason = 'expired'): Promise<RenewalResult> {
    if (reason === 'revoked') this.pendingReason = 'revoked';
    this.renewal ??= this.runRenewal();
    const { result, reason: ended } = await this.renewal;
    if (result === 'rejected') this.onUnauthorized(ended);
    return result;
  }

  /**
   * Runs the one renewal the concurrent callers share and pairs its verdict
   * with the reason they collectively gave, read at the moment it settles so a
   * caller joining while it is in flight is still accounted for. Clears both
   * afterwards, leaving the next renewal to be asked about from scratch.
   */
  private async runRenewal(): Promise<{ result: RenewalResult; reason: SessionEndReason }> {
    try {
      return { result: await this.renew(), reason: this.pendingReason };
    } finally {
      this.renewal = null;
      this.pendingReason = 'expired';
    }
  }

  private async request<T>(method: string, path: string, body?: unknown, renewed = false): Promise<T> {
    const token = this.token();
    const principal = this.principal();
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
      if (response.status === 401 && token && !renewed && principal === this.principal()) {
        // Access tokens expire within minutes, so a 401 on a request that
        // carried one usually means "renew", not "signed out". A request that
        // was already in flight when someone else renewed is retried with the
        // token it missed instead of rotating the fresh one away. A request
        // whose principal is gone is never retried: its operation belongs to
        // the account that issued it, not to whoever is signed in now.
        const current = this.token();
        if (current && current !== token) return this.request<T>(method, path, body, true);
        if ((await this.renewSession()) === 'renewed' && principal === this.principal()) {
          return this.request<T>(method, path, body, true);
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
   * Exchanges a Google ID token for a session, creating the account with `role`
   * when the address is new (an existing account keeps its role). The session is
   * already verified, so callers skip the Verify screen. Rejects with 401 for a
   * token the API would not accept and 501 when the API has no Google client ID.
   */
  signInWithGoogle(input: { id_token: string; role?: Role }) {
    return this.request<AuthSession>('POST', '/auth/google', input);
  }

  /**
   * Rotates a refresh token into a new session. Rejects with 401 once the token
   * is spent or expired. It never triggers a renewal of its own: it *is* the
   * renewal, so a 401 here is final.
   */
  refreshSession(refreshToken: string) {
    return this.request<AuthSession>('POST', '/auth/refresh', { refresh_token: refreshToken }, true);
  }

  /**
   * Confirms an email address with the 6-digit code it was sent. The session's
   * token is filled in when the caller is authenticated as the account being
   * verified, and empty otherwise (e.g. verifying from a signed-out browser).
   */
  verifyEmail(email: string, code: string) {
    return this.request<AuthSession>('POST', '/auth/verify', { email, code });
  }

  resendVerification(email: string) {
    return this.request<{ status: string }>('POST', '/auth/resend-verification', { email });
  }

  me() {
    return this.request<Profile>('GET', '/auth/me');
  }

  /**
   * Replaces the authenticated account's dating styles and phases wholesale
   * and resolves with the updated profile. Rejects with a 400 ApiError when a
   * value is outside the vocabulary or a phase is listed on both sides.
   */
  updateDatingProfile(input: DatingProfileInput) {
    return this.request<Profile>('PATCH', '/auth/me', input);
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

  /** Fetches server-side coaching switches (e.g. whether booking requires payment). */
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

  async listCoachReviews(coachID: string): Promise<CoachReview[]> {
    return (await this.request<CoachReview[] | null>('GET', `/coaching/coaches/${coachID}/reviews`)) ?? [];
  }

  /** Recommendation line and strengths distilled from the coach's reviews (aggregate-only when ml-analyzer is down). */
  coachReviewSummary(coachID: string) {
    return this.request<ReviewSummary>('GET', `/coaching/coaches/${coachID}/reviews/summary`);
  }

  /** Creates or replaces the caller's review; the server answers 403 without a completed session with the coach. */
  submitCoachReview(coachID: string, input: { rating: number; comment: string; session_id?: string }) {
    return this.request<CoachReview>('POST', `/coaching/coaches/${coachID}/reviews`, input);
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
    phases: DatingPhase[];
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

  /** Sets (or clears, with '') the session's join link. Coach-only; the server rejects non-http(s) URLs. */
  setSessionMeetingUrl(sessionID: string, meetingUrl: string) {
    return this.request<CoachingSession>('POST', `/coach/sessions/${sessionID}/meeting-url`, {
      meeting_url: meetingUrl,
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

  /**
   * Queues a stored conversation for scoring again and resolves with the new
   * pending analysis to poll. A run that is still pending or running is
   * returned unchanged, so calling this twice does not double-queue.
   */
  reanalyzeConversation(conversationID: string) {
    return this.request<AnalysisResult>(
      'POST',
      `/analysis/conversations/${conversationID}/reanalyze`,
    );
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
