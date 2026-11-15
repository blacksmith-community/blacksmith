const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// The Redis and Valkey writes ask in tiers. Flush asks the operator to type
// the instance's name. Delete key and Command ask in the page's dialog, which
// names the instance and shows what will run as visible text. Set asks
// nothing. A key name or a command never lands in an attribute, a toast, or
// the console.

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); }finally{ env.w.close(); } } });

const KEY='session:SECRET-KEY-42';
const OTHER='cart:SECRET-KEY-43';
const CMD='CONFIG';
const ARG='maxmemory-SECRET-ARG';

function start(){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.serviceInstances={'i1':{instance_name:'orders-cache'}};
 w.__routes.unshift([/\/b\/i1\/redis\//,(u,o,json)=>json(200,{success:true})]);
 d.body.insertAdjacentHTML('beforeend',`<input id="redis-flush-db-i1" value="2">
  <textarea id="redis-delete-keys-i1"></textarea>
  <input id="redis-cmd-i1"><textarea id="redis-args-i1"></textarea>
  <input id="redis-key-i1"><textarea id="redis-value-i1"></textarea><input id="redis-ttl-i1">`);
 const toasts=[];
 const feedback=w.Feedback;
 w.Feedback={...feedback,success:(m,o)=>{toasts.push(String(m));},error:(m,o)=>{toasts.push(String(m));},warning:(m,o)=>{toasts.push(String(m));},info:(m,o)=>{toasts.push(String(m));}};
 const notify=w.showNotification;
 w.showNotification=(m,type)=>{toasts.push(String(m));};
 return {...env,toasts};
}
const overlay=(d)=>d.getElementById('dialog-modal');
const posts=(env,op)=>env.calls.filter(c=>c.startsWith(`POST /b/i1/redis/${op}`));
const confirmBtn=(d)=>overlay(d).querySelector('[data-dialog-role="confirm"]');
const cancelBtn=(d)=>overlay(d).querySelector('[data-dialog-role="cancel"]');
// Every attribute on every element, so a test can prove a value never
// reached one.
const attributes=(d)=>[...d.querySelectorAll('*')].flatMap(el=>[...el.attributes].map(a=>`${el.tagName}[${a.name}]=${a.value}`));
const leaks=(env,secret)=>({
 attributes:attributes(env.d).filter(a=>a.includes(secret)),
 toasts:env.toasts.filter(m=>m.includes(secret)),
 logs:env.logs.concat(env.errors).filter(m=>m.includes(secret)),
 urls:env.calls.map(c=>c.split(' ')[1]).filter(u=>u.includes(secret))
});
const none={attributes:[],toasts:[],logs:[],urls:[]};

t('Flush asks the operator to type the instance name',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.executeRedisFlush('i1'); await sleep(10);
 expect(overlay(d).open).toBe(true);
 expect(overlay(d).querySelector('.dialog-modal-title').textContent).toBe('Flush database 2 of orders-cache?');
 expect(overlay(d).querySelector('.dialog-modal-ack-label').textContent).toBe('To continue, type "orders-cache" below.');
 confirmBtn(d).click(); await sleep(10);
 expect(posts(env,'flush')).toEqual([]);
 const input=overlay(d).querySelector('.dialog-modal-ack-input');
 input.value='orders'; input.dispatchEvent(new w.Event('input',{bubbles:true}));
 confirmBtn(d).click(); await sleep(10);
 expect(posts(env,'flush')).toEqual([]);
 input.value='Orders-Cache'; input.dispatchEvent(new w.Event('input',{bubbles:true}));
 confirmBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'flush').length).toBe(1);
 expect(posts(env,'flush')[0]).toContain('"database":2');
});

t('Flush sends nothing on Cancel',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.executeRedisFlush('i1'); await sleep(10);
 cancelBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'flush')).toEqual([]);
});

t('Delete key names the instance and shows each key as visible text',async()=>{
 const env=start(); const {w,d}=env;
 d.getElementById('redis-delete-keys-i1').value=`${KEY}\n\n ${OTHER} \n`;
 const done=w.executeRedisDelete('i1'); await sleep(10);
 expect(overlay(d).open).toBe(true);
 expect(overlay(d).querySelector('.dialog-modal-title').textContent).toBe('Delete 2 keys from orders-cache?');
 expect([...overlay(d).querySelectorAll('.dialog-modal-command')].map(n=>n.textContent)).toEqual([KEY,OTHER]);
 expect(overlay(d).textContent).toContain('orders-cache');
 expect(posts(env,'delete')).toEqual([]);
 expect(leaks(env,KEY)).toEqual(none);
 confirmBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'delete').length).toBe(1);
 expect(posts(env,'delete')[0]).toContain(`"keys":["${KEY}","${OTHER}"]`);
 expect(leaks(env,KEY).attributes).toEqual([]);
 expect(leaks(env,KEY).toasts).toEqual([]);
 expect(leaks(env,KEY).logs).toEqual([]);
});

t('Delete of one key says key, and Cancel sends nothing',async()=>{
 const env=start(); const {w,d}=env;
 d.getElementById('redis-delete-keys-i1').value=KEY;
 const done=w.executeRedisDelete('i1'); await sleep(10);
 expect(overlay(d).querySelector('.dialog-modal-title').textContent).toBe('Delete 1 key from orders-cache?');
 cancelBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'delete')).toEqual([]);
 expect(leaks(env,KEY)).toEqual(none);
});

