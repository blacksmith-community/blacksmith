const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
test('Tasks filters have labels reflecting their purpose',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.formatTasks([]);
 expect(host.querySelector('label[for="task-filter"]').textContent).toBe('Scope');
 expect(host.querySelector('#task-filter').hasAttribute('aria-label')).toBe(false);
 expect(host.querySelector('fieldset.toolbar-group legend').textContent).toBe('States');
 }finally{w.close();}
});

test('plan facts use row headings rather than a sortable title row',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderPlanDetail({name:'valkey'},{name:'cluster',blacksmith:{instances:1,limit:5}});
 expect(!!host.querySelector('.plan-detail-header')).toBe(true);
 expect(!!host.querySelector('.plan-details-table thead')).toBe(false);
 expect(host.querySelector('.plan-details-table caption').textContent).toBe('Plan information');
 expect(host.querySelector('.plan-details-table th').scope).toBe('row');
 }finally{w.close();}
});

// The scope value "blacksmith" asks the director for every task. The label
// says that, and the value the request carries stays "blacksmith".
test('the broker Tasks scope labels the all-tasks option for what it shows',async()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.formatTasks([]);
 const initial=host.querySelector('#task-filter option');
 expect(initial.value).toBe('blacksmith');
 expect(initial.textContent).toBe('Broker director (all tasks)');
 const select=d.createElement('select');select.id='task-filter';d.body.append(select);
 w.__routes.push(['/b/service-filter-options',(u,o,json)=>json(200,{options:['blacksmith','service-instances','redis']})]);
 await w.__T.populateTaskFilter();
 const options=[...d.querySelectorAll('#task-filter option')].map(o=>[o.value,o.textContent]);
 expect(options[0]).toEqual(['blacksmith','Broker director (all tasks)']);
 expect(options.slice(1)).toEqual([['service-instances','service-instances'],['redis','redis']]);
 expect(d.getElementById('task-filter').value).toBe('blacksmith');
 }finally{w.close();}
});
