import { describe, expect, it } from 'vitest';
import { getBandwidthLabel } from './bandwidth';

describe('getBandwidthLabel', () => {
  it('labels a non-preset persisted limit truthfully', () => {
    expect(getBandwidthLabel(3, 'Unlimited')).toBe('3 Mbps');
  });
});
