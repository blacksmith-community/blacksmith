const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// The rabbitmqctl and rabbitmq-plugins tabs share one command runner: a
// picker grouped by category with a filter, the chosen command laid out as a
// shell line with one input per argument and Run at the end, and one output
// panel under it with its history. These checks hold that layout in place.

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); }finally{ env.w.close(); } } });

const ctlCatalog=[
 {name:'nodes',display_name:'Nodes',description:'Node commands',commands:[
  {name:'ping',description:'Ping the RabbitMQ node',arguments:[],options:[],usage:'rabbitmqctl ping',examples:['rabbitmqctl ping'],dangerous:false},
  {name:'status',description:'Show node status',arguments:[],options:[],usage:'rabbitmqctl status',examples:[],dangerous:false}]},
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
   options:[],usage:'rabbitmqctl set_user_tags <username> [tag ...]',examples:[],dangerous:false}]},
 {name:'empty',display_name:'Empty',description:'No commands',commands:[]},
 {name:'cluster',display_name:'Cluster',description:'Cluster commands',commands:[
  {name:'join_cluster',description:'Join a cluster',arguments:[{name:'node',description:'Node to join',required:true,type:'string'}],
   options:[{name:'--ram',description:'Join as a RAM node',type:'bool'}],usage:'rabbitmqctl join_cluster [--ram] <node>',examples:[],dangerous:true}]}
];
const pluginsCatalog=[
 {name:'monitoring',display_name:'Monitoring',description:'Plugin listing',commands:[
  {name:'list',description:'Lists plugins',arguments:[{name:'pattern',description:'Pattern to filter plugin names',required:false,type:'string'}],
   options:[{name:'verbose',short:'v',description:'Show verbose output',type:'boolean'},{name:'enabled',short:'E',description:'Only enabled plugins',type:'boolean'}],
   usage:'rabbitmq-plugins list [pattern] [options]',examples:['rabbitmq-plugins list'],dangerous:false}]}
];
const findCmd=(catalog,cat,cmd)=>catalog.find(c=>c.name===cat).commands.find(c=>c.name===cmd);
const creds={host:'h',username:'u',password:'p',vhost:'v',service_type:'rabbitmq'};

// Opens the Testing screen for i1. results answers execute calls in turn;
// a result of 'hold' stays unanswered until release() runs.
async function screen(results=[],pluginsHistory=[]){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 const held={};
 let call=0;
 const answer=(json)=>{ const r=results[call++]||{success:true,output:'ok',exit_code:0}; return r==='hold'?new Promise(done=>{held.release=(v)=>done(json(200,v));}):json(200,r); };
 w.__routes.push(
  ['/credentials',(u,o,json)=>json(200,creds)],
  ['/details',(u,o,json)=>json(200,{})],
  ['/config',(u,o,json)=>json(404,{})],
  ['/rabbitmq/rabbitmqctl/categories',(u,o,json)=>json(200,ctlCatalog)],
  [/\/rabbitmq\/rabbitmqctl\/category\/([^/]+)\/command\/([^/?]+)$/,(u,o,json)=>{const m=u.match(/category\/([^/]+)\/command\/([^/?]+)$/);return json(200,findCmd(ctlCatalog,m[1],m[2]));}],
  ['/rabbitmq/rabbitmqctl/execute',(u,o,json)=>answer(json)],
  ['/rabbitmq/plugins/categories',(u,o,json)=>json(200,pluginsCatalog)],
  [/\/rabbitmq\/plugins\/category\/([^/]+)\/command\/([^/?]+)$/,(u,o,json)=>{const m=u.match(/category\/([^/]+)\/command\/([^/?]+)$/);return json(200,findCmd(pluginsCatalog,m[1],m[2]));}],
  ['/rabbitmq/plugins/execute',(u,o,json)=>answer(json)],
  ['/rabbitmq/plugins/history',(u,o,json)=>json(200,typeof pluginsHistory==='function'?pluginsHistory():pluginsHistory)],
  [/\/b\/i1\/(redis|rabbitmq)\/[a-z-]+/,(u,o,json)=>json(200,{success:true})]);
 const html=await w.__T.renderServiceTesting('i1');
 const host=d.createElement('div'); host.className='detail-content'; host.innerHTML=html; d.body.appendChild(host);
 return {...env,held,content:()=>d.getElementById('operation-content-i1')};
}
async function openTab(s,operation){ s.w.switchTestingOperation('i1',operation); await sleep(60); }
const picker=(s)=>s.content().querySelector('select[data-runner-picker]');
async function choose(s,category,command){
 const select=picker(s);
 select.value=[...select.options].find(o=>o.dataset.category===category&&o.dataset.command===command).value;
 select.dispatchEvent(new s.w.Event('change',{bubbles:true})); await sleep(30);
}
const text=(el)=>el.textContent.replace(/\s+/g,' ').trim();
const runButton=(s)=>s.content().querySelector('[data-runner-action="run"]');
const visible=(el)=>!!el&&!el.hidden&&!el.closest('[hidden]');

