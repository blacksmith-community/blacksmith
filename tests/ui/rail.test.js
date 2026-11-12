// The left rail is the console's one navigation surface. It replaces the main
// tab strip, the per-panel section column and its "View section" select, and
// the batch New Operation and Activity switch. Every entry is a real link whose
// href comes from BlacksmithRoutes.format, the current entry carries
// aria-current="page", and each area with children is a disclosure button.
const {test,expect,afterEach}=require('bun:test');
const fs=require('fs'),path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const UI=path.resolve(__dirname,'..','..','ui');
const envs=[];
afterEach(()=>{while(envs.length){const e=envs.pop();e.w.blacksmithRouter?.destroy();e.w.close();}});

const status={env:'lab',deployment:'blacksmith',plans:{},instances:{'inst-1':{instance_name:'orders-cache',service_id:'redis',plan_id:'small',plan:{name:'small'}}}};
const routes=[['/v2/catalog',(u,o,j)=>j(200,{services:[]})],['/b/status',(u,o,j)=>j(200,status)]];
// A width in CSS pixels answers the rail's media queries the way a browser would.
const width=(px)=>(q)=>{const min=/min-width:\s*(\d+)px/.exec(q),max=/max-width:\s*(\d+)px/.exec(q);return (!min||px>=+min[1])&&(!max||px<=+max[1]);};
const start=async(opts={})=>{const env=boot({noDialogs:true,router:true,routes,media:width(1440),...opts});envs.push(env);await sleep(120);return env;};
const rail=(d)=>d.getElementById('console-rail');
const current=(d)=>[...rail(d).querySelectorAll('[aria-current="page"]')];
const link=(d,sel)=>rail(d).querySelector(sel);
// The result is whether the rail took the click. A click the rail leaves
// alone is stopped afterwards, because jsdom would follow a modified click
// that a browser opens in a new tab.
const click=(w,el,init={})=>{let taken=null;const after=(e)=>{taken=e.defaultPrevented;e.preventDefault();};w.addEventListener('click',after);el.dispatchEvent(new w.MouseEvent('click',{bubbles:true,cancelable:true,button:0,...init}));w.removeEventListener('click',after);return {defaultPrevented:taken};};
// DOM nodes are compared by identity, never by a deep walk of the window.
const only=(d,el)=>{const list=current(d);expect(list.length).toBe(1);expect(list[0]).toBe(el);};

t('the rail is the Primary landmark and the old navigation is gone',async()=>{
 const {d}=await start();
 const nav=rail(d);
 expect(nav.tagName).toBe('NAV');
 expect(nav.getAttribute('aria-label')).toBe('Primary');
 expect(nav.closest('main')).toBeNull();
 expect(d.querySelector('.tabs, .tab-button, .batch-view-nav, .entity-section-select, .entity-section-picker, .entity-navigation, .detail-tab')).toBeNull();
 expect(d.querySelector('.tab-content [role="tablist"][aria-label="Main navigation"]')).toBeNull();
 expect([...nav.querySelectorAll('[data-area]')].map(a=>a.dataset.area)).toEqual(['blacksmith','services','certificates','upgrade']);
});

t('areas with sections are disclosure buttons, and Certificates is a link that keeps its badge',async()=>{
 const {d}=await start();
 for(const area of ['blacksmith','services','upgrade']){
  const button=rail(d).querySelector(`[data-area="${area}"] > .rail-area-toggle`);
  expect(button.tagName).toBe('BUTTON');
  expect(button.getAttribute('type')).toBe('button');
  expect(['true','false']).toContain(button.getAttribute('aria-expanded'));
  const region=d.getElementById(button.getAttribute('aria-controls'));
  expect(region).not.toBeNull();
  expect(rail(d).contains(region)).toBe(true);
 }
 const certs=rail(d).querySelector('[data-area="certificates"] a.rail-link');
 expect(certs.textContent).toContain('Certificates');
 expect(certs.querySelector('#certificates-badge')).not.toBeNull();
 expect(d.querySelectorAll('#certificates-badge').length).toBe(1);
});

