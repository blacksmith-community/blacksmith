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
    expect(destinations(screens, null)).toHaveLength(16);
    expect(destinations([], { gone: { deleted: true } })[0].context).toContain('Deleted');
  });
  test('includes catalog plan screens and familiar navigation aliases', () => {
    const entries = destinations(screens, {}, { services: [{ id: 'valkey', name: 'Valkey', plans: [{ id: 'small', name: 'Small cache' }] }] });
    expect(search(entries, 'valkey small')[0].planId).toBe('small');
    expect(search(entries, 'upgrade')[0].screen).toBe('upgrade');
  });
});

test('theme modes are searchable actions with clear locations', () => {
 const entries=destinations(screens,{});
 expect(search(entries,'theme').map(e=>e.theme)).toEqual(['light','dark','high-contrast-light','high-contrast-dark','system']);
 expect(search(entries,'theme system')[0].label).toBe('System');
 expect(entries.find(e=>e.section==='configs').breadcrumb).toBe('Blacksmith > BOSH Configs');
});

test('operation prefixes lead to batch options shortcuts',()=>{
 const entries=destinations(screens,{});
 for(const [query,operation] of [['upg','upgrade'],['conv','converge'],['rot','rotate']]){
  const result=search(entries,query)[0];
  expect(result.operation).toBe(operation);
  expect(result.breadcrumb).toBe('Batch Operations > Options');
 }
});

test('rotation completions distinguish unconverged from converge prefixes',()=>{
 const entries=destinations(screens,{});
 expect(search(entries,'rot pat un').map(e=>e.rotation)).toEqual([{mode:'patch',converge:'pending'}]);
 expect(search(entries,'rot pa con').map(e=>e.rotation)).toEqual([{mode:'patch',converge:'immediate'}]);
 expect(search(entries,'rot red')[0].rotation).toEqual({mode:'redeploy'});
 expect(search(entries,'rot pat')[0].rotation).toEqual({mode:'patch'});
});
