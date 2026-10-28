const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');

// The blocking browser dialogs cannot be styled, announced the way the page
// announces its own, or driven by the page's focus rules, so none may return.
test('no source file under ui calls alert, confirm, or prompt',()=>{
 const found=[];
 const walk=(dir)=>fs.readdirSync(dir,{withFileTypes:true}).forEach(entry=>{
  const full=path.join(dir,entry.name);
  if(entry.isDirectory()){ if(entry.name!=='vendor'&&entry.name!=='node_modules') walk(full); return; }
  if(!/\.(js|mjs|html)$/.test(entry.name)||/\.min\.js$/.test(entry.name)) return;
  fs.readFileSync(full,'utf8').split('\n').forEach((line,i)=>{
   if(/(^|[^.\w$])(?:(?:window|globalThis|self)\s*\.\s*)?(alert|confirm|prompt)\s*\(/.test(line)||/\[\s*['"`](alert|confirm|prompt)['"`]\s*\]\s*\(/.test(line)) found.push(`${path.relative(UI,full)}:${i+1}`);
  });
 });
 expect(found).toEqual([]);
});

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});
const start=()=>{
 const env=boot({noDialogs:true});envs.push(env);
 const {w,d}=env;
 const opener=d.createElement('button');opener.id='dlg-opener';opener.textContent='Open';d.body.appendChild(opener);
 opener.focus();
 return {...env,opener};
};
const overlayOf=(d)=>d.getElementById('dialog-modal-overlay');
const press=(w,d,key)=>d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true}));

const flows=[
 {name:'flush the Redis database',match:/redis.*flush/i,text:/flush the database/,
  run:({w})=>w.executeRedisFlush('i1')},
 {name:'delete a RabbitMQ queue',match:/queue-ops/,text:/delete queue "orders"/,
  setup:({d})=>{ d.body.insertAdjacentHTML('beforeend','<input id="rabbitmq-queue-ops-name-i1" value="orders"><input id="rabbitmq-operation-i1" value="delete">'); },
  run:({w})=>w.executeRabbitMQQueueOps('i1')},
 {name:'purge a RabbitMQ queue',match:/queue-ops/,text:/purge all messages from queue "orders"/,
  setup:({d})=>{ d.body.insertAdjacentHTML('beforeend','<input id="rabbitmq-queue-ops-name-i1" value="orders"><input id="rabbitmq-operation-i1" value="purge">'); },
  run:({w})=>w.executeRabbitMQQueueOps('i1')},
 {name:'clear the rabbitmqctl history',match:/DELETE .*rabbitmqctl\/history/,text:/clear all command history/,
  run:({w})=>w.clearRabbitMQCtlHistory('i1')},
 {name:'clear the plugins history',match:/DELETE .*plugins\/history/,text:/clear the plugins command history/,
  run:({w})=>w.clearRabbitMQPluginsHistory('i1')},
 {name:'cancel a task',match:/POST \/b\/tasks\/42\/cancel/,text:/cancel task 42/,
  run:({w})=>{ w.currentTaskId=42; return w.cancelTaskAndRefresh(); }},
 {name:'delete a registration',match:/DELETE \/b\/cf\/registrations\/r1/,text:/delete this registration/,
  run:({w})=>w.__T.CFRegistrationManager.deleteRegistration('r1')},
 {name:'delete the resurrection config',match:/DELETE \/b\/i1\/resurrection/,text:/delete the resurrection config/,
  setup:({d})=>{ d.body.insertAdjacentHTML('beforeend','<button id="delete-resurrection-btn-i1">Delete</button>'); },
  run:({w})=>w.deleteResurrectionConfig('i1')},
];

flows.forEach((flow)=>{
 const hits=(env)=>env.calls.filter(c=>flow.match.test(c));
 const open=async()=>{
  const env=start(); if(flow.setup) flow.setup(env);
  const done=flow.run(env); await sleep(10);
  return {env,done};
 };
 t(`${flow.name} asks in the page dialog and focuses Cancel`,async()=>{
  const {env,done}=await open(); const {d}=env;
  const overlay=overlayOf(d);
  expect(overlay.classList.contains('active')).toBe(true);
  expect(overlay.querySelector('.dialog-modal-message').textContent).toMatch(flow.text);
  expect(d.activeElement).toBe(overlay.querySelector('[data-dialog-role="cancel"]'));
  expect(hits(env)).toEqual([]);
  overlay.querySelector('[data-dialog-role="cancel"]').click(); await done;
 });
 t(`${flow.name} does nothing on Cancel and returns focus`,async()=>{
  const {env,done}=await open(); const {d}=env;
  overlayOf(d).querySelector('[data-dialog-role="cancel"]').click(); await done; await sleep(10);
  expect(hits(env)).toEqual([]);
  expect(overlayOf(d).classList.contains('active')).toBe(false);
  expect(d.activeElement).toBe(env.opener);
 });
 t(`${flow.name} does nothing on an overlay click`,async()=>{
  const {env,done}=await open(); const {d}=env;
  overlayOf(d).click(); await done; await sleep(10);
  expect(hits(env)).toEqual([]);
  expect(overlayOf(d).classList.contains('active')).toBe(false);
 });
 t(`${flow.name} does nothing on Escape`,async()=>{
  const {env,done}=await open(); const {w,d}=env;
  press(w,d,'Escape'); await done; await sleep(10);
  expect(hits(env)).toEqual([]);
  expect(overlayOf(d).classList.contains('active')).toBe(false);
 });
 t(`${flow.name} runs only after Confirm`,async()=>{
  const {env,done}=await open(); const {d}=env;
  expect(hits(env)).toEqual([]);
  overlayOf(d).querySelector('[data-dialog-role="confirm"]').click(); await done; await sleep(10);
  expect(hits(env).length).toBe(1);
 });
});

