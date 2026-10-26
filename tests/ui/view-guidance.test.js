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
