const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const link=(w,label)=>{
 const host=w.document.createElement('div');
 host.innerHTML=w.__T.entityMetric(1,label,'vms');
 return host.querySelector('.entity-metric-link').textContent;
};

t('the metric link keeps an acronym in capitals',()=>{
 const {w}=boot();try{
 expect(link(w,'VM status')).toBe('View VM status →');
 expect(link(w,'VMs')).toBe('View VMs →');
 expect(link(w,'VM')).toBe('View VM →');
 expect(link(w,'CPU usage')).toBe('View CPU usage →');
 expect(link(w,'Instances with VM alerts')).toBe('View instances with VM alerts →');
 }finally{w.close();}
});
t('the metric link lowercases only the first letter of a plain label',()=>{
 const {w}=boot();try{
 expect(link(w,'Service plans')).toBe('View service plans →');
 expect(link(w,'Virtual machines')).toBe('View virtual machines →');
 expect(link(w,'Plan')).toBe('View plan →');
 }finally{w.close();}
});
