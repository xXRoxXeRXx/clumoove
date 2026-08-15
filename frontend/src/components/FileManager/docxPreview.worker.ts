import mammoth from 'mammoth';

type ParseRequest = {
  type: 'parse';
  buffer: ArrayBuffer;
};

self.onmessage = async (event: MessageEvent<ParseRequest>) => {
  if (event.data.type !== 'parse') return;
  try {
    const result = await mammoth.convertToHtml({ arrayBuffer: event.data.buffer });
    self.postMessage({ ok: true, html: result.value.slice(0, 2 * 1024 * 1024) });
  } catch {
    self.postMessage({ ok: false });
  }
};
