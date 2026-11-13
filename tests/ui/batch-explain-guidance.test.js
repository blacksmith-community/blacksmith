const {test,expect,afterEach}=require('bun:test');
const fs=require('fs');
const path=require('path');
const {main:{boot}}=require('./certificates/harness.js');
const script=()=>fs.readFileSync(path.join(__dirname,'../../ui/view-guidance.js'),'utf8');

const envs=[];
afterEach(()=>{while(envs.length){const env=envs.pop();expect(env.alerts).toEqual([]);env.w.close();}});
function start(){const env=boot({noDialogs:true});envs.push(env);env.w.eval(script());return {...env,api:env.w.BlacksmithViewGuidance};}

const routes=[
 {screen:'upgrade',view:'new',step:0},
 {screen:'upgrade',view:'new',operation:'upgrade',step:1},
 {screen:'upgrade',view:'new',operation:'rotate',step:1},
 {screen:'upgrade',view:'new',operation:'converge',step:1},
 {screen:'upgrade',view:'new',operation:'command',step:1},
 {screen:'upgrade',view:'new',step:2},
 {screen:'upgrade',view:'new',step:3},
 {screen:'upgrade',view:'activity'},
 {screen:'upgrade',view:'activity',task:'7'}
];

test('every batch step and operation has its own header help',()=>{
 const {api}=start();
 const bodies=routes.map(route=>api.get(route).body);
 expect(new Set(bodies).size).toBe(routes.length);
});

test('the Run command options step explains Run command, not the generic step',()=>{
 const {api}=start();
 const entry=api.get({screen:'upgrade',view:'new',operation:'command',step:1});
 expect(entry.label).toBe('Run command');
 expect(entry.body).toContain('one shell command on every VM');
 expect(entry.body).toContain('pauses');
});

test('the operation step names all four operations and why to pick each',()=>{
 const {api}=start();
 const body=api.get({screen:'upgrade',view:'new',step:0}).body;
 for(const name of ['Choose Upgrade','Choose Rotate certificates','Choose Converge','Choose Run command']) expect(body).toContain(name);
 expect(body).toContain('Nothing starts until you confirm');
});

test('Activity warns that a paused job keeps its place under the job limit',()=>{
 const {api}=start();
 expect(api.get({screen:'upgrade',view:'activity'}).body).toContain('A paused job keeps its place');
 expect(api.get({screen:'upgrade',view:'activity',task:'7'}).body).toContain('Resume cleanup');
});

test('batch and field copy reads as spoken sentences',()=>{
 const {api}=start();
 const fields=['rotate-mode','rotate-converge','additional-releases','command-text','command-user','command-timeout','command-parallel','concurrent-jobs'].map(name=>api.getField(name).body);
 for(const body of [...routes.map(route=>api.get(route).body),...fields]){
  expect(body.includes(':')).toBe(false);
  expect(body.includes(';')).toBe(false);
  expect(/\be\.g\.|\bi\.e\./.test(body)).toBe(false);
  expect(body.includes('—')).toBe(false);
  expect(body.trim().endsWith('.')).toBe(true);
 }
});

test('the Run as help opens with the vcap limit and points privileged work at root',()=>{
 const {api}=start();
 const body=api.getField('command-user').body;
 expect(body.startsWith("A vcap command can't use sudo, su, or any other way to gain privileges, so choose root, without sudo, for privileged work.")).toBe(true);
 expect(body).toContain('commands.allow_root');
});
