const {test,expect}=require('bun:test');
const fs=require('fs');
const {main:{boot,sleep}}=require('./certificates/harness.js');
for (const query of ['certificates','rotate']) test(`Quicknav Escape cancels pending ${query} route`,async()=>{
 const {w,d}=boot();try{
 await sleep(30);w.Element.prototype.scrollIntoView=function(){};w.Element.prototype.getClientRects=function(){return this.matches('dialog[open]')?[1]:[]};const dialog=d.getElementById('navigation-dialog');
 dialog.showModal=()=>dialog.setAttribute('open','');dialog.close=()=>{dialog.removeAttribute('open');dialog.dispatchEvent(new w.Event('close'));};
 let signal,finish;
 w.blacksmithRouter={navigate:(route,options)=>{signal=options?.signal;return new Promise(resolve=>finish=resolve);}};
 w.eval(fs.readFileSync('ui/navigation.js','utf8'));if(d.readyState==='loading')d.dispatchEvent(new w.Event('DOMContentLoaded'));
 d.getElementById('navigation-trigger').click();const input=d.getElementById('navigation-search');input.value=query;input.dispatchEvent(new w.Event('input'));
 input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true}));await sleep(5);
 expect(signal).toBeDefined();expect(signal.aborted).toBe(false);
 d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));expect(signal.aborted).toBe(true);expect(dialog.open).toBe(false);
 finish(false);await sleep(5);expect(dialog.open).toBe(false);
 }finally{w.close()}
});
