const {test,expect}=require('bun:test');
const {main:{boot}}=require('./certificates/harness.js');
const {JSDOM}=require('./certificates/support.js');
const t=JSDOM?test:test.skip;

// A secret that reaches any console method must land in the logs array the
// secret checks read, so none of them can be used to leak around it.
t('every console method the page could leak through is captured',()=>{
 const env=boot({noDialogs:true}),{w}=env;try{
 const before=env.logs.length;
 const methods=['trace','dir','table','group','groupCollapsed'];
 methods.forEach((m,i)=>w.console[m](`leak-${m}-${i}`));
 w.console.assert(false,'leak-assert-failed');
 w.console.assert(true,'leak-assert-passed');
 const added=env.logs.slice(before).join('\n');
 methods.forEach((m,i)=>expect(added.includes(`leak-${m}-${i}`)).toBe(true));
 expect(added.includes('leak-assert-failed')).toBe(true);
 expect(added.includes('leak-assert-passed')).toBe(false);
 }finally{w.close();}
});
