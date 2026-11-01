const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');

// The broker Settings screen groups the read-only configuration by its first
// key segment, filters it as the operator types, and shows each value with the
// shared value cell.

const CERT='-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n';
const config={
 bosh:{address:'https://10.0.0.6:25555',cacert:CERT,password:'s3cr3t-test'},
 broker:{port:3000,debug:false,note:''},
 credhub:{ca_cert:CERT+CERT},
 debug:true
};

async function screen(){
 const env=boot();await sleep(100);
 const host=env.d.createElement('div');env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({vault_max_versions:5},config);
 const type=(text)=>{const input=host.querySelector('input.config-filter');input.value=text;input.dispatchEvent(new env.w.Event('input',{bubbles:true}));};
 const visible=()=>[...host.querySelectorAll('.config-table tbody tr')].filter(r=>!r.hidden).map(r=>r.classList.contains('config-group')?'# '+r.querySelector('.config-group-name').textContent:r.querySelector('.config-key').textContent);
 const expandAll=()=>host.querySelector('.config-expand-all').click();
 return {...env,host,type,visible,expandAll};
}

test('the two section headings use sentence case and the read-only line explains redaction',async()=>{
 const s=await screen();
 const heads=[...s.host.querySelectorAll('.settings-section-header')].map(h=>h.textContent);
 expect(heads).toEqual(['Runtime settings','Current configuration']);
 expect(s.host.textContent).toContain('Read-only. The broker redacts secret values.');
 const css=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.css'),'utf8');
 const rule=css.match(/\.settings-section-header\s*\{([^}]*)\}/)[1];
 expect(rule).not.toMatch(/uppercase/);
 s.w.close();
});

test('rows group by the first key segment in alphabetical order with counts',async()=>{
 const s=await screen();
 s.expandAll();
 const groups=[...s.host.querySelectorAll('.config-table tr.config-group')].map(r=>[r.querySelector('.config-group-name').textContent,r.querySelector('.config-group-count').textContent]);
 expect(groups).toEqual([['bosh','3'],['broker','3'],['credhub','1'],['general','1']]);
 expect(s.visible()).toEqual(['# bosh','address','cacert','password','# broker','port','debug','note','# credhub','ca_cert','# general','debug']);
 expect(s.host.querySelector('details')).not.toBeNull();
 s.w.close();
});

test('values use the shared cell, PEMs collapse, and secrets stay masked',async()=>{
 const s=await screen();
 const row=(k,g)=>[...s.host.querySelectorAll('.config-table tbody tr:not(.config-group)')].find(r=>r.querySelector('.config-key').textContent===k&&r.dataset.group===g);
 expect(row('address','bosh').querySelector('.manifest-value .manifest-value-text').textContent).toBe('https://10.0.0.6:25555');
 expect(row('address','bosh').querySelector('button[aria-label="Copy bosh.address"]')).not.toBeNull();
 expect(row('cacert','bosh').textContent).toContain('PEM certificate · 1 certificate');
 expect(row('ca_cert','credhub').textContent).toContain('PEM certificate · 2 certificates');
 expect(row('password','bosh').textContent).toContain('••••••••');
 expect(s.host.innerHTML).not.toContain('s3cr3t-test');
 expect(row('note','broker').querySelector('.empty-value')).not.toBeNull();
 expect(row('note','broker').querySelector('button')).toBeNull();
 expect(row('debug','broker').textContent).toContain('false');
 expect(s.host.querySelector('.config-table em')).toBeNull();
 s.w.close();
});

test('the filter narrows rows by key or value and hides empty groups',async()=>{
 const s=await screen();
 const input=s.host.querySelector('input.config-filter');
 expect(s.host.querySelector(`label[for="${input.id}"]`).textContent).toBe('Filter settings');
 s.type('3000');
 expect(s.visible()).toEqual(['# broker','port']);
 s.type('CA');
 expect(s.visible()).toEqual(['# bosh','cacert','# credhub','ca_cert']);
 s.type('10.0.0.6');
 expect(s.visible()).toEqual(['# bosh','address']);
 expect(s.host.querySelector('.config-no-match').hidden).toBe(true);
 s.type('zzz');
 expect(s.visible()).toEqual([]);
 expect(s.host.querySelector('.config-no-match').hidden).toBe(false);
 expect(s.host.querySelector('.config-no-match').textContent).toBe('No settings match.');
 s.type('');
 expect(s.visible()).toEqual(['# bosh','# broker','# credhub','# general']);
 s.w.close();
});

