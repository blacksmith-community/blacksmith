const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// These checks record the exact request every Testing runner sends, so a
// change to the screens' layout can never change what reaches a live
// instance. They drive the page the way an operator does: open the tab, pick
// a command, fill its inputs, and press its button. The helpers below find
// those controls by role rather than by layout, so the same checks hold for
// the category cards and for the single command picker that replaced them.

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); }finally{ env.w.close(); } } });

const ctlCatalog=[
 {name:'nodes',display_name:'Nodes',description:'Node commands',commands:[
  {name:'ping',description:'Ping the RabbitMQ node',arguments:[],options:[],usage:'rabbitmqctl ping',examples:['rabbitmqctl ping'],dangerous:false}]},
 {name:'access',display_name:'Access',description:'Access commands',commands:[
  {name:'set_permissions',description:'Set user permissions',arguments:[
   {name:'username',description:'Username',required:true,type:'username'},
   {name:'conf',description:'Configure permission regex',required:true,type:'string'},
   {name:'write',description:'Write permission regex',required:true,type:'string'},
   {name:'read',description:'Read permission regex',required:true,type:'string'}],
   options:[{name:'-p',description:'Virtual host',type:'vhost',default:'/'}],usage:'rabbitmqctl set_permissions [-p vhost] <user> <conf> <write> <read>',examples:['rabbitmqctl set_permissions guest ".*" ".*" ".*"'],dangerous:false}]},
 {name:'users',display_name:'Users',description:'User commands',commands:[
  {name:'set_user_tags',description:'Set user tags',arguments:[
   {name:'username',description:'Username',required:true,type:'username'},
   {name:'tags',description:'Tags (administrator, monitoring, etc.)',required:false,type:'string'}],
   options:[],usage:'rabbitmqctl set_user_tags <username> [tag ...]',examples:['rabbitmqctl set_user_tags guest administrator'],dangerous:false}]},
 {name:'cluster',display_name:'Cluster',description:'Cluster commands',commands:[
  {name:'join_cluster',description:'Join a cluster',arguments:[{name:'node',description:'Node to join',required:true,type:'string'}],
   options:[{name:'--ram',description:'Join as a RAM node',type:'bool'}],usage:'rabbitmqctl join_cluster [--ram] <node>',examples:['rabbitmqctl join_cluster rabbit@server1'],dangerous:true}]}
];
const pluginsCatalog=[
 {name:'monitoring',display_name:'Monitoring',description:'Plugin listing',commands:[
  {name:'list',description:'Lists plugins',arguments:[{name:'pattern',description:'Pattern to filter plugin names',required:false,type:'string'}],
   options:[{name:'verbose',short:'v',description:'Show verbose output',type:'boolean'},{name:'enabled',short:'E',description:'Only enabled plugins',type:'boolean'}],
   usage:'rabbitmq-plugins list [pattern] [options]',examples:['rabbitmq-plugins list'],dangerous:false}]},
 {name:'plugin_management',display_name:'Plugin Management',description:'Enable and disable plugins',commands:[
  {name:'enable',description:'Enables plugins',arguments:[{name:'plugin_names',description:'Names of plugins to enable',required:true,type:'string[]'}],
   options:[{name:'online',description:'Contact the running node',type:'boolean'},{name:'offline',description:'Update the configuration files only',type:'boolean'}],
   usage:'rabbitmq-plugins enable <plugin_name> [<plugin_name>...] [options]',examples:['rabbitmq-plugins enable rabbitmq_shovel'],dangerous:true},
  {name:'directories',description:'Displays plugin directories',arguments:[],options:[],usage:'rabbitmq-plugins directories',examples:[],dangerous:false}]}
];
const findCmd=(catalog,cat,cmd)=>catalog.find(c=>c.name===cat).commands.find(c=>c.name===cmd);

const rabbitCreds={host:'h',username:'u',password:'p',vhost:'v',service_type:'rabbitmq'};
const redisCreds={host:'h',password:'p'};

