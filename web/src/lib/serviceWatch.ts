// Pure helpers for the service monitor selection. Keys match the backend:
// ports are watched by listen address, everything else by name.
export type Kind = 'systemd' | 'docker' | 'ports';
export type Target = { kind: Kind; name: string };
export type Selection = Record<string, Target[]>;
type Row = { kind: Kind; name: string; ports?: string };
type Status = { server_id: string; state: string };

export const intervals = [30, 60, 120, 300, 600, 1800, 3600];
export const stateNames: Record<string, string> = { pending: '待检测', ok: '正常', fault: '异常', missing: '缺失', unknown: '无法判断' };
export const stateTone = (state: string) => state === 'ok' ? 'ok' : state === 'fault' || state === 'missing' ? 'danger' : state === 'unknown' ? 'warn' : 'muted';

export const watchKey = (t: Target) => JSON.stringify([t.kind, t.name]);

export function targetOf(row: Row): Target | null {
  const name = (row.kind === 'ports' ? row.ports || '' : row.name).trim();
  return name ? { kind: row.kind, name } : null;
}

export function isWatched(sel: Selection, server: string, t: Target | null) {
  return !!t && (sel[server] || []).some(x => watchKey(x) === watchKey(t));
}

export function toggle(sel: Selection, server: string, t: Target): Selection {
  const list = sel[server] || [];
  const next = isWatched(sel, server, t) ? list.filter(x => watchKey(x) !== watchKey(t)) : [...list, t];
  const out = { ...sel };
  if (next.length) out[server] = next; else delete out[server];
  return out;
}

// Order-insensitive comparison so the save button only lights up on real changes.
export function sameSelection(a: Selection, b: Selection) {
  const flat = (s: Selection) => Object.entries(s).flatMap(([id, list]) => list.map(t => id + ' ' + watchKey(t))).sort().join('|');
  return flat(a) === flat(b);
}

export function counts(status: Status[], server?: string) {
  const rows = server ? status.filter(s => s.server_id === server) : status;
  return {
    total: rows.length,
    ok: rows.filter(s => s.state === 'ok').length,
    bad: rows.filter(s => s.state === 'fault' || s.state === 'missing').length,
    unknown: rows.filter(s => s.state === 'unknown').length,
  };
}

export type GlanceRow = { server_id: string; kind: Kind; name: string; state: string; detail?: string; checked_at?: string };

const glanceRank = (state: string) => state === 'fault' || state === 'missing' ? 0 : state === 'unknown' ? 1 : state === 'pending' ? 2 : 3;

// Dashboard rows are the saved selection only. Servers with nothing selected,
// and services that were detected but not ticked, stay off the card.
export function glanceItems(selection: Selection, status: GlanceRow[], hostIds: string[]): GlanceRow[] {
  const known = new Set(hostIds);
  const found = new Map(status.map(s => [s.server_id + '\0' + watchKey(s), s]));
  const rows: GlanceRow[] = [];
  for (const [id, list] of Object.entries(selection)) {
    if (!known.has(id)) continue;
    for (const t of list) {
      const hit = found.get(id + '\0' + watchKey(t));
      rows.push(hit
        ? { server_id: id, kind: t.kind, name: t.name, state: hit.state || 'pending', detail: hit.detail, checked_at: hit.checked_at }
        : { server_id: id, kind: t.kind, name: t.name, state: 'pending' });
    }
  }
  return rows.sort((a, b) => glanceRank(a.state) - glanceRank(b.state) || a.name.localeCompare(b.name, 'zh') || a.server_id.localeCompare(b.server_id));
}

export function intervalLabel(seconds: number) {
  return seconds < 60 ? seconds + ' 秒' : seconds < 3600 ? seconds / 60 + ' 分钟' : seconds / 3600 + ' 小时';
}
