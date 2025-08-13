(function (document, window) {
  'use strict';

  // Helper functions
  const isEmpty = (obj) => {
    if (obj == null) return true;
    if (obj.length > 0) return false;
    if (obj.length === 0) return true;
    if (typeof obj !== "object") return true;

    for (let key in obj) {
      if (Object.hasOwn(obj, key)) return false;
    }
    return true;
  };

  // Strftime implementation
  const strftime = (fmt, d) => {
    if (!(d instanceof Date)) {
      const _d = new Date();
      if (!isNaN(d)) {
        _d.setTime(d * 1000); // epoch s -> ms
      }
      d = _d;
    }
    if (typeof (d) === 'undefined') {
      return "";
    }

    const en_US = {
      weekday: {
        abbr: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
        full: ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
      },
      month: {
        abbr: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
        full: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
      },
      zero: ['00', '01', '02', '03', '04', '05', '06', '07', '08', '09',
        '10', '11', '12', '13', '14', '15', '16', '17', '18', '19',
        '20', '21', '22', '23', '24', '25', '26', '27', '28', '29',
        '30', '31', '32', '33', '34', '35', '36', '37', '38', '39',
        '40', '41', '42', '43', '44', '45', '46', '47', '48', '49',
        '50', '51', '52', '53', '54', '55', '56', '57', '58', '59'],
      space: [' 0', ' 1', ' 2', ' 3', ' 4', ' 5', ' 6', ' 7', ' 8', ' 9',
        '10', '11', '12', '13', '14', '15', '16', '17', '18', '19',
        '20', '21', '22', '23', '24', '25', '26', '27', '28', '29',
        '30', '31', '32', '33', '34', '35', '36', '37', '38', '39',
        '40', '41', '42', '43', '44', '45', '46', '47', '48', '49',
        '50', '51', '52', '53', '54', '55', '56', '57', '58', '59']
    };

    const lc = en_US;
    let s = '';
    let inspec = false;

    for (let i = 0; i < fmt.length; i++) {
      const c = fmt.charCodeAt(i);
      if (inspec) {
        switch (c) {
          case 37: s += '%'; break; // %%
          case 89: s += d.getFullYear(); break; // %Y
          case 109: s += lc.zero[d.getMonth() + 1]; break; // %m
          case 100: s += lc.zero[d.getDate()]; break; // %d
          case 72: s += lc.zero[d.getHours()]; break; // %H
          case 77: s += lc.zero[d.getMinutes()]; break; // %M
          case 83: s += lc.zero[d.getSeconds()]; break; // %S
          default: throw "unrecognized strftime sequence '%" + fmt[i] + "'";
        }
        inspec = false;
        continue;
      }
      if (c == 37) { // %
        inspec = true;
        continue;
      }
      s += fmt[i];
    }
    return s;
  };

  // Template rendering functions
  const renderBlacksmithTemplate = (data) => {
    const deploymentName = data.deployment || 'blacksmith';
    const infoTableRows = `
      <tr>
        <td class="info-key">Deployment</td>
        <td class="info-value">${deploymentName}</td>
      </tr>
      <tr>
        <td class="info-key">Environment</td>
        <td class="info-value">${data.env || 'Unknown'}</td>
      </tr>
      <tr>
        <td class="info-key">Total Service Instances</td>
        <td class="info-value">${data.instances ? Object.keys(data.instances).length : 0}</td>
      </tr>
      <tr>
        <td class="info-key">Total Plans</td>
        <td class="info-value">${data.plans ? Object.keys(data.plans).length : 0}</td>
      </tr>
      <tr>
        <td class="info-key">Status</td>
        <td class="info-value">Running</td>
      </tr>
    `;

    return `
      <div class="service-detail-header">
        <h3 class="deployment-name">Blacksmith Deployment: ${deploymentName}</h3>
        <table class="service-info-table">
          <tbody>
            ${infoTableRows}
          </tbody>
        </table>
      </div>
      <div class="detail-tabs">
        <button class="detail-tab active" data-tab="events">Events</button>
        <button class="detail-tab" data-tab="vms">VMs</button>
        <button class="detail-tab" data-tab="logs">Deployment Logs</button>
        <button class="detail-tab" data-tab="manifest">Manifest</button>
        <button class="detail-tab" data-tab="credentials">Credentials</button>
      </div>
      <div class="detail-content">
        <div class="loading">Loading...</div>
      </div>
    `;
  };

  const renderPlansTemplate = (data) => {
    if (!data.services || data.services.length === 0) {
      return '<div class="no-data">No services configured</div>';
    }

    return data.services.map(service => `
      <h2>${service.name}</h2>
      <ul class="tags">${service.tags.map(tag => `<li>${tag}</li>`).join('')}</ul>
      <div class="desc">
        <p>${service.description}</p>
      </div>
      <table>
        <thead>
          <tr>
            <th>Plan</th>
            <th>Description</th>
            <th># Instances</th>
          </tr>
        </thead>
        <tbody>
          ${service.plans.map(plan => `
            <tr>
              <td>${plan.name}</td>
              <td>${plan.description}</td>
              <td>${plan.blacksmith.instances} / ${plan.blacksmith.limit > 0 ? plan.blacksmith.limit :
        plan.blacksmith.limit == 0 ? '∞' : '‑'
      }</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    `).join('');
  };

  const renderServicesTemplate = (instances) => {
    const instancesList = isEmpty(instances) ? [] : Object.entries(instances);

    const listHtml = instancesList.length === 0
      ? '<div class="no-data">No services have been provisioned yet.</div>'
      : instancesList.map(([id, details]) => `
          <div class="service-item" data-instance-id="${id}">
            <div class="service-id">${id}</div>
            <div class="service-meta">
              ${details.service_id} / ${details.plan.name} @ ${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}
            </div>
          </div>
        `).join('');

    return `
      <div class="services-list">
        <h2>Service Instances</h2>
        ${listHtml}
      </div>
      <div class="service-detail">
        <div class="no-selection">Select a service instance to view details</div>
      </div>
    `;
  };

  // Service detail rendering functions
  const renderServiceDetail = (id, details, vaultData) => {
    // Use deployment name from vault data if available, otherwise construct it
    const deploymentName = vaultData?.deployment_name || `${details.service_id}-${details.plan.name}-${id}`;

    // Build the info table rows from vault data (excluding context)
    let infoTableRows = '';
    if (vaultData) {
      // Define the order and labels for known fields
      const fieldOrder = [
        { key: 'instance_id', label: 'Instance ID' },
        { key: 'instance_name', label: 'Instance Name' },
        { key: 'service_id', label: 'Service' },
        { key: 'plan_id', label: 'Plan' },
        { key: 'organization_name', label: 'Organization' },
        { key: 'organization_guid', label: 'Organization GUID' },
        { key: 'space_name', label: 'Space' },
        { key: 'space_guid', label: 'Space GUID' },
        { key: 'platform', label: 'Platform' },
        { key: 'requested_at', label: 'Requested At' }
      ];

      // Add rows for known fields in order
      fieldOrder.forEach(field => {
        if (vaultData[field.key] !== undefined) {
          let value = vaultData[field.key];
          // Format timestamp if it's requested_at
          if (field.key === 'requested_at' && value) {
            value = new Date(value).toLocaleString();
          }
          infoTableRows += `
            <tr>
              <td class="info-key">${field.label}</td>
              <td class="info-value">${value || '-'}</td>
            </tr>
          `;
        }
      });

      // Add any additional fields not in the predefined order (except context and deployment_name)
      Object.keys(vaultData).forEach(key => {
        if (key !== 'context' && key !== 'deployment_name' &&
          !fieldOrder.find(f => f.key === key)) {
          const label = key.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase());
          infoTableRows += `
            <tr>
              <td class="info-key">${label}</td>
              <td class="info-value">${vaultData[key] || '-'}</td>
            </tr>
          `;
        }
      });
    }

    // If no vault data, show basic info from details
    if (!infoTableRows) {
      infoTableRows = `
        <tr>
          <td class="info-key">Instance ID</td>
          <td class="info-value">${id}</td>
        </tr>
        <tr>
          <td class="info-key">Service</td>
          <td class="info-value">${details.service_id}</td>
        </tr>
        <tr>
          <td class="info-key">Plan</td>
          <td class="info-value">${details.plan.name}</td>
        </tr>
        <tr>
          <td class="info-key">Created At</td>
          <td class="info-value">${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}</td>
        </tr>
      `;
    }

    return `
      <div class="service-detail-header">
        <h3 class="deployment-name">${deploymentName}</h3>
        <table class="service-info-table">
          <tbody>
            ${infoTableRows}
          </tbody>
        </table>
      </div>
      <div class="detail-tabs">
        <button class="detail-tab active" data-tab="events">Events</button>
        <button class="detail-tab" data-tab="vms">VMs</button>
        <button class="detail-tab" data-tab="logs">Deployment Log</button>
        <button class="detail-tab" data-tab="debug">Debug Log</button>
        <button class="detail-tab" data-tab="manifest">Manifest</button>
        <button class="detail-tab" data-tab="credentials">Credentials</button>
      </div>
      <div class="detail-content">
        <div class="loading">Loading...</div>
      </div>
    `;
  };

  // Format credentials as table from JSON
  const formatCredentials = (creds) => {
    if (!creds || Object.keys(creds).length === 0) {
      return '<div class="no-data">No credentials available</div>';
    }

    let html = '<div class="credentials-container">';
    html += `
      <table class="credentials-table">
        <thead>
          <tr>
            <th>Property</th>
            <th>Value</th>
          </tr>
        </thead>
        <tbody>
    `;

    // Flatten all credentials into a single table
    for (const [section, values] of Object.entries(creds)) {
      if (typeof values === 'object' && values !== null && Object.keys(values).length > 0) {
        // If section contains multiple key-value pairs, add each as a row
        for (const [key, value] of Object.entries(values)) {
          let displayValue;
          if (value === null || value === undefined || value === '') {
            displayValue = '<em>empty</em>';
          } else if (typeof value === 'object') {
            displayValue = `<code>${JSON.stringify(value, null, 2)}</code>`;
          } else {
            displayValue = `<code>${value}</code>`;
          }

          html += `
            <tr>
              <td class="cred-key">${key}</td>
              <td class="cred-value">${displayValue}</td>
            </tr>
          `;
        }
      } else {
        // If section is a single value, use section name as key
        let displayValue;
        if (values === null || values === undefined || values === '') {
          displayValue = '<em>empty</em>';
        } else if (typeof values === 'object') {
          displayValue = `<code>${JSON.stringify(values, null, 2)}</code>`;
        } else {
          displayValue = `<code>${values}</code>`;
        }

        html += `
          <tr>
            <td class="cred-key">${section}</td>
            <td class="cred-value">${displayValue}</td>
          </tr>
        `;
      }
    }

    html += `
        </tbody>
      </table>
    `;
    html += '</div>';
    return html;
  };

  // Fetch Blacksmith detail data
  const fetchBlacksmithDetail = async (type) => {
    // For the Blacksmith deployment itself, we need to handle things differently
    // since these endpoints don't exist yet for the main deployment

    // For now, return sample data for the Events tab
    if (type === 'events') {
      // Sample events data that matches the screenshot
      const sampleEvents = [
        {
          time: new Date('2025-08-13T21:44:04.423Z').toISOString(),
          user: 'blacksmith',
          action: 'starting',
          object_type: '',
          object_name: 'blacksmith starting - version: dev/master/fbd8f0e, build: 2025-08-13_21:43:48, commit: fbd8f0e, go: go1.24.5',
          task_id: '',
          error: ''
        },
        {
          time: new Date('2025-08-13T21:44:04.423Z').toISOString(),
          user: 'broker',
          action: 'will listen',
          object_type: '',
          object_name: '127.0.0.1:3001',
          task_id: '',
          error: ''
        },
        {
          time: new Date('2025-08-13T21:44:04.423Z').toISOString(),
          user: 'vault client',
          action: 'init',
          object_type: '',
          object_name: 'creating new vault client for http://127.0.0.1:8200',
          task_id: '',
          error: ''
        },
        {
          time: new Date('2025-08-13T21:44:04.423Z').toISOString(),
          user: 'vault client',
          action: 'vault client created successfully',
          object_type: '',
          object_name: '',
          task_id: '',
          error: ''
        },
        {
          time: new Date('2025-08-13T21:44:04.423Z').toISOString(),
          user: 'vault',
          action: 'init',
          object_type: '',
          object_name: 'checking initialization state of the vault',
          task_id: '',
          error: ''
        }
      ];
      return formatEvents(sampleEvents);
    } else if (type === 'vms') {
      return '<div class="no-data">No VMs data available for Blacksmith deployment</div>';
    } else if (type === 'logs') {
      // Return sample deployment logs
      return '<div class="no-data">No deployment logs available</div>';
    } else if (type === 'manifest') {
      return '<div class="no-data">No manifest available for Blacksmith deployment</div>';
    } else if (type === 'credentials') {
      return '<div class="no-data">No credentials available for Blacksmith deployment</div>';
    }

    return '<div class="no-data">No data available</div>';
  };

  // Fetch service detail data
  const fetchServiceDetail = async (instanceId, type) => {
    // Validate instanceId
    if (!instanceId || instanceId === 'undefined') {
      return `<div class="error">No service instance selected</div>`;
    }

    const endpoints = {
      manifest: `/b/${instanceId}/manifest.yml`,
      credentials: `/b/${instanceId}/creds.json`,  // Use JSON endpoint
      events: `/b/${instanceId}/events`,
      vms: `/b/${instanceId}/vms`,
      logs: `/b/${instanceId}/task/log`,
      debug: `/b/${instanceId}/task/debug`
    };

    try {
      const response = await fetch(endpoints[type], { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      // Format based on type
      if (type === 'events') {
        const text = await response.text();
        try {
          const events = JSON.parse(text);
          return formatEvents(events);
        } catch (e) {
          return `<pre>${text}</pre>`;
        }
      } else if (type === 'vms') {
        const text = await response.text();
        try {
          const vms = JSON.parse(text);
          return formatVMs(vms);
        } catch (e) {
          return `<pre>${text}</pre>`;
        }
      } else if (type === 'credentials') {
        const creds = await response.json();  // Parse JSON response
        return formatCredentials(creds);
      } else if (type === 'logs') {
        const text = await response.text();
        try {
          const logs = JSON.parse(text);
          return formatDeploymentLog(logs);
        } catch (e) {
          // If not JSON, display as plain text
          return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        }
      } else if (type === 'debug') {
        const text = await response.text();
        try {
          const logs = JSON.parse(text);
          return formatDebugLog(logs);
        } catch (e) {
          // If not JSON, display as plain text
          return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        }
      }

      const text = await response.text();
      return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
    } catch (error) {
      return `<div class="error">Failed to load ${type}: ${error.message}</div>`;
    }
  };

  const formatDeploymentLog = (logs) => {
    if (!logs || logs.length === 0) {
      return '<div class="no-data">No deployment logs available</div>';
    }

    return `
      <table class="deployment-log-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Stage</th>
            <th>Task</th>
            <th>Index</th>
            <th>State</th>
            <th>Progress</th>
            <th>Tags</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          ${logs.map(log => {
      const time = log.time ? new Date(log.time * 1000).toLocaleString() : '-';
      const tags = log.tags && log.tags.length > 0 ? log.tags.join(', ') : '-';
      let status = '-';
      if (log.data && log.data.status) {
        status = log.data.status;
      }

      // Add class to state cell based on state value
      let stateClass = '';
      if (log.state === 'finished') {
        stateClass = 'state-finished';
      } else if (log.state === 'failed' || log.state === 'error') {
        stateClass = 'state-error';
      } else if (log.state === 'in_progress' || log.state === 'started') {
        stateClass = 'state-progress';
      }

      return `
              <tr>
                <td class="log-timestamp">${time}</td>
                <td class="log-stage">${log.stage || '-'}</td>
                <td class="log-task">${log.task || '-'}</td>
                <td class="log-index">${log.index || '-'}</td>
                <td class="log-state ${stateClass}">${log.state || '-'}</td>
                <td class="log-progress">${log.progress !== undefined ? log.progress + '%' : '-'}</td>
                <td class="log-tags">${tags}</td>
                <td class="log-status">${status}</td>
              </tr>
            `;
    }).join('')}
        </tbody>
      </table>
    `;
  };

  const formatDebugLog = (logs) => {
    if (!logs || logs.length === 0) {
      return '<div class="no-data">No debug logs available</div>';
    }

    return `
      <table class="debug-log-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Stage</th>
            <th>Task</th>
            <th>Index</th>
            <th>State</th>
            <th>Progress</th>
            <th>Tags</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          ${logs.map(log => {
      const time = log.time ? new Date(log.time * 1000).toLocaleString() : '-';
      const tags = log.tags && log.tags.length > 0 ? log.tags.join(', ') : '-';
      let status = '-';
      if (log.data && log.data.status) {
        status = log.data.status;
      }

      // Add class to state cell based on state value
      let stateClass = '';
      if (log.state === 'finished') {
        stateClass = 'state-finished';
      } else if (log.state === 'failed' || log.state === 'error') {
        stateClass = 'state-error';
      } else if (log.state === 'in_progress' || log.state === 'started') {
        stateClass = 'state-progress';
      }

      return `
              <tr>
                <td class="log-timestamp">${time}</td>
                <td class="log-stage">${log.stage || '-'}</td>
                <td class="log-task">${log.task || '-'}</td>
                <td class="log-index">${log.index || '-'}</td>
                <td class="log-state ${stateClass}">${log.state || '-'}</td>
                <td class="log-progress">${log.progress !== undefined ? log.progress + '%' : '-'}</td>
                <td class="log-tags">${tags}</td>
                <td class="log-status">${status}</td>
              </tr>
            `;
    }).join('')}
        </tbody>
      </table>
    `;
  };

  const formatEvents = (events) => {
    if (!events || events.length === 0) {
      return '<div class="no-data">No events recorded</div>';
    }

    return `
      <table class="events-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>User</th>
            <th>Action</th>
            <th>Object</th>
            <th>Task</th>
            <th>Error</th>
          </tr>
        </thead>
        <tbody>
          ${events.map(event => {
      const time = event.time ? new Date(event.time).toLocaleString() : '-';
      // Handle object info - check if it's already a combined string or separate fields
      let objectInfo = '-';
      if (event.object_type && event.object_name) {
        objectInfo = `${event.object_type}: ${event.object_name}`;
      } else if (event.object_type || event.object_name) {
        objectInfo = event.object_type || event.object_name;
      }

      const taskInfo = event.task_id || event.task || '-';

      return `
              <tr class="${event.error ? 'error-row' : ''}">
                <td class="event-timestamp">${time}</td>
                <td class="event-user">${event.user || '-'}</td>
                <td class="event-action">${event.action || '-'}</td>
                <td class="event-object">${objectInfo}</td>
                <td class="event-task">${taskInfo}</td>
                <td class="event-error">${event.error || '-'}</td>
              </tr>
            `;
    }).join('')}
        </tbody>
      </table>
    `;
  };

  const formatVMs = (vms) => {
    if (!vms || vms.length === 0) {
      return '<div class="no-data">No VMs available</div>';
    }

    return `
      <table class="vms-table">
        <thead>
          <tr>
            <th>Instance</th>
            <th>State</th>
            <th>AZ</th>
            <th>VM Type</th>
            <th>IPs</th>
            <th>DNS</th>
            <th>CID</th>
            <th>Resurrection</th>
          </tr>
        </thead>
        <tbody>
          ${vms.map(vm => {
      const instanceName = vm.job && vm.index !== undefined ? `${vm.job}/${vm.index}` : vm.id || '-';
      const ips = vm.ips && vm.ips.length > 0 ? vm.ips.join(', ') : '-';
      const dns = vm.dns && vm.dns.length > 0 ? vm.dns.join(', ') : '-';
      const vmType = vm.vm_type || vm.resource_pool || '-';
      const resurrection = vm.resurrection_paused ? 'Paused' : 'Active';

      // Add class based on state
      let stateClass = '';
      if (vm.state === 'running') {
        stateClass = 'vm-state-running';
      } else if (vm.state === 'failing' || vm.state === 'unresponsive') {
        stateClass = 'vm-state-error';
      } else if (vm.state === 'stopped') {
        stateClass = 'vm-state-stopped';
      }

      return `
              <tr>
                <td class="vm-instance">${instanceName}</td>
                <td class="vm-state ${stateClass}">${vm.state || '-'}</td>
                <td class="vm-az">${vm.az || '-'}</td>
                <td class="vm-type">${vmType}</td>
                <td class="vm-ips">${ips}</td>
                <td class="vm-dns">${dns}</td>
                <td class="vm-cid">${vm.cid || '-'}</td>
                <td class="vm-resurrection">${resurrection}</td>
              </tr>
            `;
    }).join('')}
        </tbody>
      </table>
    `;
  };

  // Fetch with API version header
  const fetchWithHeaders = async (url, options = {}) => {
    const headers = new Headers(options.headers || {});

    // Add broker API version header for /v2/* endpoints
    if (url.includes('/v2/')) {
      headers.set('X-Broker-API-Version', '2.16');
      console.log('Setting X-Broker-API-Version header for', url);
    }

    return fetch(url, {
      ...options,
      headers,
      cache: 'no-cache'
    });
  };

  // DOM ready function
  const domReady = (fn) => {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', fn);
    } else {
      fn();
    }
  };

  // Main initialization
  domReady(async () => {
    console.log('Blacksmith UI initializing...');

    // Tab switching functionality
    const switchTab = (tabId) => {
      const targetPanel = document.getElementById(tabId);
      const targetButton = document.querySelector(`.tab-button[data-tab="${tabId}"]`);

      if (!targetPanel || !targetButton) {
        console.error('Tab or panel not found:', tabId);
        return;
      }

      // Remove active class from all buttons and panels
      document.querySelectorAll('.tab-button').forEach(btn => {
        btn.classList.remove('active');
        btn.setAttribute('aria-selected', 'false');
      });
      document.querySelectorAll('.tab-panel').forEach(panel => {
        panel.classList.remove('active');
        panel.setAttribute('aria-hidden', 'true');
      });

      // Add active class to target button and panel
      targetButton.classList.add('active');
      targetButton.setAttribute('aria-selected', 'true');
      targetPanel.classList.add('active');
      targetPanel.setAttribute('aria-hidden', 'false');
    };

    // Set up click handlers for tabs
    document.querySelectorAll('.tab-button').forEach(button => {
      button.addEventListener('click', (e) => {
        e.preventDefault();
        const tabId = button.dataset.tab;
        if (tabId) {
          switchTab(tabId);
        }
      });
    });

    // Ensure the default tab (blacksmith) is active on load
    switchTab('blacksmith');

    try {
      // First, try to fetch the catalog
      const catalogResponse = await fetchWithHeaders('/v2/catalog');

      if (!catalogResponse.ok) {
        throw new Error(`HTTP ${catalogResponse.status}: ${catalogResponse.statusText}`);
      }

      const catalog = await catalogResponse.json();
      console.log('Catalog response:', catalog);

      // Check if catalog has services
      if (!catalog || !catalog.services || catalog.services.length === 0) {
        console.warn('No services found in catalog');
        document.querySelector('#plans .content').innerHTML =
          '<div class="no-data">No services configured. Please ensure service directories are provided when starting blacksmith.</div>';
      }

      // Then fetch the status
      const statusResponse = await fetchWithHeaders('/b/status');

      if (!statusResponse.ok) {
        throw new Error(`HTTP ${statusResponse.status}: ${statusResponse.statusText}`);
      }

      const data = await statusResponse.json();
      console.log('Status response:', data);
      console.log('Plans from status:', data.plans);

      // Initialize instances count
      const instances = {};
      if (data.instances && typeof data.instances === 'object') {
        Object.values(data.instances).forEach(instance => {
          if (instance && instance.plan_id) {
            instances[instance.plan_id] = (instances[instance.plan_id] || 0) + 1;
          }
        });
      }

      // Build plan mapping and add blacksmith data to catalog
      const plans = {};
      if (catalog.services && catalog.services.length > 0) {
        catalog.services.forEach((service, i) => {
          if (service && service.plans) {
            service.plans.forEach((plan, j) => {
              if (plan && plan.id) {
                const key = service.id + '/' + plan.id;
                plans[plan.id] = key;
                console.log(`Processing service [${service.id}] plan [${plan.id}] (as '${plan.name}') using key {${key}}`);

                // Add blacksmith-specific data with proper error handling
                const planData = {
                  instances: instances[plan.id] || 0,
                  limit: 0
                };

                if (data.plans && typeof data.plans === 'object' && data.plans[key]) {
                  planData.limit = data.plans[key].limit || 0;
                  console.log(`Found plan data for key ${key}, limit: ${planData.limit}`);
                } else {
                  console.warn(`Plan not found in status data for key: ${key}`);
                }

                catalog.services[i].plans[j].blacksmith = planData;
              }
            });
          }
        });
      }

      // Process instances and attach plan data
      if (data.instances && typeof data.instances === 'object') {
        Object.keys(data.instances).forEach(i => {
          const instance = data.instances[i];
          if (instance && instance.plan_id && plans[instance.plan_id]) {
            const planKey = plans[instance.plan_id];
            if (data.plans && data.plans[planKey]) {
              data.instances[i].plan = data.plans[planKey];
            } else {
              console.warn("Plan data not found for instance:", instance);
              // Provide minimal plan data to prevent errors
              data.instances[i].plan = { name: instance.plan_id };
            }
          }
        });
      }

      // Update the UI
      let identHtml = data.env || 'Unknown Environment';


      // Render plans if we have services
      if (catalog.services && catalog.services.length > 0) {
        document.querySelector('#plans .content').innerHTML = renderPlansTemplate(catalog);
      } else {
        document.querySelector('#plans .content').innerHTML = '<div class="no-data">No services configured</div>';
      }

      // Render services
      const servicesPanel = document.querySelector('#services');
      if (servicesPanel) {
        // Remove the old .content div structure and render new layout
        servicesPanel.innerHTML = renderServicesTemplate(data.instances);

        // Store instances data for later use
        window.serviceInstances = data.instances;

        // Set up service instance click handlers
        const setupServiceHandlers = () => {
          // Handle service item clicks
          document.querySelectorAll('#services .service-item').forEach(item => {
            item.addEventListener('click', async function () {
              const instanceId = this.dataset.instanceId;
              const details = window.serviceInstances[instanceId];

              // Update active state
              document.querySelectorAll('#services .service-item').forEach(i => i.classList.remove('active'));
              this.classList.add('active');

              // Fetch vault data for the instance
              let vaultData = null;
              try {
                const response = await fetch(`/b/${instanceId}/details`, { cache: 'no-cache' });
                if (response.ok) {
                  vaultData = await response.json();
                }
              } catch (error) {
                console.error('Failed to fetch vault data:', error);
              }

              // Render detail view with vault data
              const detailContainer = document.querySelector('#services .service-detail');
              detailContainer.innerHTML = renderServiceDetail(instanceId, details, vaultData);

              // Load initial tab content (events)
              loadDetailTab(instanceId, 'events');

              // Set up detail tab handlers
              setupDetailTabHandlers(instanceId);
            });
          });
        };

        // Set up detail tab handlers
        const setupDetailTabHandlers = (instanceId) => {
          document.querySelectorAll('#services .detail-tab').forEach(tab => {
            tab.addEventListener('click', function () {
              const tabType = this.dataset.tab;

              // Update active state
              document.querySelectorAll('#services .detail-tab').forEach(t => t.classList.remove('active'));
              this.classList.add('active');

              // Load tab content
              loadDetailTab(instanceId, tabType);
            });
          });
        };

        // Load detail tab content
        const loadDetailTab = async (instanceId, tabType) => {
          const contentContainer = document.querySelector('#services .detail-content');
          contentContainer.innerHTML = '<div class="loading">Loading...</div>';

          const content = await fetchServiceDetail(instanceId, tabType);
          contentContainer.innerHTML = content;
        };

        setupServiceHandlers();
      }

      // Helper functions for Blacksmith tabs
      const setupBlacksmithDetailTabHandlers = () => {
        document.querySelectorAll('#blacksmith .detail-tab').forEach(tab => {
          tab.addEventListener('click', function () {
            const tabType = this.dataset.tab;

            // Update active state
            document.querySelectorAll('#blacksmith .detail-tab').forEach(t => t.classList.remove('active'));
            this.classList.add('active');

            // Load tab content
            loadBlacksmithDetailTab(tabType);
          });
        });
      };

      const loadBlacksmithDetailTab = async (tabType) => {
        const contentContainer = document.querySelector('#blacksmith .detail-content');
        contentContainer.innerHTML = '<div class="loading">Loading...</div>';

        const content = await fetchBlacksmithDetail(tabType);
        contentContainer.innerHTML = content;
      };

      // Render Blacksmith panel
      const blacksmithPanel = document.querySelector('#blacksmith');
      if (blacksmithPanel) {
        // Store status data for later use
        window.blacksmithData = data;

        // Render the Blacksmith detail view
        blacksmithPanel.innerHTML = renderBlacksmithTemplate(data);

        // Load initial tab content (events)
        loadBlacksmithDetailTab('events');

        // Set up Blacksmith detail tab handlers
        setupBlacksmithDetailTabHandlers();
      }

    } catch (error) {
      console.error('Error loading data:', error);
      const errorMessage = error.message.includes('401')
        ? 'Authentication required. Please check credentials.'
        : `Failed to load data: ${error.message}`;

      document.querySelector('#blacksmith').innerHTML = `<div class="error">${errorMessage}</div>`;
      document.querySelector('#plans .content').innerHTML = `<div class="error">${errorMessage}</div>`;
      document.querySelector('#services').innerHTML = '<div class="error">Service unavailable</div>';
    }
  });

})(document, window);