// Opens the Testing screen for i1 and records every request the page sends.
// A route answers execute calls; held, when set, keeps one unanswered.
async function screen(creds,details={},opts={}){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 const sent=[]; const held={};
 const recorded=w.fetch;
 w.fetch=(u,o)=>{ sent.push({url:String(u),options:o===undefined?undefined:JSON.parse(JSON.stringify(o))}); return recorded(u,o); };
 const warnings=[];
 const feedback=w.Feedback;
 w.Feedback={...feedback,warning:(m,o)=>{warnings.push(m);},error:(m,o)=>{warnings.push(m);}};
 const result=(tool)=>({success:true,command:tool,arguments:[],output:'ok',exit_code:0,timestamp:1700000000});
 w.__routes.push(
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,details)],
  ['/config',(u,o,json)=>json(404,{})],
  ['/rabbitmq/rabbitmqctl/categories',(u,o,json)=>json(200,ctlCatalog)],
  [/\/rabbitmq\/rabbitmqctl\/category\/([^/]+)\/command\/([^/?]+)$/,(u,o,json)=>{const m=u.match(/category\/([^/]+)\/command\/([^/?]+)$/);return json(200,findCmd(ctlCatalog,m[1],m[2]));}],
  [/\/rabbitmq\/rabbitmqctl\/category\/([^/?]+)$/,(u,o,json)=>json(200,ctlCatalog.find(c=>u.endsWith('/'+c.name)))],
  ['/rabbitmq/rabbitmqctl/execute',(u,o,json)=>opts.held?new Promise(r=>{held.release=()=>r(json(200,result('ping')));}):json(200,result('ping'))],
  ['/rabbitmq/plugins/categories',(u,o,json)=>json(200,pluginsCatalog)],
  [/\/rabbitmq\/plugins\/category\/([^/]+)\/command\/([^/?]+)$/,(u,o,json)=>{const m=u.match(/category\/([^/]+)\/command\/([^/?]+)$/);return json(200,findCmd(pluginsCatalog,m[1],m[2]));}],
  [/\/rabbitmq\/plugins\/category\/([^/?]+)$/,(u,o,json)=>json(200,pluginsCatalog.find(c=>u.endsWith('/'+c.name)))],
  ['/rabbitmq/plugins/execute',(u,o,json)=>json(200,result('list'))],
  ['/rabbitmq/plugins/history',(u,o,json)=>json(200,[])],
  [/\/b\/i1\/(redis|rabbitmq)\/[a-z-]+/,(u,o,json)=>json(200,{success:true}),]);
 const html=await w.__T.renderServiceTesting('i1');
 const host=d.createElement('div'); host.className='detail-content'; host.innerHTML=html; d.body.appendChild(host);
 return {...env,sent,warnings,held,content:()=>d.getElementById('operation-content-i1')};
}

// Runs a control's inline handler the way a browser does, then clicks it.
function press(w,el){
 if(!el) throw new Error('control not found');
 const code=el.getAttribute('onclick');
 if(code){ const handler=(event)=>{ el.removeEventListener('click',handler); new w.Function('event',code).call(el,event); }; el.addEventListener('click',handler); }
 el.click();
}
async function openTab(s,operation){ s.w.switchTestingOperation('i1',operation); await sleep(120); }
// Chooses a command from the picker, or from the category cards and the
// commands table where those still exist.
async function choose(s,category,command){
 const {w,d}=s; const root=s.content();
 const picker=root.querySelector('select[data-runner-picker]');
 if(picker){
  const option=[...picker.options].find(o=>o.dataset.category===category&&o.dataset.command===command);
  picker.value=option.value; picker.dispatchEvent(new w.Event('change',{bubbles:true}));
 }else{
  press(w,[...root.querySelectorAll('.category-item')].find(el=>el.getAttribute('onclick').includes(`'${category}'`))); await sleep(30);
  press(w,[...root.querySelectorAll('.command-row')].find(el=>el.getAttribute('onclick').includes(`'${command}'`)));
 }
 await sleep(30);
}
// Fills the argument inputs in catalog order. One shared input gets the
// values joined by spaces, which is how an operator typed them there.
function fill(s,values){
 const inputs=[...s.content().querySelectorAll('input[type="text"]')];
 if(inputs.length===values.length) inputs.forEach((input,i)=>{ input.value=values[i]; });
 else if(inputs.length===1) inputs[0].value=values.filter(v=>v).join(' ');
 else throw new Error(`expected ${values.length} argument inputs, found ${inputs.length}`);
}
const runButton=(s)=>[...s.content().querySelectorAll('button')].find(b=>b.textContent.trim()==='Run');
const requests=(s,pattern)=>s.sent.filter(r=>pattern.test(r.url)&&r.options?.method&&r.options.method!=='GET');
const jsonPost=(url,body)=>({url,options:{method:'POST',headers:{'Content-Type':'application/json'},body}});