t('rabbitmqctl lists every command in one picker grouped by category',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl');
 const root=s.content();
 const select=picker(s);
 expect(root.querySelectorAll('select[data-runner-picker]').length).toBe(1);
 expect(select.disabled).toBe(false);
 expect([...select.querySelectorAll('optgroup')].map(g=>g.label)).toEqual(['Nodes (2 commands)','Access (1 command)','Users (1 command)','Cluster (1 command)']);
 expect([...select.querySelectorAll('optgroup option')].map(o=>o.textContent)).toEqual(['ping','status','set_permissions','set_user_tags','join_cluster']);
 expect(select.options[0].value).toBe('');
 expect(s.d.querySelector(`label[for="${select.id}"]`).textContent).toBe('Command');
 expect(text(root.querySelector('.command-runner-count'))).toBe('5 commands');
 // No category cards, no commands table, and no placeholder boxes.
 expect(root.querySelector('.category-item, .categories-list, .commands-table, .command-row')).toBeNull();
 expect(root.querySelector('table')).toBeNull();
 expect(root.textContent).not.toMatch(/Select a category/);
 expect(root.textContent).not.toMatch(/\b1 commands\b|\b1 args\b/);
});

t('the filter narrows the picker by name or description and keeps the choice',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl');
 const filter=s.content().querySelector('input[type="search"][data-runner-filter]');
 expect(s.d.querySelector(`label[for="${filter.id}"]`).textContent).toBe('Filter');
 const names=()=>[...picker(s).querySelectorAll('optgroup option')].map(o=>o.textContent);
 const type=(v)=>{ filter.value=v; filter.dispatchEvent(new s.w.Event('input',{bubbles:true})); };
 type('PERM'); expect(names()).toEqual(['set_permissions']);
 expect([...picker(s).querySelectorAll('optgroup')].map(g=>g.label)).toEqual(['Access (1 command)']);
 expect(text(s.content().querySelector('.command-runner-count'))).toBe('1 of 5 commands');
 type('zzz'); expect(names()).toEqual([]);
 expect(picker(s).options[0].textContent).toBe('No commands match the filter');
 type('status'); expect(names()).toEqual(['status']);
 type('ping the'); expect(names()).toEqual(['ping']);
 await choose(s,'nodes','ping');
 type('cluster'); expect(names()).toEqual(['ping','join_cluster']);
 expect(picker(s).value).toBe('nodes/ping');
 type(''); expect(names().length).toBe(5);
 expect(text(s.content().querySelector('.command-runner-count'))).toBe('5 commands');
});

