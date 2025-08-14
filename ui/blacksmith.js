(function (document, window) {
  'use strict';

  // Theme Management
  const initTheme = () => {
    // Check for saved theme preference or default to system preference
    const savedTheme = localStorage.getItem('theme');
    const systemPrefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;

    // Set initial theme
    const theme = savedTheme || (systemPrefersDark ? 'dark' : 'light');
    setTheme(theme);

    // Listen for system theme changes
    if (window.matchMedia) {
      window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
        if (!localStorage.getItem('theme')) {
          setTheme(e.matches ? 'dark' : 'light');
        }
      });
    }
  };

  const setTheme = (theme) => {
    if (theme === 'dark') {
      document.documentElement.setAttribute('data-theme', 'dark');
      updateThemeToggle('light');
    } else {
      document.documentElement.removeAttribute('data-theme');
      updateThemeToggle('dark');
    }
  };

  const updateThemeToggle = (nextTheme) => {
    const themeText = document.getElementById('themeText');
    const sunIcon = document.querySelector('.sun-icon');
    const moonIcon = document.querySelector('.moon-icon');

    if (themeText) {
      themeText.textContent = nextTheme === 'dark' ? 'Dark' : 'Light';
    }

    if (sunIcon && moonIcon) {
      if (nextTheme === 'dark') {
        // Currently in light mode, show moon icon for switching to dark
        sunIcon.style.display = 'none';
        moonIcon.style.display = 'block';
      } else {
        // Currently in dark mode, show sun icon for switching to light
        sunIcon.style.display = 'block';
        moonIcon.style.display = 'none';
      }
    }
  };

  const toggleTheme = () => {
    const currentTheme = document.documentElement.getAttribute('data-theme');
    const newTheme = currentTheme === 'dark' ? 'light' : 'dark';

    // Save preference
    localStorage.setItem('theme', newTheme);
    setTheme(newTheme);
  };

  // Initialize theme on page load
  document.addEventListener('DOMContentLoaded', () => {
    initTheme();

    // Add click handler to theme toggle button
    const themeToggle = document.getElementById('themeToggle');
    if (themeToggle) {
      themeToggle.addEventListener('click', toggleTheme);
    }
  });

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

  // Copy to clipboard helper
  const copyToClipboard = async (text, button) => {
    try {
      await navigator.clipboard.writeText(text);
      // Visual feedback
      const originalTitle = button.title;
      button.classList.add('copied');
      button.title = 'Copied!';
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
      }, 2000);
    } catch (err) {
      console.error('Failed to copy:', err);
      // Fallback for older browsers
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      try {
        document.execCommand('copy');
        button.classList.add('copied');
        setTimeout(() => button.classList.remove('copied'), 2000);
      } catch (err) {
        console.error('Fallback copy failed:', err);
      }
      document.body.removeChild(textarea);
    }
  };

  // Create copy button element
  const createCopyButton = (text, className = 'copy-btn') => {
    const button = document.createElement('button');
    button.className = className;
    button.title = 'Copy to clipboard';
    button.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>';
    button.onclick = (e) => {
      e.preventDefault();
      e.stopPropagation();
      copyToClipboard(text, button);
    };
    return button;
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
    const environment = data.env || 'Unknown';
    const totalInstances = data.instances ? Object.keys(data.instances).length : 0;
    const totalPlans = data.plans ? Object.keys(data.plans).length : 0;
    const status = 'Running';

    // Store the details table content for the Details tab
    window.blacksmithDetailsContent = `
      <table class="service-info-table">
        <tbody>
          <tr>
            <td class="info-key">Deployment</td>
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${deploymentName}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${deploymentName}</span>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key">Environment</td>
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${environment}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${environment}</span>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key">Total Service Instances</td>
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${totalInstances}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${totalInstances}</span>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key">Total Plans</td>
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${totalPlans}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${totalPlans}</span>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key">Status</td>
            <td class="info-value">
              <span>${status}</span>
            </td>
          </tr>
        </tbody>
      </table>
    `;

    return `
      <div class="service-detail-header">
      </div>
      <div class="detail-tabs">
        <button class="detail-tab active" data-tab="details">Details</button>
        <button class="detail-tab" data-tab="events">Events</button>
        <button class="detail-tab" data-tab="blacksmith-logs">Logs</button>
        <button class="detail-tab" data-tab="vms">VMs</button>
        <button class="detail-tab" data-tab="logs">Deployment Logs</button>
        <button class="detail-tab" data-tab="debug">Debug Log</button>
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

    // Build the plans list for the left column
    const plansList = [];
    data.services.forEach(service => {
      service.plans.forEach(plan => {
        plansList.push({
          service: service,
          plan: plan,
          id: `${service.name}-${plan.name}`
        });
      });
    });

    const listHtml = plansList.map(item => `
      <div class="plan-item" data-plan-id="${item.id}">
        <div class="plan-name">${item.service.name} / ${item.plan.name}</div>
        <div class="plan-meta">
          Instances: ${item.plan.blacksmith.instances} / ${item.plan.blacksmith.limit > 0 ? item.plan.blacksmith.limit : item.plan.blacksmith.limit == 0 ? '∞' : '‑'}
        </div>
      </div>
    `).join('');

    return `
      <div class="plans-list">
        <h2>Service Plans</h2>
        ${listHtml}
      </div>
      <div class="plan-detail">
        <div class="no-selection">Select a plan to view details</div>
      </div>
    `;
  };

  const renderPlanDetail = (service, plan) => {
    const planName = `${service.name} / ${plan.name}`;

    // Build the plan details table
    const detailsTable = `
      <table class="plan-details-table">
        <thead>
          <tr>
            <th colspan="2">Plan Information</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td class="info-key">Service</td>
            <td class="info-value">${service.name}</td>
          </tr>
          <tr>
            <td class="info-key">Plan</td>
            <td class="info-value">${plan.name}</td>
          </tr>
          <tr>
            <td class="info-key">Description</td>
            <td class="info-value">${plan.description || '-'}</td>
          </tr>
          <tr>
            <td class="info-key">Current Instances</td>
            <td class="info-value">${plan.blacksmith.instances}</td>
          </tr>
          <tr>
            <td class="info-key">Instance Limit</td>
            <td class="info-value">${plan.blacksmith.limit > 0 ? plan.blacksmith.limit : plan.blacksmith.limit == 0 ? 'Unlimited' : 'Not Set'}</td>
          </tr>
        </tbody>
      </table>
    `;

    // Build service tags
    const tagsHtml = service.tags && service.tags.length > 0
      ? `<ul class="tags">${service.tags.map(tag => `<li>${tag}</li>`).join('')}</ul>`
      : '';

    // Build VM configurations table if available
    let vmsTable = '';
    if (plan.vms && plan.vms.length > 0) {
      vmsTable = `
        <table class="plan-vms-table">
          <thead>
            <tr>
              <th>VM Name</th>
              <th>Count</th>
              <th>Type</th>
              <th>Persistent Disk</th>
              <th>Properties</th>
            </tr>
          </thead>
          <tbody>
            ${plan.vms.map(vm => `
              <tr>
                <td>${vm.name || '-'}</td>
                <td>${vm.instances || 1}</td>
                <td>${vm.vm_type || '-'}</td>
                <td>${vm.persistent_disk_type || '-'}</td>
                <td>${vm.properties ? JSON.stringify(vm.properties, null, 2) : '-'}</td>
              </tr>
            `).join('')}
          </tbody>
        </table>
      `;
    }

    return `
      <div class="service-detail-header">
        <h2 class="deployment-name">${planName}</h2>
        ${tagsHtml}
      </div>
      <div class="service-detail-content">
        ${detailsTable}
        ${vmsTable ? '<h3>VM Configuration</h3>' + vmsTable : ''}
        ${service.description ? `<div class="service-description"><h3>Service Description</h3><p>${service.description}</p></div>` : ''}
      </div>
    `;
  };

  const renderServicesTemplate = (instances) => {
    const instancesList = isEmpty(instances) ? [] : Object.entries(instances);

    // Extract unique services and plans for filter dropdowns
    const services = new Set();
    const plansPerService = {};

    instancesList.forEach(([id, details]) => {
      if (details.service_id) {
        services.add(details.service_id);
        if (!plansPerService[details.service_id]) {
          plansPerService[details.service_id] = new Set();
        }
        if (details.plan && details.plan.name) {
          plansPerService[details.service_id].add(details.plan.name);
        }
      }
    });

    const serviceOptions = Array.from(services).sort().map(s =>
      `<option value="${s}">${s}</option>`
    ).join('');

    const filterSection = `
      <div class="services-filter-section">
        <div class="filter-controls">
          <div class="filter-group">
            <label for="service-filter">Service:</label>
            <select id="service-filter" class="filter-select">
              <option value="">All Services</option>
              ${serviceOptions}
            </select>
          </div>
          <div class="filter-group">
            <label for="plan-filter">Plan:</label>
            <select id="plan-filter" class="filter-select" disabled>
              <option value="">All Plans</option>
            </select>
          </div>
          <button id="clear-filters" class="clear-filters-btn">Clear</button>
        </div>
        <div class="filter-status">
          <span id="filter-count">Showing ${instancesList.length} of ${instancesList.length} instances</span>
        </div>
      </div>
    `;

    const listHtml = instancesList.length === 0
      ? '<div class="no-data">No services have been provisioned yet.</div>'
      : instancesList.map(([id, details]) => `
          <div class="service-item" data-instance-id="${id}" data-service="${details.service_id}" data-plan="${details.plan?.name || ''}">
            <div class="service-id">${id}</div>
            <div class="service-meta">
              ${details.service_id} / ${details.plan.name} @ ${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}
            </div>
          </div>
        `).join('');

    return `
      <div class="services-list">
        <h2>Service Instances</h2>
        ${filterSection}
        <div class="services-items-container">
          ${listHtml}
        </div>
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

    // Build the vertical table content for the Details tab
    let tableRows = [];

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
          tableRows.push(`
            <tr>
              <td class="info-key" style="font-size: 16px;">${field.label}</td>
              <td class="info-value" style="font-size: 16px;">
                <span class="copy-wrapper">
                  <button class="copy-btn-inline" onclick="window.copyValue(event, '${(value || '-').toString().replace(/'/g, "\\'")}')"
                          title="Copy ${field.label}">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                  <span>${value || '-'}</span>
                </span>
              </td>
            </tr>
          `);
        }
      });

      // Add any additional fields not in the predefined order (except context and deployment_name)
      Object.keys(vaultData).forEach(key => {
        if (key !== 'context' && key !== 'deployment_name' &&
          !fieldOrder.find(f => f.key === key)) {
          const label = key.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase());
          const value = vaultData[key];
          tableRows.push(`
            <tr>
              <td class="info-key" style="font-size: 16px;">${label}</td>
              <td class="info-value" style="font-size: 16px;">
                <span class="copy-wrapper">
                  <button class="copy-btn-inline" onclick="window.copyValue(event, '${(value || '-').toString().replace(/'/g, "\\'")}')"
                          title="Copy ${label}">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                  <span>${value || '-'}</span>
                </span>
              </td>
            </tr>
          `);
        }
      });
    } else {
      // If no vault data, show basic info from details
      const createdAt = details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown';
      tableRows = [
        `<tr>
          <td class="info-key" style="font-size: 16px;">Instance ID</td>
          <td class="info-value" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, '${id}')"
                      title="Copy Instance ID">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>${id}</span>
            </span>
          </td>
        </tr>`,
        `<tr>
          <td class="info-key" style="font-size: 16px;">Service</td>
          <td class="info-value" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, '${details.service_id}')"
                      title="Copy Service">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>${details.service_id}</span>
            </span>
          </td>
        </tr>`,
        `<tr>
          <td class="info-key" style="font-size: 16px;">Plan</td>
          <td class="info-value" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, '${details.plan.name}')"
                      title="Copy Plan">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>${details.plan.name}</span>
            </span>
          </td>
        </tr>`,
        `<tr>
          <td class="info-key" style="font-size: 16px;">Created At</td>
          <td class="info-value" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, '${createdAt}')"
                      title="Copy Created At">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>${createdAt}</span>
            </span>
          </td>
        </tr>`
      ];
    }

    // Store the details content for the Details tab
    window.serviceInstanceDetailsContent = window.serviceInstanceDetailsContent || {};
    window.serviceInstanceDetailsContent[id] = `
      <table class="service-info-table">
        <tbody>
          ${tableRows.join('')}
        </tbody>
      </table>
    `;

    return `
      <div class="service-detail-header">
        <h3 class="deployment-name">${deploymentName}</h3>
      </div>
      <div class="detail-tabs">
        <button class="detail-tab active" data-tab="details">Details</button>
        <button class="detail-tab" data-tab="events">Events</button>
        <button class="detail-tab" data-tab="vms">VMs</button>
        <button class="detail-tab" data-tab="logs">Deployment Log</button>
        <button class="detail-tab" data-tab="debug">Debug Log</button>
        <button class="detail-tab" data-tab="manifest">Manifest</button>
        <button class="detail-tab" data-tab="credentials">Credentials</button>
        <button class="detail-tab" data-tab="instance-logs">Logs</button>
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

          const copyValue = typeof value === 'object' ? JSON.stringify(value, null, 2) : (value || '');
          html += `
            <tr>
              <td class="cred-key">${key}</td>
              <td class="cred-value">
                <span class="copy-wrapper">
                  <button class="copy-btn-inline" onclick="window.copyValue(event, '${copyValue.toString().replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
                          title="Copy to clipboard">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                  <span>${displayValue}</span>
                </span>
              </td>
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

        const copyValue = typeof values === 'object' ? JSON.stringify(values, null, 2) : (values || '');
        html += `
          <tr>
            <td class="cred-key">${section}</td>
            <td class="cred-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${copyValue.toString().replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${displayValue}</span>
              </span>
            </td>
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

  // Helper function to extract the latest deployment task ID from events
  const getLatestDeploymentTaskId = (events) => {
    if (!events || events.length === 0) {
      return null;
    }

    // Sort events by time to ensure we get the most recent
    // Some APIs return oldest first, some newest first
    const sortedEvents = [...events].sort((a, b) => {
      // Parse times and sort descending (newest first)
      const timeA = a.time ? (typeof a.time === 'string' ? new Date(a.time).getTime() : a.time) : 0;
      const timeB = b.time ? (typeof b.time === 'string' ? new Date(b.time).getTime() : b.time) : 0;
      return timeB - timeA;
    });

    // First priority: Find most recent non-'hm' user 'update' or 'create' event
    for (const event of sortedEvents) {
      // Skip events without task IDs
      if (!event.task_id || event.task_id === '') {
        continue;
      }

      // Skip 'hm' user events
      if (event.user === 'hm') {
        continue;
      }

      // Look for 'update' or 'create' actions
      if (event.action === 'create' || event.action === 'update') {
        console.log(`Selected task ${event.task_id} from ${event.user} ${event.action} event at ${event.time}`);
        return event.task_id;
      }
    }

    // Second priority: Any non-'hm' deployment-related event
    for (const event of sortedEvents) {
      // Skip events without task IDs
      if (!event.task_id || event.task_id === '') {
        continue;
      }

      // Skip 'hm' user events
      if (event.user === 'hm') {
        continue;
      }

      // Skip lock acquisition/release events
      if (event.action === 'acquire' || event.action === 'release') {
        continue;
      }

      // Look for deployment-related events
      if (event.object_type === 'deployment' || event.action === 'deploy') {
        console.log(`Selected task ${event.task_id} from ${event.user} ${event.action} deployment event at ${event.time}`);
        return event.task_id;
      }
    }

    // Fallback: Any non-'hm', non-lock task
    for (const event of sortedEvents) {
      if (event.task_id && event.task_id !== '' &&
        event.user !== 'hm' &&
        event.action !== 'acquire' && event.action !== 'release') {
        console.log(`Selected task ${event.task_id} from ${event.user} ${event.action} event (fallback) at ${event.time}`);
        return event.task_id;
      }
    }

    console.log('No suitable task found in events');
    return null;
  };

  // Fetch Blacksmith detail data
  const fetchBlacksmithDetail = async (deploymentName, type) => {
    // Validate deploymentName
    if (!deploymentName || deploymentName === 'undefined') {
      return `<div class="error">No deployment name specified</div>`;
    }

    try {
      if (type === 'blacksmith-logs') {
        // Fetch blacksmith logs
        const response = await fetch('/b/blacksmith/logs', { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const data = await response.json();
        return formatBlacksmithLogs(data.logs);

      } else if (type === 'events') {
        // Direct fetch for events
        const response = await fetch(`/b/deployments/${deploymentName}/events`, { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const events = await response.json();
        return formatEvents(events);

      } else if (type === 'logs' || type === 'debug') {
        // First fetch events to get task ID
        const eventsResponse = await fetch(`/b/deployments/${deploymentName}/events`, { cache: 'no-cache' });
        if (!eventsResponse.ok) {
          throw new Error(`Failed to fetch events: HTTP ${eventsResponse.status}`);
        }
        const events = await eventsResponse.json();

        // Extract latest deployment task ID
        const taskId = getLatestDeploymentTaskId(events);
        if (!taskId) {
          return '<div class="no-data">No deployment task found in events</div>';
        }

        // Fetch the appropriate log type
        const logType = type === 'logs' ? 'log' : 'debug';
        const logResponse = await fetch(`/b/deployments/${deploymentName}/tasks/${taskId}/${logType}`, { cache: 'no-cache' });
        if (!logResponse.ok) {
          throw new Error(`HTTP ${logResponse.status}: ${logResponse.statusText}`);
        }

        const logs = await logResponse.json();
        return type === 'logs' ? formatDeploymentLog(logs) : formatDebugLog(logs);

      } else if (type === 'vms') {
        // Use existing VMs endpoint
        const response = await fetch(`/b/blacksmith/vms`, { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const vms = await response.json();
        return formatVMs(vms);

      } else if (type === 'manifest') {
        // Fetch manifest for blacksmith deployment
        const response = await fetch(`/b/deployments/${deploymentName}/manifest`, { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const text = await response.text();

        // Store the manifest text for copy functionality
        const manifestId = `manifest-blacksmith-${Date.now()}`;
        window.manifestTexts = window.manifestTexts || {};
        window.manifestTexts[manifestId] = text;

        return `
          <div class="manifest-container">
            <div class="manifest-header">
              <button class="copy-btn-manifest" onclick="window.copyManifest('${manifestId}', event)"
                      title="Copy manifest to clipboard">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                <span>Copy</span>
              </button>
            </div>
            <pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>
          </div>
        `;

      } else if (type === 'credentials') {
        // Fetch blacksmith credentials
        const response = await fetch('/b/blacksmith/credentials');
        if (!response.ok) {
          throw new Error(`Failed to load credentials: ${response.statusText}`);
        }
        const creds = await response.json();
        return formatCredentials(creds);
      }

      return `<div class="error">Unknown tab type: ${type}</div>`;
    } catch (error) {
      return `<div class="error">Failed to load ${type}: ${error.message}</div>`;
    }
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
      debug: `/b/${instanceId}/task/debug`,
      'instance-logs': `/b/${instanceId}/instance-logs`
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
      } else if (type === 'instance-logs') {
        const text = await response.text();
        try {
          const logsData = JSON.parse(text);
          return formatInstanceLogs(logsData);
        } catch (e) {
          // If not JSON, display as plain text
          return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        }
      }

      const text = await response.text();
      // Add copy button for manifest
      if (type === 'manifest') {
        // Store the manifest text in a data attribute or use a different approach
        const manifestId = `manifest-${instanceId}-${Date.now()}`;
        window.manifestTexts = window.manifestTexts || {};
        window.manifestTexts[manifestId] = text;

        return `
          <div class="manifest-container">
            <div class="manifest-header">
              <button class="copy-btn-manifest" onclick="window.copyManifest('${manifestId}', event)"
                      title="Copy manifest to clipboard">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                <span>Copy</span>
              </button>
            </div>
            <pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>
          </div>
        `;
      }
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
      <div class="log-table-container">
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
      </div>
    `;
  };

  const formatDebugLog = (logs) => {
    if (!logs || logs.length === 0) {
      return '<div class="no-data">No debug logs available</div>';
    }

    return `
      <div class="log-table-container">
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
      </div>
    `;
  };

  const formatInstanceLogs = (logsData) => {
    if (!logsData || Object.keys(logsData).length === 0) {
      return '<div class="no-data">No logs available</div>';
    }

    // Store all log data globally for handlers
    window.instanceLogsData = logsData;

    // Get list of jobs
    const jobsList = Object.keys(logsData);
    const firstJob = jobsList[0];

    // Generate job tabs
    const jobTabs = jobsList.map((job, index) => `
      <button class="instance-log-job-tab ${index === 0 ? 'active' : ''}"
              data-job="${job}"
              onclick="window.selectInstanceJob('${job}')">
        ${job}
      </button>
    `).join('');

    // Generate initial content for first job
    const initialContent = formatJobLogContent(firstJob, logsData[firstJob]);

    return `
      <div class="instance-logs-wrapper">
        <div class="instance-log-job-tabs">
          ${jobTabs}
        </div>
        <div class="instance-logs-content" id="instance-logs-content">
          ${initialContent}
        </div>
      </div>
    `;
  };

  // Helper function to format job log content with dropdown and content
  const formatJobLogContent = (jobKey, jobData) => {
    if (!jobData) {
      return '<div class="no-data">No data available for this job</div>';
    }

    if (jobData.error) {
      return `<div class="error">${jobData.error}</div>`;
    }

    const files = jobData.files || {};
    const logs = jobData.logs || 'No logs available';

    // If no structured files, show raw logs
    if (typeof files !== 'object' || Object.keys(files).length === 0) {
      return `
        <div class="job-logs-single">
          <pre class="log-data">${escapeHtml(typeof logs === 'string' ? logs : JSON.stringify(logs, null, 2))}</pre>
        </div>
      `;
    }

    // Create layout with dropdown selector and content
    const filesList = Object.keys(files);
    const firstFile = filesList[0];

    // Store files for this job
    if (!window.instanceLogFiles) window.instanceLogFiles = {};
    window.instanceLogFiles[jobKey] = files;

    const fileOptions = filesList.map(filename => {
      // Show more of the path to distinguish similar filenames
      let displayName = filename;

      // Handle different types of log files
      if (filename.includes('@') && filename.includes('.bosh.log')) {
        // For RabbitMQ or other services with @ in the name (e.g., rabbit@hostname.bosh.log)
        // Keep the full filename as it contains important service identity information
        displayName = filename;
      } else if (filename.includes('.bosh.log')) {
        // For other BOSH logs without @, try to simplify but preserve important parts
        const parts = filename.split('.');
        const boshIndex = parts.findIndex(p => p === 'bosh');
        if (boshIndex > 0) {
          // Keep the last part before .bosh and everything after
          displayName = parts.slice(boshIndex - 1).join('.');
        }
      } else {
        // For other files (non-BOSH logs), show the last 2-3 path components for context
        const parts = filename.split('/');
        if (parts.length > 2) {
          displayName = parts.slice(-3).join('/');
        } else if (parts.length > 1) {
          displayName = parts.slice(-2).join('/');
        }
      }

      // Escape any HTML entities in filenames for security
      const escapedFilename = filename.replace(/"/g, '&quot;').replace(/'/g, '&#39;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      const escapedDisplay = displayName.replace(/</g, '&lt;').replace(/>/g, '&gt;');

      return `<option value="${escapedFilename}" title="${escapedFilename}">${escapedDisplay}</option>`;
    }).join('');

    return `
      <div class="job-logs-container">
        <div class="log-file-selector">
          <label for="log-select-${jobKey.replace(/\//g, '-')}" class="log-select-label">Log File:</label>
          <select id="log-select-${jobKey.replace(/\//g, '-')}"
                  class="log-file-dropdown"
                  onchange="window.selectLogFileForJob('${jobKey}', this.value)">
            ${fileOptions}
          </select>
        </div>
        <div class="log-file-display" id="log-display-${jobKey.replace(/\//g, '-')}">
          <pre class="log-file-content">${escapeHtml(files[firstFile] || 'No content')}</pre>
        </div>
      </div>
    `;
  };

  // Helper function to escape HTML
  const escapeHtml = (text) => {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  };

  // Handler for job tab selection
  window.selectInstanceJob = (job) => {
    const logsData = window.instanceLogsData;
    if (!logsData || !logsData[job]) return;

    // Update active tab
    document.querySelectorAll('.instance-log-job-tab').forEach(tab => {
      tab.classList.remove('active');
    });
    document.querySelector(`.instance-log-job-tab[data-job="${job}"]`).classList.add('active');

    // Update content area
    const contentArea = document.getElementById('instance-logs-content');
    if (contentArea) {
      contentArea.innerHTML = formatJobLogContent(job, logsData[job]);
    }
  };

  // Handler for log file selection from dropdown
  window.selectLogFileForJob = (job, filename) => {
    const files = window.instanceLogFiles && window.instanceLogFiles[job];
    if (!files || !files[filename]) return;

    // Update content display
    const displayEl = document.getElementById(`log-display-${job.replace(/\//g, '-')}`);
    if (displayEl) {
      displayEl.innerHTML = `<pre class="log-file-content">${escapeHtml(files[filename] || 'No content')}</pre>`;
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

  // Log parsing and rendering functions
  const parseLogLine = (line) => {
    // Regex pattern to match: YYYY-MM-DD HH:MM:SS.mmm LEVEL [context] message
    const logPattern = /^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2}\.\d{3})\s+(\w+)\s+(.*)$/;
    const match = line.match(logPattern);

    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: match[3].trim(),
        message: match[4]
      };
    }

    // Handle lines that don't match the pattern (continuation lines, etc.)
    return {
      date: '',
      time: '',
      level: '',
      message: line
    };
  };

  const highlightPatterns = (text) => {
    // Much simpler approach - only highlight very specific, safe patterns

    // URLs (avoid any with < or > that might be part of HTML)
    text = text.replace(/https?:\/\/[^\s<>&]+/g, '<span class="url">$&</span>');

    // Version strings (very specific format)
    text = text.replace(/\bversion: ([\w.-]+)/gi, 'version: <span class="version">$1</span>');
    text = text.replace(/\bcommit: ([a-f0-9]+)/gi, 'commit: <span class="version">$1</span>');

    // HTTP methods (standalone)
    text = text.replace(/\b(GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD)\b/g, '<span class="http-method">$1</span>');

    // HTTP status codes (3 digits at word boundary)
    text = text.replace(/\b(\d{3})\b(?=\s|$)/g, '<span class="http-status">$1</span>');

    // Task IDs
    text = text.replace(/\b(task|Task|TASK) (\d+)\b/g, '$1 <span class="task-id">$2</span>');

    return text;
  };

  const formatLogMessage = (message) => {
    // First escape HTML characters to prevent XSS (& must be escaped first)
    let escaped = message.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

    // Then apply our formatting to the escaped text
    // Bold context markers like [instance-details], [vault init], etc.
    let formatted = escaped.replace(/\[([^\]]+)\]/g, '<strong class="log-context">[$1]</strong>');

    // Highlight specific patterns
    formatted = highlightPatterns(formatted);

    return formatted;
  };

  const renderLogRow = (logEntry) => {
    const levelClass = `log-level-${logEntry.level.toLowerCase()}`;
    const formattedMessage = formatLogMessage(logEntry.message);

    return `
      <tr class="log-row ${levelClass}">
        <td class="log-date">${logEntry.date}</td>
        <td class="log-time">${logEntry.time}</td>
        <td class="log-level">
          <span class="level-badge ${levelClass}">${logEntry.level}</span>
        </td>
        <td class="log-message">${formattedMessage}</td>
      </tr>
    `;
  };

  const renderLogsTable = (logs) => {
    const lines = logs.split('\n').filter(line => line.trim());
    const rows = lines.map(line => parseLogLine(line));

    const tableHTML = `
      <table class="logs-table">
        <thead>
          <tr>
            <th class="log-col-date">Date</th>
            <th class="log-col-time">Time</th>
            <th class="log-col-level">Level</th>
            <th class="log-col-message">Message</th>
          </tr>
        </thead>
        <tbody id="logs-table-body">
          ${rows.map(row => renderLogRow(row)).join('')}
        </tbody>
      </table>
    `;

    return tableHTML;
  };

  const formatBlacksmithLogs = (logs) => {
    if (!logs || logs === '') {
      return '<div class="no-data">No logs available</div>';
    }

    const tableHTML = renderLogsTable(logs);

    return `
      <div class="logs-container">
        <div class="logs-header">
          <h3>Blacksmith Logs</h3>
          <button class="copy-btn-logs" onclick="window.copyLogs(event)"
                  title="Copy logs to clipboard">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Copy</span>
          </button>
        </div>
        <div class="logs-table-container">
          ${tableHTML}
        </div>
        <!-- Hidden source logs for copy functionality -->
        <div class="logs-source-hidden" id="blacksmith-logs-source" style="display: none;">
          ${logs}
        </div>
      </div>
    `;
  };

  const formatVMs = (vms) => {
    if (!vms || vms.length === 0) {
      return '<div class="no-data">No VMs available</div>';
    }

    return `
      <div class="vms-table-container">
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
                <td class="vm-ips">
                  ${ips !== '-' ? `
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${ips}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <span>${ips}</span>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-dns">${dns}</td>
                <td class="vm-cid">
                  ${vm.cid ? `
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${vm.cid}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <span>${vm.cid}</span>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-resurrection">${resurrection}</td>
              </tr>
            `;
    }).join('')}
        </tbody>
      </table>
      </div>
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

  // Export copy function to window for onclick handlers
  window.copyValue = async (event, text) => {
    event.preventDefault();
    event.stopPropagation();
    const button = event.currentTarget;
    try {
      await navigator.clipboard.writeText(text);
      // Visual feedback
      button.classList.add('copied');
      const originalTitle = button.title;
      button.title = 'Copied!';
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
      }, 2000);
    } catch (err) {
      console.error('Failed to copy:', err);
      // Fallback for older browsers
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      try {
        document.execCommand('copy');
        button.classList.add('copied');
        setTimeout(() => button.classList.remove('copied'), 2000);
      } catch (err) {
        console.error('Fallback copy failed:', err);
      }
      document.body.removeChild(textarea);
    }
  };

  // Copy logs function
  window.copyLogs = async (event) => {
    // Get the hidden source logs instead of table content
    const logsSource = document.getElementById('blacksmith-logs-source');
    if (!logsSource) {
      console.error('Logs source not found');
      return;
    }

    const text = logsSource.textContent || logsSource.innerText;
    const button = event.currentTarget;

    try {
      await navigator.clipboard.writeText(text);
      // Visual feedback
      button.classList.add('copied');
      const originalTitle = button.title;
      button.title = 'Copied!';
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Copied!';
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
        spanElement.textContent = originalText;
      }, 2000);
    } catch (err) {
      console.error('Failed to copy logs:', err);
      // Fallback for older browsers
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      try {
        document.execCommand('copy');
        button.classList.add('copied');
        const spanElement = button.querySelector('span');
        const originalText = spanElement.textContent;
        spanElement.textContent = 'Copied!';
        setTimeout(() => {
          button.classList.remove('copied');
          spanElement.textContent = originalText;
        }, 2000);
      } catch (err) {
        console.error('Fallback copy failed:', err);
      }
      document.body.removeChild(textarea);
    }
  };

  // Copy manifest function
  window.copyManifest = async (manifestId, event) => {
    const text = window.manifestTexts[manifestId];
    if (!text) {
      console.error('Manifest text not found for ID:', manifestId);
      return;
    }

    const button = event.currentTarget;
    try {
      await navigator.clipboard.writeText(text);
      // Visual feedback
      button.classList.add('copied');
      const originalTitle = button.title;
      button.title = 'Copied!';
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Copied!';
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
        spanElement.textContent = originalText;
      }, 2000);
    } catch (err) {
      console.error('Failed to copy manifest:', err);
      // Fallback for older browsers
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      try {
        document.execCommand('copy');
        button.classList.add('copied');
        const spanElement = button.querySelector('span');
        const originalText = spanElement.textContent;
        spanElement.textContent = 'Copied!';
        setTimeout(() => {
          button.classList.remove('copied');
          spanElement.textContent = originalText;
        }, 2000);
      } catch (err) {
        console.error('Fallback copy failed:', err);
      }
      document.body.removeChild(textarea);
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
      const plansPanel = document.querySelector('#plans');
      if (plansPanel) {
        if (catalog.services && catalog.services.length > 0) {
          plansPanel.innerHTML = renderPlansTemplate(catalog);

          // Store plans data for later use
          window.plansData = catalog;

          // Set up plan click handlers
          document.querySelectorAll('#plans .plan-item').forEach(item => {
            item.addEventListener('click', function () {
              const planId = this.dataset.planId;

              // Find the service and plan from the stored data
              let selectedService = null;
              let selectedPlan = null;

              catalog.services.forEach(service => {
                service.plans.forEach(plan => {
                  if (`${service.name}-${plan.name}` === planId) {
                    selectedService = service;
                    selectedPlan = plan;
                  }
                });
              });

              if (selectedService && selectedPlan) {
                // Update active state
                document.querySelectorAll('#plans .plan-item').forEach(i => i.classList.remove('active'));
                this.classList.add('active');

                // Render plan details
                const detailContainer = document.querySelector('#plans .plan-detail');
                detailContainer.innerHTML = renderPlanDetail(selectedService, selectedPlan);
              }
            });
          });
        } else {
          plansPanel.innerHTML = '<div class="no-data">No services configured</div>';
        }
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

              // Load initial tab content (details)
              loadDetailTab(instanceId, 'details');

              // Set up detail tab handlers
              setupDetailTabHandlers(instanceId);
            });
          });

          // Set up filter handlers
          setupFilterHandlers();
        };

        // Filter functionality
        const setupFilterHandlers = () => {
          const serviceFilter = document.getElementById('service-filter');
          const planFilter = document.getElementById('plan-filter');
          const clearFiltersBtn = document.getElementById('clear-filters');
          const filterCount = document.getElementById('filter-count');

          if (!serviceFilter || !planFilter) return;

          // Build plans per service map for the filter
          const plansPerService = {};
          const instancesList = Object.entries(window.serviceInstances || {});

          instancesList.forEach(([id, details]) => {
            if (details.service_id) {
              if (!plansPerService[details.service_id]) {
                plansPerService[details.service_id] = new Set();
              }
              if (details.plan && details.plan.name) {
                plansPerService[details.service_id].add(details.plan.name);
              }
            }
          });

          // Apply filters function
          const applyFilters = () => {
            const selectedService = serviceFilter.value;
            const selectedPlan = planFilter.value;
            const allItems = document.querySelectorAll('#services .service-item');
            let visibleCount = 0;

            allItems.forEach(item => {
              const itemService = item.dataset.service;
              const itemPlan = item.dataset.plan;

              const matchesService = !selectedService || itemService === selectedService;
              const matchesPlan = !selectedPlan || itemPlan === selectedPlan;

              if (matchesService && matchesPlan) {
                item.style.display = '';
                visibleCount++;
              } else {
                item.style.display = 'none';
              }
            });

            // Update count
            if (filterCount) {
              filterCount.textContent = `Showing ${visibleCount} of ${allItems.length} instances`;
            }
          };

          // Service filter change handler
          serviceFilter.addEventListener('change', (e) => {
            const selectedService = e.target.value;

            // Update plan filter options
            planFilter.innerHTML = '<option value="">All Plans</option>';

            if (selectedService && plansPerService[selectedService]) {
              const plans = Array.from(plansPerService[selectedService]).sort();
              plans.forEach(plan => {
                const option = document.createElement('option');
                option.value = plan;
                option.textContent = plan;
                planFilter.appendChild(option);
              });
              planFilter.disabled = false;
            } else {
              planFilter.disabled = !selectedService;
            }

            // Apply filters
            applyFilters();
          });

          // Plan filter change handler
          planFilter.addEventListener('change', () => {
            applyFilters();
          });

          // Clear filters button
          if (clearFiltersBtn) {
            clearFiltersBtn.addEventListener('click', () => {
              serviceFilter.value = '';
              planFilter.value = '';
              planFilter.disabled = true;
              planFilter.innerHTML = '<option value="">All Plans</option>';
              applyFilters();
            });
          }
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

          // Handle the Details tab
          if (tabType === 'details') {
            contentContainer.innerHTML = window.serviceInstanceDetailsContent[instanceId] || '<div class="no-data">No details available</div>';
            return;
          }

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

        try {
          // Handle the Details tab
          if (tabType === 'details') {
            contentContainer.innerHTML = window.blacksmithDetailsContent || '<div class="no-data">No details available</div>';
            return;
          }

          // Get deployment name from the blacksmith instance data
          // This should already be available from the initial blacksmith status load
          const deploymentName = window.blacksmithDeploymentName || 'blacksmith';

          const content = await fetchBlacksmithDetail(deploymentName, tabType);
          contentContainer.innerHTML = content;
        } catch (error) {
          contentContainer.innerHTML = `<div class="error">Failed to load tab: ${error.message}</div>`;
        }
      };

      // Render Blacksmith panel
      const blacksmithPanel = document.querySelector('#blacksmith');
      if (blacksmithPanel) {
        // Fetch blacksmith instance details to get deployment name
        try {
          const instanceResponse = await fetch('/b/instance', { cache: 'no-cache' });
          if (instanceResponse.ok) {
            const instanceData = await instanceResponse.json();
            // Merge instance data with status data
            data.deployment = instanceData.deployment || 'blacksmith';
            data.az = instanceData.az;
            data.instanceId = instanceData.id;
            data.instanceName = instanceData.name;
            // Store deployment name globally for tab handlers to use
            window.blacksmithDeploymentName = instanceData.deployment || 'blacksmith';

            // Update the header with deployment name
            const deploymentNameEl = document.getElementById('deployment-name');
            if (deploymentNameEl) {
              deploymentNameEl.textContent = data.deployment;
              deploymentNameEl.style.marginLeft = '10px';
              deploymentNameEl.style.fontWeight = 'normal';
            }
          }
        } catch (error) {
          console.error('Failed to fetch blacksmith instance details:', error);
          data.deployment = 'blacksmith'; // Fallback
          window.blacksmithDeploymentName = 'blacksmith';
        }

        // Store status data for later use
        window.blacksmithData = data;

        // Render the Blacksmith detail view
        blacksmithPanel.innerHTML = renderBlacksmithTemplate(data);

        // Load initial tab content (details)
        loadBlacksmithDetailTab('details');

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
