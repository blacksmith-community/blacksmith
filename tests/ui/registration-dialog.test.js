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

// Test Connection posts the form's URL and credentials to the broker, which
// answers 200 with every check, so the form can show which step failed.
const CHECKS_PASS=[{name:'reachable',passed:true,message:'The API answered with HTTP 200.'},{name:'tls',passed:true,message:"The API's certificate verified."},{name:'cloud_foundry',passed:true,message:'The API identifies itself as Cloud Foundry.'},{name:'token',passed:true,message:"The credentials got a token from the API's authentication endpoint."}];
const SKIP={passed:false,skipped:true,message:'Not run, because an earlier check failed.'};
const CHECKS_TLS=[{name:'reachable',passed:true,message:'The API answered.'},{name:'tls',passed:false,message:"The API's certificate did not verify: the certificate is not signed by a trusted authority."},{name:'cloud_foundry',...SKIP},{name:'token',...SKIP}];
async function testSetup(handler){
 const env=await setup();const tests=[];
 env.w.__routes.push([/\/b\/cf\/test-connection$/,(u,o,j)=>{tests.push({method:o.method,body:o.body?JSON.parse(o.body):null});return handler(u,o,j);}]);
 env.manager.showRegistrationModal();fill(env.d);
 return {...env,tests};
}
const results=(d)=>d.getElementById('test-connection-results');
const rows=(d)=>[...results(d).querySelectorAll('li')].map(li=>({check:li.dataset.check,state:li.dataset.state,text:li.textContent}));
const summary=(d)=>results(d).querySelector('.test-connection-summary').textContent;

t('Test Connection posts the form credentials and shows every check passing',async()=>{
 const env=await testSetup((u,o,j)=>j(200,{success:true,message:'Connected to Cloud Foundry, and the credentials got a token.',checks:CHECKS_PASS}));const {d,tests,sent}=env;
 test_(d).click();await settle();
 expect(tests).toEqual([{method:'POST',body:{api_url:'https://api.cf.example.com',username:'admin',password:'s3cret'}}]);
 expect(sent.length).toBe(0);
 expect(results(d).hidden).toBe(false);
 expect(summary(d)).toBe('Connection test passed. Connected to Cloud Foundry, and the credentials got a token.');
 expect(rows(d).map(r=>r.check+'='+r.state)).toEqual(['reachable=passed','tls=passed','cloud_foundry=passed','token=passed']);
 expect(rows(d)[1].text).toContain("The API's certificate verified.");
 expect(d.getElementById('registration-modal').open).toBe(true);
 expect(env.logs.join('\n')).not.toContain('s3cret');
});

t('Test Connection names the failed check and the checks it did not run',async()=>{
 const env=await testSetup((u,o,j)=>j(200,{success:false,error:CHECKS_TLS[1].message,code:'cf_tls_failed',checks:CHECKS_TLS}));const {d}=env;
 test_(d).click();await settle();
 expect(summary(d)).toBe('Connection test failed. '+CHECKS_TLS[1].message);
 expect(rows(d).map(r=>r.check+'='+r.state)).toEqual(['reachable=passed','tls=failed','cloud_foundry=skipped','token=skipped']);
 expect(rows(d)[1].text).toContain('Failed');expect(rows(d)[1].text).toContain('not signed by a trusted authority');
 expect(rows(d)[2].text).toContain('Not run');
 expect(results(d).getAttribute('role')).toBe(null);
 expect(test_(d).disabled).toBe(false);
});

t('Test Connection is disabled while its request is in flight',async()=>{
 let release;const gate=new Promise((r)=>{release=r;});
 const env=await testSetup((u,o,j)=>gate.then(()=>j(200,{success:true,checks:CHECKS_PASS})));const {d,tests}=env;
 test_(d).click();await settle();
 expect(test_(d).disabled).toBe(true);
 expect(test_(d).textContent).toBe('Testing…');
 expect(results(d).getAttribute('aria-busy')).toBe('true');
 test_(d).click();await settle();
 expect(tests.length).toBe(1);
 release();await settle();
 expect(test_(d).disabled).toBe(false);
 expect(test_(d).textContent).toBe('Test Connection');
 expect(results(d).hasAttribute('aria-busy')).toBe(false);
});

t('Test Connection shows a readable error when the request fails',async()=>{
 let fail=true;
 const env=await testSetup((u,o,j)=>fail?Promise.reject(new TypeError('Failed to fetch')):j(200,{success:true,checks:CHECKS_PASS}));const {d,tests}=env;
 test_(d).click();await settle();
 expect(summary(d)).toBe('Connection test failed. The broker could not be reached: Failed to fetch');
 expect(rows(d)).toEqual([]);
 expect(test_(d).disabled).toBe(false);
 fail=false;
 test_(d).click();await settle();
 expect(tests.length).toBe(2);
 expect(summary(d)).toBe('Connection test passed.');
});

t('Test Connection shows a readable error for an HTML answer and never the HTML',async()=>{
 const page='<html><body><h1>Bad Gateway</h1><script>window.__owned=1</script></body></html>';
 const env=await testSetup(()=>Promise.resolve({ok:false,status:502,statusText:'Bad Gateway',headers:{get:()=>'text/html'},json:()=>Promise.reject(new SyntaxError('Unexpected token <')),text:()=>Promise.resolve(page)}));const {d,w}=env;
 test_(d).click();await settle();
 expect(summary(d)).toBe('Connection test failed. The broker answered HTTP 502 with a page, not a test result.');
 expect(results(d).textContent).not.toContain('<');
 expect(results(d).textContent).not.toContain('Bad Gateway');
 expect(results(d).querySelector('h1,script')).toBe(null);
 expect(w.__owned).toBe(undefined);
});

t('Test Connection shows the broker refusal for an invalid field',async()=>{
 const env=await testSetup((u,o,j)=>j(400,{success:false,error:'CF API URL is required',code:'invalid_request'}));const {d}=env;
 test_(d).click();await settle();
 expect(summary(d)).toBe('Connection test failed. CF API URL is required');
 expect(rows(d)).toEqual([]);
});

t('Test Connection results clear when the dialog opens again',async()=>{
 const env=await testSetup((u,o,j)=>j(200,{success:true,checks:CHECKS_PASS}));const {d,manager}=env;
 test_(d).click();await settle();
 expect(results(d).hidden).toBe(false);
 manager.hideRegistrationModal();manager.showRegistrationModal();
 expect(results(d).hidden).toBe(true);
 expect(results(d).textContent).toBe('');
});

t('a result that lands after the dialog reopens is not shown',async()=>{
 let release;const gate=new Promise((r)=>{release=r;});
 const env=await testSetup((u,o,j)=>gate.then(()=>j(200,{success:true,checks:CHECKS_PASS})));const {d,manager}=env;
 test_(d).click();await settle();
 manager.hideRegistrationModal();manager.showRegistrationModal();
 release();await settle();
 expect(results(d).hidden).toBe(true);
 expect(test_(d).disabled).toBe(false);
});

// The saved-endpoint test posted to a route the broker never had.
t('the saved-endpoint connection test is gone',async()=>{
 const env=boot({noDialogs:true,oat:true,routes:QUIET});envs.push(env);await sleep(40);
 expect(env.w.CFEndpointManager).toBeDefined();
 expect(env.w.CFEndpointManager.testConnection).toBe(undefined);
 const src=require('fs').readFileSync(require('path').join(__dirname,'../../ui/blacksmith.js'),'utf8');
 expect(src).not.toContain('/test`');
});
