import { useFocusEffect } from '@react-navigation/native';
import { useCallback, useState } from 'react';

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

  // eslint-disable-next-line react-hooks/exhaustive-deps
  const run = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setData(await loader());
    } catch (err) {
      setError(err instanceof Error ? err.message : 'something went wrong');
    } finally {
      setLoading(false);
    }
  }, deps);

  useFocusEffect(
    useCallback(() => {
      void run();
    }, [run]),
  );

  return { data, error, loading, reload: run };
}
