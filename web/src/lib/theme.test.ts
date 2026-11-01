import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import themeInit from '../../public/assets/theme-init.js?raw';
import { THEME_STORAGE_KEY, applyTheme, readThemePreference } from './theme';

beforeEach(() => {
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
  delete document.documentElement.dataset.themePreference;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
  delete document.documentElement.dataset.themePreference;
});

describe('首屏主题初始化', () => {
  it.each([
    [null, true, 'dark', 'system'],
    [null, false, 'light', 'system'],
    ['light', true, 'light', 'light'],
    ['dark', false, 'dark', 'dark'],
    ['system', true, 'dark', 'system'],
    ['invalid', true, 'dark', 'system'],
  ] as const)('偏好 %s 与系统深色 %s 的首屏和应用主题一致', (stored, dark, resolved, preference) => {
    if (stored !== null) window.localStorage.setItem(THEME_STORAGE_KEY, stored);
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: dark })));
    new Function(themeInit)();
    expect(document.documentElement.dataset.theme).toBe(resolved);
    expect(document.documentElement.dataset.themePreference).toBe(preference);
    applyTheme(readThemePreference());
    expect(document.documentElement.dataset.theme).toBe(resolved);
  });

  it('浏览器没有媒体查询能力时默认浅色', () => {
    vi.stubGlobal('matchMedia', undefined);
    new Function(themeInit)();
    expect(document.documentElement.dataset.theme).toBe('light');
    applyTheme(readThemePreference());
    expect(document.documentElement.dataset.theme).toBe('light');
  });

  it('首屏存储读取受限时仍按系统初始化', () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true })));
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(() => new Function(themeInit)()).not.toThrow();
    expect(document.documentElement.dataset.theme).toBe('dark');
  });
});
