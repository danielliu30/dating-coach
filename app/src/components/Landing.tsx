import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import React from 'react';
import { Image, StyleSheet, Text, useWindowDimensions, View } from 'react-native';

import { colors, elevation, fonts, gradients, radii, type } from '../theme';
import { IconDisc, type Hue } from './kit';
import type { IconName } from './ui';

const IMAGES = {
  forestWalk: 'https://images.unsplash.com/photo-1474552226712-ac0f0961a954?auto=format&fit=crop&w=1400&q=80',
  coffeeTalk: 'https://images.unsplash.com/photo-1511988617509-a57c8a288659?auto=format&fit=crop&w=1400&q=80',
  meadow: 'https://images.unsplash.com/photo-1470770841072-f978cf4d019e?auto=format&fit=crop&w=1400&q=80',
};

const JOURNEY: { icon: IconName; label: string }[] = [
  { icon: 'compass-outline', label: 'Understand what you want' },
  { icon: 'people-outline', label: 'Meet people' },
  { icon: 'hand-right-outline', label: 'Choose who to pursue' },
  { icon: 'chatbubbles-outline', label: 'Connect' },
  { icon: 'cafe-outline', label: 'Date' },
  { icon: 'journal-outline', label: 'Reflect' },
  { icon: 'bulb-outline', label: 'Learn' },
  { icon: 'repeat-outline', label: 'Choose again' },
];

const AGENCY_QUESTIONS = ['Who do I actually want?', 'Did I actually like this person?', 'Am I choosing people because I want them, or because they chose me?'];

const CONTROL: { icon: IconName; text: string }[] = [
  { icon: 'hand-right-outline', text: 'Who you pursue' },
  { icon: 'sunny-outline', text: 'How you show up' },
  { icon: 'chatbox-ellipses-outline', text: 'What you communicate' },
  { icon: 'shield-checkmark-outline', text: 'What you accept' },
  { icon: 'bulb-outline', text: 'What you learn from what happens' },
];

const FEATURES: { icon: IconName; hue: Hue; title: string; text: string }[] = [
  {
    icon: 'image-outline',
    hue: 'sun',
    title: 'Profile perspective',
    text: 'Not getting matches? Get another honest set of eyes on your photos and prompts.',
  },
  {
    icon: 'pulse-outline',
    hue: 'rose',
    title: 'Conversation patterns',
    text: 'If chats keep fading, see how they develop and where the energy shifts.',
  },
  {
    icon: 'leaf-outline',
    hue: 'sage',
    title: 'Date preparation',
    text: 'Walk in calm. Prepare for the date instead of rehearsing worries.',
  },
  {
    icon: 'journal-outline',
    hue: 'sky',
    title: 'Afterwards, reflect',
    text: "Not just \"did they like me?\" — also \"did I like them?\" Make sense of what happened, and of how you felt.",
  },
];

const PRINCIPLES: { icon: IconName; text: string }[] = [
  { icon: 'chatbox-ellipses-outline', text: 'Your words should still be your words.' },
  { icon: 'compass-outline', text: 'Your decisions should still be your decisions.' },
  { icon: 'heart-outline', text: 'Your dating life should still be yours.' },
];

const PATTERNS = [
  'Lots of matches, very few dates.',
  'Great first dates that end after the second.',
  'Investing more energy than the person on the other side.',
  'Dating almost entirely from the people who pursue you.',
  'Saying you want something serious, but choosing people who don\'t.',
];

/**
 * Eyebrow + display headline + optional serif accent that opens each landing
 * section. `light` switches to white text for dark/gradient backgrounds.
 */
function SectionTitle({
  eyebrow,
  title,
  accent,
  light,
  center,
}: {
  eyebrow: string;
  title: string;
  accent?: string;
  light?: boolean;
  center?: boolean;
}): React.ReactElement {
  const align = center ? ('center' as const) : ('left' as const);
  return (
    <View style={{ gap: 10, alignItems: center ? 'center' : 'flex-start' }}>
      <Text style={[type.eyebrow, light && { color: 'rgba(255,255,255,0.8)' }, { textAlign: align }]}>{eyebrow}</Text>
      <Text style={[styles.sectionTitle, light && { color: colors.primaryText }, { textAlign: align }]}>{title}</Text>
      {accent ? (
        <Text style={[type.accent, light && { color: colors.sunTint }, { textAlign: align, maxWidth: 560 }]}>
          {accent}
        </Text>
      ) : null}
    </View>
  );
}

/**
 * Scrolling "Product" and "About" story for the signed-out landing, drawn from
 * the Dating Humane manifesto. Renders inside the auth ScrollView; the caller
 * attaches `productRef`/`aboutRef` to enable anchor scrolling from the nav.
 */
