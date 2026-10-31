const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const rule=(file,selector)=>{
 const css=fs.readFileSync(path.join(UI,file),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 const out=[];
 css.replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};

const render=async(tasks)=>{
 const env=boot();await sleep(50);
 env.w.__T._setTasks(tasks);env.w.__T.renderUpgradeTaskList();
 return env;
};
const task=(extra)=>Object.assign({id:'t1',status:'completed',operation:'upgrade',total_count:1,completed_count:1,failed_count:0,created_at:new Date().toISOString()},extra);

t('the total counts instances in the singular and the plural',async()=>{
 const {w,d}=await render([task(),task({id:'t2',total_count:2,completed_count:2})]);try{
 const totals=[...d.querySelectorAll('#upgrade .upgrade-task-stats span:last-child')].map((n)=>n.textContent.trim());
 expect(totals).toEqual(['1 instance','2 instances']);
 }finally{w.close();}
});
t('a failed count of zero is neutral and a positive count is an error',async()=>{
 const {w,d}=await render([task(),task({id:'t2',failed_count:1,completed_count:0})]);try{
 const items=[...d.querySelectorAll('#upgrade .upgrade-task-item')];
 expect(items.length).toBe(2);
 const zero=items[0].querySelector('.upgrade-task-stats span:nth-child(2)');
 expect(zero.textContent.trim()).toBe('0 failed');
 expect(zero.classList.contains('failed')).toBe(false);
 expect(zero.classList.contains('total')).toBe(true);
 const some=items[1].querySelector('.upgrade-task-stats span:nth-child(2)');
 expect(some.textContent.trim()).toBe('1 failed');
 expect(some.classList.contains('failed')).toBe(true);
 }finally{w.close();}
});
test('the status label is a pill the size of the kind badge, in a neutral color',()=>{
 const pill=rule('forge-theme.css','#upgrade .activity-status-label');
 const badge=rule('blacksmith.css','#upgrade .task-kind-badge');
 expect(pill).toMatch(/border-radius:\s*999px/);
 for(const prop of ['padding','font-size','font-weight','border']){
  const get=(b)=>(b.match(new RegExp('(?:^|[;\\s])'+prop+':\\s*([^;]+)'))||[])[1].trim();
  expect(get(pill)).toBe(get(badge));
 }
 expect(pill).toMatch(/color:\s*var\(--text-secondary\)/);
 expect(pill).toMatch(/white-space:\s*nowrap/);
});
