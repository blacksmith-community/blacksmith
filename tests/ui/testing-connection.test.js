const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const css=fs.readFileSync(path.join(UI,'blacksmith.css'),'utf8');
const envs=[];
afterEach(()=>{ while(envs.length) envs.pop().w.close(); });

const redisCreds={host:'10.0.0.5',port:6379,tls_port:6380,password:'s3cr3t-test'};
const rabbitCreds={host:'10.0.0.6',port:5672,username:'app',password:'s3cr3t-test',vhost:'orders',service_type:'rabbitmq',
 protocols:{amqp:{host:'10.0.0.6',port:5672},amqps:{host:'10.0.0.6',port:5671}}};

// Opens the Testing screen for i1 with every request stubbed.
async function screen(creds,details={},routes=[]){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.__routes.push(...routes,
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,details)],
  ['/config',(u,o,json)=>json(404,{})],
  [/\/b\/i1\/(redis|rabbitmq)\/[a-z-]+/,(u,o,json)=>json(200,{success:true})]);
 const html=await w.__T.renderServiceTesting('i1');
 const host=d.createElement('div'); host.className='detail-content'; host.innerHTML=html; d.body.appendChild(host);
 return {...env,content:()=>d.getElementById('operation-content-i1'),host};
}
function press(w,el){
 const code=el.getAttribute('onclick');
 const handler=(event)=>{ el.removeEventListener('click',handler); new w.Function('event',code).call(el,event); };
 el.addEventListener('click',handler);
 el.click();
}
const facts=(root)=>Object.fromEntries([...root.querySelectorAll('#connection-target-i1 > div')].map((x)=>[x.querySelector('dt').textContent,x.querySelector('dd').textContent.trim()]));
const change=(w,el,value)=>{ el.value=value; el.dispatchEvent(new w.Event('change',{bubbles:true})); new w.Function('event',el.getAttribute('onchange')).call(el,{}); };

t('the Connection tab is a form with a heading, a description, and one row ending in Connect',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 const root=s.content();
 expect(root.querySelector('table')).toBeNull();
 const form=root.querySelector('div.operation-form');
 expect(form.querySelector('h5').textContent).toBe('Test connection');
 expect(form.querySelector('h5 + p').textContent).toBe('Test a connection to this instance with its own credentials.');
 const row=form.querySelector('.connection-row');
 const kids=[...row.children];
 expect(kids[0].classList.contains('connection-field')).toBe(true);
 expect(kids[0].querySelector('label').textContent).toBe('Type');
 expect(kids[kids.length-1].id).toBe('connect-btn-i1');
 expect(kids[kids.length-1].classList.contains('btn-primary')).toBe(true);
 expect(css).toMatch(/\.connection-selector\.connection-row \{[^}]*align-items:\s*flex-end/);
});

t('RabbitMQ lays out Type, User, and VHost in one row',async()=>{
 const s=await screen(rabbitCreds);
 const labels=[...s.content().querySelectorAll('.connection-row .connection-field label')].map((x)=>x.textContent);
 expect(labels).toEqual(['Type','User','VHost']);
});

t('the target shows host and port, and the TLS port once TLS is picked',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 expect(facts(s.content())).toEqual({Host:'10.0.0.5',Port:'6379'});
 change(s.w,s.d.getElementById('connection-type-i1'),'tls');
 expect(facts(s.content())).toEqual({Host:'10.0.0.5',Port:'6380'});
 expect(s.content().textContent).not.toContain('s3cr3t-test');
});

t('the RabbitMQ target follows the type, user, and vhost picks',async()=>{
 const s=await screen(rabbitCreds);
 expect(facts(s.content())).toEqual({Host:'10.0.0.6',Port:'5672',User:'app',VHost:'orders'});
 change(s.w,s.d.getElementById('connection-type-i1'),'amqps');
 change(s.w,s.d.getElementById('connection-vhost-i1'),'/');
 expect(facts(s.content())).toEqual({Host:'10.0.0.6',Port:'5671',User:'app',VHost:'/'});
 expect(s.content().textContent).not.toContain('s3cr3t-test');
});

t('a missing target value is the shared empty value',async()=>{
 const s=await screen({host:'10.0.0.5',port:6379,password:'s3cr3t-test'},{service_name:'redis'});
 change(s.w,s.d.getElementById('connection-type-i1'),'tls');
 expect(facts(s.content()).Port).toBe('—');
 expect(s.content().querySelector('#connection-target-i1 .empty-value')).not.toBeNull();
});

t('one line under the button reports the state of the connection',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 const line=()=>s.d.getElementById('connection-status-i1');
 expect(line().getAttribute('role')).toBe('status');
 expect(line().textContent.trim()).toBe('Not tested yet');
 expect(line().querySelector('.connection-status-indicator').getAttribute('data-state')).toBe('idle');
 press(s.w,s.d.getElementById('connect-btn-i1')); await sleep(30);
 expect(line().textContent.trim()).toBe('Connected');
 expect(line().querySelector('.connection-status-indicator').getAttribute('data-state')).toBe('connected');
});

t('a failed attempt shows its error in the same line',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'},[[/redis\/test/,(u,o,json)=>json(502,{error:'connection refused'})]]);
 press(s.w,s.d.getElementById('connect-btn-i1')); await sleep(30);
 const line=s.d.getElementById('connection-status-i1');
 expect(line.textContent.trim()).toBe('Connection failed. connection refused');
 expect(line.querySelector('.connection-status-text').getAttribute('data-result')).toBe('failed');
});

t('an empty response history is one muted line and Clear history is disabled',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 const entries=s.d.getElementById('history-entries-i1');
 const clear=s.d.getElementById('clear-history-i1');
 expect(entries.classList.contains('is-empty')).toBe(true);
 expect(entries.querySelector('.no-data')).toBeNull();
 expect(entries.textContent.trim()).toBe('No operations performed yet');
 expect(clear.disabled).toBe(true);
 expect(css).toMatch(/\.history-entries\.is-empty \{[^}]*border:\s*0/);
 press(s.w,s.d.getElementById('connect-btn-i1')); await sleep(30);
 expect(entries.classList.contains('is-empty')).toBe(false);
 expect(clear.disabled).toBe(false);
 press(s.w,clear);
 expect(entries.classList.contains('is-empty')).toBe(true);
 expect(clear.disabled).toBe(true);
});
