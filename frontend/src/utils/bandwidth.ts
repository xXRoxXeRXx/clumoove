export interface BandwidthOption {
  value: number; // Mbps equivalent for backend throttler (0 = unlimited)
  label: string; // Human readable speed string
  isUnlimited?: boolean;
}

export const BANDWIDTH_OPTIONS: BandwidthOption[] = [
  { value: 4, label: '500 KB/s' },
  { value: 8, label: '1 MB/s' },
  { value: 24, label: '3 MB/s' },
  { value: 40, label: '5 MB/s' },
  { value: 80, label: '10 MB/s' },
  { value: 0, label: '', isUnlimited: true },
];

/**
 * Finds the slider index corresponding to a given Mbps value.
 */
export function valueToBandwidthIndex(value: number): number {
  if (value <= 0) {
    return BANDWIDTH_OPTIONS.length - 1; // unlimited
  }
  let bestIndex = 0;
  let minDiff = Math.abs(BANDWIDTH_OPTIONS[0].value - value);
  for (let i = 1; i < BANDWIDTH_OPTIONS.length; i++) {
    const opt = BANDWIDTH_OPTIONS[i];
    if (opt.isUnlimited) continue;
    const diff = Math.abs(opt.value - value);
    if (diff < minDiff) {
      minDiff = diff;
      bestIndex = i;
    }
  }
  return bestIndex;
}

/**
 * Returns the Mbps value for a given slider index.
 */
export function bandwidthIndexToValue(index: number): number {
  const safeIndex = Math.max(0, Math.min(index, BANDWIDTH_OPTIONS.length - 1));
  return BANDWIDTH_OPTIONS[safeIndex].value;
}

/**
 * Gets the display text for a given bandwidth limit Mbps value.
 * Callers must pass a localized string for the unlimited option.
 */
export function getBandwidthLabel(value: number, unlimitedLabel: string): string {
  if (value <= 0) {
    return unlimitedLabel;
  }
  const exact = BANDWIDTH_OPTIONS.find((o) => o.value === value && !o.isUnlimited);
  if (exact) {
    return exact.label;
  }
  return `${value} Mbps`;
}
