const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

t('the Service and Plan filters have no visible labels but keep accessible names',()=>{
 const {w,d}=boot();
 try{
 w.plansData={services:[{id:'valkey',name:'Valkey'}]};
 const host=d.createElement('div');
 host.innerHTML=w.__T.renderServicesTemplate({a:{service_id:'valkey',plan_id:'small',plan:{name:'small'},instance_name:'a'}});
 const section=host.querySelector('.services-filter-section');
 const service=section.querySelector('#service-filter'),plan=section.querySelector('#plan-filter');
 expect(service).not.toBeNull();expect(plan).not.toBeNull();
 const labels=[...section.querySelectorAll('label')].map(l=>l.textContent.trim());
 expect(labels).not.toContain('Service:');
 expect(labels).not.toContain('Plan:');
 expect(section.querySelector('label[for="service-filter"],label[for="plan-filter"]')).toBeNull();
 expect(service.getAttribute('aria-label')).toBe('Service');
 expect(plan.getAttribute('aria-label')).toBe('Plan');
 expect(service.options[0].textContent).toBe('All Services');
 expect(plan.options[0].textContent).toBe('All Plans');
 }finally{w.close();}
});

t('instance filter actions are named icon buttons',()=>{
 const {w,d}=boot();try{
 const host=d.createElement('div');host.innerHTML=w.__T.renderServicesTemplate({});
 const actions=host.querySelector('.filter-buttons');
 for(const [id,name] of [['refresh-services','Refresh service instances'],['copy-deployment-names','Copy deployment names for matching instances'],['clear-filters','Clear instance filters']]){
 const button=actions.querySelector('#'+id);
 expect(button.textContent.trim()).toBe('');
 expect(button.type).toBe('button');
 expect(button.getAttribute('aria-label')).toBe(name);
 expect(button.title).toBe(name);
 expect(button.querySelector('svg').getAttribute('aria-hidden')).toBe('true');
 expect(button.querySelector('svg').getAttribute('focusable')).toBe('false');
 }
 }finally{w.close();}
});
