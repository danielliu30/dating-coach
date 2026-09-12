import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { ApiError } from '../api/client';
import type { AuthStackParams } from '../navigation/types';
import SignInScreen from './SignInScreen';
import SignUpScreen from './SignUpScreen';
import VerifyEmailScreen from './VerifyEmailScreen';

jest.mock('../components/AuthLayout', () => {
  const React = require('react');
  return { AuthLayout: ({ children }: { children: React.ReactNode }) => <>{children}</> };
});

const mockAuth = {
  signIn: jest.fn(),
  signUp: jest.fn(),
  verify: jest.fn(),
  resendVerification: jest.fn(),
  dismissSignedOutReason: jest.fn(),
  signedOutReason: null as 'expired' | 'revoked' | null,
  token: null as string | null,
  user: null as { email: string } | null,
};

jest.mock('../state/auth', () => ({ useAuth: () => mockAuth }));

const navigation = { navigate: jest.fn() };
type ScreenProps<Name extends keyof AuthStackParams> = React.ComponentProps<
  Name extends 'SignIn' ? typeof SignInScreen : Name extends 'SignUp' ? typeof SignUpScreen : typeof VerifyEmailScreen
>;

/** Builds minimal stack-screen props; only `navigate` and `route.params` are exercised by the screens. */
const props = <Name extends keyof AuthStackParams>(name: Name, params?: AuthStackParams[Name]) =>
  ({ navigation, route: { key: name, name, params } }) as unknown as ScreenProps<Name>;

beforeEach(() => {
  mockAuth.signedOutReason = null;
  mockAuth.token = null;
  mockAuth.user = null;
});

describe('SignInScreen', () => {
  it('submits the trimmed email and raw password', async () => {
    mockAuth.signIn.mockResolvedValue(undefined);
    render(<SignInScreen {...props('SignIn')} />);

    fireEvent.changeText(screen.getByPlaceholderText('you@example.com'), '  a@b.c  ');
    fireEvent.changeText(screen.getByPlaceholderText('••••••••'), 'pw ');
    await act(async () => {
      fireEvent.press(screen.getByText('Sign in'));
    });

    expect(mockAuth.signIn).toHaveBeenCalledWith('a@b.c', 'pw ');
    expect(navigation.navigate).not.toHaveBeenCalled();
    expect(screen.queryByText(/could not/)).toBeNull();
  });

  it('a 403 routes to Verify with the email and shows no error', async () => {
    mockAuth.signIn.mockRejectedValue(new ApiError(403, 'email not verified'));
    render(<SignInScreen {...props('SignIn')} />);

    fireEvent.changeText(screen.getByPlaceholderText('you@example.com'), 'a@b.c');
    await act(async () => {
      fireEvent.press(screen.getByText('Sign in'));
    });

    expect(navigation.navigate).toHaveBeenCalledWith('Verify', { email: 'a@b.c' });
    expect(screen.queryByText('email not verified')).toBeNull();
  });

  it('other errors surface their message and do not navigate', async () => {
    mockAuth.signIn.mockRejectedValue(new ApiError(401, 'invalid credentials'));
    render(<SignInScreen {...props('SignIn')} />);

    await act(async () => {
      fireEvent.press(screen.getByText('Sign in'));
    });

    expect(screen.getByText('invalid credentials')).toBeTruthy();
    expect(navigation.navigate).not.toHaveBeenCalled();
  });

  it('non-Error rejections fall back to generic copy', async () => {
    mockAuth.signIn.mockRejectedValue('boom');
    render(<SignInScreen {...props('SignIn')} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Sign in'));
    });
    expect(screen.getByText('could not sign in')).toBeTruthy();
  });

  it('renders revoked copy and dismisses through the provider', () => {
    mockAuth.signedOutReason = 'revoked';
    render(<SignInScreen {...props('SignIn')} />);

    expect(screen.getByText('Your session was ended')).toBeTruthy();
    expect(screen.getByText(/change your password/)).toBeTruthy();
    fireEvent.press(screen.getByLabelText('Dismiss'));
    expect(mockAuth.dismissSignedOutReason).toHaveBeenCalledTimes(1);
  });

  it('renders expired copy', () => {
    mockAuth.signedOutReason = 'expired';
    render(<SignInScreen {...props('SignIn')} />);
    expect(screen.getByText('You were signed out')).toBeTruthy();
    expect(screen.getByText(/Your session expired/)).toBeTruthy();
  });

  it('shows no notice without a signed-out reason', () => {
    render(<SignInScreen {...props('SignIn')} />);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('"Create an account" navigates to SignUp', () => {
    render(<SignInScreen {...props('SignIn')} />);
    fireEvent.press(screen.getByText('Create an account'));
    expect(navigation.navigate).toHaveBeenCalledWith('SignUp');
  });
});

describe('SignUpScreen', () => {
  const fill = () => {
    fireEvent.changeText(screen.getByPlaceholderText('Alex'), ' Alex ');
    fireEvent.changeText(screen.getByPlaceholderText('you@example.com'), ' a@b.c ');
    fireEvent.changeText(screen.getByPlaceholderText('at least 8 characters'), 'password1');
  };

  it('defaults to the dater role and submits trimmed fields, then routes to Verify', async () => {
    mockAuth.signUp.mockResolvedValue(undefined);
    render(<SignUpScreen {...props('SignUp')} />);
    fill();

    expect(screen.getByRole('radio', { name: /Someone dating/ })).toBeSelected();
    expect(screen.getByRole('radio', { name: /A coach/ })).not.toBeSelected();

    await act(async () => {
      fireEvent.press(screen.getByText('Create account'));
    });

    expect(mockAuth.signUp).toHaveBeenCalledWith({
      email: 'a@b.c',
      password: 'password1',
      displayName: 'Alex',
      role: 'user',
    });
    expect(navigation.navigate).toHaveBeenCalledWith('Verify', { email: 'a@b.c' });
  });

  it('"A coach" maps to the coach role', async () => {
    mockAuth.signUp.mockResolvedValue(undefined);
    render(<SignUpScreen {...props('SignUp')} />);
    fill();

    fireEvent.press(screen.getByText('A coach'));
    expect(screen.getByRole('radio', { name: /A coach/ })).toBeSelected();
    await act(async () => {
      fireEvent.press(screen.getByText('Create account'));
    });

    expect(mockAuth.signUp).toHaveBeenCalledWith(expect.objectContaining({ role: 'coach' }));
  });

  it('a failed sign-up shows the error and stays put', async () => {
    mockAuth.signUp.mockRejectedValue(new ApiError(409, 'email already registered'));
    render(<SignUpScreen {...props('SignUp')} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Create account'));
    });
    expect(screen.getByText('email already registered')).toBeTruthy();
    expect(navigation.navigate).not.toHaveBeenCalled();
  });

  it('"Sign in" footer link navigates to SignIn', () => {
    render(<SignUpScreen {...props('SignUp')} />);
    fireEvent.press(screen.getByText('Sign in'));
    expect(navigation.navigate).toHaveBeenCalledWith('SignIn');
  });
});

