const {test,expect}=require('bun:test');
const fs=require('fs');
const {JSDOM}=require('./certificates/support.js');
const {watchUncaught}=require('./watch-uncaught.js');
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
function boot(){
 const d=new JSDOM('<!doctype html><body><button id="copy" title="Copy to clipboard" aria-label="Copy deployment name"><svg></svg></button><button id="themeToggle">Anvil Light</button><dialog id="modal"><button id="inside" title="Copy certificate" aria-label="Copy certificate"><svg></svg></button></dialog></body>',{url:'http://localhost',runScripts:'outside-only',pretendToBeVisual:true});
 const w=watchUncaught(d.window);
 w.matchMedia=()=>({matches:false});
 w.HTMLElement.prototype.showModal=function(){this.setAttribute('open','')};
 w.HTMLElement.prototype.close=function(){this.removeAttribute('open');this.dispatchEvent(new w.Event('close'))};
 const src=fs.existsSync('ui/action-help.js')?fs.readFileSync('ui/action-help.js','utf8'):'';
 w.eval(fs.readFileSync('ui/icons.js','utf8'));w.eval(src);w.document.dispatchEvent(new w.Event('DOMContentLoaded'));return w;
}
test('icon help opens on focus, uses specific name, and restores title before activation',async()=>{
 const w=boot();try{
 const b=w.document.getElementById('copy');b.focus();
 expect(w.document.querySelector('[role=tooltip]')?.textContent).toBe('Copy deployment name');
 expect(b.title).toBe('');
 expect(b.hasAttribute('aria-describedby')).toBe(false);
 b.dispatchEvent(new w.MouseEvent('pointerdown',{bubbles:true}));
 expect(b.title).toBe('Copy to clipboard');
 expect(w.document.querySelector('[role=tooltip]')).toBeNull();
 }finally{w.close()}
});
test('tooltip observes changed titles, contains focus help inside modal, and consumes Escape only when shown',async()=>{
 const w=boot();try{
 const modal=w.document.getElementById('modal');modal.showModal();await sleep(300);const b=w.document.getElementById('inside');b.focus();
 expect(modal.querySelector('[role=tooltip]')).not.toBeNull();
 b.title='Certificate copied';await sleep(0);
 expect(modal.querySelector('[role=tooltip]')?.textContent).toBe('Certificate copied');
 expect(b.title).toBe('');
 const ev=new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true});b.dispatchEvent(ev);
 expect(ev.defaultPrevented).toBe(true);expect(w.document.querySelector('[role=tooltip]')).toBeNull();expect(b.title).toBe('Certificate copied');
 const again=new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true});b.dispatchEvent(again);expect(again.defaultPrevented).toBe(false);
 }finally{w.close()}
});
test('hover help remains when pointer enters tooltip and labels cover dynamic icon actions',async()=>{
 const w=boot();try{
 const b=w.document.getElementById('copy');b.dispatchEvent(new w.MouseEvent('pointerover',{bubbles:true}));await sleep(400);
 const tip=w.document.querySelector('[role=tooltip]');expect(tip).not.toBeNull();
 b.dispatchEvent(new w.MouseEvent('pointerout',{bubbles:true,relatedTarget:tip}));tip.dispatchEvent(new w.MouseEvent('pointerover',{bubbles:true,relatedTarget:b}));await sleep(150);
 expect(w.document.querySelector('[role=tooltip]')).toBe(tip);
 const n=w.document.createElement('button');n.setAttribute('aria-label','Refresh events');n.innerHTML='<svg></svg>';w.document.body.append(n);await sleep(0);
 expect(n.dataset.actionHelp).toBe('Refresh events');expect(n.hasAttribute('data-icon-help')).toBe(true);
 tip.dispatchEvent(new w.MouseEvent('pointerout',{bubbles:true,relatedTarget:w.document.body}));await sleep(150);expect(w.document.querySelector('[role=tooltip]')).toBeNull();
 }finally{w.close()}
});
test('theme chooser exposes all preferences and applies only chosen preference',()=>{
 const w=boot();try{
 const choices=[];w.blacksmithTheme={setPreference:p=>choices.push(p)};
 expect(w.blacksmithActionHelp).toBeDefined();w.blacksmithActionHelp.openThemes();
 const dialog=w.document.querySelector('#theme-choice-dialog');expect(dialog.open).toBe(true);
 const buttons=dialog.querySelectorAll('[data-theme-choice]');expect(buttons.length).toBe(5);
 dialog.querySelector('[data-theme-choice="high-contrast-dark"]').click();
 expect(choices).toEqual(['high-contrast-dark']);expect(dialog.open).toBe(false);expect(w.document.activeElement.id).toBe('themeToggle');
 }finally{w.close()}
});
test('minimized terminal activation and close are separate named native controls',()=>{
 const {w,d}=require('./certificates/harness.js').main.boot();try{
 const manager=w.TerminalManager;const calls=[];
 manager.sessions=new Map([['one',{context:{deploymentType:'service',deploymentName:'cache',instanceId:'node/0'},websocket:{readyState:1}}]]);
 manager.activeSession='one';manager.switchToSession=id=>calls.push('switch:'+id);manager.restoreAll=()=>calls.push('restore');manager.closeSession=id=>calls.push('close:'+id);
 manager.showMinimizedBar();
 const row=d.querySelector('.minimized-terminal-tab');const buttons=row.querySelectorAll('button');expect(buttons.length).toBe(2);
 expect(buttons[0].getAttribute('aria-label')).toContain('cache');expect(buttons[1].getAttribute('aria-label')).toContain('Close');
 buttons[1].click();expect(calls).toEqual(['close:one']);buttons[0].click();expect(calls).toEqual(['close:one','switch:one','restore']);
 }finally{w.close()}
});
test('supplementary help preserves existing descriptions and removal cleans up the tooltip',async()=>{
 const w=boot();try{
 const b=w.document.getElementById('copy');b.setAttribute('aria-describedby','field-help');b.title='Copies the current deployment name';b.focus();
 expect(b.getAttribute('aria-describedby')).toBe('field-help action-help-tooltip');
 b.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 expect(b.getAttribute('aria-describedby')).toBe('field-help');
 b.blur();b.focus();expect(w.document.querySelector('[role=tooltip]')).not.toBeNull();b.remove();await sleep(0);expect(w.document.querySelector('[role=tooltip]')).toBeNull();
 }finally{w.close()}
});
test('help uses titles migrated by Oat without losing field-specific copy labels',()=>{
 const w=boot();try{
 const b=w.document.getElementById('copy');b.dataset.tooltip=b.title;b.removeAttribute('title');b.focus();
 expect(w.document.querySelector('[role=tooltip]')?.textContent).toBe('Copy deployment name');
 b.dispatchEvent(new w.MouseEvent('click',{bubbles:true}));expect(b.title).toBe('Copy to clipboard');
 }finally{w.close()}
});
test('tooltip is measured at the left edge and sized to its content so it never shrinks at the viewport edge',()=>{
 const css=fs.readFileSync('ui/action-help.css','utf8');
 const rule=css.match(/\.action-tooltip\s*\{[^}]*\}/)[0];
 expect(rule).toMatch(/width:\s*max-content/);
 expect(rule).not.toMatch(/overflow-wrap:\s*anywhere/);
 const w=boot();try{
 const b=w.document.getElementById('copy');
 const seen=[];
 w.Element.prototype.getBoundingClientRect=function(){
  if(this.getAttribute&&this.getAttribute('role')==='tooltip'){seen.push(this.style.left);const l=parseFloat(this.style.left||0)+33,t=parseFloat(this.style.top||0)+5;return {left:l,top:t,right:l+90,bottom:t+20,width:90,height:20};}
  return {left:w.innerWidth-30,top:10,right:w.innerWidth,bottom:40,width:30,height:30};
 };
 b.focus();
 const tip=w.document.querySelector('[role=tooltip]');
 expect(seen[0]).toBe('0px');
 // The tip lands 33px right of where it was told, as in a dialog that is the containing block.
 expect(parseFloat(tip.style.left)+33).toBe(w.innerWidth-90-8);
 }finally{w.close()}
});
test('tooltip is placed again after a dialog that was still moving settles',async()=>{
 const w=boot();try{
 const b=w.document.getElementById('copy');
 let shift=20;
 w.Element.prototype.getBoundingClientRect=function(){
  if(this.getAttribute&&this.getAttribute('role')==='tooltip')return {left:parseFloat(this.style.left||0),top:0,right:90,bottom:20,width:90,height:20};
  return {left:100+shift,top:10,right:140+shift,bottom:40,width:40,height:30};
 };
 b.focus();
 const tip=w.document.querySelector('[role=tooltip]');
 expect(parseFloat(tip.style.left)).toBe(120);
 shift=0;await sleep(450);
 expect(parseFloat(tip.style.left)).toBe(100);
 }finally{w.close()}
});
test('tooltip inside a dialog stays within the dialog edge, which clips overflow',async()=>{
 const w=boot();try{
 const modal=w.document.getElementById('modal');modal.showModal();await sleep(300);
 w.Element.prototype.getBoundingClientRect=function(){
  if(this.getAttribute&&this.getAttribute('role')==='tooltip')return {left:parseFloat(this.style.left||0),top:0,right:0,bottom:0,width:90,height:20};
  if(this.id==='modal')return {left:0,top:0,right:300,bottom:400,width:300,height:400};
  return {left:270,top:10,right:296,bottom:36,width:26,height:26};
 };
 w.document.getElementById('inside').focus();
 const tip=modal.querySelector('[role=tooltip]');
 expect(parseFloat(tip.style.left)+90).toBeLessThanOrEqual(292);
 }finally{w.close()}
});
test('tooltip in a small dialog is capped to the dialog width and stays inside both edges, flipping above when there is no room below',async()=>{
 const w=boot();try{
 const modal=w.document.getElementById('modal');modal.showModal();await sleep(300);
 // A 300 by 200 dialog, a control near its bottom right, and a tip that wants 350 by 20.
 w.Element.prototype.getBoundingClientRect=function(){
  if(this.getAttribute&&this.getAttribute('role')==='tooltip'){
   const cap=parseFloat(this.style.maxWidth||'9999');const wd=Math.min(350,cap);
   const l=parseFloat(this.style.left||0),t=parseFloat(this.style.top||0);
   return {left:l,top:t,right:l+wd,bottom:t+20,width:wd,height:20};
  }
  if(this.id==='modal')return {left:0,top:0,right:300,bottom:200,width:300,height:200};
  return {left:250,top:170,right:290,bottom:195,width:40,height:25};
 };
 w.document.getElementById('inside').focus();
 const tip=modal.querySelector('[role=tooltip]');
 expect(parseFloat(tip.style.maxWidth)).toBeLessThanOrEqual(284);
 const l=parseFloat(tip.style.left),t=parseFloat(tip.style.top);
 expect(l).toBeGreaterThanOrEqual(8);
 expect(l+Math.min(350,parseFloat(tip.style.maxWidth))).toBeLessThanOrEqual(292);
 // No room below the control, so the tip sits above it, inside the dialog.
 expect(t+20).toBeLessThanOrEqual(170);
 expect(t).toBeGreaterThanOrEqual(8);
 }finally{w.close()}
});

test('focus that code sets as a dialog opens shows no tooltip, and later keyboard focus does',async()=>{
 const w=boot();try{
 const modal=w.document.getElementById('modal');modal.showModal();
 const b=w.document.getElementById('inside');b.focus();
 expect(w.document.querySelector('[role=tooltip]')).toBeNull();
 b.blur();await sleep(300);b.focus();
 expect(w.document.querySelector('[role=tooltip]')).not.toBeNull();
 }finally{w.close()}
});

test('focus that is not focus-visible shows no tooltip',async()=>{
 const w=boot();try{
 const b=w.document.getElementById('copy');
 const original=b.matches;b.matches=function(s){return s===':focus-visible'?false:original.call(this,s);};
 b.focus();
 expect(w.document.querySelector('[role=tooltip]')).toBeNull();
 }finally{w.close()}
});
