const { test, expect } = require('bun:test');
const fs = require('fs');
const path = require('path');
const { main: {boot} } = require('./certificates/harness.js');
const { JSDOM } = require('./certificates/support.js');
const ui = path.resolve(__dirname, '../../ui');
const t = JSDOM ? test : test.skip;

t('broker overview distinguishes known context from unverified health', () => {
  const {w,d} = boot();
  try {
    const data = {deployment:'lab <img src=x>',env:'Forge lab',instances:{a:{},b:{deleted:true}},plans:{small:{}}};
    const host=d.createElement('div');
    host.innerHTML=w.__T.renderBlacksmithOverview(data);
    expect(host.querySelectorAll('.entity-metric')).toHaveLength(3);
    expect(host.textContent).toContain('1 active instance');
    expect(host.textContent).not.toContain('Running');
    expect(host.querySelector('img')).toBeNull();
    expect(host.textContent).toContain('lab <img src=x>');
    expect(host.querySelector('details').open).toBe(false);
  } finally { w.close(); }
});

t('Blacksmith and instance navigation retain all destinations in named groups', () => {
  const {w,d}=boot();
  try {
    const broker=d.createElement('div');broker.innerHTML=w.__T.renderBlacksmithTemplate({});
    const instance=d.createElement('div');instance.innerHTML=w.__T.renderServiceDetail('id-1',{service_id:'valkey',plan_id:'cluster'},null);
    expect(broker.querySelectorAll('.detail-tab')).toHaveLength(11);
    expect(instance.querySelectorAll('.detail-tab')).toHaveLength(8);
    for (const host of [broker,instance]) {
      expect(host.querySelector('.entity-section-group').getAttribute('role')).toBe('presentation');
      expect(host.querySelector('.detail-tab').getAttribute('aria-describedby')).toMatch(/^entity-group-/);
      expect(host.querySelector('[data-tab="details"]').textContent).toBe('Summary');
      expect(host.querySelector('.entity-section-select').getAttribute('aria-label')).toBe('View section');
      expect(host.querySelector('.service-detail-header h2')).not.toBeNull();
    }
    const overview=d.createElement('div');overview.innerHTML=w.serviceInstanceDetailsContent['id-1'];
    expect(overview.textContent).toContain('Not reported');
    expect(overview.querySelector('details').open).toBe(false);
    expect(overview.textContent).toContain('cluster');
  } finally { w.close(); }
});

t('explicit light and dark choices control the Oat color scheme', () => {
  const {w,d}=boot();
  try {
    w.__T.setTheme('light');
    expect(d.documentElement.style.colorScheme).toBe('light');
    w.__T.setTheme('dark');
    expect(d.documentElement.style.colorScheme).toBe('dark');
    expect(d.getElementById('themeToggle').getAttribute('aria-label')).toBe('Choose appearance theme');
    expect(d.getElementById('themeToggle').getAttribute('aria-haspopup')).toBe('dialog');
  } finally { w.close(); }
});

test('the complete UI loads vendored Oat without Tailwind assets or utility variants', () => {
  const html=fs.readFileSync(path.join(ui,'index.html'),'utf8');
  expect(html).toContain('/vendor/oat/oat.min.css');
  expect(html).toContain('/vendor/oat/oat.min.js');
  expect(html).not.toMatch(/tailwind/i);
  expect(fs.existsSync(path.join(ui,'tailwind.css'))).toBe(false);
  expect(fs.existsSync(path.join(ui,'tailwind.min.js'))).toBe(false);
  for(const name of ['index.html','blacksmith.js','navigation.js']) {
    expect(fs.readFileSync(path.join(ui,name),'utf8')).not.toMatch(/(?:dark:|hover:|focus:)(?:bg-|text-|border-|ring-|outline-)/);
  }
});

