import { useEffect, useState } from 'react';
import { Archive, Download, Upload, ArrowRight, KeyRound, RefreshCw } from 'lucide-react';
import { Button, Field, Notice } from './ui';

export type MigrationStatus = { phase:string; message:string; busy:boolean; paused:boolean; restart_required:boolean; receiving:boolean; expires_at:string; driver:string; version:string; max_bytes:number; public_url:string };
async function request<T>(path:string, body?:unknown):Promise<T> {
  const r=await fetch('/api/'+path,{credentials:'same-origin',method:body===undefined?'GET':'POST',headers:body===undefined?{}:{'Content-Type':'application/json','X-OneFrog-CSRF':'1'},body:body===undefined?undefined:JSON.stringify(body)});
  if(!r.ok){const data=await r.json().catch(()=>({}));throw new Error(data.error||'请求失败');}
  return r.json();
}
export const migrationStatus=()=>request<MigrationStatus>('migration/status');
const text=(e:unknown)=>e instanceof Error?e.message:'操作失败';

export default function BackupMigration({onStatus}:{onStatus?:(s:MigrationStatus)=>void}) {
  const [status,setStatus]=useState<MigrationStatus|null>(null);
  const [error,setError]=useState('');const [notice,setNotice]=useState('');const [work,setWork]=useState('');
  const [password,setPassword]=useState('');const [repeat,setRepeat]=useState('');
  const [file,setFile]=useState<File|null>(null);const [restorePassword,setRestorePassword]=useState('');
  const [confirmRestore,setConfirmRestore]=useState(false);const [confirmSend,setConfirmSend]=useState(false);const [confirmResume,setConfirmResume]=useState(false);
  const [address,setAddress]=useState('');const [code,setCode]=useState('');const [receiverCode,setReceiverCode]=useState('');
  const [probeURL,setProbeURL]=useState('');const [checks,setChecks]=useState<{name:string;ok:boolean;message:string}[]>([]);
  useEffect(()=>{let active=true;const refresh=()=>migrationStatus().then(s=>{if(active){setStatus(s);onStatus?.(s);}}).catch(e=>{if(active)setError(text(e));});refresh();const timer=setInterval(refresh,3000);return()=>{active=false;clearInterval(timer);};},[onStatus]);
  const busy=!!work||!!status?.busy||!!status?.restart_required;
  async function run(name:string,action:()=>Promise<void>){setWork(name);setError('');setNotice('');try{await action();const s=await migrationStatus();setStatus(s);onStatus?.(s);}catch(e){setError(text(e));}finally{setWork('');}}
  async function download(){
    if(password!==repeat)throw new Error('两次备份密码不一致');
    const r=await fetch('/api/backup/export',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-OneFrog-CSRF':'1'},body:JSON.stringify({password})});
    if(!r.ok){const d=await r.json();throw new Error(d.error||'导出失败');}
    const blob=await r.blob();const url=URL.createObjectURL(blob);const a=document.createElement('a');a.href=url;a.download=r.headers.get('Content-Disposition')?.match(/filename="([^"]+)"/)?.[1]||'onefrog.ofbackup';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);setPassword('');setRepeat('');setNotice('备份已下载。请单独保管密码，恢复时需要使用。');
  }
  async function restore(){
    if(!file)return;const form=new FormData();form.append('backup',file);form.append('password',restorePassword);form.append('confirm','true');
    const r=await fetch('/api/backup/restore',{method:'POST',credentials:'same-origin',headers:{'X-OneFrog-CSRF':'1'},body:form});
    const d=await r.json();if(!r.ok)throw new Error(d.error||'恢复失败');setRestorePassword('');setNotice('恢复完成，正在重新加载。请用旧机账号登录。');setTimeout(()=>location.reload(),1800);
  }
  return <div className="migration-panel">
    <div className="settings-info"><Archive size={22}/><div><strong>把整个工作台搬到新服务器</strong><p>包含账号、聊天、Bot 记忆、API 配置与密钥、自动化、服务监控和附件。新旧版本及存储类型需一致；恢复目标须是全新实例。当前单包上限 256 MiB。</p></div></div>
    {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{notice}</Notice>}
    {status?.message&&<Notice error={status.phase==='failed'||status.phase==='uncertain'}>{status.message}</Notice>}
    {status?.busy&&<p role="status" className="migration-progress"><RefreshCw className="spin" size={15}/>{({packing:'正在生成加密迁移包…',transferring:'正在传输并等待新机恢复…',restoring:'正在校验并恢复…',exporting:'正在生成备份…',receiving:'正在接收迁移…'} as Record<string,string>)[status.phase]||'正在处理…'}</p>}
    {status?.restart_required&&<Button onClick={()=>location.reload()}>重新加载并登录</Button>}
    {status?.paused&&!status.restart_required&&<section className="migration-card migration-paused"><h3>当前工作台已暂停</h3><p>暂停会保留到重启后。聊天、自动化和后台写入暂停；迁移后先确认新机数据和连接，再启用任务。</p><div className="migration-actions"><Button disabled={busy} busy={work==='check'} onClick={()=>run('check',async()=>{const d=await request<{results:typeof checks}>('migration/check',{});setChecks(d.results);})}>检查服务器连接</Button></div>{checks.length>0&&<ul className="migration-checks">{checks.map((x,i)=><li key={i}><span>{x.name}</span><span className={x.ok?'ok':'danger'}>{x.message}</span></li>)}</ul>}<Field label="新面板地址" hint="若地址改变，在新机更新已安装探针的上报地址；固定域名切换到新机后可跳过。"><input type="url" value={probeURL} placeholder={status.public_url||'https://新的面板地址'} onChange={e=>setProbeURL(e.target.value)}/></Field><Button disabled={busy||!probeURL} busy={work==='probes'} onClick={()=>run('probes',async()=>{const d=await request<{results:typeof checks}>('migration/probes',{address:probeURL});setChecks(d.results);})}>更新探针上报地址</Button><label className="checkbox-label"><input type="checkbox" checked={confirmResume} onChange={e=>setConfirmResume(e.target.checked)}/>已确认另一台实例暂停或未接管，可以让本机运行任务</label><Button variant="primary" disabled={busy||!confirmResume} onClick={()=>run('resume',async()=>{await request('migration/resume',{confirm:true});setReceiverCode('');setConfirmResume(false);setNotice('已恢复运行');})}>恢复本机运行</Button></section>}
    <div className="migration-grid">
      <section className="migration-card"><h3><Download size={17}/>导出加密备份</h3><p>保存一个 .ofbackup 文件。生成快照时短暂暂停写入，导出完成后恢复原状态。</p><Field label="备份密码"><input type="password" autoComplete="new-password" minLength={12} value={password} onChange={e=>setPassword(e.target.value)} placeholder="至少 12 位"/></Field><Field label="再次输入备份密码"><input type="password" autoComplete="new-password" value={repeat} onChange={e=>setRepeat(e.target.value)}/></Field><Button disabled={busy||password.length<12||password!==repeat} busy={work==='export'} onClick={()=>run('export',download)}>下载备份</Button></section>
      <section className="migration-card"><h3><Upload size={17}/>从备份恢复</h3><p>在新安装的 OneFrog 中上传备份。恢复后使用原账号登录，自动任务保持暂停。</p><Field label="备份文件"><input type="file" accept=".ofbackup" onChange={e=>setFile(e.target.files?.[0]||null)}/></Field><Field label="恢复密码"><input type="password" autoComplete="off" value={restorePassword} onChange={e=>setRestorePassword(e.target.value)}/></Field><label className="checkbox-label"><input type="checkbox" checked={confirmRestore} onChange={e=>setConfirmRestore(e.target.checked)}/>确认恢复到本机，当前临时登录账号将替换为备份账号</label><Button disabled={busy||!file||file.size>256*1024*1024+1024||restorePassword.length<12||!confirmRestore} busy={work==='restore'} onClick={()=>run('restore',restore)}>上传并恢复</Button></section>
    </div>
    <section className="migration-card"><h3><KeyRound size={17}/>在线搬家</h3><p>先在新机生成迁移码，再在旧机填写新机地址和迁移码。数据自动加密传输，原数据保留；两端恢复运行需由你确认。</p><div className="migration-grid"><div><h4>① 新机 · 接收迁移</h4><Button disabled={busy} busy={work==='code'} onClick={()=>run('code',async()=>{const d=await request<{code:string}>('migration/code',{});setReceiverCode(d.code);})}>{receiverCode?'重新生成迁移码':'生成一次性迁移码'}</Button>{receiverCode&&status?.receiving&&<Field label="一次性迁移码" hint="15 分钟有效，请复制到旧机；不要发给其他人。"><input value={receiverCode} readOnly autoComplete="off" onFocus={e=>e.target.select()}/></Field>}{status?.receiving&&!receiverCode&&<p>接收已开启。若没有保留迁移码，可重新生成。</p>}</div><div><h4>② 旧机 · 迁移到新服务器</h4><Field label="目标面板地址"><input type="url" value={address} onChange={e=>setAddress(e.target.value)} placeholder="https://panel.example.com 或 http://IP:8080"/></Field><Field label="新机迁移码"><input type="password" autoComplete="off" value={code} onChange={e=>setCode(e.target.value.trim())}/></Field><label className="checkbox-label"><input type="checkbox" checked={confirmSend} onChange={e=>setConfirmSend(e.target.checked)}/>确认上述地址属于我的新服务器，并暂停本机直到迁移完成</label><Button variant="primary" disabled={busy||!address||!code||!confirmSend} busy={work==='send'} onClick={()=>run('send',async()=>{await request('migration/send',{address,code,confirm:true});setCode('');})}>开始迁移<ArrowRight size={15}/></Button></div></div></section>
    <p className="test-note">进行中的模型回复、SSH 命令和内存队列不会跨机继续；请等任务结束后迁移。端口、域名、反向代理和环境变量由新服务器保留，外部 OAuth 应用配置需在新机配置。</p>
  </div>;
}
