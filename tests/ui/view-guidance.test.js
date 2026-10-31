const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const script=()=>fs.readFileSync(path.join(__dirname,'../../ui/view-guidance.js'),'utf8');

test('every entity section has concise scoped help, including nested contexts',()=>{
 const {w}=boot();
 w.eval(script());
 const api=w.BlacksmithViewGuidance;
 for(const section of ['details','vms','tasks','events','blacksmith-logs','certificates','plans','broker','configs','config','manifest']) expect(api.get({screen:'blacksmith',section}).body).toBeTruthy();
 for(const section of ['details','vms','events','instance-logs','certificates','credentials','testing','manifest']) expect(api.get({screen:'services',section}).body).toBeTruthy();
 expect(api.get({screen:'services',section:'certificates',subview:'endpoint'}).body).toContain('observed');
 expect(api.get({screen:'upgrade',operation:'rotate',step:'options'}).body).toContain('BOSH');
 expect(api.get({screen:'services',section:'testing',subview:'flush'}).body).toContain('delete');
 expect(api.get({screen:'invalid'})).toBeNull();
 w.close();
});

test('header info opens on focus and click, dismisses with Escape and cleans up on navigation',()=>{
 const {w,d}=boot();w.eval(script());const host=d.createElement('header');d.body.append(host);
 const api=w.BlacksmithViewGuidance;api.mount(host,{screen:'blacksmith',section:'plans'});
 const button=host.querySelector('button');expect(button?.getAttribute('aria-label')).toBe('About service plans');
 expect(host.querySelector('details')).toBeNull();button.focus();
 expect(d.querySelector('[role=tooltip]').hidden).toBe(false);
 expect(button.getAttribute('aria-describedby')).toBe(d.querySelector('[role=tooltip]').id);
 button.click();button.blur();expect(d.querySelector('[role=tooltip]').hidden).toBe(false);
 const escape=new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true});d.dispatchEvent(escape);
 expect(escape.defaultPrevented).toBe(true);expect(d.querySelector('[role=tooltip]').hidden).toBe(true);
 api.mount(host,{screen:'services',section:'testing',subview:'flush'});
 expect(host.querySelectorAll('button').length).toBe(1);expect(d.querySelectorAll('[role=tooltip]').length).toBe(1);
 expect(host.textContent).not.toContain('delete');
 host.querySelector('button').click();expect(d.querySelector('[role=tooltip]').textContent).toContain('delete');
 api.mount(host,{screen:'invalid'});expect(host.querySelector('button')).toBeNull();expect(d.querySelector('[role=tooltip]')).toBeNull();w.close();
});

const {afterEach}=require('bun:test');
const keyEnv=[];
afterEach(()=>{while(keyEnv.length){const env=keyEnv.pop();expect(env.alerts).toEqual([]);}});
function bootKeys(){
 const env=boot({noDialogs:true});keyEnv.push(env);
 const logged=[];env.w.console.error=(...a)=>logged.push(a);env.w.eval(script());
 const host=env.d.createElement('header');env.d.body.append(host);
 return {...env,host,logged};
}
const batchRoutes=[{screen:'upgrade',view:'new',operation:'upgrade',step:0},{screen:'upgrade',view:'new',operation:'rotate',step:1},{screen:'upgrade',view:'new',operation:'converge',step:2},{screen:'upgrade',view:'new',step:3},{screen:'upgrade',view:'activity'},{screen:'upgrade',view:'activity',task:'7'}];

test('every batch view mounts a keyboard button beside the info button',()=>{
 const {w,host}=bootKeys();
 for(const route of batchRoutes){
  w.BlacksmithViewGuidance.mount(host,route);
  const buttons=[...host.querySelectorAll('button')];
  expect(buttons.length).toBe(2);
  expect(buttons[0].hasAttribute('data-context-help')).toBe(true);
  const key=buttons[1];
  expect(key.tagName).toBe('BUTTON');expect(key.type).toBe('button');
  expect(key.getAttribute('aria-label')).toBe('Keyboard shortcuts for this view');
  expect(key.title).toBe('Keyboard shortcuts for this view');
  expect(key.querySelector('svg').getAttribute('aria-hidden')).toBe('true');
 }
 w.close();
});

test('views without shortcut help get no keyboard button',()=>{
 const {w,d,host}=bootKeys();
 for(const route of [{screen:'blacksmith',section:'plans'},{screen:'services',section:'vms'},{screen:'certificates'}]){
  w.BlacksmithViewGuidance.mount(host,route);
  expect(host.querySelector('[data-keyboard-help]')===null).toBe(true);
 }
 expect(d.getElementById('keyboard-shortcuts-tip')===null).toBe(true);
 w.close();
});

