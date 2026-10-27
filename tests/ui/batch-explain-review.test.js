const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const UI=path.join(__dirname,'../../ui');
const js=fs.readFileSync(path.join(UI,'blacksmith.js'),'utf8');
const html=fs.readFileSync(path.join(UI,'index.html'),'utf8');

// Each pair is batch copy that failed the spoken-form review, and the sentence that replaced it.
const fixes=[
 [js,'— keep current —','<option value="">Keep current</option>'],
 [js,'An operator turns it on with the','An administrator turns it on with the certificate_rotation kit parameter.'],
 [js,'so give service tools by their full path','so call service tools by their full path.'],
 [js,"'Temporary repair. BOSH can restore the old certificate until you converge.'","'This is a temporary repair, because BOSH can restore the old certificate until you converge.'"],
 [html,'the parallel setting does.','except for Run command, where the parallel setting sets it.'],
 [js,"help: 'This is on by default. A certificate job","help: 'A certificate job pauses after a failed instance, so you can look at the failure before more certificates change. The setting is on by default."],
 [js,'Continue remaining instances goes on with','The Continue remaining instances button never retries the failed instance. It moves on to the instances still waiting, so look at that failure before you resume.'],
 [js,'restores the old certificate until then.','and in the meantime a recreate or resurrection restores the old certificate.']
];

test('batch copy reads as spoken sentences after the writing review',()=>{
 expect(fixes.filter(([src,,now])=>!src.includes(now)).map(([,,now])=>now)).toEqual([]);
 expect(fixes.filter(([src,old])=>src.includes(old)).map(([,old])=>old)).toEqual([]);
});
