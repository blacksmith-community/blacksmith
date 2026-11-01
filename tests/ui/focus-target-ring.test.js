const {test,expect}=require('bun:test');
const fs=require('fs'),path=require('path');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const read=(name)=>fs.readFileSync(path.join(__dirname,'../../ui',name),'utf8');
const css=read('forge-theme.css').replace(/\/\*[\s\S]*?\*\//g,'');

// The router moves focus to the active panel after a destination loads so screen
// readers start at the content. The navigation script gives that panel tabindex 0,
// so the router marks the element it focuses, and the marker is what hides the ring.
const boot=async()=>{
 const dom=new JSDOM('<button id="navigation-trigger"><kbd></kbd></button><dialog id="navigation-dialog"><input><div role="listbox"></div><button data-load-more></button><button data-close></button><div role="status"></div></dialog><div id="console-title"></div><main class="tab-content"><section id="blacksmith" class="tab-panel active"><nav class="detail-tabs entity-navigation"><button class="detail-tab active" data-tab="details">Summary</button></nav><div class="detail-content"></div></section></main>',{url:'http://localhost/#/broker/summary',runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(dom.window);
 w.blacksmithViews={activate:async r=>r};
 w.eval(read('navigation.js'));
 await new Promise(r=>setTimeout(r,20));
 w.eval(read('routes.js'));
 await new Promise(r=>setTimeout(r,40));
 return w;
};

// Reads every rule that ends in :focus-visible, so a selector can be tried against a real element.
const ringRules=()=>[...css.matchAll(/([^{}]+)\{([^}]*)\}/g)]
 .map(([,selector,body])=>({selector:selector.trim(),body}))
 .flatMap(({selector,body})=>selector.split(/,\s*(?=[.:])/).filter(s=>/detail-content|tab-panel/.test(s)&&s.endsWith(':focus-visible')).map(s=>({selector:s.replace(/:focus-visible$/,''),body})));
const applying=(w,el,declaration)=>ringRules().filter(r=>{
 const high=r.selector.startsWith(':root[data-contrast="high"]');
 if(high!==(w.document.documentElement.dataset.contrast==='high'))return false;
 return el.matches(high?r.selector.replace(':root[data-contrast="high"] ',''):r.selector)&&declaration.test(r.body);
});

test('the navigation script gives the content tabindex 0 and the router focus marks it',async()=>{
 const w=await boot();
 const content=w.document.querySelector('.tab-panel.active .detail-content');
 expect(content.getAttribute('tabindex')).toBe('0');
 await w.blacksmithRouter.navigate({screen:'blacksmith',section:'details'});
 expect(content.tabIndex).toBe(0);
 expect(w.document.activeElement).toBe(content);
 expect(content.hasAttribute('data-route-focus')).toBe(true);
 expect(applying(w,content,/outline:\s*none/).length).toBeGreaterThan(0);
 w.document.documentElement.dataset.contrast='high';
 expect(applying(w,content,/outline:\s*none/).length).toBeGreaterThan(0);
 w.blacksmithRouter.destroy();w.close();
});

test('after the router focus ends the marker is gone and the ring is inset',async()=>{
 const w=await boot();
 const content=w.document.querySelector('.tab-panel.active .detail-content');
 await w.blacksmithRouter.navigate({screen:'blacksmith',section:'details'});
 content.blur();
 // The router waits a tick to tell a focus move from the window losing focus.
 await new Promise(r=>setTimeout(r,0));
 expect(content.hasAttribute('data-route-focus')).toBe(false);
 for(const contrast of [undefined,'high']){
  if(contrast)w.document.documentElement.dataset.contrast=contrast;
  expect(applying(w,content,/outline:\s*none/)).toEqual([]);
  expect(applying(w,content,/outline-offset:\s*-3px/).length).toBeGreaterThan(0);
 }
 // A later keyboard focus on the panel is not a router move, so it has no marker.
 content.focus();
 expect(content.hasAttribute('data-route-focus')).toBe(false);
 w.blacksmithRouter.destroy();w.close();
});

test('no focus rule depends on the tabindex value of the panel',()=>{
 expect(css).not.toMatch(/\[tabindex="-1"\]:focus-visible/);
 expect(css).toMatch(/\.tab-panel\[data-route-focus\]:focus-visible/);
});
