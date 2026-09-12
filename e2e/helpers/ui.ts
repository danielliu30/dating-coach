import { expect, type Locator, type Page } from '@playwright/test';

/**
 * Locates the text input rendered under a `Field` label. The app's `Field`
 * renders `<Text>{label}</Text><TextInput/>` as siblings without a `for`
 * association, so the input is found as the label's next input/textarea.
 * React Navigation keeps the previous screen mounted but hidden, so only the
 * visible match is returned.
 */
export function field(page: Page, label: string): Locator {
  return page
    .getByText(label, { exact: true })
    .locator('xpath=following-sibling::*[self::input or self::textarea][1]')
    .filter({ visible: true });
}

/**
 * Locates a visible `Button` (or any `accessibilityRole="button"` pressable)
 * by its label. Buttons with an icon carry the Ionicons glyph (a private-use
 * codepoint) plus a space in front of the label in their accessible name, so
 * the regex tolerates a non-letter prefix instead of matching exactly.
 */
export function button(page: Page, label: string): Locator {
  return page.getByRole('button', { name: iconLabel(label) }).filter({ visible: true });
}

/** Accessible-name matcher for `label` optionally preceded by an icon glyph. */
function iconLabel(label: string): RegExp {
  const escaped = label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`^[^\\p{L}\\p{N}]*${escaped}\\s*$`, 'u');
}

/** Clicks the bottom tab with the given title (Coaches, Sessions, Chats, Analyse, Dashboard, Profile, Account). */
export async function openTab(page: Page, title: string): Promise<void> {
  await page.getByRole('tab', { name: iconLabel(title) }).click();
}

/**
 * Clicks "Remove window" on the coach Profile screen until no availability
 * window rows remain (the screen seeds a new coach with weekday evenings).
 */
export async function removeAllWindows(page: Page): Promise<void> {
  const remove = button(page, 'Remove window');
  while ((await remove.count()) > 0) {
    await remove.first().click();
  }
}

/** Asserts the Sign In screen is showing (used after sign-out, deletion and session recovery). */
export async function expectSignedOut(page: Page): Promise<void> {
  await expect(page.getByText('Sign in to Dating Humane')).toBeVisible({ timeout: 30_000 });
}

/** Locator for the session-ended `Notice` on the Sign In screen, whichever copy it carries. */
export function sessionEndedNotice(page: Page): Locator {
  return page.getByText(/^(You were signed out|Your session was ended)$/);
}

/** All slot chips on a coach detail page: the only buttons whose accessible name contains a clock time. */
export function slotChips(page: Page): Locator {
  return page.getByRole('button', { name: /\d{1,2}:\d{2}/ });
}

/**
 * Selects the nth open slot chip on a coach detail page (chips carry the
 * formatted start time as their accessible name, under the
 * "Open slots · next 7 days" heading) and returns its accessible name.
 */
export async function pickSlot(page: Page, index: number): Promise<string> {
  await expect(page.getByText('Open slots · next 7 days')).toBeVisible();
  const chips = slotChips(page);
  await expect(chips.first()).toBeVisible({ timeout: 30_000 });
  const chip = chips.nth(index);
  const name = (await chip.getAttribute('aria-label')) ?? '';
  await chip.click();
  return name;
}
