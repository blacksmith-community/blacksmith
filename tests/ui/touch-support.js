// Reads a stylesheet from ui/ and returns the rules found inside media blocks
// whose condition mentions a coarse pointer, as {selector, body} pairs. The
// checks that use it read the CSS text, because jsdom does not evaluate media
// queries or compute layout.
const fs = require('fs');
const path = require('path');

const UI = path.resolve(__dirname, '..', '..', 'ui');

const strip = (css) => css.replace(/\/\*[\s\S]*?\*\//g, '');

const blockAt = (css, open) => {
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++;
    else if (css[i] === '}' && --depth === 0) return css.slice(open + 1, i);
  }
  return css.slice(open + 1);
};

const rulesOf = (css) => {
  const rules = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(css))) rules.push({ selector: m[1].trim().replace(/\s+/g, ' '), body: m[2].trim() });
  return rules;
};

const mediaRules = (file, test) => {
  const css = strip(fs.readFileSync(path.join(UI, file), 'utf8'));
  const out = [];
  const re = /@media([^{]*)\{/g;
  let m;
  while ((m = re.exec(css))) {
    const inner = blockAt(css, m.index + m[0].length - 1);
    if (test(m[1])) out.push(...rulesOf(inner));
    re.lastIndex = m.index + m[0].length + inner.length;
  }
  return out;
};

const coarseRules = (file) => mediaRules(file, (cond) => /pointer:\s*coarse/.test(cond));
const narrowRules = (file) => mediaRules(file, (cond) => /max-width:\s*640px/.test(cond));

const decl = (body, prop) => {
  const m = body.match(new RegExp('(?:^|;)\\s*' + prop + '\\s*:\\s*([^;]+)', 'i'));
  return m ? m[1].replace(/\s*!important/, '').trim() : null;
};

module.exports = { UI, strip, coarseRules, narrowRules, decl };
