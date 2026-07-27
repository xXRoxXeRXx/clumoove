import { describe, it, expect } from 'vitest';
import { parseSseFrame, readSseStream } from './sse';

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

  it('preserves multi-line JSON payloads', () => {
    const { event, data } = parseSseFrame('event: migration\ndata: {"status":"RUNNING",\ndata: "processed_files":2}');
    expect(event).toBe('migration');
    expect(JSON.parse(data)).toEqual({ status: 'RUNNING', processed_files: 2 });
  });

  it('delivers error events and ends cleanly when the response stream closes', async () => {
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('event: error\ndata: INTERNAL_ERROR\n\n'));
        controller.close();
      },
    });
    const frames: Array<[string, string]> = [];

    const received = await readSseStream(stream, new AbortController().signal, (event, data) => {
      frames.push([event, data]);
    });

    expect(received).toBe(true);
    expect(frames).toEqual([['error', 'INTERNAL_ERROR']]);
  });
});
