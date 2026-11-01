(() => {
  let preference = 'system';
  try {
    const stored = window.localStorage.getItem('ai-router.theme');
    if (stored === 'light' || stored === 'dark') preference = stored;
  } catch {}
  const systemDark =
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: dark)').matches;
  document.documentElement.dataset.theme =
    preference === 'system' ? (systemDark ? 'dark' : 'light') : preference;
  document.documentElement.dataset.themePreference = preference;
})();
