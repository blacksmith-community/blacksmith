const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
test('Tasks filters have labels reflecting their purpose',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.formatTasks([]);
 expect(host.querySelector('label[for="task-filter"]').textContent).toBe('Scope:');
 expect(host.querySelector('#task-filter').getAttribute('aria-label')).toBe('Task scope');
 expect(host.querySelector('.checkbox-group').getAttribute('aria-labelledby')).toBe('task-states-label');
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
