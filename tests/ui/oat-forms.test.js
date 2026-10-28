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

// A control rule of ours may size and place a control. It may not repeat
// Oat's border, fill, text color, or focus treatment, and none may turn the
// outline off, because that cancels the focus ring from forge-theme.css.
const block=(css,selector)=>{
 const i=css.indexOf('\n'+selector+' {');
 expect(i).toBeGreaterThan(-1);
 return css.slice(i,css.indexOf('}',i));
};
const hasBlock=(css,selector)=>css.includes('\n'+selector+' {');
const REPEATS=/(^|[;{\s])(border|border-color|border-radius|background|background-color|color|box-shadow|outline|transition):/;

test('upgrade filter controls keep their sizes and leave the look to Oat',()=>{
 const css=read('blacksmith.css');
 ['#upgrade .upgrade-filters select','#upgrade .upgrade-filters input[type="number"]','#upgrade .upgrade-range-selection input[type="number"]']
  .forEach((sel)=>expect(block(css,sel)).not.toMatch(REPEATS));
 ['#upgrade .upgrade-filters select:focus','#upgrade .upgrade-filters select:disabled','#upgrade .upgrade-filters input[type="number"]:focus','#upgrade .upgrade-filters input[type="number"]::placeholder','#upgrade .upgrade-range-selection input[type="number"]:focus']
  .forEach((sel)=>expect(hasBlock(css,sel)).toBe(false));
});

test('the settings and dialog text fields keep their size and font and leave the look to Oat',()=>{
 const css=read('blacksmith.css');
 expect(block(css,'.settings-input')).not.toMatch(REPEATS);
 expect(hasBlock(css,'.settings-input:focus')).toBe(false);
 expect(block(css,'.dialog-modal-input')).not.toMatch(REPEATS);
 expect(hasBlock(css,'.dialog-modal-input:focus')).toBe(false);
 expect(hasBlock(css,'[data-theme="dark"] .dialog-modal-input')).toBe(false);
});

test('the certificate settings number fields keep their size and leave the look to Oat',()=>{
 const css=read('blacksmith.css');
 expect(block(css,'.cert-settings-field input[type="number"]')).not.toMatch(REPEATS);
 expect(hasBlock(css,'.cert-settings-field input[aria-invalid="true"]')).toBe(false);
 expect(hasBlock(css,'.cert-settings-field input:focus-visible')).toBe(false);
});