t('every rail entry is a link whose href is the formatted route',async()=>{
 const {w,d}=await start();
 const R=w.BlacksmithRoutes;
 const links=[...rail(d).querySelectorAll('a.rail-link')];
 expect(links.length).toBeGreaterThanOrEqual(15);
 for(const a of links){
  const route=R.parse(a.getAttribute('href'));
  expect(route).not.toBeNull();
  expect(a.getAttribute('href')).toBe(R.format(route));
 }
 const broker=[...rail(d).querySelectorAll('#rail-sections-blacksmith a.rail-link')];
 expect(broker.map(a=>a.textContent.trim())).toEqual(['Summary','Blacksmith VM','Tasks','Events','Logs','Certificates','Service plans','Endpoints','BOSH configs','Broker settings','Manifest']);
 expect(broker.map(a=>a.getAttribute('href'))).toEqual(['details','vms','tasks','events','blacksmith-logs','certificates','plans','broker','configs','config','manifest'].map(section=>R.format({screen:'blacksmith',section})));
 // Each group names its list, so a screen reader hears Overview, Operate, and Configure.
 const lists=[...rail(d).querySelectorAll('#rail-sections-blacksmith ul[aria-labelledby]')];
 expect(lists.map(ul=>d.getElementById(ul.getAttribute('aria-labelledby')).textContent.trim())).toEqual(['Overview','Operate','Configure']);
 expect(link(d,'#batch-activity').getAttribute('href')).toBe(R.format({screen:'upgrade',view:'activity'}));
 expect(link(d,'#batch-new').getAttribute('href')).toBe(R.format({screen:'upgrade',view:'new',operation:'upgrade',step:0}));
 expect(link(d,'[data-area="services"] a.rail-link').getAttribute('href')).toBe(R.format({screen:'services'}));
});

t('the current destination carries aria-current and its area is expanded',async()=>{
 const {d}=await start();
 expect(current(d).map(a=>a.textContent.trim())).toEqual(['Summary']);
 const toggle=(area)=>rail(d).querySelector(`[data-area="${area}"] > .rail-area-toggle`);
 expect(toggle('blacksmith').getAttribute('aria-expanded')).toBe('true');
 expect(toggle('services').getAttribute('aria-expanded')).toBe('false');
 expect(d.getElementById(toggle('services').getAttribute('aria-controls')).hidden).toBe(true);
});

t('a rail link navigates through the router and moves aria-current',async()=>{
 const {w,d}=await start();
 const tasks=[...rail(d).querySelectorAll('a.rail-link')].find(a=>a.textContent.trim()==='Tasks');
 const event=click(w,tasks);
 expect(event.defaultPrevented).toBe(true);
 await sleep(80);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'blacksmith',section:'tasks'});
 expect(w.location.hash).toBe('#/broker/tasks');
 only(d,tasks);
 expect(d.querySelector('#blacksmith .entity-workspace').dataset.section).toBe('tasks');
});

t('a modified or middle click is left to the browser',async()=>{
 const {w,d}=await start();
 const tasks=[...rail(d).querySelectorAll('a.rail-link')].find(a=>a.textContent.trim()==='Tasks');
 for(const init of [{ctrlKey:true},{metaKey:true},{shiftKey:true},{button:1}]){
  const event=click(w,tasks,init);
  expect(event.defaultPrevented).toBe(false);
 }
 await sleep(40);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'blacksmith',section:'details'});
});

t('an area button only opens and closes its sections',async()=>{
 const {w,d}=await start();
 const toggle=rail(d).querySelector('[data-area="upgrade"] > .rail-area-toggle');
 const region=d.getElementById(toggle.getAttribute('aria-controls'));
 toggle.click();
 expect(toggle.getAttribute('aria-expanded')).toBe('true');
 expect(region.hidden).toBe(false);
 toggle.click();
 expect(toggle.getAttribute('aria-expanded')).toBe('false');
 expect(region.hidden).toBe(true);
 expect(w.blacksmithRouter.current.screen).toBe('blacksmith');
});

