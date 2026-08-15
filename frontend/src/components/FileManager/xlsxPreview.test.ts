import { describe, expect, it } from 'vitest';
import * as XLSX from 'xlsx';
import { getFixtureBuffer } from './fixtures/fixtureData';
import { parseSpreadsheet } from './xlsxPreview';

function bufferToArrayBuffer(buffer: Uint8Array): ArrayBuffer {
  return buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + buffer.byteLength) as ArrayBuffer;
}

describe('xlsxPreview parser', () => {
  it('parses legacy BIFF5 XLS with CP1252 German umlauts', () => {
    const buffer = getFixtureBuffer('biff5_cp1252');
    const result = parseSpreadsheet(buffer);

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets).toHaveLength(1);
    expect(result.sheets[0].name).toBe('Tabelle1');
    expect(result.sheets[0].rows).toEqual([
      ['Name', 'Ort', 'Hinweis'],
      ['Müller', 'München', 'Größe & Spaß'],
      ['Schröder', 'Köln', 'Prüfung & Äpfel'],
    ]);
  });

  it('parses BIFF8 XLS matching the same CP1252 table structure', () => {
    const buffer = getFixtureBuffer('biff8_cp1252');
    const result = parseSpreadsheet(buffer);

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets).toHaveLength(1);
    expect(result.sheets[0].name).toBe('Tabelle1');
    expect(result.sheets[0].rows).toEqual([
      ['Name', 'Ort', 'Hinweis'],
      ['Müller', 'München', 'Größe & Spaß'],
      ['Schröder', 'Köln', 'Prüfung & Äpfel'],
    ]);
  });

  it('parses BIFF8 XLS with legacy non-default codepage (CP932 Japanese)', () => {
    const buffer = getFixtureBuffer('biff8_cp932');
    const result = parseSpreadsheet(buffer);

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets).toHaveLength(1);
    expect(result.sheets[0].name).toBe('社員一覧');
    expect(result.sheets[0].rows).toEqual([
      ['ID', '名前', '部署', 'ステータス'],
      ['1', '山田太郎', '開発部', '完了'],
      ['2', '佐藤花子', '営業部', '進行中'],
    ]);
  });

  it('returns failure result safely on corrupted or truncated XLS data', () => {
    const buffer = getFixtureBuffer('corrupted');
    const result = parseSpreadsheet(buffer);

    expect(result.ok).toBe(false);
  });

  it('returns failure result on truncated XLSX archive bytes', () => {
    const truncatedZip = new Uint8Array([0x50, 0x4b, 0x03, 0x04, 0x01, 0x02]).buffer;
    const result = parseSpreadsheet(truncatedZip);

    expect(result.ok).toBe(false);
  });

  it('parses modern XLSX workbooks', () => {
    const wb = XLSX.utils.book_new();
    const ws = XLSX.utils.aoa_to_sheet([
      ['Item', 'Quantity', 'Price'],
      ['Apples', '10', '2.50'],
      ['Oranges', '5', '4.00'],
    ]);
    XLSX.utils.book_append_sheet(wb, ws, 'Inventory');
    const xlsxBytes = XLSX.write(wb, { bookType: 'xlsx', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(xlsxBytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets).toHaveLength(1);
    expect(result.sheets[0].name).toBe('Inventory');
    expect(result.sheets[0].rows).toEqual([
      ['Item', 'Quantity', 'Price'],
      ['Apples', '10', '2.50'],
      ['Oranges', '5', '4.00'],
    ]);
  });

  it('parses ODS workbooks', () => {
    const wb = XLSX.utils.book_new();
    const ws = XLSX.utils.aoa_to_sheet([
      ['Metric', 'Value'],
      ['Bandwidth', '100 MB/s'],
    ]);
    XLSX.utils.book_append_sheet(wb, ws, 'Stats');
    const odsBytes = XLSX.write(wb, { bookType: 'ods', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(odsBytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets).toHaveLength(1);
    expect(result.sheets[0].name).toBe('Stats');
    expect(result.sheets[0].rows).toEqual([
      ['Metric', 'Value'],
      ['Bandwidth', '100 MB/s'],
    ]);
  });

  it('parses TSV formatted data', () => {
    const tsvContent = 'Name\tDepartment\tRole\nAlice\tEngineering\tLead\nBob\tDesign\tSpecialist';
    const wb = XLSX.read(tsvContent, { type: 'string' });
    const tsvBytes = XLSX.write(wb, { bookType: 'xlsx', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(tsvBytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets[0].rows).toEqual([
      ['Name', 'Department', 'Role'],
      ['Alice', 'Engineering', 'Lead'],
      ['Bob', 'Design', 'Specialist'],
    ]);
  });

  it('enforces maximum 10 sheets truncation', () => {
    const wb = XLSX.utils.book_new();
    for (let i = 1; i <= 15; i++) {
      const ws = XLSX.utils.aoa_to_sheet([[`Sheet ${i} data`]]);
      XLSX.utils.book_append_sheet(wb, ws, `Sheet${i}`);
    }
    const bytes = XLSX.write(wb, { bookType: 'xlsx', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(bytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets).toHaveLength(10);
    expect(result.sheets.map((s) => s.name)).toEqual([
      'Sheet1', 'Sheet2', 'Sheet3', 'Sheet4', 'Sheet5',
      'Sheet6', 'Sheet7', 'Sheet8', 'Sheet9', 'Sheet10',
    ]);
  });

  it('enforces maximum 1,000 rows per sheet truncation', () => {
    const wb = XLSX.utils.book_new();
    const data: string[][] = [];
    for (let r = 0; r < 1200; r++) {
      data.push([`Row ${r}`]);
    }
    const ws = XLSX.utils.aoa_to_sheet(data);
    XLSX.utils.book_append_sheet(wb, ws, 'ManyRows');
    const bytes = XLSX.write(wb, { bookType: 'xlsx', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(bytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets[0].rows).toHaveLength(1000);
    expect(result.sheets[0].rows[0]).toEqual(['Row 0']);
    expect(result.sheets[0].rows[999]).toEqual(['Row 999']);
  });

  it('enforces maximum 100 cells per row truncation', () => {
    const wb = XLSX.utils.book_new();
    const row: string[] = [];
    for (let c = 0; c < 150; c++) {
      row.push(`Col ${c}`);
    }
    const ws = XLSX.utils.aoa_to_sheet([row]);
    XLSX.utils.book_append_sheet(wb, ws, 'WideRow');
    const bytes = XLSX.write(wb, { bookType: 'xlsx', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(bytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets[0].rows[0]).toHaveLength(100);
    expect(result.sheets[0].rows[0][0]).toBe('Col 0');
    expect(result.sheets[0].rows[0][99]).toBe('Col 99');
  });

  it('enforces maximum 10,000 characters per cell truncation', () => {
    const longString = 'A'.repeat(15_000);
    const wb = XLSX.utils.book_new();
    const ws = XLSX.utils.aoa_to_sheet([[longString]]);
    XLSX.utils.book_append_sheet(wb, ws, 'LongCell');
    const bytes = XLSX.write(wb, { bookType: 'xlsx', type: 'buffer' });
    const result = parseSpreadsheet(bufferToArrayBuffer(bytes));

    expect(result.ok).toBe(true);
    if (!result.ok) return;

    expect(result.sheets[0].rows[0][0]).toHaveLength(10_000);
    expect(result.sheets[0].rows[0][0]).toBe('A'.repeat(10_000));
  });
});
