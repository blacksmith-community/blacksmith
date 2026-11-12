/* The left rail is the console's one navigation surface.
 *
 * From 1100px it is docked beside the content and can be minimized to an
 * icon strip, which the browser remembers. From 700px to 1099px it starts as
 * the icon strip, and choosing an icon opens the full rail over the content.
 * Under 700px a header button opens it as a modal drawer.
 *
 * Every entry is a link to its route, so a modified or middle click opens it
 * in a new tab. A plain click goes through the router, and the entry for the
 * current page carries aria-current="page". */
(function(root){
 'use strict';
 const doc=root.document;
 if(!doc)return;
 const nav=doc.getElementById('console-rail');
 if(!nav)return;
 const shell=nav.parentElement;
 const drawer=doc.getElementById('rail-drawer');
 const toggle=doc.getElementById('rail-toggle');
 const collapse=doc.getElementById('rail-collapse');
 const KEY='blacksmith.rail';

 // An instance page has these sections, grouped the way the broker's are.
 const INSTANCE_SECTIONS=[
  ['Overview',[['details','Summary']]],
  ['Operate',[['vms','VMs'],['events','Events'],['instance-logs','Logs'],['certificates','Certificates']]],
  ['Connect',[['credentials','Credentials'],['testing','Operations']]],
  ['Inspect',[['manifest','Manifest']]]
 ];

 // Storage can be blocked or throw. The rail then keeps its default.
 const stored=()=>{try{return root.localStorage.getItem(KEY);}catch(_){return null;}};
 const store=(value)=>{try{root.localStorage.setItem(KEY,value);}catch(_){/* the choice lasts for this page only */}};

 nav.querySelectorAll('.rail-icon[data-icon]').forEach(slot=>{if(!slot.firstChild&&root.blacksmithIcon)slot.innerHTML=root.blacksmithIcon(slot.dataset.icon,{size:18});});

 const query=(q)=>{try{return root.matchMedia?.(q);}catch(_){return null;}};
 const wide=query('(min-width: 1100px)');
 const medium=query('(min-width: 700px)');
 let minimized=stored()==='mini';

 const mode=()=>wide?.matches?(minimized?'mini':'full'):medium?.matches?'mini':'drawer';
 const toggles=()=>[...nav.querySelectorAll('.rail-area-toggle')];
 const regionOf=(button)=>doc.getElementById(button.getAttribute('aria-controls'));
 const expand=(button,open)=>{button.setAttribute('aria-expanded',String(open));const region=regionOf(button);if(region)region.hidden=!open;};

 // The overlay is the full rail opened over the content from the icon strip.
 let overlayOpener=null;
 function openOverlay(button){
  nav.setAttribute('data-overlay','');overlayOpener=button;
  toggles().forEach(b=>expand(b,b===button));
 }
 function closeOverlay({restore=false}={}){
  if(!nav.hasAttribute('data-overlay'))return;
  nav.removeAttribute('data-overlay');
  const opener=overlayOpener;overlayOpener=null;
  syncAreas(true);
  if(restore&&opener?.isConnected)opener.focus();
 }

 function layout(){
  const next=mode();
  if(next!=='drawer'&&drawer?.open)drawer.close();
  if(next!=='mini')closeOverlay();
  const changed=nav.dataset.rail!==next;
  nav.dataset.rail=next;
  if(changed)syncAreas(true);
  if(collapse){collapse.setAttribute('aria-pressed',String(next==='mini'&&!!wide?.matches));const label=next==='mini'?'Expand navigation':'Minimize navigation';collapse.setAttribute('aria-label',label);collapse.title=label;}
  if(collapse)collapse.hidden=!wide?.matches;
 }
 for(const q of [wide,medium]){try{q?.addEventListener?.('change',layout);}catch(_){/* older engines fire no change events */}}

 // Which page is showing. The router knows when it is running; otherwise the
 // views adapter reads the page itself.
 function currentRoute(){
  const router=root.blacksmithRouter;
  if(router?.current)return router.current;
  const screen=doc.querySelector('.tab-panel.active')?.id;
  try{const route=root.blacksmithViews?.read?.(screen);if(route)return route;}catch(_){/* fall through */}
  // Before the views adapter exists, the batch page still knows its view.
  if(screen==='upgrade'){const view=root.blacksmithBatch?.view?.();if(view)return {screen,view};}
  return screen?{screen}:null;
 }
 function areaOf(route){return route?.screen||'blacksmith';}
 // The link that stands for a route. A section page matches its section link,
 // and anything deeper matches the nearest link above it.
 function linkFor(route){
  if(!route)return null;
  if(route.screen==='blacksmith')return nav.querySelector(`#rail-sections-blacksmith a.rail-link[data-section="${route.section||'details'}"]`);
  if(route.screen==='services'){
   if(route.instance)return nav.querySelector(`#rail-sections-services .rail-instance a.rail-link[data-section="${route.section||'details'}"]`);
   return nav.querySelector('#rail-sections-services a.rail-link[data-screen="services"]:not([data-section])');
  }
  if(route.screen==='certificates')return nav.querySelector('[data-area="certificates"] a.rail-link');
  if(route.screen==='upgrade')return doc.getElementById(route.view==='activity'?'batch-activity':'batch-new');
  return null;
 }

 const format=(route)=>{try{return root.BlacksmithRoutes?.format(route)||null;}catch(_){return null;}};
 let instanceShown=null;
 function renderInstance(id){
  const group=nav.querySelector('#rail-sections-services .rail-instance');
  if(!group)return;
  if(!id){group.hidden=true;return;}
  const name=root.serviceInstances?.[id]?.instance_name||id;
  if(instanceShown!==id+'\u0000'+name){
   instanceShown=id+'\u0000'+name;
   group.replaceChildren();
   const title=doc.createElement('span');title.className='rail-instance-name';title.id='rail-instance-name';title.textContent=name;title.title=name;group.append(title);
   INSTANCE_SECTIONS.forEach(([label,entries])=>{
    const gid='rail-group-instance-'+label.toLowerCase();
    const heading=doc.createElement('span');heading.className='rail-group-label';heading.id=gid;heading.textContent=label;
    const list=doc.createElement('ul');list.className='rail-list';list.setAttribute('aria-labelledby',gid);
    entries.forEach(([section,text])=>{
     const li=doc.createElement('li');const a=doc.createElement('a');a.className='rail-link';a.dataset.screen='services';a.dataset.section=section;a.dataset.instance=id;
     const href=format({screen:'services',instance:id,section});if(href)a.href=href;a.textContent=text;li.append(a);list.append(li);
    });
    group.append(heading,list);
   });
  }
  group.hidden=false;
 }

 let shownArea=null;
 // The area of the current page is expanded and the others close, but only
 // when the area changes, so an area the operator opened stays open.
 function syncAreas(force){
  const area=areaOf(lastRoute);
  // The icon strip shows no sections, so no area there reads as expanded.
  if(nav.dataset.rail==='mini'&&!nav.hasAttribute('data-overlay')){shownArea=null;toggles().forEach(b=>expand(b,false));return;}
  if(!force&&area===shownArea)return;
  shownArea=area;
  toggles().forEach(b=>expand(b,b.closest('[data-area]')?.dataset.area===area));
 }

 let lastRoute=null;
 function sync(route){
  if(!route){const router=root.blacksmithRouter;if(router?.applying)return;route=currentRoute();}
  if(!route)return;
  lastRoute=route;
  // The open instance keeps its group in the rail until the operator goes
  // back to the instance list, so its sections stay one click away.
  if(route.screen==='services'){
   const instance=route.instance||(doc.getElementById('services')?.classList.contains('instance-open')?root.currentInstanceInfo?.id:null);
   if(instance&&!route.instance)route={...route,instance,section:route.section||'details'};
   renderInstance(instance||null);
   if(!instance)instanceShown=null;
  }
  const target=linkFor(route);
  nav.querySelectorAll('[aria-current]').forEach(el=>{if(el!==target)el.removeAttribute('aria-current');});
  target?.setAttribute('aria-current','page');
  const draft=root.blacksmithBatch?.draft?.();
  const newLink=doc.getElementById('batch-new');
  if(draft&&newLink){const href=format(draft);if(href)newLink.setAttribute('href',href);}
  if(!nav.hasAttribute('data-overlay'))syncAreas(false);
 }

 // The route a link stands for. The New Operation link returns to the draft.
 function routeOf(a){
  const {screen,section,instance,view}=a.dataset;
  if(screen==='upgrade'&&view==='new')return root.blacksmithBatch?.draft?.()||{screen,view:'new',operation:'upgrade',step:0};
  if(screen==='upgrade')return {screen,view:'activity'};
  if(screen==='services')return instance?{screen,instance,section:section||'details'}:{screen};
  if(screen==='blacksmith')return {screen,section:section||'details'};
  return {screen};
 }

 const panelOf=()=>doc.querySelector('.tab-panel.active .detail-content')||doc.querySelector('.tab-panel.active');
 function focusPanel(){const panel=panelOf();if(!panel)return;if(root.blacksmithRouteFocus){root.blacksmithRouteFocus(panel);return;}if(!panel.hasAttribute('tabindex'))panel.setAttribute('tabindex','-1');panel.focus({preventScroll:true});}

 let navigating=false;
 // A later choice wins over an earlier one still opening its screen.
 let ticket=0;
 async function go(a){
  const mine=++ticket;
  const route=routeOf(a);
  const fromDrawer=!!drawer?.open;
  closeOverlay();
  if(fromDrawer){navigating=true;drawer.close();}
  const router=root.blacksmithRouter;
  const batch=root.blacksmithBatch;
  const onBatch=!!doc.getElementById('upgrade')?.classList.contains('active');
  try{
   // Switching between New Operation and Activity keeps the draft; only an
   // open job goes back to the list. Without the router the view changes at
   // once, as it does on the batch page itself.
   const swap=route.screen==='upgrade'&&!!batch?.setView&&!(route.view==='activity'&&router?.current?.task)&&!(route.view==='new'&&router?.current?.view==='new');
   if(swap&&(onBatch||!router))batch.setView(route.view);
   if(swap&&onBatch){sync();if(fromDrawer)focusPanel();return;}
   if(router){await router.navigate(route);return;}
   if(root.blacksmithViews){await root.blacksmithViews.activate(route);sync(route);focusPanel();return;}
   // Opening the batch page resets its view, so the view the link names is
   // set again once the screen is up, unless a later choice replaced this one.
   if(await root.blacksmithNav?.switchTab(route.screen)===false||mine!==ticket)return;
   if(route.screen==='upgrade'&&batch?.view?.()!==route.view)batch?.setView?.(route.view);
   sync(route);
  }catch(error){root.blacksmithNav?.announce?.(error?.message||'That page could not be opened.');}
 }

 nav.addEventListener('click',(event)=>{
  const button=event.target.closest?.('.rail-area-toggle');
  if(button&&nav.contains(button)){
   if(nav.dataset.rail==='mini'&&!nav.hasAttribute('data-overlay')){openOverlay(button);return;}
   expand(button,button.getAttribute('aria-expanded')!=='true');
   return;
  }
  if(event.target.closest?.('#rail-collapse')){
   minimized=nav.dataset.rail!=='mini';store(minimized?'mini':'full');layout();
   return;
  }
  const a=event.target.closest?.('a.rail-link');
  if(!a||!nav.contains(a))return;
  if(event.defaultPrevented||event.button!==0||event.ctrlKey||event.metaKey||event.shiftKey||event.altKey)return;
  event.preventDefault();
  if(a.getAttribute('aria-disabled')==='true')return;
  go(a);
 });

 // The overlay closes on Escape, on a press outside it, and when focus leaves it.
 doc.addEventListener('keydown',(event)=>{if(event.key==='Escape'&&nav.hasAttribute('data-overlay')){event.preventDefault();closeOverlay({restore:true});}});
 doc.addEventListener('pointerdown',(event)=>{if(nav.hasAttribute('data-overlay')&&!nav.contains(event.target))closeOverlay();});
 nav.addEventListener('focusout',(event)=>{if(nav.hasAttribute('data-overlay')&&event.relatedTarget&&!nav.contains(event.relatedTarget))closeOverlay();});

 // The phone drawer is a modal dialog. The rail moves into it while it is
 // open and back into the page when it closes.
 if(drawer&&toggle){
  toggle.addEventListener('click',()=>{
   if(drawer.open)return;
   sync();
   drawer.append(nav);
   drawer.showModal();
   toggle.setAttribute('aria-expanded','true');
   const target=nav.querySelector('[aria-current="page"]');
   (target&&target.getClientRects?.().length!==0?target:nav.querySelector('a.rail-link, button'))?.focus();
   if(target&&doc.activeElement!==target)target.focus();
  });
  drawer.addEventListener('close',()=>{
   shell.prepend(nav);
   toggle.setAttribute('aria-expanded','false');
   if(navigating){navigating=false;return;}
   toggle.focus();
  });
  drawer.addEventListener('click',(event)=>{
   if(event.target===drawer||event.target.closest?.('[data-close]'))drawer.close();
  });
 }

 layout();
 sync();
 root.BlacksmithRail={sync,layout,sections:INSTANCE_SECTIONS};
})(globalThis);
