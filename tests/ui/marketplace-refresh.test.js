const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); expect(env.errors).toEqual([]); } finally { env.w.close(); } }
});

// The marketplace table has a Refresh button that reads the endpoint's marketplace again and redraws the
// panel. Each read answers with a different service, so a redraw shows in the table.
const start=async()=>{
 const reads=[];
 const env=boot({noDialogs:true,routes:[
  ['/b/status',(u,o,j)=>j(200,{env:'test',deployment:'blacksmith',instances:{},plans:{}})],['/b/config/ssh/ui-terminal-status',(u,o,j)=>j(200,{})],['/v2/catalog',(u,o,j)=>j(200,{services:[]})],
  ['/b/cf/endpoints/lab/marketplace',(u,o,j)=>{ reads.push(u); return j(200,{services:[{name:'service-read-'+reads.length,description:'d',plans:[{}],available:true}]}); }]]});
 envs.push(env);
 const {w,d}=env;
 w.eval(OAT);
 await sleep(60);
 const host=d.createElement('div'); host.id='broker-host'; d.body.appendChild(host);
 host.innerHTML=w.__T.renderBrokerTab();
 await w.__T.initBrokerTab();
 await sleep(20);
 const M=w.CFEndpointManager;
 M.selectedEndpoint='lab'; M.connectionStates.lab={connected:true};
 await M.loadTabContent('marketplace','lab'); await sleep(20);
 env.errors.length=0;
 return {...env,host,reads,panel:host.querySelector('[data-panel="marketplace"]')};
};

t('Refresh marketplace reads the marketplace again and redraws the table',async()=>{
 const {w,panel,reads}=await start();
 expect(reads.length).toBe(1);
 expect(panel.querySelector('.marketplace-services-table tbody').textContent).toContain('service-read-1');
 const button=panel.querySelector('.refresh-btn-logs');
 expect(button!==null).toBe(true);
 // jsdom does not run inline handlers, so the click runs the call the button's onclick makes.
 const call=button.getAttribute('onclick').match(/^window\.refreshMarketplaceServices\((.*), event\)$/);
 expect(call!==null).toBe(true);
 button.addEventListener('click',(event)=>w.refreshMarketplaceServices(JSON.parse(call[1]),event));
 button.click(); await sleep(60);
 expect(reads.length).toBe(2);
 const table=panel.querySelector('.marketplace-services-table tbody');
 expect(table.textContent).toContain('service-read-2');
 expect(table.textContent).not.toContain('service-read-1');
 expect(panel.querySelector('.refresh-btn-logs').classList.contains('refreshing')).toBe(false);
});

// A search box sits in a controls row at the top of the table head. That row is not a column header, so it
// gets no sort icon, and the real headers sort the rows.
t('the controls row has no sort icon and the column headers sort the rows',async()=>{
 const {w,panel}=await start();
 const controls=panel.querySelector('.table-controls-header');
 expect(controls.querySelector('.sort-icon')).toBeNull();
 expect(controls.classList.contains('sortable-header')).toBe(false);
 const heads=[...panel.querySelectorAll('.marketplace-services-table thead tr:not(.table-controls-row) th')];
 expect(heads.length).toBe(4);
 expect(heads.every(h=>h.classList.contains('sortable')||h.classList.contains('sortable-header'))).toBe(true);
 expect(panel.querySelectorAll('.marketplace-services-table .sort-icon').length).toBe(0);
 const order=()=>[...panel.querySelectorAll('.marketplace-services-table tbody tr td:first-child')].map(x=>x.textContent);
 const before=order();
 expect(before.length).toBeGreaterThan(0);
 heads[0].click(); await sleep(20);
 expect(order()).toEqual([...before].sort());
 w.close();
});
