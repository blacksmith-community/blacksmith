const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const read=(name)=>fs.readFileSync(path.join(__dirname,'../../ui',name),'utf8');

// The router marks the panel it focuses so the ring stays hidden for a move the operator did not make.
// Two things used to drop that marker while the panel kept or regained focus. The first is a blur that
// comes from the window losing focus, where the panel is still the active element and the browser gives
// it focus again with no marker. The second is a destination picked from the navigation dialog, which
// focuses the panel a second time after the dialog closes, and that second focus carried no marker.
const boot=async(hash='#/broker/manifest')=>{
 const dom=new JSDOM('<button id="navigation-trigger"><kbd></kbd></button><dialog id="navigation-dialog"><input id="navigation-search"><div role="listbox"></div><button data-load-more></button><button data-close></button><div role="status"></div></dialog><div id="console-title"></div><nav id="console-rail" aria-label="Primary"><ul class="rail-areas"><li class="rail-area" data-area="blacksmith"><button type="button" class="rail-area-toggle" aria-expanded="true" aria-controls="rail-sections-blacksmith"><span class="rail-label">Blacksmith Broker</span></button><div id="rail-sections-blacksmith"><ul class="rail-list"><li><a class="rail-link" href="#/broker/summary" data-screen="blacksmith" data-section="details">Summary</a></li><li><a class="rail-link" href="#/broker/manifest" data-screen="blacksmith" data-section="manifest">Manifest</a></li></ul></div></li></ul></nav><main class="tab-content"><section id="blacksmith" class="tab-panel active"><div class="entity-workspace" data-section="details"><div class="detail-content"></div></div></section></main>',{url:'http://localhost/'+hash,runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 const loaded={release:null};
 w.blacksmithViews={activate:async r=>{if(r.section==='manifest')await new Promise(done=>{loaded.release=done;});return r;}};
 w.eval(read('navigation.js'));
 await new Promise(r=>setTimeout(r,20));
 w.eval(read('routes.js'));
 await new Promise(r=>setTimeout(r,40));
 return {w,loaded};
};
const content=(w)=>w.document.querySelector('.tab-panel.active .detail-content');
const ringShown=(w,el)=>w.document.activeElement===el&&!el.hasAttribute('data-route-focus');

test('a window blur and refocus after the manifest loads keeps the router marker',async()=>{
 const {w,loaded}=await boot('#/broker/summary');
 const panel=content(w);
 const done=w.blacksmithRouter.navigate({screen:'blacksmith',section:'manifest'});
 await new Promise(r=>setTimeout(r,20));
 loaded.release();
 expect(await done).toBeTruthy();
 // The manifest finishes loading and replaces what the panel holds.
 panel.innerHTML='<div class="manifest-details-container deployment-manifest-workspace"></div>';
 expect(w.document.activeElement).toBe(panel);
 expect(panel.hasAttribute('data-route-focus')).toBe(true);
 // The window loses focus and gets it back. The panel stays the active element throughout.
 panel.dispatchEvent(new w.FocusEvent('blur'));
 await Promise.resolve();
 panel.dispatchEvent(new w.FocusEvent('focus'));
 expect(w.document.activeElement).toBe(panel);
 expect(ringShown(w,panel)).toBe(false);
 w.blacksmithRouter.destroy();w.close();
});

test('the first key the operator presses after a router move shows the ring again',async()=>{
 const {w,loaded}=await boot('#/broker/summary');
 const panel=content(w);
 const done=w.blacksmithRouter.navigate({screen:'blacksmith',section:'manifest'});
 await new Promise(r=>setTimeout(r,20));
 loaded.release();
 await done;
 panel.dispatchEvent(new w.FocusEvent('blur'));
 await Promise.resolve();
 expect(panel.hasAttribute('data-route-focus')).toBe(true);
 panel.dispatchEvent(new w.KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
 expect(panel.hasAttribute('data-route-focus')).toBe(false);
 expect(ringShown(w,panel)).toBe(true);
 w.blacksmithRouter.destroy();w.close();
});

test('moving focus to another control still ends the marker',async()=>{
 const {w,loaded}=await boot('#/broker/summary');
 const panel=content(w);
 const done=w.blacksmithRouter.navigate({screen:'blacksmith',section:'manifest'});
 await new Promise(r=>setTimeout(r,20));
 loaded.release();
 await done;
 w.document.querySelector('#console-rail .rail-link').focus();
 await Promise.resolve();
 expect(panel.hasAttribute('data-route-focus')).toBe(false);
 w.blacksmithRouter.destroy();w.close();
});

test('a destination chosen from the navigation dialog is marked when the panel takes focus after the dialog closes',async()=>{
 const {w,loaded}=await boot('#/broker/summary');
 const panel=content(w);
 const dialog=w.document.getElementById('navigation-dialog');
 dialog.showModal=()=>dialog.setAttribute('open','');
 dialog.close=()=>{dialog.removeAttribute('open');dialog.dispatchEvent(new w.Event('close'));};
 // A modal dialog makes the page behind it inert, so the panel cannot take focus until it closes.
 const focus=panel.focus.bind(panel);
 panel.focus=(options)=>{if(!dialog.hasAttribute('open'))focus(options);};
 w.Element.prototype.scrollIntoView=function(){};
 w.document.getElementById('navigation-trigger').click();
 const input=w.document.getElementById('navigation-search');
 input.value='manifest';input.dispatchEvent(new w.Event('input'));
 await new Promise(r=>setTimeout(r,20));
 input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
 await new Promise(r=>setTimeout(r,20));
 loaded.release();
 await new Promise(r=>setTimeout(r,60));
 expect(dialog.hasAttribute('open')).toBe(false);
 expect(w.document.activeElement).toBe(panel);
 expect(ringShown(w,panel)).toBe(false);
 w.blacksmithRouter.destroy();w.close();
});
