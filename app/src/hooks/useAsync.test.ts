import { act, renderHook, waitFor } from '@testing-library/react-native';

import { useAsync } from './useAsync';

const mockFocusEffect = jest.fn();
jest.mock('@react-navigation/native', () => ({
  useFocusEffect: (effect: () => void) => mockFocusEffect(effect),
}));

/** Runs the most recent focus callback the hook registered, as a navigator would on focus. */
const focus = () => {
  const effect = mockFocusEffect.mock.calls.at(-1)?.[0] as (() => void) | undefined;
  if (!effect) throw new Error('useFocusEffect was not registered');
  act(() => effect());
};

/** Creates a promise whose settlement the test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('useAsync', () => {
  it('loads on focus and exposes the data', async () => {
    const loader = jest.fn(async () => 'loaded');
    const { result } = renderHook(() => useAsync(loader));

    expect(result.current.loading).toBe(true);
    expect(loader).not.toHaveBeenCalled();

    focus();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(loader).toHaveBeenCalledTimes(1);
    expect(result.current.data).toBe('loaded');
    expect(result.current.error).toBeNull();
  });

  it('ignores a stale run that settles after a newer one', async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const loader = jest.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const { result } = renderHook(() => useAsync(loader));

    focus();
    let reload: Promise<void> = Promise.resolve();
    act(() => {
      reload = result.current.reload();
    });
    await act(async () => {
      second.resolve('second');
      await reload;
    });
    expect(result.current.data).toBe('second');

    await act(async () => {
      first.resolve('first');
      await first.promise;
    });
    expect(result.current.data).toBe('second');
    expect(result.current.loading).toBe(false);
  });

  it('reload runs the loader again', async () => {
    const loader = jest.fn().mockResolvedValueOnce('one').mockResolvedValueOnce('two');
    const { result } = renderHook(() => useAsync(loader));

    focus();
    await waitFor(() => expect(result.current.data).toBe('one'));

    await act(async () => {
      await result.current.reload();
    });
    expect(loader).toHaveBeenCalledTimes(2);
    expect(result.current.data).toBe('two');
  });

  it('surfaces the loader error message', async () => {
    const loader = jest.fn(async () => {
      throw new Error('boom');
    });
    const { result } = renderHook(() => useAsync(loader));

    focus();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBe('boom');
    expect(result.current.data).toBeNull();
  });

  it('falls back to a generic message for non-Error rejections', async () => {
    const loader = jest.fn(async () => {
      throw 'nope';
    });
    const { result } = renderHook(() => useAsync(loader));

    focus();
    await waitFor(() => expect(result.current.error).toBe('something went wrong'));
  });
});
