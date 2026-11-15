const {test,expect,afterEach}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// Delete and Purge ask in the page's dialog. The dialog names the instance
// and shows the queue as visible text, never in an attribute.

const envs=[];
afterEach(()=>{ while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); }finally{ env.w.close(); } } });

const QUEUE='orders-SECRET-QUEUE';
const overlay=(d)=>d.getElementById('dialog-modal');
const posts=(env)=>env.calls.filter(c=>c.startsWith('POST /b/i1/rabbitmq/queue-ops'));
const attributes=(d)=>[...d.querySelectorAll('*')].flatMap(el=>[...el.attributes].map(a=>`${el.tagName}[${a.name}]=${a.value}`));

function start(operation){
 const env=boot({noDialogs:true}); envs.push(env);
 const {w,d}=env;
 w.serviceInstances={'i1':{instance_name:'orders-broker',organization_name:'payments',space_name:'prod'}};
 w.__routes.unshift([/\/b\/i1\/rabbitmq\//,(u,o,json)=>json(200,{success:true})]);
 d.body.insertAdjacentHTML('beforeend',`<input id="rabbitmq-queue-ops-name-i1" value="${QUEUE}"><input id="rabbitmq-operation-i1" value="${operation}">`);
 return env;
}

for(const [operation,title,confirm] of [['delete','Delete a queue from orders-broker?','Delete queue'],['purge','Purge a queue on orders-broker?','Purge messages']]){
 t(`${operation} names the instance and shows the queue as visible text`,async()=>{
  const env=start(operation); const {w,d}=env;
  const done=w.executeRabbitMQQueueOps('i1'); await sleep(10);
  expect(overlay(d).open).toBe(true);
  expect(overlay(d).querySelector('.dialog-modal-title').textContent).toBe(title);
  expect([...overlay(d).querySelectorAll('.dialog-modal-details li')].map(n=>n.textContent)).toEqual(['Instance: orders-broker','Instance ID: i1','Organization: payments','Space: prod']);
  expect([...overlay(d).querySelectorAll('.dialog-modal-command')].map(n=>n.textContent)).toEqual([QUEUE]);
  expect(attributes(d).filter(a=>a.includes(QUEUE)&&!/INPUT\[value\]/.test(a))).toEqual([]);
  expect(posts(env)).toEqual([]);
  const ok=overlay(d).querySelector('[data-dialog-role="confirm"]');
  expect(ok.textContent).toBe(confirm);
  ok.click(); await done; await sleep(10);
  expect(posts(env).length).toBe(1);
  expect(posts(env)[0]).toContain(`"operation":"${operation}"`);
 });
 t(`${operation} sends nothing on Cancel`,async()=>{
  const env=start(operation); const {w,d}=env;
  const done=w.executeRabbitMQQueueOps('i1'); await sleep(10);
  overlay(d).querySelector('[data-dialog-role="cancel"]').click(); await done; await sleep(10);
  expect(posts(env)).toEqual([]);
 });
}
