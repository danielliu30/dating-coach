import { Ionicons } from '@expo/vector-icons';
import { createBottomTabNavigator } from '@react-navigation/bottom-tabs';
import { DefaultTheme, NavigationContainer, type LinkingOptions, type Theme } from '@react-navigation/native';
import { createNativeStackNavigator, type NativeStackNavigationOptions } from '@react-navigation/native-stack';
import React from 'react';
import { useWindowDimensions } from 'react-native';

import { Loading, type IconName } from '../components/ui';
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
import { colors, fonts } from '../theme';
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

const navTheme: Theme = {
  ...DefaultTheme,
  colors: {
    ...DefaultTheme.colors,
    primary: colors.primary,
    background: colors.bg,
    card: colors.surface,
    text: colors.text,
    border: colors.border,
    notification: colors.primary,
  },
  fonts: {
    regular: { fontFamily: fonts.sans, fontWeight: '400' },
    medium: { fontFamily: fonts.sansSemi, fontWeight: '500' },
    bold: { fontFamily: fonts.sansBold, fontWeight: '700' },
    heavy: { fontFamily: fonts.sansBlack, fontWeight: '800' },
  },
};

const stackOptions: NativeStackNavigationOptions = {
  headerShadowVisible: false,
  headerStyle: { backgroundColor: colors.bg },
  headerTintColor: colors.primaryDeep,
  headerTitleStyle: { fontFamily: fonts.sansBold, color: colors.text },
  headerBackTitleStyle: { fontFamily: fonts.sansSemi },
  contentStyle: { backgroundColor: colors.bg },
};

const tabIcon = (outline: IconName, filled: IconName) =>
  function TabIcon({ color, focused }: { color: string; focused: boolean }) {
    return <Ionicons name={focused ? filled : outline} size={22} color={color} />;
  };

function CoachesNavigator(): React.ReactElement {
  return (
    <CoachesStack.Navigator screenOptions={stackOptions}>
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
    <ChatsStack.Navigator screenOptions={stackOptions}>
      <ChatsStack.Screen name="Threads" component={ThreadsScreen} options={{ headerShown: false }} />
      <ChatsStack.Screen name="Chat" component={ChatScreen} />
    </ChatsStack.Navigator>
  );
}

function AnalysisNavigator(): React.ReactElement {
  return (
    <AnalysisStack.Navigator screenOptions={stackOptions}>
      <AnalysisStack.Screen name="Submit" component={SubmitConversationScreen} options={{ headerShown: false }} />
      <AnalysisStack.Screen name="History" component={AnalysisHistoryScreen} options={{ title: 'Past analyses' }} />
      <AnalysisStack.Screen name="Result" component={AnalysisResultScreen} options={{ title: 'Feedback' }} />
    </AnalysisStack.Navigator>
  );
}

function CoachAreaNavigator(): React.ReactElement {
  return (
    <CoachStack.Navigator screenOptions={stackOptions}>
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
        tabBarActiveTintColor: colors.primaryDeep,
        tabBarInactiveTintColor: colors.muted,
        tabBarLabelStyle: { fontFamily: fonts.sansBold, fontSize: 11 },
        // On a wide web viewport the tab bar reads better as a centred top row.
        tabBarPosition: isWide ? 'top' : 'bottom',
        tabBarStyle: isWide
          ? {
              width: '100%',
              maxWidth: 900,
              alignSelf: 'center',
              backgroundColor: colors.surface,
              borderRadius: 999,
              marginTop: 12,
              marginBottom: 4,
              borderTopWidth: 0,
              borderBottomWidth: 0,
              paddingHorizontal: 8,
            }
          : {
              backgroundColor: colors.surface,
              borderTopColor: colors.border,
              height: 64,
              paddingTop: 6,
              paddingBottom: 8,
            },
      }}
    >
      {user?.role === 'coach' ? (
        <>
          <Tabs.Screen
            name="Dashboard"
            component={CoachAreaNavigator}
            options={{ tabBarIcon: tabIcon('grid-outline', 'grid'), title: 'Dashboard' }}
          />
          <Tabs.Screen
            name="Chats"
            component={ChatsNavigator}
            options={{ tabBarIcon: tabIcon('chatbubbles-outline', 'chatbubbles') }}
          />
          <Tabs.Screen
            name="Profile"
            component={CoachProfileScreen}
            options={{ tabBarIcon: tabIcon('id-card-outline', 'id-card'), title: 'Profile' }}
          />
        </>
      ) : (
        <>
          <Tabs.Screen
            name="Coaches"
            component={CoachesNavigator}
            options={{ tabBarIcon: tabIcon('people-outline', 'people') }}
          />
          <Tabs.Screen
            name="Sessions"
            component={MySessionsScreen}
            options={{ tabBarIcon: tabIcon('calendar-outline', 'calendar') }}
          />
          <Tabs.Screen
            name="Chats"
            component={ChatsNavigator}
            options={{ tabBarIcon: tabIcon('chatbubbles-outline', 'chatbubbles') }}
          />
          <Tabs.Screen
            name="Analyse"
            component={AnalysisNavigator}
            options={{ tabBarIcon: tabIcon('sparkles-outline', 'sparkles'), title: 'Analyse' }}
          />
        </>
      )}
      <Tabs.Screen
        name="Account"
        component={AccountScreen}
        options={{ tabBarIcon: tabIcon('person-circle-outline', 'person-circle') }}
      />
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

  return (
    <NavigationContainer linking={linking} theme={navTheme}>
      {!ready ? (
        <Loading />
      ) : token && verified ? (
        <MainTabs />
      ) : (
        <AuthNavigator initialRoute={token ? 'Verify' : 'SignIn'} />
      )}
    </NavigationContainer>
  );
}
