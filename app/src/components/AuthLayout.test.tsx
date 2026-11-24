import { fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';
import { Text } from 'react-native';

import { AuthLayout } from './AuthLayout';

jest.mock('expo-status-bar', () => ({ StatusBar: () => null }));

describe('AuthLayout landing nav', () => {
  const renderLanding = () =>
    render(
      <AuthLayout landing>
        <Text>Sign in form</Text>
      </AuthLayout>,
    );

  it('shows the Product page by default and hides the other pages', () => {
    renderLanding();
    expect(screen.getByText('Dating has a feedback problem')).toBeTruthy();
    expect(screen.queryByText('Real-time chat with your coach')).toBeNull();
    expect(screen.queryByText('This is your dating life. We don\'t want to date for you.')).toBeNull();
  });

  it('switches pages from the top nav instead of scrolling', () => {
    renderLanding();
    fireEvent.press(screen.getByRole('button', { name: 'About' }));
    expect(screen.getByText('This is your dating life. We don\'t want to date for you.')).toBeTruthy();
    expect(screen.getByText(/not another dating app/)).toBeTruthy();
    expect(screen.queryByText('Dating has a feedback problem')).toBeNull();

    fireEvent.press(screen.getByRole('button', { name: 'How it works' }));
    expect(screen.getByText('Real-time chat with your coach')).toBeTruthy();
    expect(screen.getByText(/Patterns across months do/)).toBeTruthy();
    expect(screen.queryByText('This is your dating life. We don\'t want to date for you.')).toBeNull();

    fireEvent.press(screen.getByRole('button', { name: 'Product' }));
    expect(screen.getByText('Dating has a feedback problem')).toBeTruthy();
  });

  it('marks the current page link as selected', () => {
    renderLanding();
    expect(screen.getByRole('button', { name: 'Product' }).props.accessibilityState.selected).toBe(true);
    fireEvent.press(screen.getByRole('button', { name: 'About' }));
    expect(screen.getByRole('button', { name: 'About' }).props.accessibilityState.selected).toBe(true);
    expect(screen.getByRole('button', { name: 'Product' }).props.accessibilityState.selected).toBe(false);
  });
});