t('the registration password is asked in a masked dialog field',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 const overlay=overlayOf(d); const input=overlay.querySelector('.dialog-modal-input');
 expect(overlay.classList.contains('active')).toBe(true);
 expect(input.getAttribute('type')).toBe('password');
 expect(input.getAttribute('autocomplete')).toBe('off');
 expect(input.style.display).not.toBe('none');
 expect(d.activeElement).toBe(input);
 const label=d.getElementById(input.getAttribute('aria-labelledby'));
 expect(label.textContent).toMatch(/password/i);
 expect(label.hidden).toBe(false);
 overlay.querySelector('[data-dialog-role="cancel"]').click(); await done;
});
t('cancelling the password dialog sends nothing, clears the field, and returns focus',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 const input=overlayOf(d).querySelector('.dialog-modal-input');
 input.value='hunter2-secret';
 overlayOf(d).querySelector('[data-dialog-role="cancel"]').click(); await done; await sleep(10);
 expect(env.calls.filter(c=>/registrations\/r1\/register/.test(c))).toEqual([]);
 expect(input.value).toBe('');
 expect(input.getAttribute('type')).toBe('text');
 expect(d.activeElement).toBe(env.opener);
});
t('Escape cancels the password dialog',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 overlayOf(d).querySelector('.dialog-modal-input').value='hunter2-secret';
 press(w,d,'Escape'); await done; await sleep(10);
 expect(env.calls.filter(c=>/register/.test(c))).toEqual([]);
 expect(overlayOf(d).querySelector('.dialog-modal-input').value).toBe('');
});
t('an empty password sends nothing',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 overlayOf(d).querySelector('[data-dialog-role="confirm"]').click(); await done; await sleep(10);
 expect(env.calls.filter(c=>/register/.test(c))).toEqual([]);
});
t('the password goes only in the request body and never reaches the console or the URL',async()=>{
 const env=start(); const {w,d}=env;
 w.__routes.push([/registrations\/r1\/register/,(u,o,json)=>json(500,{error:'boom'})]);
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 const input=overlayOf(d).querySelector('.dialog-modal-input');
 input.value='hunter2-secret';
 overlayOf(d).querySelector('[data-dialog-role="confirm"]').click(); await done; await sleep(10);
 const sent=env.calls.filter(c=>/registrations\/r1\/register/.test(c));
 expect(sent.length).toBe(1);
 expect(sent[0].split(' ')[1]).not.toContain('hunter2');
 expect(sent[0]).toContain('"password":"hunter2-secret"');
 expect(env.logs.join('\n')).not.toContain('hunter2-secret');
 expect(d.body.textContent).not.toContain('hunter2-secret');
 expect(input.value).toBe('');
 expect(input.getAttribute('type')).toBe('text');
});
t('an overlay click clears the password field and sends nothing',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 const input=overlayOf(d).querySelector('.dialog-modal-input');
 input.value='hunter2-secret';
 overlayOf(d).click(); await done; await sleep(10);
 expect(env.calls.filter(c=>/register/.test(c))).toEqual([]);
 expect(input.value).toBe('');
 expect(input.getAttribute('type')).toBe('text');
});
t('Enter confirms the password, sends it once, and clears the field',async()=>{
 const env=start(); const {w,d}=env;
 const done=w.__T.CFRegistrationManager.startRegistration('r1'); await sleep(10);
 const input=overlayOf(d).querySelector('.dialog-modal-input');
 input.value='hunter2-secret';
 input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 await done; await sleep(10);
 const sent=env.calls.filter(c=>/registrations\/r1\/register/.test(c));
 expect(sent.length).toBe(1);
 expect(sent[0]).toContain('"password":"hunter2-secret"');
 expect(input.value).toBe('');
 expect(input.getAttribute('type')).toBe('text');
 expect(env.logs.join('\n')).not.toContain('hunter2-secret');
});
