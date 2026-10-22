import { describe, test, expect } from 'bun:test';
import '../../ui/navigation.js';

const { destinations, search } = globalThis.BlacksmithNavigation;
const screens = [{ id: 'blacksmith', label: 'Blacksmith', sections: [{ id: 'configs', label: 'BOSH Configs' }] }, { id: 'services', label: 'Service Instances' }, { id: 'upgrade', label: 'Batch Operations' }];

describe('navigation destinations', () => {
  test('covers main screens, sections and every instance detail screen', () => {
    const entries = destinations(screens, { 'cache-1': { instance_name: 'Orders cache', service_id: 'valkey' } });
    expect(entries.filter(e => e.instanceId === 'cache-1')).toHaveLength(8);
    expect(entries.some(e => e.screen === 'upgrade')).toBe(true);
    expect(entries.some(e => e.section === 'configs')).toBe(true);
    expect(search(entries, 'orders credentials')[0].section).toBe('credentials');
    expect(search(entries, 'cache-1 vms')[0].instanceId).toBe('cache-1');
  });
  test('ranks exact and prefix labels above contextual matches', () => {
    const entries = [{ label: 'Events', context: 'Blacksmith' }, { label: 'Details', context: 'Events service' }, { label: 'Events log', context: '' }];
    expect(search(entries, 'events').map(e => e.label)).toEqual(['Events', 'Events log', 'Details']);
  });
  test('matches all words, case insensitively, without interpreting regex or HTML', () => {
    const entries = destinations(screens, { '[x]': { instance_name: '<img onerror=alert(1)>', service_id: 'valkey' } });
    expect(search(entries, '  VALKEY   [x] ')).toHaveLength(8);
    expect(search(entries, 'missing')).toEqual([]);
    expect(search(entries, '<img')[0].context).toContain('<img');
  });
  test('empty search prioritizes screens and never indexes secrets', () => {
    const entries = destinations(screens, { abc: { instance_name: 'Cache', credentials: { password: 'SECRET' } } });
    expect(search(entries, '')[0].screen).toBe('blacksmith');
    expect(search(entries, 'SECRET')).toEqual([]);
    expect(entries.every(e => !('action' in e))).toBe(true);
  });
  test('supports an unavailable catalog and deleted instance context', () => {
    expect(destinations(screens, null)).toHaveLength(4);
    expect(destinations([], { gone: { deleted: true } })[0].context).toContain('Deleted');
  });
  test('includes catalog plan screens and familiar navigation aliases', () => {
    const entries = destinations(screens, {}, { services: [{ id: 'valkey', name: 'Valkey', plans: [{ id: 'small', name: 'Small cache' }] }] });
    expect(search(entries, 'valkey small')[0].planId).toBe('small');
    expect(search(entries, 'upgrade')[0].screen).toBe('upgrade');
  });
});
