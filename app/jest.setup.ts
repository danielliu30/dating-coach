import '@testing-library/jest-native/extend-expect';

jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'),
);

jest.mock('react-native-reanimated', () => {
  const Reanimated = require('react-native-reanimated/mock');
  Reanimated.default.call = () => undefined;
  return Reanimated;
});

jest.mock('react-native-keyboard-controller', () => {
  const React = require('react');
  const { ScrollView, View } = require('react-native');
  const passthrough = ({ children }: { children?: React.ReactNode }) => React.createElement(React.Fragment, null, children);
  return {
    KeyboardProvider: passthrough,
    KeyboardAwareScrollView: ScrollView,
    KeyboardAvoidingView: View,
    KeyboardStickyView: View,
    KeyboardController: { dismiss: jest.fn(), setInputMode: jest.fn(), setDefaultMode: jest.fn() },
    useKeyboardHandler: jest.fn(),
    useReanimatedKeyboardAnimation: () => ({ height: { value: 0 }, progress: { value: 0 } }),
    useKeyboardAnimation: () => ({ height: { value: 0 }, progress: { value: 0 } }),
  };
});

jest.mock('expo-font', () => ({
  useFonts: () => [true, null],
  loadAsync: jest.fn(async () => undefined),
  isLoaded: () => true,
}));

jest.mock('expo-linear-gradient', () => {
  const React = require('react');
  const { View } = require('react-native');
  return {
    LinearGradient: ({ children, ...props }: { children?: React.ReactNode }) =>
      React.createElement(View, props, children),
  };
});

jest.mock('@expo/vector-icons', () => {
  const React = require('react');
  const { Text } = require('react-native');
  const Icon = ({ name }: { name: string }) => React.createElement(Text, null, name);
  return { Ionicons: Icon, MaterialIcons: Icon, MaterialCommunityIcons: Icon, FontAwesome: Icon, Feather: Icon };
});

jest.mock('@expo/vector-icons/Ionicons', () => {
  const React = require('react');
  const { Text } = require('react-native');
  return { __esModule: true, default: ({ name }: { name: string }) => React.createElement(Text, null, name) };
});
