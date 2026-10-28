const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UUID=/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
// Routes for the page's own load, so a console error means something broke.
const QUIET=[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],[/inventory\?summary_only=true/,(u,o,j)=>j(200,{summary:{certificates:0,needs_attention:0}})]];

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);expect(env.errors).toEqual([]);env.w.close();}});

// A booted page with the registration routes scripted. The posts, puts, and
// every other request are recorded so a test can prove what was sent.
async function setup({post,put}={}){
 const env=boot({noDialogs:true,oat:true,routes:QUIET}),{w,d}=env;envs.push(env);
 const sent=[];
 const record=(method)=>(u,o,j)=>{sent.push({method,url:u,body:o.body?JSON.parse(o.body):null});const handler=method==='POST'?post:put;return handler?handler(u,o,j):j(200,{message:'ok'});};
 w.__routes.push([/\/b\/cf\/registrations$/,(u,o,j)=>(o.method||'GET')==='POST'?record('POST')(u,o,j):j(200,{registrations:[],count:0})],
  [/\/b\/cf\/registrations\/[^/]+$/,(u,o,j)=>(o.method||'GET')==='PUT'?record('PUT')(u,o,j):undefined]);
 await sleep(40);
 return {...env,sent,manager:w.__T.CFRegistrationManager};
}
const form=(d)=>d.getElementById('registration-form');
const save=(d)=>d.getElementById('save-registration-btn');
const test_=(d)=>d.getElementById('test-connection-btn');
function fill(d,over={}){
 const v={name:'lab-cf',api_url:'https://api.cf.example.com',username:'admin',password:'s3cret',broker_name:'',...over};
 d.getElementById('reg-name').value=v.name;d.getElementById('reg-api-url').value=v.api_url;d.getElementById('reg-username').value=v.username;
 d.getElementById('reg-password').value=v.password;d.getElementById('reg-broker-name').value=v.broker_name;
}
const toasts=(d)=>[...d.querySelectorAll('.toast-message')].map(n=>n.textContent);
const settle=()=>sleep(30);

t('Save posts the registration with an id and closes the dialog',async()=>{
 const env=await setup();const {d,manager,sent}=env;
 manager.showRegistrationModal();
 expect(d.getElementById('registration-modal').open).toBe(true);
 fill(d);
 save(d).click();await settle();
 expect(sent.length).toBe(1);
 const {method,url,body}=sent[0];
 expect(method).toBe('POST');expect(url).toContain('/b/cf/registrations');
 expect(body.id).toMatch(UUID);
 expect(body).toMatchObject({name:'lab-cf',api_url:'https://api.cf.example.com',username:'admin',password:'s3cret',broker_name:'blacksmith',auto_register:false});
 expect(toasts(d)).toContain('Registration created successfully');
 expect(d.getElementById('registration-modal').open).toBe(false);
});

t('Save shows progress in the dialog while the request is pending',async()=>{
 let release;const gate=new Promise((r)=>{release=r;});
 const env=await setup({post:(u,o,j)=>gate.then(()=>j(200,{message:'ok'}))});const {d,manager,sent}=env;
 manager.showRegistrationModal();fill(d);
 save(d).click();await settle();
 expect(save(d).disabled).toBe(true);
 expect(save(d).textContent).toBe('Saving…');
 expect(form(d).getAttribute('aria-busy')).toBe('true');
 // A second submit while the first is pending sends nothing more.
 form(d).requestSubmit();await settle();
 expect(sent.length).toBe(1);
 release();await settle();
 expect(save(d).disabled).toBe(false);
 expect(save(d).textContent).toBe('Save Registration');
 expect(form(d).hasAttribute('aria-busy')).toBe(false);
});

