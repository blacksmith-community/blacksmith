const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
const rule=(selector)=>{
 const out=[];
 css.replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};

// The Proxmox CPI's disk CIDs are base64 strings of about 180 characters.
const LONG=(c)=>c.repeat(180);
const vm=(extra={})=>Object.assign({id:'abc',job_name:'blacksmith',index:0,state:'running',job_state:'running',az:'z1',vm_type:'small',
 vm_cid:LONG('v'),agent_id:LONG('a'),disk_cids:[LONG('d'),LONG('e')]},extra);

t('long CID values are capped, titled, and still copy in full',async()=>{
 const {w,d}=boot();await sleep(50);try{
 const host=d.createElement('div');d.body.appendChild(host);
 host.innerHTML=w.__T.formatVMs([vm()],'inst-1');
 for(const [cell,value] of [['vm-cid',LONG('v')],['vm-agent-id',LONG('a')]]){
  const td=host.querySelector('td.'+cell);
  const shown=td.querySelector('.cid-value');
  expect(shown.getAttribute('title')).toBe(value);
  expect(shown.textContent).toBe(value);
  expect(td.querySelector('button.copy-btn-inline').getAttribute('onclick')).toContain(value);
 }
 }finally{w.close();}
});
t('each disk CID sits on its own line with its own copy button',async()=>{
 const {w,d}=boot();await sleep(50);try{
 const host=d.createElement('div');d.body.appendChild(host);
 host.innerHTML=w.__T.formatVMs([vm()],'inst-1');
 const lines=[...host.querySelectorAll('td.vm-disk-cids .cid-line')];
 expect(lines.length).toBe(2);
 expect(lines.map((l)=>l.querySelector('.cid-value').getAttribute('title'))).toEqual([LONG('d'),LONG('e')]);
 lines.forEach((l,i)=>expect(l.querySelector('button.copy-btn-inline').getAttribute('onclick')).toContain(LONG(i?'e':'d')));
 }finally{w.close();}
});
t('a short CID and a single disk CID read as before',async()=>{
 const {w,d}=boot();await sleep(50);try{
 const host=d.createElement('div');d.body.appendChild(host);
 host.innerHTML=w.__T.formatVMs([vm({vm_cid:'vm-1',disk_cids:['disk-1']})],'inst-1');
 expect(host.querySelector('td.vm-cid .cid-value').textContent).toBe('vm-1');
 expect(host.querySelectorAll('td.vm-disk-cids .cid-line').length).toBe(1);
 expect(host.querySelector('td.vm-disk-cids .cid-value').textContent).toBe('disk-1');
 }finally{w.close();}
});
t('the sorted table body caps the same values',async()=>{
 const {w,d}=boot();await sleep(50);try{
 const host=d.createElement('div');d.body.appendChild(host);
 host.innerHTML=w.__T.formatVMs([vm(),vm({id:'def'})],'inst-1');
 w.__T.initializeSorting('vms-table');
 host.querySelector('.vms-table thead th').click();
 const cell=host.querySelector('.vms-table td.vm-disk-cids');
 expect(cell.querySelectorAll('.cid-value').length).toBe(2);
 expect(cell.querySelector('.cid-value').getAttribute('title')).toBe(LONG('d'));
 expect(host.querySelector('.vms-table td.vm-cid .cid-value').getAttribute('title')).toBe(LONG('v'));
 }finally{w.close();}
});
test('the CSS holds a CID value to 24ch with an ellipsis',()=>{
 const b=rule('.vms-table .cid-value');
 expect(b).toMatch(/max-width:\s*24ch/);
 expect(b).toMatch(/text-overflow:\s*ellipsis/);
 expect(b).toMatch(/overflow:\s*hidden/);
 expect(rule('.vms-table .cid-list')).toMatch(/flex-direction:\s*column/);
});