const ctlCases=[
 {name:'a command with no arguments',cat:'nodes',cmd:'ping',values:[],body:'{"category":"nodes","command":"ping","arguments":[]}'},
 {name:'a command with several arguments',cat:'access',cmd:'set_permissions',values:['guest','.*','^amq\\.','.*'],body:'{"category":"access","command":"set_permissions","arguments":["guest",".*","^amq\\\\.",".*"]}'},
 {name:'a command with its optional argument left empty',cat:'users',cmd:'set_user_tags',values:['guest',''],body:'{"category":"users","command":"set_user_tags","arguments":["guest"]}'},
 {name:'a command with a space-separated argument',cat:'users',cmd:'set_user_tags',values:['guest','administrator monitoring'],body:'{"category":"users","command":"set_user_tags","arguments":["guest","administrator","monitoring"]}'},
 {name:'a dangerous command, which the page runs without a confirmation',cat:'cluster',cmd:'join_cluster',values:['rabbit@server1'],body:'{"category":"cluster","command":"join_cluster","arguments":["rabbit@server1"]}'}
];
ctlCases.forEach(c=>t(`rabbitmqctl sends ${c.name} unchanged`,async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'rabbitmqctl'); await choose(s,c.cat,c.cmd); fill(s,c.values);
 press(s.w,runButton(s)); await sleep(30);
 expect(requests(s,/rabbitmqctl/)).toStrictEqual([jsonPost('/b/i1/rabbitmq/rabbitmqctl/execute',c.body)]);
 expect(s.d.getElementById('dialog-modal')?.open||false).toBe(false);
}));

t('rabbitmqctl disables Run while a command runs and enables it after',async()=>{
 const s=await screen(rabbitCreds,{},{held:true});
 await openTab(s,'rabbitmqctl'); await choose(s,'nodes','ping');
 press(s.w,runButton(s)); await sleep(20);
 const button=s.content().querySelector('#rabbitmqctl-execute-btn-i1');
 expect(button.disabled).toBe(true);
 press(s.w,button); await sleep(20);
 expect(requests(s,/execute/).length).toBe(1);
 s.held.release(); await sleep(30);
 expect(button.disabled).toBe(false);
});

const pluginCases=[
 {name:'a command with no arguments',cat:'plugin_management',cmd:'directories',values:[],flags:[],body:'{"category":"plugin_management","command":"directories","arguments":[]}'},
 {name:'a command with an optional argument left empty and an option',cat:'monitoring',cmd:'list',values:[''],flags:['enabled'],body:'{"category":"monitoring","command":"list","arguments":["--enabled"]}'},
 {name:'a command with an argument and two options',cat:'monitoring',cmd:'list',values:[' rabbit '],flags:['verbose','enabled'],body:'{"category":"monitoring","command":"list","arguments":[" rabbit ","--verbose","--enabled"]}'},
 {name:'a dangerous command with a list argument',cat:'plugin_management',cmd:'enable',values:['rabbitmq_shovel  rabbitmq_federation'],flags:['online'],body:'{"category":"plugin_management","command":"enable","arguments":["rabbitmq_shovel","rabbitmq_federation","--online"]}'}
];
pluginCases.forEach(c=>t(`plugins sends ${c.name} unchanged`,async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'plugins'); await choose(s,c.cat,c.cmd); fill(s,c.values);
 c.flags.forEach(f=>{ s.d.getElementById(`plugins-opt-${f}-i1`).checked=true; });
 press(s.w,runButton(s)); await sleep(30);
 expect(requests(s,/plugins/)).toStrictEqual([jsonPost('/b/i1/rabbitmq/plugins/execute',c.body)]);
}));

