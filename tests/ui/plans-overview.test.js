const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');

const data=()=>({services:[
 {id:'redis',name:'Redis',plans:[
  {id:'small',name:'Small',blacksmith:{instances:2,limit:5},vms:[{name:'redis',instances:1}]},
  {id:'cluster',name:'Cluster',blacksmith:{instances:0,limit:0},vms:[{name:'master',instances:1},{name:'replica',instances:2}]}]},
 {id:'valkey',name:'Valkey',plans:[{id:'solo',name:'Solo'}]}
]});
const mount=(w,d,input=data())=>{
 const host=d.createElement('div'); d.body.appendChild(host);
 host.innerHTML=w.__T.renderPlansTemplate(input);
 return host;
};
const cells=(row)=>[...row.children].map(c=>c.textContent.replace(/\s+/g,' ').trim());

test('with no plan open the detail pane compares every plan in one table',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d);
  const pane=host.querySelector('.plan-detail');
  expect(pane.querySelector('.no-selection')).toBeNull();
  expect(pane.textContent).not.toContain('Select a service plan');
  const table=pane.querySelector('table.plans-overview-table');
  expect([...table.querySelectorAll('thead th')].map(th=>th.textContent)).toEqual(['Service','Plan','Topology','Instances','Limit']);
  const rows=[...table.querySelectorAll('tbody tr')].map(cells);
  expect(rows).toEqual([
   ['Redis','Small','1 redis','2','5'],
   ['Redis','Cluster','1 master, 2 replica','0','Unlimited'],
   ['Valkey','Solo','—','—','—']]);
  expect(host.querySelectorAll('.plans-list .plan-item').length).toBe(3);
  expect(pane.querySelector('.table-controls-container .table-search-input')).not.toBeNull();
  expect(pane.querySelector('.table-toolbar-actions .copy-btn-logs')).not.toBeNull();
  expect(table.querySelector('td .empty-value')).not.toBeNull();
 }finally{w.close();}
});

test('numeric columns carry sort values',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d);
  const rows=[...host.querySelectorAll('.plans-overview-table tbody tr')];
  expect(rows.map(r=>r.children[3].dataset.sortValue)).toEqual(['2','0',undefined]);
  expect(rows.map(r=>r.children[4].dataset.sortValue)).toEqual(['5',String(Number.MAX_SAFE_INTEGER),undefined]);
 }finally{w.close();}
});

test('a pointer click on a row opens that plan, and the name is a real link',()=>{
 const {w,d}=boot();try{
  const host=mount(w,d);
  const opened=[];
  host.querySelectorAll('.plan-item').forEach(item=>item.addEventListener('click',()=>opened.push(item.dataset.serviceIndex+'/'+item.dataset.planIndex)));
  const rows=[...host.querySelectorAll('.plans-overview-table tbody tr')];
  // The row is not a tab stop. The plan name is the focusable link.
  expect(rows.every(r=>!r.hasAttribute('tabindex'))).toBe(true);
  expect(rows.every(r=>r.querySelector('td a[href^="#/"]')!==null)).toBe(true);
  rows[1].querySelector('td').dispatchEvent(new w.MouseEvent('click',{bubbles:true}));
  // A click that lands on the link is the link's own, so the row does not open a second time.
  rows[0].querySelector('a').dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true}));
  expect(opened).toEqual(['0/1']);
 }finally{w.close();}
});

test('plan names are escaped in the table',()=>{
 const {w,d}=boot();try{
  const evil='"><img src=x onerror=alert(1)>';
  const host=mount(w,d,{services:[{id:'s',name:evil,plans:[{id:'p',name:evil,vms:[{name:evil}]}]}]});
  expect(host.querySelector('.plans-overview-table img')).toBeNull();
  expect(host.querySelector('.plans-overview-table tbody td').textContent).toBe(evil);
 }finally{w.close();}
});

test('a route to the plans overview leaves an overview already showing alone',async()=>{
 const {w,d}=boot({noDialogs:true,routes:[
  ['/v2/catalog',(u,o,json)=>json(200,data())],
  ['/b/status',(u,o,json)=>json(200,{env:'lab',deployment:'blacksmith',instances:{},plans:{}})]]});
 try{
  await sleep(150);
  await w.blacksmithViews.activate({screen:'blacksmith',section:'plans'},{});
  const first=d.querySelector('#blacksmith .plans-overview');
  expect(first).not.toBeNull();
  first.dataset.mark='kept';
  await w.blacksmithViews.activate({screen:'blacksmith',section:'plans'},{});
  expect(d.querySelector('#blacksmith .plans-overview').dataset.mark).toBe('kept');
  // With a plan open, the overview is mounted again.
  await w.blacksmithViews.activate({screen:'blacksmith',section:'plans',service:'redis',plan:'small'},{});
  expect(d.querySelector('#blacksmith .plans-overview')).toBeNull();
  await w.blacksmithViews.activate({screen:'blacksmith',section:'plans'},{});
  expect(d.querySelector('#blacksmith .plans-overview')).not.toBeNull();
 }finally{w.close();}
});