t('a command reads as a shell line with one labeled input per argument and Run last',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl'); await choose(s,'access','set_permissions');
 const line=s.content().querySelector('.command-runner-line');
 expect(text(line.querySelector('code.command-runner-command'))).toBe('rabbitmqctl set_permissions');
 const inputs=[...line.querySelectorAll('input[type="text"]')];
 expect(inputs.map(i=>i.id)).toEqual(['username','conf','write','read'].map(n=>`rabbitmqctl-arg-${n}-i1`));
 expect(inputs.map(i=>i.placeholder)).toEqual(['Username','Configure permission regex','Write permission regex','Read permission regex']);
 expect(inputs.map(i=>text(s.d.querySelector(`label[for="${i.id}"]`)))).toEqual(['username','conf','write','read']);
 expect(inputs.every(i=>i.required)).toBe(true);
 const run=line.lastElementChild;
 expect(run.tagName).toBe('BUTTON'); expect(run.textContent).toBe('Run');
 expect(run.classList.contains('btn-primary')).toBe(true);
 expect(s.content().querySelectorAll('input[type="text"]').length).toBe(4);
 expect(text(s.content().querySelector('.command-runner-description'))).toBe('Set user permissions · 4 arguments');
 const help=s.content().querySelector('details.command-runner-help');
 expect(help.open).toBe(false);
 expect(text(help.querySelector('summary'))).toBe('Usage and examples');
 expect(help.textContent).toContain('rabbitmqctl set_permissions [-p vhost] <user> <conf> <write> <read>');
 expect(help.textContent).toContain('rabbitmqctl set_permissions guest ".*" ".*" ".*"');
 expect(s.content().querySelector('table')).toBeNull();
 expect(s.content().querySelector('.command-runner-danger')).toBeNull();
});

t('the plurals read 1 argument, 0 arguments, and an optional marker',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl');
 await choose(s,'cluster','join_cluster');
 expect(text(s.content().querySelector('.command-runner-description'))).toBe('Join a cluster · 1 argument');
 expect(s.content().querySelector('.command-runner-danger[role="note"][data-variant="warning"]').textContent).toContain('This is a dangerous command that may affect the system');
 await choose(s,'users','set_user_tags');
 expect(text(s.d.querySelector('label[for="rabbitmqctl-arg-tags-i1"]'))).toBe('tags optional');
 expect(s.d.getElementById('rabbitmqctl-arg-tags-i1').required).toBe(false);
 await choose(s,'nodes','ping');
 expect(text(s.content().querySelector('.command-runner-description'))).toBe('Ping the RabbitMQ node · 0 arguments');
});

t('a command with no arguments shows only the line and Run',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl'); await choose(s,'nodes','ping');
 const line=s.content().querySelector('.command-runner-line');
 expect([...line.children].map(el=>el.tagName)).toEqual(['CODE','BUTTON']);
 expect(s.content().querySelectorAll('input[type="text"]').length).toBe(0);
});

t('the output panel waits with one muted line, then shows the run',async()=>{
 const s=await screen([{success:true,command:'ping',output:'Ping succeeded',exit_code:0},{success:false,output:'',error:'node down',exit_code:69}]);
 await openTab(s,'rabbitmqctl');
 const panel=s.content().querySelector('.command-runner-output');
 const empty=panel.querySelector('.command-runner-empty');
 expect(visible(empty)).toBe(true);
 expect(empty.textContent).toBe('Choose a command and select Run.');
 expect(visible(panel.querySelector('pre'))).toBe(false);
 expect(visible(panel.querySelector('.command-runner-output-header'))).toBe(false);
 expect(panel.textContent).not.toMatch(/No command executed yet/);
 // The panel sits directly under the form.
 expect(s.content().querySelector('.command-runner-form').nextElementSibling).toBe(panel);
 await choose(s,'nodes','ping');
 runButton(s).click(); await sleep(30);
 const header=panel.querySelector('.command-runner-output-header');
 expect(visible(empty)).toBe(false);
 expect(text(header.querySelector('code'))).toBe('rabbitmqctl ping');
 expect(header.querySelector('.badge[data-variant="success"]').textContent).toBe('Success');
 expect(header.textContent).toContain('Exit code 0');
 expect(text(header)).toMatch(/\d+ ms/);
 expect(panel.querySelector('pre').textContent).toBe('Ping succeeded');
 expect(header.querySelector('[data-runner-action="copy"]').getAttribute('aria-label')).toBe('Copy output');
 expect(header.querySelector('[data-runner-action="copy"] svg')).not.toBeNull();
 expect(header.querySelector('[data-runner-action="clear"]').getAttribute('aria-label')).toBe('Clear output');
 runButton(s).click(); await sleep(30);
 expect(header.querySelector('.badge[data-variant="danger"]').textContent).toBe('Failure');
 expect(header.textContent).toContain('Exit code 69');
 expect(panel.querySelector('pre').textContent).toBe('node down');
 header.querySelector('[data-runner-action="clear"]').click();
 expect(visible(empty)).toBe(true);
 expect(visible(panel.querySelector('pre'))).toBe(false);
});

