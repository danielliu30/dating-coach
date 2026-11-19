import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import ForgotPasswordScreen from './ForgotPasswordScreen';

jest.mock('../api/client', () => ({ api: { forgotPassword: jest.fn(), resetPassword: jest.fn() } }));
jest.mock('../components/AuthLayout', () => {
  const ReactActual = require('react');
  return { AuthLayout: ({ children }: { children: React.ReactNode }) => ReactActual.createElement(ReactActual.Fragment, null, children) };
});

const navigate = jest.fn();
function mount() {
  const Screen = ForgotPasswordScreen as unknown as React.ComponentType<{ navigation: unknown; route: unknown }>;
  return render(<Screen navigation={{ navigate }} route={{ params: { email: 'a@example.com' } }} />);
}

beforeEach(() => {
  jest.clearAllMocks();
  jest.useFakeTimers();
  (api.forgotPassword as jest.Mock).mockResolvedValue({ status: 'sent' });
  (api.resetPassword as jest.Mock).mockResolvedValue({ status: 'reset' });
});

afterEach(() => jest.useRealTimers());

async function send() {
  fireEvent.press(screen.getByRole('button', { name: 'Send code' }));
  await waitFor(() => expect(screen.getByLabelText('Reset code')).toBeTruthy());
}

it('sends a code, resends, and resets before returning to sign in', async () => {
  mount();
  await send();
  expect(api.forgotPassword).toHaveBeenCalledWith('a@example.com');
  expect(screen.getByRole('button', { name: 'Resend code (60s)' })).toBeDisabled();
  fireEvent.changeText(screen.getByLabelText('Reset code'), '654321');
  act(() => jest.advanceTimersByTime(60_000));
  fireEvent.press(screen.getByRole('button', { name: 'Resend code' }));
  await waitFor(() => expect(api.forgotPassword).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.getByLabelText('Reset code').props.value).toBe(''));
  fireEvent.changeText(screen.getByLabelText('Reset code'), '123456');
  fireEvent.changeText(screen.getByLabelText('New password'), 'new-password');
  fireEvent.changeText(screen.getByLabelText('Confirm password'), 'new-password');
  fireEvent.press(screen.getByRole('button', { name: 'Reset password' }));
  await waitFor(() => expect(navigate).toHaveBeenCalledWith('SignIn', {
    email: 'a@example.com', notice: 'Your password has been reset. Sign in with your new password.',
  }));
  expect(api.resetPassword).toHaveBeenCalledWith('a@example.com', '123456', 'new-password');
});

it('returns to email entry and clears the reset fields', async () => {
  mount();
  await send();
  fireEvent.changeText(screen.getByLabelText('Reset code'), '123456');
  fireEvent.press(screen.getByRole('button', { name: 'Use a different email' }));
  expect(screen.queryByLabelText('Reset code')).toBeNull();
  fireEvent.changeText(screen.getByLabelText('Email'), 'other@example.com');
  await send();
  expect(api.forgotPassword).toHaveBeenLastCalledWith('other@example.com');
  expect(screen.getByLabelText('Reset code').props.value).toBe('');
});

it.each([
  ['é'.repeat(37), 'Password must be at most 72 bytes.'],
  [' '.repeat(8), 'Password cannot be only whitespace.'],
])('rejects an invalid password locally', async (password, message) => {
  mount();
  await send();
  fireEvent.changeText(screen.getByLabelText('New password'), password);
  fireEvent.changeText(screen.getByLabelText('Confirm password'), password);
  fireEvent.press(screen.getByRole('button', { name: 'Reset password' }));
  expect(screen.getByText(message)).toBeTruthy();
  expect(api.resetPassword).not.toHaveBeenCalled();
});

it('shows backend errors', async () => {
  (api.resetPassword as jest.Mock).mockRejectedValue(new Error('invalid or expired reset code'));
  mount();
  await send();
  fireEvent.changeText(screen.getByLabelText('New password'), 'new-password');
  fireEvent.changeText(screen.getByLabelText('Confirm password'), 'new-password');
  fireEvent.press(screen.getByRole('button', { name: 'Reset password' }));
  await waitFor(() => expect(screen.getByText('invalid or expired reset code')).toBeTruthy());
  expect(navigate).not.toHaveBeenCalled();
});

it('rejects short and mismatched passwords locally', async () => {
  mount();
  await send();
  fireEvent.press(screen.getByRole('button', { name: 'Reset password' }));
  expect(screen.getByText('Password must be at least 8 characters.')).toBeTruthy();
  fireEvent.changeText(screen.getByLabelText('New password'), 'new-password');
  fireEvent.press(screen.getByRole('button', { name: 'Reset password' }));
  expect(screen.getByText('Passwords do not match.')).toBeTruthy();
  expect(api.resetPassword).not.toHaveBeenCalled();
});
