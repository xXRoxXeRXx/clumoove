import * as XLSX from 'xlsx';

type ParseRequest = {
  type: 'parse';
  buffer: ArrayBuffer;
};

type Sheet = {
  name: string;
  rows: string[][];
};

self.onmessage = (event: MessageEvent<ParseRequest>) => {
  if (event.data.type !== 'parse') return;
  try {
    const workbook = XLSX.read(event.data.buffer, {
      type: 'array',
      cellFormula: false,
      cellHTML: false,
      cellText: true,
      bookFiles: false,
      bookVBA: false,
      WTF: true,
    });
    const sheets: Sheet[] = workbook.SheetNames.slice(0, 10).map((name) => {
      const rows = XLSX.utils.sheet_to_json<unknown[]>(workbook.Sheets[name], {
        header: 1,
        raw: false,
        blankrows: false,
        defval: '',
      }).slice(0, 1000).map((row) => row.slice(0, 100).map((value) => String(value).slice(0, 10_000)));
      return { name, rows };
    });
    self.postMessage({ ok: true, sheets });
  } catch {
    self.postMessage({ ok: false });
  }
};