t('a running command shows the running pill and Copy writes the output',async()=>{
 const s=await screen(['hold']);
 const copied=[]; s.w.navigator.clipboard={writeText:(v)=>{copied.push(v);return Promise.resolve();}};
 await openTab(s,'rabbitmqctl'); await choose(s,'nodes','ping');
 runButton(s).click(); await sleep(20);
 const header=s.content().querySelector('.command-runner-output-header');
 expect(header.querySelector('.status-badge[data-status="active"]').textContent).toBe('Running');
 expect(header.querySelector('[data-runner-action="copy"]')).toBeNull();
 s.held.release({success:true,output:'pong',exit_code:0}); await sleep(30);
 header.querySelector('[data-runner-action="copy"]').click(); await sleep(20);
 expect(copied).toEqual(['pong']);
});

t('history is a collapsed list under the output and an entry restores its output',async()=>{
 const s=await screen([{success:true,output:'first',exit_code:0},{success:true,output:'second',exit_code:0}]);
 await openTab(s,'rabbitmqctl');
 const history=s.content().querySelector('details.command-runner-history');
 expect(visible(history)).toBe(false);
 expect(s.content().querySelector('.command-runner-output').nextElementSibling).toBe(history);
 await choose(s,'nodes','ping'); runButton(s).click(); await sleep(30);
 await choose(s,'nodes','status'); runButton(s).click(); await sleep(30);
 expect(visible(history)).toBe(true);
 expect(history.open).toBe(false);
 expect(text(history.querySelector('summary'))).toBe('History (2 runs)');
 const entries=[...history.querySelectorAll('[data-runner-action="restore"]')];
 expect(entries.map(e=>text(e.querySelector('code')))).toEqual(['rabbitmqctl status','rabbitmqctl ping']);
 entries[1].click();
 expect(s.content().querySelector('.command-runner-output pre').textContent).toBe('first');
 expect(text(s.content().querySelector('.command-runner-output-header code'))).toBe('rabbitmqctl ping');
 const clear=history.querySelector('[data-runner-action="clear-history"]');
 expect(clear.textContent).toBe('Clear history');
 expect(clear.getAttribute('data-variant')).toBe('secondary');
});

t('clearing the history asks first, then empties the list',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl'); await choose(s,'nodes','ping'); runButton(s).click(); await sleep(30);
 const history=s.content().querySelector('details.command-runner-history');
 history.querySelector('[data-runner-action="clear-history"]').click(); await sleep(10);
 const dialog=s.d.getElementById('dialog-modal');
 expect(dialog.querySelector('.dialog-modal-message').textContent).toBe('Are you sure you want to clear all command history?');
 dialog.querySelector('[data-dialog-role="confirm"]').click(); await sleep(20);
 expect(history.querySelectorAll('li').length).toBe(0);
 expect(visible(history)).toBe(false);
});

// The server's plugins history joins this session's runs in the list. A run
// the server also recorded shows once, and every entry restores its output.
t('the plugins history shows the server entries beside this session\'s runs',async()=>{
 const s=await screen([{success:true,output:'listed here',exit_code:0}],()=>[
  {timestamp:Date.now(),category:'monitoring',command:'list',arguments:['--enabled'],success:true,duration_ms:40,execution_id:'e2',output_sample:'listed on the server'},
  {timestamp:Date.UTC(2026,9,30,12),category:'plugin_management',command:'enable',arguments:['rabbitmq_shovel'],success:false,duration_ms:900,execution_id:'e1',output_sample:'enable failed'}]);
 await openTab(s,'plugins');
 const history=s.content().querySelector('details.command-runner-history');
 expect(visible(history)).toBe(true);
 expect(history.open).toBe(false);
 expect(text(history.querySelector('summary'))).toBe('History');
 await choose(s,'monitoring','list');
 s.d.getElementById('plugins-opt-enabled-i1').checked=true;
 runButton(s).click(); await sleep(30);
 history.querySelector('summary').click(); await sleep(30);
 expect(history.open).toBe(true);
 expect(text(history.querySelector('summary'))).toBe('History (2 runs)');
 const entries=[...history.querySelectorAll('[data-runner-action="restore"]')];
 expect(entries.map(e=>text(e.querySelector('code')))).toEqual(['rabbitmq-plugins list --enabled','rabbitmq-plugins enable rabbitmq_shovel']);
 expect(entries.map(e=>text(e.querySelector('.badge')))).toEqual(['Success','Failure']);
 entries[1].click();
 expect(s.content().querySelector('.command-runner-output pre').textContent).toBe('enable failed');
 expect(text(s.content().querySelector('.command-runner-output-header code'))).toBe('rabbitmq-plugins enable rabbitmq_shovel');
 expect(text(s.content().querySelector('.command-runner-output-header'))).toContain('900 ms');
 entries[0].click();
 expect(s.content().querySelector('.command-runner-output pre').textContent).toBe('listed here');
});

