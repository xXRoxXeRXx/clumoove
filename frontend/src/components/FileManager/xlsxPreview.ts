import * as XLSX from 'xlsx';
import * as cptable from 'xlsx/dist/cpexcel.full.mjs';

XLSX.set_cptable(cptable);

export type Sheet = {
  name: string;
  rows: string[][];
};

export type SpreadsheetParseResult =
  | { ok: true; sheets: Sheet[] }
  | { ok: false };

export type ParseRequest = {
  type: 'parse';
  buffer: ArrayBuffer;
};

export function parseSpreadsheet(buffer: ArrayBuffer): SpreadsheetParseResult {
  try {
    const data = new Uint8Array(buffer);
    const workbook = XLSX.read(data, {
      type: 'array',
      cellFormula: false,
      cellHTML: false,
      cellText: true,
      bookFiles: false,
      bookVBA: false,
      WTF: true,
    });
    const sheets: Sheet[] = workbook.SheetNames.slice(0, 10).map((name) => {
      const sheet = workbook.Sheets[name];
      const rawRows = sheet
        ? XLSX.utils.sheet_to_json<unknown[]>(sheet, {
            header: 1,
            raw: false,
            blankrows: false,
            defval: '',
          })
        : [];
      const rows = rawRows
        .slice(0, 1000)
        .map((row) => (Array.isArray(row) ? row : []).slice(0, 100).map((value) => String(value ?? '').slice(0, 10_000)));
      return { name, rows };
    });
    return { ok: true, sheets };
  } catch {
    return { ok: false };
  }
}
