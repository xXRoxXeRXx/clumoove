const boundaryErrors = new WeakSet<Error>();

export function markBoundaryError(error: Error): void {
  boundaryErrors.add(error);
}

export function isBoundaryError(error: unknown): boolean {
  return error instanceof Error && boundaryErrors.has(error);
}
