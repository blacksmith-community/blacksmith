// Reads the console's own stylesheets as text. jsdom does not evaluate media
// queries or compute layout, so the stylesheet checks work on the source.
const fs = require('fs');
const path = require('path');

const UI = path.resolve(__dirname, '..', '..', 'ui');

const strip = (css) => css.replace(/\/\*[\s\S]*?\*\//g, '');

// Every stylesheet directly under ui/, which leaves out ui/vendor/.
const sheets = () => fs.readdirSync(UI).filter((f) => f.endsWith('.css')).sort();

const read = (file) => strip(fs.readFileSync(path.join(UI, file), 'utf8'));

const blockEnd = (css, open) => {
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++;
    else if (css[i] === '}' && --depth === 0) return i;
  }
  return css.length - 1;
};

// Splits a stylesheet into {at, selector, body} entries. Rules inside an
// at-rule block carry the at-rule's prelude in `at`, such as "@media (max-width: 700px)".
const rulesIn = (css, at = '') => {
  const out = [];
  let i = 0;
  while (i < css.length) {
    const open = css.indexOf('{', i);
    if (open < 0) break;
    const prelude = css.slice(i, open).trim().replace(/\s+/g, ' ');
    const end = blockEnd(css, open);
    const inner = css.slice(open + 1, end);
    if (prelude.startsWith('@')) {
      if (/^@(media|supports|layer|container)/.test(prelude)) out.push(...rulesIn(inner, prelude));
      else out.push({ at, selector: prelude, body: inner.trim() });
    } else {
      out.push({ at, selector: prelude, body: inner.trim() });
    }
    i = end + 1;
  }
  return out;
};

const rules = (file) => rulesIn(read(file));

const decl = (body, prop) => {
  const m = body.match(new RegExp('(?:^|;)\\s*' + prop + '\\s*:\\s*([^;]+)', 'i'));
  return m ? m[1].replace(/\s*!important/, '').trim() : null;
};

module.exports = { UI, strip, sheets, read, rules, rulesIn, decl };
