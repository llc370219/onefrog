import { Download, ExternalLink, FileCode2, FileImage, FileText } from 'lucide-react';
import { api, isImage } from '../api';
import { base, sizeOf } from '../lib/format';

// 产物 / 附件卡片：图片直接显示，其他文件可以打开或下载
export default function Artifact({ path, size, mime, compact }: { path: string; size?: number; mime?: string; compact?: boolean }) {
  const name = base(path);
  const img = isImage(name, mime);
  const code = /\.(go|ts|tsx|js|py|rs|sh|json|ya?ml|toml|html|css|sql)$/i.test(name);
  const Icon = img ? FileImage : code ? FileCode2 : FileText;
  if (img && !compact) {
    return (
      <a href={api.fileURL(path)} target="_blank" rel="noreferrer" className="group block w-fit overflow-hidden rounded-xl border border-line bg-s2" title={`打开 ${path}`}>
        <img src={api.fileURL(path)} alt={name} loading="lazy" className="max-h-64 max-w-full object-contain transition group-hover:opacity-95" />
      </a>
    );
  }
  return (
    <div className="flex min-w-0 max-w-full items-center gap-2.5 rounded-xl border border-line bg-s1 py-2 pl-2.5 pr-1.5">
      <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-s2 text-fg3"><Icon size={16} /></span>
      <div className="min-w-0 flex-1 leading-tight">
        <div className="truncate text-[13px] font-medium text-fg">{name}</div>
        <div className="truncate text-[11.5px] text-fg4">{path}{size ? ` · ${sizeOf(size)}` : ''}</div>
      </div>
      <a href={api.fileURL(path)} target="_blank" rel="noreferrer" aria-label={`打开 ${name}`} title="打开" className="icon-btn"><ExternalLink size={14} /></a>
      <a href={api.fileURL(path, true)} aria-label={`下载 ${name}`} title="下载" className="icon-btn"><Download size={14} /></a>
    </div>
  );
}
