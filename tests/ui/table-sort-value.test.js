const { test, expect, afterEach } = require('bun:test');
const H = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');

const t = JSDOM ? test : test.skip;
const { boot } = H.main;

const envs = [];
afterEach(() => { envs.splice(0).forEach((e) => { expect(e.alerts).toEqual([]); e.w.close(); }); });

// A table whose Expires text sorts wrongly as plain text.
const build = () => {
  const env = boot({ noDialogs: true });
  envs.push(env);
  const { d, w } = env;
  const table = d.createElement('table');
  const rows = [
    ['b', '2 Jan 2027', Date.parse('2027-01-02T00:00:00Z')],
    ['c', '11 Oct 2027', Date.parse('2027-10-11T00:00:00Z')],
    ['a', '9 Mar 2026', Date.parse('2026-03-09T00:00:00Z')],
    ['u', 'Unknown', null],
    ['e', '', null],
  ];
  table.innerHTML = '<thead><tr><th>Name</th><th>Expires</th></tr></thead><tbody></tbody>';
  rows.forEach(([name, text, value]) => {
    const tr = d.createElement('tr');
    const first = d.createElement('td'); first.textContent = name;
    const second = d.createElement('td'); second.textContent = text;
    if (value !== null) second.setAttribute('data-sort-value', String(value));
    tr.append(first, second);
    table.querySelector('tbody').appendChild(tr);
  });
  d.body.appendChild(table);
  w.__T.initializeAllTables();
  return { d, table };
};
const order = (table) => [...table.querySelectorAll('tbody tr')].map((r) => r.cells[0].textContent).join('');

t('the sorter orders dates by data-sort-value, then empty, then Unknown when ascending', () => {
  const { table } = build();
  const header = table.querySelectorAll('th')[1];
  header.click();
  expect(order(table)).toBe('abceu');
  header.click();
  expect(order(table)).toBe('uecba');
});