describe('VerifyEmailScreen', () => {
  it('seeds the non-editable email from the route param and verifies with the code', async () => {
    mockAuth.verify.mockResolvedValue(null);
    render(<VerifyEmailScreen {...props('Verify', { email: 'route@b.c' })} />);

    const emailField = screen.getByDisplayValue('route@b.c');
    expect(emailField.props.editable).toBe(false);
    fireEvent.changeText(screen.getByPlaceholderText('••••••'), ' 123456 ');
    await act(async () => {
      fireEvent.press(screen.getByText('Verify email'));
    });

    expect(mockAuth.verify).toHaveBeenCalledWith('route@b.c', '123456');
    expect(screen.getByText('Email verified.')).toBeTruthy();
  });

  it('falls back to the signed-in user email when no route param is given', () => {
    mockAuth.user = { email: 'user@b.c' };
    mockAuth.token = 'signup-token';
    render(<VerifyEmailScreen {...props('Verify')} />);
    expect(screen.getByDisplayValue('user@b.c')).toBeTruthy();
    expect(screen.queryByText('Back to sign in')).toBeNull();
  });

  it('navigates to SignIn after verifying when no session token is on hand', async () => {
    mockAuth.verify.mockResolvedValue(null);
    render(<VerifyEmailScreen {...props('Verify', { email: 'a@b.c' })} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Verify email'));
    });
    await waitFor(() => expect(navigation.navigate).toHaveBeenCalledWith('SignIn'));
  });

  it('does not navigate to SignIn when a session token exists after verifying', async () => {
    mockAuth.token = 'session';
    mockAuth.verify.mockResolvedValue(null);
    render(<VerifyEmailScreen {...props('Verify', { email: 'a@b.c' })} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Verify email'));
    });
    expect(screen.getByText('Email verified.')).toBeTruthy();
    expect(navigation.navigate).not.toHaveBeenCalled();
  });

  it('a failed verify surfaces the error', async () => {
    mockAuth.verify.mockRejectedValue(new ApiError(400, 'invalid code'));
    render(<VerifyEmailScreen {...props('Verify', { email: 'a@b.c' })} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Verify email'));
    });
    expect(screen.getByText('invalid code')).toBeTruthy();
    expect(navigation.navigate).not.toHaveBeenCalled();
  });

  it('"Resend code" calls resendVerification and confirms', async () => {
    mockAuth.resendVerification.mockResolvedValue(undefined);
    render(<VerifyEmailScreen {...props('Verify', { email: 'a@b.c' })} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Resend code'));
    });
    expect(mockAuth.resendVerification).toHaveBeenCalledWith('a@b.c');
    expect(screen.getByText('Verification code sent.')).toBeTruthy();
  });

  it('a failed resend surfaces the error', async () => {
    mockAuth.resendVerification.mockRejectedValue(new ApiError(429, 'slow down'));
    render(<VerifyEmailScreen {...props('Verify', { email: 'a@b.c' })} />);
    await act(async () => {
      fireEvent.press(screen.getByText('Resend code'));
    });
    expect(screen.getByText('slow down')).toBeTruthy();
  });

  it('"Back to sign in" is offered only when signed out and navigates to SignIn', () => {
    render(<VerifyEmailScreen {...props('Verify', { email: 'a@b.c' })} />);
    fireEvent.press(screen.getByText('Back to sign in'));
    expect(navigation.navigate).toHaveBeenCalledWith('SignIn');
  });
});