const luminance=hex=>{
  const rgb=hex.replace('#','').match(/../g).map(s=>parseInt(s,16)/255).map(v=>v<=0.04045?v/12.92:((v+.055)/1.055)**2.4);
  return .2126*rgb[0]+.7152*rgb[1]+.0722*rgb[2];
};
const contrast=(a,b)=>(Math.max(luminance(a),luminance(b))+.05)/(Math.min(luminance(a),luminance(b))+.05);
test('forge reading and control palettes meet contrast targets in both themes',()=>{
  const css=fs.readFileSync(path.join(ui,'forge-theme.css'),'utf8');
  for(const selector of [':root','[data-theme="dark"]']) {
    const block=css.slice(css.indexOf(selector)).match(/\{([^}]+)\}/)[1];
    const raw=Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[\da-f]{6}|var\(--[\w-]+\))/gi)].map(m=>[m[1],m[2]]));
    const settle=v=>v.startsWith('var(')?settle(raw[v.slice(6,-1)]):v;
    const vars=Object.fromEntries(Object.entries(raw).map(([k,v])=>[k,settle(v)]));
    for(const bg of ['background','card','secondary']) {
      expect(contrast(vars.foreground,vars[bg])).toBeGreaterThanOrEqual(7);
      expect(contrast(vars['muted-foreground'],vars[bg])).toBeGreaterThanOrEqual(4.5);
    }
    expect(contrast(vars.primary,vars['primary-foreground'])).toBeGreaterThanOrEqual(4.5);
    expect(contrast(vars.ring,vars.background)).toBeGreaterThanOrEqual(3);
    expect(contrast(vars.input,vars.card)).toBeGreaterThanOrEqual(3);
  }
});

 test('install metadata provides matching platform icons without caching live state', () => {
 const manifest=JSON.parse(fs.readFileSync(path.join(ui,'manifest.webmanifest'),'utf8'));
 expect(manifest.name).toBe('Blacksmith Management Console');
 expect(manifest.start_url).toBe('/');
 for (const size of ['192x192','512x512']) {
   expect(manifest.icons.some(i=>i.sizes===size && i.purpose==='any')).toBe(true);
   expect(manifest.icons.some(i=>i.sizes===size && i.purpose==='maskable')).toBe(true);
 }
 for (const icon of manifest.icons) expect(fs.existsSync(path.join(ui,icon.src))).toBe(true);
 const html=fs.readFileSync(path.join(ui,'index.html'),'utf8');
 expect(html).toContain('rel="apple-touch-icon"');
 expect(html).toContain('rel="manifest"');
});

t('instance list escapes operator-controlled labels', () => {
 const {w,d}=boot();
 try {
 const host=d.createElement('div');
 host.innerHTML=w.__T.renderServicesTemplate({'hostile"id':{service_id:'<img src=x>',instance_name:'<img src=x>',plan_id:'small',vm_status:'<img src=x>'}});
 expect(host.querySelector('img')).toBeNull();
 expect(host.querySelector('.service-item').dataset.instanceId).toBe('hostile"id');
 expect(host.querySelector('#instance-search')).not.toBeNull();
 expect(host.querySelector('#instance-next')).not.toBeNull();
 } finally {w.close();}
});

t('registration dialog opens as a native modal and closes again',()=>{
 const {w,d}=boot();
 try {
 w.__T.CFRegistrationManager.showRegistrationModal();
 const modal=d.getElementById('registration-modal');
 expect(modal.open).toBe(true);
 expect(modal.hasAttribute('aria-hidden')).toBe(false);
 w.__T.CFRegistrationManager.hideRegistrationModal();
 expect(modal.open).toBe(false);
 } finally {w.close();}
});

t('confirmation isolates the background and restores prior inert state',async()=>{
 const {w,d}=boot();
 try {
 await new Promise(resolve=>setTimeout(resolve,50));
 const main=d.querySelector('main'); const header=d.querySelector('body > header');
 header.setAttribute('inert','');
 const result=w.__T.showConfirmDialog({title:'Review',message:'Confirm this change'});
 expect(main.hasAttribute('inert')).toBe(true);
 d.querySelector('[data-dialog-role="cancel"]').click();
 expect(await result).toBe(false);
 expect(main.hasAttribute('inert')).toBe(false);
 expect(header.hasAttribute('inert')).toBe(true);
 }finally{w.close();}
});

test('the tabs sit at the top with the page title kept for assistive tech', () => {
  const html = fs.readFileSync(path.join(ui, 'index.html'), 'utf8');
  expect(html).not.toContain('Manage services, inspect deployments, and monitor batch operations.');
  expect(html).not.toContain('console-heading');
  expect(html).toMatch(/<h1 id="console-title" class="visually-hidden">Blacksmith<\/h1>\s*<!-- Tab navigation -->/);
});

