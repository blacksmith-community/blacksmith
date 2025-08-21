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

  // Log Selection Management Utilities
  const LogSelectionManager = {
    // Storage keys
    BLACKSMITH_LOGS_KEY: 'blacksmith.logs.lastSelected',
    INSTANCE_LOGS_PREFIX: 'blacksmith.logs.instance',

    // Get last selected blacksmith log
    getLastBlacksmithLog: function () {
      try {
        return localStorage.getItem(this.BLACKSMITH_LOGS_KEY);
      } catch (e) {
        console.warn('Failed to read from localStorage:', e);
        return null;
      }
    },

    // Save selected blacksmith log
    saveBlacksmithLog: function (logPath) {
      try {
        localStorage.setItem(this.BLACKSMITH_LOGS_KEY, logPath);
      } catch (e) {
        console.warn('Failed to save to localStorage:', e);
      }
    },

    // Get last selected instance log
    getLastInstanceLog: function (instanceId, jobName) {
      try {
        const key = `${this.INSTANCE_LOGS_PREFIX}.${instanceId}.${jobName}`;
        return localStorage.getItem(key);
      } catch (e) {
        console.warn('Failed to read from localStorage:', e);
        return null;
      }
    },

    // Save selected instance log
    saveInstanceLog: function (instanceId, jobName, logPath) {
      try {
        const key = `${this.INSTANCE_LOGS_PREFIX}.${instanceId}.${jobName}`;
        localStorage.setItem(key, logPath);
      } catch (e) {
        console.warn('Failed to save to localStorage:', e);
      }
    },

    // Get default blacksmith log
    getDefaultBlacksmithLog: function (availableFiles) {
      // Check for last selection
      const lastSelected = this.getLastBlacksmithLog();
      if (lastSelected && availableFiles.some(f => f.path === lastSelected)) {
        return lastSelected;
      }

      // Default to blacksmith stdout log
      const defaultLog = '/var/vcap/sys/log/blacksmith/blacksmith.stdout.log';
      if (availableFiles.some(f => f.path === defaultLog)) {
        return defaultLog;
      }

      // Fallback to first available
      return availableFiles[0]?.path;
    },

    // Get smart default for service instance logs
    getSmartDefault: function (files, serviceName, servicePlan, instanceId, jobName) {
      // Check for last selection first
      if (instanceId && jobName) {
        const lastSelected = this.getLastInstanceLog(instanceId, jobName);
        if (lastSelected && files.includes(lastSelected)) {
          return lastSelected;
        }
      }

      // Normalize service name for matching
      const normalizedService = serviceName ? serviceName.toLowerCase() : '';

      // RabbitMQ specific
      if (normalizedService.includes('rabbitmq') || normalizedService.includes('rabbit')) {
        // Look for logs starting with rabbitmq@ or rabbit@
        const rabbitLog = files.find(f => {
          const filename = f.split('/').pop().toLowerCase();
          return filename.startsWith('rabbitmq@') || filename.startsWith('rabbit@');
        });
        if (rabbitLog) return rabbitLog;

        // Fallback to any rabbitmq log
        const anyRabbitLog = files.find(f => f.toLowerCase().includes('rabbitmq'));
        if (anyRabbitLog) return anyRabbitLog;
      }

      // Redis specific
      if (normalizedService.includes('redis')) {
        // Look for standalone or cluster stdout logs (but not pre-start)
        const redisLog = files.find(f => {
          const lowerFile = f.toLowerCase();
          if (lowerFile.includes('pre-start')) return false;

          // For Redis, look for standalone-N/standalone-N.stdout.log pattern
          const filename = f.split('/').pop();
          if (filename.includes('standalone') && filename.endsWith('.stdout.log')) {
            // Check if it's the main service log (e.g., standalone-6.stdout.log)
            const parts = filename.split('.');
            if (parts[0].includes('standalone') && !parts[0].includes('pre-start')) {
              return true;
            }
          }

          return (lowerFile.includes('standalone') && lowerFile.includes('stdout') && !lowerFile.includes('pre-start')) ||
            (lowerFile.includes('cluster') && lowerFile.includes('stdout') && !lowerFile.includes('pre-start')) ||
            (lowerFile.includes('redis') && lowerFile.includes('stdout') && !lowerFile.includes('pre-start'));
        });
        if (redisLog) return redisLog;

        // Fallback to any redis stdout log (excluding pre-start)
        const anyRedisStdout = files.find(f => {
          const lowerFile = f.toLowerCase();
          return lowerFile.includes('redis') && lowerFile.includes('stdout') && !lowerFile.includes('pre-start');
        });
        if (anyRedisStdout) return anyRedisStdout;
      }

      // General service logic
      // Try to match job name with stdout first (e.g., standalone-6/standalone-6.stdout.log)
      if (jobName) {
        const jobBaseName = jobName.split('/')[0]; // Get "standalone-6" from "standalone-6/0"
        const jobLog = files.find(f => {
          const filename = f.split('/').pop();
          return filename.toLowerCase().startsWith(jobBaseName.toLowerCase()) &&
            filename.endsWith('.stdout.log') &&
            !filename.includes('pre-start');
        });
        if (jobLog) return jobLog;
      }

      // Try to match service name with stdout (excluding pre-start)
      if (serviceName) {
        const serviceLog = files.find(f => {
          const lowerFile = f.toLowerCase();
          return lowerFile.includes(normalizedService) &&
            lowerFile.includes('stdout') &&
            !lowerFile.includes('pre-start');
        });
        if (serviceLog) return serviceLog;
      }

      // Try to match service plan with stdout (excluding pre-start)
      if (servicePlan) {
        const normalizedPlan = servicePlan.toLowerCase();
        const planLog = files.find(f => {
          const lowerFile = f.toLowerCase();
          return lowerFile.includes(normalizedPlan) &&
            lowerFile.includes('stdout') &&
            !lowerFile.includes('pre-start');
        });
        if (planLog) return planLog;
      }

      // Try to find any stdout log (excluding pre-start)
      const stdoutLog = files.find(f => {
        const lowerFile = f.toLowerCase();
        return lowerFile.includes('stdout') && !lowerFile.includes('pre-start');
      });
      if (stdoutLog) return stdoutLog;

      // Fallback to first file
      return files[0];
    }
  };

  // Table search filter functionality
  const createSearchFilter = (tableId, placeholder = 'Search...') => {
    return `
      <div class="table-search-container">
        <input type="text"
               class="table-search-input"
               id="search-${tableId}"
               placeholder="${placeholder}"
               autocomplete="off">
        <button class="clear-search-btn"
                id="clear-${tableId}"
                title="Clear search">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <line x1="18" y1="6" x2="6" y2="18"></line>
            <line x1="6" y1="6" x2="18" y2="18"></line>
          </svg>
        </button>
      </div>
    `;
  };

  const attachSearchFilter = (tableId) => {
    const searchInput = document.getElementById(`search-${tableId}`);
    const clearBtn = document.getElementById(`clear-${tableId}`);

    if (!searchInput) return;

    const filterTable = () => {
      const filter = searchInput.value.toLowerCase();
      const table = document.querySelector(`.${tableId}`);
      if (!table) return;

      const rows = table.querySelectorAll('tbody tr');
      let visibleCount = 0;

      rows.forEach(row => {
        const text = row.textContent.toLowerCase();
        if (text.includes(filter)) {
          row.style.display = '';
          visibleCount++;
        } else {
          row.style.display = 'none';
        }
      });

      // Show/hide clear button
      if (clearBtn) {
        clearBtn.style.display = filter ? 'block' : 'none';
      }

      // Add no results message if needed
      const existingMsg = table.parentElement.querySelector('.no-results-msg');
      if (existingMsg) {
        existingMsg.remove();
      }

      if (visibleCount === 0 && filter) {
        const msg = document.createElement('div');
        msg.className = 'no-results-msg';
        msg.textContent = 'No matching results found';
        table.parentElement.appendChild(msg);
      }
    };

    searchInput.addEventListener('input', filterTable);
    searchInput.addEventListener('keyup', filterTable);

    if (clearBtn) {
      clearBtn.addEventListener('click', () => {
        searchInput.value = '';
        filterTable();
        searchInput.focus();
      });
    }
  };

  // Search filter state persistence utilities
  const captureSearchFilterState = (tableId) => {
    const searchInput = document.getElementById(`search-${tableId}`);
    return searchInput ? searchInput.value : '';
  };

  const restoreSearchFilterState = (tableId, searchValue) => {
    if (!searchValue) return;

    const searchInput = document.getElementById(`search-${tableId}`);
    if (searchInput) {
      searchInput.value = searchValue;
      // Trigger the filter function
      searchInput.dispatchEvent(new Event('input'));
    }
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

  // Table Sorting and Filtering Utilities

  // Global state for table sorting
  const tableSortStates = new Map();
  const tableOriginalData = new Map();

  // Cycle through sort states: null -> asc -> desc -> null
  const cycleSortState = (currentState) => {
    if (!currentState || currentState === null) return 'asc';
    if (currentState === 'asc') return 'desc';
    return null;
  };

  // Get nested object value by path (e.g., "data.status")
  const getNestedValue = (obj, path) => {
    if (!obj || !path) return null;
    const keys = path.split('.');
    let value = obj;
    for (const key of keys) {
      value = value?.[key];
      if (value === undefined) return null;
    }
    return value;
  };

  // Determine data type for sorting
  const detectDataType = (value) => {
    if (value === null || value === undefined || value === '-') return 'null';

    // Check if it's a date
    const datePatterns = [
      /^\d{4}-\d{2}-\d{2}$/,  // YYYY-MM-DD
      /^\d{2}:\d{2}:\d{2}/,   // HH:MM:SS
      /^\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}/  // Full datetime
    ];

    if (typeof value === 'string') {
      for (const pattern of datePatterns) {
        if (pattern.test(value)) return 'date';
      }

      // Check if it's a number
      if (/^-?\d+(\.\d+)?%?$/.test(value)) return 'number';

      // Check if it's an IP address
      if (/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/.test(value)) return 'ip';
    }

    if (typeof value === 'number') return 'number';

    return 'string';
  };

  // Compare values based on data type
  const compareValues = (a, b, dataType, direction) => {
    // Handle null/undefined/'-' values
    if (a === null || a === undefined || a === '-') a = null;
    if (b === null || b === undefined || b === '-') b = null;

    if (a === null && b === null) return 0;
    if (a === null) return direction === 'asc' ? 1 : -1;
    if (b === null) return direction === 'asc' ? -1 : 1;

    let result = 0;

    switch (dataType) {
      case 'date':
        const dateA = new Date(a);
        const dateB = new Date(b);
        result = dateA - dateB;
        break;

      case 'number':
        const numA = parseFloat(String(a).replace('%', ''));
        const numB = parseFloat(String(b).replace('%', ''));
        result = numA - numB;
        break;

      case 'ip':
        const ipA = a.split('.').map(n => parseInt(n)).reduce((acc, n, i) => acc + n * Math.pow(256, 3 - i), 0);
        const ipB = b.split('.').map(n => parseInt(n)).reduce((acc, n, i) => acc + n * Math.pow(256, 3 - i), 0);
        result = ipA - ipB;
        break;

      default:
        // String comparison
        const strA = String(a).toLowerCase();
        const strB = String(b).toLowerCase();
        result = strA.localeCompare(strB);
    }

    return direction === 'desc' ? -result : result;
  };

  // Sort array of objects by key
  const sortData = (data, key, direction) => {
    if (!data || !Array.isArray(data) || data.length === 0) return data;
    if (!direction || direction === null) return data;

    // Detect data type from first non-null value
    let dataType = 'string';
    for (const item of data) {
      const value = getNestedValue(item, key);
      if (value !== null && value !== undefined && value !== '-') {
        dataType = detectDataType(value);
        break;
      }
    }

    // Special handling for certain columns
    if (key === 'level') {
      // Log level priority ordering
      const levelPriority = { 'ERROR': 0, 'WARN': 1, 'WARNING': 1, 'INFO': 2, 'DEBUG': 3 };
      return [...data].sort((a, b) => {
        const aLevel = (getNestedValue(a, key) || '').toUpperCase();
        const bLevel = (getNestedValue(b, key) || '').toUpperCase();
        const aPriority = levelPriority[aLevel] ?? 999;
        const bPriority = levelPriority[bLevel] ?? 999;
        const result = aPriority - bPriority;
        return direction === 'desc' ? -result : result;
      });
    }

    if (key === 'state') {
      // State priority ordering
      const statePriority = {
        'running': 0, 'finished': 1, 'in_progress': 2, 'started': 2,
        'stopped': 3, 'failing': 4, 'failed': 4, 'error': 5, 'unresponsive': 5
      };
      return [...data].sort((a, b) => {
        const aState = (getNestedValue(a, key) || '').toLowerCase();
        const bState = (getNestedValue(b, key) || '').toLowerCase();
        const aPriority = statePriority[aState] ?? 999;
        const bPriority = statePriority[bState] ?? 999;
        const result = aPriority - bPriority;
        return direction === 'desc' ? -result : result;
      });
    }

    // General sorting
    return [...data].sort((a, b) => {
      const aVal = getNestedValue(a, key);
      const bVal = getNestedValue(b, key);
      return compareValues(aVal, bVal, dataType, direction);
    });
  };

  // Create sort indicator element
  const createSortIndicator = (column, currentSort) => {
    const indicator = document.createElement('span');
    indicator.className = 'sort-icon';
    indicator.setAttribute('data-column', column);

    if (currentSort && currentSort.column === column) {
      indicator.classList.add('active');
      indicator.classList.add(currentSort.direction);
    } else {
      indicator.classList.add('unsorted');
    }

    return indicator;
  };

  // Update sort indicators after re-render
  const updateSortIndicators = (tableClass, sortState) => {
    const table = document.querySelector(`.${tableClass}`);
    if (!table) return;

    const indicators = table.querySelectorAll('.sort-icon');
    indicators.forEach(indicator => {
      const column = indicator.getAttribute('data-column');
      indicator.className = 'sort-icon';

      if (sortState && sortState.column === column && sortState.direction) {
        indicator.classList.add('active', sortState.direction);
      } else {
        indicator.classList.add('unsorted');
      }
    });
  };

  // Get table column configuration
  const getTableColumns = (tableClass) => {
    const columnConfigs = {
      'events-table': [
        { key: 'time', sortable: true },
        { key: 'user', sortable: true },
        { key: 'action', sortable: true },
        { key: 'object_type', sortable: true },
        { key: 'task_id', sortable: true },
        { key: 'error', sortable: true }
      ],
      'vms-table': [
        { key: 'instance', sortable: true },
        { key: 'state', sortable: true },
        { key: 'job_state', sortable: true },
        { key: 'az', sortable: true },
        { key: 'vm_type', sortable: true },
        { key: 'active', sortable: true },
        { key: 'bootstrap', sortable: true },
        { key: 'ips', sortable: true },
        { key: 'dns', sortable: true },
        { key: 'vm_cid', sortable: true },
        { key: 'agent_id', sortable: true },
        { key: 'vm_created_at', sortable: true },
        { key: 'disk_cids', sortable: true },
        { key: 'resurrection', sortable: true }
      ],
      'logs-table': [
        { key: 'date', sortable: true },
        { key: 'time', sortable: true },
        { key: 'level', sortable: true },
        { key: 'message', sortable: true }
      ],
      'deployment-log-table': [
        { key: 'time', sortable: true },
        { key: 'stage', sortable: true },
        { key: 'task', sortable: true },
        { key: 'index', sortable: true },
        { key: 'state', sortable: true },
        { key: 'progress', sortable: true },
        { key: 'tags', sortable: false },
        { key: 'status', sortable: true }
      ],
      'debug-log-table': [
        { key: 'time', sortable: true },
        { key: 'stage', sortable: true },
        { key: 'task', sortable: true },
        { key: 'index', sortable: true },
        { key: 'state', sortable: true },
        { key: 'progress', sortable: true },
        { key: 'tags', sortable: false },
        { key: 'status', sortable: true }
      ],
      'manifest-table': [
        { key: 'name', sortable: true },
        { key: 'version', sortable: true },
        { key: 'url', sortable: true },
        { key: 'sha1', sortable: true }
      ]
    };

    // Check if this is an instance logs table (dynamic class name)
    if (tableClass.startsWith('instance-logs-table-')) {
      return [
        { key: 'date', sortable: true },
        { key: 'time', sortable: true },
        { key: 'level', sortable: true },
        { key: 'message', sortable: true }
      ];
    }

    return columnConfigs[tableClass] || [];
  };

  // Filtering utilities

  // Debounce function for filter input
  const debounce = (func, wait) => {
    let timeout;
    return function executedFunction(...args) {
      const later = () => {
        clearTimeout(timeout);
        func(...args);
      };
      clearTimeout(timeout);
      timeout = setTimeout(later, wait);
    };
  };

  // Filter table rows
  const filterTableRows = (tableClass, filterText) => {
    const table = document.querySelector(`.${tableClass}`);
    if (!table) return;

    const tbody = table.querySelector('tbody');
    if (!tbody) return;

    const rows = tbody.querySelectorAll('tr');
    const searchText = filterText.toLowerCase().trim();

    if (!searchText) {
      // Show all rows if filter is empty
      rows.forEach(row => {
        row.style.display = '';
        row.classList.remove('filtered-out');
      });
      updateFilteredCount(tableClass, rows.length, rows.length);
      return;
    }

    let visibleCount = 0;
    rows.forEach(row => {
      const cells = row.querySelectorAll('td');
      let matches = false;

      for (const cell of cells) {
        const text = cell.textContent.toLowerCase();
        if (text.includes(searchText)) {
          matches = true;
          break;
        }
      }

      if (matches) {
        row.style.display = '';
        row.classList.remove('filtered-out');
        visibleCount++;
      } else {
        row.style.display = 'none';
        row.classList.add('filtered-out');
      }
    });

    updateFilteredCount(tableClass, visibleCount, rows.length);
  };

  // Update filtered count display
  const updateFilteredCount = (tableClass, visible, total) => {
    const container = document.querySelector(`.${tableClass}`).closest('.logs-container, .log-table-container');
    if (!container) return;

    let countDisplay = container.querySelector('.filter-count');
    if (!countDisplay) {
      countDisplay = document.createElement('span');
      countDisplay.className = 'filter-count';
      const filterContainer = container.querySelector('.filter-container');
      if (filterContainer) {
        filterContainer.appendChild(countDisplay);
      }
    }

    if (visible < total) {
      countDisplay.textContent = `(${visible}/${total})`;
      countDisplay.style.display = 'inline';
    } else {
      countDisplay.style.display = 'none';
    }
  };

  // Create filter input element with magnifying glass icon
  const createFilterInput = (tableClass) => {
    const container = document.createElement('div');
    container.className = 'filter-container';
    container.innerHTML = `
      <span class="filter-icon" title="Filter logs">🔍</span>
      <input type="text" class="filter-input" placeholder="Filter..." />
      <span class="filter-count" style="display: none;"></span>
    `;

    const input = container.querySelector('.filter-input');
    const debouncedFilter = debounce((value) => filterTableRows(tableClass, value), 300);

    input.addEventListener('input', (e) => {
      debouncedFilter(e.target.value);
    });

    // Add keyboard shortcut (Escape to clear)
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        input.value = '';
        filterTableRows(tableClass, '');
      }
    });

    return container;
  };

  // Initialize sorting on a table
  const initializeSorting = (tableClass) => {
    const table = document.querySelector(`.${tableClass}`);
    if (!table) return;

    // For instance logs tables, skip the control row and get the actual header row
    let headers;
    if (tableClass.startsWith('instance-logs-table-')) {
      // Find the row with actual column headers (not the controls row)
      const headerRows = table.querySelectorAll('thead tr');
      const headerRow = Array.from(headerRows).find(row => {
        const firstTh = row.querySelector('th');
        return firstTh && !firstTh.classList.contains('table-controls-header');
      });
      headers = headerRow ? headerRow.querySelectorAll('th') : [];
    } else {
      headers = table.querySelectorAll('thead th');
    }

    const columns = getTableColumns(tableClass);
    const sortState = tableSortStates.get(tableClass) || { column: null, direction: null };

    headers.forEach((header, index) => {
      if (index >= columns.length) return;

      const column = columns[index];
      if (!column.sortable) return;

      // Remove existing sort icon if present (to handle re-initialization)
      const existingIcon = header.querySelector('.sort-icon');
      if (existingIcon) {
        existingIcon.remove();
      }

      // Add sort indicator
      const indicator = createSortIndicator(column.key, sortState);
      header.appendChild(indicator);
      header.style.cursor = 'pointer';

      // Remove old event listeners and add new one
      // Clone the header to remove all existing event listeners
      const cleanHeader = header.cloneNode(false);
      cleanHeader.innerHTML = header.innerHTML;
      cleanHeader.style.cursor = header.style.cursor;

      // Replace the old header with the clean one
      header.parentNode.replaceChild(cleanHeader, header);

      // Add click handler to the clean header
      cleanHeader.addEventListener('click', () => {
        handleTableSort(tableClass, column.key);
      });
    });
  };

  // Simplified handle sort for internal use
  const handleTableSort = (tableClass, columnKey) => {
    const currentState = tableSortStates.get(tableClass) || { column: null, direction: null };

    let newDirection;
    if (currentState.column !== columnKey) {
      newDirection = 'asc';
    } else {
      newDirection = cycleSortState(currentState.direction);
    }

    const newState = { column: columnKey, direction: newDirection };
    tableSortStates.set(tableClass, newState);

    // Get the data key based on table class
    const dataKeyMap = {
      'events-table': 'events',
      'vms-table': 'vms',
      'logs-table': 'blacksmith-logs',
      'deployment-log-table': 'deployment-logs',
      'debug-log-table': 'debug-logs'
    };

    // Check if this is a service events table (uses different dataKey)
    if (tableClass === 'events-table' && tableOriginalData.has('service-events')) {
      dataKeyMap['events-table'] = 'service-events';
    }

    // For instance logs tables, the data key is stored with the job name
    let dataKey = dataKeyMap[tableClass];
    if (tableClass.startsWith('instance-logs-table-')) {
      // Extract job name from the table class (e.g., 'instance-logs-table-redis-0' -> 'redis/0')
      const jobPart = tableClass.replace('instance-logs-table-', '').replace(/-/g, '/');
      dataKey = `instance-logs-${jobPart}`;
    }

    const originalData = tableOriginalData.get(dataKey);

    if (!originalData) return;

    const sortedData = newDirection ? sortData(originalData, columnKey, newDirection) : [...originalData];

    // Re-render the table body
    updateTableBody(tableClass, sortedData);

    // Update indicators
    updateSortIndicators(tableClass, newState);
  };

  // Update table body with sorted data
  const updateTableBody = (tableClass, data) => {
    const table = document.querySelector(`.${tableClass}`);
    if (!table) return;

    const tbody = table.querySelector('tbody');
    if (!tbody) return;

    // Re-render based on table type
    if (tableClass === 'events-table') {
      tbody.innerHTML = data.map(event => {
        const time = formatTimestamp(event.time);
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
      }).join('');
    } else if (tableClass === 'logs-table') {
      tbody.innerHTML = data.map(row => renderLogRow(row)).join('');
    } else if (tableClass === 'vms-table') {
      tbody.innerHTML = data.map(vm => {
        // Use the correct field names from our enhanced VM struct
        const instanceName = vm.job_name && vm.index !== undefined ? `${vm.job_name}/${vm.index}` : vm.id || '-';
        const ips = vm.ips && vm.ips.length > 0 ? vm.ips.join(', ') : '-';
        const dns = vm.dns && vm.dns.length > 0 ? vm.dns.join(', ') : '-';
        const vmType = vm.vm_type || vm.resource_pool || '-';
        const resurrection = vm.resurrection_paused ? 'Paused' : 'Active';
        const agentId = vm.agent_id || '-';
        const vmCreatedAt = vm.vm_created_at ? new Date(vm.vm_created_at).toLocaleString() : '-';
        const active = vm.active !== undefined ? (vm.active ? 'Yes' : 'No') : '-';
        const bootstrap = vm.bootstrap ? 'Yes' : 'No';
        const diskCids = vm.disk_cids && vm.disk_cids.length > 0 ? vm.disk_cids.join(', ') : (vm.disk_cid || '-');

        let stateClass = '';
        if (vm.state === 'running') {
          stateClass = 'vm-state-running';
        } else if (vm.state === 'failing' || vm.state === 'unresponsive') {
          stateClass = 'vm-state-error';
        } else if (vm.state === 'stopped') {
          stateClass = 'vm-state-stopped';
        }

        let jobStateClass = '';
        if (vm.job_state === 'running') {
          jobStateClass = 'vm-state-running';
        } else if (vm.job_state === 'failing' || vm.job_state === 'unresponsive') {
          jobStateClass = 'vm-state-error';
        } else if (vm.job_state === 'stopped') {
          jobStateClass = 'vm-state-stopped';
        }

        return `
          <tr>
            <td class="vm-instance">${instanceName}</td>
            <td class="vm-state ${stateClass}">${vm.state || '-'}</td>
            <td class="vm-job-state ${jobStateClass}">${vm.job_state || '-'}</td>
            <td class="vm-az">${vm.az || '-'}</td>
            <td class="vm-type">${vmType}</td>
            <td class="vm-active">${active}</td>
            <td class="vm-bootstrap">${bootstrap}</td>
            <td class="vm-ips">${ips}</td>
            <td class="vm-dns">${dns}</td>
            <td class="vm-cid">${vm.vm_cid || '-'}</td>
            <td class="vm-agent-id">${agentId}</td>
            <td class="vm-created-at">${vmCreatedAt}</td>
            <td class="vm-disk-cids">${diskCids}</td>
            <td class="vm-resurrection">${resurrection}</td>
          </tr>
        `;
      }).join('');
    } else if (tableClass === 'deployment-log-table' || tableClass === 'debug-log-table') {
      tbody.innerHTML = data.map(log => {
        const time = formatTimestamp(log.time);
        const tags = log.tags && log.tags.length > 0 ? log.tags.join(', ') : '-';
        let status = '-';
        if (log.data && log.data.status) {
          status = log.data.status;
        }

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
      }).join('');
    } else if (tableClass.startsWith('instance-logs-table-')) {
      // Instance logs tables use the same format as regular logs
      tbody.innerHTML = data.map(row => renderLogRow(row)).join('');
    }
  };

  // Initialize filtering on log tables
  const initializeFiltering = (tableClass) => {
    const table = document.querySelector(`.${tableClass}`);
    if (!table) return;

    const container = table.closest('.logs-container, .log-table-container');
    if (!container) return;

    // Check if filter already exists
    if (container.querySelector('.filter-container')) return;

    // Find header element
    const header = container.querySelector('.logs-header, h3');
    if (!header) return;

    // Create filter container
    const filterContainer = createFilterInput(tableClass);

    // Insert before copy button or append to header
    const copyBtn = container.querySelector('.copy-btn-logs');
    if (copyBtn) {
      copyBtn.parentElement.insertBefore(filterContainer, copyBtn);
    } else if (header.parentElement === container) {
      header.appendChild(filterContainer);
    } else {
      container.insertBefore(filterContainer, container.firstChild);
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

  // Helper function to safely format timestamps
  const formatTimestamp = (timestamp) => {
    if (!timestamp && timestamp !== 0) {
      return '-';
    }

    try {
      let date;

      if (typeof timestamp === 'number') {
        // Handle Unix timestamps - if it's a reasonable Unix timestamp (after year 2000)
        // then multiply by 1000 to convert to milliseconds
        if (timestamp > 946684800) { // Jan 1, 2000 in Unix seconds
          date = new Date(timestamp * 1000);
        } else {
          // Already in milliseconds or invalid
          date = new Date(timestamp);
        }
      } else if (typeof timestamp === 'string') {
        // Handle string timestamps (ISO format, etc.)
        date = new Date(timestamp);
      } else {
        // Handle other formats
        date = new Date(timestamp);
      }

      // Check if the date is valid
      if (isNaN(date.getTime())) {
        return '-';
      }

      return date.toLocaleString();
    } catch (error) {
      console.warn('Error formatting timestamp:', timestamp, error);
      return '-';
    }
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
          ${data.boshDNS ? `
          <tr>
            <td class="info-key">BOSH DNS</td>
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${data.boshDNS}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${data.boshDNS}</span>
              </span>
            </td>
          </tr>
          ` : ''}
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
        <button class="detail-tab" data-tab="config">Config</button>
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
      if (!service || !service.plans) return;
      service.plans.forEach(plan => {
        if (!plan) return;
        plansList.push({
          service: service,
          plan: plan,
          id: `${service.name || service.id || 'unknown'}-${plan.name || plan.id || 'unknown'}`
        });
      });
    });

    const listHtml = plansList.map(item => `
      <div class="plan-item" data-plan-id="${item.id}">
        <div class="plan-name">${item.service.name || item.service.id || 'unknown'} / ${item.plan.name || item.plan.id || 'unknown'}</div>
        <div class="plan-meta">
          Instances: ${item.plan.blacksmith?.instances || 0} / ${item.plan.blacksmith?.limit > 0 ? item.plan.blacksmith.limit : item.plan.blacksmith?.limit == 0 ? '∞' : '‑'}
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
    const planName = `${service?.name || service?.id || 'unknown'} / ${plan?.name || plan?.id || 'unknown'}`;

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
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${service?.name || service?.id || 'unknown'}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${service?.name || service?.id || 'unknown'}</span>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key">Plan</td>
            <td class="info-value">
              <span class="copy-wrapper">
                <button class="copy-btn-inline" onclick="window.copyValue(event, '${plan?.name || plan?.id || 'unknown'}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                <span>${plan?.name || plan?.id || 'unknown'}</span>
              </span>
            </td>
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
        <div class="filter-row">
          <label for="service-filter">Service:</label>
          <select id="service-filter" class="filter-select">
            <option value="">All Services</option>
            ${serviceOptions}
          </select>
        </div>
        <div class="filter-row">
          <label for="plan-filter">Plan:</label>
          <select id="plan-filter" class="filter-select" disabled>
            <option value="">All Plans</option>
          </select>
        </div>
        <div class="filter-buttons">
          <button id="refresh-services" class="copy-deployment-names-btn" title="Refresh Service Instances">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="m3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            <span></span>
          </button>
          <button id="copy-deployment-names" class="copy-deployment-names-btn" title="Copy Service Instance Deployment Names">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Deployments</span>
          </button>
          <button id="clear-filters" class="clear-filters-btn">Clear</button>
        </div>
        <div class="filter-status">
          <span id="filter-count">Showing ${instancesList.length} of ${instancesList.length} instances</span>
        </div>
      </div>
    `;

    const listHtml = instancesList.length === 0
      ? '<div class="no-data">No services have been provisioned yet.</div>'
      : instancesList.map(([id, details]) => {
        const vmStatusHtml = details.vm_status ? `
            <div class="vm-status-badge vm-status-${details.vm_status}" title="VM Status: ${details.vm_status}${details.vm_count ? ` (${details.vm_healthy || 0}/${details.vm_count} healthy)` : ''}" onclick="selectInstance('${id}', 'vms')">
              <span class="vm-status-icon"></span>
              <span class="vm-status-text">${details.vm_status}</span>
              ${details.vm_count ? `<span class="vm-count">${details.vm_healthy || 0}/${details.vm_count}</span>` : ''}
            </div>
          ` : '';

        // Check if instance is marked for deletion
        const deletionStatusHtml = details.deletion_in_progress || details.status === 'deprovision_requested' ? `
            <div class="deletion-status-badge" title="Delete requested at ${details.delete_requested_at || details.deprovision_requested_at || 'Unknown'}">
              <span class="deletion-status-icon"></span>
              <span class="deletion-status-text">Deleting...</span>
            </div>
          ` : '';

        const isDeleting = details.deletion_in_progress || details.status === 'deprovision_requested';

        return `
            <div class="service-item ${isDeleting ? 'deleting' : ''}" data-instance-id="${id}" data-service="${details.service_id}" data-plan="${details.plan?.name || ''}">
              <div class="service-id">${id}</div>
              ${details.instance_name ? `<div class="service-instance-name">${details.instance_name}</div>` : ''}
              <div class="service-meta">
                ${details.service_id} / ${details.plan?.name || details.plan_id || 'unknown'} @ ${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}
              </div>
              ${deletionStatusHtml}
              ${vmStatusHtml}
            </div>
          `;
      }).join('');

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
    const deploymentName = vaultData?.deployment_name || `${details.service_id}-${details.plan?.name || details.plan_id || 'unknown'}-${id}`;

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
        { key: 'requested_at', label: 'Requested At' },
        { key: 'delete_requested_at', label: 'Delete Requested At' },
        { key: 'deprovision_requested_at', label: 'Deprovision Requested At' }
      ];

      // Add rows for known fields in order
      fieldOrder.forEach(field => {
        if (vaultData[field.key] !== undefined) {
          let value = vaultData[field.key];
          // Format timestamp fields
          if ((field.key === 'requested_at' || field.key === 'delete_requested_at' || field.key === 'deprovision_requested_at') && value) {
            value = formatTimestamp(value);
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
              <button class="copy-btn-inline" onclick="window.copyValue(event, '${details.plan?.name || details.plan_id || 'unknown'}')"
                      title="Copy Plan">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>${details.plan?.name || details.plan_id || 'unknown'}</span>
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
        <h3 class="deployment-name">
          <button class="copy-btn-inline deployment-name-copy" onclick="window.copyValue(event, '${deploymentName.replace(/'/g, "\\'")}')"
                  title="Copy deployment name to clipboard">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
          ${deploymentName}
        </h3>
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
        <button class="detail-tab" data-tab="testing">Testing</button>
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

  // Format config as a hierarchical table similar to credentials
  const formatConfig = (config) => {
    if (!config || Object.keys(config).length === 0) {
      return '<div class="no-data">No configuration available</div>';
    }

    let html = '<div class="config-container">';
    html += `
      <table class="config-table">
        <thead>
          <tr>
            <th>Property</th>
            <th>Value</th>
          </tr>
        </thead>
        <tbody>
    `;

    // Recursive function to flatten nested objects with hierarchical keys
    const flattenConfig = (obj, prefix = '') => {
      const result = [];

      for (const [key, value] of Object.entries(obj)) {
        const fullKey = prefix ? `${prefix}.${key}` : key;

        if (value === null || value === undefined) {
          result.push({ key: fullKey, value: '' });
        } else if (typeof value === 'object' && !Array.isArray(value)) {
          // Recursively flatten nested objects
          result.push(...flattenConfig(value, fullKey));
        } else if (Array.isArray(value)) {
          // Handle arrays - display as JSON string
          result.push({ key: fullKey, value: JSON.stringify(value) });
        } else {
          // Handle primitive values
          result.push({ key: fullKey, value: value.toString() });
        }
      }

      return result;
    };

    // Flatten the config and create table rows
    const flatConfig = flattenConfig(config);

    for (const item of flatConfig) {
      let displayValue;
      if (item.value === '' || item.value === null || item.value === undefined) {
        displayValue = '<em>empty</em>';
      } else if (item.value.startsWith('[') || item.value.startsWith('{')) {
        // Format JSON strings with code styling
        try {
          const parsed = JSON.parse(item.value);
          displayValue = `<code>${JSON.stringify(parsed, null, 2)}</code>`;
        } catch {
          displayValue = `<code>${item.value}</code>`;
        }
      } else if (item.value === 'true' || item.value === 'false') {
        // Boolean values
        displayValue = `<code>${item.value}</code>`;
      } else if (!isNaN(item.value) && item.value !== '') {
        // Numeric values
        displayValue = `<code>${item.value}</code>`;
      } else {
        // String values
        displayValue = `<code>${item.value}</code>`;
      }

      const copyValue = item.value || '';
      html += `
        <tr>
          <td class="config-key">${item.key}</td>
          <td class="config-value">
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
        // Fetch manifest details for blacksmith deployment
        const response = await fetch(`/b/deployments/${deploymentName}/manifest-details`, { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const manifestData = await response.json();

        return formatManifestDetails(manifestData, `blacksmith-${deploymentName}`);

      } else if (type === 'credentials') {
        // Fetch blacksmith credentials
        const response = await fetch('/b/blacksmith/credentials');
        if (!response.ok) {
          throw new Error(`Failed to load credentials: ${response.statusText}`);
        }
        const creds = await response.json();
        return formatCredentials(creds);
      } else if (type === 'config') {
        // Fetch blacksmith configuration
        const response = await fetch('/b/blacksmith/config');
        if (!response.ok) {
          throw new Error(`Failed to load config: ${response.statusText}`);
        }
        const config = await response.json();
        return formatConfig(config);
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

    // Handle special cases that don't use endpoints
    if (type === 'testing') {
      return await renderServiceTesting(instanceId);
    }

    const endpoints = {
      manifest: `/b/${instanceId}/manifest-details`,
      credentials: `/b/${instanceId}/creds.json`,  // Use JSON endpoint
      events: `/b/${instanceId}/events`,
      vms: `/b/${instanceId}/vms`,
      logs: `/b/${instanceId}/task/log`,
      debug: `/b/${instanceId}/task/debug`,
      'instance-logs': `/b/${instanceId}/instance-logs`,
      config: `/b/${instanceId}/config`
    };

    // Check if the endpoint exists
    if (!endpoints[type]) {
      return `<div class="error">Unknown tab type: ${type}</div>`;
    }

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
          return formatEvents(events, 'service-events', instanceId);
        } catch (e) {
          return `<pre>${text}</pre>`;
        }
      } else if (type === 'vms') {
        const text = await response.text();
        try {
          const vms = JSON.parse(text);
          return formatVMs(vms, instanceId);
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
          return formatDeploymentLog(logs, instanceId);
        } catch (e) {
          // If not JSON, display as plain text
          return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        }
      } else if (type === 'debug') {
        const text = await response.text();
        try {
          const logs = JSON.parse(text);
          return formatDebugLog(logs, instanceId);
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
      } else if (type === 'config') {
        const text = await response.text();
        try {
          const config = JSON.parse(text);
          return formatConfig(config);
        } catch (e) {
          // If not JSON, display as plain text
          return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        }
      }

      // Handle manifest separately since it returns JSON
      if (type === 'manifest') {
        const manifestData = await response.json();
        return formatManifestDetails(manifestData, instanceId);
      }

      const text = await response.text();
      return `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
    } catch (error) {
      return `<div class="error">Failed to load ${type}: ${error.message}</div>`;
    }
  };

  const formatDeploymentLog = (logs, instanceId = null) => {
    if (!logs || logs.length === 0) {
      return '<div class="no-data">No deployment logs available</div>';
    }

    // Store original data for sorting
    tableOriginalData.set('deployment-logs', [...logs]);

    // Determine the refresh function based on whether this is for blacksmith or service instance
    const refreshFunction = instanceId
      ? `window.refreshServiceInstanceDeploymentLog('${instanceId}', event)`
      : `window.refreshBlacksmithDeploymentLog(event)`;

    return `
      <div class="deployment-log-wrapper">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter('deployment-log-table', 'Search deployment logs...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.deployment-log-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Copy</span>
          </button>
          <button class="refresh-btn-logs" onclick="${refreshFunction}"
                  title="Refresh deployment logs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            <span>Refresh</span>
          </button>
        </div>
        <div class="deployment-log-table-container">
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
      const time = formatTimestamp(log.time);
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
      </div>
    `;
  };

  const formatDebugLog = (logs, instanceId = null) => {
    if (!logs || logs.length === 0) {
      return '<div class="no-data">No debug logs available</div>';
    }

    // Store original data for sorting
    tableOriginalData.set('debug-logs', [...logs]);

    // Determine the refresh function based on whether this is for blacksmith or service instance
    const refreshFunction = instanceId
      ? `window.refreshServiceInstanceDebugLog('${instanceId}', event)`
      : `window.refreshBlacksmithDebugLog(event)`;

    return `
      <div class="debug-log-wrapper">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter('debug-log-table', 'Search debug logs...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.debug-log-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Copy</span>
          </button>
          <button class="refresh-btn-logs" onclick="${refreshFunction}"
                  title="Refresh debug logs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            <span>Refresh</span>
          </button>
        </div>
        <div class="debug-log-table-container">
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
      const time = formatTimestamp(log.time);
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
      // Parse and render as table even for single logs
      const parsedLogs = typeof logs === 'string'
        ? logs.split('\n').filter(line => line.trim()).map(line => parseLogLine(line))
        : [];

      // Store for sorting
      const tableKey = `instance-logs-${jobKey}`;
      tableOriginalData.set(tableKey, parsedLogs);

      // Store original text for copy
      if (!window.instanceLogOriginalText) window.instanceLogOriginalText = {};
      window.instanceLogOriginalText[jobKey] = typeof logs === 'string' ? logs : JSON.stringify(logs, null, 2);

      return `
        <div class="job-logs-container">
          <div class="logs-table-container" id="log-display-${jobKey.replace(/\//g, '-')}">
            <table class="instance-logs-table instance-logs-table-${jobKey.replace(/\//g, '-')}" data-job="${jobKey}">
              <thead>
                <tr class="table-controls-row">
                  <th colspan="4" class="table-controls-header">
                    <div class="table-controls-container">
                      <div class="search-filter-container">
                        ${createSearchFilter(`instance-logs-table-${jobKey.replace(/\//g, '-')}`, 'Search logs...')}
                      </div>
                      <button class="copy-btn-logs" onclick="window.copyInstanceLogs('${jobKey}', event)"
                              title="Copy filtered table rows">
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        <span>Copy</span>
                      </button>
                      <button class="refresh-btn-logs" onclick="window.refreshInstanceLogs('${jobKey}', event)"
                              title="Refresh logs">
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
                        <span>Refresh</span>
                      </button>
                    </div>
                  </th>
                </tr>
                <tr>
                  <th class="log-col-date">Date</th>
                  <th class="log-col-time">Time</th>
                  <th class="log-col-level">Level</th>
                  <th class="log-col-message">Message</th>
                </tr>
              </thead>
              <tbody>
                ${parsedLogs.map(row => renderLogRow(row)).join('')}
              </tbody>
            </table>
          </div>
        </div>
      `;
    }

    // Create layout with dropdown selector and content
    const filesList = Object.keys(files);

    // Get smart default file selection
    const instanceInfo = window.currentInstanceInfo || {};
    const defaultFile = LogSelectionManager.getSmartDefault(
      filesList,
      instanceInfo.service,
      instanceInfo.plan,
      instanceInfo.id,
      jobKey
    );

    // Store files for this job
    if (!window.instanceLogFiles) window.instanceLogFiles = {};
    window.instanceLogFiles[jobKey] = files;

    // Store original text for copy - use the selected default file
    if (!window.instanceLogOriginalText) window.instanceLogOriginalText = {};
    window.instanceLogOriginalText[jobKey] = files[defaultFile] || 'No content';

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

      // Mark the selected file
      const selected = filename === defaultFile ? 'selected' : '';
      return `<option value="${escapedFilename}" ${selected} title="${escapedFilename}">${escapedDisplay}</option>`;
    }).join('');

    // Parse logs for the selected default file
    const logContent = files[defaultFile] || 'No content';
    const parsedLogs = logContent.split('\n').filter(line => line.trim()).map(line => parseLogLine(line));

    // Store for sorting
    const tableKey = `instance-logs-${jobKey}`;
    tableOriginalData.set(tableKey, parsedLogs);

    return `
      <div class="job-logs-container">
        <div class="logs-table-container" id="log-display-${jobKey.replace(/\//g, '-')}">
          <table class="instance-logs-table instance-logs-table-${jobKey.replace(/\//g, '-')}" data-job="${jobKey}">
            <thead>
              <tr class="table-controls-row">
                <th colspan="4" class="table-controls-header">
                  <div class="table-controls-container">
                    <div class="search-filter-container">
                      ${createSearchFilter(`instance-logs-table-${jobKey.replace(/\//g, '-')}`, 'Search logs...')}
                    </div>
                    <div class="log-file-selector">
                      <select id="log-select-${jobKey.replace(/\//g, '-')}"
                              class="log-file-dropdown"
                              onchange="window.selectLogFileForJob('${jobKey}', this.value)">
                        ${fileOptions}
                      </select>
                    </div>
                    <button class="copy-btn-logs" onclick="window.copyInstanceLogs('${jobKey}', event)"
                            title="Copy filtered table rows">
                      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      <span>Copy</span>
                    </button>
                    <button class="refresh-btn-logs" onclick="window.refreshInstanceLogs('${jobKey}', event)"
                            title="Refresh logs">
                      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
                      <span>Refresh</span>
                    </button>
                  </div>
                </th>
              </tr>
              <tr>
                <th class="log-col-date">Date</th>
                <th class="log-col-time">Time</th>
                <th class="log-col-level">Level</th>
                <th class="log-col-message">Message</th>
              </tr>
            </thead>
            <tbody>
              ${parsedLogs.map(row => renderLogRow(row)).join('')}
            </tbody>
          </table>
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
      // Re-initialize sorting and filtering for the new job table
      setTimeout(() => {
        const tableClass = `instance-logs-table-${job.replace(/\//g, '-')}`;
        initializeSorting(tableClass);
        attachSearchFilter(tableClass);
      }, 100);
    }
  };

  // Handler for log file selection from dropdown
  window.selectLogFileForJob = (job, filename) => {
    const files = window.instanceLogFiles && window.instanceLogFiles[job];
    if (!files || !files[filename]) return;

    // Save the selected log file to localStorage
    const instanceInfo = window.currentInstanceInfo || {};
    if (instanceInfo.id) {
      LogSelectionManager.saveInstanceLog(instanceInfo.id, job, filename);
    }

    // Parse the selected log file
    const logContent = files[filename] || 'No content';
    const parsedLogs = logContent.split('\n').filter(line => line.trim()).map(line => parseLogLine(line));

    // Store for sorting
    const tableKey = `instance-logs-${job}`;
    tableOriginalData.set(tableKey, parsedLogs);

    // Store original text for copy
    if (!window.instanceLogOriginalText) window.instanceLogOriginalText = {};
    window.instanceLogOriginalText[job] = logContent;

    // Update table body
    const tableEl = document.querySelector(`#log-display-${job.replace(/\//g, '-')} tbody`);
    if (tableEl) {
      tableEl.innerHTML = parsedLogs.map(row => renderLogRow(row)).join('');

      // Re-initialize sorting and filtering
      const tableClass = `instance-logs-table-${job.replace(/\//g, '-')}`;
      initializeSorting(tableClass);
      attachSearchFilter(tableClass);
    }
  };

  // Copy instance logs to clipboard - copies the original raw text
  window.copyInstanceLogs = async (jobKey, event) => {
    const button = event.currentTarget;

    // Get the original log text - moved outside try block to be accessible in catch
    const originalText = window.instanceLogOriginalText && window.instanceLogOriginalText[jobKey];
    if (!originalText) {
      console.error('No original log text found for job:', jobKey);
      return;
    }

    try {
      // Copy to clipboard
      await navigator.clipboard.writeText(originalText);

      // Visual feedback
      button.classList.add('copied');
      const originalTitle = button.title;
      button.title = 'Copied!';
      const spanElement = button.querySelector('span');
      const originalButtonText = spanElement ? spanElement.textContent : '';
      if (spanElement) {
        spanElement.textContent = 'Copied!';
      }
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
        if (spanElement) {
          spanElement.textContent = originalButtonText;
        }
      }, 2000);
    } catch (err) {
      console.error('Failed to copy logs:', err);
      // Fallback for older browsers
      const textarea = document.createElement('textarea');
      textarea.value = originalText;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      try {
        document.execCommand('copy');
        button.classList.add('copied');
        const spanElement = button.querySelector('span');
        const originalButtonText = spanElement ? spanElement.textContent : '';
        if (spanElement) {
          spanElement.textContent = 'Copied!';
        }
        setTimeout(() => {
          button.classList.remove('copied');
          if (spanElement) {
            spanElement.textContent = originalButtonText;
          }
        }, 2000);
      } catch (err) {
        console.error('Failed to copy using fallback:', err);
      } finally {
        document.body.removeChild(textarea);
      }
    }
  };

  // Refresh instance logs - re-fetches logs from server
  window.refreshInstanceLogs = async (jobKey, event) => {
    const button = event.currentTarget;

    // Get the current instance ID from the active service instance
    const activeInstanceItem = document.querySelector('.service-instance-item.active');
    if (!activeInstanceItem) {
      console.error('No active service instance selected');
      return;
    }

    const instanceId = activeInstanceItem.dataset.id;
    if (!instanceId) {
      console.error('No instance ID found');
      return;
    }

    // Capture current search filter state
    const tableId = `instance-logs-table-${jobKey.replace(/\//g, '-')}`;
    const currentSearchFilter = captureSearchFilterState(tableId);

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Fetch fresh logs data
      const response = await fetch(`/b/${instanceId}/instance-logs`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();
      const logsData = JSON.parse(text);

      // Update the global logs data
      window.instanceLogsData = logsData;

      // Check if the current job still exists in the new data
      if (!logsData[jobKey]) {
        console.error('Job not found in refreshed data:', jobKey);
        return;
      }

      // Update the files for this job
      const jobData = logsData[jobKey];
      const files = jobData.files || {};

      if (Object.keys(files).length === 0) {
        // Handle case with no files
        const logs = jobData.logs || 'No logs available';

        // Store original text
        if (!window.instanceLogOriginalText) window.instanceLogOriginalText = {};
        window.instanceLogOriginalText[jobKey] = typeof logs === 'string' ? logs : JSON.stringify(logs, null, 2);

        // Parse and update table
        const parsedLogs = typeof logs === 'string'
          ? logs.split('\n').filter(line => line.trim()).map(line => parseLogLine(line))
          : [];

        const tableKey = `instance-logs-${jobKey}`;
        tableOriginalData.set(tableKey, parsedLogs);

        // Update table body
        const tableEl = document.querySelector(`#log-display-${jobKey.replace(/\//g, '-')} tbody`);
        if (tableEl) {
          tableEl.innerHTML = parsedLogs.map(row => renderLogRow(row)).join('');
          // Restore search filter state after updating table
          setTimeout(() => {
            restoreSearchFilterState(tableId, currentSearchFilter);
          }, 100);
        }
      } else {
        // Update files data
        if (!window.instanceLogFiles) window.instanceLogFiles = {};
        window.instanceLogFiles[jobKey] = files;

        // Get currently selected file from dropdown
        const dropdownEl = document.getElementById(`log-select-${jobKey.replace(/\//g, '-')}`);
        const currentFile = dropdownEl ? dropdownEl.value : Object.keys(files)[0];

        // Use the selected file or first file
        const fileToShow = files[currentFile] ? currentFile : Object.keys(files)[0];
        const logContent = files[fileToShow] || 'No content';

        // Store original text
        if (!window.instanceLogOriginalText) window.instanceLogOriginalText = {};
        window.instanceLogOriginalText[jobKey] = logContent;

        // Parse and update table
        const parsedLogs = logContent.split('\n').filter(line => line.trim()).map(line => parseLogLine(line));

        const tableKey = `instance-logs-${jobKey}`;
        tableOriginalData.set(tableKey, parsedLogs);

        // Update table body
        const tableEl = document.querySelector(`#log-display-${jobKey.replace(/\//g, '-')} tbody`);
        if (tableEl) {
          tableEl.innerHTML = parsedLogs.map(row => renderLogRow(row)).join('');
          // Restore search filter state after updating table
          setTimeout(() => {
            restoreSearchFilterState(tableId, currentSearchFilter);
          }, 100);
        }
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalButtonText = spanElement ? spanElement.textContent : '';
      if (spanElement) {
        spanElement.textContent = 'Refreshed!';
      }
      setTimeout(() => {
        button.classList.remove('success');
        if (spanElement) {
          spanElement.textContent = originalButtonText;
        }
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh instance logs:', error);

      // Visual feedback for error
      button.classList.add('error');
      const spanElement = button.querySelector('span');
      const originalButtonText = spanElement ? spanElement.textContent : '';
      if (spanElement) {
        spanElement.textContent = 'Error';
      }
      setTimeout(() => {
        button.classList.remove('error');
        if (spanElement) {
          spanElement.textContent = originalButtonText;
        }
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  const formatEvents = (events, dataKey = 'events', instanceId = null) => {
    if (!events || events.length === 0) {
      return '<div class="no-data">No events recorded</div>';
    }

    // Store original data for sorting with appropriate key
    tableOriginalData.set(dataKey, [...events]);

    // Add unique identifier for this table instance
    const tableId = `events-table-${dataKey}`;

    // Determine the refresh function based on whether this is for blacksmith or service instance
    const refreshFunction = instanceId
      ? `window.refreshServiceInstanceEvents('${instanceId}', event)`
      : `window.refreshBlacksmithEvents(event)`;

    return `
      <div class="table-controls-container">
        <div class="search-filter-container">
          ${createSearchFilter(tableId, 'Search events...')}
        </div>
        <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableId}', event)"
                title="Copy filtered table rows">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          <span>Copy</span>
        </button>
        <button class="refresh-btn-logs" onclick="${refreshFunction}"
                title="Refresh events">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
          <span>Refresh</span>
        </button>
      </div>
      <div class="events-table-container">
        <table class="${tableId} events-table">
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
      const time = formatTimestamp(event.time);
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
      </div>
    `;
  };

  // Log parsing and rendering functions
  const parseLogLine = (line) => {
    // Try different log formats in order of specificity

    // JSON format - check first as it's easy to detect
    if (line.startsWith('{') && line.endsWith('}')) {
      try {
        const json = JSON.parse(line);

        // Extract timestamp and convert if needed
        let date = '';
        let time = '';
        if (json.timestamp) {
          // Check if it's Unix timestamp (numeric or string of numbers with optional decimal)
          if (/^\d+(\.\d+)?$/.test(json.timestamp.toString())) {
            const ts = new Date(parseFloat(json.timestamp) * 1000);
            date = ts.toISOString().split('T')[0];
            time = ts.toISOString().split('T')[1].replace('Z', '');
          } else if (json.timestamp.includes('T')) {
            // ISO format
            const parts = json.timestamp.split('T');
            date = parts[0];
            time = parts[1].replace('Z', '').split('+')[0].split('-')[0];
          }
        }

        // Extract level (could be level, log_level, severity, etc.)
        let level = json.level || json.log_level || json.severity || 'INFO';
        if (typeof level === 'number') {
          // Convert numeric levels (0=debug, 1=info, 2=warn, 3=error, 4=fatal)
          const levelMap = ['DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL'];
          level = levelMap[level] || 'INFO';
        }
        level = level.toString().toUpperCase();

        // Build message from various fields
        let message = json.message || json.msg || '';
        if (json.source) {
          message = `[${json.source}] ${message}`;
        }
        if (json.data) {
          // Append data as formatted JSON
          message += ' ' + JSON.stringify(json.data);
        }

        return {
          date: date,
          time: time,
          level: level,
          message: message
        };
      } catch (e) {
        // If JSON parsing fails, continue to other formats
      }
    }

    // Prometheus/Go-kit format: ts=TIMESTAMP caller=file:line level=LEVEL key=value...
    const prometheusPattern = /^ts=(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}\.\d+)Z?\s+(.*)$/;
    let match = line.match(prometheusPattern);
    if (match) {
      // Parse key=value pairs
      const kvPairs = match[3];
      const pairs = {};

      // Match key=value or key="quoted value"
      const kvPattern = /(\w+)=("(?:[^"\\]|\\.)*"|[^\s]+)/g;
      let kvMatch;
      while ((kvMatch = kvPattern.exec(kvPairs)) !== null) {
        const key = kvMatch[1];
        let value = kvMatch[2];
        // Remove quotes if present
        if (value.startsWith('"') && value.endsWith('"')) {
          value = value.slice(1, -1);
        }
        pairs[key] = value;
      }

      const level = (pairs.level || 'info').toUpperCase();

      // Build message from remaining key-value pairs
      let message = '';
      if (pairs.msg) {
        message = pairs.msg;
      }
      if (pairs.caller) {
        message = `[${pairs.caller}] ${message}`;
      }
      if (pairs.collector) {
        message = `[collector:${pairs.collector}] ${message}`;
      }

      // Add other important fields
      for (const [key, value] of Object.entries(pairs)) {
        if (!['ts', 'level', 'msg', 'caller', 'collector'].includes(key)) {
          message += ` ${key}=${value}`;
        }
      }

      return {
        date: match[1],
        time: match[2],
        level: level,
        message: message.trim()
      };
    }

    // Component-prefixed format: [Component] YYYY-MM-DDTHH:MM:SS.mmmZ LEVEL - message
    const componentPattern = /^\[([^\]]+)\]\s+(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}\.\d+)Z?\s+(\w+)\s+-\s+(.*)$/;
    match = line.match(componentPattern);
    if (match) {
      return {
        date: match[2],
        time: match[3],
        level: match[4].toUpperCase(),
        message: `[${match[1]}] ${match[5]}`
      };
    }

    // Bracketed timestamp with path: [YYYY-MM-DDTHH:MM:SS.mmmZ] path message
    const bracketTimestampPattern = /^\[(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}\.\d+)Z?\]\s+(.*)$/;
    match = line.match(bracketTimestampPattern);
    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: 'INFO',
        message: match[3]
      };
    }

    // RabbitMQ format: YYYY-MM-DD HH:MM:SS.mmm+TZ [level] <pid> message
    const rabbitMQPattern = /^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2}\.\d+[+-]\d{2}:\d{2})\s+\[(\w+)\]\s+<[^>]+>\s+(.*)$/;
    match = line.match(rabbitMQPattern);
    if (match) {
      // Extract just the time portion without timezone for consistency
      const timeMatch = match[2].match(/(\d{2}:\d{2}:\d{2}\.\d+)/);
      return {
        date: match[1],
        time: timeMatch ? timeMatch[1] : match[2].split('+')[0].split('-')[0],
        level: match[3].toUpperCase(),
        message: match[4]
      };
    }

    // Redis format: PID:TYPE DD Mon YYYY HH:MM:SS.mmm # message
    const redisPattern = /^(\d+):([A-Z])\s+(\d{2})\s+(\w{3})\s+(\d{4})\s+(\d{2}:\d{2}:\d{2}\.\d+)\s+([#*-])\s+(.*)$/;
    match = line.match(redisPattern);
    if (match) {
      // Convert Redis type to level
      const typeToLevel = {
        'C': 'CONFIG',
        'M': 'MASTER',
        'S': 'SLAVE',
        'X': 'SENTINEL',
        'N': 'NO_TYPE'
      };
      const level = typeToLevel[match[2]] || 'INFO';

      // Convert month abbreviation to number
      const months = {
        'Jan': '01', 'Feb': '02', 'Mar': '03', 'Apr': '04',
        'May': '05', 'Jun': '06', 'Jul': '07', 'Aug': '08',
        'Sep': '09', 'Oct': '10', 'Nov': '11', 'Dec': '12'
      };
      const month = months[match[4]] || '01';
      const date = `${match[5]}-${month}-${match[3]}`;

      return {
        date: date,
        time: match[6],
        level: level,
        message: `[${match[1]}] ${match[8]}`
      };
    }

    // PostgreSQL format: YYYY-MM-DD HH:MM:SS.mmm TZ [PID] LEVEL: message
    const postgresPattern = /^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2}\.\d+)\s+\w+\s+\[\d+\]\s+(\w+):\s+(.*)$/;
    match = line.match(postgresPattern);
    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: match[3].toUpperCase(),
        message: match[4]
      };
    }

    // MySQL/MariaDB format: YYYY-MM-DD HH:MM:SS PID [Level] message
    const mysqlPattern = /^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})\s+\d+\s+\[(\w+)\]\s+(.*)$/;
    match = line.match(mysqlPattern);
    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: match[3].toUpperCase(),
        message: match[4]
      };
    }

    // MongoDB format: YYYY-MM-DDTHH:MM:SS.mmm+TZ LEVEL [component] message
    const mongoPattern = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}\.\d+)[+-]\d{4}\s+([A-Z])\s+(\w+)\s+\[([^\]]+)\]\s+(.*)$/;
    match = line.match(mongoPattern);
    if (match) {
      const levelMap = {
        'F': 'FATAL',
        'E': 'ERROR',
        'W': 'WARNING',
        'I': 'INFO',
        'D': 'DEBUG'
      };
      return {
        date: match[1],
        time: match[2],
        level: levelMap[match[3]] || match[3],
        message: `[${match[5]}] ${match[6]}`
      };
    }

    // Original Blacksmith format: YYYY-MM-DD HH:MM:SS.mmm LEVEL [context] message
    const blacksmithPattern = /^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2}\.\d{3})\s+(\w+)\s+(.*)$/;
    match = line.match(blacksmithPattern);
    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: match[3].trim().toUpperCase(),
        message: match[4]
      };
    }

    // Syslog-style format: Mon DD HH:MM:SS hostname process[pid]: message
    const syslogPattern = /^(\w{3})\s+(\d{1,2})\s+(\d{2}:\d{2}:\d{2})\s+\S+\s+([^[]+)(?:\[\d+\])?: (.*)$/;
    match = line.match(syslogPattern);
    if (match) {
      const months = {
        'Jan': '01', 'Feb': '02', 'Mar': '03', 'Apr': '04',
        'May': '05', 'Jun': '06', 'Jul': '07', 'Aug': '08',
        'Sep': '09', 'Oct': '10', 'Nov': '11', 'Dec': '12'
      };
      const currentYear = new Date().getFullYear();
      const month = months[match[1]] || '01';
      const day = match[2].padStart(2, '0');
      const date = `${currentYear}-${month}-${day}`;

      return {
        date: date,
        time: match[3],
        level: 'INFO',
        message: `[${match[4]}] ${match[5]}`
      };
    }

    // Simple timestamp format: [YYYY-MM-DD HH:MM:SS] message
    const simpleTimestampPattern = /^\[(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})\]\s+(.*)$/;
    match = line.match(simpleTimestampPattern);
    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: 'INFO',
        message: match[3]
      };
    }

    // ISO 8601 format: YYYY-MM-DDTHH:MM:SS.mmmZ message
    const isoPattern = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}(?:\.\d{3})?)[Z+-][\d:]*\s+(.*)$/;
    match = line.match(isoPattern);
    if (match) {
      return {
        date: match[1],
        time: match[2],
        level: 'INFO',
        message: match[3]
      };
    }

    // Handle lines that don't match any pattern (continuation lines, etc.)
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

  const renderLogsTable = (logs, parsedRows = null, includeSearchFilter = true) => {
    // If parsedRows is provided, use it; otherwise parse the logs
    const rows = parsedRows || (typeof logs === 'string'
      ? logs.split('\n').filter(line => line.trim()).map(line => parseLogLine(line))
      : logs);

    const searchFilterHTML = includeSearchFilter ? createSearchFilter('logs-table', 'Search logs...') : '';

    const tableHTML = `
      ${searchFilterHTML}
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

  const formatBlacksmithLogs = (logs, logFile = null) => {
    if (!logs || logs === '') {
      return '<div class="no-data">No logs available</div>';
    }

    // Parse logs and store for sorting
    const parsedLogs = logs.split('\n').filter(line => line.trim()).map(line => parseLogLine(line));
    tableOriginalData.set('blacksmith-logs', parsedLogs);

    // Available log files for selection
    const logFiles = [
      { path: '/var/vcap/sys/log/blacksmith/blacksmith.stdout.log', name: 'Blacksmith stdout' },
      { path: '/var/vcap/sys/log/blacksmith/blacksmith.stderr.log', name: 'Blacksmith stderr' },
      { path: '/var/vcap/sys/log/blacksmith/vault.stdout.log', name: 'Vault stdout' },
      { path: '/var/vcap/sys/log/blacksmith/vault.stderr.log', name: 'Vault stderr' },
      { path: '/var/vcap/sys/log/blacksmith.vault/bpm.log', name: 'Vault BPM log' },
      { path: '/var/vcap/sys/log/blacksmith/bpm.log', name: 'Blacksmith BPM log' },
      { path: '/var/vcap/sys/log/blacksmith/pre-start.stdout.log', name: 'Pre-start stdout' },
      { path: '/var/vcap/sys/log/blacksmith/pre-start.stderr.log', name: 'Pre-start stderr' }
    ];

    // Determine which log file to select
    const selectedLogPath = logFile || LogSelectionManager.getDefaultBlacksmithLog(logFiles);

    const logFileOptions = logFiles.map(file => {
      const selected = file.path === selectedLogPath ? 'selected' : '';
      return `<option value="${file.path}" ${selected}>${file.path}</option>`;
    }).join('');

    return `
      <div class="logs-container">
        <div class="logs-controls-row">
          <div class="search-filter-container">
            ${createSearchFilter('logs-table', 'Search logs...')}
          </div>
          <div class="log-file-selector">
            <select id="blacksmith-log-select" class="log-file-dropdown" onchange="window.selectBlacksmithLogFile(this.value)">
              ${logFileOptions}
            </select>
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.logs-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Copy</span>
          </button>
          <button class="refresh-btn-logs" onclick="window.refreshBlacksmithLogs(event)"
                  title="Refresh logs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            <span>Refresh</span>
          </button>
        </div>
        <div class="logs-table-container" id="blacksmith-logs-display">
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
              ${parsedLogs.map(row => renderLogRow(row)).join('')}
            </tbody>
          </table>
        </div>
      </div>
    `;
  };

  const formatVMs = (vms, instanceId = null) => {
    if (!vms || vms.length === 0) {
      return '<div class="no-data">No VMs available</div>';
    }

    // Store original data for sorting
    tableOriginalData.set('vms', [...vms]);

    // Determine the refresh function based on whether this is for blacksmith or service instance
    const refreshFunction = instanceId
      ? `window.refreshServiceInstanceVMs('${instanceId}', event)`
      : `window.refreshBlacksmithVMs(event)`;

    return `
      <div class="vms-table-wrapper">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter('vms-table', 'Search VMs...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.vms-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Copy</span>
          </button>
          <button class="refresh-btn-logs" onclick="${refreshFunction}"
                  title="Refresh VMs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            <span>Refresh</span>
          </button>
        </div>
        <div class="vms-table-container">
          <table class="vms-table">
        <thead>
          <tr>
            <th>Instance</th>
            <th>VM State</th>
            <th>Job State</th>
            <th>AZ</th>
            <th>VM Type</th>
            <th>Active</th>
            <th>Bootstrap</th>
            <th>IPs</th>
            <th>DNS</th>
            <th>VM CID</th>
            <th>Agent ID</th>
            <th>Created At</th>
            <th>Disk CIDs</th>
            <th>Resurrection</th>
          </tr>
        </thead>
        <tbody>
          ${vms.map(vm => {
      // Use the correct field names from our enhanced VM struct
      const instanceName = vm.job_name && vm.index !== undefined ? `${vm.job_name}/${vm.index}` : vm.id || '-';
      const ips = vm.ips && vm.ips.length > 0 ? vm.ips.join(', ') : '-';
      const dns = vm.dns && vm.dns.length > 0 ? vm.dns.join(', ') : '-';
      const vmType = vm.vm_type || vm.resource_pool || '-';
      const resurrection = vm.resurrection_paused ? 'Paused' : 'Active';
      const agentId = vm.agent_id || '-';
      const vmCreatedAt = vm.vm_created_at ? new Date(vm.vm_created_at).toLocaleString() : '-';
      const active = vm.active !== undefined ? (vm.active ? 'Yes' : 'No') : '-';
      const bootstrap = vm.bootstrap ? 'Yes' : 'No';
      const diskCids = vm.disk_cids && vm.disk_cids.length > 0 ? vm.disk_cids.join(', ') : (vm.disk_cid || '-');

      // Add class based on state
      let stateClass = '';
      if (vm.state === 'running') {
        stateClass = 'vm-state-running';
      } else if (vm.state === 'failing' || vm.state === 'unresponsive') {
        stateClass = 'vm-state-error';
      } else if (vm.state === 'stopped') {
        stateClass = 'vm-state-stopped';
      }

      // Add class based on job_state as well
      let jobStateClass = '';
      if (vm.job_state === 'running') {
        jobStateClass = 'vm-state-running';
      } else if (vm.job_state === 'failing' || vm.job_state === 'unresponsive') {
        jobStateClass = 'vm-state-error';
      } else if (vm.job_state === 'stopped') {
        jobStateClass = 'vm-state-stopped';
      }

      return `
              <tr>
                <td class="vm-instance">${instanceName}</td>
                <td class="vm-state ${stateClass}">${vm.state || '-'}</td>
                <td class="vm-job-state ${jobStateClass}">${vm.job_state || '-'}</td>
                <td class="vm-az">${vm.az || '-'}</td>
                <td class="vm-type">${vmType}</td>
                <td class="vm-active">${active}</td>
                <td class="vm-bootstrap">${bootstrap}</td>
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
                  ${vm.vm_cid ? `
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${vm.vm_cid}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <span>${vm.vm_cid}</span>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-agent-id">
                  ${agentId !== '-' ? `
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${agentId}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <span>${agentId}</span>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-created-at">${vmCreatedAt}</td>
                <td class="vm-disk-cids">
                  ${diskCids !== '-' ? `
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${diskCids}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <span>${diskCids}</span>
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

  // Service Testing Functions
  const renderServiceTesting = async (instanceId) => {
    try {
      // Get both credentials and config/manifest for fallback information
      const [credsResponse, configResponse] = await Promise.all([
        fetch(`/b/${instanceId}/creds.json`, { cache: 'no-cache' }),
        fetch(`/b/${instanceId}/config`, { cache: 'no-cache' }).catch(() => null)
      ]);

      if (!credsResponse.ok) {
        return `<div class="error">Unable to fetch credentials for service testing</div>`;
      }

      const creds = await credsResponse.json();

      // Get config/manifest data for fallbacks if available
      let manifestData = null;
      if (configResponse && configResponse.ok) {
        try {
          const configText = await configResponse.text();
          manifestData = JSON.parse(configText);
        } catch (e) {
          console.warn('Failed to parse config data for fallbacks:', e);
        }
      }

      // Determine service type
      const isRedis = isRedisService(creds);
      const isRabbitMQ = isRabbitMQService(creds);

      if (!isRedis && !isRabbitMQ) {
        return `<div class="no-data">Service testing not available for this service type</div>`;
      }

      const serviceType = isRedis ? 'redis' : 'rabbitmq';

      // Merge credentials with manifest fallbacks
      const enhancedCreds = enhanceCredentialsWithManifestFallbacks(creds, manifestData, serviceType);

      // Store the service type and credentials globally for testing functions
      window.currentTestingInstance = {
        id: instanceId,
        serviceType: serviceType,
        credentials: enhancedCreds,
        manifestData: manifestData
      };

      // Initialize connection state management
      if (!window.serviceConnections) {
        window.serviceConnections = {};
      }

      // Initialize connection state for this instance
      window.serviceConnections[instanceId] = {
        connected: false,
        connectionParams: null,
        connectedAt: null,
        timeout: null
      };

      return renderServiceTestingInterface(instanceId, serviceType, enhancedCreds);
    } catch (error) {
      console.error('Error rendering service testing:', error);
      return `<div class="error">Failed to load service testing: ${error.message}</div>`;
    }
  };

  const isRedisService = (creds) => {
    // Check for Redis-specific patterns
    if (creds.uri && (creds.uri.startsWith('redis://') || creds.uri.startsWith('rediss://'))) {
      return true;
    }
    if (creds.port === 6379 || creds.tls_port === 6380) {
      return true;
    }
    if (creds.password && (creds.host || creds.hostname) && !creds.username) {
      return true; // Redis typically doesn't use username
    }
    return false;
  };

  const isRabbitMQService = (creds) => {
    // Check for RabbitMQ-specific patterns
    if (creds.uri && (creds.uri.startsWith('amqp://') || creds.uri.startsWith('amqps://'))) {
      return true;
    }
    if (creds.protocols && (creds.protocols.amqp || creds.protocols.amqps)) {
      return true;
    }
    if (creds.port === 5672 || creds.port === 5671) {
      return true;
    }
    if (creds.vhost !== undefined || creds.username) {
      return true; // RabbitMQ uses vhost and username
    }
    return false;
  };

  // Enhance credentials with manifest fallback data
  const enhanceCredentialsWithManifestFallbacks = (creds, manifestData, serviceType) => {
    const enhanced = { ...creds };

    if (serviceType === 'rabbitmq' && manifestData) {
      // Look for RabbitMQ-specific manifest data
      const searchManifestValue = (path) => {
        const keys = path.split('.');
        let current = manifestData;
        for (const key of keys) {
          if (current && typeof current === 'object' && current[key] !== undefined) {
            current = current[key];
          } else {
            return null;
          }
        }
        return current;
      };

      // Look for admin_user/admin_password patterns
      if (!enhanced.admin_user) {
        enhanced.admin_user = searchManifestValue('rabbitmq.admin_user') ||
          searchManifestValue('admin_user') ||
          searchManifestValue('properties.rabbitmq.admin_user');
      }

      // Look for username/password patterns
      if (!enhanced.username) {
        enhanced.username = searchManifestValue('rabbitmq.username') ||
          searchManifestValue('username') ||
          searchManifestValue('properties.rabbitmq.username') ||
          enhanced.admin_user;
      }

      // Look for vhost patterns
      if (!enhanced.vhost) {
        enhanced.vhost = searchManifestValue('rabbitmq.vhost') ||
          searchManifestValue('vhost') ||
          searchManifestValue('properties.rabbitmq.vhost');
      }
    }

    return enhanced;
  };

  // Helper function to extract user/vhost info from credentials with fallbacks
  const extractConnectionInfo = (creds, serviceType) => {
    const info = { users: [], vhosts: [] };

    if (serviceType === 'rabbitmq') {
      // Extract users from credentials
      if (creds.username) {
        info.users.push(creds.username);
      }

      // Look for common credential keys for additional users
      Object.keys(creds).forEach(key => {
        if (key.includes('user') && key !== 'username' && creds[key]) {
          info.users.push(creds[key]);
        }
      });

      // Extract admin user from manifest fallback patterns
      if (creds.admin_user) {
        info.users.push(creds.admin_user);
      }

      // Remove duplicates
      info.users = [...new Set(info.users)];

      // Extract vhosts
      if (creds.vhost) {
        info.vhosts.push(creds.vhost);
      }

      // Look for vhost in other credential structures
      Object.keys(creds).forEach(key => {
        if (key.includes('vhost') && creds[key]) {
          info.vhosts.push(creds[key]);
        }
      });

      // Always include '/' as default vhost option
      info.vhosts.push('/');

      // Remove duplicates
      info.vhosts = [...new Set(info.vhosts)];

      // If no users found, add fallback options
      if (info.users.length === 0) {
        info.users = ['admin', 'guest'];
      }

      // If no vhosts found except '/', ensure it's there
      if (info.vhosts.length === 1 && info.vhosts[0] === '/') {
        // Keep just the default
      } else if (info.vhosts.length === 0) {
        info.vhosts = ['/'];
      }
    }

    return info;
  };

  // Generate connection fields based on service type
  const generateConnectionFields = (instanceId, serviceType, creds) => {
    const connectionInfo = extractConnectionInfo(creds, serviceType);

    if (serviceType === 'redis') {
      return `
        <div class="connection-field">
          <label for="connection-type-${instanceId}">Type</label>
          <select id="connection-type-${instanceId}" name="connection-type">
            <option value="standard">Standard</option>
            <option value="tls">TLS</option>
          </select>
        </div>
      `;
    } else if (serviceType === 'rabbitmq') {
      const userOptions = connectionInfo.users.map(user =>
        `<option value="${user}">${user}</option>`
      ).join('');

      const vhostOptions = connectionInfo.vhosts.map(vhost =>
        `<option value="${vhost}">${vhost}</option>`
      ).join('');

      return `
        <div class="connection-field">
          <label for="connection-type-${instanceId}">Type</label>
          <select id="connection-type-${instanceId}" name="connection-type">
            <option value="amqp">AMQP</option>
            <option value="amqps">AMQPS</option>
          </select>
        </div>
        <div class="connection-field">
          <label for="connection-user-${instanceId}">User</label>
          <select id="connection-user-${instanceId}" name="connection-user">
            ${userOptions}
          </select>
        </div>
        <div class="connection-field">
          <label for="connection-vhost-${instanceId}">VHost</label>
          <select id="connection-vhost-${instanceId}" name="connection-vhost">
            ${vhostOptions}
          </select>
        </div>
      `;
    }

    return '';
  };

  const renderServiceTestingInterface = (instanceId, serviceType, creds) => {
    return `
      <div class="service-testing-container">
        <div class="testing-operations">
          <div class="operation-tabs">
            ${getOperationTabs(serviceType, instanceId)}
          </div>

          <div class="operation-content" id="operation-content-${instanceId}">
            ${getDefaultOperationContent(serviceType, instanceId)}
          </div>
        </div>

        <div class="testing-history">
          <div class="history-header">
            <h5>Response History</h5>
            <button class="clear-history-btn" onclick="clearTestingHistory('${instanceId}')">Clear History</button>
          </div>
          <div class="history-entries" id="history-entries-${instanceId}">
            <div class="no-data">No operations performed yet</div>
          </div>
        </div>
      </div>
    `;
  };

  const getOperationTabs = (serviceType, instanceId) => {
    if (serviceType === 'redis') {
      return `
        <button class="operation-tab active" data-operation="test" data-instance="${instanceId}">Connection</button>
        <button class="operation-tab" data-operation="info" data-instance="${instanceId}">INFO</button>
        <button class="operation-tab" data-operation="set" data-instance="${instanceId}">SET</button>
        <button class="operation-tab" data-operation="get" data-instance="${instanceId}">GET</button>
        <button class="operation-tab" data-operation="delete" data-instance="${instanceId}">DELETE</button>
        <button class="operation-tab" data-operation="keys" data-instance="${instanceId}">KEYS</button>
        <button class="operation-tab" data-operation="command" data-instance="${instanceId}">COMMAND</button>
        <button class="operation-tab" data-operation="flush" data-instance="${instanceId}">FLUSH</button>
      `;
    } else if (serviceType === 'rabbitmq') {
      return `
        <button class="operation-tab active" data-operation="test" data-instance="${instanceId}">Connection</button>
        <button class="operation-tab" data-operation="publish" data-instance="${instanceId}">PUBLISH</button>
        <button class="operation-tab" data-operation="consume" data-instance="${instanceId}">CONSUME</button>
        <button class="operation-tab" data-operation="queues" data-instance="${instanceId}">QUEUE INFO</button>
        <button class="operation-tab" data-operation="queue-ops" data-instance="${instanceId}">QUEUE OPS</button>
        <button class="operation-tab" data-operation="management" data-instance="${instanceId}">MANAGEMENT</button>
      `;
    }
    return '';
  };

  const getDefaultOperationContent = (serviceType, instanceId) => {
    if (serviceType === 'redis') {
      return renderRedisTestOperation(instanceId);
    } else if (serviceType === 'rabbitmq') {
      return renderRabbitMQTestOperation(instanceId);
    }
    return '<div class="no-data">No operations available</div>';
  };

  // Redis Operation Renderers
  const renderRedisTestOperation = (instanceId) => {
    const testingInstance = window.currentTestingInstance;
    const connectionFields = testingInstance ? generateConnectionFields(instanceId, testingInstance.serviceType, testingInstance.credentials) : '';

    return `
      <table class="operation-form">
        <tr>
          <th>Connection Configuration</th>
          <th>Status</th>
        </tr>
        <tr>
          <td>
            <div class="connection-selector">
              ${connectionFields}
            </div>
          </td>
          <td>
            <button class="execute-btn" id="connect-btn-${instanceId}" onclick="executeRedisTest('${instanceId}')">Connect</button>
          </td>
        </tr>
      </table>
    `;
  };

  const renderRedisInfoOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis INFO</h5>
        <p>Get Redis server information and statistics.</p>
        <button class="execute-btn" onclick="executeRedisInfo('${instanceId}')">Get Server Info</button>
      </div>
    `;
  };

  const renderRedisSetOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis SET</h5>
        <div class="form-row">
          <label for="redis-key-${instanceId}">Key:</label>
          <input type="text" id="redis-key-${instanceId}" placeholder="Enter key name" />
        </div>
        <div class="form-row">
          <label for="redis-value-${instanceId}">Value:</label>
          <textarea id="redis-value-${instanceId}" placeholder="Enter value" rows="3"></textarea>
        </div>
        <div class="form-row">
          <label for="redis-ttl-${instanceId}">TTL (seconds, optional):</label>
          <input type="number" id="redis-ttl-${instanceId}" placeholder="0 for no expiration" min="0" />
        </div>
        <button class="execute-btn" onclick="executeRedisSet('${instanceId}')">SET</button>
      </div>
    `;
  };

  const renderRedisGetOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis GET</h5>
        <div class="form-row">
          <label for="redis-get-key-${instanceId}">Key:</label>
          <input type="text" id="redis-get-key-${instanceId}" placeholder="Enter key name" />
        </div>
        <button class="execute-btn" onclick="executeRedisGet('${instanceId}')">GET</button>
      </div>
    `;
  };

  const renderRedisDeleteOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis DELETE</h5>
        <div class="form-row">
          <label for="redis-delete-keys-${instanceId}">Keys (one per line):</label>
          <textarea id="redis-delete-keys-${instanceId}" placeholder="Enter key names, one per line" rows="3"></textarea>
        </div>
        <button class="execute-btn" onclick="executeRedisDelete('${instanceId}')">DELETE</button>
      </div>
    `;
  };

  const renderRedisKeysOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis KEYS</h5>
        <div class="form-row">
          <label for="redis-pattern-${instanceId}">Pattern:</label>
          <input type="text" id="redis-pattern-${instanceId}" placeholder="* (default)" value="*" />
        </div>
        <button class="execute-btn" onclick="executeRedisKeys('${instanceId}')">List Keys</button>
      </div>
    `;
  };

  const renderRedisCommandOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis COMMAND</h5>
        <div class="form-row">
          <label for="redis-cmd-${instanceId}">Command:</label>
          <input type="text" id="redis-cmd-${instanceId}" placeholder="e.g., PING, DBSIZE, TYPE" />
        </div>
        <div class="form-row">
          <label for="redis-args-${instanceId}">Arguments (JSON array, optional):</label>
          <textarea id="redis-args-${instanceId}" placeholder='["arg1", "arg2"]' rows="2"></textarea>
        </div>
        <button class="execute-btn" onclick="executeRedisCommand('${instanceId}')">Execute</button>
      </div>
    `;
  };

  const renderRedisFlushOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Redis FLUSH</h5>
        <p><strong>Warning:</strong> This will delete all data in the database!</p>
        <div class="form-row">
          <label for="redis-flush-db-${instanceId}">Database:</label>
          <input type="number" id="redis-flush-db-${instanceId}" value="0" min="0" />
        </div>
        <button class="execute-btn warning" onclick="executeRedisFlush('${instanceId}')">FLUSH DATABASE</button>
      </div>
    `;
  };

  // RabbitMQ Operation Renderers
  const renderRabbitMQTestOperation = (instanceId) => {
    const testingInstance = window.currentTestingInstance;
    const connectionFields = testingInstance ? generateConnectionFields(instanceId, testingInstance.serviceType, testingInstance.credentials) : '';

    return `
      <table class="operation-form">
        <tr>
          <th>Connection Configuration</th>
          <th>Status</th>
        </tr>
        <tr>
          <td>
            <div class="connection-selector">
              ${connectionFields}
            </div>
          </td>
          <td>
            <button class="execute-btn" id="connect-btn-${instanceId}" onclick="executeRabbitMQTest('${instanceId}')">Connect</button>
          </td>
        </tr>
      </table>
    `;
  };

  const renderRabbitMQPublishOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Publish Message</h5>
        <div class="form-row">
          <label for="rabbitmq-queue-${instanceId}">Queue Name:</label>
          <input type="text" id="rabbitmq-queue-${instanceId}" placeholder="test-queue" />
        </div>
        <div class="form-row">
          <label for="rabbitmq-exchange-${instanceId}">Exchange (optional):</label>
          <input type="text" id="rabbitmq-exchange-${instanceId}" placeholder="Leave empty for default exchange" />
        </div>
        <div class="form-row">
          <label for="rabbitmq-message-${instanceId}">Message:</label>
          <textarea id="rabbitmq-message-${instanceId}" placeholder="Enter message content" rows="3"></textarea>
        </div>
        <div class="form-row">
          <label class="checkbox-label">
            <input type="checkbox" id="rabbitmq-persistent-${instanceId}" />
            <span>Persistent Message</span>
          </label>
        </div>
        <button class="execute-btn" onclick="executeRabbitMQPublish('${instanceId}')">Publish</button>
      </div>
    `;
  };

  const renderRabbitMQConsumeOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Consume Messages</h5>
        <div class="form-row">
          <label for="rabbitmq-consume-queue-${instanceId}">Queue Name:</label>
          <input type="text" id="rabbitmq-consume-queue-${instanceId}" placeholder="test-queue" />
        </div>
        <div class="form-row">
          <label for="rabbitmq-count-${instanceId}">Message Count:</label>
          <input type="number" id="rabbitmq-count-${instanceId}" value="5" min="1" max="100" />
        </div>
        <div class="form-row">
          <label for="rabbitmq-timeout-${instanceId}">Timeout (ms):</label>
          <input type="number" id="rabbitmq-timeout-${instanceId}" value="5000" min="1000" max="30000" />
        </div>
        <div class="form-row">
          <label class="checkbox-label">
            <input type="checkbox" id="rabbitmq-auto-ack-${instanceId}" checked />
            <span>Auto Acknowledge</span>
          </label>
        </div>
        <button class="execute-btn" onclick="executeRabbitMQConsume('${instanceId}')">Consume</button>
      </div>
    `;
  };

  const renderRabbitMQQueuesOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Queue Information</h5>
        <p>List all queues and their current status.</p>
        <button class="execute-btn" onclick="executeRabbitMQQueues('${instanceId}')">Get Queue Info</button>
      </div>
    `;
  };

  const renderRabbitMQQueueOpsOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Queue Operations</h5>
        <div class="form-row">
          <label for="rabbitmq-queue-ops-name-${instanceId}">Queue Name:</label>
          <input type="text" id="rabbitmq-queue-ops-name-${instanceId}" placeholder="queue-name" />
        </div>
        <div class="form-row">
          <label for="rabbitmq-operation-${instanceId}">Operation:</label>
          <select id="rabbitmq-operation-${instanceId}">
            <option value="create">Create Queue</option>
            <option value="delete">Delete Queue</option>
            <option value="purge">Purge Queue</option>
          </select>
        </div>
        <div class="form-row">
          <label class="checkbox-label">
            <input type="checkbox" id="rabbitmq-durable-${instanceId}" checked />
            <span>Durable (for create operation)</span>
          </label>
        </div>
        <button class="execute-btn" onclick="executeRabbitMQQueueOps('${instanceId}')">Execute</button>
      </div>
    `;
  };

  const renderRabbitMQManagementOperation = (instanceId) => {
    return `
      <div class="operation-form">
        <h5>Management API</h5>
        <div class="form-row">
          <label for="rabbitmq-mgmt-path-${instanceId}">API Path:</label>
          <input type="text" id="rabbitmq-mgmt-path-${instanceId}" placeholder="/api/overview" />
        </div>
        <div class="form-row">
          <label for="rabbitmq-mgmt-method-${instanceId}">Method:</label>
          <select id="rabbitmq-mgmt-method-${instanceId}">
            <option value="GET">GET</option>
            <option value="POST">POST</option>
            <option value="PUT">PUT</option>
            <option value="DELETE">DELETE</option>
          </select>
        </div>
        <div class="form-row">
          <label class="checkbox-label">
            <input type="checkbox" id="rabbitmq-mgmt-ssl-${instanceId}" />
            <span>Use SSL</span>
          </label>
        </div>
        <button class="execute-btn" onclick="executeRabbitMQManagement('${instanceId}')">Execute</button>
      </div>
    `;
  };

  // Service Testing Execution Functions

  // Global history storage
  window.serviceTestingHistory = window.serviceTestingHistory || {};

  const addToTestingHistory = (instanceId, operation, params, result, success) => {
    if (!window.serviceTestingHistory[instanceId]) {
      window.serviceTestingHistory[instanceId] = [];
    }

    const entry = {
      timestamp: Date.now(),
      operation: operation,
      params: params,
      result: result,
      success: success
    };

    window.serviceTestingHistory[instanceId].unshift(entry);

    // Limit history to 50 entries
    if (window.serviceTestingHistory[instanceId].length > 50) {
      window.serviceTestingHistory[instanceId] = window.serviceTestingHistory[instanceId].slice(0, 50);
    }

    updateTestingHistoryDisplay(instanceId);
  };

  const updateTestingHistoryDisplay = (instanceId) => {
    const historyContainer = document.getElementById(`history-entries-${instanceId}`);
    if (!historyContainer) return;

    const history = window.serviceTestingHistory[instanceId] || [];
    if (history.length === 0) {
      historyContainer.innerHTML = '<div class="no-data">No operations performed yet</div>';
      return;
    }

    const historyHtml = history.map(entry => {
      const timestamp = new Date(entry.timestamp).toLocaleString();
      const statusClass = entry.success ? 'success' : 'error';
      const statusIcon = entry.success ? '✓' : '✗';

      return `
        <div class="history-entry ${statusClass}">
          <div class="history-entry-header">
            <span class="history-operation">${statusIcon} ${entry.operation.toUpperCase()}</span>
            <span class="history-timestamp">${timestamp}</span>
          </div>
          <div class="history-entry-body">
            <div class="history-params">
              <strong>Request:</strong>
              <pre>${JSON.stringify(entry.params, null, 2)}</pre>
            </div>
            <div class="history-result">
              <strong>Response:</strong>
              <pre>${JSON.stringify(entry.result, null, 2)}</pre>
            </div>
          </div>
        </div>
      `;
    }).join('');

    historyContainer.innerHTML = historyHtml;
  };

  const clearTestingHistory = (instanceId) => {
    window.serviceTestingHistory[instanceId] = [];
    updateTestingHistoryDisplay(instanceId);
  };

  // Updated to get connection info from dropdowns
  const getConnectionInfo = (instanceId) => {
    const connectionType = document.getElementById(`connection-type-${instanceId}`)?.value;
    const connectionUser = document.getElementById(`connection-user-${instanceId}`)?.value;
    const connectionVhost = document.getElementById(`connection-vhost-${instanceId}`)?.value;

    return {
      type: connectionType || 'standard',
      user: connectionUser || '',
      vhost: connectionVhost || '/'
    };
  };

  // Backward compatibility
  const getConnectionType = (instanceId) => {
    return getConnectionInfo(instanceId).type;
  };

  const executeServiceOperation = async (instanceId, serviceType, operation, params = {}) => {
    const connectionInfo = getConnectionInfo(instanceId);

    // Add connection information to params
    if (serviceType === 'redis') {
      params.use_tls = connectionInfo.type === 'tls';
      params.connection_type = connectionInfo.type;
    } else if (serviceType === 'rabbitmq') {
      params.use_amqps = connectionInfo.type === 'amqps';
      params.connection_type = connectionInfo.type;
      params.connection_user = connectionInfo.user;
      params.connection_vhost = connectionInfo.vhost;
    }

    try {
      const url = `/b/${instanceId}/${serviceType}/${operation}`;
      const method = operation === 'test' || operation === 'info' || operation === 'queues' ? 'GET' : 'POST';

      const fetchOptions = {
        method: method,
        cache: 'no-cache'
      };

      if (method === 'POST') {
        fetchOptions.headers = { 'Content-Type': 'application/json' };
        fetchOptions.body = JSON.stringify(params);
      } else if (Object.keys(params).length > 0) {
        // Add query parameters for GET requests
        const queryParams = new URLSearchParams();
        Object.entries(params).forEach(([key, value]) => {
          if (value !== undefined && value !== null && value !== '') {
            queryParams.append(key, value.toString());
          }
        });
        if (queryParams.toString()) {
          url += '?' + queryParams.toString();
        }
      }

      const response = await fetch(url, fetchOptions);
      const result = await response.json();

      addToTestingHistory(instanceId, operation, params, result, response.ok);

      if (!response.ok) {
        throw new Error(result.error || `HTTP ${response.status}`);
      }

      return result;
    } catch (error) {
      const errorResult = { success: false, error: error.message };
      addToTestingHistory(instanceId, operation, params, errorResult, false);
      throw error;
    }
  };

  // Redis Execution Functions
  // Connection state management functions
  const connectToService = (instanceId) => {
    const connectionParams = getConnectionInfo(instanceId);
    const connectionState = window.serviceConnections[instanceId];

    connectionState.connected = true;
    connectionState.connectionParams = connectionParams;
    connectionState.connectedAt = Date.now();

    // Set 10-minute timeout
    if (connectionState.timeout) {
      clearTimeout(connectionState.timeout);
    }

    connectionState.timeout = setTimeout(() => {
      disconnectFromService(instanceId);
    }, 10 * 60 * 1000); // 10 minutes

    updateConnectionUI(instanceId, true);
    updateConnectionStatus(instanceId, true);
  };

  const disconnectFromService = (instanceId) => {
    const connectionState = window.serviceConnections[instanceId];

    if (connectionState.timeout) {
      clearTimeout(connectionState.timeout);
      connectionState.timeout = null;
    }

    connectionState.connected = false;
    connectionState.connectionParams = null;
    connectionState.connectedAt = null;

    updateConnectionUI(instanceId, false);
    updateConnectionStatus(instanceId, false);
  };

  const updateConnectionUI = (instanceId, connected) => {
    const connectBtn = document.getElementById(`connect-btn-${instanceId}`);
    if (connectBtn) {
      connectBtn.textContent = connected ? 'Disconnect' : 'Connect';
      connectBtn.onclick = connected ?
        () => handleDisconnect(instanceId) :
        () => handleConnect(instanceId);
    }
  };

  const updateConnectionStatus = (instanceId, connected) => {
    // Find the Connection tab and add status indicator
    const connectionTab = document.querySelector(`.operation-tab[data-operation="test"][data-instance="${instanceId}"]`);
    if (connectionTab) {
      // Remove existing status indicators
      const existingIndicator = connectionTab.querySelector('.connection-status-indicator');
      if (existingIndicator) {
        existingIndicator.remove();
      }

      // Add new status indicator
      const indicator = document.createElement('span');
      indicator.className = 'connection-status-indicator';
      indicator.style.cssText = `
        display: inline-block;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        margin-right: 6px;
        background-color: ${connected ? '#28a745' : '#dc3545'};
      `;

      connectionTab.insertBefore(indicator, connectionTab.firstChild);
    }
  };

  const handleConnect = async (instanceId) => {
    try {
      const testingInstance = window.currentTestingInstance;
      if (!testingInstance || testingInstance.id !== instanceId) {
        throw new Error('Testing instance not found');
      }

      const result = await executeServiceOperation(instanceId, testingInstance.serviceType, 'test');

      if (result && !result.error) {
        connectToService(instanceId);
        console.log('Service connected successfully:', result);
      } else {
        throw new Error(result.error || 'Connection failed');
      }
    } catch (error) {
      console.error('Connection failed:', error);
      updateConnectionStatus(instanceId, false);
    }
  };

  const handleDisconnect = (instanceId) => {
    disconnectFromService(instanceId);
    console.log('Service disconnected');
  };

  window.executeRedisTest = async (instanceId) => {
    const connectionState = window.serviceConnections[instanceId];

    if (connectionState && connectionState.connected) {
      handleDisconnect(instanceId);
    } else {
      await handleConnect(instanceId);
    }
  };

  window.executeRedisInfo = async (instanceId) => {
    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'info');
      console.log('Redis info result:', result);
    } catch (error) {
      console.error('Redis info failed:', error);
    }
  };

  window.executeRedisSet = async (instanceId) => {
    const key = document.getElementById(`redis-key-${instanceId}`)?.value;
    const value = document.getElementById(`redis-value-${instanceId}`)?.value;
    const ttl = parseInt(document.getElementById(`redis-ttl-${instanceId}`)?.value) || 0;

    if (!key || !value) {
      alert('Please provide both key and value');
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'set', { key, value, ttl });
      console.log('Redis SET result:', result);
    } catch (error) {
      console.error('Redis SET failed:', error);
    }
  };

  window.executeRedisGet = async (instanceId) => {
    const key = document.getElementById(`redis-get-key-${instanceId}`)?.value;

    if (!key) {
      alert('Please provide a key');
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'get', { key });
      console.log('Redis GET result:', result);
    } catch (error) {
      console.error('Redis GET failed:', error);
    }
  };

  window.executeRedisDelete = async (instanceId) => {
    const keysText = document.getElementById(`redis-delete-keys-${instanceId}`)?.value;

    if (!keysText) {
      alert('Please provide keys to delete');
      return;
    }

    const keys = keysText.split('\n').map(k => k.trim()).filter(k => k);

    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'delete', { keys });
      console.log('Redis DELETE result:', result);
    } catch (error) {
      console.error('Redis DELETE failed:', error);
    }
  };

  window.executeRedisKeys = async (instanceId) => {
    const pattern = document.getElementById(`redis-pattern-${instanceId}`)?.value || '*';

    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'keys', { pattern });
      console.log('Redis KEYS result:', result);
    } catch (error) {
      console.error('Redis KEYS failed:', error);
    }
  };

  window.executeRedisCommand = async (instanceId) => {
    const command = document.getElementById(`redis-cmd-${instanceId}`)?.value;
    const argsText = document.getElementById(`redis-args-${instanceId}`)?.value;

    if (!command) {
      alert('Please provide a command');
      return;
    }

    let args = [];
    if (argsText) {
      try {
        args = JSON.parse(argsText);
      } catch (e) {
        alert('Invalid JSON in arguments field');
        return;
      }
    }

    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'command', { command, args });
      console.log('Redis COMMAND result:', result);
    } catch (error) {
      console.error('Redis COMMAND failed:', error);
    }
  };

  window.executeRedisFlush = async (instanceId) => {
    const database = parseInt(document.getElementById(`redis-flush-db-${instanceId}`)?.value) || 0;

    if (!confirm('Are you sure you want to flush the database? This will delete all data!')) {
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'redis', 'flush', { database });
      console.log('Redis FLUSH result:', result);
    } catch (error) {
      console.error('Redis FLUSH failed:', error);
    }
  };

  // RabbitMQ Execution Functions
  window.executeRabbitMQTest = async (instanceId) => {
    const connectionState = window.serviceConnections[instanceId];

    if (connectionState && connectionState.connected) {
      handleDisconnect(instanceId);
    } else {
      await handleConnect(instanceId);
    }
  };

  window.executeRabbitMQPublish = async (instanceId) => {
    const queue = document.getElementById(`rabbitmq-queue-${instanceId}`)?.value;
    const exchange = document.getElementById(`rabbitmq-exchange-${instanceId}`)?.value || '';
    const message = document.getElementById(`rabbitmq-message-${instanceId}`)?.value;
    const persistent = document.getElementById(`rabbitmq-persistent-${instanceId}`)?.checked || false;

    if (!queue || !message) {
      alert('Please provide both queue name and message');
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'rabbitmq', 'publish', {
        queue, exchange, message, persistent
      });
      console.log('RabbitMQ PUBLISH result:', result);
    } catch (error) {
      console.error('RabbitMQ PUBLISH failed:', error);
    }
  };

  window.executeRabbitMQConsume = async (instanceId) => {
    const queue = document.getElementById(`rabbitmq-consume-queue-${instanceId}`)?.value;
    const count = parseInt(document.getElementById(`rabbitmq-count-${instanceId}`)?.value) || 5;
    const timeout = parseInt(document.getElementById(`rabbitmq-timeout-${instanceId}`)?.value) || 5000;
    const autoAck = document.getElementById(`rabbitmq-auto-ack-${instanceId}`)?.checked || false;

    if (!queue) {
      alert('Please provide a queue name');
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'rabbitmq', 'consume', {
        queue, count, timeout, auto_ack: autoAck
      });
      console.log('RabbitMQ CONSUME result:', result);
    } catch (error) {
      console.error('RabbitMQ CONSUME failed:', error);
    }
  };

  window.executeRabbitMQQueues = async (instanceId) => {
    try {
      const result = await executeServiceOperation(instanceId, 'rabbitmq', 'queues');
      console.log('RabbitMQ QUEUES result:', result);
    } catch (error) {
      console.error('RabbitMQ QUEUES failed:', error);
    }
  };

  window.executeRabbitMQQueueOps = async (instanceId) => {
    const queue = document.getElementById(`rabbitmq-queue-ops-name-${instanceId}`)?.value;
    const operation = document.getElementById(`rabbitmq-operation-${instanceId}`)?.value;
    const durable = document.getElementById(`rabbitmq-durable-${instanceId}`)?.checked || false;

    if (!queue) {
      alert('Please provide a queue name');
      return;
    }

    if (operation === 'delete' && !confirm(`Are you sure you want to delete queue "${queue}"?`)) {
      return;
    }

    if (operation === 'purge' && !confirm(`Are you sure you want to purge all messages from queue "${queue}"?`)) {
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'rabbitmq', 'queue-ops', {
        queue, operation, durable
      });
      console.log('RabbitMQ QUEUE-OPS result:', result);
    } catch (error) {
      console.error('RabbitMQ QUEUE-OPS failed:', error);
    }
  };

  window.executeRabbitMQManagement = async (instanceId) => {
    const path = document.getElementById(`rabbitmq-mgmt-path-${instanceId}`)?.value;
    const method = document.getElementById(`rabbitmq-mgmt-method-${instanceId}`)?.value || 'GET';
    const useSSL = document.getElementById(`rabbitmq-mgmt-ssl-${instanceId}`)?.checked || false;

    if (!path) {
      alert('Please provide an API path');
      return;
    }

    try {
      const result = await executeServiceOperation(instanceId, 'rabbitmq', 'management', {
        path, method, use_ssl: useSSL
      });
      console.log('RabbitMQ MANAGEMENT result:', result);
    } catch (error) {
      console.error('RabbitMQ MANAGEMENT failed:', error);
    }
  };

  // Operation Tab Switching
  window.switchTestingOperation = (instanceId, operation) => {
    // Update active tab
    document.querySelectorAll(`.operation-tab[data-instance="${instanceId}"]`).forEach(tab => {
      tab.classList.remove('active');
    });
    document.querySelector(`.operation-tab[data-operation="${operation}"][data-instance="${instanceId}"]`).classList.add('active');

    // Get service type from global storage
    const testingInstance = window.currentTestingInstance;
    if (!testingInstance || testingInstance.id !== instanceId) {
      console.error('Testing instance not found');
      return;
    }

    // Update operation content
    const contentContainer = document.getElementById(`operation-content-${instanceId}`);
    if (contentContainer) {
      contentContainer.innerHTML = getOperationContent(testingInstance.serviceType, operation, instanceId);

      // After content is loaded, restore connection state for connection tab
      setTimeout(() => {
        const connectionState = window.serviceConnections && window.serviceConnections[instanceId];
        if (connectionState) {
          // Update connection status indicator
          updateConnectionStatus(instanceId, connectionState.connected);

          // If on connection tab, restore connection UI state
          if (operation === 'test') {
            updateConnectionUI(instanceId, connectionState.connected);

            // Restore connection parameters if connected
            if (connectionState.connected && connectionState.connectionParams) {
              restoreConnectionParams(instanceId, connectionState.connectionParams);
            }
          }
        }
      }, 50);
    }
  };

  // Function to restore connection parameters to the UI
  const restoreConnectionParams = (instanceId, params) => {
    Object.keys(params).forEach(key => {
      const element = document.getElementById(`${key}-${instanceId}`);
      if (element) {
        element.value = params[key];
      }
    });
  };

  const getOperationContent = (serviceType, operation, instanceId) => {
    if (serviceType === 'redis') {
      switch (operation) {
        case 'test': return renderRedisTestOperation(instanceId);
        case 'info': return renderRedisInfoOperation(instanceId);
        case 'set': return renderRedisSetOperation(instanceId);
        case 'get': return renderRedisGetOperation(instanceId);
        case 'delete': return renderRedisDeleteOperation(instanceId);
        case 'keys': return renderRedisKeysOperation(instanceId);
        case 'command': return renderRedisCommandOperation(instanceId);
        case 'flush': return renderRedisFlushOperation(instanceId);
      }
    } else if (serviceType === 'rabbitmq') {
      switch (operation) {
        case 'test': return renderRabbitMQTestOperation(instanceId);
        case 'publish': return renderRabbitMQPublishOperation(instanceId);
        case 'consume': return renderRabbitMQConsumeOperation(instanceId);
        case 'queues': return renderRabbitMQQueuesOperation(instanceId);
        case 'queue-ops': return renderRabbitMQQueueOpsOperation(instanceId);
        case 'management': return renderRabbitMQManagementOperation(instanceId);
      }
    }

    return '<div class="no-data">Operation not found</div>';
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

  // Reusable function to copy table rows as text
  window.copyTableRowsAsText = async (tableSelector, event) => {
    const button = event.currentTarget;

    try {
      // Find the table
      const table = document.querySelector(tableSelector);
      if (!table) {
        console.error(`Table ${tableSelector} not found`);
        return;
      }

      // Get all visible rows (not hidden by filter)
      const rows = table.querySelectorAll('tbody tr:not([style*="display: none"])');
      let textContent = [];

      // Extract headers first
      const headers = table.querySelectorAll('thead th');
      const headerTexts = Array.from(headers).map(h => h.textContent.trim());
      if (headerTexts.length > 0) {
        textContent.push(headerTexts.join('\t'));
      }

      // Extract visible row data
      rows.forEach(row => {
        const cells = row.querySelectorAll('td');
        const rowData = Array.from(cells).map(cell => {
          // Get text content, stripping HTML
          const text = cell.textContent || cell.innerText || '';
          return text.trim();
        });
        if (rowData.length > 0) {
          textContent.push(rowData.join('\t'));
        }
      });

      const text = textContent.join('\n');

      // Copy to clipboard
      await navigator.clipboard.writeText(text);

      // Visual feedback
      button.classList.add('copied');
      const originalTitle = button.title;
      button.title = 'Copied!';
      const spanElement = button.querySelector('span');
      const originalText = spanElement ? spanElement.textContent : '';
      if (spanElement) {
        spanElement.textContent = 'Copied!';
      }
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
        if (spanElement) {
          spanElement.textContent = originalText;
        }
      }, 2000);
    } catch (err) {
      console.error('Failed to copy table rows:', err);
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
        const originalText = spanElement ? spanElement.textContent : '';
        if (spanElement) {
          spanElement.textContent = 'Copied!';
        }
        setTimeout(() => {
          button.classList.remove('copied');
          if (spanElement) {
            spanElement.textContent = originalText;
          }
        }, 2000);
      } catch (err) {
        console.error('Fallback copy failed:', err);
      }
      document.body.removeChild(textarea);
    }
  };

  // Format manifest details with tabbed view
  const formatManifestDetails = (manifestData, instanceId) => {
    if (!manifestData || !manifestData.text || !manifestData.parsed) {
      return '<div class="error">Failed to load manifest details</div>';
    }

    const manifestId = `manifest-${instanceId}-${Date.now()}`;
    window.manifestTexts = window.manifestTexts || {};
    window.manifestTexts[manifestId] = manifestData.text;

    // Create unique IDs for this manifest's tabs
    const tabGroupId = `manifest-tabs-${manifestId}`;

    // Helper function to flatten nested objects into dot notation
    const flattenObject = (obj, prefix = '') => {
      const result = {};
      for (const key in obj) {
        if (obj.hasOwnProperty(key)) {
          const newKey = prefix ? `${prefix}.${key}` : key;
          if (typeof obj[key] === 'object' && obj[key] !== null && !Array.isArray(obj[key])) {
            Object.assign(result, flattenObject(obj[key], newKey));
          } else {
            result[newKey] = obj[key];
          }
        }
      }
      return result;
    };

    // Helper function to format value for display
    const formatValue = (value) => {
      if (value === null || value === undefined) return '<em>null</em>';
      if (typeof value === 'boolean') return `<span class="boolean-value">${value}</span>`;
      if (typeof value === 'number') return `<span class="number-value">${value}</span>`;
      if (Array.isArray(value)) {
        if (value.length === 0) return '<em>[]</em>';
        if (typeof value[0] === 'string') return value.join(', ');
        return `<em>[${value.length} items]</em>`;
      }
      if (typeof value === 'object') return '<em>[object]</em>';
      if (typeof value === 'string' && value.includes('\n')) {
        return `<pre style="margin: 0; white-space: pre-wrap;">${value.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
      }
      return value.toString().replace(/</g, '&lt;').replace(/>/g, '&gt;');
    };

    // Extract data from parsed manifest
    const parsed = manifestData.parsed;
    const directorUuid = parsed.director_uuid || 'Not specified';
    const instanceGroups = parsed.instance_groups || [];
    const releases = parsed.releases || [];
    const stemcells = parsed.stemcells || [];
    const features = parsed.features || {};
    const update = parsed.update || {};
    const variables = parsed.variables || [];
    const addons = parsed.addons || [];

    // Create HTML for tabs
    let html = `
      <div class="manifest-details-container">
        <div class="manifest-tabs-nav">
          <button class="manifest-tab-btn active" data-tab="global" data-group="${tabGroupId}">Global</button>
          ${instanceGroups.length > 0 ? `<button class="manifest-tab-btn" data-tab="instance-groups" data-group="${tabGroupId}">Instance Groups</button>` : ''}
          ${releases.length > 0 ? `<button class="manifest-tab-btn" data-tab="releases" data-group="${tabGroupId}">Releases</button>` : ''}
          ${stemcells.length > 0 ? `<button class="manifest-tab-btn" data-tab="stemcells" data-group="${tabGroupId}">Stemcells</button>` : ''}
          ${Object.keys(features).length > 0 ? `<button class="manifest-tab-btn" data-tab="features" data-group="${tabGroupId}">Features</button>` : ''}
          ${Object.keys(update).length > 0 ? `<button class="manifest-tab-btn" data-tab="update" data-group="${tabGroupId}">Update</button>` : ''}
          ${variables.length > 0 ? `<button class="manifest-tab-btn" data-tab="variables" data-group="${tabGroupId}">Variables</button>` : ''}
          ${addons.length > 0 ? `<button class="manifest-tab-btn" data-tab="addons" data-group="${tabGroupId}">Addons</button>` : ''}
          <button class="manifest-tab-btn" data-tab="manifest" data-group="${tabGroupId}">YAML</button>
        </div>

        <div class="manifest-tab-content">
          <!-- Global Tab -->
          <div class="manifest-tab-pane active" data-tab="global" data-group="${tabGroupId}">
            <table class="manifest-table">
              <thead>
                <tr>
                  <th>Property</th>
                  <th>Value</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>Director UUID</td>
                  <td>
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${directorUuid.replace(/'/g, "\\'")}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <code>${directorUuid}</code>
                    </span>
                  </td>
                </tr>
                <tr>
                  <td>Name</td>
                  <td>
                    <span class="copy-wrapper">
                      <button class="copy-btn-inline" onclick="window.copyValue(event, '${(parsed.name || 'Not specified').replace(/'/g, "\\'")}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <code>${parsed.name || 'Not specified'}</code>
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Instance Groups Tab -->
          ${instanceGroups.length > 0 ? `
          <div class="manifest-tab-pane" data-tab="instance-groups" data-group="${tabGroupId}" style="display: none;">
            <div class="instance-groups-container">
              <div class="instance-group-selector">
                <label for="instance-group-select-${manifestId}">Select Instance Group:</label>
                <select id="instance-group-select-${manifestId}" class="instance-group-select">
                  ${instanceGroups.map((ig, idx) =>
      `<option value="${idx}">${ig.name} (${ig.instances || 1} instance${(ig.instances || 1) > 1 ? 's' : ''})</option>`
    ).join('')}
                </select>
              </div>

              <div class="instance-group-details">
                ${instanceGroups.map((ig, idx) => {
      const jobs = ig.jobs || [];
      return `
                    <div class="instance-group-pane ${idx === 0 ? 'active' : ''}" data-group-index="${idx}">
                      <h4>Instance Group: ${ig.name}</h4>
                      <table class="manifest-table">
                        <thead>
                          <tr>
                            <th>Property</th>
                            <th>Value</th>
                          </tr>
                        </thead>
                        <tbody>
                          <tr>
                            <td>Instances</td>
                            <td>
                              <span class="copy-wrapper">
                                <button class="copy-btn-inline" onclick="window.copyValue(event, '${ig.instances || 1}')"
                                        title="Copy to clipboard">
                                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                </button>
                                <span>${ig.instances || 1}</span>
                              </span>
                            </td>
                          </tr>
                          <tr>
                            <td>AZs</td>
                            <td>
                              <span class="copy-wrapper">
                                <button class="copy-btn-inline" onclick="window.copyValue(event, '${((ig.azs || []).join(', ') || 'None').replace(/'/g, "\\'")}')"
                                        title="Copy to clipboard">
                                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                </button>
                                <span>${(ig.azs || []).join(', ') || 'None'}</span>
                              </span>
                            </td>
                          </tr>
                          <tr>
                            <td>Networks</td>
                            <td>
                              <span class="copy-wrapper">
                                <button class="copy-btn-inline" onclick="window.copyValue(event, '${((ig.networks || []).map(n => n.name || n).join(', ') || 'None').replace(/'/g, "\\'")}')"
                                        title="Copy to clipboard">
                                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                </button>
                                <span>${(ig.networks || []).map(n => n.name || n).join(', ') || 'None'}</span>
                              </span>
                            </td>
                          </tr>
                          <tr>
                            <td>VM Type</td>
                            <td>
                              <span class="copy-wrapper">
                                <button class="copy-btn-inline" onclick="window.copyValue(event, '${(ig.vm_type || 'Not specified').replace(/'/g, "\\'")}')"
                                        title="Copy to clipboard">
                                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                </button>
                                <span>${ig.vm_type || 'Not specified'}</span>
                              </span>
                            </td>
                          </tr>
                          <tr>
                            <td>Persistent Disk Type</td>
                            <td>
                              <span class="copy-wrapper">
                                <button class="copy-btn-inline" onclick="window.copyValue(event, '${(ig.persistent_disk_type || 'None').replace(/'/g, "\\'")}')"
                                        title="Copy to clipboard">
                                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                </button>
                                <span>${ig.persistent_disk_type || 'None'}</span>
                              </span>
                            </td>
                          </tr>
                          <tr>
                            <td>Stemcell</td>
                            <td>
                              <span class="copy-wrapper">
                                <button class="copy-btn-inline" onclick="window.copyValue(event, '${(ig.stemcell || 'default').replace(/'/g, "\\'")}')"
                                        title="Copy to clipboard">
                                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                </button>
                                <span>${ig.stemcell || 'default'}</span>
                              </span>
                            </td>
                          </tr>
                        </tbody>
                      </table>

                      ${jobs.length > 0 ? `
                        <h5>Jobs</h5>
                        <div class="jobs-container">
                          <div class="job-selector">
                            <label for="job-select-${manifestId}-${idx}">Select Job:</label>
                            <select id="job-select-${manifestId}-${idx}" class="job-select" data-group-index="${idx}">
                              ${jobs.map((job, jidx) =>
        `<option value="${jidx}">${job.name} (${job.release || 'unknown release'})</option>`
      ).join('')}
                            </select>
                          </div>

                          <div class="job-details">
                            ${jobs.map((job, jidx) => {
        const properties = job.properties || {};
        const flatProps = flattenObject(properties);
        const propKeys = Object.keys(flatProps).sort();

        return `
                                <div class="job-pane ${jidx === 0 ? 'active' : ''}" data-job-index="${jidx}" data-group-index="${idx}">
                                  <h6>Job: ${job.name}</h6>
                                  <table class="manifest-table">
                                    <thead>
                                      <tr>
                                        <th>Property</th>
                                        <th>Value</th>
                                      </tr>
                                    </thead>
                                    <tbody>
                                      <tr>
                                        <td>Release</td>
                                        <td>
                                          <span class="copy-wrapper">
                                            <button class="copy-btn-inline" onclick="window.copyValue(event, '${(job.release || 'Not specified').replace(/'/g, "\\'")}')"
                                                    title="Copy to clipboard">
                                              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                            </button>
                                            <span>${job.release || 'Not specified'}</span>
                                          </span>
                                        </td>
                                      </tr>
                                      ${propKeys.length > 0 ? propKeys.map(key => {
          const value = flatProps[key];
          const copyValue = typeof value === 'object' ? JSON.stringify(value) : String(value);
          return `
                                        <tr>
                                          <td><code>${key}</code></td>
                                          <td>
                                            <span class="copy-wrapper">
                                              <button class="copy-btn-inline" onclick="window.copyValue(event, '${copyValue.replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
                                                      title="Copy to clipboard">
                                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                                              </button>
                                              <span>${formatValue(value)}</span>
                                            </span>
                                          </td>
                                        </tr>
                                      `;
        }).join('') : '<tr><td colspan="2"><em>No properties defined</em></td></tr>'}
                                    </tbody>
                                  </table>
                                </div>
                              `;
      }).join('')}
                          </div>
                        </div>
                      ` : ''}
                    </div>
                  `;
    }).join('')}
              </div>
            </div>
          </div>
          ` : ''}

          <!-- Releases Tab -->
          ${releases.length > 0 ? `
          <div class="manifest-tab-pane" data-tab="releases" data-group="${tabGroupId}" style="display: none;">
            <table class="manifest-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Version</th>
                  <th>URL</th>
                  <th>SHA1</th>
                </tr>
              </thead>
              <tbody>
                ${releases.map(release => `
                  <tr>
                    <td>
                      <span class="copy-wrapper">
                        <button class="copy-btn-inline" onclick="window.copyValue(event, '${release.name.replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                        <span>${release.name}</span>
                      </span>
                    </td>
                    <td>
                      <span class="copy-wrapper">
                        <button class="copy-btn-inline" onclick="window.copyValue(event, '${(release.version || 'latest').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                        <span>${release.version || 'latest'}</span>
                      </span>
                    </td>
                    <td>
                      ${release.url ? `
                        <span class="copy-wrapper">
                          <button class="copy-btn-inline" onclick="window.copyValue(event, '${release.url.replace(/'/g, "\\'")}')"
                                  title="Copy URL">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                          </button>
                          <a href="${release.url}" target="_blank" style="word-break: break-all;">${release.url}</a>
                        </span>
                      ` : 'N/A'}
                    </td>
                    <td>
                      ${release.sha1 && release.sha1 !== 'N/A' ? `
                        <span class="copy-wrapper">
                          <button class="copy-btn-inline" onclick="window.copyValue(event, '${release.sha1.replace(/'/g, "\\'")}')"
                                  title="Copy SHA1">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                          </button>
                          <code style="font-size: 0.8em;">${release.sha1}</code>
                        </span>
                      ` : '<code style="font-size: 0.8em;">N/A</code>'}
                    </td>
                  </tr>
                `).join('')}
              </tbody>
            </table>
          </div>
          ` : ''}

          <!-- Stemcells Tab -->
          ${stemcells.length > 0 ? `
          <div class="manifest-tab-pane" data-tab="stemcells" data-group="${tabGroupId}" style="display: none;">
            <table class="manifest-table">
              <thead>
                <tr>
                  <th>Alias</th>
                  <th>OS</th>
                  <th>Version</th>
                </tr>
              </thead>
              <tbody>
                ${stemcells.map(stemcell => `
                  <tr>
                    <td>
                      <span class="copy-wrapper">
                        <button class="copy-btn-inline" onclick="window.copyValue(event, '${(stemcell.alias || 'default').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                        <span>${stemcell.alias || 'default'}</span>
                      </span>
                    </td>
                    <td>
                      <span class="copy-wrapper">
                        <button class="copy-btn-inline" onclick="window.copyValue(event, '${(stemcell.os || 'Not specified').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                        <span>${stemcell.os || 'Not specified'}</span>
                      </span>
                    </td>
                    <td>
                      <span class="copy-wrapper">
                        <button class="copy-btn-inline" onclick="window.copyValue(event, '${(stemcell.version || 'latest').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                        <span>${stemcell.version || 'latest'}</span>
                      </span>
                    </td>
                  </tr>
                `).join('')}
              </tbody>
            </table>
          </div>
          ` : ''}

          <!-- Features Tab -->
          ${Object.keys(features).length > 0 ? `
          <div class="manifest-tab-pane" data-tab="features" data-group="${tabGroupId}" style="display: none;">
            <table class="manifest-table">
              <thead>
                <tr>
                  <th>Feature</th>
                  <th>Value</th>
                </tr>
              </thead>
              <tbody>
                ${Object.entries(features).map(([key, value]) => `
                  <tr>
                    <td>${key}</td>
                    <td>${formatValue(value)}</td>
                  </tr>
                `).join('')}
              </tbody>
            </table>
          </div>
          ` : ''}

          <!-- Update Tab -->
          ${Object.keys(update).length > 0 ? `
          <div class="manifest-tab-pane" data-tab="update" data-group="${tabGroupId}" style="display: none;">
            <table class="manifest-table">
              <thead>
                <tr>
                  <th>Property</th>
                  <th>Value</th>
                </tr>
              </thead>
              <tbody>
                ${Object.entries(update).map(([key, value]) => `
                  <tr>
                    <td>${key}</td>
                    <td>${formatValue(value)}</td>
                  </tr>
                `).join('')}
              </tbody>
            </table>
          </div>
          ` : ''}

          <!-- Variables Tab -->
          ${variables.length > 0 ? `
          <div class="manifest-tab-pane" data-tab="variables" data-group="${tabGroupId}" style="display: none;">
            <table class="manifest-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Type</th>
                  <th>Options</th>
                </tr>
              </thead>
              <tbody>
                ${variables.map(variable => `
                  <tr>
                    <td>${variable.name}</td>
                    <td>${variable.type || 'Not specified'}</td>
                    <td>${variable.options ? `<pre style="margin: 0;">${JSON.stringify(variable.options, null, 2)}</pre>` : 'None'}</td>
                  </tr>
                `).join('')}
              </tbody>
            </table>
          </div>
          ` : ''}

          <!-- Addons Tab -->
          ${addons.length > 0 ? `
          <div class="manifest-tab-pane" data-tab="addons" data-group="${tabGroupId}" style="display: none;">
            <div class="addons-list">
              ${addons.map((addon, idx) => `
                <div class="addon-item">
                  <h5>Addon ${idx + 1}: ${addon.name || 'Unnamed'}</h5>
                  ${addon.jobs ? `
                    <table class="manifest-table">
                      <thead>
                        <tr>
                          <th>Job Name</th>
                          <th>Release</th>
                        </tr>
                      </thead>
                      <tbody>
                        ${addon.jobs.map(job => `
                          <tr>
                            <td>${job.name}</td>
                            <td>${job.release || 'Not specified'}</td>
                          </tr>
                        `).join('')}
                      </tbody>
                    </table>
                  ` : ''}
                </div>
              `).join('')}
            </div>
          </div>
          ` : ''}

          <!-- Manifest Tab -->
          <div class="manifest-tab-pane" data-tab="manifest" data-group="${tabGroupId}" style="display: none;">
            <div class="manifest-container">
              <div class="manifest-header">
                <button class="copy-btn-manifest" onclick="window.copyManifest('${manifestId}', event)"
                        title="Copy manifest to clipboard">
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  <span>Copy</span>
                </button>
              </div>
              <pre>${manifestData.text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>
            </div>
          </div>
        </div>
      </div>
    `;

    // Set up event handlers after the HTML is added to the DOM
    setTimeout(() => {
      // Tab switching
      const tabButtons = document.querySelectorAll(`.manifest-tab-btn[data-group="${tabGroupId}"]`);
      const tabPanes = document.querySelectorAll(`.manifest-tab-pane[data-group="${tabGroupId}"]`);

      tabButtons.forEach(button => {
        button.addEventListener('click', () => {
          const tabName = button.dataset.tab;

          // Update active states
          tabButtons.forEach(btn => btn.classList.remove('active'));
          button.classList.add('active');

          tabPanes.forEach(pane => {
            if (pane.dataset.tab === tabName) {
              pane.style.display = 'block';
            } else {
              pane.style.display = 'none';
            }
          });
        });
      });

      // Instance group selector
      const instanceGroupSelect = document.getElementById(`instance-group-select-${manifestId}`);
      if (instanceGroupSelect) {
        instanceGroupSelect.addEventListener('change', (e) => {
          const index = e.target.value;
          const panes = document.querySelectorAll('.instance-group-pane');
          panes.forEach(pane => {
            if (pane.dataset.groupIndex === index) {
              pane.classList.add('active');
              pane.style.display = 'block';
            } else {
              pane.classList.remove('active');
              pane.style.display = 'none';
            }
          });
        });
      }

      // Job selectors
      const jobSelects = document.querySelectorAll('.job-select');
      jobSelects.forEach(select => {
        select.addEventListener('change', (e) => {
          const jobIndex = e.target.value;
          const groupIndex = e.target.dataset.groupIndex;
          const panes = document.querySelectorAll(`.job-pane[data-group-index="${groupIndex}"]`);
          panes.forEach(pane => {
            if (pane.dataset.jobIndex === jobIndex) {
              pane.classList.add('active');
              pane.style.display = 'block';
            } else {
              pane.classList.remove('active');
              pane.style.display = 'none';
            }
          });
        });
      });

      // Initialize manifest table sorting
      // Just call once as initializeSorting handles all tables with this class
      if (document.querySelector('.manifest-table')) {
        initializeSorting('manifest-table');
      }
    }, 100);

    return html;
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

  // Handler for blacksmith log file selection
  window.selectBlacksmithLogFile = async (logFilePath) => {
    const displayContainer = document.getElementById('blacksmith-logs-display');

    if (!displayContainer) {
      console.error('Log display container not found');
      return;
    }

    // Show loading state
    displayContainer.innerHTML = '<div class="loading">Loading...</div>';

    try {
      // Fetch the logs for the selected file
      const response = await fetch(`/b/blacksmith/logs?file=${encodeURIComponent(logFilePath)}`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();
      const logs = data.logs;

      if (!logs || logs === '') {
        displayContainer.innerHTML = '<div class="no-data">No logs available for this file</div>';
        return;
      }

      // Save the selected log file to localStorage
      LogSelectionManager.saveBlacksmithLog(logFilePath);

      // Update the display with formatted logs
      // Pass false for includeSearchFilter since the search filter already exists in logs-controls-row
      const tableHTML = renderLogsTable(logs, null, false);
      displayContainer.innerHTML = tableHTML;

      // Re-attach the search filter functionality to the existing search filter
      // and initialize sorting
      initializeSorting('logs-table');
      attachSearchFilter('logs-table');

    } catch (error) {
      console.error('Failed to fetch log file:', error);
      displayContainer.innerHTML = `<div class="error">Failed to load log file: ${error.message}</div>`;
    }
  };

  // Handler for refreshing blacksmith events
  window.refreshBlacksmithEvents = async (event) => {
    const button = event.currentTarget;
    const displayContainer = document.querySelector('.events-table-container');

    if (!displayContainer) {
      console.error('Events container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('events-table-events');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Use the stored deployment name
      const deploymentName = window.blacksmithDeploymentName || 'blacksmith';

      // Fetch events
      const response = await fetch(`/b/deployments/${deploymentName}/events`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const events = await response.json();

      if (!events || events.length === 0) {
        // Find the detail-content container and update it
        const detailContent = document.querySelector('#blacksmith .detail-content');
        if (detailContent) {
          detailContent.innerHTML = '<div class="no-data">No events recorded</div>';
        }
      } else {
        // Update the detail-content with formatted events
        const detailContent = document.querySelector('#blacksmith .detail-content');
        if (detailContent) {
          detailContent.innerHTML = formatEvents(events);
          // Re-initialize sorting and filtering
          setTimeout(() => {
            initializeSorting('events-table');
            attachSearchFilter('events-table-events');
            // Restore search filter state
            restoreSearchFilterState('events-table-events', currentSearchFilter);
          }, 100);
        }
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh events:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing blacksmith VMs
  window.refreshBlacksmithVMs = async (event) => {
    const button = event.currentTarget;
    const displayContainer = document.querySelector('.vms-table-container');

    if (!displayContainer) {
      console.error('VMs container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('vms-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Use the stored deployment name
      const deploymentName = window.blacksmithDeploymentName || 'blacksmith';

      // Fetch VMs
      const response = await fetch(`/b/deployments/${deploymentName}/vms`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const vms = await response.json();

      if (!vms || vms.length === 0) {
        // Find the detail-content container and update it
        const detailContent = document.querySelector('#blacksmith .detail-content');
        if (detailContent) {
          detailContent.innerHTML = '<div class="no-data">No VMs available</div>';
        }
      } else {
        // Update the detail-content with formatted VMs
        const detailContent = document.querySelector('#blacksmith .detail-content');
        if (detailContent) {
          detailContent.innerHTML = formatVMs(vms);
          // Re-initialize sorting and filtering
          setTimeout(() => {
            initializeSorting('vms-table');
            attachSearchFilter('vms-table');
            // Restore search filter state
            restoreSearchFilterState('vms-table', currentSearchFilter);
          }, 100);
        }
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh VMs:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing service instance VMs
  window.refreshServiceInstanceVMs = async (instanceId, event) => {
    const button = event.currentTarget;
    const displayContainer = document.querySelector('.vms-table-container');

    if (!displayContainer) {
      console.error('Service VMs container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('vms-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Fetch VMs for the service instance
      const response = await fetch(`/b/${instanceId}/vms`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();
      let vms;
      try {
        vms = JSON.parse(text);
      } catch (e) {
        displayContainer.parentElement.parentElement.innerHTML = `<pre>${text}</pre>`;
        return;
      }

      if (!vms || vms.length === 0) {
        // Find the detail-content container and update it
        const detailContent = document.querySelector('#services .detail-content');
        if (detailContent) {
          detailContent.innerHTML = '<div class="no-data">No VMs available</div>';
        }
      } else {
        // Update the detail-content with formatted VMs
        const detailContent = document.querySelector('#services .detail-content');
        if (detailContent) {
          detailContent.innerHTML = formatVMs(vms, instanceId);
          // Re-initialize sorting and filtering
          setTimeout(() => {
            initializeSorting('vms-table');
            attachSearchFilter('vms-table');
            // Restore search filter state
            restoreSearchFilterState('vms-table', currentSearchFilter);
          }, 100);
        }
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh service VMs:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing blacksmith deployment logs
  window.refreshBlacksmithDeploymentLog = async (event) => {
    const button = event.currentTarget;

    // Find the detail-content container - this is what we need to update
    const detailContent = document.querySelector('#blacksmith .detail-content');
    if (!detailContent) {
      console.error('Detail content container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('deployment-log-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Use the stored deployment name
      const deploymentName = window.blacksmithDeploymentName || 'blacksmith';

      // First fetch events to get task ID
      const eventsResponse = await fetch(`/b/deployments/${deploymentName}/events`, { cache: 'no-cache' });
      if (!eventsResponse.ok) {
        throw new Error(`Failed to fetch events: HTTP ${eventsResponse.status}`);
      }
      const events = await eventsResponse.json();

      // Extract latest deployment task ID
      const taskId = getLatestDeploymentTaskId(events);
      if (!taskId) {
        detailContent.innerHTML = '<div class="no-data">No deployment logs available</div>';
        return;
      }

      // Fetch the deployment log using task ID
      const logResponse = await fetch(`/b/deployments/${deploymentName}/tasks/${taskId}/log`, { cache: 'no-cache' });
      if (!logResponse.ok) {
        throw new Error(`HTTP ${logResponse.status}: ${logResponse.statusText}`);
      }

      const logs = await logResponse.json();

      if (!logs || logs.length === 0) {
        detailContent.innerHTML = '<div class="no-data">No deployment logs available</div>';
      } else {
        // Update the detail-content with formatted logs
        detailContent.innerHTML = formatDeploymentLog(logs);
        // Re-initialize sorting and filtering
        setTimeout(() => {
          initializeSorting('deployment-log-table');
          attachSearchFilter('deployment-log-table');
          // Restore search filter state
          restoreSearchFilterState('deployment-log-table', currentSearchFilter);
        }, 100);
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh deployment logs:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing service instance deployment logs
  window.refreshServiceInstanceDeploymentLog = async (instanceId, event) => {
    const button = event.currentTarget;

    // Find the detail-content container - this is what we need to update
    const detailContent = document.querySelector('#services .detail-content');
    if (!detailContent) {
      console.error('Detail content container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('deployment-log-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Fetch deployment logs for the service instance
      const response = await fetch(`/b/${instanceId}/task/log`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();
      let logs;
      try {
        logs = JSON.parse(text);
      } catch (e) {
        detailContent.innerHTML = `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        return;
      }

      if (!logs || logs.length === 0) {
        detailContent.innerHTML = '<div class="no-data">No deployment logs available</div>';
      } else {
        // Update the detail-content with formatted logs
        detailContent.innerHTML = formatDeploymentLog(logs, instanceId);
        // Re-initialize sorting and filtering
        setTimeout(() => {
          initializeSorting('deployment-log-table');
          attachSearchFilter('deployment-log-table');
          // Restore search filter state
          restoreSearchFilterState('deployment-log-table', currentSearchFilter);
        }, 100);
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh service deployment logs:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing blacksmith debug logs
  window.refreshBlacksmithDebugLog = async (event) => {
    const button = event.currentTarget;

    // Find the detail-content container - this is what we need to update
    const detailContent = document.querySelector('#blacksmith .detail-content');
    if (!detailContent) {
      console.error('Detail content container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('debug-log-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Use the stored deployment name
      const deploymentName = window.blacksmithDeploymentName || 'blacksmith';

      // First fetch events to get task ID
      const eventsResponse = await fetch(`/b/deployments/${deploymentName}/events`, { cache: 'no-cache' });
      if (!eventsResponse.ok) {
        throw new Error(`Failed to fetch events: HTTP ${eventsResponse.status}`);
      }
      const events = await eventsResponse.json();

      // Extract latest deployment task ID
      const taskId = getLatestDeploymentTaskId(events);
      if (!taskId) {
        detailContent.innerHTML = '<div class="no-data">No debug logs available</div>';
        return;
      }

      // Fetch the debug log using task ID
      const logResponse = await fetch(`/b/deployments/${deploymentName}/tasks/${taskId}/debug`, { cache: 'no-cache' });
      if (!logResponse.ok) {
        throw new Error(`HTTP ${logResponse.status}: ${logResponse.statusText}`);
      }

      const logs = await logResponse.json();

      if (!logs || logs.length === 0) {
        detailContent.innerHTML = '<div class="no-data">No debug logs available</div>';
      } else {
        // Update the detail-content with formatted logs
        detailContent.innerHTML = formatDebugLog(logs);
        // Re-initialize sorting and filtering
        setTimeout(() => {
          initializeSorting('debug-log-table');
          attachSearchFilter('debug-log-table');
          // Restore search filter state
          restoreSearchFilterState('debug-log-table', currentSearchFilter);
        }, 100);
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh debug logs:', error);

      // Show error in UI
      detailContent.innerHTML = `<div class="error">Failed to refresh debug logs: ${error.message}</div>`;

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing service instance debug logs
  window.refreshServiceInstanceDebugLog = async (instanceId, event) => {
    const button = event.currentTarget;

    // Find the detail-content container - this is what we need to update
    const detailContent = document.querySelector('#services .detail-content');
    if (!detailContent) {
      console.error('Detail content container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('debug-log-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Fetch debug logs for the service instance
      const response = await fetch(`/b/${instanceId}/task/debug`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();
      let logs;
      try {
        logs = JSON.parse(text);
      } catch (e) {
        detailContent.innerHTML = `<pre>${text.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>`;
        return;
      }

      if (!logs || logs.length === 0) {
        detailContent.innerHTML = '<div class="no-data">No debug logs available</div>';
      } else {
        // Update the detail-content with formatted logs
        detailContent.innerHTML = formatDebugLog(logs, instanceId);
        // Re-initialize sorting and filtering
        setTimeout(() => {
          initializeSorting('debug-log-table');
          attachSearchFilter('debug-log-table');
          // Restore search filter state
          restoreSearchFilterState('debug-log-table', currentSearchFilter);
        }, 100);
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh service debug logs:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing service instance events
  window.refreshServiceInstanceEvents = async (instanceId, event) => {
    const button = event.currentTarget;
    const displayContainer = document.querySelector('#services .detail-content .events-table-container');

    if (!displayContainer) {
      console.error('Service events container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('events-table-service-events');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Fetch events for the service instance
      const response = await fetch(`/b/${instanceId}/events`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const text = await response.text();
      let events;
      try {
        events = JSON.parse(text);
      } catch (e) {
        displayContainer.parentElement.innerHTML = `<pre>${text}</pre>`;
        return;
      }

      if (!events || events.length === 0) {
        // Find the detail-content container and update it
        const detailContent = document.querySelector('#services .detail-content');
        if (detailContent) {
          detailContent.innerHTML = '<div class="no-data">No events recorded</div>';
        }
      } else {
        // Update the detail-content with formatted events
        const detailContent = document.querySelector('#services .detail-content');
        if (detailContent) {
          detailContent.innerHTML = formatEvents(events, 'service-events', instanceId);
          // Re-initialize sorting and filtering
          setTimeout(() => {
            initializeSorting('events-table');
            attachSearchFilter('events-table-service-events');
            // Restore search filter state
            restoreSearchFilterState('events-table-service-events', currentSearchFilter);
          }, 100);
        }
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      const spanElement = button.querySelector('span');
      const originalText = spanElement.textContent;
      spanElement.textContent = 'Refreshed!';
      setTimeout(() => {
        button.classList.remove('success');
        spanElement.textContent = originalText;
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh service events:', error);

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
    }
  };

  // Handler for refreshing blacksmith logs
  window.refreshBlacksmithLogs = async (event) => {
    const button = event.currentTarget;
    const logFileDropdown = document.getElementById('blacksmith-log-select');
    const displayContainer = document.getElementById('blacksmith-logs-display');

    if (!logFileDropdown || !displayContainer) {
      console.error('Required elements not found');
      return;
    }

    // Get currently selected log file
    const currentLogFile = logFileDropdown.value || '/var/vcap/sys/log/blacksmith/blacksmith.stdout.log';

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('logs-table');

    // Add spinning animation to refresh button
    button.classList.add('refreshing');
    button.disabled = true;

    try {
      // Fetch the logs for the current file
      const response = await fetch(`/b/blacksmith/logs?file=${encodeURIComponent(currentLogFile)}`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();
      const logs = data.logs;

      if (!logs || logs === '') {
        displayContainer.innerHTML = '<div class="no-data">No logs available for this file</div>';
      } else {
        // Update the display with formatted logs
        // Pass false for includeSearchFilter since the search filter already exists in logs-controls-row
        const tableHTML = renderLogsTable(logs, null, false);
        displayContainer.innerHTML = tableHTML;

        // Re-attach the search filter functionality to the existing search filter
        attachSearchFilter('logs-table');
        // Restore search filter state
        restoreSearchFilterState('logs-table', currentSearchFilter);
      }

      // Visual feedback for successful refresh
      button.classList.add('success');
      setTimeout(() => {
        button.classList.remove('success');
      }, 1000);

    } catch (error) {
      console.error('Failed to refresh logs:', error);
      displayContainer.innerHTML = `<div class="error">Failed to refresh logs: ${error.message}</div>`;

      // Visual feedback for error
      button.classList.add('error');
      setTimeout(() => {
        button.classList.remove('error');
      }, 2000);
    } finally {
      // Remove spinning animation
      button.classList.remove('refreshing');
      button.disabled = false;
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
                if (!service || !service.plans) return;
                service.plans.forEach(plan => {
                  if (!plan) return;
                  if (`${service.name || service.id || 'unknown'}-${plan.name || plan.id || 'unknown'}` === planId) {
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

              // Store current instance info globally for log selection
              window.currentInstanceInfo = {
                id: instanceId,
                service: details.service_id,
                plan: details.plan?.name
              };

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
          const copyDeploymentNamesBtn = document.getElementById('copy-deployment-names');
          const refreshServicesBtn = document.getElementById('refresh-services');
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

          // Copy deployment names button
          if (copyDeploymentNamesBtn) {
            copyDeploymentNamesBtn.addEventListener('click', async (e) => {
              e.preventDefault();

              // Get all visible service items
              const visibleItems = Array.from(document.querySelectorAll('#services .service-item'))
                .filter(item => item.style.display !== 'none');

              // Extract deployment names
              const deploymentNames = visibleItems.map(item => {
                const instanceId = item.dataset.instanceId;
                const details = window.serviceInstances[instanceId];
                if (details) {
                  // Use the same pattern as in renderServiceDetail function
                  return `${details.service_id}-${details.plan?.name || details.plan_id || 'unknown'}-${instanceId}`;
                }
                return instanceId; // fallback
              });

              if (deploymentNames.length === 0) {
                console.warn('No deployment names to copy');
                return;
              }

              const text = deploymentNames.join('\n');

              try {
                await navigator.clipboard.writeText(text);
                // Visual feedback
                const button = e.currentTarget;
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
                console.error('Failed to copy deployment names:', err);
                // Fallback for older browsers
                const textarea = document.createElement('textarea');
                textarea.value = text;
                textarea.style.position = 'fixed';
                textarea.style.opacity = '0';
                document.body.appendChild(textarea);
                textarea.select();
                try {
                  document.execCommand('copy');
                  const button = e.currentTarget;
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
            });
          }

          // Refresh services button
          if (refreshServicesBtn) {
            refreshServicesBtn.addEventListener('click', async (e) => {
              e.preventDefault();

              const button = e.currentTarget;
              const spanElement = button.querySelector('span');
              const originalText = spanElement.textContent;

              // Show loading state
              spanElement.textContent = 'Refreshing...';
              button.disabled = true;

              try {
                // Capture current filter state before refreshing
                const serviceFilter = document.getElementById('service-filter');
                const planFilter = document.getElementById('plan-filter');
                const currentServiceFilter = serviceFilter ? serviceFilter.value : '';
                const currentPlanFilter = planFilter ? planFilter.value : '';

                // Re-fetch the catalog and status data
                const [catalogResponse, statusResponse] = await Promise.all([
                  fetchWithHeaders('/v2/catalog'),
                  fetchWithHeaders('/b/status')
                ]);

                if (!catalogResponse.ok) {
                  throw new Error(`Catalog HTTP ${catalogResponse.status}: ${catalogResponse.statusText}`);
                }
                if (!statusResponse.ok) {
                  throw new Error(`Status HTTP ${statusResponse.status}: ${statusResponse.statusText}`);
                }

                const catalog = await catalogResponse.json();
                const data = await statusResponse.json();
                console.log('Refreshed catalog and status response:', { catalog, data });

                // Process the data to merge catalog and status info (same logic as initial load)
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

                          // Add blacksmith-specific data
                          const planData = {
                            instances: instances[plan.id] || 0,
                            limit: 0
                          };

                          if (data.plans && typeof data.plans === 'object' && data.plans[key]) {
                            planData.limit = data.plans[key].limit || 0;
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
                        // Provide minimal plan data to prevent errors
                        data.instances[i].plan = { name: instance.plan_id };
                      }
                    }
                  });
                }

                // Update the global instances data
                window.serviceInstances = data.instances;

                // Update plans data as well
                window.plansData = catalog;

                // Re-render the services template (which includes the dropdowns)
                const servicesPanel = document.querySelector('#services');
                if (servicesPanel) {
                  servicesPanel.innerHTML = renderServicesTemplate(data.instances);

                  // Re-setup handlers
                  setupServiceHandlers();

                  // Restore filter state after rendering
                  const newServiceFilter = document.getElementById('service-filter');
                  const newPlanFilter = document.getElementById('plan-filter');

                  if (newServiceFilter && currentServiceFilter) {
                    newServiceFilter.value = currentServiceFilter;
                    // Trigger change event to update plan filter options
                    newServiceFilter.dispatchEvent(new Event('change'));

                    // Restore plan filter if it was set
                    if (newPlanFilter && currentPlanFilter) {
                      // Wait for plan options to be populated, then set the value
                      setTimeout(() => {
                        if (newPlanFilter.querySelector(`option[value="${currentPlanFilter}"]`)) {
                          newPlanFilter.value = currentPlanFilter;
                          newPlanFilter.dispatchEvent(new Event('change'));
                        }
                      }, 0);
                    }
                  }
                }

                // Also refresh the plans panel if needed
                const plansPanel = document.querySelector('#plans');
                if (plansPanel && catalog.services && catalog.services.length > 0) {
                  plansPanel.innerHTML = renderPlansTemplate(catalog);

                  // Re-setup plan click handlers
                  document.querySelectorAll('#plans .plan-item').forEach(item => {
                    item.addEventListener('click', function () {
                      const planId = this.dataset.planId;

                      // Find the service and plan from the stored data
                      let selectedService = null;
                      let selectedPlan = null;

                      catalog.services.forEach(service => {
                        if (!service || !service.plans) return;
                        service.plans.forEach(plan => {
                          if (!plan) return;
                          if (`${service.name || service.id || 'unknown'}-${plan.name || plan.id || 'unknown'}` === planId) {
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
                }

                // Show success feedback
                spanElement.textContent = 'Refreshed!';
                setTimeout(() => {
                  spanElement.textContent = originalText;
                }, 2000);

              } catch (error) {
                console.error('Failed to refresh services:', error);
                spanElement.textContent = 'Error';
                setTimeout(() => {
                  spanElement.textContent = originalText;
                }, 2000);
              } finally {
                button.disabled = false;
              }
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

          // Initialize sorting and filtering for tables
          setTimeout(() => {
            if (tabType === 'events') {
              initializeSorting('events-table');
              attachSearchFilter('events-table-service-events');
            } else if (tabType === 'vms') {
              initializeSorting('vms-table');
              attachSearchFilter('vms-table');
            } else if (tabType === 'logs') {
              initializeSorting('deployment-log-table');
              attachSearchFilter('deployment-log-table');
            } else if (tabType === 'debug') {
              initializeSorting('debug-log-table');
              attachSearchFilter('debug-log-table');
            } else if (tabType === 'manifest') {
              // Initialize sorting for manifest tables
              document.querySelectorAll('.manifest-table').forEach((table, index) => {
                initializeSorting('manifest-table');
              });
            } else if (tabType === 'instance-logs') {
              // Initialize sorting and filtering for each job table
              const logsData = window.instanceLogsData;
              if (logsData) {
                Object.keys(logsData).forEach(job => {
                  const tableClass = `instance-logs-table-${job.replace(/\//g, '-')}`;
                  initializeSorting(tableClass);
                  attachSearchFilter(tableClass);
                });
              }
            } else if (tabType === 'testing') {
              // Set up operation tab switching for service testing
              setTimeout(() => {
                document.querySelectorAll('.operation-tab').forEach(tab => {
                  tab.addEventListener('click', function () {
                    const operation = this.dataset.operation;
                    const instanceId = this.dataset.instance;
                    if (operation && instanceId) {
                      window.switchTestingOperation(instanceId, operation);
                    }
                  });
                });
              }, 50);
            }
          }, 100);
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

          // Initialize sorting and filtering for tables
          setTimeout(() => {
            if (tabType === 'events') {
              initializeSorting('events-table');
              attachSearchFilter('events-table-events');
            } else if (tabType === 'vms') {
              initializeSorting('vms-table');
              attachSearchFilter('vms-table');
            } else if (tabType === 'blacksmith-logs') {
              initializeSorting('logs-table');
              attachSearchFilter('logs-table');
            } else if (tabType === 'logs') {
              attachSearchFilter('deployment-log-table');
              initializeSorting('deployment-log-table');
            } else if (tabType === 'debug') {
              attachSearchFilter('debug-log-table');
              initializeSorting('debug-log-table');
            }
          }, 100);
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
            data.boshDNS = instanceData.bosh_dns;
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
