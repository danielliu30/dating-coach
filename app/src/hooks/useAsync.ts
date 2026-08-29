import { useFocusEffect } from '@react-navigation/native';
import { useCallback, useRef, useState } from 'react';

interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => Promise<void>;
}

/** Loads data on focus (so lists refresh after booking, sending, etc.). */
export function useAsync<T>(loader: () => Promise<T>, deps: unknown[] = []): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  // Only the newest run may write state, so a slow earlier load cannot replace it.
  const latest = useRef(0);

  // eslint-disable-next-line react-hooks/exhaustive-deps
  const run = useCallback(async () => {
    const runID = ++latest.current;
    setLoading(true);
    setError(null);
    try {
      const result = await loader();
      if (runID !== latest.current) return;
      setData(result);
    } catch (err) {
      if (runID !== latest.current) return;
      setError(err instanceof Error ? err.message : 'something went wrong');
    } finally {
      if (runID === latest.current) setLoading(false);
    }
  }, deps);

  useFocusEffect(
    useCallback(() => {
      void run();
    }, [run]),
  );

  return { data, error, loading, reload: run };
}