t('the broker header is one line of environment and deployment', () => {
  const {w,d}=boot();
  try {
    const host=d.createElement('div');
    host.innerHTML=w.__T.renderBlacksmithTemplate({env:'ocfp-lab-wayneeseguin-ocf',deployment:'ocfp-lab-wayneeseguin-ocf-blacksmith'});
    const header=host.querySelector('.entity-header');
    expect(header.querySelector('h2').textContent).toBe('ocfp-lab-wayneeseguin-ocf · ocfp-lab-wayneeseguin-ocf-blacksmith');
    expect(header.querySelector('.entity-eyebrow')).toBeNull();
    expect(header.querySelector('.entity-context-line')).toBeNull();
    expect(header.textContent).not.toMatch(/blacksmith broker/i);
    host.innerHTML=w.__T.renderBlacksmithTemplate({deployment:'only-deployment'});
    expect(host.querySelector('.entity-header h2').textContent).toBe('only-deployment');
    host.innerHTML=w.__T.renderBlacksmithTemplate({});
    expect(host.querySelector('.entity-header h2').textContent).toBe('Blacksmith');
    host.innerHTML=w.__T.renderServiceDetail('id-1',{service_id:'valkey',plan_id:'cluster'},null);
    expect(host.querySelector('.entity-eyebrow')).toBeNull();
    expect(host.querySelector('.entity-context-line')).not.toBeNull();
  } finally { w.close(); }
});

test('the broker tab is named Blacksmith Broker and the instance list has no visible heading', () => {
  const html = fs.readFileSync(path.join(ui, 'index.html'), 'utf8');
  expect(html).toMatch(/data-tab="blacksmith"[^>]*>\s*Blacksmith Broker<\/button>/);
  const js = fs.readFileSync(path.join(ui, 'blacksmith.js'), 'utf8');
  expect(js).not.toContain('<h2>Service Instances</h2>');
  expect(js).toContain('<div class="services-list" role="region" aria-label="Service instances">');
  for (const css of ['blacksmith.css', 'console.css']) {
    expect(fs.readFileSync(path.join(ui, css), 'utf8')).not.toContain('services-list h2');
  }
});

test('high contrast palettes meet stronger text and visible boundary targets',()=>{
 const css=fs.readFileSync(path.join(ui,'forge-theme.css'),'utf8');
 for(const selector of [':root[data-contrast="high"] {',':root[data-contrast="high"][data-theme="dark"] {']){
  const block=css.slice(css.indexOf(selector)).match(/\{([^}]+)\}/)[1];
  const raw=Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[\da-f]{6}|var\(--[\w-]+\))/gi)].map(m=>[m[1],m[2]]));
    const settle=v=>v.startsWith('var(')?settle(raw[v.slice(6,-1)]):v;
    const vars=Object.fromEntries(Object.entries(raw).map(([k,v])=>[k,settle(v)]));
  for(const bg of ['background','card','secondary']){
   for(const text of ['foreground','muted-foreground','primary']) expect(contrast(vars[text],vars[bg])).toBeGreaterThanOrEqual(7);
   expect(contrast(vars.border,vars[bg])).toBeGreaterThanOrEqual(3);
  }
  for(const color of ['primary','danger','success','warning']) expect(contrast(vars[color],vars[color+'-foreground'])).toBeGreaterThanOrEqual(7);
 }
});

t('instance identity omits redundant eyebrow and keeps context beside the name',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderServiceDetail('id-1',{instance_name:'Cache <one>',service_id:'valkey',plan_id:'cluster'},null);
 expect(host.querySelector('.entity-eyebrow')).toBeNull();
 expect(host.querySelector('.entity-identity h2').textContent).toBe('Cache <one>');
 expect(host.querySelector('.entity-identity .entity-context-line').textContent).toBe('valkey / cluster');
 expect(host.querySelector('[data-tab="testing"]').textContent).toBe('Testing');
 expect(host.querySelector('[data-tab="details"]').textContent).toBe('Summary');
 }finally{w.close();}
});
