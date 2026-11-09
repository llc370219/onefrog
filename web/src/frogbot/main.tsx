import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource/inter/400.css';
import '@fontsource/inter/500.css';
import '@fontsource/inter/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import './index.css';
import App from './App';

// 主题跟随 OneFrog 面板：面板（同源的另一个页面）切换主题时同步过来。
window.addEventListener('storage', (event) => {
  if (event.key !== 'onefrog-theme') return;
  if (event.newValue === 'light' || event.newValue === 'dark') document.documentElement.dataset.theme = event.newValue;
  else delete document.documentElement.dataset.theme;
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