test('the filter does not match a masked secret value',async()=>{
 const s=await screen();
 s.type('s3cr3t');
 expect(s.visible()).toEqual([]);
 s.w.close();
});

test('the Runtime settings form is unchanged',async()=>{
 const s=await screen();
 expect(s.host.querySelector('#settings-max-versions').value).toBe('5');
 expect(s.host.querySelector('#apply-max-versions').textContent).toBe('Apply');
 expect(s.host.querySelector('#apply-reconciler-interval').textContent).toBe('Apply');
 s.w.close();
});

test('the second guard masks a connection string and a list holding one',async()=>{
 const env=boot();await sleep(100);
 const host=env.d.createElement('div');env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({}, {redis:{uri:'redis://:s3cr3t-test@h:6379',peers:['amqp://u:s3cr3t-test@h','amqp://h']}});
 expect(host.innerHTML).not.toContain('s3cr3t-test');
 expect(host.querySelectorAll('.config-table button[aria-pressed]').length).toBe(2);
 env.w.close();
});

test('the Settings guard masks a secret nested inside a list of objects',async()=>{
 const env=boot();await sleep(100);
 const host=env.d.createElement('div');env.d.body.appendChild(host);
 host.innerHTML=env.w.__T.renderBlacksmithSettings({}, {protocols:{amqp:{uri:'amqp://u:s3cr3t-test@h',password:'s3cr3t-test'}},peers:[{password:'s3cr3t-test'},{host:'h'}],hosts:[{host:'h'}]});
 expect(host.innerHTML).not.toContain('s3cr3t-test');
 expect(host.querySelectorAll('.config-table button[aria-pressed]').length).toBe(3);
 env.w.close();
});

test('every group starts collapsed',async()=>{
 const s=await screen();
 expect(s.visible()).toEqual(['# bosh','# broker','# credhub','# general']);
 const toggles=[...s.host.querySelectorAll('.config-group-toggle')];
 expect(toggles.length).toBe(4);
 for(const t of toggles){expect(t.tagName).toBe('BUTTON');expect(t.getAttribute('aria-expanded')).toBe('false');}
 s.w.close();
});

test('a group toggle opens and closes its rows and sets aria-expanded',async()=>{
 const s=await screen();
 const toggle=s.host.querySelector('tr[data-group="bosh"] .config-group-toggle');
 const ids=toggle.getAttribute('aria-controls').split(' ');
 expect(ids.length).toBe(3);
 for(const id of ids)expect(s.host.querySelector('#'+id).dataset.group).toBe('bosh');
 toggle.click();
 expect(toggle.getAttribute('aria-expanded')).toBe('true');
 expect(s.visible()).toEqual(['# bosh','address','cacert','password','# broker','# credhub','# general']);
 toggle.click();
 expect(toggle.getAttribute('aria-expanded')).toBe('false');
 expect(s.visible().length).toBe(4);
 s.w.close();
});

test('Expand all and Collapse all open and close every group',async()=>{
 const s=await screen();
 s.host.querySelector('.config-expand-all').click();
 expect(s.visible().length).toBe(12);
 expect([...s.host.querySelectorAll('.config-group-toggle')].every(t=>t.getAttribute('aria-expanded')==='true')).toBe(true);
 s.host.querySelector('.config-collapse-all').click();
 expect(s.visible().length).toBe(4);
 s.w.close();
});

test('the filter opens matching groups and clearing it restores the earlier choices',async()=>{
 const s=await screen();
 s.host.querySelector('tr[data-group="broker"] .config-group-toggle').click();
 s.type('ca');
 expect(s.visible()).toEqual(['# bosh','cacert','# credhub','ca_cert']);
 expect(s.host.querySelector('tr[data-group="bosh"] .config-group-toggle').getAttribute('aria-expanded')).toBe('true');
 s.type('');
 expect(s.visible()).toEqual(['# bosh','# broker','port','debug','note','# credhub','# general']);
 expect(s.host.querySelector('tr[data-group="bosh"] .config-group-toggle').getAttribute('aria-expanded')).toBe('false');
 s.w.close();
});