t('Command names the instance and shows the command only as visible text',async()=>{
 const env=start(); const {w,d}=env;
 d.getElementById('redis-cmd-i1').value=CMD;
 d.getElementById('redis-args-i1').value=JSON.stringify(['GET',ARG]);
 const done=w.executeRedisCommand('i1'); await sleep(10);
 expect(overlay(d).open).toBe(true);
 expect(overlay(d).querySelector('.dialog-modal-title').textContent).toBe('Run a command on orders-cache?');
 expect(overlay(d).querySelector('.dialog-modal-command').textContent).toBe(`${CMD} GET ${ARG}`);
 expect(posts(env,'command')).toEqual([]);
 expect(leaks(env,ARG)).toEqual(none);
 confirmBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'command').length).toBe(1);
 expect(posts(env,'command')[0]).toContain(`"command":"${CMD}","args":["GET","${ARG}"]`);
 expect(leaks(env,ARG).toasts).toEqual([]);
 expect(leaks(env,ARG).logs).toEqual([]);
 expect(leaks(env,ARG).urls).toEqual([]);
});

t('Command sends nothing on Cancel or Escape',async()=>{
 const env=start(); const {w,d}=env;
 d.getElementById('redis-cmd-i1').value=CMD;
 let done=w.executeRedisCommand('i1'); await sleep(10);
 cancelBtn(d).click(); await done; await sleep(10);
 done=w.executeRedisCommand('i1'); await sleep(10);
 d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})); await done; await sleep(10);
 expect(posts(env,'command')).toEqual([]);
 expect(leaks(env,CMD+' ')).toEqual(none);
});

t('Set runs with no dialog',async()=>{
 const env=start(); const {w,d}=env;
 d.getElementById('redis-key-i1').value='k';
 d.getElementById('redis-value-i1').value='v';
 await w.executeRedisSet('i1'); await sleep(10);
 expect(overlay(d)?.open||false).toBe(false);
 expect(posts(env,'set').length).toBe(1);
});

// The instance's identity rows, so an operator with several caches named
// alike can tell which one the dialog acts on.
const identity=(d)=>[...overlay(d).querySelectorAll('.dialog-modal-details li')].map(n=>n.textContent);
const placed=(w)=>{ w.serviceInstances={'i1':{instance_name:'Orders-Cache',organization_name:'payments',space_name:'prod'}}; };
const IDENTITY=['Instance: Orders-Cache','Instance ID: i1','Organization: payments','Space: prod'];

t('Flush shows the instance ID, org, and space, and the name in its own case',async()=>{
 const env=start(); const {w,d}=env;
 placed(w);
 const done=w.executeRedisFlush('i1'); await sleep(10);
 expect(identity(d)).toEqual(IDENTITY);
 expect(overlay(d).querySelector('.dialog-modal-ack-label').textContent).toBe('To continue, type "Orders-Cache" below.');
 const input=overlay(d).querySelector('.dialog-modal-ack-input');
 expect(input.placeholder).toBe('Orders-Cache');
 input.value='Orders-Cache'; input.dispatchEvent(new w.Event('input',{bubbles:true}));
 confirmBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'flush').length).toBe(1);
});

t('Delete key shows the instance ID, org, and space beside the keys',async()=>{
 const env=start(); const {w,d}=env;
 placed(w);
 d.getElementById('redis-delete-keys-i1').value=KEY;
 const done=w.executeRedisDelete('i1'); await sleep(10);
 expect(identity(d)).toEqual(IDENTITY);
 expect([...overlay(d).querySelectorAll('.dialog-modal-command')].map(n=>n.textContent)).toEqual([KEY]);
 expect(leaks(env,KEY)).toEqual(none);
 cancelBtn(d).click(); await done; await sleep(10);
});

t('Command shows where each argument starts and ends, as visible text only',async()=>{
 const env=start(); const {w,d}=env;
 placed(w);
 d.getElementById('redis-cmd-i1').value='SET';
 d.getElementById('redis-args-i1').value=JSON.stringify(['my SECRET-ARG key','','plain',7,'say "hi"']);
 const done=w.executeRedisCommand('i1'); await sleep(10);
 expect(identity(d)).toEqual(IDENTITY);
 expect(overlay(d).querySelector('.dialog-modal-command').textContent).toBe('SET "my SECRET-ARG key" "" plain 7 "say \\"hi\\""');
 expect(overlay(d).querySelector('.dialog-modal-run-warning').textContent).toContain('Quotation marks show where an argument with spaces, or an empty one, starts and ends.');
 expect(leaks(env,'SECRET-ARG')).toEqual(none);
 confirmBtn(d).click(); await done; await sleep(10);
 expect(posts(env,'command')[0]).toContain('"args":["my SECRET-ARG key","","plain",7,"say \\"hi\\""]');
 expect(leaks(env,'SECRET-ARG').toasts).toEqual([]);
 expect(leaks(env,'SECRET-ARG').logs).toEqual([]);
 expect(leaks(env,'SECRET-ARG').urls).toEqual([]);
});
