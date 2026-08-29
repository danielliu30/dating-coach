import { NavigationContainer, type LinkingOptions } from '@react-navigation/native';
import { createBottomTabNavigator } from '@react-navigation/bottom-tabs';
import { createNativeStackNavigator } from '@react-navigation/native-stack';
import React, { useEffect, useState } from 'react';
import { Linking, Text, useWindowDimensions } from 'react-native';

import { Loading } from '../components/ui';
import AccountScreen from '../screens/AccountScreen';
import AnalysisHistoryScreen from '../screens/AnalysisHistoryScreen';
import AnalysisResultScreen from '../screens/AnalysisResultScreen';
import ChatScreen from '../screens/ChatScreen';
import CoachDashboardScreen from '../screens/CoachDashboardScreen';
import CoachDetailScreen from '../screens/CoachDetailScreen';
import CoachListScreen from '../screens/CoachListScreen';
import CoachProfileScreen from '../screens/CoachProfileScreen';
import MySessionsScreen from '../screens/MySessionsScreen';
import SignInScreen from '../screens/SignInScreen';
import SignUpScreen from '../screens/SignUpScreen';
import SubmitConversationScreen from '../screens/SubmitConversationScreen';
import ThreadsScreen from '../screens/ThreadsScreen';
import VerifyEmailScreen from '../screens/VerifyEmailScreen';
import { useAuth } from '../state/auth';
import { colors } from '../theme';
import type {
  AnalysisStackParams,
  AuthStackParams,
  ChatsStackParams,
  CoachStackParams,
  CoachesStackParams,
  RootTabParams,
} from './types';

const linking: LinkingOptions<RootTabParams> = {
  prefixes: ['datingcoach://', 'http://localhost:8081'],
  config: {
    screens: {
      Coaches: { screens: { CoachList: 'coaches', CoachDetail: 'coaches/:coachID' } },
      Sessions: 'sessions',
      Chats: { screens: { Threads: 'chats', Chat: 'chats/:threadID' } },
      Analyse: { screens: { Submit: 'analyse', History: 'analyse/history', Result: 'analyse/:analysisID' } },
      Dashboard: 'dashboard',
      Profile: 'profile',
      Account: 'account',
    },
  },
};

const AuthStack = createNativeStackNavigator<AuthStackParams>();
const CoachesStack = createNativeStackNavigator<CoachesStackParams>();
const ChatsStack = createNativeStackNavigator<ChatsStackParams>();
const AnalysisStack = createNativeStackNavigator<AnalysisStackParams>();
const CoachStack = createNativeStackNavigator<CoachStackParams>();
const Tabs = createBottomTabNavigator<RootTabParams>();

const tabIcon = (glyph: string) =>
  function TabIcon({ color }: { color: string }) {
    return <Text style={{ color, fontSize: 18 }}>{glyph}</Text>;
  };

function CoachesNavigator(): React.ReactElement {
  return (
    <CoachesStack.Navigator>
      <CoachesStack.Screen name="CoachList" component={CoachListScreen} options={{ headerShown: false }} />
      <CoachesStack.Screen
        name="CoachDetail"
        component={CoachDetailScreen}
        options={({ route }) => ({ title: route.params.coachName })}
      />
    </CoachesStack.Navigator>
  );
}

function ChatsNavigator(): React.ReactElement {
  return (
    <ChatsStack.Navigator>
      <ChatsStack.Screen name="Threads" component={ThreadsScreen} options={{ headerShown: false }} />
      <ChatsStack.Screen name="Chat" component={ChatScreen} />
    </ChatsStack.Navigator>
  );
}

function AnalysisNavigator(): React.ReactElement {
  return (
    <AnalysisStack.Navigator>
      <AnalysisStack.Screen name="Submit" component={SubmitConversationScreen} options={{ headerShown: false }} />
      <AnalysisStack.Screen name="History" component={AnalysisHistoryScreen} options={{ title: 'Past analyses' }} />
      <AnalysisStack.Screen name="Result" component={AnalysisResultScreen} options={{ title: 'Feedback' }} />
    </AnalysisStack.Navigator>
  );
}

function CoachAreaNavigator(): React.ReactElement {
  return (
    <CoachStack.Navigator>
      <CoachStack.Screen name="Dashboard" component={CoachDashboardScreen} options={{ headerShown: false }} />
      <CoachStack.Screen name="CoachProfile" component={CoachProfileScreen} options={{ title: 'Profile' }} />
    </CoachStack.Navigator>
  );
}

function MainTabs(): React.ReactElement {
  const { user } = useAuth();
  const { width } = useWindowDimensions();
  const isWide = width >= 900;

  return (
    <Tabs.Navigator
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: colors.primary,
        tabBarInactiveTintColor: colors.muted,
        // On a wide web viewport the tab bar reads better as a centred top row.
        tabBarPosition: isWide ? 'top' : 'bottom',
        tabBarStyle: isWide ? { maxWidth: 900, alignSelf: 'center' } : undefined,
      }}
    >
      {user?.role === 'coach' ? (
        <>
          <Tabs.Screen
            name="Dashboard"
            component={CoachAreaNavigator}
            options={{ tabBarIcon: tabIcon('▦'), title: 'Dashboard' }}
          />
          <Tabs.Screen name="Chats" component={ChatsNavigator} options={{ tabBarIcon: tabIcon('◍') }} />
          <Tabs.Screen
            name="Profile"
            component={CoachProfileScreen}
            options={{ tabBarIcon: tabIcon('☰'), title: 'Profile' }}
          />
        </>
      ) : (
        <>
          <Tabs.Screen name="Coaches" component={CoachesNavigator} options={{ tabBarIcon: tabIcon('◆') }} />
          <Tabs.Screen name="Sessions" component={MySessionsScreen} options={{ tabBarIcon: tabIcon('▤') }} />
          <Tabs.Screen name="Chats" component={ChatsNavigator} options={{ tabBarIcon: tabIcon('◍') }} />
          <Tabs.Screen
            name="Analyse"
            component={AnalysisNavigator}
            options={{ tabBarIcon: tabIcon('✦'), title: 'Analyse' }}
          />
        </>
      )}
      <Tabs.Screen name="Account" component={AccountScreen} options={{ tabBarIcon: tabIcon('◉') }} />
    </Tabs.Navigator>
  );
}

function AuthNavigator({ initialRoute }: { initialRoute: keyof AuthStackParams }): React.ReactElement {
  return (
    <AuthStack.Navigator screenOptions={{ headerShown: false }} initialRouteName={initialRoute}>
      <AuthStack.Screen name="SignIn" component={SignInScreen} />
      <AuthStack.Screen name="SignUp" component={SignUpScreen} />
      <AuthStack.Screen name="Verify" component={VerifyEmailScreen} />
    </AuthStack.Navigator>
  );
}

export default function RootNavigator(): React.ReactElement {
  const { ready, token, user } = useAuth();
  const verified = Boolean(user?.email_verified);
  const [linkedVerify, setLinkedVerify] = useState(false);

  // Emails link to <app>/verify?token=..., which has to land on the verification
  // screen even for a visitor without a session.
  useEffect(() => {
    void Linking.getInitialURL().then((url) => {
      if (url && /\/verify\b/.test(url)) setLinkedVerify(true);
    });
  }, []);

  return (
    <NavigationContainer linking={linking}>
      {!ready ? (
        <Loading />
      ) : token && verified ? (
        <MainTabs />
      ) : (
        <AuthNavigator initialRoute={token || linkedVerify ? 'Verify' : 'SignIn'} />
      )}
    </NavigationContainer>
  );
}
