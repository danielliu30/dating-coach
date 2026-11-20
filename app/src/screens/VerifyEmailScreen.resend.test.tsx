import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { useAuth } from '../state/auth';
import VerifyEmailScreen from './VerifyEmailScreen';

jest.mock('../state/auth', () => ({ useAuth: jest.fn() }));
jest.mock('../components/AuthLayout', () => {
  const ReactActual = require('react');
  return { AuthLayout: ({ children }: { children: React.ReactNode }) => ReactActual.createElement(ReactActual.Fragment, null, children) };
});

const mockUseAuth = useAuth as jest.Mock;

/** Renders the Verify screen with a useAuth stub whose resend method is `resendVerification`. */
function mount(resendVerification: jest.Mock) {
  mockUseAuth.mockReturnValue({
    verify: jest.fn(),
    resendVerification,
    token: null,
    user: null,
  });
  const ScreenAny = VerifyEmailScreen as unknown as React.ComponentType<{ navigation: unknown; route: unknown }>;
  return render(<ScreenAny navigation={{ navigate: jest.fn() }} route={{ params: { email: 'a@example.com' } }} />);
}

describe('Resend code', () => {
  it('issues a single request while one is in flight, then re-enables', async () => {
    let finish: () => void = () => {};
    const resendVerification = jest.fn(() => new Promise<{ status: string; resend_after_seconds: number }>((resolve) => {
      finish = () => resolve({ status: 'sent', resend_after_seconds: 90 });
    }));
    mount(resendVerification);

    const button = screen.getByRole('button', { name: 'Resend code' });
    fireEvent.press(button);
    fireEvent.press(button);
    fireEvent.press(button);

    expect(resendVerification).toHaveBeenCalledTimes(1);
    expect(resendVerification).toHaveBeenCalledWith('a@example.com');
    expect(screen.getByRole('button', { name: 'Sending…' })).toBeDisabled();

    await act(async () => finish());
    await waitFor(() => expect(screen.getByText('If your email is unverified, check your inbox for a code. Please wait 90 seconds before requesting another code.')).toBeTruthy());
    expect(screen.getByRole('button', { name: 'Resend code' })).toBeEnabled();
  });

  it.each([undefined, 0, -1, Number.NaN])('uses a safe default when cooldown is %s', async (cooldown) => {
    mount(jest.fn().mockResolvedValue({ status: 'sent', resend_after_seconds: cooldown }));
    fireEvent.press(screen.getByRole('button', { name: 'Resend code' }));
    await waitFor(() => expect(screen.getByText(/Please wait 60 seconds/)).toBeTruthy());
  });

  it('re-enables and shows the error when the resend fails', async () => {
    const resendVerification = jest.fn().mockRejectedValue(new Error('too many requests'));
    mount(resendVerification);

    fireEvent.press(screen.getByRole('button', { name: 'Resend code' }));
    await waitFor(() => expect(screen.getByText('too many requests')).toBeTruthy());
    expect(screen.getByRole('button', { name: 'Resend code' })).toBeEnabled();
  });
});
