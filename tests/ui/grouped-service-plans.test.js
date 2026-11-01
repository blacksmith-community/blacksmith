const {test, expect} = require('bun:test');
const {main: {boot}} = require('./certificates/harness.js');

test('plans group by service with stable identity and explicit capacity', () => {
 const {w,d}=boot(); const host=d.createElement('div');
 host.innerHTML=w.__T.renderPlansTemplate({services:[
  {id:'rabbit/id', name:'RabbitMQ', plans:[{id:'cluster',name:'Cluster',blacksmith:{instances:1,limit:2}},{id:'solo',name:'Standalone',blacksmith:{instances:0,limit:0}}]},
  {id:'valkey',name:'Valkey',plans:[{id:'cluster',name:'Cluster'}]}
 ]});
 const groups=[...host.querySelectorAll('.plan-service-group')];
 expect(groups.length).toBe(2);
 expect(groups.map(g=>g.querySelector('h3').textContent)).toEqual(['RabbitMQ','Valkey']);
 const plans=[...host.querySelectorAll('button.plan-item')];
 expect(plans.length).toBe(3);
 expect(plans[0].dataset.serviceId).toBe('rabbit/id');
 expect(plans[0].dataset.planKey).toBe('cluster');
 expect(plans[0].textContent).toContain('1 instance · limit 2');
 expect(plans[1].textContent).toContain('0 instances · Unlimited');
 expect(plans[2].textContent).toContain('Instances not reported');
 expect(plans[2].textContent).toContain('Limit not reported');
 expect(host.querySelector('.plans-list h2')).toBeNull();
 expect(host.querySelector('.plan-detail .plans-overview-table')).not.toBeNull();
 w.close();
});

test('catalog content is escaped and missing metadata remains readable', () => {
 const {w,d}=boot(); const host=d.createElement('div');
 const evil='\"<img src=x onerror=alert(1)>\'';
 const service={id:evil,name:evil,description:evil,tags:[evil]};
 const plan={id:evil,name:evil,description:evil,vms:[{name:evil,instances:0,vm_type:evil,properties:{text:evil}}]};
 expect(()=>{host.innerHTML=w.__T.renderPlanDetail(service,plan)}).not.toThrow();
 expect(host.querySelector('img')).toBeNull();
 expect(host.querySelector('[onclick]')).toBeNull();
 expect(host.querySelector('.plan-collection-link').getAttribute('href')).toBe('#/broker/plans');
 expect(host.querySelector('[data-entity-copy]').dataset.entityCopy).toBe(evil);
 expect(host.textContent).toContain('Not reported');
 expect(host.querySelector('.plan-vms-table tbody tr').children[1].textContent).toBe('0');
 host.innerHTML=w.__T.renderPlansTemplate({services:[{...service,plans:[plan]}]});
 expect(host.querySelector('img')).toBeNull();
 expect(host.querySelector('.plan-item').dataset.serviceId).toBe(evil);
 w.close();
});
