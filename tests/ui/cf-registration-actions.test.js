const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// Routes for the page's own load, so a console error means something broke.
const QUIET=[[/\/b\/status$/,(u,o,j)=>j(200,{env:'Local fixture',instances:{},plans:{}})],[/\/b\/instance$/,(u,o,j)=>j(200,{deployment:'blacksmith'})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],[/inventory\?summary_only=true/,(u,o,j)=>j(200,{summary:{certificates:0,needs_attention:0}})]];

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);expect(env.errors).toEqual([]);env.w.close();}});

t('a registration card offers no Sync action',async()=>{
 const env=boot({noDialogs:true,oat:true,routes:QUIET});envs.push(env);
 await sleep(40);
 const manager=env.w.__T.CFRegistrationManager;
 const holder=env.d.createElement('div');
 holder.innerHTML=manager.renderRegistrationCard({id:'reg-1',name:'lab-cf',api_url:'https://api.cf.example.com',username:'admin',status:'active'});
 const actions=[...holder.querySelectorAll('.registration-actions button')].map(b=>b.textContent.trim());
 expect(actions).toEqual(['Re-register','View Progress','Edit','Delete']);
 expect(holder.querySelector('.sync-registration-btn')).toBeNull();
 expect(manager.syncRegistration).toBeUndefined();
});

// The broker has no sync route, so nothing in the page may call one.
t('the page never posts to a registration sync route',()=>{
 const src=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.js'),'utf8');
 const calls=src.match(/\/b\/cf\/registrations\/[^`'"]*\/sync/g)||[];
 expect(calls).toEqual([]);
});

// The broker stores no CF password, so a save never sends one. The form's
// password feeds Test Connection and, when Auto-register is on, the register
// request that follows the save.
const SAVES=(u,o,j)=>{ const get=(o.method||'GET')==='GET'; if(/\/register$/.test(u)) return j(202,{message:'started'}); return j(get?200:201,get?{registrations:[]}:{ok:true}); };
const fillAndSave=async(env,fields)=>{
 const {w,d}=env;
 // The progress view after a register opens a stream that this suite stubs.
 w.EventSource=class{ close(){} };
 Object.entries(fields).forEach(([id,value])=>{ const input=d.getElementById(id); if(input.type==='checkbox') input.checked=value; else input.value=value; });
 d.getElementById('registration-form').requestSubmit();
 await sleep(700);
};
const saves=(env)=>env.calls.filter(c=>/^(POST|PUT) \/b\/cf\/registrations(\/[^/ ]+)? /.test(c));
const registers=(env)=>env.calls.filter(c=>/^POST \/b\/cf\/registrations\/[^/ ]+\/register /.test(c));

t('saving a new registration sends no password',async()=>{
 const env=boot({noDialogs:true,oat:true,routes:[...QUIET,[/\/b\/cf\/registrations/,SAVES]]});envs.push(env);
 await sleep(40);
 env.w.__T.CFRegistrationManager.showRegistrationModal();
 await fillAndSave(env,{'reg-name':'lab-cf','reg-api-url':'https://api.cf.example.com','reg-username':'admin','reg-password':'hunter2-form','reg-auto-register':false});
 const sent=saves(env);
 expect(sent.length).toBe(1);
 expect(sent[0]).toContain('"username":"admin"');
 expect(sent[0]).not.toContain('password');
 expect(registers(env)).toEqual([]);
});

t('an edit saves without a password and sends none',async()=>{
 const env=boot({noDialogs:true,oat:true,routes:[...QUIET,[/\/b\/cf\/registrations/,SAVES]]});envs.push(env);
 await sleep(40);
 const pw=env.d.getElementById('reg-password');
 expect(pw.required).toBe(false);
 expect(pw.getAttribute('autocomplete')).toBe('off');
 env.w.__T.CFRegistrationManager.showRegistrationModal({id:'reg-1',name:'lab-cf',api_url:'https://api.cf.example.com',username:'admin',password:'legacy-stored'});
 await fillAndSave(env,{'reg-name':'lab-cf-2'});
 const sent=saves(env);
 expect(sent.length).toBe(1);
 expect(sent[0]).toMatch(/^PUT \/b\/cf\/registrations\/reg-1 /);
 expect(sent[0]).not.toContain('password');
});

t('auto-register sends the form password in the register body only',async()=>{
 const env=boot({noDialogs:true,oat:true,routes:[...QUIET,[/\/b\/cf\/registrations/,SAVES]]});envs.push(env);
 await sleep(40);
 env.w.__T.CFRegistrationManager.showRegistrationModal();
 await fillAndSave(env,{'reg-name':'lab-cf','reg-api-url':'https://api.cf.example.com','reg-username':'admin','reg-password':'hunter2-form','reg-auto-register':true});
 const sent=saves(env);
 expect(sent.length).toBe(1);
 expect(sent[0]).not.toContain('hunter2-form');
 const id=JSON.parse(sent[0].slice(sent[0].indexOf('{'))).id;
 const reg=registers(env);
 expect(reg.length).toBe(1);
 expect(reg[0].split(' ')[1]).toBe(`/b/cf/registrations/${id}/register`);
 expect(JSON.parse(reg[0].slice(reg[0].indexOf('{')))).toEqual({password:'hunter2-form'});
 expect(env.logs.join('\n')).not.toContain('hunter2-form');
});