test('keyboard button toggles aria-expanded, keeps kbd and dl markup, and closes on Escape with focus returned',()=>{
 const {w,d,host}=bootKeys();
 w.BlacksmithViewGuidance.mount(host,batchRoutes[0]);
 const key=host.querySelector('[data-keyboard-help]'),tip=d.getElementById('keyboard-shortcuts-tip');
 expect(key.getAttribute('aria-controls')).toBe('keyboard-shortcuts-tip');
 expect(key.getAttribute('aria-expanded')).toBe('false');expect(tip.hidden).toBe(true);
 key.focus();expect(key.getAttribute('aria-expanded')).toBe('true');expect(tip.hidden).toBe(false);
 key.click();key.blur();expect(tip.hidden).toBe(false);
 expect(tip.querySelectorAll('kbd').length).toBeGreaterThan(5);
 expect([...tip.querySelectorAll('dt')].map(e=>e.textContent)).toEqual(['N','A','U / R / C / X','→ / ←']);
 expect(tip.querySelectorAll('dd').length).toBe(4);
 expect(tip.textContent).toContain('Shortcuts pause in text fields and dialogs');
 host.setAttribute('tabindex','-1');host.focus();
 expect(d.activeElement===host).toBe(true);
 d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 expect(tip.hidden).toBe(true);expect(key.getAttribute('aria-expanded')).toBe('false');
 expect(d.activeElement===key).toBe(true);
 key.click();expect(tip.hidden).toBe(false);key.click();expect(tip.hidden).toBe(true);
 key.click();d.body.dispatchEvent(new w.Event('pointerdown',{bubbles:true}));expect(tip.hidden).toBe(true);
 w.close();
});

test('remounting replaces both popovers and leaving batch removes them',()=>{
 const {w,d,host}=bootKeys();
 w.BlacksmithViewGuidance.mount(host,batchRoutes[0]);w.BlacksmithViewGuidance.mount(host,batchRoutes[4]);
 expect(d.querySelectorAll('[role=tooltip]').length).toBe(2);expect(host.querySelectorAll('button').length).toBe(2);
 w.BlacksmithViewGuidance.mount(host,{screen:'blacksmith',section:'plans'});
 expect(d.querySelectorAll('[role=tooltip]').length).toBe(1);
 w.BlacksmithViewGuidance.mount(host,{screen:'invalid'});expect(d.querySelectorAll('[role=tooltip]').length).toBe(0);
 w.close();
});

test('the old shortcuts disclosure is gone from the markup',()=>{
 const html=fs.readFileSync(path.join(__dirname,'../../ui/index.html'),'utf8');
 expect(html.includes('batch-keyboard-help')).toBe(false);
 const {w,d}=bootKeys();
 expect(d.querySelector('details.batch-keyboard-help')===null).toBe(true);
 w.close();
});

test('Enter and Space on either button toggle its popover',()=>{
 const {w,d,host}=bootKeys();
 w.BlacksmithViewGuidance.mount(host,batchRoutes[0]);
 const press=(button,key)=>{const down=new w.KeyboardEvent('keydown',{key,bubbles:true,cancelable:true});button.dispatchEvent(down);button.dispatchEvent(new w.KeyboardEvent('keyup',{key,bubbles:true,cancelable:true}));return down;};
 for(const [selector,id] of [['[data-keyboard-help]','keyboard-shortcuts-tip'],['[data-context-help]','view-guidance-tip']]){
  const button=host.querySelector(selector),tip=d.getElementById(id);
  for(const key of ['Enter',' ']){
   expect(tip.hidden).toBe(true);
   const down=press(button,key);
   expect(down.defaultPrevented).toBe(true);expect(tip.hidden).toBe(false);expect(button.getAttribute('aria-expanded')).toBe('true');
   press(button,key);expect(tip.hidden).toBe(true);expect(button.getAttribute('aria-expanded')).toBe('false');
  }
 }
 w.close();
});

test('opening one popover closes the other, so one Escape leaves none open',()=>{
 const {w,d,host}=bootKeys();
 w.BlacksmithViewGuidance.mount(host,batchRoutes[0]);
 const info=host.querySelector('[data-context-help]'),key=host.querySelector('[data-keyboard-help]');
 const infoTip=d.getElementById('view-guidance-tip'),keyTip=d.getElementById('keyboard-shortcuts-tip');
 info.click();expect(infoTip.hidden).toBe(false);
 key.click();expect(keyTip.hidden).toBe(false);expect(infoTip.hidden).toBe(true);expect(info.getAttribute('aria-expanded')).toBe('false');
 d.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 expect(infoTip.hidden).toBe(true);expect(keyTip.hidden).toBe(true);
 w.close();
});