// The old History button read the server's plugins history each time it
// opened, with a bare fetch and no options. Opening the history list does
// the same, and closing it reads nothing.
t('opening the plugins history reads GET /plugins/history each time',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'plugins');
 const reads=()=>s.sent.filter(r=>r.url.includes('/history'));
 expect(reads()).toEqual([]);
 const summary=s.content().querySelector('details.command-runner-history > summary');
 summary.click(); await sleep(30);
 expect(reads()).toStrictEqual([{url:'/b/i1/rabbitmq/plugins/history',options:undefined}]);
 summary.click(); await sleep(30);
 expect(reads().length).toBe(1);
 summary.click(); await sleep(30);
 expect(reads()).toStrictEqual([{url:'/b/i1/rabbitmq/plugins/history',options:undefined},{url:'/b/i1/rabbitmq/plugins/history',options:undefined}]);
});

// The old rabbitmqctl History button showed only this session's runs, so
// opening its list reads nothing from the server.
t('opening the rabbitmqctl history reads nothing from the server',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'rabbitmqctl'); await choose(s,'nodes','ping');
 press(s.w,runButton(s)); await sleep(30);
 s.content().querySelector('details.command-runner-history > summary').click(); await sleep(30);
 expect(s.sent.filter(r=>r.url.includes('/history'))).toEqual([]);
});

t('plugins refuses a missing required argument with the same message',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'plugins'); await choose(s,'plugin_management','enable'); fill(s,['']);
 press(s.w,runButton(s)); await sleep(30);
 expect(requests(s,/plugins/)).toEqual([]);
 expect(s.warnings).toEqual(['plugin_names is required.']);
});

t('plugins refuses to run before a command is chosen',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'plugins');
 await s.w.executeRabbitMQPluginsCommand('i1');
 expect(requests(s,/plugins/)).toEqual([]);
 expect(s.warnings).toEqual(['Please select a command first.']);
});

const rabbitBody=(fields)=>JSON.stringify({...fields,use_amqps:false,connection_type:'standard',connection_user:'',connection_vhost:'/'});
t('Publish sends the same request and refuses a missing message',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'publish');
 const {d}=s;
 d.getElementById('rabbitmq-queue-i1').value='orders';
 press(s.w,s.content().querySelector('button[onclick*="executeRabbitMQPublish"]')); await sleep(20);
 expect(s.warnings).toEqual(['Please provide both queue name and message.']);
 d.getElementById('rabbitmq-exchange-i1').value='ex';
 d.getElementById('rabbitmq-message-i1').value='hello "world"';
 d.getElementById('rabbitmq-persistent-i1').checked=true;
 press(s.w,s.content().querySelector('button[onclick*="executeRabbitMQPublish"]')); await sleep(20);
 expect(requests(s,/rabbitmq\/publish/)).toStrictEqual([{url:'/b/i1/rabbitmq/publish',options:{method:'POST',cache:'no-cache',headers:{'Content-Type':'application/json'},body:rabbitBody({queue:'orders',exchange:'ex',message:'hello "world"',persistent:true})}}]);
});

t('Consume sends the same request',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'consume');
 const {d}=s;
 d.getElementById('rabbitmq-consume-queue-i1').value='orders';
 d.getElementById('rabbitmq-count-i1').value='3';
 d.getElementById('rabbitmq-auto-ack-i1').checked=false;
 press(s.w,s.content().querySelector('button[onclick*="executeRabbitMQConsume"]')); await sleep(20);
 expect(requests(s,/rabbitmq\/consume/)).toStrictEqual([{url:'/b/i1/rabbitmq/consume',options:{method:'POST',cache:'no-cache',headers:{'Content-Type':'application/json'},body:rabbitBody({queue:'orders',count:3,timeout:5000,auto_ack:false})}}]);
});

