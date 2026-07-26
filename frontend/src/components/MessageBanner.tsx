export type MessageState = { text: string; type: 'success' | 'error' | 'info' } | null;

export function MessageBanner({ message }: { message: MessageState }) {
  if (!message) return null;
  const styles =
    message.type === 'success'
      ? 'bg-emerald-50 border-emerald-200 text-emerald-800'
      : message.type === 'info'
        ? 'bg-[var(--color-info-bg)] border-[var(--color-info-border)] text-blue-800'
        : 'bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)]';
  return (
    <div
      role="status"
      className={`p-3 rounded-xl border text-[11px] font-mono text-center leading-relaxed ${styles}`}
    >
      {message.text}
    </div>
  );
}
