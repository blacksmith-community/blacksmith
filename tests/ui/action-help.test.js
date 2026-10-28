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
 w.eval(src);w.document.dispatchEvent(new w.Event('DOMContentLoaded'));return w;
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
 const modal=w.document.getElementById('modal');modal.showModal();const b=w.document.getElementById('inside');b.focus();
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