t('an open instance shows its name and its eight sections as links',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'services',instance:'inst-1',section:'vms'});
 await sleep(60);
 const R=w.BlacksmithRoutes;
 const group=rail(d).querySelector('#rail-sections-services .rail-instance');
 expect(group.hidden).toBe(false);
 expect(group.textContent).toContain('orders-cache');
 const links=[...group.querySelectorAll('a.rail-link')];
 expect(links.map(a=>a.textContent.trim())).toEqual(['Summary','VMs','Events','Logs','Certificates','Credentials','Operations','Manifest']);
 expect(links.map(a=>a.getAttribute('href'))).toEqual(['details','vms','events','instance-logs','certificates','credentials','testing','manifest'].map(section=>R.format({screen:'services',instance:'inst-1',section})));
 expect(current(d).map(a=>a.textContent.trim())).toEqual(['VMs']);
 expect(rail(d).querySelector('[data-area="services"] > .rail-area-toggle').getAttribute('aria-expanded')).toBe('true');
 links.find(a=>a.textContent.trim()==='Credentials').click();
 await sleep(80);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'services',instance:'inst-1',section:'credentials'});
 await w.blacksmithRouter.navigate({screen:'services'});
 await sleep(40);
 expect(group.hidden).toBe(true);
 expect(current(d).map(a=>a.textContent.trim())).toEqual(['All instances']);
});

t('the batch links switch views and keep the New Operation draft',async()=>{
 const {w,d}=await start();
 await w.blacksmithRouter.navigate({screen:'upgrade',view:'new',operation:'upgrade',step:0});
 await sleep(60);
 only(d,link(d,'#batch-new'));
 link(d,'#batch-activity').click();
 await sleep(40);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'upgrade',view:'activity'});
 expect(d.querySelector('#upgrade .upgrade-right-panel').hidden).toBe(false);
 only(d,link(d,'#batch-activity'));
 link(d,'#batch-new').click();
 await sleep(40);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'upgrade',view:'new'});
 expect(d.querySelector('#upgrade .upgrade-main').hidden).toBe(false);
 only(d,link(d,'#batch-new'));
});

t('the docked rail minimizes to the icon strip and remembers it',async()=>{
 const {w,d}=await start();
 const collapse=d.getElementById('rail-collapse');
 expect(rail(d).dataset.rail).toBe('full');
 expect(collapse.getAttribute('aria-pressed')).toBe('false');
 collapse.click();
 expect(rail(d).dataset.rail).toBe('mini');
 expect(collapse.getAttribute('aria-pressed')).toBe('true');
 expect(w.localStorage.getItem('blacksmith.rail')).toBe('mini');
 const again=await start({setup:(win)=>win.localStorage.setItem('blacksmith.rail','mini')});
 expect(rail(again.d).dataset.rail).toBe('mini');
});

t('storage that throws leaves the rail docked and raises nothing',async()=>{
 const {d,errors}=await start({setup:(win)=>{Object.defineProperty(win,'localStorage',{configurable:true,get(){throw new win.DOMException('blocked','SecurityError');}});}});
 expect(rail(d).dataset.rail).toBe('full');
 d.getElementById('rail-collapse').click();
 expect(rail(d).dataset.rail).toBe('mini');
 expect(errors).toEqual([]);
});