// A server entry whose timestamp no Date can hold keeps its place in the
// list with a dash for its time, and the entries around it still render.
t('a plugins history entry with an out-of-range timestamp shows a dash',async()=>{
 const s=await screen([],[
  {timestamp:Date.UTC(2026,9,30,12),category:'monitoring',command:'list',arguments:[],success:true,duration_ms:20,execution_id:'e1',output_sample:'listed'},
  {timestamp:1e20,category:'plugin_management',command:'enable',arguments:['rabbitmq_shovel'],success:false,duration_ms:10,execution_id:'e2',output_sample:'far future'}]);
 await openTab(s,'plugins');
 const history=s.content().querySelector('details.command-runner-history');
 history.querySelector('summary').click(); await sleep(30);
 expect(text(history.querySelector('summary'))).toBe('History (2 runs)');
 const entries=[...history.querySelectorAll('[data-runner-action="restore"]')];
 expect(entries.map(e=>text(e.querySelector('code')))).toEqual(['rabbitmq-plugins list','rabbitmq-plugins enable rabbitmq_shovel']);
 expect(entries[0].querySelector('time').getAttribute('datetime')).toBe('2026-10-30T12:00:00.000Z');
 expect(entries[1].querySelector('time')).toBeNull();
 expect(text(entries[1].querySelector('.command-runner-history-time'))).toBe('—');
 expect(entries[1].querySelector('.command-runner-history-time .empty-value')).not.toBeNull();
 entries[1].click();
 expect(s.content().querySelector('.command-runner-output pre').textContent).toBe('far future');
});

t('an empty plugins history says so once it loads',async()=>{
 const s=await screen();
 await openTab(s,'plugins');
 const history=s.content().querySelector('details.command-runner-history');
 history.querySelector('summary').click(); await sleep(30);
 expect(history.open).toBe(true);
 expect(history.querySelectorAll('li').length).toBe(0);
 expect(text(history.querySelector('.command-runner-history-note'))).toBe('No command history available');
});

// A dangerous command opens the page's own dialog: the command line as it
// will run in monospace, the catalog's warning under it, Cancel focused, a
// danger Run button, and the shared close button. Nothing else gets it.
t('a dangerous command asks first in the page dialog',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl'); await choose(s,'cluster','join_cluster');
 s.d.getElementById('rabbitmqctl-arg-node-i1').value='rabbit@server1';
 runButton(s).click(); await sleep(30);
 const dialog=s.d.getElementById('dialog-modal');
 expect(dialog.open).toBe(true);
 expect(dialog.querySelector('.dialog-modal-title').textContent).toBe('Run rabbitmqctl join_cluster?');
 const line=dialog.querySelector('.dialog-modal-body code.dialog-modal-command');
 expect(line.textContent).toBe('rabbitmqctl join_cluster rabbit@server1');
 expect(line.nextElementSibling.textContent).toBe('This is a dangerous command that may affect the system');
 const cancel=dialog.querySelector('[data-dialog-role="cancel"]');
 const run=dialog.querySelector('[data-dialog-role="confirm"]');
 expect(cancel.textContent).toBe('Cancel');
 expect(s.d.activeElement).toBe(cancel);
 expect(run.textContent).toBe('Run');
 expect(run.classList.contains('dialog-modal-btn-danger')).toBe(true);
 const close=dialog.querySelector('.dialog-modal-header button.dialog-close');
 expect(close.getAttribute('aria-label')).toBe('Close');
 expect(close.querySelector('svg')).not.toBeNull();
 expect(dialog.getAttribute('aria-describedby')).toContain('dialog-modal-run');
 run.click(); await sleep(30);
 expect(dialog.open).toBe(false);
 expect(dialog.querySelector('.dialog-close, .dialog-modal-command')).toBeNull();
 expect(text(s.content().querySelector('.command-runner-output-header code'))).toBe('rabbitmqctl join_cluster rabbit@server1');
 const history=s.content().querySelector('details.command-runner-history');
 history.querySelector('[data-runner-action="clear-history"]').click(); await sleep(10);
 expect(dialog.open).toBe(true);
 expect(dialog.querySelector('.dialog-close')).toBeNull();
 dialog.querySelector('[data-dialog-role="cancel"]').click(); await sleep(10);
 const css=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.css'),'utf8');
 expect(css).toMatch(/\.dialog-modal-command\s*\{[^}]*font-family:\s*var\(--font-mono\)/);
});

