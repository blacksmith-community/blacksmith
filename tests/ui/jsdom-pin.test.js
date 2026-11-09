const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');

// jsdom 30 and later fail the keyboard and focus suites under bun 1.4.4, so
// every place that tells a person how to install jsdom names jsdom@26.
test('every jsdom install hint pins jsdom 26', () => {
  const files = ['README.md', 'certificates/support.js'].map((f) => path.join(__dirname, f));
  files.forEach((file) => {
    const hints = fs.readFileSync(file, 'utf8').match(/bun add --no-save jsdom[^\s`]*/g) || [];
    expect(hints.length).toBeGreaterThan(0);
    hints.forEach((hint) => expect(hint).toBe('bun add --no-save jsdom@26'));
  });
});
