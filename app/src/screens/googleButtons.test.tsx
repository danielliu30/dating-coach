import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { useAuth } from '../state/auth';
import SignInScreen from './SignInScreen';
import SignUpScreen from './SignUpScreen';

jest.mock('../state/auth', () => ({ useAuth: jest.fn() }));
jest.mock('../components/AuthLayout', () => {
  const ReactActual = require('react');
  return { AuthLayout: ({ children }: { children: React.ReactNode }) => ReactActual.createElement(ReactActual.Fragment, null, children) };
});
jest.mock('../lib/googleIdentity', () => ({ googleSignInAvailable: true }));

const mockUseAuth = useAuth as jest.Mock;
const googleIdentity = jest.requireMock('../lib/googleIdentity') as { googleSignInAvailable: boolean };

type Nav = { navigate: jest.Mock };
const nav = (): Nav => ({ navigate: jest.fn() });

/** Renders `Screen` with a useAuth stub whose Google method is `signInWithGoogle`. */
function mount(Screen: typeof SignInScreen | typeof SignUpScreen, signInWithGoogle: jest.Mock, navigation: Nav) {
  mockUseAuth.mockReturnValue({
    signIn: jest.fn(),
    signUp: jest.fn(),
    signInWithGoogle,
    signedOutReason: null,
    dismissSignedOutReason: jest.fn(),
  });
  const ScreenAny = Screen as unknown as React.ComponentType<{ navigation: Nav; route: unknown }>;
  return render(<ScreenAny navigation={navigation} route={{}} />);
}

const googleButton = () => screen.getByRole('button', { name: /Continue with Google/ });

describe('Continue with Google', () => {
  afterEach(() => {
    googleIdentity.googleSignInAvailable = true;
  });

  it('is hidden on both screens when the build has no Google client ID', () => {
    googleIdentity.googleSignInAvailable = false;
    mount(SignInScreen, jest.fn(), nav());
    expect(screen.queryByRole('button', { name: /Continue with Google/ })).toBeNull();
    screen.unmount();
    mount(SignUpScreen, jest.fn(), nav());
    expect(screen.queryByRole('button', { name: /Continue with Google/ })).toBeNull();
    expect(screen.getByText('We email a verification code right after sign-up.')).toBeTruthy();
  });

  it('signs in without a role from the sign-in screen and surfaces failures inline', async () => {
    const signInWithGoogle = jest.fn().mockRejectedValueOnce(new Error('Google sign-in was closed before finishing'));
    const navigation = nav();
    mount(SignInScreen, signInWithGoogle, navigation);

    fireEvent.press(googleButton());
    await waitFor(() => expect(screen.getByText('Google sign-in was closed before finishing')).toBeTruthy());
    expect(signInWithGoogle).toHaveBeenCalledWith();
    expect(navigation.navigate).not.toHaveBeenCalled();
  });

  it('signs up with the selected role and never routes to Verify', async () => {
    const signInWithGoogle = jest.fn().mockResolvedValue({ email_verified: true });
    const navigation = nav();
    mount(SignUpScreen, signInWithGoogle, navigation);

    fireEvent.press(screen.getByRole('radio', { name: /A coach/ }));
    fireEvent.press(screen.getByRole('button', { name: 'Continue with Google as a coach' }));
    await waitFor(() => expect(signInWithGoogle).toHaveBeenCalledWith('coach'));
    expect(navigation.navigate).not.toHaveBeenCalled();
  });
});
