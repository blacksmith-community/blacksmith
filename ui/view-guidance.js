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
    config: help('Broker Settings', 'Runtime settings can be changed using their Apply buttons. Each field explains its scope and units. Current Configuration is read-only. A failed save leaves your entered value available to correct and retry.'),
    manifest: shared.manifest
  };
  const instance = {
    details: help('Summary', 'Inspect the selected service instance and its reported state. All instances returns to the collection. Service and plan identify its offering; the instance ID distinguishes instances with the same display name.'),
    vms: shared.vms,
    events: shared.events,
    'instance-logs': shared.logs,
    certificates: shared.certificates,
    credentials: help('credentials', 'Connection fields and certificate material belong to this instance. Copy controls copy the named field. Certificate blocks preserve their original line breaks. Treat passwords and private keys as sensitive when copying or sharing them.'),
    testing: help('Testing', 'Choose an operation before entering its inputs. Connection checks inspect connectivity; commands that write, delete, publish, consume, or administer a service may change its data or behavior. Review the selected operation and target before running it.'),
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
    info: help('INFO', 'Read server information from the selected service. Results describe the response at the time the operation ran.'),
    get: help('GET', 'Read the value for a key. Check the database and key before running the request.'),
    keys: help('KEYS', 'Find keys matching the supplied pattern. A broad pattern may inspect many keys; narrow the pattern on large databases.'),
    set: help('SET', 'Write a value to a key. This can replace existing data; check the database, key, and value before running it.'),
    delete: help('DELETE', 'Delete the selected key. Check the target database and key before confirming the operation.'),
    command: help('COMMAND', 'Run a command against the service. Commands may modify data or service behavior; inspect the complete command and target before execution.'),
    flush: help('FLUSH', 'This operation can delete many keys. Review the database or server scope and the confirmation carefully before executing it.'),
    publish: help('PUBLISH', 'Publish a message to the selected exchange and routing key. Check the target and payload before sending.'),
    consume: help('CONSUME', 'Retrieve messages from the selected queue. Acknowledgement options can remove messages; review those options before running the request.'),
    queues: help('QUEUE INFO', 'Read queue information for the selected service. Counts describe the response at the time the request ran.'),
    'queue-ops': help('QUEUE OPS', 'Queue operations may declare, purge, or delete queues. Inspect the chosen action, queue, and options before executing it.'),
    management: help('MANAGEMENT', 'Management requests can inspect or change RabbitMQ configuration. Check the method, path, and payload before execution.'),
    rabbitmqctl: help('RABBITMQCTL', 'Administrative commands can change RabbitMQ behavior. Review the complete command and target before execution.'),
    plugins: help('PLUGINS', 'Plugin operations can change available RabbitMQ features. Review the action and plugin names before applying changes.')
  };
  const batch = {
    operation: help('batch operations', 'Choose Upgrade, Rotate certificates, or Converge, then review its options and select instances. Moving through the steps prepares a draft; only final confirmation starts the operation.'),
    upgrade: help('upgrade options', 'Deploy version is the target to apply. Minimum current version only filters eligible instances; it does not change the target. Advanced releases lets you choose additional release versions before applying them to the draft.'),
    rotate: help('rotation options', 'Redeploy uses BOSH to roll out certificates. Patch in place updates supported instances without that redeploy. Leaving an instance unconverged means BOSH deployment state still needs reconciliation; it does not by itself mean the certificate is invalid.'),
    converge: help('convergence', 'Convergence reconciles deployment state for instances left unconverged. Review the eligible instances and deployment impact before confirmation; this is a separate operation from choosing replacement certificates.'),
    instances: help('instance selection', 'Selections are retained when filtering or changing pages. Check the selected count, including selections hidden by filters. Ineligible instances explain why they cannot be included.'),
    review: help('review and confirmation', 'Check the requested changes, instance scope, and deployment impact. Go back to adjust the draft. Job name helps you find it in Activity; reason records why the operation is needed. Final confirmation creates a task.'),
    activity: help('Activity', 'Open a batch job to inspect progress and per-instance outcomes. BOSH Task opens a related director task when one exists. Execution settings apply to batch jobs, not the deployment rollout policy inside one instance.'),
    task: help('batch task details', 'Overall progress summarizes the job; inspect each instance for its outcome and evidence. Batch job IDs and BOSH task IDs identify different tasks. Close returns to Activity; refreshing should not repeat the operation.')
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
  let dispose;
  function mount(host, context) {
    dispose?.();dispose=null;
    if (!host || !root.document) return null;
    const value=get(context);if(!value)return null;
    const doc=root.document, node=doc.createElement('span'), button=doc.createElement('button'), tip=doc.createElement('div');
    node.className='view-guidance';button.type='button';button.className='view-guidance-button';
    button.dataset.contextHelp='';button.setAttribute('aria-label',`About ${value.label}`);
    button.setAttribute('aria-expanded','false');button.setAttribute('aria-controls','view-guidance-tip');
    button.innerHTML='<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7v1"/></svg>';
    tip.id='view-guidance-tip';tip.className='view-guidance-tip';tip.setAttribute('role','tooltip');tip.setAttribute('popover','manual');tip.hidden=true;
    const title=doc.createElement('strong'), text=doc.createElement('p');title.textContent=`About ${value.label}`;text.textContent=value.body;tip.append(title,text);
    node.append(button);host.append(node);doc.body.append(tip);
    let pinned=false,hover=false,timer;
    function position(){
      if(tip.hidden)return;
      const rect=button.getBoundingClientRect(), width=tip.getBoundingClientRect().width;
      tip.style.left=Math.max(8,Math.min(rect.right-width,root.innerWidth-width-8))+'px';
      const height=tip.getBoundingClientRect().height;
      tip.style.top=Math.max(8,rect.bottom+8+height>root.innerHeight?rect.top-height-8:rect.bottom+8)+'px';
    }
    function show(){clearTimeout(timer);tip.hidden=false;if(tip.showPopover&&!tip.matches(':popover-open'))tip.showPopover();button.setAttribute('aria-expanded','true');button.setAttribute('aria-describedby',tip.id);position();}
    function hide(){clearTimeout(timer);pinned=false;if(tip.hidePopover&&tip.matches(':popover-open'))tip.hidePopover();tip.hidden=true;button.setAttribute('aria-expanded','false');button.removeAttribute('aria-describedby');}
    function leave(){hover=false;timer=setTimeout(()=>{if(!hover&&!pinned&&doc.activeElement!==button)hide();},120);}
    button.addEventListener('pointerenter',()=>{hover=true;show();});button.addEventListener('pointerleave',leave);
    tip.addEventListener('pointerenter',()=>{hover=true;clearTimeout(timer);});tip.addEventListener('pointerleave',leave);
    button.addEventListener('focus',show);button.addEventListener('blur',leave);
    button.addEventListener('click',()=>{if(pinned)hide();else{pinned=true;show();}});
    const escape=event=>{if(event.key==='Escape'&&!tip.hidden){event.preventDefault();event.stopImmediatePropagation();hide();}};
    const outside=event=>{if(!node.contains(event.target)&&!tip.contains(event.target))hide();};
    doc.addEventListener('keydown',escape,true);doc.addEventListener('pointerdown',outside,true);root.addEventListener('resize',position);doc.addEventListener('scroll',position,true);
    dispose=()=>{hide();node.remove();tip.remove();doc.removeEventListener('keydown',escape,true);doc.removeEventListener('pointerdown',outside,true);root.removeEventListener('resize',position);doc.removeEventListener('scroll',position,true);};
    return node;
  }
  root.BlacksmithViewGuidance = {get,mount};
})(globalThis);
