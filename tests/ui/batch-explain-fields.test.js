const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const script=()=>fs.readFileSync(path.join(__dirname,'../../ui/view-guidance.js'),'utf8');

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);env.w.close();}});
function start(){
 const env=boot({noDialogs:true});envs.push(env);
 env.w.eval(script());
 const form=env.d.createElement('form');env.d.body.append(form);
 return {...env,form,api:env.w.BlacksmithViewGuidance};
}
const key=(w,name,target)=>{const event=new w.KeyboardEvent('keydown',{key:name,bubbles:true,cancelable:true});(target||w.document).dispatchEvent(event);return event;};

test('field builds a labelled toggle button whose popover sits right after it',()=>{
 const {d,form,api}=start();
 const node=api.field(form,'command-user');
 const button=node.querySelector('button');
 expect(button.type).toBe('button');
 expect(button.hasAttribute('data-context-help')).toBe(true);
 expect(button.getAttribute('aria-label')).toBe('About Run as');
 expect(button.getAttribute('aria-expanded')).toBe('false');
 const tip=d.getElementById(button.getAttribute('aria-controls'));
 expect(tip===null).toBe(false);
 expect(button.nextElementSibling===tip).toBe(true);
 expect(tip.hidden).toBe(true);
 expect(tip.getAttribute('popover')).toBe('manual');
 expect(tip.querySelector('[role=status]')===null).toBe(false);
});

test('click toggles the popover and announces its text through a status region',()=>{
 const {d,form,api}=start();
 const button=api.field(form,'command-timeout').querySelector('button');
 const tip=d.getElementById(button.getAttribute('aria-controls'));
 button.click();
 expect(tip.hidden).toBe(false);
 expect(button.getAttribute('aria-expanded')).toBe('true');
 expect(button.getAttribute('aria-describedby')).toBe(tip.id);
 expect(tip.querySelector('[role=status]').textContent).toContain('timeout');
 button.click();
 expect(tip.hidden).toBe(true);
 expect(button.getAttribute('aria-expanded')).toBe('false');
 expect(button.hasAttribute('aria-describedby')).toBe(false);
});

test('Escape closes the popover, is consumed, and keeps focus on the button',()=>{
 const {w,d,form,api}=start();
 const button=api.field(form,'command-parallel').querySelector('button');
 const tip=d.getElementById(button.getAttribute('aria-controls'));
 let reached=false;d.addEventListener('keydown',()=>{reached=true;});
 button.focus();button.click();
 const escape=key(w,'Escape',button);
 expect(escape.defaultPrevented).toBe(true);
 expect(reached).toBe(false);
 expect(tip.hidden).toBe(true);
 expect(d.activeElement===button).toBe(true);
});

test('Escape returns focus to the button when focus moved into the popover',()=>{
 const {w,d,form,api}=start();
 const button=api.field(form,'command-text').querySelector('button');
 const tip=d.getElementById(button.getAttribute('aria-controls'));
 button.click();tip.tabIndex=-1;tip.focus();
 key(w,'Escape',tip);
 expect(tip.hidden).toBe(true);
 expect(d.activeElement===button).toBe(true);
});

test('Escape with no open popover is left for other handlers',()=>{
 const {w,form,api}=start();
 api.field(form,'command-text');
 const escape=key(w,'Escape');
 expect(escape.defaultPrevented).toBe(false);
});

test('a pointer press outside closes the popover and a press inside keeps it',()=>{
 const {w,d,form,api}=start();
 const button=api.field(form,'rotate-mode').querySelector('button');
 const tip=d.getElementById(button.getAttribute('aria-controls'));
 button.click();
 tip.dispatchEvent(new w.Event('pointerdown',{bubbles:true}));
 expect(tip.hidden).toBe(false);
 d.body.dispatchEvent(new w.Event('pointerdown',{bubbles:true}));
 expect(tip.hidden).toBe(true);
});

test('opening a second popover closes the first, and ids stay unique',()=>{
 const {d,form,api}=start();
 const first=api.field(form,'rotate-mode').querySelector('button');
 const second=api.field(form,'rotate-converge').querySelector('button');
 const again=api.field(form,'rotate-mode').querySelector('button');
 const ids=[first,second,again].map(b=>b.getAttribute('aria-controls'));
 expect(new Set(ids).size).toBe(3);
 expect(d.querySelectorAll('[id="'+ids[0]+'"]').length).toBe(1);
 first.click();second.click();
 expect(d.getElementById(ids[0]).hidden).toBe(true);
 expect(d.getElementById(ids[1]).hidden).toBe(false);
 expect(first.getAttribute('aria-expanded')).toBe('false');
});

test('focus alone leaves the popover closed and raises no action tooltip',()=>{
 const {d,form,api}=start();
 const button=api.field(form,'concurrent-jobs').querySelector('button');
 button.focus();
 expect(d.getElementById(button.getAttribute('aria-controls')).hidden).toBe(true);
 expect(d.querySelector('.action-tooltip')===null).toBe(true);
});

test('hover shows the popover and leaving hides it unless it was pinned',async()=>{
 const {w,d,form,api}=start();
 const button=api.field(form,'additional-releases').querySelector('button');
 const tip=d.getElementById(button.getAttribute('aria-controls'));
 button.dispatchEvent(new w.Event('pointerenter'));
 expect(tip.hidden).toBe(false);
 button.dispatchEvent(new w.Event('pointerleave'));
 await new Promise(r=>setTimeout(r,200));
 expect(tip.hidden).toBe(true);
 button.dispatchEvent(new w.Event('pointerenter'));button.click();
 button.dispatchEvent(new w.Event('pointerleave'));
 await new Promise(r=>setTimeout(r,200));
 expect(tip.hidden).toBe(false);
});

test('an unknown key or a missing host returns null and adds nothing',()=>{
 const {d,form,api}=start();
 expect(api.field(form,'no-such-field')===null).toBe(true);
 expect(api.field(null,'command-text')===null).toBe(true);
 expect(form.children.length).toBe(0);
 expect(d.querySelector('[id^="batch-help-"]')===null).toBe(true);
});

test('every field entry has a label and a body for each documented key',()=>{
 const {api}=start();
 for(const name of ['rotate-mode','rotate-converge','additional-releases','command-text','command-user','command-timeout','command-parallel','concurrent-jobs']){
  const entry=api.getField(name);
  expect(Boolean(entry&&entry.label&&entry.body)).toBe(true);
 }
 expect(api.getField('nope')===null).toBe(true);
});

test('field buttons get a 24 pixel target and stay visible in forced colors',()=>{
 const css=fs.readFileSync(path.join(__dirname,'../../ui/view-guidance.css'),'utf8');
 const rule=(css.match(/\.field-guidance-button\s*\{[^}]*\}/)||[''])[0];
 expect(/width:\s*24px/.test(rule)&&/height:\s*24px/.test(rule)).toBe(true);
 const forced=css.slice(css.indexOf('@media (forced-colors: active)'));
 expect(forced.includes('.view-guidance-button')&&forced.includes('.view-guidance-tip')).toBe(true);
});
