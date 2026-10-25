import type { ApprovalMode } from './api';

/** User-facing copy for the approval modes, shared by the first-login choice, Settings and chat controls. */
export const primaryModes: { id: ApprovalMode; name: string; short: string; desc: string; risks?: string[]; recommended?: boolean }[] = [
  { id: 'smart', name: '智能审批', short: '智能审批', recommended: true, desc: '查看状态、读日志等只读命令直接执行；删除、重启、改配置、装软件、改防火墙或账号等有风险的命令，会先暂停，等你在弹窗里确认。' },
  { id: 'off', name: '关闭审批', short: '关闭审批', desc: 'AI 直接执行它认为需要的命令，不再询问你。', risks: ['AI 可能误删数据、改坏配置、重启线上服务或把自己锁在服务器外', '服务器输出、文件或附件里藏的恶意指令可能诱导 AI 执行命令', '只有极少数灾难性命令仍会被内置清单拦截，其余全部直接执行'] },
];

export const advancedModes: { id: ApprovalMode; name: string; short: string; desc: string }[] = [
  { id: 'manual', name: '逐条确认', short: '逐条确认', desc: '每条命令都弹窗等你确认，包括只读命令。最稳妥，但打扰最多。' },
  { id: 'model', name: '替我审批', short: '替我审批', desc: '每条命令交给你指定的独立模型判断；拒绝、超时或结果无效时不执行。需要先配置审批模型。' },
  { id: 'plan', name: '仅规划', short: '仅规划', desc: '只对话和拆解任务，不执行任何远程命令。' },
];

export const modeLabel = (mode: string | undefined) => [...primaryModes, ...advancedModes].find(m => m.id === mode)?.short || '智能审批（未选择）';

export const smartLimits = '智能审批靠规则与 AI 自评共同判断，能减少但无法完全消除风险：规则可能漏判少见的危险写法，弹窗也需要你认真看清再批准。重要数据请先备份。';
