const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot,sleep}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

const OAT=fs.readFileSync(path.resolve(__dirname,'..','..','ui','vendor','oat','oat.min.js'),'utf8');

const envs=[];
afterEach(()=>{
 while(envs.length){ const env=envs.pop(); try{ expect(env.alerts).toEqual([]); } finally { env.w.close(); } }
});
// Oat's own script runs in the page. jsdom lacks the popover methods, so they are stubbed.
const start=({oat=true}={})=>{
 const env=boot({noDialogs:true});envs.push(env);
 const {w}=env;
 if(oat){
  w.HTMLElement.prototype.showPopover=function(){};
  w.HTMLElement.prototype.hidePopover=function(){};
  w.requestAnimationFrame=(fn)=>setTimeout(fn,0);
  w.eval(OAT);
 }
 return env;
};
const container=(d)=>d.querySelector('.toast-container');

t('a notification becomes an Oat toast with the matching variant',()=>{
 const {w,d}=start();
 w.showNotification('Saved it','success');
 w.showNotification('Careful now','warning');
 w.showNotification('It broke','error');
 w.showNotification('For your information');
 const toasts=[...container(d).querySelectorAll('output.toast')];
 expect(toasts.map(n=>n.getAttribute('data-variant'))).toEqual(['success','warning','error','info']);
 expect(toasts.map(n=>n.querySelector('.toast-message').textContent)).toEqual(['Saved it','Careful now','It broke','For your information']);
 expect(d.querySelector('.notification')).toBeNull();
});
t('an error toast is announced assertively and the others politely',()=>{
 const {w,d}=start();
 w.showNotification('It broke','error');
 w.showNotification('Saved it','success');
 const [bad,good]=container(d).querySelectorAll('output.toast');
 expect(bad.getAttribute('role')).toBe('alert');
 expect(bad.getAttribute('aria-live')).toBe('assertive');
 expect(good.getAttribute('role')).toBe('status');
 expect(good.getAttribute('aria-live')).toBe('polite');
});
t('a toast keeps the three second duration',async()=>{
 const {w,d}=start();
 const timers=[];const real=w.setTimeout;
 w.setTimeout=(fn,ms,...a)=>{ timers.push(ms); return real.call(w,fn,ms,...a); };
 w.showNotification('Saved it','success');
 expect(timers).toContain(3000);
 expect(timers).not.toContain(4000);
});
t('a toast stays announced and reachable while a modal is open',async()=>{
 const {w,d}=start();
 w.showNotification('Before','info');
 const done=w.showConfirmDialog({title:'Sure?',message:'Really?'});
 await sleep(10);
 expect(d.getElementById('dialog-modal-overlay').classList.contains('active')).toBe(true);
 w.showNotification('During','error');
 const box=container(d);
 expect(box.hasAttribute('inert')).toBe(false);
 expect(box.hasAttribute('aria-hidden')).toBe(false);
 expect(box.querySelectorAll('output.toast').length).toBe(2);
 d.querySelector('[data-dialog-role="cancel"]').click(); await done;
});
t('without Oat the notification falls back to the page banner and live region',async()=>{
 const {w,d}=start({oat:false});
 w.showNotification('Saved it','success');
 const banner=d.querySelector('.notification');
 expect(banner.textContent).toBe('Saved it');
 expect(banner.classList.contains('notification-success')).toBe(true);
 await sleep(120);
 expect(d.getElementById('blacksmith-live-region').textContent).toBe('Saved it');
});
