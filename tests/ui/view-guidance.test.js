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

test('help is a single named disclosure, retains expansion and never renders HTML from context',()=>{
 const {w,d}=boot(); w.eval(script());
 const host=d.createElement('div'); d.body.append(host);
 const api=w.BlacksmithViewGuidance;
 api.mount(host,{screen:'blacksmith',section:'plans'});
 expect(host.querySelectorAll('details').length).toBe(1);
 expect(host.querySelector('summary').textContent).toBe('About service plans');
 host.querySelector('details').open=true;
 api.mount(host,{screen:'blacksmith',section:'plans'});
 expect(host.querySelector('details').open).toBe(true);
 api.mount(host,{screen:'services',section:'testing',subview:'flush'});
 expect(host.querySelectorAll('details').length).toBe(1);
 expect(host.querySelector('details').open).toBe(false);
 api.mount(host,{screen:'invalid',section:'<img src=x>'});
 expect(host.querySelector('details')).toBeNull();
 w.close();
});
