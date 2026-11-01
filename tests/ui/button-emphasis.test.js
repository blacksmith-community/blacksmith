const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: { boot } } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const t = JSDOM ? test : test.skip;

const UI = path.resolve(__dirname, '..', '..', 'ui');

t('on Endpoints only the empty state Connect is primary', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    d.body.appendChild(host);
    host.innerHTML = w.__T.renderBrokerTab();
    expect(host.querySelector('#register-btn').classList.contains('btn-primary')).toBe(false);
    expect(host.querySelector('#register-btn').classList.contains('btn-secondary')).toBe(true);
    ['Refresh endpoints', 'Add endpoint'].forEach((label) => {
      const button = host.querySelector(`button[aria-label="${label}"]`);
      expect(button.classList.contains('btn-secondary')).toBe(true);
      expect(button.classList.contains('btn-primary')).toBe(false);
    });
    // Nothing in the toolbar offers Connect before a connection exists.
    const toolbarButton = host.querySelector('#connection-btn');
    expect(toolbarButton.hidden).toBe(true);
    expect(toolbarButton.classList.contains('btn-primary')).toBe(false);
  } finally { w.close(); }
});

t('the toolbar button appears only to disconnect', () => {
  const { w, d } = boot();
  try {
    const host = d.createElement('div');
    d.body.appendChild(host);
    host.innerHTML = w.__T.renderBrokerTab();
    const manager = w.CFEndpointManager;
    manager.endpoints = { ocf: { name: 'ocf' } };
    manager.selectedEndpoint = 'ocf';
    const button = host.querySelector('#connection-btn');
    manager.connectionStates = { ocf: { connected: true, testing: false } };
    manager.updateConnectionStatus('ocf');
    expect(button.hidden).toBe(false);
    expect(button.textContent).toBe('Disconnect');
    manager.connectionStates = { ocf: { connected: false, testing: false } };
    manager.updateConnectionStatus('ocf');
    expect(button.hidden).toBe(true);
    expect(host.querySelector('#connection-status-text').textContent).toBe('Not connected');
  } finally { w.close(); }
});

t('a BOSH task is an ordinary link that reads BOSH task and the number', () => {
  const src = fs.readFileSync(path.join(UI, 'blacksmith.js'), 'utf8');
  expect(src).toContain('<a href="#" class="bosh-task-link" data-bosh-task-id="${boshTaskId}">BOSH task ${boshTaskId}</a>');
  expect(src).not.toContain('bosh-task-btn');
  expect(src).not.toContain('BOSH Task:');
  const css = fs.readFileSync(path.join(UI, 'blacksmith.css'), 'utf8');
  expect(css).not.toContain('bosh-task-btn');
});
