import React from 'react';
import { Text } from 'react-native';

/**
 * Builds a module shape that stands in for a screen: it renders `label:<json params>`
 * so navigation tests can assert which route mounted and with what params.
 */
export function screenStub(label: string): { __esModule: true; default: React.ComponentType<{ route?: { params?: unknown } }> } {
  const Stub = ({ route }: { route?: { params?: unknown } }) =>
    React.createElement(Text, null, `${label}:${JSON.stringify(route?.params ?? null)}`);
  return { __esModule: true, default: Stub };
}
