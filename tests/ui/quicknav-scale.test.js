const {test,expect}=require('bun:test');
require('../../ui/navigation.js');
const api=globalThis.BlacksmithNavigation;
const fleet=Object.fromEntries(Array.from({length:10000},(_,i)=>['instance-'+i,{instance_name:'Cache '+i,service_id:'valkey',plan_id:'cluster'}]));
test('large fleet has one searchable destination per instance and a useful empty state',()=>{
 const entries=api.destinations([{id:'services',label:'Service Instances'}],fleet);
 expect(entries.filter(e=>e.instanceId).length).toBe(10000);
 expect(api.search(entries,'').every(e=>!e.instanceId)).toBe(true);
 expect(api.search(entries,'instance-9999')[0].instanceId).toBe('instance-9999');
 expect(api.search(entries,'Cache 9999 logs')[0].section).toBe('instance-logs');
 expect(api.search(entries,'valkey cluster').length).toBe(10000);
});
test('instance name is primary, IDs are exact-ranked, deleted matches follow active matches',()=>{
 const entries=api.destinations([],{a:{instance_name:'Orders',deleted:true},b:{instance_name:'Orders'},'orders':{instance_name:'Other'}});
 expect(api.search(entries,'orders')[0].instanceId).toBe('orders');
 expect(api.search(api.destinations([],{gone:{deleted:true},a:{instance_name:'gone replica'}}),'gone')[0].instanceId).toBe('gone');
 expect(api.search(entries,'orders').find(e=>e.instanceId==='b').label).toBe('Orders');
 expect(api.search(entries,'orders').map(e=>e.instanceId).indexOf('b')).toBeLessThan(api.search(entries,'orders').map(e=>e.instanceId).indexOf('a'));
});
test('cooperative search cancels old work and agrees with synchronous ranking',async()=>{
 const entries=api.destinations([],fleet), controller=new AbortController();let yields=0;
 const canceled=await api.searchAsync(entries,'cache',{signal:controller.signal,yieldWork:async()=>{yields++;controller.abort();}});
 expect(yields).toBeGreaterThan(0);expect(canceled).toBeNull();
 const found=await api.searchAsync(entries,'9999 logs',{yieldWork:async()=>{}});
 expect(found).toEqual(api.search(entries,'9999 logs'));
});

const fs=require('fs');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('progressive results stay bounded initially, keyboard loads next page, and query changes cancel old results',async()=>{
 const {w,d}=boot();try{
 await sleep(30);w.serviceInstances=fleet;
 w.Element.prototype.scrollIntoView=function(){};w.Element.prototype.getClientRects=function(){return this.matches('dialog[open]')?[1]:[]};
 const dialog=d.getElementById('navigation-dialog');dialog.showModal=()=>dialog.setAttribute('open','');dialog.close=()=>dialog.removeAttribute('open');
 w.eval(fs.readFileSync('ui/navigation.js','utf8'));
 d.getElementById('navigation-trigger').click();
 const input=d.getElementById('navigation-search'),list=d.getElementById('navigation-results');
 const query=value=>{input.value=value;input.dispatchEvent(new w.Event('input'));};
 query('cache');await sleep(250);
 expect(list.children.length).toBe(40);expect(list.firstChild.getAttribute('aria-setsize')).toBe('10000');
 for(let i=0;i<40;i++)input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
 expect(list.children.length).toBe(80);expect(input.getAttribute('aria-activedescendant')).toBe('navigation-option-40');
 dialog.querySelector('[data-load-more]').click();expect(list.children.length).toBe(120);
 query('9998 logs');query('9999 credentials');await sleep(250);
 expect(list.children.length).toBe(1);expect(list.textContent).toContain('Cache 9999');expect(list.textContent).toContain('Credentials');
 expect(dialog.querySelector('[data-load-more]').hidden).toBe(true);
 query('missing-instance');await sleep(250);expect(list.children.length).toBe(0);expect(input.hasAttribute('aria-activedescendant')).toBe(false);
 }finally{w.close();}
});

test('friendly catalog service and plan names remain searchable alongside IDs',()=>{
 const entries=api.destinations([],{i:{instance_name:'Orders',service_id:'service-id',plan_id:'plan-id'}},{services:[{id:'service-id',name:'Valkey',plans:[{id:'plan-id',name:'Cluster'}]}]});
 expect(api.search(entries,'orders valkey cluster')[0].instanceId).toBe('i');
 expect(api.search(entries,'service-id plan-id')[0].instanceId).toBe('i');
});
