/** Whether the reader is close enough to the end to follow new chat activity. */
export function nearChatBottom(
  { scrollTop, scrollHeight, clientHeight }: Pick<HTMLElement, 'scrollTop' | 'scrollHeight' | 'clientHeight'>,
  threshold = 96,
): boolean {
  return scrollHeight - clientHeight - scrollTop <= threshold;
}