t('deleting a queue sends nothing on Cancel and the same request on Confirm',async()=>{
 const s=await screen(rabbitCreds);
 await openTab(s,'queue-ops');
 const {d}=s;
 d.getElementById('rabbitmq-queue-ops-name-i1').value='orders';
 d.getElementById('rabbitmq-operation-i1').value='delete';
 const button=()=>s.content().querySelector('button[onclick*="executeRabbitMQQueueOps"]');
 press(s.w,button()); await sleep(20);
 d.getElementById('dialog-modal').querySelector('[data-dialog-role="cancel"]').click(); await sleep(20);
 expect(requests(s,/queue-ops/)).toEqual([]);
 press(s.w,button()); await sleep(20);
 expect(d.getElementById('dialog-modal').querySelector('.dialog-modal-message').textContent).toBe('Are you sure you want to delete queue "orders"?');
 d.getElementById('dialog-modal').querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expect(requests(s,/queue-ops/)).toStrictEqual([{url:'/b/i1/rabbitmq/queue-ops',options:{method:'POST',cache:'no-cache',headers:{'Content-Type':'application/json'},body:rabbitBody({queue:'orders',operation:'delete',durable:true})}}]);
});

t('Connect sends the same request',async()=>{
 const s=await screen(rabbitCreds);
 press(s.w,s.content().querySelector('#connect-btn-i1')); await sleep(20);
 const sent=s.sent.filter(r=>/rabbitmq\/test/.test(r.url));
 expect(sent.length).toBe(1);
 expect(sent[0].options).toStrictEqual({method:'GET',cache:'no-cache'});
 expect(sent[0].url).toMatch(/^\/b\/i1\/rabbitmq\/test\?/);
});

const redisBody=(fields)=>JSON.stringify({...fields,use_tls:false,connection_type:'standard'});
const redisPost=(op,fields)=>({url:`/b/i1/redis/${op}`,options:{method:'POST',cache:'no-cache',headers:{'Content-Type':'application/json'},body:redisBody(fields)}});
t('Redis Command sends the same request and refuses invalid JSON',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 await openTab(s,'command');
 const {d}=s;
 const button=()=>s.content().querySelector('button[onclick*="executeRedisCommand"]');
 press(s.w,button()); await sleep(20);
 expect(s.warnings).toEqual(['Please provide a command.']);
 d.getElementById('redis-cmd-i1').value='PING';
 press(s.w,button()); await sleep(20);
 d.getElementById('redis-args-i1').value='["a", 2';
 press(s.w,button()); await sleep(20);
 expect(s.warnings).toEqual(['Please provide a command.','Invalid JSON in arguments field.']);
 d.getElementById('redis-cmd-i1').value='TYPE';
 d.getElementById('redis-args-i1').value='["k1", 2]';
 press(s.w,button()); await sleep(20);
 expect(requests(s,/redis\/command/)).toStrictEqual([redisPost('command',{command:'PING',args:[]}),redisPost('command',{command:'TYPE',args:['k1',2]})]);
});

t('Redis Set and Get send the same requests',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 const {d}=s;
 await openTab(s,'set');
 d.getElementById('redis-key-i1').value='k';
 d.getElementById('redis-value-i1').value='v v';
 d.getElementById('redis-ttl-i1').value='30';
 press(s.w,s.content().querySelector('button[onclick*="executeRedisSet"]')); await sleep(20);
 await openTab(s,'get');
 d.getElementById('redis-get-key-i1').value='k';
 press(s.w,s.content().querySelector('button[onclick*="executeRedisGet"]')); await sleep(20);
 expect(requests(s,/redis\/(set|get)/)).toStrictEqual([redisPost('set',{key:'k',value:'v v',ttl:30}),redisPost('get',{key:'k'})]);
});

t('Redis Flush sends nothing on Cancel and the same request on Confirm',async()=>{
 const s=await screen(redisCreds,{service_name:'redis'});
 await openTab(s,'flush');
 const {d}=s;
 d.getElementById('redis-flush-db-i1').value='2';
 const button=()=>s.content().querySelector('button[onclick*="executeRedisFlush"]');
 press(s.w,button()); await sleep(20);
 d.getElementById('dialog-modal').querySelector('[data-dialog-role="cancel"]').click(); await sleep(20);
 expect(requests(s,/redis\/flush/)).toEqual([]);
 press(s.w,button()); await sleep(20);
 expect(d.getElementById('dialog-modal').querySelector('.dialog-modal-message').textContent).toBe('Are you sure you want to flush the database? This will delete all data!');
 d.getElementById('dialog-modal').querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expect(requests(s,/redis\/flush/)).toStrictEqual([redisPost('flush',{database:2})]);
});
