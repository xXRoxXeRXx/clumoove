import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const localeFiles = [
  resolve(root, 'src/locales/en/translation.json'),
  resolve(root, 'src/locales/de/translation.json'),
];
const apiErrorCodeFiles = [
  resolve(root, '../backend/cmd/api/response.go'),
  resolve(root, '../backend/internal/httpresp/response.go'),
];
// UNKNOWN is the translation fallback and NETWORK is emitted by the frontend's
// API client when a request cannot reach the server.
const frontendErrorCodes = ['UNKNOWN', 'NETWORK'];

function duplicateKeys(source, file) {
  const keysByObject = [];
  let index = 0;

  const skip = () => { while (/\s/.test(source[index] ?? '')) index += 1; };
  const string = () => {
    const start = index;
    index += 1;
    while (index < source.length) {
      if (source[index] === '\\') index += 2;
      else if (source[index++] === '"') return JSON.parse(source.slice(start, index));
    }
    throw new Error(`${file}: unterminated JSON string`);
  };
  const value = () => {
    skip();
    if (source[index] === '{') return object();
    if (source[index] === '[') {
      index += 1;
      skip();
      while (source[index] !== ']') { value(); skip(); if (source[index] === ',') { index += 1; skip(); } else break; }
      if (source[index++] !== ']') throw new Error(`${file}: invalid JSON array`);
      return;
    }
    if (source[index] === '"') { string(); return; }
    while (index < source.length && !',]}\s'.includes(source[index])) index += 1;
  };
  const object = () => {
    const keys = new Set();
    keysByObject.push(keys);
    index += 1;
    skip();
    while (source[index] !== '}') {
      if (source[index] !== '"') throw new Error(`${file}: invalid JSON object key`);
      const key = string();
      if (keys.has(key)) throw new Error(`${file}: duplicate translation key "${key}"`);
      keys.add(key);
      skip();
      if (source[index++] !== ':') throw new Error(`${file}: invalid JSON object`);
      value();
      skip();
      if (source[index] === ',') { index += 1; skip(); } else break;
    }
    if (source[index++] !== '}') throw new Error(`${file}: invalid JSON object`);
  };

  value();
  return keysByObject;
}

function shape(value) {
  if (Array.isArray(value)) return value.map(shape);
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, child]) => [key, shape(child)]));
  return typeof value;
}

function apiErrorCodes() {
  const codes = new Set();
  const pattern = /\bAPIErrorCode\s*=\s*"([A-Z][A-Z0-9_]*)"/g;

  for (const file of apiErrorCodeFiles) {
    const source = readFileSync(file, 'utf8');
    for (const match of source.matchAll(pattern)) codes.add(match[1]);
  }

  return codes;
}

function compareErrorKeys(locale, file, expectedCodes) {
  const actualCodes = new Set(Object.keys(locale.errors ?? {}));
  const missing = [...expectedCodes].filter((code) => !actualCodes.has(code));
  const orphaned = [...actualCodes].filter((code) => !expectedCodes.has(code));

  if (missing.length || orphaned.length) {
    const details = [
      missing.length && `missing: ${missing.sort().join(', ')}`,
      orphaned.length && `orphaned: ${orphaned.sort().join(', ')}`,
    ].filter(Boolean).join('; ');
    throw new Error(`${file}: error translations do not match backend API error codes (${details})`);
  }
}

const [enFile, deFile] = localeFiles;
const enText = readFileSync(enFile, 'utf8');
const deText = readFileSync(deFile, 'utf8');
duplicateKeys(enText, enFile);
duplicateKeys(deText, deFile);
const en = JSON.parse(enText);
const de = JSON.parse(deText);
if (JSON.stringify(shape(en)) !== JSON.stringify(shape(de))) {
  throw new Error('Translation files do not have matching key structures');
}
const expectedErrorCodes = apiErrorCodes();
for (const code of frontendErrorCodes) expectedErrorCodes.add(code);
compareErrorKeys(en, enFile, expectedErrorCodes);
compareErrorKeys(de, deFile, expectedErrorCodes);
console.log('Locale key structure, duplicate-key, and API error-code validation passed.');
