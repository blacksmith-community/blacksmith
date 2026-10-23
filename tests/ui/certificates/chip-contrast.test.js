const { test, expect } = require('bun:test');
const H = require('./harness.js');
const { checks, JSDOM } = require('./support.js');
const fs = require('fs');
const path = require('path');

const { ok, failures } = checks();
const t = JSDOM ? test : test.skip;

test('rotate chip contrast', () => {
  const root=path.resolve(__dirname,'..','..','..');
  const css=fs.readFileSync(root+'/ui/blacksmith.css','utf8'), con=fs.readFileSync(root+'/ui/console.css','utf8');
  const L=h=>{h=h.replace('#','');const c=[0,2,4].map(i=>parseInt(h.slice(i,i+2),16)/255).map(x=>x<=.03928?x/12.92:((x+.055)/1.055)**2.4);return .2126*c[0]+.7152*c[1]+.0722*c[2];};
  const ratio=(a,b)=>{const [x,y]=[L(a),L(b)].sort((m,n)=>n-m);return (x+.05)/(y+.05);};
  const block=(src,sel)=>{const i=src.lastIndexOf('\n'+sel+' {');return i<0?'':src.slice(i,src.indexOf('}',i));};
  const tok=(src,sel,name)=>{const m=block(src,sel).match(new RegExp('--'+name+':\\s*(#[0-9a-fA-F]{6})'));return m&&m[1];};
  const tail=css.slice(css.indexOf('.rotate-chip[aria-pressed="true"]')-400);
  ok(!/#1d4ed8/i.test(css.slice(css.indexOf('.rotate-chip {')>0?css.indexOf('.rotate-chip {'):0).replace(/--rotate-pressed-bg:\s*#1d4ed8/,'')),'no hard-coded blue left in the chip rules');
  const ring=css.match(/\.rotate-chip:focus-visible,\s*#upgrade \.rotate-operation-option input:focus-visible\s*\{\s*outline:\s*2px solid ([^;]+);/);
  ok(ring&&ring[1]==='var(--border-active)','focus ring uses the console focus token: '+(ring&&ring[1]));
  const lightRing=tok(con,':root','border-active')||con.match(/--border-active:\s*(#[0-9a-f]{6})/i)[1];
  const darkRing=tok(con,'[data-theme="dark"]','border-active');
  const lightBg=[tok(con,':root','bg-primary'),tok(con,':root','bg-secondary')], darkBg=[tok(con,'[data-theme="dark"]','bg-primary'),tok(con,'[data-theme="dark"]','bg-secondary')];
  ok(lightBg.every(b=>ratio(lightRing,b)>=3),'light ring >= 3:1 on both backgrounds: '+lightBg.map(b=>ratio(lightRing,b).toFixed(2)));
  ok(darkBg.every(b=>ratio(darkRing,b)>=3),'dark ring >= 3:1 on both backgrounds: '+darkBg.map(b=>ratio(darkRing,b).toFixed(2)));
  const lp=tok(css,'#upgrade','rotate-pressed-bg'), lt=tok(css,'#upgrade','rotate-pressed-text'), dp=tok(css,'[data-theme="dark"] #upgrade','rotate-pressed-bg'), dt=tok(css,'[data-theme="dark"] #upgrade','rotate-pressed-text');
  ok(lp&&lt&&dp&&dt,'pressed pair defined for both themes');
  ok(lp&&ratio(lp,lt)>=4.5&&dp&&ratio(dp,dt)>=4.5,'pressed text >= 4.5:1: '+[lp&&ratio(lp,lt),dp&&ratio(dp,dt)].map(n=>n&&n.toFixed(2)));
  ok(lp&&ratio(lp,lightBg[0])>=3&&dp&&ratio(dp,darkBg[0])>=3,'pressed chip >= 3:1 against the page in both themes');
  ok(/\.rotate-chip\[aria-pressed="true"\]\s*\{\s*background-color:\s*var\(--rotate-pressed-bg\)/.test(css),'pressed rule reads the themed token');
  expect(failures).toEqual([]);
});
