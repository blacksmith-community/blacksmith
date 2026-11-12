const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const css=fs.readFileSync(path.resolve(__dirname,'..','..','ui','blacksmith.css'),'utf8');
const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const PEM='-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----';
const html=async(fn)=>{
 const env=boot();envs.push(env);
 await sleep(40);
 env.d.body.innerHTML=fn(env.w.__T);
 return env;
};
const firstKid=(el)=>el.firstElementChild;

t('Broker settings values lead with the copy button',async()=>{
 const {d}=await html((T)=>T.formatConfig({server:{port:8443,name:'broker'}}));
 const cells=[...d.querySelectorAll('.config-table td.config-value')];
 expect(cells.length).toBe(2);
 for(const cell of cells){
  const box=cell.querySelector('.manifest-value');
  expect(box.firstElementChild.tagName).toBe('BUTTON');
  expect(box.firstElementChild.classList.contains('copy-btn-inline')).toBe(true);
  expect(box.lastElementChild.classList.contains('manifest-value-text')).toBe(true);
 }
});

t('an empty Broker setting keeps an empty slot where the button would be',async()=>{
 const {d}=await html((T)=>T.formatConfig({server:{port:8443,note:null}}));
 const cell=[...d.querySelectorAll('.config-table tr')].find((r)=>r.textContent.includes('note')).querySelector('td.config-value');
 const box=cell.querySelector('.manifest-value');
 expect(box.querySelector('button')).toBeNull();
 expect(firstKid(box).classList.contains('copy-slot')).toBe(true);
 expect(firstKid(box).getAttribute('aria-hidden')).toBe('true');
 expect(firstKid(box).textContent).toBe('');
 expect(box.querySelector('.empty-value').textContent).toBe('—');
 expect(css).toMatch(/\.manifest-value > \.copy-slot\s*\{[^}]*width:\s*28px/);
 expect(css).toMatch(/\.config-table \.manifest-value > \.copy-slot\s*\{[^}]*width:\s*24px/);
});

t('a masked secret puts the copy button first and the eye button after the mask',async()=>{
 const {d,w}=await html((T)=>T.formatConfig({auth:{password:'hunter2'}}));
 const box=d.querySelector('.secret-value');
 expect([...box.children].map((c)=>c.className.split(' ')[0]||c.tagName)).toEqual(['copy-btn-inline','secret-text','secret-toggle']);
 expect(box.firstElementChild.getAttribute('aria-label')).toBe('Copy auth.password');
 expect(box.lastElementChild.getAttribute('aria-label')).toBe('Show auth.password');
 expect(d.body.innerHTML).not.toContain('hunter2');
});

t('a certificate value puts the copy button before the certificate',async()=>{
 const {d}=await html((T)=>T.formatConfig({tls:{cert:PEM}}));
 const box=d.querySelector('.config-table td.config-value .manifest-value');
 expect(box.firstElementChild.classList.contains('entity-copy')).toBe(true);
 expect(box.children.length).toBe(2);
});

t('every manifest tab leads with copy buttons and keeps a slot for empty values',async()=>{
 const {d}=await html((T)=>T.formatManifestDetails({text:'x',parsed:{name:'m',instance_groups:[{name:'g',instances:1,azs:['z1'],vm_type:'small',jobs:[{name:'j',release:'r',properties:{a:1}}]}],releases:[{name:'r',version:'1',sha1:'abc'},{name:'bare',version:'2'}],stemcells:[{alias:'default',version:'latest'}],update:{canaries:1},variables:[{name:'pw',type:'password'}]}},'instance'));
 const boxes=[...d.querySelectorAll('.manifest-tab-pane .manifest-value')].filter((b)=>b.querySelector('button'));
 expect(boxes.length).toBeGreaterThan(5);
 for(const box of boxes) expect(box.firstElementChild.tagName).toBe('BUTTON');
 // A plain value, such as a variable type, never had a button.
 expect([...d.querySelectorAll('.manifest-tab-pane .manifest-value')].filter((b)=>!b.querySelector('button')&&!b.classList.contains('manifest-value-empty')).every((b)=>b.children.length===1)).toBe(true);
 const empties=[...d.querySelectorAll('.manifest-value-empty')];
 expect(empties.length).toBeGreaterThan(0);
 for(const box of empties){
  expect(box.firstElementChild.classList.contains('copy-slot')).toBe(true);
  expect(box.querySelector('button')).toBeNull();
 }
});
