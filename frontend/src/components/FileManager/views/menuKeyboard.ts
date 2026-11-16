/** Computes the next roving menuitem index for ArrowDown, ArrowUp, Home and End keys. */
export function nextMenuIndex(key: string, current: number, count: number): number | null {
  if (count <= 0) return null;
  const last = count - 1;
  const targets: Record<string, number> = {
    ArrowDown: current === last ? 0 : current + 1,
    ArrowUp: current === 0 ? last : current - 1,
    Home: 0,
    End: last,
  };
  return key in targets ? targets[key] : null;
}
