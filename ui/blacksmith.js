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
              <div class="service-plan">Plan: ${details.service_id}/${details.plan.name}</div>
              <div class="service-created">Created: ${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}</div>
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
  const renderServiceDetail = (id, details) => {
    return `
      <div class="service-detail-header">
        <h2>Instance: ${id}</h2>
        <div class="service-detail-info">
          <div><strong>Service:</strong> ${details.service_id}</div>
          <div><strong>Plan:</strong> ${details.plan.name}</div>
          <div><strong>Created:</strong> ${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}</div>
        </div>
      </div>
      <div class="detail-tabs">
        <button class="detail-tab active" data-tab="events">Events</button>
        <button class="detail-tab" data-tab="manifest">Manifest</button>
        <button class="detail-tab" data-tab="credentials">Credentials</button>
        <button class="detail-tab" data-tab="logs">Deployment Log</button>
      </div>
      <div class="detail-content">
        <div class="loading">Loading manifest...</div>
      </div>
    `;
  };

  // Fetch service detail data
  const fetchServiceDetail = async (instanceId, type) => {
    const endpoints = {
      manifest: `/b/${instanceId}/manifest.yml`,
      credentials: `/b/${instanceId}/creds.yml`,
      events: `/b/${instanceId}/events`,
      logs: `/b/${instanceId}/task.log`
    };

    try {
      const response = await fetch(endpoints[type], { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();

      // Format based on type
      if (type === 'events') {
        try {
          const events = JSON.parse(text);
          return formatEvents(events);
        } catch (e) {
          return `<pre>${text}</pre>`;
        }
      }

      return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
    } catch (error) {
      return `<div class="error">Failed to load ${type}: ${error.message}</div>`;
    }
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
            const objectInfo = event.object_type && event.object_name ? 
              `${event.object_type}: ${event.object_name}` : 
              (event.object_type || event.object_name || '-');
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
      console.log('Switching to tab:', tabId);

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

    // Ensure the default tab (plans) is active on load
    switchTab('plans');

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
      if (data.version) {
        identHtml += ` <span style="font-size: 0.8em; color: #888;">| v${data.version}`;
        if (data.git_commit && data.git_commit !== 'unknown') {
          identHtml += ` (${data.git_commit.substring(0, 7)})`;
        }
        identHtml += '</span>';
      }
      document.getElementById('ident').innerHTML = identHtml;

      // Update version info in footer
      if (data.version) {
        let versionText = `Blacksmith v${data.version}`;
        if (data.build_time && data.build_time !== 'unknown') {
          versionText += ` | Built: ${data.build_time}`;
        }
        if (data.git_commit && data.git_commit !== 'unknown') {
          versionText += ` | Commit: ${data.git_commit.substring(0, 7)}`;
        }
        document.getElementById('version-info').innerHTML = versionText;
      }

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
            item.addEventListener('click', function () {
              const instanceId = this.dataset.instanceId;
              const details = window.serviceInstances[instanceId];

              // Update active state
              document.querySelectorAll('#services .service-item').forEach(i => i.classList.remove('active'));
              this.classList.add('active');

              // Render detail view
              const detailContainer = document.querySelector('#services .service-detail');
              detailContainer.innerHTML = renderServiceDetail(instanceId, details);

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

      // Render log
      if (data.log) {
        document.querySelector('#log code').innerHTML = data.log
          .replace(/</g, '&lt;')
          .replace(/>/g, '&gt;')
          .replace(/^([\d-]+\s\S+)/mg, '<span class="d">$1</span>')
          .replace(/\[([^\]]+)\]/g, '[<span class="i">$1</span>]')
          .replace(/ (ERROR|INFO|DEBUG) /g, ' <span class="l $1">$1</span> ');
      } else {
        document.querySelector('#log code').innerHTML = 'No log entries available';
      }

    } catch (error) {
      console.error('Error loading data:', error);
      const errorMessage = error.message.includes('401')
        ? 'Authentication required. Please check credentials.'
        : `Failed to load data: ${error.message}`;

      document.querySelector('#plans .content').innerHTML = `<div class="error">${errorMessage}</div>`;
      document.querySelector('#services .content').innerHTML = '<div class="error">Service unavailable</div>';
      document.querySelector('#log code').innerHTML = 'Service unavailable';
    }
  });

})(document, window);
