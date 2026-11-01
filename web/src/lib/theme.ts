export type ThemePreference = 'system' | 'light' | 'dark';

export const THEME_STORAGE_KEY = 'ai-router.theme';

export function isThemePreference(value: unknown): value is ThemePreference {
  return value === 'system' || value === 'light' || value === 'dark';
}

export function readThemePreference(): ThemePreference {
  try {
    const value = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isThemePreference(value) ? value : 'system';
  } catch {
    return 'system';
  }
}

export function saveThemePreference(value: ThemePreference): void {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, value);
  } catch {
    // 存储受限时仍允许在当前页面切换主题。
  }
}

export function applyTheme(value: ThemePreference): void {
  const systemDark =
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: dark)').matches;
  document.documentElement.dataset.theme = value === 'system' ? (systemDark ? 'dark' : 'light') : value;
  document.documentElement.dataset.themePreference = value;
}
