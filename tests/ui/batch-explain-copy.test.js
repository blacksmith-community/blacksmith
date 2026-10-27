const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');
const UI=path.join(__dirname,'../../ui');
const js=fs.readFileSync(path.join(UI,'blacksmith.js'),'utf8');
const html=fs.readFileSync(path.join(UI,'index.html'),'utf8');

// Each pair is copy that said what without why, and the text that replaced it.
const rewrites=[
 ["'Certificate inventory is turned off.'","Certificate inventory is turned off on this broker, so certificate operations can\\'t start here. An administrator turns it on with certificates.inventory.enabled."],
 ['in the confirmation field.`','in the confirmation field. The typed word keeps a stray click or keypress from starting this operation.`'],
 ['Shown in Activity, e.g.','A name helps you find this job in Activity later, such as “October service update”. Leave it blank to use a generated name.'],
 ["'The last attempt to start this run did not finish.'","The last attempt to start this run didn\\'t finish, so check Activity before you run it again."],
 ['The selected service’s primary release. Choose a service above to enable.','This is the service’s own release. Each service has a different one, so choose a single service above to pick a version.'],
 ['Choose other releases uploaded to the director. Each applies only to instances that already use it.','Use this when the change you need is in a release other than the service’s own. Each release goes only to instances that already use it, so this never adds new software.'],
 ['No service instances available.','Blacksmith has no service instances yet, so there\\\'s nothing to select.'],
 ['No instances match the current filters.</div>','No instances match the current filters. Clear a filter or change the search to see more.'],
 ['reason = `Busy: ${holder}`','reason = `Busy with ${holder}. You can select it once that finishes.`'],
 ['`Busy: ${holder}`)}','`Busy with ${holder}. You can select it once that finishes.`)}'],
 ["'No instance is waiting for a deploy of its patched certificates.'","No instance is waiting for a deploy of its patched certificates, so there\\'s nothing to converge. An instance appears here after a patch in place leaves it unconverged."],
 ['BOSH has not picked up yet.','BOSH hasn\\\'t picked up yet, so a recreate can no longer bring the old ones back.'],
 ["['pending', 'Leave unconverged', 'The instance stays","'Leave unconverged', 'Choose this to restart the jobs only once today. The instance stays flagged as unconverged until a converge runs, and a recreate or resurrection restores the old certificate until then.'"],
 ["['immediate', 'Converge right away', 'Every job","'Converge right away', 'Choose this to make the repair permanent in the same job. Every job restarts a second time, when the deploy that follows the patch runs.'"],
 ["'Temporary repair: BOSH can restore the old certificate'","'Temporary repair. BOSH can restore the old certificate until you converge.'"],
 ['Updates certificate files in place and restarts affected jobs.','Patch writes the new certificate files onto the running VMs and restarts the affected jobs, so the new certificates are in service without waiting for a deploy. Choose below when BOSH should deploy them.'],
 ['Issues new certificates and deploys them through BOSH using the deployment update policy, including canaries and drains.','Redeploy issues new certificates and deploys them through BOSH with the deployment\\\'s update policy, so canaries and drains protect the service while it rolls.'],
 ["'e.g. ","'For example, October certificate renewal'"],
 ['Scheduled renewal — change CHG-1234',"'For example, scheduled renewal for change CHG-1234'"],
 ['e.g. Check disk usage',"'For example, check disk usage on valkey nodes'"],
 ['Investigating an incident — change CHG-1234',"'For example, investigating an incident for change CHG-1234'"],
 ['A short label to find this operation in Activity.','A short name helps you find this job in Activity later. Leave it blank to use a generated name.'],
 ['against the instance while the patch runs.',"Before you patch, make sure nobody runs director commands against this instance until the patch finishes. BOSH still records the old certificate, so a deploy, recreate, or resurrection during the patch would put it back, and the broker can\\'t stop commands run against the director directly."],
 ['so here is a fresh one.','The state changed since the last preview, so this is a fresh one. Check it again before you confirm.'],
 ['A redeploy applies the patched values as permanent.','A redeploy makes the patched certificates permanent, so a recreate can no longer bring the old ones back.'],
 ['A failed health gate pauses the task.','A failed health check pauses the task, so you can look into it before more changes go out.'],
 ["|| 'Certificate rotation is turned off on this broker.'","|| 'Certificate rotation is turned off on this broker. An operator turns it on with the certificate_rotation kit parameter.'"],
 ['or converge instances.</div>','No batch jobs yet. Choose New Operation to upgrade, rotate certificates, converge, or run a command.'],
 ['The failed instance is not retried.','Continue remaining instances goes on with the instances still waiting. It never retries the failed one, so look at that failure first.'],
 ['Are you sure you want to cancel this job?',"Cancelling stops the BOSH task that\\'s running now and skips the remaining instances. The instance being deployed may be left part way through its update."],
 ["'Saved!'","'Saved'"],
 ['`Failed to start upgrade:','`The upgrade wasn\'t started. ${'],
 ['This director cannot do what the operation needs, and nothing was changed.',"This director can\\'t do what the operation needs, so nothing was changed. Update the director, or leave this instance out."]
];

test('batch copy says why, and the old wording is gone',()=>{
 const missing=rewrites.filter(([,now])=>!js.includes(now)).map(([,now])=>now);
 const left=rewrites.filter(([old])=>js.includes(old)).map(([old])=>old);
 expect(missing).toEqual([]);
 expect(left).toEqual([]);
});

test('the empty Activity list in the page names every operation',()=>{
 expect(html.includes('Select instances and a target (stemcell and/or release)')).toBe(false);
 expect(html.includes('<div class="no-tasks">No batch jobs yet. Choose New Operation to upgrade, rotate certificates, converge, or run a command.</div>')).toBe(true);
});
