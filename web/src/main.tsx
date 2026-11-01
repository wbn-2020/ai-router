import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import { App } from './app/App';
import { applyTheme, readThemePreference } from './lib/theme';
import './styles.css';

applyTheme(readThemePreference());

const container = document.getElementById('root');
if (container === null) {
  throw new Error('管理端缺少 #root 挂载点');
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
