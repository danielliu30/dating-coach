import { API_BASE_URL, API_PREFIX } from '../config';
import { ApiClient, ApiError } from './client';

type FetchMock = jest.MockedFunction<typeof fetch>;

/** Builds a minimal Response-like object the client can read `ok`, `status` and `text()` from. */
const reply = (status: number, body?: unknown): Response =>
  ({
    ok: status >= 200 && status < 300,
    status,
    text: async () => (body === undefined ? '' : JSON.stringify(body)),
  }) as Response;

/** Creates a promise whose settlement the test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

let fetchMock: FetchMock;

beforeEach(() => {
  fetchMock = jest.fn() as FetchMock;
  globalThis.fetch = fetchMock;
});

const lastRequest = () => {
  const call = fetchMock.mock.calls.at(-1);
  if (!call) throw new Error('fetch was not called');
  return { url: String(call[0]), init: call[1] as RequestInit };
};

describe('ApiClient request plumbing', () => {
  it('builds the URL from API_BASE_URL + API_PREFIX + path and sends JSON', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { token: 't' }));
    const client = new ApiClient();

    await client.signIn({ email: 'a@b.c', password: 'pw' });

    const { url, init } = lastRequest();
    expect(url).toBe(`${API_BASE_URL}${API_PREFIX}/auth/signin`);
    expect(init.method).toBe('POST');
    expect(init.headers).toMatchObject({ Accept: 'application/json', 'Content-Type': 'application/json' });
    expect(init.headers).not.toHaveProperty('Authorization');
    expect(init.body).toBe(JSON.stringify({ email: 'a@b.c', password: 'pw' }));
  });

  it('sends Authorization: Bearer <token> and no Content-Type on body-less requests', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { id: 'u1' }));
    const client = new ApiClient();
    client.useToken(() => 'tok');

    await client.me();

    const { url, init } = lastRequest();
    expect(url).toBe(`${API_BASE_URL}${API_PREFIX}/auth/me`);
    expect(init.method).toBe('GET');
    expect(init.headers).toMatchObject({ Authorization: 'Bearer tok' });
    expect(init.headers).not.toHaveProperty('Content-Type');
    expect(init.body).toBeUndefined();
  });

  it('throws ApiError with status and the parsed error message', async () => {
    fetchMock.mockResolvedValueOnce(reply(400, { error: 'bad input' }));
    const client = new ApiClient();

    const failure = client.signIn({ email: 'a', password: 'b' }).catch((e: unknown) => e);
    const error = await failure;
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).status).toBe(400);
    expect((error as ApiError).message).toBe('bad input');
  });

  it('falls back to a generic message when the error body has no error field', async () => {
    fetchMock.mockResolvedValueOnce(reply(500));
    const client = new ApiClient();

    await expect(client.me()).rejects.toMatchObject({ status: 500, message: 'request failed with 500' });
  });
});

describe('ApiClient 401 renewal', () => {
  it('renews once and retries a token-carrying request on 401', async () => {
    let token = 'old';
    fetchMock.mockResolvedValueOnce(reply(401, { error: 'expired' })).mockResolvedValueOnce(reply(200, { id: 'me' }));
    const client = new ApiClient();
    client.useToken(() => token);
    const renew = jest.fn(async () => {
      token = 'new';
      return 'renewed' as const;
    });
    client.useRenewal(renew);

    await expect(client.me()).resolves.toEqual({ id: 'me' });

    expect(renew).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toMatchObject({ Authorization: 'Bearer new' });
  });

  it('does not renew for a 401 on an unauthenticated request', async () => {
    fetchMock.mockResolvedValueOnce(reply(401, { error: 'nope' }));
    const client = new ApiClient();
    const renew = jest.fn(async () => 'renewed' as const);
    client.useRenewal(renew);

    await expect(client.signIn({ email: 'a', password: 'b' })).rejects.toMatchObject({ status: 401 });
    expect(renew).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('retries only once: a second 401 after renewal is surfaced', async () => {
    fetchMock.mockResolvedValue(reply(401, { error: 'still no' }));
    const client = new ApiClient();
    client.useToken(() => 'tok');
    const renew = jest.fn(async () => 'renewed' as const);
    client.useRenewal(renew);

    await expect(client.me()).rejects.toMatchObject({ status: 401, message: 'still no' });
    expect(renew).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('reports a rejected renewal to onSessionRejected and does not retry', async () => {
    fetchMock.mockResolvedValueOnce(reply(401, { error: 'expired' }));
    const client = new ApiClient();
    client.useToken(() => 'tok');
    client.useRenewal(async () => 'rejected');
    const rejected = jest.fn();
    client.onSessionRejected(rejected);

    await expect(client.me()).rejects.toMatchObject({ status: 401 });
    expect(rejected).toHaveBeenCalledWith('expired');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('passes a stronger `revoked` reason through to the rejection handler', async () => {
    const client = new ApiClient();
    client.useRenewal(async () => 'rejected');
    const rejected = jest.fn();
    client.onSessionRejected(rejected);

    await expect(client.renewSession('revoked')).resolves.toBe('rejected');
    expect(rejected).toHaveBeenCalledWith('revoked');
  });

  it('retries with the fresher token instead of renewing when another caller already renewed', async () => {
    let token = 'old';
    const client = new ApiClient();
    client.useToken(() => token);
    const renew = jest.fn(async () => 'renewed' as const);
    client.useRenewal(renew);

    // Rotate the token between the request being sent and the 401 arriving.
    fetchMock
      .mockImplementationOnce(async () => {
        token = 'fresh';
        return reply(401, { error: 'expired' });
      })
      .mockResolvedValueOnce(reply(200, { id: 'me' }));

    await expect(client.me()).resolves.toEqual({ id: 'me' });
    expect(renew).not.toHaveBeenCalled();
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toMatchObject({ Authorization: 'Bearer fresh' });
  });

  it('never retries a request whose principal changed while it was in flight', async () => {
    let principal = 1;
    fetchMock.mockImplementationOnce(async () => {
      principal = 2;
      return reply(401, { error: 'expired' });
    });
    const client = new ApiClient();
    client.useToken(() => 'tok');
    client.usePrincipal(() => principal);
    const renew = jest.fn(async () => 'renewed' as const);
    client.useRenewal(renew);

    await expect(client.me()).rejects.toMatchObject({ status: 401 });
    expect(renew).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not retry when the principal changes during the renewal itself', async () => {
    let principal = 1;
    fetchMock.mockResolvedValueOnce(reply(401, { error: 'expired' }));
    const client = new ApiClient();
    client.useToken(() => 'tok');
    client.usePrincipal(() => principal);
    client.useRenewal(async () => {
      principal = 2;
      return 'renewed';
    });

    await expect(client.me()).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('collapses concurrent 401s onto a single renewal', async () => {
    const renewal = deferred<'renewed'>();
    let token = 'old';
    fetchMock
      .mockResolvedValueOnce(reply(401, { error: 'expired' }))
      .mockResolvedValueOnce(reply(401, { error: 'expired' }))
      .mockResolvedValue(reply(200, { ok: true }));
    const client = new ApiClient();
    client.useToken(() => token);
    const renew = jest.fn(() => renewal.promise);
    client.useRenewal(renew);

    const first = client.me();
    const second = client.coachingConfig();
    await Promise.resolve();
    await Promise.resolve();
    token = 'new';
    renewal.resolve('renewed');

    await expect(Promise.all([first, second])).resolves.toEqual([{ ok: true }, { ok: true }]);
    expect(renew).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });

  it('runs a fresh renewal after the previous one settled', async () => {
    const client = new ApiClient();
    const renew = jest.fn(async () => 'unavailable' as const);
    client.useRenewal(renew);

    await client.renewSession();
    await client.renewSession();
    expect(renew).toHaveBeenCalledTimes(2);
  });
});

describe('ApiClient wrappers', () => {
  it.each([
    ['listCoaches', (c: ApiClient) => c.listCoaches(), '/coaching/coaches?accepting_only=true', 'coaches'],
    ['coachAvailability', (c: ApiClient) => c.coachAvailability('c1'), '/coaching/coaches/c1/availability', 'availability'],
    ['openSlots', (c: ApiClient) => c.openSlots('c1'), '/coaching/coaches/c1/slots?duration_minutes=45', 'slots'],
    ['mySessions', (c: ApiClient) => c.mySessions(), '/coaching/sessions', 'sessions'],
    ['coachSessions', (c: ApiClient) => c.coachSessions('scheduled'), '/coach/sessions?status=scheduled', 'sessions'],
    ['coachThreads', (c: ApiClient) => c.coachThreads(), '/chat/coach/threads?status=active', 'threads'],
    ['threads', (c: ApiClient) => c.threads(), '/chat/threads', 'threads'],
    ['threadMessages', (c: ApiClient) => c.threadMessages('t1'), '/chat/threads/t1/messages?limit=50', 'messages'],
    ['conversations', (c: ApiClient) => c.conversations(), '/analysis/conversations', 'conversations'],
  ] as const)('%s unwraps the envelope and falls back to [] on null', async (_name, call, path, key) => {
    const client = new ApiClient();
    fetchMock.mockResolvedValueOnce(reply(200, { [key]: null }));
    await expect(call(client)).resolves.toEqual([]);
    expect(lastRequest().url).toBe(`${API_BASE_URL}${API_PREFIX}${path}`);

    fetchMock.mockResolvedValueOnce(reply(200, { [key]: [{ id: 'x' }] }));
    await expect(call(client)).resolves.toEqual([{ id: 'x' }]);
  });

  it('openSlots appends exclude_session_id when rescheduling', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { slots: [] }));
    await new ApiClient().openSlots('c1', 30, 's9');
    expect(lastRequest().url).toContain('/coaching/coaches/c1/slots?duration_minutes=30&exclude_session_id=s9');
  });

  it('startThread sends null for a missing session id', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { id: 't1' }));
    await new ApiClient().startThread('c1');
    expect(lastRequest().init.body).toBe(JSON.stringify({ coach_id: 'c1', session_id: null }));
  });

  it('refreshSession never triggers a renewal on 401', async () => {
    fetchMock.mockResolvedValueOnce(reply(401, { error: 'spent' }));
    const client = new ApiClient();
    client.useToken(() => 'tok');
    const renew = jest.fn(async () => 'renewed' as const);
    client.useRenewal(renew);

    await expect(client.refreshSession('r')).rejects.toMatchObject({ status: 401 });
    expect(renew).not.toHaveBeenCalled();
  });

  it('labelConversation fills the nulls the API expects', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, {}));
    await new ApiClient().labelConversation('c1', { outcome: 'date_set', consented: true });
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({
      outcome: 'date_set',
      reply_received: null,
      engagement_score: null,
      segment_labels: null,
      notes: '',
      consented: true,
      label_source: 'user',
    });
  });
});
