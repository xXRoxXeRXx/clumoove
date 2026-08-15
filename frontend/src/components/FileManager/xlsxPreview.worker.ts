import { parseSpreadsheet, type ParseRequest } from './xlsxPreview';

self.onmessage = (event: MessageEvent<ParseRequest>) => {
  if (event.data?.type !== 'parse' || !event.data.buffer) return;
  const result = parseSpreadsheet(event.data.buffer);
  self.postMessage(result);
};

