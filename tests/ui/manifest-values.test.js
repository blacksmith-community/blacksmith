const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
const SHA='a'.repeat(40);
const URL_='https://bosh.io/d/github.com/example/valkey-forge-release?v=1.2.3';
const data={
 text:'name: m',
 parsed:{
  name:'m',
  instance_groups:[{name:'valkey',instances:3,azs:['z1','z2'],networks:[{name:'default'}],vm_type:'small',jobs:[{name:'valkey',release:'valkey-forge',properties:{enabled:true,port:6379,note:'x'}}]}],
  releases:[
   {name:'valkey-forge',version:'1.2.3',url:URL_,sha1:SHA},
   {name:'bare',version:'2'}
  ],
  stemcells:[{alias:'default',version:'latest'}],
  features:{use_dns_addresses:true},
  update:{canaries:1,max_in_flight:10},
  variables:[
   {name:'tls',type:'certificate',options:{ca:'root',common_name:'valkey',alternative_names:['a.example','b.example']}},
   {name:'pw',type:'password'}
  ]
 }
};
const render=async()=>{
 const env=boot();
 await sleep(40);
 env.d.body.innerHTML=env.w.__T.formatManifestDetails(data,'instance');
 return env;
};
const pane=(d,name)=>d.querySelector(`.manifest-tab-pane[data-tab="${name}"]`);
const rows=(d,name)=>[...pane(d,name).querySelectorAll('tbody tr')];

t('a value cell leads with its copy button and the value follows',async()=>{
 const {w,d}=await render();
 try{
  const sha=rows(d,'releases')[0].cells[3];
  const cell=sha.querySelector('.manifest-value');
  expect(cell).not.toBeNull();
  expect(cell.children[1].classList.contains('manifest-value-text')).toBe(true);
  expect(cell.children[1].textContent).toBe(SHA);
  expect(cell.children[1].getAttribute('title')).toBe(SHA);
  expect(cell.children[0].tagName).toBe('BUTTON');
  expect(cell.children[0].getAttribute('aria-label')).toBe('Copy release SHA1');
  expect(sha.querySelector('code')).toBeNull();
  expect(cell.children[0].getAttribute('onclick')).toContain(SHA);
  const url=rows(d,'releases')[0].cells[2].querySelector('.manifest-value a');
  expect(url.getAttribute('href')).toBe(URL_);
  expect(url.getAttribute('title')).toBe(URL_);
   const instanceCells=[...pane(d,'instance-groups').querySelectorAll('.instance-group-pane.active tbody tr td:nth-child(2)')];
 for(const td of instanceCells){
   if(td.querySelector('.empty-value')) continue;
   expect(td.querySelector('.manifest-value > button + .manifest-value-text')).not.toBeNull();
  }
 }finally{w.close()}
});

t('a name cell is plain text with no copy button',async()=>{
 const {w,d}=await render();
 try{
  const name=rows(d,'releases')[0].cells[0];
  expect(name.textContent.trim()).toBe('valkey-forge');
  expect(name.querySelector('button')).toBeNull();
  expect(rows(d,'variables')[0].cells[0].querySelector('button')).toBeNull();
 }finally{w.close()}
});

t('a missing value is the shared empty value with no copy button',async()=>{
 const {w,d}=await render();
 try{
  const bare=rows(d,'releases')[1];
  for(const i of [2,3]){
   expect(bare.cells[i].querySelector('.empty-value').textContent).toBe('—');
   expect(bare.cells[i].querySelector('button')).toBeNull();
  }
  const global=rows(d,'global')[0];
  expect(global.cells[1].querySelector('.empty-value').textContent).toBe('—');
  expect(d.body.textContent).not.toContain('N/A');
  expect(d.body.textContent).not.toContain('Not specified');
 }finally{w.close()}
});

t('values carry no type coloring',async()=>{
 const {w,d}=await render();
 try{
  expect(d.querySelector('.boolean-value, .number-value')).toBeNull();
  expect(css).not.toMatch(/\.boolean-value|\.number-value/);
  const update=rows(d,'update').map((r)=>r.cells[1].querySelector('.manifest-value-text').textContent);
  expect(update).toEqual(['1','10']);
  expect(rows(d,'features')[0].cells[1].querySelector('.manifest-value-text').textContent).toBe('true');
 }finally{w.close()}
});

t('variable options are a label and value list',async()=>{
 const {w,d}=await render();
 try{
  const list=rows(d,'variables')[0].cells[2].querySelector('dl.manifest-options');
  expect(list).not.toBeNull();
  const pairs=[...list.querySelectorAll(':scope > div')].map((x)=>[x.querySelector('dt').textContent,x.querySelector('dd').textContent]);
  expect(pairs).toEqual([['ca','root'],['common_name','valkey'],['alternative_names','a.example, b.example']]);
  expect(rows(d,'variables')[0].cells[2].querySelector('pre')).toBeNull();
  expect(rows(d,'variables')[1].cells[2].querySelector('.empty-value').textContent).toBe('—');
 }finally{w.close()}
});

t('the instance group picker is labelled on one line without a colon',async()=>{
 const {w,d}=await render();
 try{
  const label=d.querySelector('.instance-group-selector label');
  expect(label.textContent).toBe('Instance group');
  expect(css).toMatch(/\.instance-group-selector label,\s*\.job-selector label \{[^}]*white-space:\s*nowrap/);
 }finally{w.close()}
});

t('property tables have no sorting and the long tables keep it',async()=>{
 const {w,d}=await render();
 try{
  for(const name of ['global','features','update']) expect(pane(d,name).querySelector('table').getAttribute('data-sortable')).toBe('false');
  for(const table of pane(d,'instance-groups').querySelectorAll('table')) expect(table.getAttribute('data-sortable')).toBe('false');
  for(const name of ['releases','stemcells','variables']) expect(pane(d,name).querySelector('table').getAttribute('data-sortable')).toBeNull();
 }finally{w.close()}
});
