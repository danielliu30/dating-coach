import { Ionicons } from '@expo/vector-icons';
import { BottomTabBar, type BottomTabBarProps } from '@react-navigation/bottom-tabs';
import { CommonActions } from '@react-navigation/native';
import React, { useState } from 'react';
import { Pressable, StyleSheet, Text, useWindowDimensions, View } from 'react-native';

import { colors, elevation, fonts, radii } from '../theme';

/** Below this viewport width the default bottom tab bar is kept. */
const WIDE_BREAKPOINT = 900;
const EXPANDED_WIDTH = 236;
const COLLAPSED_WIDTH = 74;

/**
 * Wide viewports get a collapsible left sidebar — the app-shell navigation
 * pattern — while narrow viewports keep the stock bottom tab bar. Tab items
 * keep `role="tab"` and their plain label as the accessible name so the
 * signed-in detection and `openTab` helpers in e2e keep working, collapsed
 * or not.
 */
export function SidebarTabBar(props: BottomTabBarProps): React.ReactElement {
  const { width } = useWindowDimensions();
  return width >= WIDE_BREAKPOINT ? <CollapsibleSidebar {...props} /> : <BottomTabBar {...props} />;
}

export function CollapsibleSidebar({ state, descriptors, navigation, insets }: BottomTabBarProps): React.ReactElement {
  const [collapsed, setCollapsed] = useState(false);

  return (
    <View
      role="navigation"
      style={[
        styles.bar,
        { width: collapsed ? COLLAPSED_WIDTH : EXPANDED_WIDTH, paddingTop: insets.top + 12, paddingBottom: insets.bottom + 12 },
      ]}
    >
      <View style={[styles.brand, collapsed && styles.brandCollapsed]}>
        <View style={styles.brandMark}>
          <Ionicons name="leaf" size={15} color={colors.primaryText} />
        </View>
        {collapsed ? null : (
          <Text style={styles.wordmark} numberOfLines={1}>
            Dating Humane
          </Text>
        )}
      </View>

      <View role="tablist" style={styles.items}>
        {state.routes.map((route, index) => {
          const focused = index === state.index;
          const { options } = descriptors[route.key];
          const label =
            (typeof options.tabBarLabel === 'string' && options.tabBarLabel) || options.title || route.name;
          const color = focused
            ? (options.tabBarActiveTintColor ?? colors.primaryDeep)
            : (options.tabBarInactiveTintColor ?? colors.muted);

          return (
            <Pressable
              key={route.key}
              role="tab"
              accessibilityLabel={label}
              accessibilityState={{ selected: focused }}
              onPress={() => {
                const event = navigation.emit({ type: 'tabPress', target: route.key, canPreventDefault: true });
                if (!focused && !event.defaultPrevented) {
                  navigation.dispatch({ ...CommonActions.navigate(route.name, route.params), target: state.key });
                }
              }}
              onLongPress={() => navigation.emit({ type: 'tabLongPress', target: route.key })}
              style={({ pressed }) => [
                styles.item,
                focused && styles.itemActive,
                collapsed && styles.itemCollapsed,
                pressed && styles.itemPressed,
              ]}
            >
              {options.tabBarIcon?.({ focused, color, size: 22 })}
              {collapsed ? null : (
                <Text style={[styles.itemLabel, { color }]} numberOfLines={1}>
                  {label}
                </Text>
              )}
            </Pressable>
          );
        })}
      </View>

      <View style={styles.spacer} />

      <Pressable
        accessibilityRole="button"
        accessibilityLabel={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        onPress={() => setCollapsed((c) => !c)}
        style={({ pressed }) => [styles.item, collapsed && styles.itemCollapsed, pressed && styles.itemPressed]}
      >
        <Ionicons name={collapsed ? 'chevron-forward' : 'chevron-back'} size={18} color={colors.muted} />
        {collapsed ? null : <Text style={styles.collapseLabel}>Collapse</Text>}
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  bar: {
    backgroundColor: colors.surface,
    borderRightWidth: StyleSheet.hairlineWidth,
    borderRightColor: colors.border,
    paddingHorizontal: 10,
    gap: 4,
    alignSelf: 'stretch',
    ...elevation.low,
  },
  brand: { flexDirection: 'row', alignItems: 'center', gap: 10, paddingHorizontal: 8, paddingBottom: 14, paddingTop: 4 },
  brandCollapsed: { justifyContent: 'center', paddingHorizontal: 0 },
  brandMark: {
    width: 30,
    height: 30,
    borderRadius: 15,
    backgroundColor: colors.primary,
    alignItems: 'center',
    justifyContent: 'center',
    ...elevation.primary,
  },
  wordmark: { fontFamily: fonts.sansBlack, fontSize: 17, letterSpacing: -0.4, color: colors.text, flexShrink: 1 },
  items: { gap: 4 },
  item: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 10,
    paddingHorizontal: 12,
    paddingVertical: 10,
    borderRadius: radii.md,
    minHeight: 44,
  },
  itemCollapsed: { justifyContent: 'center', paddingHorizontal: 0 },
  itemActive: { backgroundColor: colors.primaryTint },
  itemPressed: { opacity: 0.75 },
  itemLabel: { fontFamily: fonts.sansBold, fontSize: 13, flexShrink: 1 },
  spacer: { flex: 1 },
  collapseLabel: { fontFamily: fonts.sansSemi, fontSize: 13, color: colors.muted },
});