t('Save keeps the dialog open and reports the server error',async()=>{
 const env=await setup({post:(u,o,j)=>j(500,{error:'vault is sealed'})});const {d,manager}=env;
 manager.showRegistrationModal();fill(d);
 save(d).click();await settle();
 expect(toasts(d).some((m)=>m.includes('Failed to create registration')&&m.includes('vault is sealed'))).toBe(true);
 expect(d.getElementById('registration-modal').open).toBe(true);
 expect(save(d).disabled).toBe(false);
 expect(form(d).hasAttribute('aria-busy')).toBe(false);
 expect(d.getElementById('reg-name').value).toBe('lab-cf');
 // The failure was reported by a toast, so the console stays clean for the log check.
 env.errors.length=0;
});

t('Save reports a failed request and lets the operator try again',async()=>{
 let fail=true;
 const env=await setup({post:(u,o,j)=>fail?Promise.reject(new Error('network down')):j(200,{message:'ok'})});const {d,manager,sent}=env;
 manager.showRegistrationModal();fill(d);
 save(d).click();await settle();
 expect(toasts(d).some((m)=>m.includes('Failed to save registration')&&m.includes('network down'))).toBe(true);
 expect(save(d).disabled).toBe(false);
 expect(d.getElementById('registration-modal').open).toBe(true);
 env.errors.length=0;
 fail=false;
 save(d).click();await settle();
 expect(sent.length).toBe(2);
 expect(d.getElementById('registration-modal').open).toBe(false);
});

t('Save sends nothing while a required field is empty',async()=>{
 const env=await setup();const {d,manager,sent}=env;
 manager.showRegistrationModal();fill(d,{name:''});
 save(d).click();await settle();
 expect(sent.length).toBe(0);
 expect(d.getElementById('registration-modal').open).toBe(true);
});

t('Save on an edited registration puts the merged record under its own id',async()=>{
 const env=await setup();const {d,manager,sent}=env;
 const existing={id:'reg-1',name:'old',api_url:'https://old.example.com',username:'admin',broker_name:'blacksmith',status:'active',created_at:'2026-10-01T00:00:00Z'};
 manager.registrations=[existing];
 manager.editRegistration('reg-1');
 expect(d.getElementById('registration-modal-title').textContent).toBe('Edit CF Registration');
 d.getElementById('reg-name').value='renamed';d.getElementById('reg-password').value='s3cret';
 save(d).click();await settle();
 expect(sent.length).toBe(1);
 expect(sent[0].method).toBe('PUT');expect(sent[0].url).toContain('/b/cf/registrations/reg-1');
 expect(sent[0].body).toMatchObject({id:'reg-1',name:'renamed',status:'active',created_at:'2026-10-01T00:00:00Z',password:'s3cret'});
 expect(toasts(d)).toContain('Registration updated successfully');
 expect(d.getElementById('registration-modal').open).toBe(false);
});

t('a new registration after an edit posts as a create again',async()=>{
 const env=await setup();const {d,manager,sent}=env;
 manager.registrations=[{id:'reg-1',name:'old',api_url:'https://old.example.com',username:'admin'}];
 manager.editRegistration('reg-1');
 manager.hideRegistrationModal();
 manager.showRegistrationModal();fill(d);
 save(d).click();await settle();
 expect(sent.length).toBe(1);
 expect(sent[0].method).toBe('POST');expect(sent[0].body.id).toMatch(UUID);expect(sent[0].body.id).not.toBe('reg-1');
});

// The broker has no route that tests a connection. The only CF route that
// resembles one is /b/cf/endpoints/{id}/connect, which takes a configured
// endpoint's key, never a URL and credentials from this form, and always
// reports success. Until the broker grows a real route, Test Connection stays
// unwired and sends nothing.
t('Test Connection sends nothing because the broker has no route for it',async()=>{
 const env=await setup();const {d,manager,calls}=env;
 manager.showRegistrationModal();fill(d);
 const before=calls.length;
 test_(d).click();await settle();
 expect(calls.length).toBe(before);
 expect(d.getElementById('registration-modal').open).toBe(true);
});