test('the filter, Expand all and Collapse all share one toolbar line',async()=>{
 const s=await screen();
 const bar=s.host.querySelector('.config-toolbar');
 expect(bar.querySelector('input.config-filter')).not.toBeNull();
 expect(bar.querySelector('.table-toolbar-actions .config-expand-all')).not.toBeNull();
 expect(bar.querySelector('.table-toolbar-actions .config-collapse-all')).not.toBeNull();
 expect(s.host.querySelector('.settings-section-heading .settings-hint').textContent).toBe('Read-only. The broker redacts secret values.');
 s.w.close();
});

test('an empty feedback span stays in the DOM and takes no space',async()=>{
 const s=await screen();
 const fb=s.host.querySelector('#feedback-max-versions');
 expect(fb.getAttribute('role')).toBe('status');
 expect(fb.textContent).toBe('');
 const css=fs.readFileSync(path.join(__dirname,'../../ui/forge-theme.css'),'utf8');
 expect(css).toMatch(/\.settings-feedback:empty\s*\{[^}]*position:\s*absolute[^}]*clip-path:\s*inset\(50%\)/);
 expect(css).not.toMatch(/\.settings-feedback:empty\s*\{[^}]*display:\s*none/);
 expect(css).toMatch(/\.blacksmith-settings \.settings-apply-btn\s*\{[^}]*min-height:\s*28px/);
 expect(css).toMatch(/\.blacksmith-settings \.settings-input\s*\{[^}]*padding-block:\s*3px/);
 expect(css).toMatch(/\.settings-row:last-of-type\s*\{[^}]*margin-bottom:\s*0/);
 const layout=fs.readFileSync(path.join(__dirname,'../../ui/inspection-layout.css'),'utf8');
 expect(layout).toMatch(/^#blacksmith \.blacksmith-settings \.settings-row\s*\{\s*align-items:\s*center\s*;?\s*\}/m);
 expect(layout).not.toMatch(/\.settings-row\s*\{[^}]*align-items:\s*start/);
 expect(css).toMatch(/\.config-group-toggle:focus-visible\s*\{\s*outline-offset:\s*-2px/);
 expect(css.match(/\.blacksmith-settings \.settings-feedback\s*\{([^}]*)\}/)[1]).toMatch(/min-height:\s*0/);
 s.w.close();
});

test('the settings table is dense and its copy target is 24 pixels, scoped to the config container',()=>{
 const css=fs.readFileSync(path.join(__dirname,'../../ui/forge-theme.css'),'utf8');
 expect(css).toMatch(/\.config-container \.config-table :is\(th, td\)\s*\{[^}]*padding-top:\s*4px;\s*padding-bottom:\s*4px/);
 expect(css).toMatch(/\.config-container \.config-table \.copy-btn-inline\s*\{[^}]*width:\s*24px;\s*height:\s*24px/);
 expect(css).not.toMatch(/^\.copy-btn-inline\s*\{[^}]*height:\s*2[0-3]px/m);
 expect(css).not.toMatch(/\.manifest[^{]*\{[^}]*padding-top:\s*4px/);
});

test('a group toggle works while filtering and clearing restores the saved state',async()=>{
 const s=await screen();
 s.host.querySelector('tr[data-group="broker"] .config-group-toggle').click();
 s.type('ca');
 s.host.querySelector('tr[data-group="bosh"] .config-group-toggle').click();
 expect(s.visible()).toEqual(['# bosh','# credhub','ca_cert']);
 s.host.querySelector('tr[data-group="bosh"] .config-group-toggle').click();
 expect(s.visible()).toEqual(['# bosh','cacert','# credhub','ca_cert']);
 s.host.querySelector('tr[data-group="bosh"] .config-group-toggle').click();
 s.type('');
 expect(s.visible()).toEqual(['# bosh','# broker','port','debug','note','# credhub','# general']);
 s.w.close();
});

test('Collapse all while filtering leaves the saved choices intact',async()=>{
 const s=await screen();
 s.host.querySelector('tr[data-group="broker"] .config-group-toggle').click();
 s.type('ca');
 s.host.querySelector('.config-collapse-all').click();
 expect(s.visible()).toEqual(['# bosh','# credhub']);
 s.host.querySelector('.config-expand-all').click();
 expect(s.visible()).toEqual(['# bosh','cacert','# credhub','ca_cert']);
 s.host.querySelector('.config-collapse-all').click();
 s.type('');
 expect(s.visible()).toEqual(['# bosh','# broker','port','debug','note','# credhub','# general']);
 s.w.close();
});