export function Landing({
  productRef,
  aboutRef,
}: {
  productRef: React.RefObject<View | null>;
  aboutRef: React.RefObject<View | null>;
}): React.ReactElement {
  const { width } = useWindowDimensions();
  const twoUp = width >= 760;

  return (
    <View style={styles.root}>
      {/* Product */}
      <View ref={productRef} collapsable={false} style={styles.section}>
        <SectionTitle
          eyebrow="Dating has a feedback problem"
          title="You try something. You see what happens. Then you're left guessing."
          accent="If a conversation dies, nobody tells you what changed. If you're ghosted, there's no explanation at all."
        />
        <View style={[styles.split, twoUp && styles.splitRow]}>
          <View style={[styles.imageCard, twoUp && { flex: 1 }]}>
            <Image source={{ uri: IMAGES.coffeeTalk }} style={styles.image} resizeMode="cover" />
            <LinearGradient
              colors={['rgba(58,44,36,0)', 'rgba(58,44,36,0.7)']}
              style={styles.fill}
            />
            <Text style={styles.imageCaption}>Real talk, real perspective.</Text>
          </View>
          <View style={[styles.quoteCard, twoUp && { flex: 1 }]}>
            <Ionicons name="chatbubble-ellipses" size={22} color={colors.primary} />
            <Text style={styles.quote}>
              People already send screenshots to friends, ask someone to check their Hinge profile and paste
              conversations into a chatbot.
            </Text>
            <Text style={styles.quoteAccent}>People are already looking for dating coaches. We just don't call it that.</Text>
          </View>
        </View>
      </View>

      {/* Agency */}
      <View style={[styles.section, styles.sectionDark]}>
        <LinearGradient colors={gradients.meadow} start={{ x: 0, y: 0 }} end={{ x: 1, y: 1 }} style={styles.fill} />
        <View style={styles.darkOrb} />
        <SectionTitle
          light
          eyebrow="Dating has an agency problem too"
          title="Be open to what comes your way. But be the driver."
          accent="We swipe through whoever an algorithm shows us and date whoever approaches us. Somewhere in there, it's easy to forget to ask:"
        />
        <View style={[styles.split, twoUp && styles.splitRow, { alignItems: 'stretch' }]}>
          <View style={[styles.questionCard, twoUp && { flex: 1 }]}>
            {AGENCY_QUESTIONS.map((q) => (
              <Text key={q} style={styles.question}>
                {q}
              </Text>
            ))}
          </View>
          <View style={[{ gap: 12 }, twoUp && { flex: 1 }]}>
            <Text style={[type.eyebrow, { color: 'rgba(255,255,255,0.8)' }]}>Agency isn't control</Text>
            <Text style={[type.body, { color: 'rgba(255,255,255,0.9)' }]}>
              You can't control whether someone texts back or wants a second date. You can control:
            </Text>
            {CONTROL.map((c) => (
              <View key={c.text} style={styles.controlRow}>
                <Ionicons name={c.icon} size={18} color={colors.sunTint} />
                <Text style={styles.controlText}>{c.text}</Text>
              </View>
            ))}
          </View>
        </View>
      </View>

      {/* Journey */}
      <View style={[styles.section, styles.sectionSunken]}>
        <SectionTitle
          eyebrow="The problem is bigger than messaging"
          title="Under the app journey there's another loop — and at every step, a place to get stuck."
          accent="Having options isn't the same as exercising agency over them. A big pool is still a pool somebody else selected."
        />
        <View style={styles.journey}>
          {JOURNEY.map((step, i) => (
            <View key={step.label} style={styles.journeyStep}>
              <View style={styles.journeyDisc}>
                <Ionicons name={step.icon} size={20} color={colors.sageDeep} />
              </View>
              <Text style={styles.journeyLabel}>{step.label}</Text>
              {i < JOURNEY.length - 1 ? <View style={styles.journeyTick} /> : null}
            </View>
          ))}
        </View>
      </View>

      {/* Features */}
      <View style={styles.section}>
        <SectionTitle
          eyebrow="Your journey, with company"
          title="You drive. We help at every step — software when it's useful, a person when it matters."
        />
        <View style={styles.grid}>
          {FEATURES.map((f) => (
            <View key={f.title} style={[styles.feature, twoUp && styles.featureHalf]}>
              <IconDisc icon={f.icon} hue={f.hue} size={48} />
              <Text style={type.heading}>{f.title}</Text>
              <Text style={[type.body, { color: colors.muted }]}>{f.text}</Text>
            </View>
          ))}
        </View>
        <View style={styles.coachCard}>
          <LinearGradient colors={gradients.meadow} start={{ x: 0, y: 0 }} end={{ x: 1, y: 1 }} style={styles.fill} />
          <Image source={{ uri: IMAGES.forestWalk }} style={[styles.fill, { opacity: 0.35 }]} resizeMode="cover" />
          <View style={{ gap: 10, maxWidth: 520 }}>
            <Text style={[type.eyebrow, { color: 'rgba(255,255,255,0.85)' }]}>And sometimes you want a person</Text>
            <Text style={[styles.sectionTitle, { color: colors.primaryText, fontSize: 28, lineHeight: 34 }]}>
              Talk to a live coach who understands dating and can actually listen.
            </Text>
            <Text style={[type.body, { color: 'rgba(255,255,255,0.92)' }]}>
              Nervous before a date. Ten first dates that went nowhere. Or nothing is technically wrong, but you keep
              ending up in situations you didn't actually want. You should be able to talk it through with a real
              person — and choose whichever kind of help makes sense for you.
            </Text>
          </View>
        </View>
      </View>

      {/* About */}
      <View ref={aboutRef} collapsable={false} style={[styles.section, styles.sectionDark]}>
        <LinearGradient colors={gradients.dusk} start={{ x: 0, y: 0 }} end={{ x: 1, y: 1 }} style={styles.fill} />
        <View style={styles.darkOrb} />
        <SectionTitle
          light
          eyebrow="About us"
          title="This is your dating life. We don't want to date for you."
          accent="At some point they aren't talking to you anymore — they're talking to an algorithm pretending to be you."
        />
        <View style={[styles.split, twoUp && styles.splitRow]}>
          {PRINCIPLES.map((p) => (
            <View key={p.text} style={[styles.principle, twoUp && { flex: 1 }]}>
              <Ionicons name={p.icon} size={22} color={colors.sunTint} />
              <Text style={styles.principleText}>{p.text}</Text>
            </View>
          ))}
        </View>
        <Text style={[type.body, { color: 'rgba(255,255,255,0.85)', maxWidth: 640 }]}>
          A coach doesn't play the game for you. They help you understand yourself well enough to make better
          decisions on your own — and the wins stay yours.
        </Text>
      </View>

      {/* Patterns */}
      <View style={styles.section}>
        <View style={[styles.split, twoUp && styles.splitRow, { alignItems: 'stretch' }]}>
          <View style={[{ gap: 18 }, twoUp && { flex: 1.2 }]}>
            <SectionTitle
              eyebrow="Over time, the feedback gets better"
              title="One bad date doesn't tell you much. Patterns across months do — outside and inside."
            />
            <View style={{ gap: 10 }}>
              {PATTERNS.map((p) => (
                <View key={p} style={styles.patternRow}>
                  <View style={styles.patternDot}>
                    <Ionicons name="checkmark" size={14} color={colors.primaryText} />
                  </View>
                  <Text style={[type.body, { flex: 1 }]}>{p}</Text>
                </View>
              ))}
            </View>
            <View style={styles.noteCard}>
              <Text style={[type.caption, { fontFamily: fonts.sansBold, color: colors.bark }]}>
                Not "here's why they rejected you." We can't know that.
              </Text>
              <Text style={[type.accent, { fontSize: 18, lineHeight: 24 }]}>
                "Here's a pattern we've noticed. Is this worth thinking about?"
              </Text>
              <Text style={[type.caption, { color: colors.muted }]}>
                And never "here's who you should date." That's your decision. Self-awareness is what creates agency.
              </Text>
            </View>
          </View>
          <View style={[styles.imageCard, twoUp && { flex: 1, minHeight: 360 }]}>
            <Image source={{ uri: IMAGES.meadow }} style={styles.image} resizeMode="cover" />
            <LinearGradient colors={['rgba(58,44,36,0)', 'rgba(58,44,36,0.65)']} style={styles.fill} />
            <Text style={styles.imageCaption}>Advice that grows from your actual experiences.</Text>
          </View>
        </View>
      </View>

      {/* Closing */}
      <View style={[styles.section, styles.sectionSunken, { alignItems: 'center' }]}>
        <SectionTitle
          center
          eyebrow="What we're really building"
          title="We're not another dating app. We help you understand what you want, go after it, and learn from what happens."
          accent="More confident. More self-aware. More intentional about who you pursue. More willing to go after what you want."
        />
        <View style={styles.closingRow}>
          {(['Profile', 'Conversations', 'Preparation', 'A real person', 'Reflection', 'Your call'] as const).map((t) => (
            <View key={t} style={styles.closingChip}>
              <Text style={styles.closingChipText}>{t}</Text>
            </View>
          ))}
        </View>
        <Text style={[type.body, { color: colors.muted, textAlign: 'center', maxWidth: 520 }]}>
          Dating apps can introduce you to someone. A coach can give you perspective. Technology can show you
          patterns. But you should be the person driving your dating life — don't let what comes your way define
          what you're allowed to want.
        </Text>
      </View>

      <View style={styles.footer}>
        <View style={styles.footerBrand}>
          <Ionicons name="leaf" size={16} color={colors.sageDeep} />
          <Text style={styles.footerWordmark}>Dating Humane</Text>
        </View>
        <Text style={type.caption}>Helping people become better, more intentional daters.</Text>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  root: { width: '100%', maxWidth: 1040, alignSelf: 'center', gap: 20, paddingBottom: 24 },
  fill: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 },
  section: {
    gap: 24,
    padding: 24,
    borderRadius: radii.xl,
    backgroundColor: colors.surface,
    overflow: 'hidden',
    ...elevation.mid,
  },
  sectionSunken: { backgroundColor: colors.surfaceAlt },
  sectionDark: { backgroundColor: colors.moss },
  questionCard: {
    gap: 14,
    padding: 22,
    borderRadius: radii.lg,
    backgroundColor: 'rgba(255,255,255,0.12)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.22)',
  },
  question: { ...type.accent, color: colors.primaryText, fontSize: 22, lineHeight: 30 },
  controlRow: { flexDirection: 'row', alignItems: 'center', gap: 10 },
  controlText: { fontFamily: fonts.sansSemi, fontSize: 15, color: colors.primaryText },
  darkOrb: {
    pointerEvents: 'none',
    position: 'absolute',
    right: -80,
    top: -100,
    width: 320,
    height: 320,
    borderRadius: 160,
    backgroundColor: 'rgba(255,255,255,0.08)',
  },
  sectionTitle: {
    fontFamily: fonts.sansBlack,
    fontSize: 34,
    lineHeight: 40,
    letterSpacing: -1,
    color: colors.text,
    maxWidth: 720,
  },
  split: { gap: 16 },
  splitRow: { flexDirection: 'row', alignItems: 'flex-start' },
  imageCard: {
    minHeight: 260,
    borderRadius: radii.lg,
    overflow: 'hidden',
    justifyContent: 'flex-end',
    padding: 18,
    backgroundColor: colors.sand,
  },
  image: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0, width: '100%', height: '100%' },
  imageCaption: { fontFamily: fonts.serifItalicBold, fontSize: 20, color: colors.primaryText },
  quoteCard: {
    gap: 14,
    padding: 22,
    borderRadius: radii.lg,
    backgroundColor: colors.primaryTint,
    justifyContent: 'center',
    minHeight: 260,
  },
  quote: { ...type.body, fontSize: 16, lineHeight: 24 },
  quoteAccent: { ...type.accent, fontSize: 18, lineHeight: 24 },
  journey: { flexDirection: 'row', flexWrap: 'wrap', gap: 12 },
  journeyStep: { width: 120, alignItems: 'center', gap: 8 },
  journeyDisc: {
    width: 48,
    height: 48,
    borderRadius: 24,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.sageTint,
    ...elevation.low,
  },
  journeyLabel: { ...type.caption, fontFamily: fonts.sansSemi, color: colors.text, textAlign: 'center' },
  journeyTick: {
    position: 'absolute',
    top: 22,
    right: -8,
    width: 6,
    height: 6,
    borderRadius: 3,
    backgroundColor: colors.sand,
  },
  grid: { flexDirection: 'row', flexWrap: 'wrap', gap: 14 },
  feature: {
    width: '100%',
    gap: 12,
    padding: 20,
    borderRadius: radii.lg,
    backgroundColor: colors.surfaceAlt,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: colors.border,
  },
  featureHalf: { width: '48.5%', flexGrow: 1 },
  coachCard: {
    borderRadius: radii.lg,
    padding: 26,
    overflow: 'hidden',
    minHeight: 220,
    justifyContent: 'center',
  },
  principle: {
    gap: 10,
    padding: 18,
    borderRadius: radii.lg,
    backgroundColor: 'rgba(255,255,255,0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.18)',
  },
  principleText: { ...type.subheading, color: colors.primaryText, fontSize: 16, lineHeight: 22 },
  patternRow: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  patternDot: {
    width: 26,
    height: 26,
    borderRadius: 13,
    backgroundColor: colors.sage,
    alignItems: 'center',
    justifyContent: 'center',
  },
  noteCard: {
    gap: 8,
    padding: 18,
    borderRadius: radii.lg,
    backgroundColor: colors.sunTint,
  },
  closingRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 8, justifyContent: 'center' },
  closingChip: {
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderRadius: radii.pill,
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.sand,
  },
  closingChipText: { fontFamily: fonts.sansSemi, fontSize: 13, color: colors.bark },
  footer: { alignItems: 'center', gap: 6, paddingVertical: 12 },
  footerBrand: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  footerWordmark: { fontFamily: fonts.sansBlack, fontSize: 16, color: colors.text, letterSpacing: -0.3 },
});