t('plugins uses the same runner with its options as checkboxes',async()=>{
 const s=await screen();
 await openTab(s,'plugins');
 expect([...picker(s).querySelectorAll('optgroup')].map(g=>g.label)).toEqual(['Monitoring (1 command)']);
 expect(text(s.content().querySelector('.command-runner-count'))).toBe('1 command');
 await choose(s,'monitoring','list');
 const line=s.content().querySelector('.command-runner-line');
 expect(text(line.querySelector('code.command-runner-command'))).toBe('rabbitmq-plugins list');
 expect([...line.querySelectorAll('input[type="checkbox"]')].map(i=>i.id)).toEqual(['plugins-opt-verbose-i1','plugins-opt-enabled-i1']);
 expect(line.lastElementChild.textContent).toBe('Run');
 expect(s.content().querySelector('.category-item, table')).toBeNull();
 s.d.getElementById('plugins-arg-pattern-i1').value='rabbit';
 s.d.getElementById('plugins-opt-enabled-i1').checked=true;
 runButton(s).click(); await sleep(30);
 expect(text(s.content().querySelector('.command-runner-output-header code'))).toBe('rabbitmq-plugins list rabbit --enabled');
});

t('the shared Response History hides on the runner tabs and returns elsewhere',async()=>{
 const s=await screen();
 const shared=s.d.querySelector('.service-testing-container > .testing-history');
 await openTab(s,'rabbitmqctl'); expect(shared.style.display).toBe('none');
 await openTab(s,'publish'); expect(shared.style.display).toBe('');
 await openTab(s,'plugins'); expect(shared.style.display).toBe('none');
 await openTab(s,'rabbitmqctl'); expect(shared.style.display).toBe('none');
 await openTab(s,'test'); expect(shared.style.display).toBe('');
 expect(s.content().querySelectorAll('.testing-history').length).toBe(0);
});

t('the runner keeps its chosen command when its tab comes back',async()=>{
 const s=await screen();
 await openTab(s,'rabbitmqctl'); await choose(s,'access','set_permissions');
 await openTab(s,'publish'); await openTab(s,'rabbitmqctl');
 expect(picker(s).value).toBe('access/set_permissions');
 expect(s.content().querySelectorAll('input[type="text"]').length).toBe(4);
 runButton(s).click(); await sleep(30);
 expect(s.content().querySelector('.command-runner-output .badge').textContent).toBe('Success');
});

test('the runner styles its line, output, and history in blacksmith.css',()=>{
 const css=fs.readFileSync(path.join(__dirname,'../../ui/blacksmith.css'),'utf8');
 for(const sel of ['.command-runner-picker','.command-runner-line','.command-runner-output','.command-runner-pre','.command-runner-empty','.command-runner-history','.command-runner-icon-btn']) expect(css).toContain(`${sel} {`);
 const pre=css.match(/\.command-runner-pre \{([^}]*)\}/)[1];
 expect(pre).toMatch(/font-family: var\(--font-mono\)/);
 const empty=css.match(/\.command-runner-empty \{([^}]*)\}/)[1];
 expect(empty).not.toMatch(/background/);
});