t('from 700 to 1099px the rail starts as icons and an icon opens it over the content',async()=>{
 const {w,d}=await start({media:width(1024)});
 expect(rail(d).dataset.rail).toBe('mini');
 const toggle=rail(d).querySelector('[data-area="upgrade"] > .rail-area-toggle');
 toggle.click();
 expect(rail(d).hasAttribute('data-overlay')).toBe(true);
 expect(toggle.getAttribute('aria-expanded')).toBe('true');
 d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 expect(rail(d).hasAttribute('data-overlay')).toBe(false);
 expect(d.activeElement).toBe(toggle);
 toggle.click();
 link(d,'#batch-activity').click();
 await sleep(60);
 expect(rail(d).hasAttribute('data-overlay')).toBe(false);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'upgrade',view:'activity'});
});

t('under 700px a hamburger opens the rail as a modal drawer without touching history',async()=>{
 const {w,d,stubs}=await start({media:width(390)});
 const toggle=d.getElementById('rail-toggle');
 const drawer=d.getElementById('rail-drawer');
 expect(toggle.closest('header')).not.toBeNull();
 expect(toggle.getAttribute('aria-label')).toBe('Open navigation');
 expect(toggle.getAttribute('aria-controls')).toBe('rail-drawer');
 expect(toggle.getAttribute('aria-expanded')).toBe('false');
 expect(drawer.tagName).toBe('DIALOG');
 const before=w.history.length;
 toggle.focus();toggle.click();
 expect(stubs.isModal(drawer)).toBe(true);
 expect(drawer.contains(rail(d))).toBe(true);
 expect(toggle.getAttribute('aria-expanded')).toBe('true');
 expect(d.activeElement).toBe(current(d)[0]);
 d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 await stubs.settle();
 expect(drawer.open).toBe(false);
 expect(drawer.contains(rail(d))).toBe(false);
 expect(toggle.getAttribute('aria-expanded')).toBe('false');
 expect(d.activeElement).toBe(toggle);
 expect(w.history.length).toBe(before);
 toggle.click();
 [...rail(d).querySelectorAll('a.rail-link')].find(a=>a.textContent.trim()==='Events').click();
 await stubs.settle();await sleep(80);
 expect(drawer.open).toBe(false);
 expect(w.blacksmithRouter.current).toMatchObject({screen:'blacksmith',section:'events'});
 expect(d.activeElement).toBe(d.querySelector('#blacksmith .detail-content'));
});

t('the breadcrumb is the first row of the content column and Back shows only below the top level',async()=>{
 const {w,d}=await start();
 const bar=d.getElementById('route-context');
 expect(bar.nextElementSibling).toBe(d.querySelector('.tab-content'));
 const back=()=>bar.querySelector('.route-back');
 for(const route of [{screen:'blacksmith',section:'details'},{screen:'services'},{screen:'certificates'},{screen:'upgrade',view:'new',operation:'upgrade',step:0},{screen:'upgrade',view:'activity'}]){
  await w.blacksmithRouter.navigate(route);await sleep(40);
  expect(back()?.hidden??true).toBe(true);
 }
 await w.blacksmithRouter.navigate({screen:'blacksmith',section:'tasks'});await sleep(40);
 expect(back().hidden).toBe(false);
});

t('the shell no longer locks the page height, and the rail and drawer pad for the notch',()=>{
 const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');
 const html=read('index.html');
 expect(html).toMatch(/<meta name="viewport" content="[^"]*viewport-fit=cover/);
 const body=read('blacksmith.css').match(/\nbody \{([^}]*)\}/)[1];
 expect(body).not.toMatch(/overflow:\s*hidden/);
 expect(body).not.toMatch(/height:\s*100vh/);
 expect(read('console.css')).not.toMatch(/\nbody \{[^}]*height: 100dvh/);
 const shell=read('routes.css');
 // The fixed app shell applies only where there is room for it.
 expect(shell).toMatch(/@media \(min-width: 700px\) and \(min-height: 641px\)/);
 for(const sel of ['body > header','.console-rail','.rail-drawer']) expect(shell).toMatch(new RegExp(sel.replace(/[.>]/g,'\\$&')+'[^{]*\\{[^}]*env\\(safe-area-inset-'));
});
