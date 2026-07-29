import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const localeFiles = [
  resolve(root, 'src/locales/en/translation.json'),
  resolve(root, 'src/locales/de/translation.json'),
];

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
console.log('Locale key structure and duplicate-key validation passed.');
