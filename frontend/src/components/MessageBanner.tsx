export type MessageState = { text: string; type: 'success' | 'error' | 'info' } | null;

export function MessageBanner({ message }: { message: MessageState }) {
  if (!message) return null;
  const styles =
    message.type === 'success'
      ? 'bg-[var(--color-success-bg)] border-[var(--color-success-border)] text-[var(--color-success-text)]'
      : message.type === 'info'
        ? 'bg-[var(--color-info-bg)] border-[var(--color-info-border)] text-[var(--color-info-text)]'
        : 'bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)]';
  return (
    <div
      role="status"
      className={`rounded-md border px-3 py-2 text-sm text-center leading-relaxed ${styles}`}
    >
      {message.text}
    </div>
  );
}
