import type { BottomTabBarProps } from '@react-navigation/bottom-tabs';
import { fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';
import { Text } from 'react-native';

import { CollapsibleSidebar } from './SidebarTabBar';

jest.mock('@react-navigation/native', () => {
  const actual = jest.requireActual('@react-navigation/native');
  return {
    ...actual,
    useLinkBuilder: () => ({ buildHref: () => undefined }),
    useTheme: () => actual.DefaultTheme,
  };
});

const ROUTES = ['Coaches', 'Sessions', 'Chats', 'Analyse', 'Account'];

function makeProps(index = 0): BottomTabBarProps {
  const emit = jest.fn(() => ({ defaultPrevented: false }));
  const dispatch = jest.fn();
  return {
    state: {
      key: 'tab-state',
      index,
      routeNames: ROUTES,
      routes: ROUTES.map((name, i) => ({ key: `route-${i}`, name })),
      history: [],
      type: 'tab',
      stale: false,
    },
    descriptors: Object.fromEntries(
      ROUTES.map((name, i) => [
        `route-${i}`,
        {
          options: {
            title: name,
            tabBarIcon: ({ color }: { color: string }) => <Text style={{ color }}>icon-{name}</Text>,
          },
          navigation: {},
          render: () => null,
        },
      ]),
    ),
    navigation: { emit, dispatch },
    insets: { top: 0, left: 0, right: 0, bottom: 0 },
  } as unknown as BottomTabBarProps;
}

describe('CollapsibleSidebar', () => {
  it('renders the brand and one tab per route', () => {
    render(<CollapsibleSidebar {...makeProps()} />);
    expect(screen.getByText('Dating Humane')).toBeTruthy();
    for (const name of ROUTES) {
      expect(screen.getByRole('tab', { name })).toBeTruthy();
    }
  });

  it('navigates to an unfocused route on press', () => {
    const props = makeProps();
    render(<CollapsibleSidebar {...props} />);
    fireEvent.press(screen.getByRole('tab', { name: 'Chats' }));
    expect(props.navigation.emit).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'tabPress', target: 'route-2' }),
    );
    expect(props.navigation.dispatch).toHaveBeenCalledWith(
      expect.objectContaining({ target: 'tab-state', payload: expect.objectContaining({ name: 'Chats' }) }),
    );
  });

  it('emits tabPress but does not navigate for the focused route', () => {
    const props = makeProps();
    render(<CollapsibleSidebar {...props} />);
    fireEvent.press(screen.getByRole('tab', { name: 'Coaches' }));
    expect(props.navigation.emit).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'tabPress', target: 'route-0' }),
    );
    expect(props.navigation.dispatch).not.toHaveBeenCalled();
  });

  it('honours a prevented tabPress', () => {
    const props = makeProps();
    jest.mocked(props.navigation.emit).mockReturnValue({ defaultPrevented: true } as never);
    render(<CollapsibleSidebar {...props} />);
    fireEvent.press(screen.getByRole('tab', { name: 'Sessions' }));
    expect(props.navigation.dispatch).not.toHaveBeenCalled();
  });

  it('collapses to icons and back while tabs stay reachable by name', () => {
    render(<CollapsibleSidebar {...makeProps()} />);
    fireEvent.press(screen.getByRole('button', { name: 'Collapse sidebar' }));

    expect(screen.queryByText('Coaches')).toBeNull();
    expect(screen.queryByText('Dating Humane')).toBeNull();
    expect(screen.getByRole('tab', { name: 'Coaches' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Account' })).toBeTruthy();

    fireEvent.press(screen.getByRole('button', { name: 'Expand sidebar' }));
    expect(screen.getByText('Coaches')).toBeTruthy();
    expect(screen.getByText('Dating Humane')).toBeTruthy();
  });
});
