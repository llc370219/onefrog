import { useEffect, useMemo, useRef, useState } from 'react';
import { Bot, TriangleAlert } from 'lucide-react';
import { api } from '../api';
import { Button } from './ui';
import { useTheme } from '../theme';

function randomToken() {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
}

export default function CustomModuleFrame({
  moduleId,
  title,
  version,
  compact,
  onDataChanged,
  onFixError,
}: {
  moduleId: string;
  title?: string;
  version?: string;
  compact?: boolean;
  onDataChanged?: () => void;
  onFixError?: (errorMessage: string) => void;
}) {
  const frameRef = useRef<HTMLIFrameElement>(null);
  const token = useMemo(() => randomToken(), [moduleId, version]);
  const [height, setHeight] = useState(compact ? 148 : 220);
  const [runtimeError, setRuntimeError] = useState('');
  const { theme } = useTheme();
  const themeRef = useRef(theme);
  themeRef.current = theme;

  // The sandbox follows the panel's light/dark theme.
  useEffect(() => {
    frameRef.current?.contentWindow?.postMessage({ __onefrog_theme: true, theme }, '*');
  }, [theme]);

  useEffect(() => {
    setRuntimeError('');
  }, [moduleId, version]);

  useEffect(() => {
    let alive = true;
    async function sendInit() {
      const win = frameRef.current?.contentWindow;
      if (!win) return;
      try {
        const data = await api.devModuleData(moduleId, { action: 'get' });
        if (!alive) return;
        win.postMessage({ __onefrog_init: true, token, compact: Boolean(compact), theme: themeRef.current, data }, '*');
      } catch (err) {
        if (!alive) return;
        setRuntimeError(err instanceof Error ? err.message : '读取模块数据失败');
      }
    }

    function onMessage(ev: MessageEvent) {
      const win = frameRef.current?.contentWindow;
      if (!win || ev.source !== win) return;
      const d = ev.data;
      if (!d || typeof d !== 'object') return;
      if (d.__onefrog_hello) {
        void sendInit();
        return;
      }
      if (d.token !== token) return;
      if (d.__onefrog_resize && typeof d.height === 'number' && Number.isFinite(d.height)) {
        const minH = compact ? 96 : 130;
        const maxH = compact ? 440 : 1200;
        setHeight(Math.min(Math.max(Math.ceil(d.height), minH), maxH));
        return;
      }
      if (d.__onefrog_error && d.message) {
        setRuntimeError(String(d.message));
        return;
      }
      if (d.__onefrog_req && d.reqId && typeof d.action === 'string') {
        const payload = (d.payload && typeof d.payload === 'object') ? d.payload : {};
        void api
          .devModuleData(moduleId, {
            action: d.action,
            id: typeof payload.id === 'string' ? payload.id : undefined,
            state: payload.state && typeof payload.state === 'object' ? payload.state : undefined,
            data: payload.data && typeof payload.data === 'object' ? payload.data : undefined,
          })
          .then(res => {
            if (!alive) return;
            win.postMessage({ __onefrog_reply: true, token, reqId: d.reqId, data: res }, '*');
            onDataChanged?.();
          })
          .catch(err => {
            if (!alive) return;
            win.postMessage({
              __onefrog_reply: true,
              token,
              reqId: d.reqId,
              error: err instanceof Error ? err.message : '保存失败',
            }, '*');
          });
      }
    }

    window.addEventListener('message', onMessage);
    return () => {
      alive = false;
      window.removeEventListener('message', onMessage);
    };
  }, [moduleId, token, compact, onDataChanged]);

  const src = '/api/dev/modules/' + encodeURIComponent(moduleId) + '/frame?v=' + encodeURIComponent(version || '1');

  return (
    <div className="custom-frame-wrap">
      <iframe
        ref={frameRef}
        className="custom-module-frame"
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        title={title || '自定义模块'}
        src={src}
        style={{ height: height + 'px' }}
      />
      {runtimeError && onFixError && (
        <div className="custom-frame-error">
          <span><TriangleAlert size={14}/>脚本报错：{runtimeError}</span>
          <Button onClick={() => onFixError(runtimeError)}><Bot size={14}/>交给自定义模块修复</Button>
        </div>
      )}
    </div>
  );
}

