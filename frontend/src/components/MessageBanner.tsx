export type MessageState = { text: string; type: 'success' | 'error' | 'info' } | null;

export function MessageBanner({ message }: { message: MessageState }) {
  if (!message) return null;
  const styles =
    message.type === 'success'
      ? 'ui-alert-success'
      : message.type === 'info'
        ? 'ui-alert-info'
        : 'ui-alert-error';
  return (
    <div
      role="status"
      className={`ui-alert px-3 py-2 text-sm text-center leading-relaxed ${styles}`}
    >
      {message.text}
    </div>
  );
}
