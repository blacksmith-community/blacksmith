/* Contextual explanations are shared by pointer, touch, and keyboard users. */
(function (root) {
  'use strict';
  const help = (label, body) => ({label, body});
  const shared = {
    vms: help('VMs', 'Inspect reported VM and job state. Resurrection is the BOSH policy for recreating failed VMs; it is separate from the state reported here. Refresh retrieves new data. Opening a terminal starts an SSH session for that VM.'),
    events: help('events', 'Inspect events for this deployment. Search narrows the rows; copy includes the filtered rows. No matching events does not mean that the deployment is healthy.'),
    logs: help('logs', 'Choose a source and file, then search the loaded lines. Collection details explain missing or truncated files. Copy includes filtered loaded rows, not an entire remote file. Refresh retrieves new data.'),
    manifest: help('the manifest', 'Inspect the deployment configuration by section, or use YAML for the complete document. Manifest values describe configuration; they are not a live health check. Copy controls name the value or document being copied.'),
    certificates: help('certificates', 'Expiry describes time remaining; health describes findings and observed deployment state. Missing or stale observations are not a successful health check. Open a certificate for its evidence and available actions.')
  };
  const broker = {
    details: help('Summary', 'Start here for broker context and fleet counts. Summary cards open the corresponding view. Reported VM alerts are not a complete service health check; inspect the instance for more detail.'),
    vms: shared.vms,
    tasks: help('tasks', 'Inspect BOSH tasks using search, task scope, deployment, and state filters. Open a task ID for its metadata and output. Closing task details returns to this list; a task state is separate from an instance health check.'),
    events: shared.events,
    'blacksmith-logs': shared.logs,
    certificates: shared.certificates,
    plans: help('service plans', 'Plans are grouped by service. Each row shows the current instance count and configured limit; Unlimited means no plan limit is configured. Select a plan to inspect its description and VM configuration. Not reported means the value is unavailable.'),
    broker: help('endpoints', 'Choose the Cloud Foundry endpoint to inspect. Connect and Disconnect change the connection explicitly; Marketplace lists its offerings and Services lists its service instances. Register adds this broker to the selected endpoint.'),
    configs: help('BOSH configs', 'Filter configuration records by type or name. The active version is marked in the list. Open a config ID to inspect its versions or compare changes; viewing a version does not apply it.'),
    config: help('Broker settings', 'Runtime settings can be changed using their Apply buttons. Each field explains its scope and units. Current Configuration is read-only. A failed save leaves your entered value available to correct and retry.'),
    manifest: shared.manifest
  };
  const instance = {
    details: help('Summary', 'Inspect the selected service instance and its reported state. All instances returns to the collection. Service and plan identify its offering; the instance ID distinguishes instances with the same display name.'),
    vms: shared.vms,
    events: shared.events,
    'instance-logs': shared.logs,
    certificates: shared.certificates,
    credentials: help('credentials', 'Connection fields and certificate material belong to this instance. Copy controls copy the named field. Certificate blocks preserve their original line breaks. Treat passwords and private keys as sensitive when copying or sharing them.'),
    testing: help('Operations', 'Choose an operation before entering its inputs. Connection checks inspect connectivity; commands that write, delete, publish, consume, or administer a service may change its data or behavior. Review the selected operation and target before running it.'),
    manifest: shared.manifest
  };
  const certificateSources = {
    overview: shared.certificates,
    trusted: help('trusted certificates', 'Inspect certificates from the selected trusted files. A file path identifies the source; the certificate details show identity, issuer, and validity. An unreadable file is not evidence that no certificate exists.'),
    config: help('configuration certificates', 'Inspect certificate material found in configuration. Configuration is one source of evidence; it does not by itself prove which certificate an endpoint currently serves.'),
    manifest: help('manifest certificates', 'Inspect certificate variables referenced by this instance manifest. Compare them with deployment and endpoint observations when checking what is actually in use.'),
    endpoint: help('endpoint certificates', 'Choose the endpoint to inspect before running a probe. Results show the certificate observed at that target and time; they do not prove coverage of other listeners or nodes.')
  };
  const manifestSources = {
    global: help('manifest context', 'Deployment name and global configuration identify this manifest. Copy controls copy the named value.'),
    'instance-groups': help('instance groups', 'Choose an instance group to inspect its jobs, VM type, networks, and properties. Counts here are configured counts, not a report of healthy VMs.'),
    releases: help('manifest releases', 'These are the release versions referenced by the manifest. Inspect the exact name and version before comparing them with upgrade targets.'),
    stemcells: help('stemcells', 'Stemcells define the operating system images referenced by the manifest. A configured stemcell version is separate from a live VM observation.'),
    features: help('manifest features', 'Feature flags describe BOSH deployment behavior. Viewing these values does not change them.'),
    update: help('deployment update policy', 'The update policy controls deployment rollout, including canaries and parallel updates. Review it when assessing the impact of a redeploy or upgrade.'),
    variables: help('manifest variables', 'Variables describe values resolved for the deployment. Certificate variables may need separate inspection of deployed and endpoint evidence.'),
    addons: help('manifest addons', 'Addons apply additional jobs and configuration according to their inclusion rules. Inspect the rules before assuming every VM receives an addon.'),
    manifest: help('manifest YAML', 'This is the complete manifest document. Whitespace is preserved; use the local scroll area for long lines and Copy manifest for the complete content.')
  };
  const testOperations = {
    test: help('connection testing', 'Check connectivity to the selected instance using its configured credentials. A successful connection is not a complete application health check.'),
    info: help('Info', 'Read server information from the selected service. Results describe the response at the time the operation ran.'),
    get: help('Get', 'Read the value for a key. Check the database and key before running the request.'),
    keys: help('Keys', 'Find keys matching the supplied pattern. A broad pattern may inspect many keys; narrow the pattern on large databases.'),
    set: help('Set', 'Write a value to a key. This can replace existing data; check the database, key, and value before running it.'),
    delete: help('Delete', 'Delete the selected key. Check the target database and key before confirming the operation.'),
    command: help('Command', 'Run a command against the service. Commands may modify data or service behavior; inspect the complete command and target before execution.'),
    flush: help('Flush', 'This operation can delete many keys. Review the database or server scope and the confirmation carefully before executing it.'),
    publish: help('Publish', 'Publish a message to the selected exchange and routing key. Check the target and payload before sending.'),
    consume: help('Consume', 'Retrieve messages from the selected queue. Acknowledgement options can remove messages; review those options before running the request.'),
    queues: help('Queue info', 'Read queue information for the selected service. Counts describe the response at the time the request ran.'),
    'queue-ops': help('Queue ops', 'Queue operations may declare, purge, or delete queues. Inspect the chosen action, queue, and options before executing it.'),
    management: help('Management', 'Management requests can inspect or change RabbitMQ configuration. Check the method, path, and payload before execution.'),
    rabbitmqctl: help('rabbitmqctl', 'Administrative commands can change RabbitMQ behavior. Review the complete command and target before execution.'),
    plugins: help('Plugins', 'Plugin operations can change available RabbitMQ features. Review the action and plugin names before applying changes.')
  };
  const batch = {
    operation: help('batch operations', 'Pick the operation that matches the job you need done. Choose Upgrade to move instances to a newer stemcell or service release. Choose Rotate certificates when certificates are expiring or need replacing. Choose Converge after a patch in place, so that BOSH deploys the patched certificates and a recreate can\'t bring the old ones back. Choose Run command for one-off maintenance on every VM of the selected instances, such as turning on a RabbitMQ feature flag. Nothing starts until you confirm on the Review step.'),
    upgrade: help('upgrade options', 'Choose versions here for the instances you\'ll select next, and the job redeploys them one instance at a time. Set a deploy version for the system image, the service\'s own release, or any additional release, and leave the rest set to keep current. Use Minimum current version to upgrade only instances that are already at or past a version, such as when you finish a rollout in stages. If an instance fails, the job records the failure and moves on to the next instance, so watch its progress in Activity. Turn on Pause the job if an instance fails when you\'d rather look at a failure before the rest go ahead.'),
    rotate: help('rotation options', 'Choose Redeploy for a routine rotation. It issues new certificates and rolls them out through BOSH with the deployment\'s own update policy, so canaries and drains protect the service. Choose Patch in place when the new certificate has to be in service sooner than a deploy can finish. Patch writes the files onto the running VMs and restarts the affected jobs, but BOSH still holds the old certificate until a converge runs, so a recreate or resurrection would bring the old one back. A certificate job pauses after a failed instance while Pause the job if an instance fails is on, so you can look into the failure before more certificates change. That checkbox starts from the broker\'s certificate setting.'),
    converge: help('convergence', 'Converge redeploys instances that a patch in place left unconverged, so BOSH deploys the certificate versions the patch already stored and a recreate can no longer bring the old ones back. Run it when a restart suits you, such as in a maintenance window, because the deploy restarts every affected job. It doesn\'t issue new certificates.'),
    command: help('Run command', 'Use Run command for maintenance that BOSH doesn\'t do for you. It runs one shell command on every VM, or node, of the selected instances. Deployments run one at a time, and each node gets the command once, with no second try, even after a failure. When a node fails, times out, or loses its connection, the nodes already running finish, the rest of that deployment is skipped, and the job pauses so you can decide whether the next deployment should still get the command. Before anything runs, you\'ll type a confirmation that names the user the command runs as.'),
    instances: help('instance selection', 'Select every instance the operation should touch. Your selection stays when you filter or change pages, and the count above the list tells you when filters hide part of it, so use View selected to check it before you go on. An instance that another operation is using shows as busy and can\'t be selected until that operation releases it. Set Availability to Eligible only to hide those instances.'),
    review: help('review and confirmation', 'This is your last check before anything changes. The review comes from a fresh preview, so it shows what the broker will actually do, and if the state changes before you confirm, you\'ll get a new preview to check. You\'ll type a word first for any operation on more than ten instances, any patch, any upgrade of an unconverged instance, and every command run, so a stray click can\'t start them. Go back to change anything. Only the confirm button starts the job.'),
    activity: help('Activity', 'Activity lists every batch job, and each job is one operation you confirmed. Open a job to see each instance\'s outcome and to pause, resume, or cancel it. Execution settings limit how many jobs run at once across the whole broker. A paused job keeps its place under that limit until you resume or cancel it, so a job left paused can hold others back.'),
    task: help('batch job details', 'Pause stops the job before its next instance or node, and anything already in progress finishes first. After a failure, the button reads Continue remaining instances, and it moves on to the instances still waiting without retrying the one that failed. Resume cleanup appears when a command job couldn\'t confirm that the command has ended on every VM. It checks those VMs again and doesn\'t run the command. Cancel stops the job, but an instance whose command might still be running stays reserved until cleanup confirms it has ended.')
  };
  function get(context = {}) {
    const {screen, section, subview, operation} = context;
    if (screen === 'blacksmith' || screen === 'services') {
      if (section === 'certificates' && certificateSources[subview]) return certificateSources[subview];
      if (section === 'manifest' && manifestSources[subview]) return manifestSources[subview];
      if (section === 'testing' && testOperations[subview]) return testOperations[subview];
      if (screen === 'services' && !section) return help('service instances', 'Search by name, ID, service, or plan, then select an instance. Filters narrow the collection; the copy action uses matching instances. Deleted and unavailable instances remain distinct from healthy instances.');
      return (screen === 'blacksmith' ? broker : instance)[section] || null;
    }
    if (screen === 'certificates') return help('fleet certificates', 'Expiry horizons count certificates expiring within each interval. Health findings, freshness, and coverage are separate from expiry. Combine filters to narrow the table; selected certificates can be handed to Batch Operations for review before rotation.');
    if (screen === 'upgrade') {
      const step = ['operation','options','instances','review'][context.step] || context.step || 'operation';
      return batch[(section === 'activity' || context.view === 'activity') ? ((context.taskId || context.task) ? 'task' : 'activity') : step === 'options' ? operation : step] || batch.operation;
    }
    return null;
  }
  // Keyboard shortcuts exist only on Batch Operations; the same text was a disclosure there before.
  function getShortcuts(context = {}) {
    return context.screen === 'upgrade' ? {label: 'Keyboard shortcuts for this view'} : null;
  }
  function shortcutBody(doc) {
    const el = (name, text) => {const node = doc.createElement(name); if (text) node.textContent = text; return node;};
    const kbds = (parts) => parts.map(part => part.k ? el('kbd', part.k) : doc.createTextNode(part));
    const para = (...parts) => {const p = el('p'); p.append(...kbds(parts)); return p;};
    const list = el('dl');
    [['N', 'New Operation (keeps your draft)'], ['A', 'Activity'], ['U / R / C / X', 'Upgrade / Rotate / Converge / Run command options'], ['→ / ←', 'Next / Back']]
      .forEach(([key, action]) => list.append(el('dt', key), el('dd', action)));
    return [
      para('On this screen, hold ', {k: 'Alt / Option'}, ' + ', {k: 'Shift'}, ' and press:'),
      list,
      para({k: 'Tab'}, ' / ', {k: 'Shift Tab'}, ' moves between controls. Arrow keys choose operations, modes, and select options. ', {k: 'Space'}, ' selects a checkbox; ', {k: 'Enter'}, ' activates a focused button.'),
      para('Shortcuts pause in text fields and dialogs. On Review, Next focuses the confirmation button; press Enter to submit.')
    ];
  }
  // Field help explains why to pick one setting over another, beside the control it covers.
  const batchFields = {
    'rotate-mode': help('rotation modes', 'Choose Redeploy unless you have a reason not to. It\'s the normal path, because BOSH rolls the new certificates out with canaries and drains, and its records match the VMs afterwards. Choose Patch in place when time matters more than a clean rollout, such as when a certificate expires before a deploy could finish. Patch is a temporary repair, because BOSH still holds the old certificate, and a recreate or resurrection puts that certificate back until you converge.'),
    'rotate-converge': help('after patching', 'Choose Converge right away when you can accept a second restart now and want the repair made permanent in the same job. Choose Leave unconverged when the jobs should restart only once today, and plan a Converge for a later maintenance window. Until then, the instance is flagged as unconverged, and upgrading it also deploys its latest certificates.'),
    'additional-releases': help('additional releases', 'Use additional releases when the change you need is in a release other than the service\'s own, such as a shared add-on release that several services use. A release only goes to deployments that already use it, so this never adds new software to an instance. The version you choose must already be uploaded to the director.'),
    'command-text': help('the command', 'Write the command so it\'s safe on every node of every selected instance, because it runs on all of them, including nodes whose jobs are stopped. Service packages aren\'t on the search path, so give the full path, such as /var/vcap/packages/rabbitmq/sbin/rabbitmqctl. The command can\'t read input, so a prompt gets end of file instead of an answer. The job keeps the command text for the audit, so leave passwords out of it when you can. A command can be up to 64 kilobytes long, and each node\'s output appears when that node finishes.'),
    'command-user': help('Run as', 'A vcap command can\'t use sudo, su, or any other way to gain privileges, so choose root, without sudo, for privileged work. Choose vcap whenever it can do the job, because a vcap command can only change what vcap owns. Most service maintenance works as vcap, including the service tools under /var/vcap/packages. Root can change anything on the VM, and nothing the broker does can undo or contain what a root command starts. Running as root stays off, and its choice stays greyed out, until an administrator turns on commands.allow_root in the broker configuration, which the Blacksmith kit does with its commands_allow_root parameter.'),
    'command-timeout': help('the timeout', 'Set the timeout a little above the longest you expect the command to take on one node, because a timeout counts as a failure and pauses the job. Each node has its own clock. At the deadline the command gets a stop signal, and ten seconds later it\'s killed. Keep it short for quick checks, such as reading a file or checking a status, and allow more for work that restarts a service or waits for a cluster to settle. A long timeout lets a hung command hold its instance until the deadline. When nodes run one at a time, a slow command can take close to the timeout on every node in turn.'),
    'command-parallel': help('parallel runs', 'Leave parallel off for anything that restarts a service, changes cluster membership, or could take a node down. One node at a time means a bad command stops at its first failure instead of reaching a whole cluster at once. Turn it on for read-only or independent work on deployments with many nodes, such as collecting a value or checking disk space. Deployments still run one at a time, and the limit caps how many nodes run the command at once. When a node fails, the nodes already running finish before the job pauses, so a higher limit lets more nodes get a bad command. For a clustered service, keep the limit at or below the number of nodes the cluster can lose at once. The limit must stay below the director\'s worker count, because each node\'s setup and cleanup take a director worker, and the director needs one left for everything else. With the default of four workers, the most you can set is 3, and the Review step checks the number against your director.'),
    'pause-on-failure': help('pausing after a failure', 'Turn this on when a failure on one instance probably means the rest would fail the same way, such as the first run of a new release or a rotation you haven\'t tried on this service yet. The job lets any deploy already running finish, starts no other instance, and pauses. Continue remaining instances then moves on to the instances still waiting without retrying the one that failed. Leave it off for routine work you\'ll watch from Activity, where one failed instance shouldn\'t hold up the rest. Your choice applies to this job only and never changes the broker\'s settings. Run command jobs always pause after a failure.'),
    'concurrent-jobs': help('concurrent batch jobs', 'This limit decides how many batch jobs run at the same time across the whole broker, and the rest wait their turn. Each job works through its instances one at a time, so the limit is how many instances can be changing at once. Keep it low, such as 1 or 2, when the director is busy or when you want changes to land in a predictable order. Raise it when you have several independent jobs and the director has room for them. A paused job keeps its place, so with a limit of 1, one paused job holds every other job back. When the field is empty, any number of jobs can run, and deploys are still capped by the broker\'s batch connection limit, which is 10 unless an administrator changed it.')
  };
  const getField = name => Object.prototype.hasOwnProperty.call(batchFields, name) ? batchFields[name] : null;
  const infoIcon=blacksmithIcon('info',{size:18});
  // Keeps a fixed tip beside its button and inside the viewport.
  function place(button, tip){
    if(tip.hidden)return;
    const rect=button.getBoundingClientRect(), width=tip.getBoundingClientRect().width;
    tip.style.left=Math.max(8,Math.min(rect.right-width,root.innerWidth-width-8))+'px';
    const height=tip.getBoundingClientRect().height;
    tip.style.top=Math.max(8,rect.bottom+8+height>root.innerHeight?rect.top-height-8:rect.bottom+8)+'px';
  }
  // Field popovers are toggletips. Only one is open at a time, and one set of
  // document listeners serves all of them, so a removed pane leaks nothing.
  // Escape is consumed only when focus is on the open popover or its button,
  // so a dialog or field elsewhere still gets the key.
  let openField=null, fieldSerial=0;
  const fieldDocs=new WeakSet();
  function listenForFields(doc){
    if(fieldDocs.has(doc))return;
    fieldDocs.add(doc);
    doc.addEventListener('keydown',event=>{
      if(event.key!=='Escape'||!openField)return;
      const {button,tip}=openField, inside=button.contains(doc.activeElement)||tip.contains(doc.activeElement);
      openField.hide();
      if(!inside||!button.isConnected)return;
      event.preventDefault();event.stopImmediatePropagation();button.focus();
    },true);
    doc.addEventListener('pointerdown',event=>{
      if(!openField||openField.button.contains(event.target)||openField.tip.contains(event.target))return;
      openField.hide();
    },true);
    const reposition=()=>{if(!openField)return;if(openField.button.isConnected)place(openField.button,openField.tip);else openField.hide();};
    root.addEventListener('resize',reposition);doc.addEventListener('scroll',reposition,true);
  }
  function field(host, name){
    const value=getField(name), doc=root.document;
    if(!host||!doc||!value)return null;
    listenForFields(doc);
    const node=doc.createElement('span'), button=doc.createElement('button'), tip=doc.createElement('div');
    let id=`batch-help-${name}`;
    while(doc.getElementById(id))id=`batch-help-${name}-${++fieldSerial}`;
    node.className='field-guidance';
    button.type='button';button.className='view-guidance-button field-guidance-button';button.dataset.contextHelp='';
    button.setAttribute('aria-label',`About ${value.label}`);button.setAttribute('aria-expanded','false');button.setAttribute('aria-controls',id);
    button.innerHTML=infoIcon;
    tip.id=id;tip.className='view-guidance-tip field-guidance-tip';tip.setAttribute('popover','manual');tip.hidden=true;
    const title=doc.createElement('strong'), status=doc.createElement('div');
    title.textContent=`About ${value.label}`;status.setAttribute('role','status');
    tip.append(title,status);node.append(button,tip);
    // Hover shows the text as plain content. A click moves it into the status
    // region, so a screen reader announces only the popover the user asked for.
    const text=doc.createElement('p');text.textContent=value.body;
    let pinned=false,hover=false,timer;
    const self={button,tip,hide};
    function show(announce){
      if(openField&&openField!==self)openField.hide();
      clearTimeout(timer);openField=self;tip.hidden=false;
      if(tip.showPopover&&!tip.matches(':popover-open'))tip.showPopover();
      button.setAttribute('aria-expanded','true');button.setAttribute('aria-describedby',id);
      if(announce)status.append(text);else if(!text.isConnected)tip.append(text);
      place(button,tip);
    }
    function hide(){
      clearTimeout(timer);pinned=false;
      if(tip.hidePopover&&tip.matches(':popover-open'))tip.hidePopover();
      tip.hidden=true;text.remove();
      button.setAttribute('aria-expanded','false');button.removeAttribute('aria-describedby');
      if(openField===self)openField=null;
    }
    function leave(){hover=false;timer=setTimeout(()=>{if(!hover&&!pinned)hide();},120);}
    button.addEventListener('click',()=>{if(pinned)hide();else{pinned=true;show(true);}});
    button.addEventListener('pointerenter',()=>{hover=true;show();});button.addEventListener('pointerleave',leave);
    tip.addEventListener('pointerenter',()=>{hover=true;clearTimeout(timer);});tip.addEventListener('pointerleave',leave);
    node.addEventListener('focusout',event=>{if(event.relatedTarget&&!node.contains(event.relatedTarget))hide();});
    host.append(node);
    return node;
  }
  let dispose;
  function mount(host, context) {
    dispose?.();dispose=null;
    if (!host || !root.document) return null;
    const value=get(context), keys=getShortcuts(context);if(!value&&!keys)return null;
    const doc=root.document, node=doc.createElement('span'), disposers=[];
    node.className='view-guidance';
    // One popover mechanism serves every header button: hover or focus shows, click pins, Escape and outside clicks dismiss.
    const hiders=[];
    function attach(spec){
      const button=doc.createElement('button'), tip=doc.createElement('div');
      button.type='button';button.className='view-guidance-button';button.dataset[spec.dataset]='';
      button.setAttribute('aria-label',spec.name);if(spec.titled)button.title=spec.name;
      button.setAttribute('aria-expanded','false');button.setAttribute('aria-controls',spec.id);
      button.innerHTML=spec.icon;
      tip.id=spec.id;tip.className='view-guidance-tip';tip.setAttribute('role','tooltip');tip.setAttribute('popover','manual');tip.hidden=true;
      const title=doc.createElement('strong');title.textContent=spec.heading;tip.append(title,...spec.content());
      node.append(button);doc.body.append(tip);
      let pinned=false,hover=false,timer;
      const position=()=>place(button,tip);
      function show(){hiders.forEach(fn=>{if(fn!==hide)fn();});clearTimeout(timer);tip.hidden=false;if(tip.showPopover&&!tip.matches(':popover-open'))tip.showPopover();button.setAttribute('aria-expanded','true');button.setAttribute('aria-describedby',tip.id);position();}
      function hide(){clearTimeout(timer);pinned=false;if(tip.hidePopover&&tip.matches(':popover-open'))tip.hidePopover();tip.hidden=true;button.setAttribute('aria-expanded','false');button.removeAttribute('aria-describedby');}
      hiders.push(hide);
      function leave(){hover=false;timer=setTimeout(()=>{if(!hover&&!pinned&&doc.activeElement!==button)hide();},120);}
      button.addEventListener('pointerenter',()=>{hover=true;show();});button.addEventListener('pointerleave',leave);
      tip.addEventListener('pointerenter',()=>{hover=true;clearTimeout(timer);});tip.addEventListener('pointerleave',leave);
      let restoring=false;
      button.addEventListener('focus',()=>{if(!restoring)show();});button.addEventListener('blur',leave);
      const toggle=()=>{if(pinned)hide();else{pinned=true;show();}};
      button.addEventListener('click',toggle);
      // Handle Enter and Space directly and cancel the native click so a press toggles once.
      const activation=event=>event.key==='Enter'||event.key===' ';
      button.addEventListener('keydown',event=>{if(!activation(event))return;event.preventDefault();toggle();});
      button.addEventListener('keyup',event=>{if(activation(event))event.preventDefault();});
      const escape=event=>{if(event.key==='Escape'&&!tip.hidden){event.preventDefault();event.stopImmediatePropagation();hide();restoring=true;button.focus();restoring=false;}};
      const outside=event=>{if(!node.contains(event.target)&&!tip.contains(event.target))hide();};
      doc.addEventListener('keydown',escape,true);doc.addEventListener('pointerdown',outside,true);root.addEventListener('resize',position);doc.addEventListener('scroll',position,true);
      disposers.push(()=>{hide();tip.remove();doc.removeEventListener('keydown',escape,true);doc.removeEventListener('pointerdown',outside,true);root.removeEventListener('resize',position);doc.removeEventListener('scroll',position,true);});
    }
    if(value)attach({dataset:'contextHelp',id:'view-guidance-tip',name:`About ${value.label}`,heading:`About ${value.label}`,icon:infoIcon,content:()=>{const text=doc.createElement('p');text.textContent=value.body;return [text];}});
    if(keys)attach({titled:true,dataset:'keyboardHelp',id:'keyboard-shortcuts-tip',name:keys.label,heading:'Keyboard shortcuts',icon:blacksmithIcon('keyboard',{size:18}),content:()=>shortcutBody(doc)});
    host.append(node);
    dispose=()=>{disposers.forEach(fn=>fn());node.remove();};
    return node;
  }
  root.BlacksmithViewGuidance = {get,getShortcuts,getField,field,mount};
})(globalThis);
