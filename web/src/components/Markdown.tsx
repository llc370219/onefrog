import { Fragment, type ReactNode } from 'react';

// Safe Markdown for model replies. It builds React elements only (no raw HTML),
// so anything that looks like a tag is shown as text. Unclosed code fences
// render as code, which keeps partial (streaming) text readable.

const SAFE_URL = /^(https?:\/\/|mailto:)/i;
const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+?\*\*)|(\[[^\]\n]+\]\([^)\s]+\))|(https?:\/\/[^\s<>()\u3000-\u303f\uff00-\uffef]+)|(\*[^*\s][^*\n]*?\*)/g;

function link(href: string, children: ReactNode, key: number) {
  if (!SAFE_URL.test(href)) return <Fragment key={key}>{children}</Fragment>;
  return <a key={key} href={href} target="_blank" rel="noopener noreferrer">{children}</a>;
}

export function inline(text: string, depth = 0): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0, n = 0;
  for (const m of text.matchAll(INLINE)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const [token, code, bold, md, url, em] = m;
    if (code) out.push(<code key={n++}>{code.slice(1, -1)}</code>);
    else if (bold) out.push(<strong key={n++}>{depth < 3 ? inline(bold.slice(2, -2), depth + 1) : bold.slice(2, -2)}</strong>);
    else if (md) {
      const split = md.lastIndexOf('](');
      const label = md.slice(1, split), href = md.slice(split + 2, -1);
      out.push(link(href, depth < 3 ? inline(label, depth + 1) : label, n++));
    } else if (url) {
      const clean = url.replace(/[.,;:!?'"]+$/, '');
      out.push(link(clean, clean, n++));
      if (clean.length < url.length) out.push(url.slice(clean.length));
    } else if (em) out.push(<em key={n++}>{depth < 3 ? inline(em.slice(1, -1), depth + 1) : em.slice(1, -1)}</em>);
    else out.push(token);
    last = at + token.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function lines(rows: string[]) {
  return rows.map((row, i) => <Fragment key={i}>{i > 0 && <br />}{inline(row)}</Fragment>);
}

type Block = { kind: 'code'; lang: string; text: string } | { kind: 'p' | 'quote'; rows: string[] } | { kind: 'h'; level: number; text: string } | { kind: 'ul' | 'ol'; items: string[]; start: number } | { kind: 'hr' };

export function parseBlocks(src: string): Block[] {
  const rows = src.replace(/\r\n?/g, '\n').split('\n');
  const blocks: Block[] = [];
  let i = 0;
  while (i < rows.length) {
    const row = rows[i];
    const fence = row.match(/^\s*(```+|~~~+)\s*([\w+-]*)/);
    if (fence) {
      const close = fence[1];
      const body: string[] = [];
      i++;
      while (i < rows.length && !rows[i].trim().startsWith(close)) body.push(rows[i++]);
      i++;
      blocks.push({ kind: 'code', lang: fence[2] || '', text: body.join('\n') });
      continue;
    }
    if (!row.trim()) { i++; continue; }
    const heading = row.match(/^(#{1,6})\s+(.*)$/);
    if (heading) { blocks.push({ kind: 'h', level: heading[1].length, text: heading[2].replace(/\s#+\s*$/, '') }); i++; continue; }
    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(row)) { blocks.push({ kind: 'hr' }); i++; continue; }
    const bullet = /^\s*[-*+]\s+(.*)$/, ordered = /^\s*(\d{1,9})[.)]\s+(.*)$/;
    if (bullet.test(row) || ordered.test(row)) {
      const kind = bullet.test(row) ? 'ul' : 'ol';
      const re = kind === 'ul' ? bullet : ordered;
      const items: string[] = [];
      const start = kind === 'ol' ? Number(row.match(ordered)![1]) : 1;
      while (i < rows.length) {
        const m = rows[i].match(re);
        if (m) { items.push(kind === 'ul' ? m[1] : m[2]); i++; continue; }
        if (rows[i].trim() && /^\s{2,}\S/.test(rows[i]) && items.length) { items[items.length - 1] += '\n' + rows[i].trim(); i++; continue; }
        break;
      }
      blocks.push({ kind, items, start });
      continue;
    }
    if (/^\s*>/.test(row)) {
      const quote: string[] = [];
      while (i < rows.length && /^\s*>/.test(rows[i])) quote.push(rows[i++].replace(/^\s*>\s?/, ''));
      blocks.push({ kind: 'quote', rows: quote });
      continue;
    }
    const para: string[] = [];
    while (i < rows.length && rows[i].trim() && !/^\s*(```|~~~|#{1,6}\s|>|[-*+]\s|\d{1,9}[.)]\s)/.test(rows[i])) para.push(rows[i++]);
    if (!para.length) para.push(rows[i++]);
    blocks.push({ kind: 'p', rows: para });
  }
  return blocks;
}

export default function Markdown({ text }: { text: string }) {
  return <>{parseBlocks(text).map((b, i) => {
    switch (b.kind) {
      case 'code': return <pre key={i} className="reply-code"><code>{b.text}</code></pre>;
      case 'h': return <p key={i} className={'md-h md-h' + Math.min(b.level, 3)}>{inline(b.text)}</p>;
      case 'hr': return <hr key={i} />;
      case 'quote': return <blockquote key={i}>{lines(b.rows)}</blockquote>;
      case 'ul': return <ul key={i}>{b.items.map((it, n) => <li key={n}>{lines(it.split('\n'))}</li>)}</ul>;
      case 'ol': return <ol key={i} start={b.start}>{b.items.map((it, n) => <li key={n}>{lines(it.split('\n'))}</li>)}</ol>;
      default: return <p key={i}>{lines(b.rows)}</p>;
    }
  })}</>;
}
