import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const themeCss = readFileSync(join(root, 'frontend/src/theme.css'), 'utf8');
const stylesCss = readFileSync(join(root, 'frontend/src/styles.css'), 'utf8');
const indexHtml = readFileSync(join(root, 'frontend/index.html'), 'utf8');

function parseBlock(css, selector) {
  const re = new RegExp(`${selector.replace('.', '\\.')}\\s*\\{([\\s\\S]*?)\\n\\}`);
  const match = css.match(re);
  assert.ok(match, `missing ${selector} block`);
  const vars = {};
  for (const line of match[1].split('\n')) {
    const item = line.match(/^\s*(--[A-Za-z0-9-]+)\s*:\s*(.*?);?\s*$/);
    if (item) vars[item[1]] = item[2].trim().replace(/;$/, '');
  }
  return vars;
}

function resolve(vars, value, seen = new Set()) {
  const trimmed = value.trim();
  const ref = trimmed.match(/^var\((--[A-Za-z0-9-]+)\)\s*$/);
  if (!ref) return trimmed;
  assert.ok(!seen.has(ref[1]), `cycle at ${ref[1]}`);
  assert.ok(vars[ref[1]], `unresolved ${ref[1]}`);
  seen.add(ref[1]);
  return resolve(vars, vars[ref[1]], seen);
}

function parseColor(value) {
  const hex = value.trim().match(/^#([0-9a-fA-F]{3,8})$/);
  if (hex) {
    let h = hex[1];
    if (h.length === 3 || h.length === 4) h = [...h].map((c) => c + c).join('');
    const n = parseInt(h.slice(0, 6), 16);
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255, alpha: h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1 };
  }
  const rgb = value.trim().match(/^rgba?\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/);
  if (rgb) {
    const alpha = rgb[4] == null ? 1 : rgb[4].endsWith('%') ? parseFloat(rgb[4]) / 100 : parseFloat(rgb[4]);
    return { r: Number(rgb[1]), g: Number(rgb[2]), b: Number(rgb[3]), alpha };
  }
  return null;
}

