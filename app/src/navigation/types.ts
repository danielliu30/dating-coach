import type { NavigatorScreenParams } from '@react-navigation/native';

export type AuthStackParams = {
  SignIn: undefined;
  SignUp: undefined;
  Verify: { email?: string } | undefined;
};

export type CoachesStackParams = {
  CoachList: undefined;
  CoachDetail: { coachID: string; coachName: string };
};

export type ChatsStackParams = {
  Threads: undefined;
  Chat: { threadID: string; title: string };
};

export type AnalysisStackParams = {
  Submit: undefined;
  History: undefined;
  Result: { analysisID: string; conversationID: string };
};

export type CoachStackParams = {
  Dashboard: undefined;
  CoachProfile: undefined;
};

/** Tabs are role dependent: clients see Coaches/Sessions/Chats/Analyse, coaches see Dashboard/Chats/Profile. */
export type RootTabParams = {
  Coaches: NavigatorScreenParams<CoachesStackParams>;
  Sessions: undefined;
  Chats: NavigatorScreenParams<ChatsStackParams>;
  Analyse: NavigatorScreenParams<AnalysisStackParams>;
  Dashboard: NavigatorScreenParams<CoachStackParams>;
  Profile: undefined;
  Account: undefined;
};
