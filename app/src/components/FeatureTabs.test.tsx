import { fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import { FeatureTabs } from './FeatureTabs';

describe('FeatureTabs', () => {
  it('shows the live chat internals by default', () => {
    render(<FeatureTabs />);
    expect(screen.getByRole('button', { name: 'Live chat' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Booking' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Analyzer' })).toBeTruthy();
    expect(screen.getByText('Real-time chat with your coach')).toBeTruthy();
    expect(screen.getByText(/dedicated WebSocket/)).toBeTruthy();
  });

  it('switches the panel when a tab is pressed', () => {
    render(<FeatureTabs />);
    fireEvent.press(screen.getByRole('button', { name: 'Booking' }));
    expect(screen.getByText('From coach profile to booked session')).toBeTruthy();
    expect(screen.getByText(/open slots/)).toBeTruthy();
    expect(screen.queryByText('Real-time chat with your coach')).toBeNull();

    fireEvent.press(screen.getByRole('button', { name: 'Analyzer' }));
    expect(screen.getByText('Feedback on your real conversations')).toBeTruthy();
    expect(screen.getByText(/scoring queue/)).toBeTruthy();
  });

  it('marks the selected tab in its accessibility state', () => {
    render(<FeatureTabs />);
    expect(screen.getByRole('button', { name: 'Live chat' }).props.accessibilityState.selected).toBe(true);
    fireEvent.press(screen.getByRole('button', { name: 'Analyzer' }));
    expect(screen.getByRole('button', { name: 'Analyzer' }).props.accessibilityState.selected).toBe(true);
    expect(screen.getByRole('button', { name: 'Live chat' }).props.accessibilityState.selected).toBe(false);
  });
});
