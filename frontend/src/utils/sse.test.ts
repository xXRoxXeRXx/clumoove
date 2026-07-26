import { describe, it, expect } from 'vitest';
import { parseSseFrame } from './sse';

describe('parseSseFrame', () => {
  it('parses event and data lines', () => {
    const { event, data } = parseSseFrame('event: migrations\ndata: {"a":1}');
    expect(event).toBe('migrations');
    expect(data).toBe('{"a":1}');
  });

  it('joins multi-line data', () => {
    const { event, data } = parseSseFrame('data: line1\ndata: line2');
    expect(event).toBe('message');
    expect(data).toBe('line1\nline2');
  });
});
