import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

// Fails when the code reads a CSS custom property that index.css never defines. An undefined
// variable silently becomes `initial` (no color, no z-index), which a type checker cannot see.
const srcDir = resolve(import.meta.dirname, '../src');

function sourceFiles(dir) {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.(ts|tsx|css)$/.test(entry) && !/\.test\./.test(entry) ? [path] : [];
  });
}

const defined = new Set(
  [...readFileSync(join(srcDir, 'index.css'), 'utf8').matchAll(/^\s*(--[\w-]+)\s*:/gm)].map((match) => match[1]),
);

const problems = [];
for (const file of sourceFiles(srcDir)) {
  for (const [, name, next] of readFileSync(file, 'utf8').matchAll(/var\(\s*(--[\w-]+)\s*([,)])/g)) {
    // Tailwind generates its own --tw-* helpers; a var() with a fallback degrades safely.
    if (name.startsWith('--tw-') || next === ',' || defined.has(name)) continue;
    problems.push(`${name} (${relative(srcDir, file)})`);
  }
}

if (problems.length > 0) {
  console.error('CSS variables used but not defined in src/index.css:');
  for (const problem of problems) console.error(`  ${problem}`);
  process.exit(1);
}
console.log('CSS variable validation passed.');
