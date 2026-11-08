/**
 * Label for one stored approval decision. The API returns `allow` plus
 * `pending` (an ask that could not be answered yet); a pending ask must never
 * be shown as a denial. When the backend starts returning an explicit
 * `decision` of allow/deny/ask, it takes precedence.
 */
export type ApprovalReview = { allow?: boolean; pending?: boolean; decision?: string };

export function approvalVerdict(review: ApprovalReview | undefined): { label: string; tone: 'ok' | 'danger' | 'warn' } {
  const decision = (review?.decision || '').toLowerCase();
  if (decision === 'allow') return { label: '已批准', tone: 'ok' };
  if (decision === 'deny') return { label: '已拒绝', tone: 'danger' };
  if (decision === 'ask') return { label: '等待你确认', tone: 'warn' };
  if (review?.pending) return { label: '等待你确认', tone: 'warn' };
  if (review?.allow === true) return { label: '已批准', tone: 'ok' };
  if (review?.allow === false) return { label: '已拒绝', tone: 'danger' };
  return { label: '结果不明确', tone: 'warn' };
}
