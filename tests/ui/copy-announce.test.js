const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const data={text:'name: m',parsed:{name:'m',director_uuid:'d-1',variables:[{name:'tls',type:'certificate'},{name:'pw',type:'password'}]}};

t('a variable type is plain text with no copy button',async()=>{
 const {w,d}=boot(); await sleep(40);
 try{
  d.body.innerHTML=w.__T.formatManifestDetails(data,'instance');
  const rows=[...d.querySelectorAll('.manifest-tab-pane[data-tab="variables"] tbody tr')];
  expect(rows.length).toBe(2);
  for(const row of rows){
   expect(row.cells[1].querySelector('button')).toBeNull();
   expect(row.cells[1].querySelector('.manifest-value-text')).not.toBeNull();
  }
  expect(rows[0].cells[1].textContent.trim()).toBe('certificate');
  const uuid=d.querySelector('[aria-label="Copy director UUID"]');
  expect(uuid).not.toBeNull();
 }finally{w.close();}
});

t('copying a field announces its name in the live region and never the value',async()=>{
 const {w,d}=boot(); await sleep(40);
 try{
  const copied=[];
  w.navigator.clipboard={writeText:(v)=>{copied.push(v);return Promise.resolve();}};
  d.body.innerHTML=w.__T.formatManifestDetails(data,'instance');
  const button=d.querySelector('[aria-label="Copy director UUID"]');
  await w.copyValue({preventDefault(){},stopPropagation(){},currentTarget:button},'d-1');
  await sleep(120);
  expect(copied).toEqual(['d-1']);
  const region=d.getElementById('blacksmith-live-region');
  expect(region.getAttribute('aria-live')).toBe('polite');
  expect(region.textContent).toBe('Copied director UUID');
  expect(region.textContent).not.toContain('d-1');
 }finally{w.close();}
});
