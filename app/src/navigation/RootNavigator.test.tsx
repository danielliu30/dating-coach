import { render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';
import { Linking } from 'react-native';

import type { Profile } from '../api/types';
import RootNavigator from './RootNavigator';

// Every screen is replaced with a stub that prints its name and params so routing can be observed.
jest.mock('../screens/AccountScreen', () => require('./screenStub.testutil').screenStub('Account'));
jest.mock('../screens/AnalysisHistoryScreen', () => require('./screenStub.testutil').screenStub('History'));
jest.mock('../screens/AnalysisResultScreen', () => require('./screenStub.testutil').screenStub('Result'));
jest.mock('../screens/ChatScreen', () => require('./screenStub.testutil').screenStub('Chat'));
jest.mock('../screens/CoachDashboardScreen', () => require('./screenStub.testutil').screenStub('Dashboard'));
jest.mock('../screens/CoachDetailScreen', () => require('./screenStub.testutil').screenStub('CoachDetail'));
jest.mock('../screens/CoachListScreen', () => require('./screenStub.testutil').screenStub('CoachList'));
jest.mock('../screens/CoachProfileScreen', () => require('./screenStub.testutil').screenStub('CoachProfile'));
jest.mock('../screens/MySessionsScreen', () => require('./screenStub.testutil').screenStub('Sessions'));
jest.mock('../screens/SignInScreen', () => require('./screenStub.testutil').screenStub('SignIn'));
jest.mock('../screens/SignUpScreen', () => require('./screenStub.testutil').screenStub('SignUp'));
jest.mock('../screens/SubmitConversationScreen', () => require('./screenStub.testutil').screenStub('Submit'));
jest.mock('../screens/ThreadsScreen', () => require('./screenStub.testutil').screenStub('Threads'));
jest.mock('../screens/VerifyEmailScreen', () => require('./screenStub.testutil').screenStub('Verify'));

jest.mock('../components/ui', () => {
  const React = require('react');
  const { Text } = require('react-native');
  return { Loading: () => React.createElement(Text, null, 'Loading') };
});

const mockAuth = {
  ready: true,
  token: null as string | null,
  user: null as Pick<Profile, 'role' | 'email_verified'> | null,
};

jest.mock('../state/auth', () => ({ useAuth: () => mockAuth }));

const client = { role: 'user' as const, email_verified: true };
const coach = { role: 'coach' as const, email_verified: true };

/** Fetches the two bottom-tab bar labels currently rendered (each tab label appears once in the bar). */
const tabLabels = () =>
  screen.getAllByRole('button').flatMap((node) => {
    const label = node.props.accessibilityLabel as string | undefined;
    return label && label.includes(', tab') ? [label.split(',')[0]] : [];
  });

beforeEach(() => {
  mockAuth.ready = true;
  mockAuth.token = null;
  mockAuth.user = null;
  jest.spyOn(Linking, 'getInitialURL').mockResolvedValue(null);
});

afterEach(() => {
  jest.restoreAllMocks();
});

describe('RootNavigator gating', () => {
  it('shows Loading until the session restore finishes', async () => {
    mockAuth.ready = false;
    render(<RootNavigator />);
    await screen.findByText('Loading');
    expect(screen.queryByText(/^SignIn:/)).toBeNull();
  });

  it('shows the auth stack at SignIn when signed out', async () => {
    render(<RootNavigator />);
    await screen.findByText(/^SignIn:/);
    expect(screen.queryByText(/^Verify:/)).toBeNull();
  });

  it('starts the auth stack at Verify when a token exists but the email is unverified', async () => {
    mockAuth.token = 'tok';
    mockAuth.user = { role: 'user', email_verified: false };
    render(<RootNavigator />);
    await screen.findByText(/^Verify:/);
    expect(screen.queryByText(/^SignIn:/)).toBeNull();
  });

  it('keeps a verified user without a token out of the tabs', async () => {
    mockAuth.user = client;
    render(<RootNavigator />);
    await screen.findByText(/^SignIn:/);
  });
});

describe('MainTabs by role', () => {
  it('renders client tabs plus Account for a dater', async () => {
    mockAuth.token = 'tok';
    mockAuth.user = client;
    render(<RootNavigator />);
    await screen.findByText(/^CoachList:/);
    expect(tabLabels()).toEqual(['Coaches', 'Sessions', 'Chats', 'Analyse', 'Account']);
  });

  it('renders coach tabs plus Account for a coach', async () => {
    mockAuth.token = 'tok';
    mockAuth.user = coach;
    render(<RootNavigator />);
    await screen.findByText(/^Dashboard:/);
    expect(tabLabels()).toEqual(['Dashboard', 'Chats', 'Profile', 'Account']);
  });
});

describe('deep links', () => {
  const open = async (url: string, user: Pick<Profile, 'role' | 'email_verified'> = client) => {
    mockAuth.token = 'tok';
    mockAuth.user = user;
    jest.spyOn(Linking, 'getInitialURL').mockResolvedValue(url);
    render(<RootNavigator />);
  };

  it('coaches/:coachID opens CoachDetail with the id', async () => {
    await open('datingcoach://coaches/c-42');
    await waitFor(() => expect(screen.getByText(/^CoachDetail:/)).toBeTruthy());
    expect(screen.getByText(/^CoachDetail:/).props.children).toContain('"coachID":"c-42"');
  });

  it('sessions opens the Sessions tab', async () => {
    await open('datingcoach://sessions');
    await screen.findByText(/^Sessions:/);
  });

  it('chats/:threadID opens Chat inside the Chats tab', async () => {
    await open('http://localhost:8081/chats/t-7');
    const chat = await screen.findByText(/^Chat:/);
    expect(chat.props.children).toContain('"threadID":"t-7"');
  });

  it('analyse/history opens History', async () => {
    await open('datingcoach://analyse/history');
    await screen.findByText(/^History:/);
  });

  it('analyse/:analysisID opens Result with the id', async () => {
    await open('datingcoach://analyse/an-1');
    const result = await screen.findByText(/^Result:/);
    expect(result.props.children).toContain('"analysisID":"an-1"');
  });

  it('account opens the Account tab for either role', async () => {
    await open('datingcoach://account', coach);
    await screen.findByText(/^Account:/);
  });

  it('dashboard and profile open the coach tabs', async () => {
    await open('datingcoach://profile', coach);
    await screen.findByText(/^CoachProfile:/);
  });
});
