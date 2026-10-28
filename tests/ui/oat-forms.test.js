const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');

test('the task progress bar is Oat progress, with only status colors from us',()=>{
 const css=read('blacksmith.css');
 expect(css).not.toContain('upgrade-task-progress-bar');
 const i=css.indexOf('#upgrade .upgrade-task-progress {');
 expect(i).toBeGreaterThan(-1);
 expect(css.slice(i,css.indexOf('}',i))).not.toMatch(/(^|[;{\s])(height|background|background-color|border-radius|overflow):/);
});
