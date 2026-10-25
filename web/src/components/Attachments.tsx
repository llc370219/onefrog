import { FileText, X } from 'lucide-react';
import type { Attachment } from '../api';
export default function Attachments({files,onRemove,disabled}:{files:Attachment[];onRemove?:(id:string)=>void;disabled?:boolean}){
 if(!files.length)return null;
 return <div className={`chat-attachments ${onRemove?'draft-attachments':''}`}>{files.map(f=><div className="chat-attachment" key={f.id}>{f.mime.startsWith('image/')?<a href={`/api/attachments/${encodeURIComponent(f.id)}/content`} target="_blank" rel="noreferrer"><img src={`/api/attachments/${encodeURIComponent(f.id)}/content`} alt={f.name}/></a>:<FileText size={19}/>}<span><strong title={f.name}>{f.name}</strong><small>{f.size>=1024*1024?`${(f.size/1024/1024).toFixed(1)} MB`:`${Math.ceil(f.size/1024)} KB`}</small></span>{onRemove&&<button type="button" aria-label={`移除附件 ${f.name}`} disabled={disabled} onClick={()=>onRemove(f.id)}><X size={13}/></button>}</div>)}</div>;
}