function linearize(channel) {
  const c = channel / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance({ r, g, b }) {
  return 0.2126 * linearize(r) + 0.7152 * linearize(g) + 0.0722 * linearize(b);
}

function contrastRatio(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

function tokenColor(vars, name) {
  const raw = vars[name];
  assert.ok(raw, `missing token ${name}`);
  const color = parseColor(resolve(vars, raw));
  assert.ok(color && color.alpha === 1, `${name} is not an opaque color: ${raw}`);
  return color;
}

function reportPair(rows, scheme, vars, fg, bg, min = 4.5) {
  const ratio = contrastRatio(tokenColor(vars, fg), tokenColor(vars, bg));
  rows.push({ scheme, foreground: fg, background: bg, ratio: Number(ratio.toFixed(2)), minimum: min, pass: ratio + 1e-9 >= min });
  return ratio;
}

const textPairs = [
  ['--foreground', '--background'],
  ['--card-foreground', '--card'],
  ['--popover-foreground', '--popover'],
  ['--secondary-foreground', '--secondary'],
  ['--muted-foreground', '--background'],
  ['--muted-foreground', '--card'],
  ['--muted-foreground', '--popover'],
  ['--muted-foreground', '--muted'],
  ['--muted-foreground', '--secondary'],
  ['--muted-foreground', '--accent'],
  ['--muted-foreground', '--primary-surface'],
  ['--muted-foreground', '--sidebar'],
  ['--sidebar-foreground', '--sidebar'],
  ['--primary-foreground', '--primary'],
  ['--accent-foreground', '--accent'],
  ['--primary', '--background'],
  ['--primary', '--card'],
  ['--primary', '--primary-surface'],
  ['--destructive', '--background'],
  ['--destructive', '--card'],
  ['--destructive-foreground', '--destructive'],
  ['--positive', '--positive-bg'],
  ['--positive', '--card'],
  ['--warning', '--warning-bg'],
  ['--warning', '--card'],
  ['--hero-foreground', '--hero-from'],
  ['--hero-foreground', '--hero-to'],
  ['--hero-muted', '--hero-from'],
  ['--hero-muted', '--hero-to'],
  ['--hero-accent', '--hero-from'],
  ['--hero-accent', '--hero-to'],
  ...[0, 1, 2, 3, 4].flatMap((i) => [[`--site-tone-${i}-fg`, `--site-tone-${i}-bg`]]),
];

const uiPairs = [
  ['--input', '--background', 3],
  ['--input', '--card', 3],
  ['--input', '--popover', 3],
  ['--ring', '--background', 3],
  ['--ring', '--card', 3],
  ['--sidebar-ring', '--sidebar', 3],
];

const light = parseBlock(themeCss, ':root');
const dark = parseBlock(themeCss, '.dark');
const contrastRows = [];
for (const [scheme, vars] of [['light', light], ['dark', dark]]) {
  for (const [fg, bg] of textPairs) reportPair(contrastRows, scheme, vars, fg, bg, 4.5);
  for (const [fg, bg, min] of uiPairs) reportPair(contrastRows, scheme, vars, fg, bg, min);
}

test('semantic tokens keep AA text contrast in light and dark', () => {
  const failed = contrastRows.filter((row) => !row.pass);
  assert.deepEqual(failed, [], failed.map((row) => `${row.scheme} ${row.foreground} on ${row.background}: ${row.ratio} < ${row.minimum}`).join('; '));
});

test('styles.css @theme maps popover, ring, and sidebar foreground to the matching variables', () => {
  const theme = stylesCss.match(/@theme inline \{([\s\S]*?)\n\}/);
  assert.ok(theme, 'missing @theme inline block');
  const maps = Object.fromEntries([...theme[1].matchAll(/^\s*(--[A-Za-z0-9-]+)\s*:\s*(.*?);$/gm)].map((m) => [m[1], m[2].trim()]));
  const expected = {
    '--color-popover': 'var(--popover)',
    '--color-popover-foreground': 'var(--popover-foreground)',
    '--color-ring': 'var(--ring)',
    '--color-card-foreground': 'var(--card-foreground)',
    '--color-secondary-foreground': 'var(--secondary-foreground)',
    '--color-accent-foreground': 'var(--accent-foreground)',
    '--color-sidebar-foreground': 'var(--sidebar-foreground)',
    '--color-sidebar-ring': 'var(--sidebar-ring)',
    '--color-chart-1': 'var(--chart-1)',
  };
  for (const [name, value] of Object.entries(expected)) {
    assert.equal(maps[name], value, `${name} should be ${value}`);
  }
});

test('component styles use semantic tokens instead of raw palette colors', () => {
  const stripped = stylesCss.replace(/\/\*[\s\S]*?\*\//g, '');
  const raw = stripped.match(/#(?:[0-9a-fA-F]{3,8})\b|rgba?\([^)]*\)|hsla?\([^)]*\)/g) || [];
  assert.deepEqual(raw, [], `raw colors remain in styles.css: ${raw.join(', ')}`);
  const banned = ['#ecefff', '#6a73c9', '#f0eefa', '#9a77b9', '#303852', '#3e4771', '#b5baff', '#f6f7ff', '#b4beda', '#c1c8df', '#b6bde7', '#5861dc', '#9a9ffd', '#edeffd'];
  for (const color of banned) {
    assert.equal(stripped.toLowerCase().includes(color), false, `legacy purple ${color} still present`);
  }
  assert.match(stripped, /outline:\s*2px solid var\(--ring\)/);
  assert.match(stripped, /@media\s*\(prefers-reduced-motion:\s*reduce\)/);
  assert.match(stripped, /background:\s*var\(--site-tone-0-bg\)/);
  assert.match(stripped, /background:\s*linear-gradient\(120deg,\s*var\(--hero-from\),\s*var\(--hero-to\)\)/);
});

test('document theme-color matches the light background token', () => {
  const color = indexHtml.match(/name="theme-color"\s+content="([^"]+)"/);
  assert.ok(color, 'missing theme-color meta');
  assert.equal(color[1].toLowerCase(), resolve(light, light['--background']).toLowerCase());
});

test('contrast evidence is available without a machine-specific output path', () => {
  assert.ok(contrastRows.length > 0);
  const output = process.env.THEME_CONTRAST_REPORT;
  if (output) {
    mkdirSync(dirname(output), { recursive: true });
    writeFileSync(output, `${JSON.stringify({ generated: 'theme-contrast.test.js', rows: contrastRows }, null, 2)}\n`);
  }
});
