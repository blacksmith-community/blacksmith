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

  // Universal Table Initialization - Auto-sort functionality
  const initializeAllTables = () => {
    // Find all tables in the document
    const tables = document.querySelectorAll('table');
    
    tables.forEach(table => {
      // Skip if table already has sorting initialized
      if (table.hasAttribute('data-sorting-initialized')) {
        return;
      }
      
      // Add sorting to all table headers
      const headers = table.querySelectorAll('thead th');
      headers.forEach((header, index) => {
        // Skip if header already has sorting
        if (header.classList.contains('sortable') || header.classList.contains('sort-asc') || header.classList.contains('sort-desc')) {
          return;
        }
        
        // Add sortable class and indicator
        header.classList.add('sortable');
        header.style.cursor = 'pointer';
        header.setAttribute('data-column-index', index);
        
        // Add click handler for sorting
        header.addEventListener('click', () => {
          sortTableByColumn(table, index, header);
        });
      });
      
      // Mark table as initialized
      table.setAttribute('data-sorting-initialized', 'true');
    });
  };

  // Sort table by column
  const sortTableByColumn = (table, columnIndex, headerElement) => {
    const tbody = table.querySelector('tbody');
    if (!tbody) return;
    
    const rows = Array.from(tbody.querySelectorAll('tr'));
    const isCurrentlyAsc = headerElement.classList.contains('sort-asc');
    const isCurrentlyDesc = headerElement.classList.contains('sort-desc');
    
    // Clear all sort indicators in this table
    table.querySelectorAll('th').forEach(th => {
      th.classList.remove('sort-asc', 'sort-desc');
    });
    
    // Determine sort direction
    let sortDirection = 'asc';
    if (isCurrentlyAsc) {
      sortDirection = 'desc';
      headerElement.classList.add('sort-desc');
    } else {
      sortDirection = 'asc';
      headerElement.classList.add('sort-asc');
    }
    
    // Sort rows
    rows.sort((a, b) => {
      const aCell = a.cells[columnIndex];
      const bCell = b.cells[columnIndex];
      
      if (!aCell || !bCell) return 0;
      
      const aText = aCell.textContent.trim();
      const bText = bCell.textContent.trim();
      
      // Try to parse as numbers first
      const aNum = parseFloat(aText);
      const bNum = parseFloat(bText);
      
      let comparison = 0;
      if (!isNaN(aNum) && !isNaN(bNum)) {
        comparison = aNum - bNum;
      } else {
        comparison = aText.localeCompare(bText, undefined, { numeric: true, sensitivity: 'base' });
      }
      
      return sortDirection === 'desc' ? -comparison : comparison;
    });
    
    // Re-append sorted rows
    rows.forEach(row => tbody.appendChild(row));
  };

  // Set up observer for dynamic content
  const setupTableObserver = () => {
    // Create a MutationObserver to watch for new tables added to the DOM
    const observer = new MutationObserver((mutations) => {
      mutations.forEach((mutation) => {
        if (mutation.type === 'childList') {
          // Check if any added nodes contain tables
          mutation.addedNodes.forEach((node) => {
            if (node.nodeType === Node.ELEMENT_NODE) {
              // Check if the node itself is a table or contains tables
              const tables = node.tagName === 'TABLE' ? [node] : node.querySelectorAll('table');
              if (tables.length > 0) {
                // Re-run table initialization for new tables
                initializeAllTables();
              }
            }
          });
        }
      });
    });

    // Start observing the document body for child changes
    observer.observe(document.body, {
      childList: true,
      subtree: true
    });
  };

  // Initialize theme on page load
  document.addEventListener('DOMContentLoaded', () => {
    initTheme();

    // Add click handler to theme toggle button
    const themeToggle = document.getElementById('themeToggle');
    if (themeToggle) {
      themeToggle.addEventListener('click', toggleTheme);
    }
    
    // Initialize tables with sorting
    initializeAllTables();
    
    // Set up observer for dynamic content
    setupTableObserver();
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
        <svg class="table-search-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="11" cy="11" r="8"></circle>
          <path d="m21 21-4.35-4.35"></path>
        </svg>
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

    // Special handling for timestamp column in log tables
    if (key === 'timestamp') {
      return [...data].sort((a, b) => {
        // Construct full timestamp from date and time fields if available
        let aTimestamp = '';
        let bTimestamp = '';

        if (a.timestamp) {
          aTimestamp = a.timestamp;
        } else if (a.date && a.time) {
          aTimestamp = `${a.date} ${a.time}`;
        } else if (a.date) {
          aTimestamp = a.date;
        } else if (a.time) {
          aTimestamp = a.time;
        }

        if (b.timestamp) {
          bTimestamp = b.timestamp;
        } else if (b.date && b.time) {
          bTimestamp = `${b.date} ${b.time}`;
        } else if (b.date) {
          bTimestamp = b.date;
        } else if (b.time) {
          bTimestamp = b.time;
        }

        // Parse as dates and compare
        const dateA = new Date(aTimestamp);
        const dateB = new Date(bTimestamp);

        // Handle invalid dates
        if (isNaN(dateA.getTime()) && isNaN(dateB.getTime())) return 0;
        if (isNaN(dateA.getTime())) return direction === 'asc' ? 1 : -1;
        if (isNaN(dateB.getTime())) return direction === 'asc' ? -1 : 1;

        const result = dateA - dateB;
        return direction === 'desc' ? -result : result;
      });
    }

    // Special handling for time column in events table
    if (key === 'time') {
      return [...data].sort((a, b) => {
        const aTime = a.time || '';
        const bTime = b.time || '';

        // Parse as dates and compare (these should be Unix timestamps or ISO strings)
        const dateA = new Date(aTime);
        const dateB = new Date(bTime);

        // Handle invalid dates
        if (isNaN(dateA.getTime()) && isNaN(dateB.getTime())) return 0;
        if (isNaN(dateA.getTime())) return direction === 'asc' ? 1 : -1;
        if (isNaN(dateB.getTime())) return direction === 'asc' ? -1 : 1;

        const result = dateA - dateB;
        return direction === 'desc' ? -result : result;
      });
    }

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
        { key: 'timestamp', sortable: true },
        { key: 'level', sortable: true },
        { key: 'message', sortable: true }
      ],
      'deployment-log-table': [
        { key: 'timestamp', sortable: true },
        { key: 'stage', sortable: true },
        { key: 'task', sortable: true },
        { key: 'index', sortable: true },
        { key: 'state', sortable: true },
        { key: 'progress', sortable: true },
        { key: 'tags', sortable: false },
        { key: 'status', sortable: true }
      ],
      'debug-log-table': [
        { key: 'timestamp', sortable: true },
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
      ],
      'service-instances-table': [
        { key: 'name', sortable: true },
        { key: 'type', sortable: true },
        { key: 'status', sortable: true },
        { key: 'bindings', sortable: true }
      ],
      'marketplace-services-table': [
        { key: 'service', sortable: true },
        { key: 'description', sortable: true },
        { key: 'plans', sortable: true },
        { key: 'available', sortable: true }
      ],
      'tasks-table': [
        { key: 'id', sortable: true },
        { key: 'state', sortable: true },
        { key: 'description', sortable: true },
        { key: 'deployment', sortable: true },
        { key: 'user', sortable: true },
        { key: 'started', sortable: true },
        { key: 'duration', sortable: true }
      ],
      'configs-table': [
        { key: 'id', sortable: true },
        { key: 'name', sortable: true },
        { key: 'type', sortable: true },
        { key: 'team', sortable: true },
        { key: 'created_at', sortable: true }
      ]
    };

    // Check if this is an instance logs table (dynamic class name)
    if (tableClass.startsWith('instance-logs-table-')) {
      return [
        { key: 'timestamp', sortable: true },
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

  // Store event listeners for cleanup
  const tableHeaderListeners = new Map();

  // Initialize sorting on a table
  const initializeSorting = (tableClass) => {
    const table = document.querySelector(`.${tableClass}`);
    if (!table) return;

    // Clean up existing listeners for this table
    if (tableHeaderListeners.has(tableClass)) {
      const listeners = tableHeaderListeners.get(tableClass);
      listeners.forEach(({ element, handler }) => {
        element.removeEventListener('click', handler);
      });
      tableHeaderListeners.delete(tableClass);
    }

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
    const listeners = [];

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
      header.classList.add('sortable-header');

      // Create click handler
      const clickHandler = () => {
        handleTableSort(tableClass, column.key);
      };

      // Remove any existing click listeners and add new one
      header.removeEventListener('click', clickHandler); // Remove if exists
      header.addEventListener('click', clickHandler);

      // Store reference for cleanup
      listeners.push({ element: header, handler: clickHandler });
    });

    // Store listeners for this table
    if (listeners.length > 0) {
      tableHeaderListeners.set(tableClass, listeners);
    }
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
      'debug-log-table': 'debug-logs',
      'tasks-table': 'tasks',
      'configs-table': 'configs'
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
        const taskId = event.task_id || event.task;
        let taskInfo = '-';

        // Make task ID clickable if it exists and is numeric
        if (taskId && taskId !== '-' && !isNaN(taskId)) {
          taskInfo = `<a href="#" class="task-link" data-task-id="${taskId}" onclick="showTaskDetails(${taskId}, event); return false;">${taskId}</a>`;
        } else if (taskId) {
          taskInfo = taskId;
        }

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
            <td class="vm-instance">
              ${instanceName !== '-' ? `
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${instanceName}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${instanceName}')"
                          title="Copy to clipboard">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                </span>
              ` : '-'}
            </td>
            <td class="vm-state ${stateClass}">${vm.state || '-'}</td>
            <td class="vm-job-state ${jobStateClass}">${vm.job_state || '-'}</td>
            <td class="vm-az">
              ${vm.az && vm.az !== '-' ? `
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${vm.az}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${vm.az}')"
                          title="Copy to clipboard">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                </span>
              ` : '-'}
            </td>
            <td class="vm-type">
              ${vmType !== '-' ? `
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${vmType}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${vmType}')"
                          title="Copy to clipboard">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                </span>
              ` : '-'}
            </td>
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
    } else if (tableClass === 'tasks-table') {
      tbody.innerHTML = data.map(task => {
        const startedAt = formatTimestamp(task.started_at);

        // Calculate duration
        let duration = '-';
        if (task.started_at) {
          const start = new Date(task.started_at);
          const end = task.ended_at ? new Date(task.ended_at) : new Date();
          const durationMs = end - start;
          if (durationMs > 0) {
            const minutes = Math.floor(durationMs / 60000);
            const seconds = Math.floor((durationMs % 60000) / 1000);
            if (minutes > 0) {
              duration = `${minutes}m ${seconds}s`;
            } else {
              duration = `${seconds}s`;
            }
          }
        }

        // Create clickable task ID for details
        const taskIdLink = `<a href="#" class="task-link" data-task-id="${task.id}" onclick="showTaskDetails(${task.id}, event); return false;">${task.id}</a>`;

        // Format state with badge
        const stateBadge = `<span class="task-state ${task.state}">${task.state}</span>`;

        return `
          <tr>
            <td class="task-id">${taskIdLink}</td>
            <td class="task-state">${stateBadge}</td>
            <td class="task-description">${task.description || '-'}</td>
            <td class="task-deployment">${task.deployment || '-'}</td>
            <td class="task-user">${task.user || '-'}</td>
            <td class="task-started">${startedAt}</td>
            <td class="task-duration">${duration}</td>
          </tr>
        `;
      }).join('');
    } else if (tableClass === 'configs-table') {
      tbody.innerHTML = data.map(config => {
        const createdAt = formatTimestamp(config.created_at);

        // Create clickable config ID for details
        const configIdLink = `<a href="#" class="config-link" data-config-id="${config.id}" onclick="showConfigDetails('${config.id}', event); return false;">${config.id}</a>`;

        // Format type with badge
        const typeBadge = `<span class="config-type ${config.type}">${config.type}</span>`;

        return `
          <tr>
            <td class="config-id">${configIdLink}</td>
            <td class="config-name">${config.name || '-'}</td>
            <td class="config-type">${typeBadge}</td>
            <td class="config-team">${config.team || '-'}</td>
            <td class="config-created">${createdAt}</td>
          </tr>
        `;
      }).join('');
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
    // Return '-' for missing, null, undefined, or 0 timestamps
    if (!timestamp || timestamp === 0) {
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
      <table class="service-info-table w-full bg-white dark:bg-gray-900 rounded-lg overflow-hidden shadow-sm border border-gray-200 dark:border-gray-700 max-h-[calc(100vh-200px)] overflow-y-auto">
        <tbody>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Deployment</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${deploymentName}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${deploymentName}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Environment</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${environment}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${environment}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Total Service Instances</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${totalInstances}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${totalInstances}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Total Plans</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${totalPlans}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${totalPlans}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Status</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span>${status}</span>
            </td>
          </tr>
          ${data.boshDNS ? `
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">BOSH DNS</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${data.boshDNS}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${data.boshDNS}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
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
        <button class="detail-tab" data-tab="plans">Plans</button>
        <button class="detail-tab" data-tab="broker">Broker</button>
        <button class="detail-tab" data-tab="blacksmith-logs">Logs</button>
        <button class="detail-tab" data-tab="vms">VMs</button>
        <button class="detail-tab" data-tab="events">Events</button>
        <button class="detail-tab" data-tab="tasks">Tasks</button>
        <button class="detail-tab" data-tab="configs">Configs</button>
        <button class="detail-tab" data-tab="certificates">Certificates</button>
        <button class="detail-tab" data-tab="manifest">Manifest</button>
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
      <table class="plan-details-table w-full bg-white dark:bg-gray-900 rounded-lg overflow-hidden shadow-sm border border-gray-200 dark:border-gray-700">
        <thead class="bg-gray-50 dark:bg-gray-800">
          <tr>
            <th colspan="2" class="px-4 py-3 text-center font-bold text-gray-800 dark:text-gray-200 bg-gray-50 dark:bg-gray-800 border-b-2 border-gray-200 dark:border-gray-600 text-sm uppercase tracking-wider">Plan Information</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Service</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${service?.name || service?.id || 'unknown'}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${service?.name || service?.id || 'unknown'}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Plan</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <span>${plan?.name || plan?.id || 'unknown'}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${plan?.name || plan?.id || 'unknown'}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </span>
            </td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Description</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">${plan.description || '-'}</td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Current Instances</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">${plan.blacksmith.instances}</td>
          </tr>
          <tr>
            <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Instance Limit</td>
            <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">${plan.blacksmith.limit > 0 ? plan.blacksmith.limit : plan.blacksmith.limit == 0 ? 'Unlimited' : 'Not Set'}</td>
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
        <table class="plan-vms-table w-full bg-white dark:bg-gray-900 rounded-lg overflow-hidden shadow-sm border border-gray-200 dark:border-gray-700">
          <thead class="bg-gray-50 dark:bg-gray-800">
            <tr>
              <th class="px-4 py-3 text-left font-semibold border-b-2 border-gray-200 dark:border-gray-600 bg-gray-50 dark:bg-gray-800 text-gray-600 dark:text-gray-400 whitespace-nowrap text-xs uppercase tracking-wider">VM Name</th>
              <th class="px-4 py-3 text-left font-semibold border-b-2 border-gray-200 dark:border-gray-600 bg-gray-50 dark:bg-gray-800 text-gray-600 dark:text-gray-400 whitespace-nowrap text-xs uppercase tracking-wider">Count</th>
              <th class="px-4 py-3 text-left font-semibold border-b-2 border-gray-200 dark:border-gray-600 bg-gray-50 dark:bg-gray-800 text-gray-600 dark:text-gray-400 whitespace-nowrap text-xs uppercase tracking-wider">Type</th>
              <th class="px-4 py-3 text-left font-semibold border-b-2 border-gray-200 dark:border-gray-600 bg-gray-50 dark:bg-gray-800 text-gray-600 dark:text-gray-400 whitespace-nowrap text-xs uppercase tracking-wider">Persistent Disk</th>
              <th class="px-4 py-3 text-left font-semibold border-b-2 border-gray-200 dark:border-gray-600 bg-gray-50 dark:bg-gray-800 text-gray-600 dark:text-gray-400 whitespace-nowrap text-xs uppercase tracking-wider">Properties</th>
            </tr>
          </thead>
          <tbody>
            ${plan.vms.map(vm => `
              <tr class="hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors duration-150">
                <td class="px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-medium text-sm">${vm.name || '-'}</td>
                <td class="px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm">${vm.instances || 1}</td>
                <td class="px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm">${vm.vm_type || '-'}</td>
                <td class="px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm">${vm.persistent_disk_type || '-'}</td>
                <td class="px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-xs max-w-xs overflow-hidden text-ellipsis">${vm.properties ? JSON.stringify(vm.properties, null, 2) : '-'}</td>
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

    // Create service id to name mapping from catalog
    const serviceIdToName = {};
    const hasCatalog = window.plansData && window.plansData.services && window.plansData.services.length > 0;

    if (hasCatalog) {
      window.plansData.services.forEach(service => {
        if (service && service.id && service.name) {
          serviceIdToName[service.id] = service.name;
        }
      });
    }

    // Extract unique services and plans for filter dropdowns
    const services = new Set();
    const plansPerService = {};

    instancesList.forEach(([id, details]) => {
      if (details.service_id) {
        // Only add services that have a proper name mapping from the catalog
        const serviceName = serviceIdToName[details.service_id];
        if (serviceName) {
          services.add(serviceName);
          if (!plansPerService[serviceName]) {
            plansPerService[serviceName] = new Set();
          }
          if (details.plan && details.plan.name) {
            plansPerService[serviceName].add(details.plan.name);
          }
        }
      }
    });

    const serviceOptions = Array.from(services).sort().map(s =>
      `<option value="${s}">${s}</option>`
    ).join('');

    // Only show filter if we have catalog data and services
    const showFilter = hasCatalog && services.size > 0;

    const filterSection = `
      <div class="services-filter-section">
        ${showFilter ? `
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
        </div>` : `
        <div class="filter-row">
          <label>Service Filter:</label>
          <span style="color: #666; font-style: italic;">Catalog data not available</span>
        </div>`}
        <div class="filter-buttons">
          <button id="refresh-services" class="copy-deployment-names-btn" title="Refresh Service Instances">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="m3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            <span></span>
          </button>
          <button id="copy-deployment-names" class="copy-deployment-names-btn" title="Copy Service Instance Deployment Names">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            <span>Deployments</span>
          </button>
          ${showFilter ? '<button id="clear-filters" class="clear-filters-btn">Clear</button>' : ''}
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

        // Use service name for display and data attribute
        // Only use mapped service names for filtering, show service_id for display if no mapping
        const serviceName = serviceIdToName[details.service_id] || '';
        const displayServiceName = serviceName || details.service_id;

        return `
            <div class="service-item ${isDeleting ? 'deleting' : ''}" data-instance-id="${id}" data-service="${serviceName}" data-plan="${details.plan?.name || ''}">
              <div class="service-id">${id}</div>
              ${details.instance_name ? `<div class="service-instance-name">${details.instance_name}</div>` : ''}
              <div class="service-meta">
                ${displayServiceName} / ${details.plan?.name || details.plan_id || 'unknown'} @ ${details.created ? strftime("%Y-%m-%d %H:%M:%S", details.created) : 'Unknown'}
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
    // vaultData now contains merged data from both secret/{instance-id} and secret/{instance-id}/metadata

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
          console.log(`Field ${field.key}: ${value}`); // Debug logging
          // Format different value types
          if (typeof value === 'object' && value !== null) {
            // Convert objects to JSON string
            value = JSON.stringify(value, null, 2);
          } else if ((field.key === 'requested_at' || field.key === 'delete_requested_at' || field.key === 'deprovision_requested_at') && value) {
            // Format timestamp fields
            value = formatTimestamp(value);
          }
          tableRows.push(`
            <tr>
              <td class="info-key" style="font-size: 16px;">
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${field.label}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${field.key}')"
                          title="Copy key name">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                </span>
              </td>
              <td class="info-value" style="font-size: 16px;">
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${value || '-'}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(value || '-').toString().replace(/'/g, "\\'")}')"
                          title="Copy ${field.label}">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
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
          let value = vaultData[key];

          // Format different value types
          if (typeof value === 'object' && value !== null) {
            // Convert objects to JSON string
            value = JSON.stringify(value, null, 2);
          } else if (typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(value)) {
            // Format timestamp fields (detect ISO 8601 format)
            value = new Date(value).toLocaleString();
          }

          tableRows.push(`
            <tr>
              <td class="info-key" style="font-size: 16px;">
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${key}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${key}')"
                          title="Copy key name">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
                </span>
              </td>
              <td class="info-value" style="font-size: 16px;">
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <span>${value || '-'}</span>
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(value || '-').toString().replace(/'/g, "\\'")}')"
                          title="Copy value">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                  </button>
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
          <td class="info-key" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, 'instance_id')"
                      title="Copy key name">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>Instance ID</span>
            </span>
          </td>
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
          <td class="info-key" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, 'service_id')"
                      title="Copy key name">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>Service</span>
            </span>
          </td>
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
          <td class="info-key" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, 'plan_id')"
                      title="Copy key name">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>Plan</span>
            </span>
          </td>
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
          <td class="info-key" style="font-size: 16px;">
            <span class="copy-wrapper">
              <button class="copy-btn-inline" onclick="window.copyValue(event, 'created_at')"
                      title="Copy key name">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
              <span>Created At</span>
            </span>
          </td>
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
      <table class="service-info-table w-full bg-white dark:bg-gray-900 rounded-lg overflow-hidden shadow-sm border border-gray-200 dark:border-gray-700 max-h-[calc(100vh-200px)] overflow-y-auto">
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
        <button class="detail-tab" data-tab="manifest">Manifest</button>
        <button class="detail-tab" data-tab="certificates">Certificates</button>
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
      <table class="credentials-table w-full bg-white dark:bg-gray-900 rounded-lg overflow-hidden shadow-sm border border-gray-200 dark:border-gray-700">
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
                <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                  <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${copyValue.toString().replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
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
              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${copyValue.toString().replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
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
              <span>${displayValue}</span>
              <button class="copy-btn-inline" onclick="window.copyValue(event, '${copyValue.toString().replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
                      title="Copy to clipboard">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
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

      } else if (type === 'config') {
        // Fetch blacksmith configuration
        const response = await fetch('/b/blacksmith/config');
        if (!response.ok) {
          throw new Error(`Failed to load config: ${response.statusText}`);
        }
        const config = await response.json();
        return formatConfig(config);
      } else if (type === 'certificates') {
        // Render certificates tab content
        return renderCertificatesTab();
      } else if (type === 'broker') {
        // Render broker tab content with CF endpoints management
        return renderBrokerTab();
      } else if (type === 'tasks') {
        // Fetch BOSH director tasks
        const response = await fetch('/b/tasks?type=recent&limit=100', { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const tasks = await response.json();
        return formatTasks(tasks);
      } else if (type === 'configs') {
        // Fetch BOSH director configs
        const response = await fetch('/b/configs?limit=100', { cache: 'no-cache' });
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        const configs = await response.json();
        return formatConfigs(configs);
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

    if (type === 'certificates') {
      return renderServiceCertificatesTab(instanceId);
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
      } else if (type === 'debug') {
        const text = await response.text();
        try {
          const logs = JSON.parse(text);
          return formatDebugLog(logs, instanceId);
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

    // Refresh function for service instance
    const refreshFunction = instanceId
      ? `window.refreshServiceInstanceDeploymentLog('${instanceId}', event)`
      : '';

    return `
      <div class="deployment-log-wrapper">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter('deployment-log-table', 'Search deployment logs...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.deployment-log-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
          ${instanceId ? `<button class="refresh-btn-logs" onclick="${refreshFunction}"
                  title="Refresh deployment logs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
          </button>` : ''}
        </div>
        <div class="deployment-log-table-container">
        <table class="deployment-log-table">
        <thead>
          <tr>
            <th>Timestamp</th>
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

    // Refresh function for service instance
    const refreshFunction = instanceId
      ? `window.refreshServiceInstanceDebugLog('${instanceId}', event)`
      : '';

    return `
      <div class="debug-log-wrapper">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter('debug-log-table', 'Search debug logs...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.debug-log-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
          ${instanceId ? `<button class="refresh-btn-logs" onclick="${refreshFunction}"
                  title="Refresh debug logs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
          </button>` : ''}
        </div>
        <div class="debug-log-table-container">
        <table class="debug-log-table">
        <thead>
          <tr>
            <th>Timestamp</th>
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
                  <th colspan="3" class="table-controls-header">
                    <div class="table-controls-container">
                      <div class="search-filter-container">
                        ${createSearchFilter(`instance-logs-table-${jobKey.replace(/\//g, '-')}`, 'Search logs...')}
                      </div>
                      <button class="copy-btn-logs" onclick="window.copyInstanceLogs('${jobKey}', event)"
                              title="Copy filtered table rows">
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                      <button class="refresh-btn-logs" onclick="window.refreshInstanceLogs('${jobKey}', event)"
                              title="Refresh logs">
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
                      </button>
                    </div>
                  </th>
                </tr>
                <tr>
                  <th class="log-col-timestamp">Timestamp</th>
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
                <th colspan="3" class="table-controls-header">
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
                      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                    </button>
                    <button class="refresh-btn-logs" onclick="window.refreshInstanceLogs('${jobKey}', event)"
                            title="Refresh logs">
                      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
                    </button>
                  </div>
                </th>
              </tr>
              <tr>
                <th class="log-col-timestamp">Timestamp</th>
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
          // Re-initialize sorting and restore search filter state after updating table
          setTimeout(() => {
            const tableClass = `instance-logs-table-${jobKey.replace(/\//g, '-')}`;
            initializeSorting(tableClass);
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
          // Re-initialize sorting and restore search filter state after updating table
          setTimeout(() => {
            const tableClass = `instance-logs-table-${jobKey.replace(/\//g, '-')}`;
            initializeSorting(tableClass);
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
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
        </button>
        <button class="refresh-btn-logs" onclick="${refreshFunction}"
                title="Refresh events">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
        </button>
      </div>
      <div class="events-table-container">
        <table class="${tableId} events-table">
          <thead>
            <tr>
              <th>Timestamp</th>
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

      const taskId = event.task_id || event.task;
      let taskInfo = '-';

      // Make task ID clickable if it exists and is numeric
      if (taskId && taskId !== '-' && !isNaN(taskId)) {
        taskInfo = `<a href="#" class="task-link" data-task-id="${taskId}" onclick="showTaskDetails(${taskId}, event); return false;">${taskId}</a>`;
      } else if (taskId) {
        taskInfo = taskId;
      }

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

  // Format tasks table
  const formatTasks = (tasks, dataKey = 'tasks') => {
    // Always store data for sorting, even if empty
    tableOriginalData.set(dataKey, tasks || []);

    const tableId = `tasks-table`;

    // Check if we have tasks
    const hasTasks = tasks && tasks.length > 0;

    return `
      <div class="table-controls-container compact-tasks-controls">
        <div class="tasks-controls-row-1">
          <div class="search-filter-container">
            ${createSearchFilter(tableId, 'Search tasks...')}
          </div>
          <div class="filter-group">
            <label>Type:</label>
            <select id="task-type-filter">
              <option value="recent">Recent</option>
              <option value="current">Current</option>
              <option value="all">All</option>
            </select>
          </div>
          <div class="filter-group">
            <label>Filter by:</label>
            <select id="task-filter">
              <option value="blacksmith">blacksmith</option>
            </select>
          </div>
          <div class="tasks-action-buttons">
            <button id="refresh-tasks-btn" class="refresh-btn" title="Refresh tasks">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            </button>
            <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableId}', event)"
                    title="Copy filtered table rows">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            </button>
          </div>
        </div>
        <div class="tasks-controls-row-2">
          <div class="filter-group">
            <label>States:</label>
            <div class="checkbox-group">
              <label><input type="checkbox" value="processing" checked> Processing</label>
              <label><input type="checkbox" value="queued" checked> Queued</label>
              <label><input type="checkbox" value="done" checked> Done</label>
              <label><input type="checkbox" value="error" checked> Error</label>
              <label><input type="checkbox" value="cancelled" checked> Cancelled</label>
            </div>
          </div>
        </div>
      </div>
      <div class="tasks-table-container">
        <table class="${tableId}">
          <thead>
            <tr>
              <th>ID</th>
              <th>State</th>
              <th>Description</th>
              <th>Deployment</th>
              <th>User</th>
              <th>Started</th>
              <th>Duration</th>
            </tr>
          </thead>
          <tbody>
            ${hasTasks ? tasks.map(task => {
      const startedAt = formatTimestamp(task.started_at);

      // Calculate duration
      let duration = '-';
      if (task.started_at) {
        const start = new Date(task.started_at);
        const end = task.ended_at ? new Date(task.ended_at) : new Date();
        const durationMs = end - start;
        if (durationMs > 0) {
          const minutes = Math.floor(durationMs / 60000);
          const seconds = Math.floor((durationMs % 60000) / 1000);
          if (minutes > 0) {
            duration = `${minutes}m ${seconds}s`;
          } else {
            duration = `${seconds}s`;
          }
        }
      }

      // Create clickable task ID for details
      const taskIdLink = `<a href="#" class="task-link" data-task-id="${task.id}" onclick="showTaskDetails(${task.id}, event); return false;">${task.id}</a>`;

      // Format state with badge
      const stateBadge = `<span class="task-state ${task.state}">${task.state}</span>`;

      return `
                <tr>
                  <td class="task-id">${taskIdLink}</td>
                  <td class="task-state">${stateBadge}</td>
                  <td class="task-description">${task.description || '-'}</td>
                  <td class="task-deployment">${task.deployment || '-'}</td>
                  <td class="task-user">${task.user || '-'}</td>
                  <td class="task-started">${startedAt}</td>
                  <td class="task-duration">${duration}</td>
                </tr>
              `;
    }).join('') : `
              <tr>
                <td colspan="7" class="no-data-row">No current tasks found</td>
              </tr>
            `}
          </tbody>
        </table>
      </div>
    `;
  };

  // Format configs table
  const formatConfigs = (configsData, dataKey = 'configs') => {
    // Always store data for sorting, even if empty
    const configs = configsData.configs || [];
    tableOriginalData.set(dataKey, configs);

    // Group configs by type
    const configsByType = {
      cloud: [],
      runtime: [],
      cpi: [],
      resurrection: []
    };

    configs.forEach(config => {
      if (configsByType[config.type]) {
        configsByType[config.type].push(config);
      }
    });

    // Sort each type's configs by name
    Object.keys(configsByType).forEach(type => {
      configsByType[type].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
    });

    const tableId = `configs-table`;

    // Check if we have configs
    const hasConfigs = configs && configs.length > 0;

    // Create the table body with configs grouped by type
    let tableBody = '';
    if (hasConfigs) {
      ['cloud', 'cpi', 'resurrection', 'runtime'].forEach(type => {
        const typeConfigs = configsByType[type];
        if (typeConfigs.length > 0) {
          typeConfigs.forEach(config => {
            const createdAt = formatTimestamp(config.created_at);
            
            // Add asterisk for active configs and make it clickable to show versions
            const isActive = config.is_active || config.id.endsWith('*');
            const cleanId = config.id.replace('*', '');
            const configIdDisplay = isActive ? `${cleanId}*` : cleanId;
            const configIdLink = `<a href="#" class="config-link" data-config-id="${cleanId}" data-config-type="${config.type}" data-config-name="${config.name}" onclick="showConfigVersions('${config.type}', '${config.name ? config.name.replace(/'/g, "\\'"): ''}', '${cleanId}', event); return false;">${configIdDisplay}</a>`;
            
            // Format team, show dash for empty
            const teamDisplay = config.team || '-';

            tableBody += `
              <tr data-config-type="${config.type}">
                <td class="config-id">${configIdLink}</td>
                <td class="config-type"><span class="config-type-badge ${config.type}">${config.type}</span></td>
                <td class="config-name">${config.name || '-'}</td>
                <td class="config-team">${teamDisplay}</td>
                <td class="config-created">${createdAt}</td>
              </tr>
            `;
          });
        }
      });
    } else {
      tableBody = `
        <tr>
          <td colspan="5" class="no-data-row">No configs found</td>
        </tr>
      `;
    }

    // Count configs by type for display
    const typeCounts = Object.keys(configsByType).map(type => {
      const count = configsByType[type].length;
      return count > 0 ? `${count} ${type}` : null;
    }).filter(Boolean).join(', ');

    return `
      <div class="table-controls-container compact-configs-controls">
        <div class="configs-controls-row-1">
          <div class="search-filter-container">
            ${createSearchFilter(tableId, 'Search configs...')}
          </div>
          <div class="configs-action-buttons">
            <button id="refresh-configs-btn" class="refresh-btn" title="Refresh configs">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
            </button>
            <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableId}', event)"
                    title="Copy filtered table rows">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            </button>
          </div>
        </div>
        <div class="configs-controls-row-2">
          <div class="filter-group">
            <label>Types:</label>
            <div class="checkbox-group">
              <label><input type="checkbox" value="cloud" checked> Cloud</label>
              <label><input type="checkbox" value="runtime" checked> Runtime</label>
              <label><input type="checkbox" value="cpi" checked> CPI</label>
              <label><input type="checkbox" value="resurrection" checked> Resurrection</label>
            </div>
          </div>
        </div>
      </div>
      ${hasConfigs ? `<div class="configs-summary">${configs.length} configs${typeCounts ? ': ' + typeCounts : ''}</div>` : ''}
      <div class="configs-table-container">
        <table class="${tableId}">
          <thead>
            <tr>
              <th>ID</th>
              <th>Type</th>
              <th>Name</th>
              <th>Team</th>
              <th>Created At</th>
            </tr>
          </thead>
          <tbody>
            ${tableBody}
          </tbody>
        </table>
      </div>
      ${hasConfigs ? `<div class="configs-note">(*) Currently active - Click ID to see all versions</div>` : ''}
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

    // BOSH Task Output format: Task 12345 | 00:06:55 | Stage: Description (00:00:00)
    const boshTaskPattern = /^Task\s+\d+\s+\|\s+(\d{2}:\d{2}:\d{2})\s+\|\s+(.+)$/;
    match = line.match(boshTaskPattern);
    if (match) {
      const currentDate = new Date().toISOString().split('T')[0]; // Use today's date as fallback
      return {
        date: currentDate,
        time: match[1],
        level: 'INFO',
        message: match[2]
      };
    }

    // BOSH Director format: LEVEL, [YYYY-MM-DDTHH:MM:SS.mmm #PID] [task:TASKID] LEVEL -- MODULE: message
    const boshPattern = /^([IDWEF]), \[(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}\.\d+) #\d+\] (\[(?:task:\d+|)\])\s*(\w+) -- ([^:]+):\s*(.*)$/;
    match = line.match(boshPattern);
    if (match) {
      const levelMap = {
        'I': 'INFO',
        'D': 'DEBUG',
        'W': 'WARN',
        'E': 'ERROR',
        'F': 'FATAL'
      };
      const level = levelMap[match[1]] || match[5];
      const taskInfo = match[4] !== '[]' ? match[4] + ' ' : '';

      return {
        date: match[2],
        time: match[3],
        level: level,
        message: `${taskInfo}[${match[6]}] ${match[7]}`
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
        timestamp: `${match[1]} ${match[2]}`,
        level: 'INFO',
        message: match[3]
      };
    }

    // Handle lines that don't match any pattern (continuation lines, etc.)
    // Try to extract level from simple patterns as fallback
    let fallbackLevel = '';
    let fallbackMessage = line;
    
    // Look for common level indicators
    const levelIndicators = /\b(INFO|DEBUG|WARN|WARNING|ERROR|FATAL|TRACE)\b/i;
    const levelMatch = line.match(levelIndicators);
    if (levelMatch) {
      fallbackLevel = levelMatch[1].toUpperCase();
    }
    
    // Try to extract basic timestamp if present at line start
    let fallbackDate = '';
    let fallbackTime = '';
    const timestampMatch = line.match(/^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2}(?:\.\d+)?)/);
    if (timestampMatch) {
      fallbackDate = timestampMatch[1];
      fallbackTime = timestampMatch[2];
      // Remove timestamp from message
      fallbackMessage = line.replace(/^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2}(?:\.\d+)?)\s*/, '');
    }
    
    return {
      date: fallbackDate,
      time: fallbackTime,
      level: fallbackLevel,
      message: fallbackMessage
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

    // Handle different timestamp formats
    let displayTimestamp = '-';
    if (logEntry.timestamp) {
      // Use timestamp if available (ISO format)
      displayTimestamp = logEntry.timestamp;
    } else if (logEntry.date && logEntry.time) {
      // Combine date and time if separate
      displayTimestamp = `${logEntry.date} ${logEntry.time}`;
    } else if (logEntry.date) {
      // Use just date if only date is available
      displayTimestamp = logEntry.date;
    } else if (logEntry.time) {
      // Use just time if only time is available
      displayTimestamp = logEntry.time;
    }

    return `
      <tr class="log-row ${levelClass}">
        <td class="log-timestamp">${displayTimestamp}</td>
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
            <th class="log-col-timestamp">Timestamp</th>
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
    tableOriginalData.set('logs-table', parsedLogs);  // Also store under table class name as fallback

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
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
          <button class="refresh-btn-logs" onclick="window.refreshBlacksmithLogs(event)"
                  title="Refresh logs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
          </button>
        </div>
        <div class="logs-table-container" id="blacksmith-logs-display">
          <table class="logs-table">
            <thead>
              <tr>
                <th class="log-col-timestamp">Timestamp</th>
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

    const result = `
      <div class="vms-table-wrapper">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter('vms-table', 'Search VMs...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.vms-table', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
          <button class="refresh-btn-logs" onclick="${refreshFunction}"
                  title="Refresh VMs">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
          </button>
          <div class="resurrection-toggle-container">
            <label class="resurrection-toggle-label">
              <span class="resurrection-label-text">Resurrection:</span>
              <div class="toggle-switch" onclick="window.toggleResurrection('${instanceId || 'blacksmith'}', event)">
                <div class="toggle-slider" id="resurrection-slider-${instanceId || 'blacksmith'}">
                  <span class="toggle-text-off">Off</span>
                  <span class="toggle-text-on">On</span>
                </div>
              </div>
            </label>
            <button class="delete-resurrection-btn"
                    id="delete-resurrection-btn-${instanceId || 'blacksmith'}"
                    onclick="window.deleteResurrectionConfig('${instanceId || 'blacksmith'}', event)"
                    title="Delete resurrection config"
                    style="display: none;">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <polyline points="3,6 5,6 21,6"></polyline>
                <path d="m19,6v14a2,2 0 0,1 -2,2H7a2,2 0 0,1 -2,-2V6m3,0V4a2,2 0 0,1 2,-2h4a2,2 0 0,1 2,2v2"></path>
                <line x1="10" y1="11" x2="10" y2="17"></line>
                <line x1="14" y1="11" x2="14" y2="17"></line>
              </svg>
            </button>
          </div>
        </div>
        <div class="vms-table-container">
          <table class="vms-table">
        <thead>
          <tr>
            <th>SSH</th>
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

      // Determine if this is a blacksmith or service instance context
      const isBlacksmith = !instanceId;
      const deploymentName = isBlacksmith ? 'blacksmith' : instanceId;
      const sshButtonClass = isBlacksmith ? 'blacksmith-ssh' : 'service-instance-ssh';
      const deploymentType = isBlacksmith ? 'blacksmith' : 'service-instance';

      return `
              <tr>
                <td class="vm-ssh">
                  <button class="ssh-btn ${sshButtonClass}"
                          onclick="window.openTerminal('${deploymentType}', '${deploymentName}', '${instanceName}', event)"
                          title="SSH to ${instanceName}">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                      <rect x="2" y="3" width="20" height="14" rx="2" ry="2"></rect>
                      <line x1="8" y1="21" x2="16" y2="21"></line>
                      <line x1="12" y1="17" x2="12" y2="21"></line>
                    </svg>
                  </button>
                </td>
                <td class="vm-instance">
                  ${instanceName !== '-' ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${instanceName}</span>
                      <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${instanceName}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-state ${stateClass}">${vm.state || '-'}</td>
                <td class="vm-job-state ${jobStateClass}">${vm.job_state || '-'}</td>
                <td class="vm-az">
                  ${vm.az && vm.az !== '-' ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${vm.az}</span>
                      <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${vm.az}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-type">
                  ${vmType !== '-' ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${vmType}</span>
                      <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${vmType}')"
                              title="Copy to clipboard">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                      </button>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-active">${active}</td>
                <td class="vm-bootstrap">${bootstrap}</td>
                <td class="vm-ips">
                  ${ips !== '-' ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${ips}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${ips}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-dns">${dns}</td>
                <td class="vm-cid">
                  ${vm.vm_cid ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${vm.vm_cid}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${vm.vm_cid}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-agent-id">
                  ${agentId !== '-' ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${agentId}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${agentId}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                    </span>
                  ` : '-'}
                </td>
                <td class="vm-created-at">${vmCreatedAt}</td>
                <td class="vm-disk-cids">
                  ${diskCids !== '-' ? `
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <span>${diskCids}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${diskCids}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
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

    // Initialize resurrection toggle state after a short delay to ensure DOM is ready
    setTimeout(() => {
      window.initializeResurrectionToggle(instanceId || 'blacksmith', vms);
    }, 100);

    return result;
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
      // Get credentials, config/manifest, and vault data for service detection
      const [credsResponse, configResponse, vaultResponse] = await Promise.all([
        fetch(`/b/${instanceId}/creds.json`, { cache: 'no-cache' }),
        fetch(`/b/${instanceId}/config`, { cache: 'no-cache' }).catch(() => null),
        fetch(`/b/${instanceId}/details`, { cache: 'no-cache' }).catch(() => null)
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

      // Get vault data for service type detection
      let vaultData = null;
      if (vaultResponse && vaultResponse.ok) {
        try {
          vaultData = await vaultResponse.json();
        } catch (e) {
          console.warn('Failed to parse vault data:', e);
        }
      }

      // Determine service type using vault data first, then credentials
      const isRedis = isRedisService(creds, vaultData);
      const isRabbitMQ = isRabbitMQService(creds, vaultData);

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

  const isRedisService = (creds, vaultData) => {
    // First check vault data for service_name
    if (vaultData?.service_name === 'redis') {
      return true;
    }

    // Then check for explicit service_type
    if (vaultData?.service_type === 'redis') {
      return true;
    }

    // Finally fall back to credential patterns for older instances
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

  const isRabbitMQService = (creds, vaultData) => {
    // First check vault data for service_name
    if (vaultData?.service_name === 'rabbitmq') {
      return true;
    }

    // Then check for explicit service_type
    if (vaultData?.service_type === 'rabbitmq') {
      return true;
    }

    // Finally fall back to credential patterns for older instances
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

  // Helper function to extract user/vhost info from credentials with sophisticated mapping
  const extractConnectionInfo = (creds, serviceType, manifestData = null) => {
    console.log('Debug: extractConnectionInfo called with:', {
      serviceType,
      credsKeys: Object.keys(creds),
      hasManifestData: !!manifestData
    });

    const info = {
      users: [],
      vhosts: [],
      userCredentials: {},    // Maps display name -> {username, password}
      vhostCredentials: {}    // Maps display name -> actual vhost
    };

    if (serviceType === 'rabbitmq') {

      // === MANIFEST-BASED CREDENTIALS ===
      if (manifestData) {
        console.log('Debug: Manifest data available:', Object.keys(manifestData));

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

        // Search in job properties for RabbitMQ credentials
        const findInJobProperties = (path) => {
          if (!manifestData.instance_groups) return null;

          // Look for rabbitmq job in instance groups
          for (const ig of manifestData.instance_groups) {
            if (ig.jobs) {
              for (const job of ig.jobs) {
                if (job.name === 'rabbitmq' && job.properties) {
                  const keys = path.split('.');
                  let current = job.properties;
                  for (const key of keys) {
                    if (current && typeof current === 'object' && current[key] !== undefined) {
                      current = current[key];
                    } else {
                      return null;
                    }
                  }
                  return current;
                }
              }
            }
          }
          return null;
        };

        // Extract manifest admin user from job properties
        const adminUser = findInJobProperties('rabbitmq.admin.user');
        const adminPass = findInJobProperties('rabbitmq.admin.pass');
        console.log('Debug: Found manifest admin user/pass:', adminUser, adminPass ? '[REDACTED]' : null);
        if (adminUser && adminPass) {
          info.users.push('manifest.admin.user');
          info.userCredentials['manifest.admin.user'] = {
            username: adminUser,
            password: adminPass
          };
        }

        // Extract manifest app user from job properties
        const appUser = findInJobProperties('rabbitmq.app.user');
        const appPass = findInJobProperties('rabbitmq.app.pass');
        console.log('Debug: Found manifest app user/pass:', appUser, appPass ? '[REDACTED]' : null);
        if (appUser && appPass) {
          info.users.push('manifest.app.user');
          info.userCredentials['manifest.app.user'] = {
            username: appUser,
            password: appPass
          };
        }

        // Extract manifest monitoring user from job properties
        const monitoringUser = findInJobProperties('rabbitmq.monitoring.user');
        const monitoringPass = findInJobProperties('rabbitmq.monitoring.pass');
        console.log('Debug: Found manifest monitoring user/pass:', monitoringUser, monitoringPass ? '[REDACTED]' : null);
        if (monitoringUser && monitoringPass) {
          info.users.push('manifest.monitoring.user');
          info.userCredentials['manifest.monitoring.user'] = {
            username: monitoringUser,
            password: monitoringPass
          };
        }

        // Extract manifest vhost from job properties
        const manifestVhost = findInJobProperties('rabbitmq.vhost');
        console.log('Debug: Found manifest vhost:', manifestVhost);
        if (manifestVhost) {
          info.vhosts.push('manifest.vhost');
          info.vhostCredentials['manifest.vhost'] = manifestVhost;
        }
      }

      // === BINDING CREDENTIALS ===

      // Binding user (username/password)
      if (creds.username && creds.password) {
        info.users.push('binding.user');
        info.userCredentials['binding.user'] = {
          username: creds.username,
          password: creds.password
        };
      }

      // Binding admin (admin_username/admin_password)
      if (creds.admin_username && creds.admin_password) {
        info.users.push('binding.admin');
        info.userCredentials['binding.admin'] = {
          username: creds.admin_username,
          password: creds.admin_password
        };
      }

      // Binding vhost
      if (creds.vhost) {
        info.vhosts.push('binding.vhost');
        info.vhostCredentials['binding.vhost'] = creds.vhost;
      }

      // Note: Protocol-based credentials are not exposed as user options
      // They're used internally by the backend but users should select manifest/binding credentials

      // === DEFAULT VHOST ===
      // Always include default vhost
      if (!info.vhosts.includes('/')) {
        info.vhosts.unshift('/');  // Add at beginning
        info.vhostCredentials['/'] = '/';
      }

      // === FALLBACKS ===
      // If no users found from any source, add basic fallbacks
      if (info.users.length === 0) {
        if (creds.username) {
          info.users.push('fallback.user');
          info.userCredentials['fallback.user'] = {
            username: creds.username,
            password: creds.password || ''
          };
        } else {
          // Last resort fallbacks
          info.users.push('fallback.admin', 'fallback.guest');
          info.userCredentials['fallback.admin'] = { username: 'admin', password: 'admin' };
          info.userCredentials['fallback.guest'] = { username: 'guest', password: 'guest' };
        }
      }
    }

    console.log('Debug: Final extracted connection info:', {
      users: info.users,
      vhosts: info.vhosts,
      userCredMappings: Object.keys(info.userCredentials),
      vhostCredMappings: Object.keys(info.vhostCredentials)
    });
    return info;
  };

  // Generate connection fields based on service type
  const generateConnectionFields = (instanceId, serviceType, creds, manifestData = null) => {
    const connectionInfo = extractConnectionInfo(creds, serviceType, manifestData);

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
      const userOptions = connectionInfo.users.map(user => {
        // Default to binding.user if available, otherwise select the first option
        const isSelected = user === 'binding.user' ||
          (connectionInfo.users.indexOf('binding.user') === -1 && user === connectionInfo.users[0]);
        return `<option value="${user}"${isSelected ? ' selected' : ''}>${user}</option>`;
      }).join('');

      const vhostOptions = connectionInfo.vhosts.map(vhost => {
        // Default to binding.vhost if available, otherwise select the first option
        const isSelected = vhost === 'binding.vhost' ||
          (connectionInfo.vhosts.indexOf('binding.vhost') === -1 && vhost === connectionInfo.vhosts[0]);
        return `<option value="${vhost}"${isSelected ? ' selected' : ''}>${vhost}</option>`;
      }).join('');

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
        <button class="operation-tab" data-operation="rabbitmqctl" data-instance="${instanceId}">RABBITMQCTL</button>
        <button class="operation-tab" data-operation="plugins" data-instance="${instanceId}">PLUGINS</button>
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
    const connectionFields = testingInstance ? generateConnectionFields(instanceId, testingInstance.serviceType, testingInstance.credentials, testingInstance.manifestData) : '';

    return `
      <table class="operation-form">
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
    const connectionFields = testingInstance ? generateConnectionFields(instanceId, testingInstance.serviceType, testingInstance.credentials, testingInstance.manifestData) : '';

    return `
      <table class="operation-form">
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
          <input type="text" id="rabbitmq-mgmt-path-${instanceId}" placeholder="/b/overview" />
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

  const renderRabbitMQCtlOperation = (instanceId) => {
    // Initialize the rabbitmqctl state for this instance if it doesn't exist
    window.rabbitmqCtl = window.rabbitmqCtl || {};
    window.rabbitmqCtl[instanceId] = window.rabbitmqCtl[instanceId] || {
      categories: [],
      selectedCategory: null,
      selectedCommand: null,
      commandHistory: [],
      activeExecution: null,
      websocket: null
    };

    // When switching to rabbitmqctl tab, hide the regular response history
    setTimeout(() => {
      const regularHistory = document.querySelector('.testing-history');
      if (regularHistory) {
        regularHistory.style.display = 'none';
      }
    }, 0);

    return `
      <div class="rabbitmqctl-container">
        <div class="rabbitmqctl-layout">
          <div class="rabbitmqctl-categories">
            <h5>Command Categories</h5>
            <div id="rabbitmqctl-categories-${instanceId}" class="categories-list">
              <div class="loading">Loading categories...</div>
            </div>
          </div>
          <div class="rabbitmqctl-command-details">
            <div id="rabbitmqctl-command-section-${instanceId}" class="command-section">
              <div class="no-data">Select a category to view available commands</div>
            </div>
            <div id="rabbitmqctl-execution-section-${instanceId}" class="execution-section">
              <div id="rabbitmqctl-command-help-${instanceId}" class="command-help"></div>
              <div id="rabbitmqctl-command-table-${instanceId}" class="command-table-container">
                <div class="no-data">Select a category and command to configure execution</div>
              </div>
            </div>
            <div class="rabbitmqctl-output-container">
              <div class="output-header">
                <h5>Command Output</h5>
                <div class="output-controls">
                  <button onclick="clearRabbitMQCtlOutput('${instanceId}')" class="clear-btn">Clear</button>
                  <button onclick="toggleRabbitMQCtlHistory('${instanceId}')" class="history-btn">History</button>
                </div>
              </div>
              <div id="rabbitmqctl-output-${instanceId}" class="command-output">
                <div class="no-data">No command executed yet</div>
              </div>
            </div>
            <div id="rabbitmqctl-history-${instanceId}" class="rabbitmqctl-history" style="display: none;">
              <h5>Command History</h5>
              <div id="rabbitmqctl-history-content-${instanceId}" class="history-content">
                <div class="no-data">No command history available</div>
              </div>
            </div>
          </div>
        </div>
        <div class="testing-history" id="rabbitmqctl-response-history-${instanceId}">
          <div class="history-header">
            <h5>Response History</h5>
            <button class="clear-history-btn" onclick="clearRabbitMQCtlResponseHistory('${instanceId}');">Clear History</button>
          </div>
          <div class="history-entries" id="history-entries-rabbitmqctl-${instanceId}">
            <div class="no-data">No commands executed yet</div>
          </div>
        </div>
      </div>
    `;
  };

  const renderRabbitMQPluginsOperation = (instanceId) => {
    // Initialize the rabbitmq-plugins state for this instance if it doesn't exist
    window.rabbitmqPlugins = window.rabbitmqPlugins || {};
    window.rabbitmqPlugins[instanceId] = window.rabbitmqPlugins[instanceId] || {
      categories: [],
      selectedCategory: null,
      selectedCommand: null,
      commandHistory: [],
      activeExecution: null,
      websocket: null
    };

    // When switching to plugins tab, hide the regular response history
    setTimeout(() => {
      const regularHistory = document.querySelector('.testing-history');
      if (regularHistory) {
        regularHistory.style.display = 'none';
      }
    }, 0);

    return `
      <div class="plugins-container">
        <div class="plugins-layout">
          <div class="plugins-categories">
            <h5>Plugin Categories</h5>
            <div id="plugins-categories-${instanceId}" class="categories-list">
              <div class="loading">Loading categories...</div>
            </div>
          </div>
          <div class="plugins-command-details">
            <div id="plugins-command-section-${instanceId}" class="command-section">
              <div class="no-data">Select a category to view available commands</div>
            </div>
            <div id="plugins-execution-section-${instanceId}" class="execution-section">
              <div id="plugins-command-help-${instanceId}" class="command-help"></div>
              <div id="plugins-command-table-${instanceId}" class="command-table-container">
                <div class="no-data">Select a category and command to configure execution</div>
              </div>
            </div>
            <div class="plugins-output-container">
              <div class="output-header">
                <h5>Command Output</h5>
                <div class="output-controls">
                  <button onclick="clearRabbitMQPluginsOutput('${instanceId}')" class="clear-btn">Clear</button>
                  <button onclick="toggleRabbitMQPluginsHistory('${instanceId}')" class="history-btn">History</button>
                </div>
              </div>
              <div id="plugins-output-${instanceId}" class="command-output">
                <div class="no-data">No command executed yet</div>
              </div>
            </div>
            <div id="plugins-history-${instanceId}" class="plugins-history" style="display: none;">
              <h5>Command History</h5>
              <div id="plugins-history-content-${instanceId}" class="history-content">
                <div class="no-data">No command history available</div>
              </div>
            </div>
          </div>
        </div>
        <div class="testing-history" id="plugins-response-history-${instanceId}">
          <div class="history-header">
            <h5>Response History</h5>
            <button class="clear-history-btn" onclick="clearRabbitMQPluginsResponseHistory('${instanceId}');">Clear History</button>
          </div>
          <div class="history-entries" id="history-entries-plugins-${instanceId}">
            <div class="no-data">No commands executed yet</div>
          </div>
        </div>
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
    // Use stored connection parameters if available, otherwise read from DOM
    const connectionState = window.serviceConnections && window.serviceConnections[instanceId];
    const connectionInfo = (connectionState && connectionState.connected && connectionState.connectionParams)
      ? connectionState.connectionParams
      : getConnectionInfo(instanceId);

    // Debug logging to show which connection parameters are being used
    if (connectionState && connectionState.connected && connectionState.connectionParams) {
      console.log(`Using stored connection parameters for ${serviceType}:`, connectionInfo);
    } else {
      console.log(`Using DOM connection parameters for ${serviceType}:`, connectionInfo);
    }

    // Add connection information to params
    if (serviceType === 'redis') {
      params.use_tls = connectionInfo.type === 'tls';
      params.connection_type = connectionInfo.type;
    } else if (serviceType === 'rabbitmq') {
      params.use_amqps = connectionInfo.type === 'amqps';
      params.connection_type = connectionInfo.type;

      // Resolve display names to actual credentials
      const testingInstance = window.currentTestingInstance;
      if (testingInstance && testingInstance.credentials) {
        const connInfo = extractConnectionInfo(testingInstance.credentials, serviceType, testingInstance.manifestData);

        // Resolve user credentials (username and password)
        if (connInfo.userCredentials && connInfo.userCredentials[connectionInfo.user]) {
          const userCreds = connInfo.userCredentials[connectionInfo.user];
          params.connection_user = userCreds.username;
          params.connection_password = userCreds.password;
        } else {
          // Fallback to display name if not found in mapping
          params.connection_user = connectionInfo.user;
        }

        // Resolve vhost
        if (connInfo.vhostCredentials && connInfo.vhostCredentials[connectionInfo.vhost]) {
          params.connection_vhost = connInfo.vhostCredentials[connectionInfo.vhost];
        } else {
          // Fallback to display name if not found in mapping
          params.connection_vhost = connectionInfo.vhost;
        }
      }
    }

    try {
      let url = `/b/${instanceId}/${serviceType}/${operation}`;
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

  // RabbitMQCtl Functions
  window.loadRabbitMQCtlCategories = async (instanceId) => {
    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/rabbitmqctl/categories`);
      const categories = await response.json();

      window.rabbitmqCtl[instanceId].categories = categories;

      const container = document.getElementById(`rabbitmqctl-categories-${instanceId}`);
      if (container) {
        container.innerHTML = categories.map(category =>
          `<div class="category-item" onclick="selectRabbitMQCtlCategory('${instanceId}', '${category.name}')">
            <div class="category-name">${category.display_name}</div>
            <div class="category-description">${category.description}</div>
            <div class="category-command-count">${category.commands.length} commands</div>
          </div>`
        ).join('');
      }
    } catch (error) {
      console.error('Failed to load rabbitmqctl categories:', error);
      const container = document.getElementById(`rabbitmqctl-categories-${instanceId}`);
      if (container) {
        container.innerHTML = '<div class="error">Failed to load categories</div>';
      }
    }
  };

  window.selectRabbitMQCtlCategory = async (instanceId, categoryName) => {
    const state = window.rabbitmqCtl[instanceId];
    state.selectedCategory = categoryName;
    state.selectedCommand = null;

    // Update category selection
    document.querySelectorAll(`#rabbitmqctl-categories-${instanceId} .category-item`).forEach(item => {
      item.classList.remove('active');
    });
    event.target.closest('.category-item').classList.add('active');

    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/rabbitmqctl/category/${categoryName}`);
      const category = await response.json();

      // Update command selector
      const commandSelect = document.getElementById(`rabbitmqctl-command-select-${instanceId}`);
      if (commandSelect) {
        commandSelect.innerHTML = '<option value="">Select a command...</option>' +
          category.commands.map(cmd =>
            `<option value="${cmd.name}">${cmd.name} - ${cmd.description}</option>`
          ).join('');
      }

      // Update command details section
      const commandSection = document.getElementById(`rabbitmqctl-command-section-${instanceId}`);
      if (commandSection) {
        commandSection.innerHTML = `
          <div class="category-info">
            <h6>${category.display_name}</h6>
            <p>${category.description}</p>
            <div class="commands-table">
              <table>
                <thead>
                  <tr>
                    <th>Command</th>
                    <th>Description</th>
                    <th>Arguments</th>
                  </tr>
                </thead>
                <tbody>
                  ${category.commands.map(cmd => `
                    <tr class="command-row" onclick="selectRabbitMQCtlCommandFromTable('${instanceId}', '${cmd.name}')">
                      <td class="command-name">${cmd.name}</td>
                      <td class="command-desc">${cmd.description}</td>
                      <td class="command-args">${cmd.arguments.length} args</td>
                    </tr>
                  `).join('')}
                </tbody>
              </table>
            </div>
          </div>
        `;
      }
    } catch (error) {
      console.error('Failed to load category commands:', error);
    }
  };

  window.selectRabbitMQCtlCommand = async (instanceId) => {
    const commandSelect = document.getElementById(`rabbitmqctl-command-select-${instanceId}`);
    const commandName = commandSelect.value;

    if (!commandName) return;

    await loadRabbitMQCtlCommandDetails(instanceId, commandName);
  };

  window.selectRabbitMQCtlCommandFromTable = async (instanceId, commandName) => {
    const commandSelect = document.getElementById(`rabbitmqctl-command-select-${instanceId}`);
    if (commandSelect) {
      commandSelect.value = commandName;
    }

    await loadRabbitMQCtlCommandDetails(instanceId, commandName);
  };

  const loadRabbitMQCtlCommandDetails = async (instanceId, commandName) => {
    const state = window.rabbitmqCtl[instanceId];
    const categoryName = state.selectedCategory;

    if (!categoryName) return;

    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/rabbitmqctl/category/${categoryName}/command/${commandName}`);
      const command = await response.json();

      state.selectedCommand = command;

      // Update command help
      const helpContainer = document.getElementById(`rabbitmqctl-command-help-${instanceId}`);
      if (helpContainer) {
        helpContainer.innerHTML = `
          <div class="command-help-content">
            <div class="command-usage">
              <strong>Usage:</strong> <code>${command.usage}</code>
            </div>
            <div class="command-description">
              <strong>Description:</strong> ${command.description}
            </div>
            ${command.examples.length > 0 ? `
              <div class="command-examples">
                <strong>Examples:</strong>
                ${command.examples.map(ex => `<code>${ex}</code>`).join('<br>')}
              </div>
            ` : ''}
            ${command.dangerous ? '<div class="warning">⚠️ This is a dangerous command that may affect the system</div>' : ''}
          </div>
        `;
      }

      // Update command table section
      const tableContainer = document.getElementById(`rabbitmqctl-command-table-${instanceId}`);
      if (tableContainer) {
        tableContainer.innerHTML = `
          <div class="command-execution-table">
            <table class="rabbitmqctl-table">
              <thead>
                <tr>
                  <th>rabbitmqctl</th>
                  <th>Command</th>
                  <th>Command Arguments</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td class="command-base">rabbitmqctl</td>
                  <td class="command-name-cell">${command.name}</td>
                  <td>
                    <input type="text"
                           id="rabbitmqctl-command-args-${instanceId}"
                           placeholder="${command.arguments.map(arg => arg.required ? `<${arg.name}>` : `[${arg.name}]`).join(' ')}"
                           class="command-args-input" />
                  </td>
                  <td>
                    <button id="rabbitmqctl-execute-btn-${instanceId}"
                            class="execute-btn"
                            onclick="executeRabbitMQCtlCommand('${instanceId}')">
                      Run
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          ${command.arguments.length > 0 ? `
            <div class="arguments-help">
              <h6>Available Arguments:</h6>
              <div class="arguments-list">
                ${command.arguments.map(arg => `
                  <div class="argument-help">
                    <strong>${arg.name}</strong> (${arg.type})${arg.required ? ' - Required' : ' - Optional'}
                    <br><small>${arg.description}${arg.default ? ` (default: ${arg.default})` : ''}</small>
                  </div>
                `).join('')}
              </div>
            </div>
          ` : ''}
        `;
      }

    } catch (error) {
      console.error('Failed to load command details:', error);
    }
  };

  window.executeRabbitMQCtlCommand = async (instanceId) => {
    const state = window.rabbitmqCtl[instanceId];
    const command = state.selectedCommand;

    if (!command) return;

    // Gather command arguments
    const commandArgs = document.getElementById(`rabbitmqctl-command-args-${instanceId}`)?.value || '';

    // Build arguments array from command arguments
    const args = [];
    if (commandArgs.trim()) {
      args.push(...commandArgs.trim().split(/\s+/));
    }

    try {
      const executeBtn = document.getElementById(`rabbitmqctl-execute-btn-${instanceId}`);
      executeBtn.disabled = true;
      executeBtn.textContent = 'Executing...';

      const response = await fetch(`/b/${instanceId}/rabbitmq/rabbitmqctl/execute`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          category: state.selectedCategory,
          command: command.name,
          arguments: args
        })
      });

      const result = await response.json();

      displayRabbitMQCtlResult(instanceId, result);

    } catch (error) {
      console.error('Command execution failed:', error);
      displayRabbitMQCtlError(instanceId, error.message);
    } finally {
      const executeBtn = document.getElementById(`rabbitmqctl-execute-btn-${instanceId}`);
      executeBtn.disabled = false;
      executeBtn.textContent = 'Run';
    }
  };

  // Streaming functionality removed as requested

  const displayRabbitMQCtlResult = (instanceId, result) => {
    const outputContainer = document.getElementById(`rabbitmqctl-output-${instanceId}`);
    if (outputContainer) {
      outputContainer.innerHTML = `
        <div class="command-result ${result.success ? 'success' : 'error'}">
          <div class="result-header">
            <span class="result-status">${result.success ? '✓' : '✗'}</span>
            <span class="result-command">${result.command}</span>
            <span class="result-time">${new Date(result.timestamp * 1000).toLocaleString()}</span>
            <span class="result-exit-code">Exit Code: ${result.exit_code}</span>
          </div>
          <div class="result-output">
            <pre>${result.output || 'No output'}</pre>
          </div>
        </div>
      `;
    }

    // Add to command history
    const state = window.rabbitmqCtl[instanceId];
    if (state) {
      const historyEntry = {
        category: state.selectedCategory,
        command: result.command,
        arguments: result.arguments || [],
        timestamp: result.timestamp,
        success: result.success,
        exit_code: result.exit_code,
        output: result.output || 'No output'
      };

      state.commandHistory.unshift(historyEntry);

      // Keep only last 20 entries
      if (state.commandHistory.length > 20) {
        state.commandHistory = state.commandHistory.slice(0, 20);
      }

      // Update history display if visible
      updateRabbitMQCtlHistory(instanceId);

      // Also update the response history section
      updateRabbitMQCtlResponseHistory(instanceId, historyEntry);
    }
  };

  const displayRabbitMQCtlError = (instanceId, error) => {
    const outputContainer = document.getElementById(`rabbitmqctl-output-${instanceId}`);
    if (outputContainer) {
      outputContainer.innerHTML = `
        <div class="command-result error">
          <div class="result-header">
            <span class="result-status">✗</span>
            <span class="result-error">Execution Error</span>
          </div>
          <div class="result-error">${error}</div>
        </div>
      `;
    }
  };

  window.clearRabbitMQCtlOutput = (instanceId) => {
    const outputContainer = document.getElementById(`rabbitmqctl-output-${instanceId}`);
    if (outputContainer) {
      outputContainer.innerHTML = '<div class="no-data">Output cleared</div>';
    }
  };

  const appendRabbitMQCtlOutput = (instanceId, text) => {
    const outputContainer = document.getElementById(`rabbitmqctl-output-${instanceId}`);
    if (outputContainer) {
      if (outputContainer.querySelector('.no-data')) {
        outputContainer.innerHTML = '<pre class="stream-output"></pre>';
      }
      const pre = outputContainer.querySelector('.stream-output');
      if (pre) {
        pre.textContent += text;
        pre.scrollTop = pre.scrollHeight;
      }
    }
  };

  window.toggleRabbitMQCtlHistory = (instanceId) => {
    const historyContainer = document.getElementById(`rabbitmqctl-history-${instanceId}`);
    if (historyContainer) {
      if (historyContainer.style.display === 'none') {
        historyContainer.style.display = 'block';
        updateRabbitMQCtlHistory(instanceId);
      } else {
        historyContainer.style.display = 'none';
      }
    }
  };

  const updateRabbitMQCtlHistory = (instanceId) => {
    const state = window.rabbitmqCtl[instanceId];
    const historyContent = document.getElementById(`rabbitmqctl-history-content-${instanceId}`);

    if (historyContent && state) {
      const history = state.commandHistory;

      if (history.length > 0) {
        historyContent.innerHTML = `
          ${history.map(entry => `
            <div class="history-entry">
              <div class="history-header">
                <span class="history-command">${entry.category}.${entry.command}</span>
                <span class="history-time">${new Date(entry.timestamp * 1000).toLocaleString()}</span>
                <span class="history-status ${entry.success ? 'success' : 'error'}">${entry.success ? '✓' : '✗'}</span>
              </div>
              ${entry.arguments.length > 0 ? `<div class="history-args">Args: ${entry.arguments.join(' ')}</div>` : ''}
              <div class="history-output">
                <pre>${entry.output.substring(0, 200)}${entry.output.length > 200 ? '...' : ''}</pre>
              </div>
            </div>
          `).join('')}
          <button onclick="clearRabbitMQCtlHistory('${instanceId}')" class="clear-btn">Clear History</button>
        `;
      } else {
        historyContent.innerHTML = '<div class="no-data">No command history available</div>';
      }
    }
  };

  window.showRabbitMQCtlHistory = async (instanceId) => {
    // Now just toggle the history visibility
    toggleRabbitMQCtlHistory(instanceId);
  };

  window.clearRabbitMQCtlHistory = (instanceId) => {
    const state = window.rabbitmqCtl[instanceId];
    if (state) {
      state.commandHistory = [];
      updateRabbitMQCtlHistory(instanceId);
    }
  };

  // Functions for the Response History section in rabbitmqctl
  const updateRabbitMQCtlResponseHistory = (instanceId, entry) => {
    const historyContainer = document.getElementById(`history-entries-rabbitmqctl-${instanceId}`);
    if (!historyContainer) return;

    const timestamp = new Date(entry.timestamp * 1000).toLocaleString();
    const statusClass = entry.success ? 'success' : 'error';
    const statusIcon = entry.success ? '✓' : '✗';

    const newEntry = document.createElement('div');
    newEntry.className = `history-entry ${statusClass}`;
    newEntry.innerHTML = `
      <div class="history-entry-header">
        <span class="history-operation">${statusIcon} ${entry.category}.${entry.command}</span>
        <span class="history-timestamp">${timestamp}</span>
      </div>
      <div class="history-entry-body">
        <div class="history-params">
          <strong>Arguments:</strong>
          <pre>${entry.arguments.length > 0 ? entry.arguments.join(' ') : 'None'}</pre>
        </div>
        <div class="history-result">
          <strong>Output:</strong>
          <pre>${entry.output}</pre>
        </div>
      </div>
    `;

    // Remove "no data" message if present
    const noData = historyContainer.querySelector('.no-data');
    if (noData) {
      noData.remove();
    }

    // Insert at the beginning
    historyContainer.insertBefore(newEntry, historyContainer.firstChild);

    // Keep only last 20 entries
    const entries = historyContainer.querySelectorAll('.history-entry');
    if (entries.length > 20) {
      entries[entries.length - 1].remove();
    }
  };

  window.clearRabbitMQCtlResponseHistory = (instanceId) => {
    const historyContainer = document.getElementById(`history-entries-rabbitmqctl-${instanceId}`);
    if (historyContainer) {
      historyContainer.innerHTML = '<div class="no-data">No commands executed yet</div>';
    }
  };

  // Legacy function for compatibility
  window.showRabbitMQCtlHistoryOld = async (instanceId) => {
    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/rabbitmqctl/history`);
      const history = await response.json();

      const historyContainer = document.getElementById(`rabbitmqctl-history-${instanceId}`);
      if (historyContainer) {
        if (history.length > 0) {
          historyContainer.innerHTML = `
            <h5>Command History</h5>
            <div class="history-content">
              ${history.map(entry => `
                <div class="history-entry">
                  <div class="history-header">
                    <span class="history-command">${entry.category}.${entry.command}</span>
                    <span class="history-time">${new Date(entry.timestamp * 1000).toLocaleString()}</span>
                    <span class="history-status ${entry.success ? 'success' : 'error'}">${entry.success ? '✓' : '✗'}</span>
                  </div>
                  <div class="history-args">${entry.arguments.join(' ')}</div>
                  <div class="history-output">
                    <pre>${entry.output.substring(0, 200)}${entry.output.length > 200 ? '...' : ''}</pre>
                  </div>
                </div>
              `).join('')}
            </div>
            <button onclick="clearRabbitMQCtlHistory('${instanceId}')" class="clear-btn">Clear History</button>
          `;
        } else {
          historyContainer.innerHTML = `
            <h5>Command History</h5>
            <div class="no-data">No command history available</div>
          `;
        }
        historyContainer.style.display = 'block';
      }
    } catch (error) {
      console.error('Failed to load history:', error);
    }
  };

  window.clearRabbitMQCtlHistory = async (instanceId) => {
    if (!confirm('Are you sure you want to clear all command history?')) {
      return;
    }

    try {
      await fetch(`/b/${instanceId}/rabbitmq/rabbitmqctl/history`, {
        method: 'DELETE'
      });

      const historyContainer = document.getElementById(`rabbitmqctl-history-${instanceId}`);
      if (historyContainer) {
        historyContainer.innerHTML = `
          <h5>Command History</h5>
          <div class="no-data">Command history cleared</div>
        `;
      }
    } catch (error) {
      console.error('Failed to clear history:', error);
    }
  };

  // RabbitMQ Plugins Functions
  window.loadRabbitMQPluginsCategories = async (instanceId) => {
    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/plugins/categories`);
      const categories = await response.json();

      window.rabbitmqPlugins[instanceId].categories = categories;

      const container = document.getElementById(`plugins-categories-${instanceId}`);
      if (container) {
        container.innerHTML = categories.map(category =>
          `<div class="category-item" onclick="selectRabbitMQPluginsCategory('${instanceId}', '${category.name}')">
            <div class="category-name">${category.display_name}</div>
            <div class="category-description">${category.description}</div>
            <div class="category-command-count">${category.commands.length} commands</div>
          </div>`
        ).join('');
      }
    } catch (error) {
      console.error('Failed to load plugins categories:', error);
      const container = document.getElementById(`plugins-categories-${instanceId}`);
      if (container) {
        container.innerHTML = '<div class="error">Failed to load categories</div>';
      }
    }
  };

  window.selectRabbitMQPluginsCategory = async (instanceId, categoryName) => {
    const state = window.rabbitmqPlugins[instanceId];
    state.selectedCategory = categoryName;
    state.selectedCommand = null;

    // Update category selection
    document.querySelectorAll(`#plugins-categories-${instanceId} .category-item`).forEach(item => {
      item.classList.remove('active');
    });
    event.target.closest('.category-item').classList.add('active');

    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/plugins/category/${categoryName}`);
      const category = await response.json();

      // Check if response is valid and has commands
      if (!category || !category.commands || !Array.isArray(category.commands)) {
        throw new Error(`Invalid category response: ${JSON.stringify(category)}`);
      }

      // Update command section
      const commandSection = document.getElementById(`plugins-command-section-${instanceId}`);
      if (commandSection) {
        commandSection.innerHTML = `
          <div class="category-info">
            <h6>${category.display_name}</h6>
            <p>${category.description}</p>
            <div class="commands-table">
              <table>
                <thead>
                  <tr>
                    <th>Command</th>
                    <th>Description</th>
                    <th>Arguments</th>
                  </tr>
                </thead>
                <tbody>
                  ${category.commands.map(cmd => `
                    <tr class="command-row" onclick="selectRabbitMQPluginsCommandFromTable('${instanceId}', '${cmd.name}')">
                      <td class="command-name">${cmd.name}</td>
                      <td class="command-desc">${cmd.description}</td>
                      <td class="command-args">${cmd.arguments ? cmd.arguments.length : 0} args</td>
                    </tr>
                  `).join('')}
                </tbody>
              </table>
            </div>
          </div>
        `;
      }
    } catch (error) {
      console.error('Failed to load plugins commands:', error);
      // Show error in UI
      const commandSection = document.getElementById(`plugins-command-section-${instanceId}`);
      if (commandSection) {
        commandSection.innerHTML = `
          <div class="error-message">
            <p>Failed to load commands for category "${categoryName}". Please try again.</p>
          </div>
        `;
      }
    }
  };

  window.selectRabbitMQPluginsCommand = async (instanceId) => {
    const select = document.getElementById(`plugins-command-select-${instanceId}`);
    const commandName = select.value;

    if (!commandName) return;

    selectRabbitMQPluginsCommandFromTable(instanceId, commandName);
  };

  window.selectRabbitMQPluginsCommandFromTable = async (instanceId, commandName) => {
    const state = window.rabbitmqPlugins[instanceId];
    state.selectedCommand = commandName;

    await loadRabbitMQPluginsCommandDetails(instanceId, commandName);
  };

  const loadRabbitMQPluginsCommandDetails = async (instanceId, commandName) => {
    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/plugins/category/${window.rabbitmqPlugins[instanceId].selectedCategory}/command/${commandName}`);
      const command = await response.json();

      // Display command help
      const helpContainer = document.getElementById(`plugins-command-help-${instanceId}`);
      if (helpContainer) {
        helpContainer.innerHTML = `
          <div class="command-help-content">
            <h6>${command.name}</h6>
            <p>${command.description}</p>
            <div class="command-usage">
              <strong>Usage:</strong> <code>${command.usage}</code>
            </div>
            ${command.examples && command.examples.length > 0 ? `
              <div class="command-examples">
                <strong>Examples:</strong>
                <ul>
                  ${command.examples.map(ex => `<li><code>${ex}</code></li>`).join('')}
                </ul>
              </div>
            ` : ''}
            ${command.dangerous ? '<div class="warning">⚠️ This is a dangerous command. Use with caution.</div>' : ''}
          </div>
        `;
      }

      // Update command table section
      const tableContainer = document.getElementById(`plugins-command-table-${instanceId}`);
      if (tableContainer) {
        tableContainer.innerHTML = `
          <div class="command-execution-table">
            <table class="plugins-table">
              <thead>
                <tr>
                  <th>rabbitmq-plugins</th>
                  <th>Command</th>
                  <th>Arguments</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr id="plugins-command-row-${instanceId}">
                  <td>rabbitmq-plugins</td>
                  <td>${command.name}</td>
                  <td>
                    <div class="argument-inputs">
                      ${command.arguments ? command.arguments.map(arg => `
                        <div class="argument-group">
                          <label>${arg.name}${arg.required ? '*' : ''}:</label>
                          <input type="${arg.type === 'string[]' ? 'text' : 'text'}"
                                 id="plugins-arg-${arg.name}-${instanceId}"
                                 placeholder="${arg.description}"
                                 ${arg.required ? 'required' : ''}>
                          ${arg.type === 'string[]' ? '<small>Space-separated for multiple values</small>' : ''}
                        </div>
                      `).join('') : ''}
                      ${command.options ? command.options.map(opt => `
                        <div class="option-group">
                          <label>
                            <input type="checkbox"
                                   id="plugins-opt-${opt.name}-${instanceId}"
                                   value="${opt.name}">
                            --${opt.name}${opt.short ? ` (-${opt.short})` : ''}
                          </label>
                          <small>${opt.description}</small>
                        </div>
                      `).join('') : ''}
                    </div>
                  </td>
                  <td>
                    <div class="command-actions">
                      <button onclick="executeRabbitMQPluginsCommand('${instanceId}')" class="btn btn-primary">Run</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        `;
      }
    } catch (error) {
      console.error('Failed to load plugins command details:', error);
    }
  };

  window.executeRabbitMQPluginsCommand = async (instanceId) => {
    const state = window.rabbitmqPlugins[instanceId];
    if (!state.selectedCommand) {
      alert('Please select a command first');
      return;
    }

    // Collect arguments
    const command = state.categories
      .find(cat => cat.name === state.selectedCategory)
      ?.commands.find(cmd => cmd.name === state.selectedCommand);

    const cmdArguments = [];

    // Collect argument inputs
    if (command.arguments) {
      for (const arg of command.arguments) {
        const input = document.getElementById(`plugins-arg-${arg.name}-${instanceId}`);
        if (input && input.value) {
          if (arg.type === 'string[]') {
            cmdArguments.push(...input.value.split(/\s+/).filter(v => v));
          } else {
            cmdArguments.push(input.value);
          }
        } else if (arg.required) {
          alert(`${arg.name} is required`);
          return;
        }
      }
    }

    // Collect option checkboxes
    if (command.options) {
      for (const opt of command.options) {
        const checkbox = document.getElementById(`plugins-opt-${opt.name}-${instanceId}`);
        if (checkbox && checkbox.checked) {
          cmdArguments.push(`--${opt.name}`);
        }
      }
    }

    try {
      displayRabbitMQPluginsResult(instanceId, {
        output: 'Executing command...',
        success: true,
        loading: true
      });

      const response = await fetch(`/b/${instanceId}/rabbitmq/plugins/execute`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          category: state.selectedCategory,
          command: state.selectedCommand,
          arguments: cmdArguments
        })
      });

      const result = await response.json();
      displayRabbitMQPluginsResult(instanceId, result);

      // Add to response history
      updateRabbitMQPluginsResponseHistory(instanceId, {
        category: state.selectedCategory,
        command: state.selectedCommand,
        arguments: cmdArguments,
        result: result,
        timestamp: Date.now()
      });

    } catch (error) {
      displayRabbitMQPluginsError(instanceId, error.message);
    }
  };

  const displayRabbitMQPluginsResult = (instanceId, result) => {
    const outputContainer = document.getElementById(`plugins-output-${instanceId}`);
    if (outputContainer) {
      if (result.loading) {
        outputContainer.innerHTML = `
          <div class="loading-output">
            <div class="loading-spinner"></div>
            <span>Executing command...</span>
          </div>
        `;
      } else {
        const statusClass = result.success ? 'success' : 'error';
        const statusIcon = result.success ? '✅' : '❌';

        outputContainer.innerHTML = `
          <div class="command-result ${statusClass}">
            <div class="result-header">
              <span class="status">${statusIcon} ${result.success ? 'Success' : 'Failed'}</span>
              ${result.exit_code !== undefined ? `<span class="exit-code">Exit Code: ${result.exit_code}</span>` : ''}
            </div>
            <div class="result-output">
              <pre>${result.output || (result.error || 'No output')}</pre>
            </div>
          </div>
        `;
      }

      outputContainer.scrollTop = outputContainer.scrollHeight;
    }
  };

  const displayRabbitMQPluginsError = (instanceId, error) => {
    const outputContainer = document.getElementById(`plugins-output-${instanceId}`);
    if (outputContainer) {
      outputContainer.innerHTML = `
        <div class="command-result error">
          <div class="result-header">
            <span class="status">❌ Error</span>
          </div>
          <div class="result-output">
            <pre>${error}</pre>
          </div>
        </div>
      `;
    }
  };

  window.clearRabbitMQPluginsOutput = (instanceId) => {
    const outputContainer = document.getElementById(`plugins-output-${instanceId}`);
    if (outputContainer) {
      outputContainer.innerHTML = '<div class="no-data">No command executed yet</div>';
    }
  };

  window.toggleRabbitMQPluginsHistory = (instanceId) => {
    const historyContainer = document.getElementById(`plugins-history-${instanceId}`);

    if (historyContainer) {
      const isVisible = historyContainer.style.display !== 'none';
      historyContainer.style.display = isVisible ? 'none' : 'block';

      if (!isVisible) {
        showRabbitMQPluginsHistory(instanceId);
      }
    }
  };

  window.showRabbitMQPluginsHistory = async (instanceId) => {
    try {
      const response = await fetch(`/b/${instanceId}/rabbitmq/plugins/history`);
      const history = await response.json();

      const historyContent = document.getElementById(`plugins-history-content-${instanceId}`);
      if (historyContent) {
        if (history && history.length > 0) {
          historyContent.innerHTML = `
            <div class="history-entries">
              ${history.map(entry => `
                <div class="history-entry">
                  <div class="history-header">
                    <span class="history-command">${entry.category}.${entry.command}</span>
                    <span class="history-time">${new Date(entry.timestamp).toLocaleString()}</span>
                    <span class="history-status ${entry.success ? 'success' : 'error'}">${entry.success ? '✓' : '✗'}</span>
                  </div>
                  <div class="history-args">${(entry.arguments || []).join(' ')}</div>
                  <div class="history-output">
                    <pre>${(entry.output_sample || '').substring(0, 200)}${(entry.output_sample || '').length > 200 ? '...' : ''}</pre>
                  </div>
                </div>
              `).join('')}
            </div>
            <button onclick="clearRabbitMQPluginsHistory('${instanceId}')" class="clear-btn">Clear History</button>
          `;
        } else {
          historyContent.innerHTML = '<div class="no-data">No command history available</div>';
        }
      }
    } catch (error) {
      console.error('Failed to load plugins history:', error);
      const historyContent = document.getElementById(`plugins-history-content-${instanceId}`);
      if (historyContent) {
        historyContent.innerHTML = '<div class="error">Failed to load history</div>';
      }
    }
  };

  window.clearRabbitMQPluginsHistory = async (instanceId) => {
    if (confirm('Are you sure you want to clear the plugins command history?')) {
      try {
        await fetch(`/b/${instanceId}/rabbitmq/plugins/history`, {
          method: 'DELETE'
        });

        const historyContent = document.getElementById(`plugins-history-content-${instanceId}`);
        if (historyContent) {
          historyContent.innerHTML = '<div class="no-data">No command history available</div>';
        }
      } catch (error) {
        console.error('Failed to clear plugins history:', error);
      }
    }
  };

  const updateRabbitMQPluginsResponseHistory = (instanceId, entry) => {
    const historyContainer = document.getElementById(`history-entries-plugins-${instanceId}`);
    if (!historyContainer) return;

    // Remove no-data message if present
    const noData = historyContainer.querySelector('.no-data');
    if (noData) {
      historyContainer.innerHTML = '';
    }

    const timestamp = new Date(entry.timestamp).toLocaleString();
    const statusClass = entry.result?.success ? 'success' : 'error';
    const statusIcon = entry.result?.success ? '✅' : '❌';

    const entryElement = document.createElement('div');
    entryElement.className = 'response-entry';
    entryElement.innerHTML = `
      <div class="response-header">
        <span class="response-command">${entry.category}.${entry.command}</span>
        <span class="response-time">${timestamp}</span>
        <span class="response-status ${statusClass}">${statusIcon}</span>
      </div>
      <div class="response-args">Arguments: ${entry.arguments.join(' ') || 'none'}</div>
      <div class="response-output">
        <pre>${(entry.result?.output || entry.result?.error || 'No output').substring(0, 300)}${((entry.result?.output || entry.result?.error || '').length > 300) ? '...' : ''}</pre>
      </div>
    `;

    historyContainer.insertBefore(entryElement, historyContainer.firstChild);

    // Keep only last 50 entries
    const entries = historyContainer.querySelectorAll('.response-entry');
    if (entries.length > 50) {
      for (let i = 50; i < entries.length; i++) {
        entries[i].remove();
      }
    }
  };

  window.clearRabbitMQPluginsResponseHistory = (instanceId) => {
    const historyContainer = document.getElementById(`history-entries-plugins-${instanceId}`);
    if (historyContainer) {
      historyContainer.innerHTML = '<div class="no-data">No commands executed yet</div>';
    }
  };

  window.streamRabbitMQPluginsCommand = async (instanceId) => {
    const state = window.rabbitmqPlugins[instanceId];
    if (!state.selectedCommand) {
      alert('Please select a command first');
      return;
    }

    // Collect arguments (same as execute)
    const command = state.categories
      .find(cat => cat.name === state.selectedCategory)
      ?.commands.find(cmd => cmd.name === state.selectedCommand);

    const cmdArguments = [];

    // Collect argument inputs
    if (command.arguments) {
      for (const arg of command.arguments) {
        const input = document.getElementById(`plugins-arg-${arg.name}-${instanceId}`);
        if (input && input.value) {
          if (arg.type === 'string[]') {
            cmdArguments.push(...input.value.split(/\s+/).filter(v => v));
          } else {
            cmdArguments.push(input.value);
          }
        } else if (arg.required) {
          alert(`${arg.name} is required`);
          return;
        }
      }
    }

    // Collect option checkboxes
    if (command.options) {
      for (const opt of command.options) {
        const checkbox = document.getElementById(`plugins-opt-${opt.name}-${instanceId}`);
        if (checkbox && checkbox.checked) {
          cmdArguments.push(`--${opt.name}`);
        }
      }
    }

    // Clear output and show connecting message
    const outputContainer = document.getElementById(`plugins-output-${instanceId}`);
    if (outputContainer) {
      outputContainer.innerHTML = `
        <div class="streaming-output">
          <div class="streaming-header">
            <span>🔄 Connecting to stream...</span>
          </div>
          <pre class="streaming-content"></pre>
        </div>
      `;
    }

    // Close existing websocket if any
    if (state.websocket) {
      state.websocket.close();
    }

    try {
      // Create WebSocket connection
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${protocol}//${window.location.host}/b/${instanceId}/rabbitmq/plugins/stream`;
      state.websocket = new WebSocket(wsUrl);

      state.websocket.onopen = () => {
        console.log('Plugins WebSocket connected');
        const headerElement = outputContainer.querySelector('.streaming-header span');
        if (headerElement) {
          headerElement.textContent = '🔴 Streaming...';
        }

        // Send execute command
        state.websocket.send(JSON.stringify({
          type: 'execute',
          category: state.selectedCategory,
          command: state.selectedCommand,
          arguments: cmdArguments
        }));
      };

      state.websocket.onmessage = (event) => {
        const message = JSON.parse(event.data);
        const contentElement = outputContainer.querySelector('.streaming-content');

        switch (message.type) {
          case 'execution_started':
            if (contentElement) {
              contentElement.textContent += `Starting execution: ${message.command}\n`;
            }
            break;
          case 'output':
            if (contentElement) {
              contentElement.textContent += message.data;
              contentElement.scrollTop = contentElement.scrollHeight;
            }
            break;
          case 'execution_completed':
            const headerElement = outputContainer.querySelector('.streaming-header span');
            if (headerElement) {
              const statusIcon = message.success ? '✅' : '❌';
              headerElement.textContent = `${statusIcon} Completed (exit code: ${message.exit_code})`;
            }
            if (contentElement) {
              contentElement.textContent += `\n--- Execution completed ---\n`;
            }
            // Add to response history
            updateRabbitMQPluginsResponseHistory(instanceId, {
              category: state.selectedCategory,
              command: state.selectedCommand,
              arguments: cmdArguments,
              result: {
                success: message.success,
                exit_code: message.exit_code,
                output: contentElement?.textContent || ''
              },
              timestamp: Date.now()
            });
            break;
          case 'error':
            if (contentElement) {
              contentElement.textContent += `\nError: ${message.error}\n`;
            }
            const errorHeaderElement = outputContainer.querySelector('.streaming-header span');
            if (errorHeaderElement) {
              errorHeaderElement.textContent = '❌ Error occurred';
            }
            break;
        }
      };

      state.websocket.onerror = (error) => {
        console.error('Plugins WebSocket error:', error);
        const headerElement = outputContainer.querySelector('.streaming-header span');
        if (headerElement) {
          headerElement.textContent = '❌ Connection error';
        }
      };

      state.websocket.onclose = () => {
        console.log('Plugins WebSocket connection closed');
        const headerElement = outputContainer.querySelector('.streaming-header span');
        if (headerElement && headerElement.textContent.includes('Connecting')) {
          headerElement.textContent = '❌ Connection failed';
        }
        state.websocket = null;
      };

    } catch (error) {
      console.error('Failed to create plugins WebSocket:', error);
      displayRabbitMQPluginsError(instanceId, 'Failed to establish streaming connection');
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

        // Initialize rabbitmqctl tab
        if (operation === 'rabbitmqctl') {
          loadRabbitMQCtlCategories(instanceId);
        }

        // Initialize plugins tab
        if (operation === 'plugins') {
          loadRabbitMQPluginsCategories(instanceId);
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
        case 'management':
          // Show regular history for non-rabbitmqctl tabs
          setTimeout(() => {
            const regularHistory = document.querySelector('.testing-history');
            if (regularHistory && !regularHistory.id?.includes('rabbitmqctl')) {
              regularHistory.style.display = 'block';
            }
          }, 0);
          return renderRabbitMQManagementOperation(instanceId);
        case 'rabbitmqctl':
          // Show regular history for other tabs
          setTimeout(() => {
            const regularHistory = document.querySelector('.testing-history');
            if (regularHistory && !regularHistory.id?.includes('rabbitmqctl')) {
              regularHistory.style.display = 'block';
            }
          }, 0);
          return renderRabbitMQCtlOperation(instanceId);
        case 'plugins':
          // Hide regular history for plugins tab
          setTimeout(() => {
            const regularHistory = document.querySelector('.testing-history');
            if (regularHistory && !regularHistory.id?.includes('plugins')) {
              regularHistory.style.display = 'none';
            }
          }, 0);
          return renderRabbitMQPluginsOperation(instanceId);
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
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${directorUuid.replace(/'/g, "\\'")}')"
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
                    <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                      <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(parsed.name || 'Not specified').replace(/'/g, "\\'")}')"
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
                              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                <span>${ig.instances || 1}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${ig.instances || 1}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                              </span>
                            </td>
                          </tr>
                          <tr>
                            <td>AZs</td>
                            <td>
                              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${((ig.azs || []).join(', ') || 'None').replace(/'/g, "\\'")}')"
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
                              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${((ig.networks || []).map(n => n.name || n).join(', ') || 'None').replace(/'/g, "\\'")}')"
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
                              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(ig.vm_type || 'Not specified').replace(/'/g, "\\'")}')"
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
                              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(ig.persistent_disk_type || 'None').replace(/'/g, "\\'")}')"
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
                              <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(ig.stemcell || 'default').replace(/'/g, "\\'")}')"
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
                                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                            <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(job.release || 'Not specified').replace(/'/g, "\\'")}')"
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
                                            <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                                              <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${copyValue.replace(/'/g, "\\'").replace(/\n/g, "\\n")}')"
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
                      <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                        <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${release.name.replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                        <span>${release.name}</span>
                      </span>
                    </td>
                    <td>
                      <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                        <span>${release.version || 'latest'}</span>
                        <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(release.version || 'latest').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                      </span>
                    </td>
                    <td>
                      ${release.url ? `
                        <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                          <a href="${release.url}" target="_blank" style="word-break: break-all;">${release.url}</a>
                          <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${release.url.replace(/'/g, "\\'")}')"
                                  title="Copy URL">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                          </button>
                        </span>
                      ` : 'N/A'}
                    </td>
                    <td>
                      ${release.sha1 && release.sha1 !== 'N/A' ? `
                        <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                          <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${release.sha1.replace(/'/g, "\\'")}')"
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
                      <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                        <span>${stemcell.alias || 'default'}</span>
                        <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(stemcell.alias || 'default').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                      </span>
                    </td>
                    <td>
                      <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                        <span>${stemcell.os || 'Not specified'}</span>
                        <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(stemcell.os || 'Not specified').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                      </span>
                    </td>
                    <td>
                      <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                        <span>${stemcell.version || 'latest'}</span>
                        <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${(stemcell.version || 'latest').replace(/'/g, "\\'")}')"
                                title="Copy to clipboard">
                          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
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
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
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
    const button = event && event.currentTarget || null;
    const displayContainer = document.querySelector('.vms-table-container');

    if (!displayContainer) {
      console.error('VMs container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('vms-table');

    // Add spinning animation to refresh button (if button exists)
    if (button) {
      button.classList.add('refreshing');
      button.disabled = true;
    }

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

      // Visual feedback for successful refresh (if button exists)
      if (button) {
        button.classList.add('success');
        const spanElement = button.querySelector('span');
        if (spanElement) {
          const originalText = spanElement.textContent;
          spanElement.textContent = 'Refreshed!';
          setTimeout(() => {
            button.classList.remove('success');
            spanElement.textContent = originalText;
          }, 1000);
        }
      }

    } catch (error) {
      console.error('Failed to refresh VMs:', error);

      // Visual feedback for error (if button exists)
      if (button) {
        button.classList.add('error');
        setTimeout(() => {
          button.classList.remove('error');
        }, 2000);
      }
    } finally {
      // Remove spinning animation (if button exists)
      if (button) {
        button.classList.remove('refreshing');
        button.disabled = false;
      }
    }
  };

  // Handler for refreshing service instance VMs
  window.refreshServiceInstanceVMs = async (instanceId, event) => {
    const button = event && event.currentTarget || null;
    const displayContainer = document.querySelector('.vms-table-container');

    if (!displayContainer) {
      console.error('Service VMs container not found');
      return;
    }

    // Capture current search filter state
    const currentSearchFilter = captureSearchFilterState('vms-table');

    // Add spinning animation to refresh button if available
    if (button) {
      button.classList.add('refreshing');
      button.disabled = true;
    }

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

      // Visual feedback for successful refresh (if button exists)
      if (button) {
        button.classList.add('success');
        const spanElement = button.querySelector('span');
        if (spanElement) {
          const originalText = spanElement.textContent;
          spanElement.textContent = 'Refreshed!';
          setTimeout(() => {
            button.classList.remove('success');
            spanElement.textContent = originalText;
          }, 1000);
        }
      }

    } catch (error) {
      console.error('Failed to refresh service VMs:', error);

      // Visual feedback for error (if button exists)
      if (button) {
        button.classList.add('error');
        setTimeout(() => {
          button.classList.remove('error');
        }, 2000);
      }
    } finally {
      // Remove spinning animation (if button exists)
      if (button) {
        button.classList.remove('refreshing');
        button.disabled = false;
      }
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
        // Parse the logs and store them for sorting/filtering
        const parsedLogs = logs.split('\n').filter(line => line.trim()).map(line => parseLogLine(line));

        // Store under both keys to ensure data is available for sorting
        tableOriginalData.set('blacksmith-logs', parsedLogs);
        tableOriginalData.set('logs-table', parsedLogs);  // Also store under table class name as fallback

        // Update the display with formatted logs
        // Pass false for includeSearchFilter since the search filter already exists in logs-controls-row
        const tableHTML = renderLogsTable(logs, parsedLogs, false);
        displayContainer.innerHTML = tableHTML;

        // Re-attach the search filter functionality and initialize sorting
        initializeSorting('logs-table');
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

      // Tab-specific initialization
      // (broker now handled as a sub-tab under blacksmith)

      // Re-initialize sorting for any tables in the newly activated tab
      setTimeout(() => {
        const tables = targetPanel.querySelectorAll('table[class*="table"]');
        tables.forEach(table => {
          // Extract table class name(s) that contain "table"
          const classList = Array.from(table.classList);
          const tableClass = classList.find(cls => cls.includes('table'));
          if (tableClass) {
            // Re-initialize sorting to ensure click handlers are properly attached
            initializeSorting(tableClass);
          }
        });
      }, 10);
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
        // Plans will show this message when the sub-tab is accessed
        window.plansData = null;
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


      // Store plans data for later use when Plans sub-tab is accessed
      if (catalog.services && catalog.services.length > 0) {
        window.plansData = catalog;
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
                  console.log('Vault data received:', vaultData); // Debug logging
                }
              } catch (error) {
                console.error('Failed to fetch vault data:', error);
              }

              // Fetch manifest to extract deployment name if not already in vault data
              if (vaultData && !vaultData.deployment_name) {
                try {
                  const manifestResponse = await fetch(`/b/${instanceId}/manifest-details`, { cache: 'no-cache' });
                  if (manifestResponse.ok) {
                    const manifestData = await manifestResponse.json();
                    if (manifestData && manifestData.parsed && manifestData.parsed.name) {
                      vaultData.deployment_name = manifestData.parsed.name;
                      console.log(`Extracted deployment name from manifest for ${instanceId}: ${vaultData.deployment_name}`);
                    }
                  }
                } catch (error) {
                  console.error('Failed to fetch manifest data:', error);
                }
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

          // Create service id to name mapping from catalog
          const serviceIdToName = {};
          if (window.plansData && window.plansData.services) {
            window.plansData.services.forEach(service => {
              if (service && service.id && service.name) {
                serviceIdToName[service.id] = service.name;
              }
            });
          }

          // Build plans per service map for the filter
          const plansPerService = {};
          const instancesList = Object.entries(window.serviceInstances || {});

          instancesList.forEach(([id, details]) => {
            if (details.service_id) {
              // Only use properly mapped service names for filtering
              const serviceName = serviceIdToName[details.service_id];
              if (serviceName) {
                if (!plansPerService[serviceName]) {
                  plansPerService[serviceName] = new Set();
                }
                if (details.plan && details.plan.name) {
                  plansPerService[serviceName].add(details.plan.name);
                }
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

              // Extract deployment names - try to get actual deployment names from manifests
              const deploymentNames = [];
              for (const item of visibleItems) {
                const instanceId = item.dataset.instanceId;
                const details = window.serviceInstances[instanceId];
                if (details) {
                  try {
                    // Try to fetch manifest data to get actual deployment name
                    const manifestResponse = await fetch(`/b/${instanceId}/manifest-details`, { cache: 'no-cache' });
                    if (manifestResponse.ok) {
                      const manifestData = await manifestResponse.json();
                      if (manifestData && manifestData.parsed && manifestData.parsed.name) {
                        deploymentNames.push(manifestData.parsed.name);
                        continue;
                      }
                    }
                  } catch (error) {
                    console.warn(`Failed to fetch manifest for ${instanceId}:`, error);
                  }

                  // Fallback to constructed deployment name
                  deploymentNames.push(`${details.service_id}-${details.plan?.name || details.plan_id || 'unknown'}-${instanceId}`);
                } else {
                  deploymentNames.push(instanceId); // final fallback
                }
              }

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

                // Update plans data for when Plans sub-tab is accessed
                window.plansData = catalog;

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
            } else if (tabType === 'certificates') {
              // Set up tab switching for service instance certificates
              setTimeout(() => {
                const tabGroupId = `service-cert-tabs-${instanceId}`;
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
                        pane.classList.add('active');
                        pane.style.display = 'block';
                      } else {
                        pane.classList.remove('active');
                        pane.style.display = 'none';
                      }
                    });
                  });
                });
              }, 100);
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
        // Clean up any special layout classes
        contentContainer.classList.remove('plans-view');
        contentContainer.innerHTML = '<div class="loading">Loading...</div>';

        try {
          // Handle the Details tab
          if (tabType === 'details') {
            // Fetch credentials and merge with details
            try {
              const response = await fetch('/b/blacksmith/credentials');
              if (response.ok) {
                const creds = await response.json();
                
                // Create details content with credentials
                const data = window.blacksmithData || {};
                const deploymentName = data.deployment || 'blacksmith';
                const environment = data.env || 'Unknown';
                const totalInstances = data.instances ? Object.keys(data.instances).length : 0;
                const totalPlans = data.plans ? Object.keys(data.plans).length : 0;
                const status = 'Running';
                
                contentContainer.innerHTML = `
                  <table class="service-info-table w-full bg-white dark:bg-gray-900 rounded-lg overflow-hidden shadow-sm border border-gray-200 dark:border-gray-700 max-h-[calc(100vh-200px)] overflow-y-auto">
                    <tbody>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Deployment</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${deploymentName}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${deploymentName}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Environment</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${environment}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${environment}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Total Service Instances</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${totalInstances}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${totalInstances}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Total Plans</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${totalPlans}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${totalPlans}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Status</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span>${status}</span>
                        </td>
                      </tr>
                      ${data.boshDNS ? `
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">BOSH DNS</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${data.boshDNS}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${data.boshDNS}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      ` : ''}
                      <tr>
                        <td colspan="2" class="info-section-header">BOSH Configuration</td>
                      </tr>
                      ${creds.BOSH ? `
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">BOSH Address</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.BOSH.address}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.BOSH.address}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">BOSH Username</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.BOSH.username}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.BOSH.username}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">BOSH Network</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.BOSH.network}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.BOSH.network}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      ` : ''}
                      <tr>
                        <td colspan="2" class="info-section-header">Vault Configuration</td>
                      </tr>
                      ${creds.Vault ? `
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Vault Address</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.Vault.address}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.Vault.address}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      ` : ''}
                      <tr>
                        <td colspan="2" class="info-section-header">Broker Configuration</td>
                      </tr>
                      ${creds.Broker ? `
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Broker Username</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.Broker.username}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.Broker.username}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Broker Port</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.Broker.port}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.Broker.port}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      <tr>
                        <td class="info-key px-4 py-3 font-semibold text-gray-700 dark:text-gray-300 bg-gray-50 dark:bg-gray-800 border-b border-gray-100 dark:border-gray-700 text-sm uppercase tracking-wide whitespace-nowrap">Broker Bind IP</td>
                        <td class="info-value px-4 py-3 text-gray-900 dark:text-gray-100 border-b border-gray-100 dark:border-gray-700 font-mono text-sm whitespace-nowrap">
                          <span class="copy-wrapper inline-flex items-center gap-2 justify-between w-full">
                            <span>${creds.Broker.bind_ip}</span>
                <button class="copy-btn-inline p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300 transition-colors duration-200" onclick="window.copyValue(event, '${creds.Broker.bind_ip}')"
                        title="Copy to clipboard">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
                          </span>
                        </td>
                      </tr>
                      ` : ''}
                    </tbody>
                  </table>
                `;
              } else {
                // Fallback to original details content if credentials can't be fetched
                contentContainer.innerHTML = window.blacksmithDetailsContent || '<div class="no-data">No details available</div>';
              }
            } catch (error) {
              console.error('Failed to load credentials:', error);
              // Fallback to original details content
              contentContainer.innerHTML = window.blacksmithDetailsContent || '<div class="no-data">No details available</div>';
            }
            return;
          }

          // Handle the Plans tab
          if (tabType === 'plans') {
            // Add CSS class for plans layout
            contentContainer.classList.add('plans-view');
            
            // Use the existing plans data if available, or load it
            if (window.plansData && window.plansData.services && window.plansData.services.length > 0) {
              contentContainer.innerHTML = renderPlansTemplate(window.plansData);
              
              // Set up plan click handlers
              document.querySelectorAll('#blacksmith .plan-item').forEach(item => {
                item.addEventListener('click', function () {
                  const planId = this.dataset.planId;
                  
                  // Find the service and plan from the stored data
                  let selectedService = null;
                  let selectedPlan = null;
                  
                  if (window.plansData && window.plansData.services) {
                    window.plansData.services.forEach(service => {
                      if (!service || !service.plans) return;
                      service.plans.forEach(plan => {
                        if (plan && plan.id === planId) {
                          selectedService = service;
                          selectedPlan = plan;
                        }
                      });
                    });
                  }
                  
                  if (selectedService && selectedPlan) {
                    // Update active plan selection
                    document.querySelectorAll('#blacksmith .plan-item').forEach(i => i.classList.remove('active'));
                    this.classList.add('active');
                    
                    // Update detail panel
                    const detailContainer = document.querySelector('#blacksmith .plan-detail');
                    if (detailContainer) {
                      detailContainer.innerHTML = renderPlanDetail(selectedService, selectedPlan);
                    }
                  }
                });
              });
            } else {
              contentContainer.innerHTML = '<div class="no-data">No plans data available. Please reload the page.</div>';
            }
            return;
          }

          // Handle the Broker tab (CF Endpoints Management)
          if (tabType === 'broker') {
            contentContainer.innerHTML = renderBrokerTab();

            // Initialize broker functionality after content is rendered
            setTimeout(() => {
              initBrokerTab();
            }, 50);
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
              initializeSorting('deployment-log-table');
              attachSearchFilter('deployment-log-table');
            } else if (tabType === 'debug') {
              initializeSorting('debug-log-table');
              attachSearchFilter('debug-log-table');
            } else if (tabType === 'certificates') {
              // Initialize certificate functionality
              initializeCertificatesTab();
            } else if (tabType === 'tasks') {
              // Initialize tasks functionality
              initializeSorting('tasks-table');
              attachSearchFilter('tasks-table');
              initializeTasksTab();
            } else if (tabType === 'configs') {
              // Initialize configs functionality
              initializeSorting('configs-table');
              attachSearchFilter('configs-table');
              initializeConfigsTab();
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
      document.querySelector('#services').innerHTML = '<div class="error">Service unavailable</div>';
      // Plans error will be shown when the sub-tab is accessed
      window.plansData = null;
    }
  });

  // ================================================================
  // BLACKSMITH CERTIFICATES TAB FUNCTIONALITY
  // ================================================================

  // Render the main certificates tab content
  const renderCertificatesTab = () => {
    const tabGroupId = 'cert-main-tabs';
    return `
      <div class="service-testing-container">
        <div class="manifest-details-container">
          <div class="manifest-tabs-nav">
            <button class="manifest-tab-btn active" data-tab="trusted" data-group="${tabGroupId}">Trusted</button>
            <button class="manifest-tab-btn" data-tab="config" data-group="${tabGroupId}">Config</button>
            <button class="manifest-tab-btn" data-tab="endpoint" data-group="${tabGroupId}">Endpoint</button>
          </div>

          <div class="manifest-tab-content">
            <!-- Trusted Certificates Tab -->
            <div class="manifest-tab-pane active" data-tab="trusted" data-group="${tabGroupId}">
              <div class="operation-content">
                <table class="operation-form">
                  <tr>
                    <td>
                      <div class="form-row">
                        <button class="execute-btn" onclick="fetchTrustedCertificates()">
                          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
                            <polyline points="3.27,6.96 12,12.01 20.73,6.96"/>
                            <line x1="12" y1="22.08" x2="12" y2="12"/>
                          </svg>
                          Fetch Trusted Certificates
                        </button>
                        <p>Inspect certificates from the VM's trusted certificate store (/etc/ssl/certs/bosh-trusted-cert-*.pem</p>
                      </div>
                      <div id="trusted-certificates-results" class="testing-history" style="display: none;">
                        <div id="trusted-certificates-list" class="cert-list-container">
                          <!-- Certificate list will be populated here -->
                        </div>
                      </div>
                    </td>
                  </tr>
                </table>
              </div>
            </div>

            <!-- Configuration Certificates Tab -->
            <div class="manifest-tab-pane" data-tab="config" data-group="${tabGroupId}">
              <div class="operation-content">
                <table class="operation-form">
                  <tr>
                    <td>
                      <div class="form-row">
                        <button class="execute-btn" onclick="fetchBlacksmithCertificates()">
                          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>
                          </svg>
                          Fetch Configuration Certificates
                        </button>
                        Configuration Certificates
                      </div>
                      <div id="blacksmith-certificates-results" class="testing-history" style="display: none;">
                        <div id="blacksmith-certificates-list" class="cert-list-container">
                          <!-- Certificate list will be populated here -->
                        </div>
                      </div>
                    </td>
                  </tr>
                </table>
              </div>
            </div>

            <!-- Endpoint Certificate Tab -->
            <div class="manifest-tab-pane" data-tab="endpoint" data-group="${tabGroupId}">
              <div class="operation-content">
                <table class="operation-form">
                  <tr>
                    <td>
                      <h5>Network Endpoint Certificate</h5>
                      <p>Connect to a network endpoint and retrieve its SSL/TLS certificate.</p>
                      <div class="form-row">
                        <label>Endpoint Address:</label>
                        <input type="text" id="endpoint-address" placeholder="example.com:443 or https://example.com"
                               value="" />
                      </div>
                      <div class="form-row">
                        <button class="execute-btn" onclick="fetchEndpointCertificate()">
                          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>
                            <rect x="8" y="2" width="8" height="4" rx="1" ry="1"/>
                            <path d="M12 11h4"/>
                            <path d="M12 16h4"/>
                            <path d="M8 11h.01"/>
                            <path d="M8 16h.01"/>
                          </svg>
                          Fetch Certificate
                        </button>
                      </div>
                      <div id="endpoint-certificate-results" class="testing-history" style="display: none;">
                        <div class="history-header">
                          <h5>Endpoint Certificate</h5>
                        </div>
                        <div id="endpoint-certificate-details" class="cert-list-container">
                          <!-- Certificate details will be populated here -->
                        </div>
                      </div>
                    </td>
                  </tr>
                </table>
              </div>
            </div>
          </div>
        </div>
      </div>
    `;
  };

  // Initialize certificates tab functionality
  const initializeCertificatesTab = () => {
    // Clear any existing state
    console.log('Certificates tab initialized');

    // Reset form fields
    const endpointInput = document.getElementById('endpoint-address');
    if (endpointInput) {
      endpointInput.value = '';
    }

    // Hide result sections
    ['trusted-certificates-results', 'blacksmith-certificates-results', 'endpoint-certificate-results'].forEach(id => {
      const element = document.getElementById(id);
      if (element) {
        element.style.display = 'none';
      }
    });

    // Set up tab switching for certificates tabs
    setTimeout(() => {
      const tabGroupId = 'cert-main-tabs';
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
              pane.classList.add('active');
              pane.style.display = 'block';
            } else {
              pane.classList.remove('active');
              pane.style.display = 'none';
            }
          });
        });
      });
    }, 100);
  };

  // Initialize tasks tab functionality
  const initializeTasksTab = () => {
    console.log('Tasks tab initialized');

    // Set up filter change handlers
    const typeFilter = document.getElementById('task-type-filter');
    const stateCheckboxes = document.querySelectorAll('#blacksmith .checkbox-group input[type="checkbox"]');
    const refreshBtn = document.getElementById('refresh-tasks-btn');

    // Handle type filter changes
    if (typeFilter) {
      typeFilter.addEventListener('change', () => {
        refreshTasksTable();
      });
    }

    // Handle state filter changes
    stateCheckboxes.forEach(checkbox => {
      checkbox.addEventListener('change', () => {
        applyTasksFilter();
      });
    });

    // Handle task filter changes
    const taskFilter = document.getElementById('task-filter');
    if (taskFilter) {
      taskFilter.addEventListener('change', () => {
        refreshTasksTable();
      });
    }

    // Handle refresh button
    if (refreshBtn) {
      refreshBtn.addEventListener('click', (e) => {
        e.preventDefault();
        refreshTasksTable();
      });
    }

    // Set up auto-refresh for running tasks every 30 seconds
    const autoRefreshInterval = setInterval(() => {
      // Only auto-refresh if tasks tab is active and there are running tasks
      const tasksTab = document.querySelector('.detail-tab[data-tab="tasks"]');
      if (tasksTab && tasksTab.classList.contains('active')) {
        const runningTasks = document.querySelectorAll('.task-state.processing, .task-state.queued');
        if (runningTasks.length > 0) {
          refreshTasksTable();
        }
      }
    }, 30000);

    // Store interval for cleanup
    if (!window.tasksAutoRefreshInterval) {
      window.tasksAutoRefreshInterval = autoRefreshInterval;
    }

    // Populate the task filter dropdown
    populateTaskFilter();
  };

  // Populate task filter dropdown with filter options
  const populateTaskFilter = async (preserveSelection = null) => {
    try {
      const response = await fetch('/b/service-filter-options', { cache: 'no-cache' });
      if (!response.ok) {
        console.warn('Failed to load service filter options, using default');
        return;
      }

      const data = await response.json();
      const taskFilter = document.getElementById('task-filter');
      
      if (taskFilter && data && data.options && Array.isArray(data.options)) {
        // Save current selection if not provided
        const currentSelection = preserveSelection || taskFilter.value || 'blacksmith';
        
        // Clear existing options
        taskFilter.innerHTML = '';
        
        // Add all options in the order specified:
        // blacksmith, service-instances, then service names, then plan IDs
        data.options.forEach(option => {
          const optionElement = document.createElement('option');
          optionElement.value = option;
          optionElement.textContent = option;
          if (option === currentSelection) optionElement.selected = true;
          taskFilter.appendChild(optionElement);
        });
      }
    } catch (error) {
      console.warn('Error loading service filter options:', error);
    }
  };

  // Refresh tasks table
  const refreshTasksTable = async () => {
    const typeFilter = document.getElementById('task-type-filter');
    const stateCheckboxes = document.querySelectorAll('#blacksmith .checkbox-group input[type="checkbox"]:checked');
    const taskFilter = document.getElementById('task-filter');

    // Save current filter values before refresh
    const taskType = typeFilter ? typeFilter.value : 'recent';
    const checkedStates = Array.from(stateCheckboxes).map(cb => cb.value);
    const uncheckedStates = Array.from(document.querySelectorAll('#blacksmith .checkbox-group input[type="checkbox"]:not(:checked)')).map(cb => cb.value);
    const taskFilterValue = taskFilter ? taskFilter.value : 'blacksmith';

    try {
      let url = `/b/tasks?type=${taskType}&limit=100&team=${encodeURIComponent(taskFilterValue)}`;
      if (checkedStates.length > 0) {
        url += `&states=${checkedStates.join(',')}`;
      }

      const response = await fetch(url, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const tasks = await response.json();
      const contentContainer = document.querySelector('#blacksmith .detail-content');

      if (contentContainer) {
        contentContainer.innerHTML = formatTasks(tasks);

        // Restore filter states after refresh
        setTimeout(async () => {
          // First repopulate the task filter dropdown since formatTasks() recreated the HTML
          await populateTaskFilter(taskFilterValue);
          
          // Restore dropdown selections
          const newTypeFilter = document.getElementById('task-type-filter');
          if (newTypeFilter) {
            newTypeFilter.value = taskType;
            // Reattach the change event handler
            newTypeFilter.addEventListener('change', () => {
              refreshTasksTable();
            });
          }
          const newTaskFilter = document.getElementById('task-filter');
          if (newTaskFilter) {
            newTaskFilter.value = taskFilterValue;
            // Reattach the change event handler
            newTaskFilter.addEventListener('change', () => {
              refreshTasksTable();
            });
          }

          // Restore checkbox states and reattach handlers
          checkedStates.forEach(state => {
            const checkbox = document.querySelector(`#blacksmith .checkbox-group input[type="checkbox"][value="${state}"]`);
            if (checkbox) {
              checkbox.checked = true;
              checkbox.addEventListener('change', () => {
                applyTasksFilter();
              });
            }
          });
          uncheckedStates.forEach(state => {
            const checkbox = document.querySelector(`#blacksmith .checkbox-group input[type="checkbox"][value="${state}"]`);
            if (checkbox) {
              checkbox.checked = false;
              checkbox.addEventListener('change', () => {
                applyTasksFilter();
              });
            }
          });

          // Reinitialize functionality
          initializeSorting('tasks-table');
          attachSearchFilter('tasks-table');
          // Don't call initializeTasksTab() again - it would repopulate the dropdown
        }, 50);
      }
    } catch (error) {
      console.error('Failed to refresh tasks:', error);
      const contentContainer = document.querySelector('#blacksmith .detail-content');
      if (contentContainer) {
        contentContainer.innerHTML = `<div class="error">Failed to load tasks: ${error.message}</div>`;
      }
    }
  };

  // Apply state filters to existing task table
  const applyTasksFilter = () => {
    const stateCheckboxes = document.querySelectorAll('#blacksmith .checkbox-group input[type="checkbox"]:checked');
    const checkedStates = new Set(Array.from(stateCheckboxes).map(cb => cb.value));

    const rows = document.querySelectorAll('.tasks-table tbody tr');

    rows.forEach(row => {
      const stateCell = row.querySelector('.task-state span');
      if (stateCell) {
        const taskState = stateCell.classList[1]; // Second class is the state
        if (checkedStates.has(taskState)) {
          row.style.display = '';
        } else {
          row.style.display = 'none';
        }
      }
    });
  };

  // Initialize configs tab functionality
  const initializeConfigsTab = () => {
    console.log('Configs tab initialized');

    // Set up filter change handlers
    const typeCheckboxes = document.querySelectorAll('#blacksmith .configs-filters .checkbox-group input[type="checkbox"]');
    const refreshBtn = document.getElementById('refresh-configs-btn');

    // Handle type filter changes
    typeCheckboxes.forEach(checkbox => {
      checkbox.addEventListener('change', () => {
        applyConfigsFilter();
      });
    });

    // Handle refresh button
    if (refreshBtn) {
      refreshBtn.addEventListener('click', (e) => {
        e.preventDefault();
        refreshConfigsTable();
      });
    }
  };

  // Refresh configs table
  const refreshConfigsTable = async () => {
    console.log('Refreshing configs table');

    const typeCheckboxes = document.querySelectorAll('#blacksmith .configs-filters .checkbox-group input[type="checkbox"]:checked');
    const checkedTypes = Array.from(typeCheckboxes).map(cb => cb.value);

    try {
      let url = `/b/configs?limit=100`;
      if (checkedTypes.length > 0 && checkedTypes.length < 4) {
        url += `&types=${checkedTypes.join(',')}`;
      }

      const response = await fetch(url, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const configs = await response.json();
      const contentContainer = document.querySelector('#blacksmith .detail-content');

      if (contentContainer) {
        contentContainer.innerHTML = formatConfigs(configs);

        // Reinitialize functionality
        setTimeout(() => {
          initializeSorting('configs-table');
          attachSearchFilter('configs-table');
          initializeConfigsTab();
        }, 100);
      }
    } catch (error) {
      console.error('Failed to refresh configs:', error);
      const contentContainer = document.querySelector('#blacksmith .detail-content');
      if (contentContainer) {
        contentContainer.innerHTML = `<div class="error">Failed to load configs: ${error.message}</div>`;
      }
    }
  };

  // Apply type filters to existing configs table
  const applyConfigsFilter = () => {
    const typeCheckboxes = document.querySelectorAll('#blacksmith .configs-filters .checkbox-group input[type="checkbox"]:checked');
    const checkedTypes = new Set(Array.from(typeCheckboxes).map(cb => cb.value));

    const rows = document.querySelectorAll('.configs-table tbody tr');

    rows.forEach(row => {
      const typeCell = row.querySelector('.config-type span');
      if (typeCell) {
        const configType = typeCell.classList[1]; // Second class is the type
        if (checkedTypes.has(configType)) {
          row.style.display = '';
        } else {
          row.style.display = 'none';
        }
      }
    });
  };

  // Show task details modal
  window.showTaskDetails = async (taskId, event) => {
    if (event) {
      event.preventDefault();
    }

    console.log('Opening task details modal for task:', taskId);

    // Create or get the modal
    let modal = document.getElementById('task-details-modal');
    if (!modal) {
      const modalHTML = `
        <div id="task-details-modal" class="modal-overlay" role="dialog" aria-labelledby="task-details-modal-title" aria-hidden="true">
          <div class="modal">
            <div class="modal-content">
              <div class="modal-header">
                <h3 id="task-details-modal-title">Task Details</h3>
                <div class="task-actions">
                  <button id="cancel-task-btn" class="btn btn-secondary" style="display: none;" onclick="cancelTaskAndRefresh()">Cancel Task</button>
                </div>
                <button class="modal-close" onclick="hideTaskDetailsModal()" aria-label="Close modal">&times;</button>
              </div>
              <div class="modal-body">
                <div id="task-details-loading" class="loading">Loading task details...</div>
                <div id="task-details-content" style="display: none;">
                  <div class="task-summary">
                    <div class="task-info-table">
                      <table class="task-details-table">
                        <thead>
                          <tr>
                            <th>State</th>
                            <th>Description</th>
                            <th>Deployment</th>
                            <th>User</th>
                            <th>Context ID</th>
                            <th>Started</th>
                            <th>Finished</th>
                            <th>Duration</th>
                          </tr>
                        </thead>
                        <tbody>
                          <tr>
                            <td id="task-state-badge"></td>
                            <td id="task-description"></td>
                            <td id="task-deployment"></td>
                            <td id="task-user"></td>
                            <td id="task-context-id"></td>
                            <td id="task-started"></td>
                            <td id="task-finished"></td>
                            <td id="task-duration"></td>
                          </tr>
                        </tbody>
                      </table>
                    </div>
                  </div>

                  <div class="manifest-tabs-nav">
                    <button class="manifest-tab-btn active" data-tab="task" data-group="task-tabs">Task</button>
                    <button class="manifest-tab-btn" data-tab="debug" data-group="task-tabs">Debug</button>
                    <button class="manifest-tab-btn" data-tab="raw" data-group="task-tabs">Raw</button>
                    <button class="manifest-tab-btn" data-tab="cpi" data-group="task-tabs">CPI</button>
                  </div>

                  <div class="manifest-tab-content">
                    <div id="task-task-content" class="manifest-tab-pane active" data-tab="task" data-group="task-tabs">
                      <div class="loading">Loading task output...</div>
                    </div>
                    <div id="task-debug-content" class="manifest-tab-pane" data-tab="debug" data-group="task-tabs">
                      <div class="loading">Loading debug...</div>
                    </div>
                    <div id="task-raw-content" class="manifest-tab-pane" data-tab="raw" data-group="task-tabs">
                      <div class="loading">Loading raw data...</div>
                    </div>
                    <div id="task-cpi-content" class="manifest-tab-pane" data-tab="cpi" data-group="task-tabs">
                      <div class="loading">Loading CPI output...</div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      `;

      document.body.insertAdjacentHTML('beforeend', modalHTML);
      modal = document.getElementById('task-details-modal');

      // Set up modal tab switching
      setupTaskModalTabs();

      // Set up escape key handler
      modal.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
          hideTaskDetailsModal();
        }
      });

      // Set up click outside to close
      modal.addEventListener('click', (e) => {
        if (e.target === modal) {
          hideTaskDetailsModal();
        }
      });
    }

    // Store current task ID for refresh
    window.currentTaskId = taskId;

    // Show modal using existing modal system
    modal.style.display = 'flex';

    // Force reflow to ensure CSS transitions work
    modal.offsetHeight;

    modal.classList.add('active');
    modal.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';

    // Load task details
    await loadTaskDetails(taskId);

    // Set up auto-refresh for running tasks
    startTaskModalAutoRefresh();
  };

  // Hide task details modal
  window.hideTaskDetailsModal = () => {
    const modal = document.getElementById('task-details-modal');
    if (modal) {
      modal.classList.remove('active');
      modal.style.display = 'none';
      modal.setAttribute('aria-hidden', 'true');
      document.body.style.overflow = '';
    }

    // Clear auto-refresh
    if (window.taskModalAutoRefresh) {
      clearInterval(window.taskModalAutoRefresh);
      window.taskModalAutoRefresh = null;
    }

    window.currentTaskId = null;
  };

  // Show config details modal
  window.showConfigDetails = async (configId, event) => {
    if (event) {
      event.preventDefault();
    }

    console.log('Opening config details modal for config:', configId);

    // Get the modal
    const modal = document.getElementById('config-details-modal');
    if (!modal) {
      console.error('Config details modal not found in DOM');
      return;
    }

    // Show the modal
    modal.style.display = 'flex';
    modal.classList.add('active');
    modal.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';

    // Show loading state
    const loadingDiv = document.getElementById('config-details-loading');
    const contentDiv = document.getElementById('config-details-content');
    if (loadingDiv) loadingDiv.style.display = 'block';
    if (contentDiv) contentDiv.style.display = 'none';

    try {
      // Fetch config details from API
      const response = await fetch(`/b/configs/${configId}`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }
      const config = await response.json();

      // Update modal title
      document.getElementById('config-details-modal-title').textContent = `Config Details - ${config.name}`;

      // Populate overview tab
      document.getElementById('config-id').textContent = config.id;
      document.getElementById('config-name').textContent = config.name;
      document.getElementById('config-type').textContent = config.type;
      document.getElementById('config-team').textContent = config.team || 'N/A';
      document.getElementById('config-created-at').textContent = formatTimestamp(config.created_at);
      document.getElementById('config-status').textContent = config.id.endsWith('*') ? 'Active' : 'Inactive';

      // Populate content tab
      document.getElementById('config-content-text').textContent = config.content || 'No content available';

      // Populate metadata tab
      document.getElementById('config-metadata-text').textContent = JSON.stringify(config.metadata, null, 2);

      // Hide loading and show content
      if (loadingDiv) loadingDiv.style.display = 'none';
      if (contentDiv) contentDiv.style.display = 'block';

      // Set up modal tab switching for this config modal
      setupConfigModalTabs();

      // Set up copy functionality
      setupConfigCopyFunctionality();

    } catch (error) {
      console.error('Failed to load config details:', error);
      if (loadingDiv) {
        loadingDiv.innerHTML = `<div class="error">Failed to load config details: ${error.message}</div>`;
      }
    }
  };

  // Hide config details modal
  window.hideConfigDetailsModal = () => {
    const modal = document.getElementById('config-details-modal');
    if (modal) {
      modal.classList.remove('active');
      modal.style.display = 'none';
      modal.setAttribute('aria-hidden', 'true');
      document.body.style.overflow = '';
    }
  };

  // Set up config modal tab switching
  const setupConfigModalTabs = () => {
    const tabGroupId = 'config-tabs';
    const tabButtons = document.querySelectorAll(`.manifest-tab-btn[data-group="${tabGroupId}"]`);
    const tabPanes = document.querySelectorAll(`.manifest-tab-pane[data-group="${tabGroupId}"]`);

    tabButtons.forEach(button => {
      button.addEventListener('click', () => {
        const tabName = button.dataset.tab;

        // Update active states
        tabButtons.forEach(btn => btn.classList.remove('active'));
        button.classList.add('active');

        // Show/hide content panes
        tabPanes.forEach(pane => {
          pane.classList.remove('active');
          pane.style.display = 'none';
        });

        const targetPane = document.querySelector(`.manifest-tab-pane[data-tab="${tabName}"][data-group="${tabGroupId}"]`);
        if (targetPane) {
          targetPane.classList.add('active');
          targetPane.style.display = 'block';
        }
      });
    });
  };

  // Set up config copy functionality
  const setupConfigCopyFunctionality = () => {
    const copyButton = document.getElementById('copy-config-content');
    if (copyButton) {
      copyButton.addEventListener('click', async () => {
        const contentText = document.getElementById('config-content-text').textContent;
        try {
          await navigator.clipboard.writeText(contentText);
          // Show success feedback
          const originalText = copyButton.innerHTML;
          copyButton.innerHTML = '<span>Copied!</span>';
          setTimeout(() => {
            copyButton.innerHTML = originalText;
          }, 2000);
        } catch (err) {
          console.error('Failed to copy config content:', err);
          alert('Failed to copy to clipboard');
        }
      });
    }
  };

  // Show config details modal
  window.showConfigVersions = async (configType, configName, currentId, event) => {
    if (event) {
      event.preventDefault();
    }

    console.log('Opening config details modal for config:', configType, configName);

    // Create or get the modal
    let modal = document.getElementById('config-details-modal');
    if (!modal) {
      const modalHTML = `
        <div id="config-details-modal" class="modal-overlay" role="dialog" aria-labelledby="config-details-modal-title" aria-hidden="true">
          <div class="modal config-details-modal">
            <div class="modal-header">
              <h2 id="config-details-modal-title">Config Details</h2>
              <button class="close-btn" onclick="hideConfigVersionsModal()" title="Close" aria-label="Close modal">
                <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                  <line x1="18" y1="6" x2="6" y2="18"></line>
                  <line x1="6" y1="6" x2="18" y2="18"></line>
                </svg>
              </button>
            </div>
            <div class="modal-body config-details-body">
              <!-- Tab navigation -->
              <div class="modal-tabs">
                <button class="manifest-tab-btn active" data-tab="versions" data-group="config-details-tabs">Versions</button>
                <button class="manifest-tab-btn" data-tab="diffs" data-group="config-details-tabs">Diffs</button>
              </div>
              
              <!-- Tab content -->
              <div class="modal-tab-content">
                <!-- Versions tab -->
                <div id="config-versions-tab" class="manifest-tab-pane active" data-tab="versions" data-group="config-details-tabs">
                  <div id="config-versions-loading" class="loading">
                    <div class="loading-spinner"></div>
                    <p>Loading config versions...</p>
                  </div>
                  <div id="config-versions-content" style="display: none;">
                    <div class="config-versions-container">
                      <div class="config-versions-sidebar">
                        <h3>Versions</h3>
                        <div id="config-versions-list" class="versions-list"></div>
                      </div>
                      <div class="config-version-details">
                        <div class="version-details-header">
                          <h3>Config Content</h3>
                          <button id="copy-config-version-content" class="copy-btn" title="Copy content">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                              <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
                              <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
                            </svg>
                          </button>
                        </div>
                        <pre id="config-version-content" class="config-content-pre"></pre>
                      </div>
                    </div>
                  </div>
                </div>
                
                <!-- Diffs tab -->
                <div id="config-diffs-tab" class="manifest-tab-pane" data-tab="diffs" data-group="config-details-tabs">
                  <div id="config-diffs-loading" class="loading" style="display: none;">
                    <div class="loading-spinner"></div>
                    <p>Loading diff...</p>
                  </div>
                  <div id="config-diffs-content">
                    <div class="diff-navigation">
                      <button id="diff-prev-btn" class="diff-nav-btn" onclick="navigateConfigDiff('prev')" disabled>
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                          <polyline points="15 18 9 12 15 6"></polyline>
                        </svg>
                        Previous
                      </button>
                      <div class="diff-version-info">
                        <span id="diff-from-version" class="diff-version">Select versions</span>
                        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                          <polyline points="9 18 15 12 9 6"></polyline>
                        </svg>
                        <span id="diff-to-version" class="diff-version">to compare</span>
                      </div>
                      <button id="diff-next-btn" class="diff-nav-btn" onclick="navigateConfigDiff('next')">
                        Next
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                          <polyline points="9 18 15 12 9 6"></polyline>
                        </svg>
                      </button>
                    </div>
                    <div class="diff-viewer">
                      <div class="diff-panel diff-panel-left">
                        <div class="diff-panel-header">
                          <h4>Previous Version</h4>
                          <span id="diff-left-version-info" class="version-info"></span>
                        </div>
                        <pre id="diff-left-content" class="diff-content"></pre>
                      </div>
                      <div class="diff-panel diff-panel-right">
                        <div class="diff-panel-header">
                          <h4>Current Version</h4>
                          <span id="diff-right-version-info" class="version-info"></span>
                        </div>
                        <pre id="diff-right-content" class="diff-content"></pre>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      `;
      document.body.insertAdjacentHTML('beforeend', modalHTML);
      modal = document.getElementById('config-details-modal');
      
      // Setup tab switching for config details modal
      setupConfigDetailsTabs();
    }

    // Show the modal
    modal.style.display = 'flex';
    modal.classList.add('active');
    modal.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';

    // Show loading state
    const loadingDiv = document.getElementById('config-details-loading');
    const contentDiv = document.getElementById('config-details-content');
    if (loadingDiv) loadingDiv.style.display = 'block';
    if (contentDiv) contentDiv.style.display = 'none';

    try {
      // Fetch config versions from API
      const response = await fetch(`/b/configs/${configType}/${configName}/versions?limit=30`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }
      const data = await response.json();
      const versions = data.configs || [];

      // Update modal title
      document.getElementById('config-details-modal-title').textContent = `Config Details - ${configName} (${configType})`;
      
      // Populate config overview table with latest version info
      const latestVersion = versions.length > 0 ? versions[0] : null;
      if (latestVersion) {
        const setElementText = (id, value) => {
          const elem = document.getElementById(id);
          if (elem) elem.textContent = value || '-';
        };
        
        setElementText('config-id', latestVersion.id || '-');
        setElementText('config-name', configName);
        setElementText('config-type', configType);
        setElementText('config-team', latestVersion.team || '-');
        setElementText('config-created-at', latestVersion.created_at || '-');
        
        // Set status with active indicator
        const statusElem = document.getElementById('config-status');
        if (statusElem) {
          // Check if this is the active version using the same logic as version list
          const isActive = latestVersion.is_active || latestVersion.current || 
                          (latestVersion.id && latestVersion.id.includes('*'));
          statusElem.innerHTML = isActive ? 
            '<span class="badge badge-success">ACTIVE</span>' : 
            '<span class="badge badge-secondary">INACTIVE</span>';
        }
      } else {
        // Clear table if no versions
        ['config-id', 'config-name', 'config-type', 'config-team', 'config-created-at', 'config-status']
          .forEach(id => {
            const elem = document.getElementById(id);
            if (elem) elem.textContent = '-';
          });
      }
      
      // Clear any previous config info and reset diff tab
      window.currentConfigInfo = null;
      window.currentDiffIndex = null;
      
      // Reset diff tab if it exists
      const diffSelector = document.getElementById('diff-version-selector');
      if (diffSelector) {
        diffSelector.innerHTML = '<option value="">Select versions to compare</option>';
        diffSelector.value = '';
      }
      const diffOutput = document.getElementById('diff-output');
      if (diffOutput) {
        diffOutput.classList.remove('active');
        diffOutput.innerHTML = '';
      }
      const diffPlaceholder = document.getElementById('diff-placeholder');
      if (diffPlaceholder) {
        diffPlaceholder.classList.remove('hidden');
        diffPlaceholder.textContent = 'Select versions from the dropdown above to view configuration changes.';
      }
      
      // Store new config info for diff tab
      window.currentConfigInfo = { type: configType, name: configName, versions: versions };

      // Populate versions list
      const versionsList = document.getElementById('config-versions-list');
      versionsList.innerHTML = '';

      if (versions.length === 0) {
        versionsList.innerHTML = '<div class="no-versions">No versions found</div>';
      } else {
        versions.forEach((version, index) => {
          const isActive = version.is_active || version.id === currentId || (index === 0 && currentId && version.id.replace('*', '') === currentId);
          const versionItem = document.createElement('div');
          versionItem.className = 'version-item' + (isActive ? ' active' : '');
          versionItem.dataset.versionId = version.id;
          versionItem.innerHTML = `
            <div class="version-id">${version.id}${isActive ? ' *' : ''}</div>
            <div class="version-date">${formatTimestamp(version.created_at)}</div>
            ${version.team ? `<div class="version-team">Team: ${version.team}</div>` : ''}
          `;
          versionItem.addEventListener('click', () => loadConfigVersion(version.id, versionItem));
          versionsList.appendChild(versionItem);
        });

        // Auto-select the first (most recent) version
        if (versions.length > 0) {
          const firstItem = versionsList.querySelector('.version-item');
          if (firstItem) {
            firstItem.click();
          }
        }
      }

      // Hide loading and show content
      if (loadingDiv) loadingDiv.style.display = 'none';
      if (contentDiv) contentDiv.style.display = 'block';

      // Set up copy functionality
      setupConfigVersionCopyFunctionality();
      
      // Set up tab functionality for static modal
      setupConfigDetailsTabs();

    } catch (error) {
      console.error('Failed to load config versions:', error);
      if (loadingDiv) {
        loadingDiv.innerHTML = `<div class="error">Failed to load config versions: ${error.message}</div>`;
      }
    }
  };

  // Load specific config version content
  window.loadConfigVersion = async (versionId, versionItem) => {
    // Remove the asterisk if present
    const cleanId = versionId.replace('*', '');

    // Update selected state in the list
    document.querySelectorAll('.version-item').forEach(item => {
      item.classList.remove('selected');
    });
    versionItem.classList.add('selected');

    // Show loading state in content area
    const contentPre = document.getElementById('config-content-text');
    contentPre.textContent = 'Loading config content...';

    try {
      // Fetch config details for this version
      const response = await fetch(`/b/configs/${cleanId}`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }
      const config = await response.json();

      // Display the content
      contentPre.textContent = config.content || 'No content available';

    } catch (error) {
      console.error('Failed to load config version content:', error);
      contentPre.textContent = `Failed to load config content: ${error.message}`;
    }
  };

  // Hide config versions modal
  window.hideConfigVersionsModal = () => {
    const modal = document.getElementById('config-details-modal');
    if (modal) {
      modal.classList.remove('active');
      modal.style.display = 'none';
      modal.setAttribute('aria-hidden', 'true');
      document.body.style.overflow = '';
      // Clear stored config info
      window.currentConfigInfo = null;
      window.currentDiffIndex = null;
    }
  };

  // Set up config version copy functionality
  const setupConfigVersionCopyFunctionality = () => {
    const copyButton = document.getElementById('copy-config-content');
    if (copyButton) {
      copyButton.removeEventListener('click', copyButton._clickHandler);
      copyButton._clickHandler = async () => {
        const contentText = document.getElementById('config-content-text').textContent;
        try {
          await navigator.clipboard.writeText(contentText);
          // Show success feedback
          const originalHTML = copyButton.innerHTML;
          copyButton.innerHTML = '<span>Copied!</span>';
          setTimeout(() => {
            copyButton.innerHTML = originalHTML;
          }, 2000);
        } catch (err) {
          console.error('Failed to copy config content:', err);
          alert('Failed to copy to clipboard');
        }
      };
      copyButton.addEventListener('click', copyButton._clickHandler);
    }
  };

  // Set up config details modal tab switching
  const setupConfigDetailsTabs = () => {
    const tabGroupId = 'config-tabs';
    const tabButtons = document.querySelectorAll(`.manifest-tab-btn[data-group="${tabGroupId}"]`);
    const tabPanes = document.querySelectorAll(`.manifest-tab-pane[data-group="${tabGroupId}"]`);
    
    tabButtons.forEach(button => {
      button.addEventListener('click', () => {
        const tabName = button.dataset.tab;

        // Update active states
        tabButtons.forEach(btn => btn.classList.remove('active'));
        button.classList.add('active');

        // Show/hide content panes
        tabPanes.forEach(pane => {
          pane.classList.remove('active');
          pane.style.display = 'none';
        });

        const targetPane = document.querySelector(`.manifest-tab-pane[data-tab="${tabName}"][data-group="${tabGroupId}"]`);
        if (targetPane) {
          targetPane.classList.add('active');
          targetPane.style.display = 'block';
        }

        // Initialize diff tab when clicked
        if (tabName === 'diffs' && window.currentConfigInfo) {
          // Small delay to ensure DOM elements are available after tab switch
          setTimeout(() => {
            initConfigDiffTab(window.currentConfigInfo.type, window.currentConfigInfo.name);
          }, 10);
        }
      });
    });

    // Set up diff version selector
    const diffSelector = document.getElementById('diff-version-selector');
    if (diffSelector) {
      // Remove any existing event listeners
      const newSelector = diffSelector.cloneNode(true);
      diffSelector.parentNode.replaceChild(newSelector, diffSelector);
      
      newSelector.addEventListener('change', async (event) => {
        const selectedValue = event.target.value;
        if (selectedValue) {
          const [fromId, toId] = selectedValue.split('|');
          await loadConfigDiffById(fromId, toId);
        } else {
          // Clear diff display
          const diffOutput = document.getElementById('diff-output');
          const placeholder = document.getElementById('diff-placeholder');
          if (diffOutput) {
            diffOutput.classList.remove('active');
            diffOutput.innerHTML = '';
          }
          if (placeholder) {
            placeholder.classList.remove('hidden');
          }
        }
      });
    }
  };

  // Initialize config diff tab
  window.initConfigDiffTab = async (configType, configName) => {
    const selector = document.getElementById('diff-version-selector');
    const placeholder = document.getElementById('diff-placeholder');
    const diffOutput = document.getElementById('diff-output');
    
    if (!window.currentConfigInfo || !window.currentConfigInfo.versions || window.currentConfigInfo.versions.length < 2) {
      if (placeholder) {
        placeholder.textContent = 'At least 2 versions are required for diff comparison.';
        placeholder.classList.remove('hidden');
      }
      if (selector) {
        selector.innerHTML = '<option value="">No versions available</option>';
        selector.disabled = true;
      }
      return;
    }

    // Clear previous state
    if (diffOutput) {
      diffOutput.classList.remove('active');
      diffOutput.innerHTML = '';
    }
    if (placeholder) {
      placeholder.classList.remove('hidden');
    }

    // Populate dropdown with version pairs
    if (selector) {
      selector.disabled = false;
      selector.innerHTML = '<option value="">Select versions to compare</option>';
      
      const versions = window.currentConfigInfo.versions;
      // Create options for each consecutive pair
      for (let i = 0; i < versions.length - 1; i++) {
        const newer = versions[i];
        const older = versions[i + 1];
        const option = document.createElement('option');
        option.value = `${older.id}|${newer.id}`;
        option.textContent = `${older.id} → ${newer.id}`;
        if (i === 0) {
          option.textContent += ' (latest change)';
        }
        selector.appendChild(option);
      }
      
      // Reset selection
      selector.value = '';
    }
  };

  // Load config diff by version IDs
  window.loadConfigDiffById = async (fromId, toId) => {
    const diffOutput = document.getElementById('diff-output');
    const placeholder = document.getElementById('diff-placeholder');
    
    if (!diffOutput) return;
    
    // Show loading state
    diffOutput.innerHTML = 'Loading diff...';
    diffOutput.classList.add('active');
    if (placeholder) placeholder.classList.add('hidden');
    
    try {
      // Fetch diff from API
      const response = await fetch(
        `/b/configs/${window.currentConfigInfo.type}/${window.currentConfigInfo.name}/diff?from=${fromId}&to=${toId}`,
        { cache: 'no-cache' }
      );

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const diffData = await response.json();
      
      // Display the diff
      displaySimpleDiff(diffData, fromId, toId);
      
    } catch (error) {
      console.error('Failed to load config diff:', error);
      diffOutput.innerHTML = `Error loading diff: ${error.message}`;
      diffOutput.classList.add('active');
    }
  };

  // Display simple diff in monospace box
  const displaySimpleDiff = (diffData, fromId, toId) => {
    const diffOutput = document.getElementById('diff-output');
    
    if (!diffOutput) return;
    
    // Create header
    let output = `Configuration Changes: ${fromId} → ${toId}\n`;
    output += '─'.repeat(60) + '\n\n';
    
    if (!diffData.has_changes) {
      output += 'No changes detected between these configuration versions.\n';
    } else if (diffData.diff_string) {
      // Display the spruce diff string directly
      output += diffData.diff_string;
    } else {
      output += 'Diff information not available.\n';
    }
    
    // Set the text content to preserve formatting
    diffOutput.textContent = output;
    diffOutput.classList.add('active');
  };

  // DEPRECATED: Old load config diff function - to be removed
  const loadConfigDiff = async (index) => {
    const versions = window.currentConfigInfo.versions;
    if (index < 0 || index >= versions.length - 1) return;

    const fromVersion = versions[index + 1]; // Older version (N-1)
    const toVersion = versions[index]; // Newer version (N)

    // Update navigation buttons
    const prevDisabled = index === 0;
    const nextDisabled = index >= versions.length - 2;
    
    console.log(`Navigation state: index=${index}, versions.length=${versions.length}, prevDisabled=${prevDisabled}, nextDisabled=${nextDisabled}`);
    console.log(`Current diff index: ${window.currentDiffIndex}, comparing ${fromVersion.id} → ${toVersion.id}`);
    
    document.getElementById('prev-diff-btn').disabled = prevDisabled;
    document.getElementById('next-diff-btn').disabled = nextDisabled;

    // Update info display
    const diffInfo = document.getElementById('diff-info');
    if (diffInfo) {
      diffInfo.textContent = `Comparing ${fromVersion.id} → ${toVersion.id}`;
    }

    // Show loading in diff display
    const diffDisplay = document.getElementById('config-diff-display');
    const placeholder = document.getElementById('diff-placeholder');
    const diffPanels = document.querySelector('.diff-panels');
    
    if (placeholder) placeholder.style.display = 'none';
    if (diffPanels) diffPanels.style.display = 'none';
    
    // Create or update loading message without destroying the diff panels
    let loadingMsg = document.getElementById('diff-loading-msg');
    if (!loadingMsg) {
      loadingMsg = document.createElement('p');
      loadingMsg.id = 'diff-loading-msg';
      diffDisplay.appendChild(loadingMsg);
    }
    loadingMsg.textContent = 'Loading diff...';
    loadingMsg.style.display = 'block';

    try {
      // Fetch diff from API
      const response = await fetch(
        `/b/configs/${window.currentConfigInfo.type}/${window.currentConfigInfo.name}/diff?from=${fromVersion.id}&to=${toVersion.id}`,
        { cache: 'no-cache' }
      );

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const diffData = await response.json();

      // Display the diff using spruce diff string if available
      if (diffData.diff_string && diffData.has_changes) {
        displaySpruceConfigDiff(diffData.diff_string, fromVersion, toVersion);
      } else if (!diffData.has_changes) {
        displayNoChangesMessage(fromVersion, toVersion);
      } else {
        // Fallback to side-by-side if no diff string
        const [fromContent, toContent] = await Promise.all([
          fetchConfigContent(fromVersion.id),
          fetchConfigContent(toVersion.id)
        ]);
        displayConfigDiff(fromContent, toContent, diffData.changes);
      }

    } catch (error) {
      console.error('Failed to load config diff:', error);
      const loadingMsg = document.getElementById('diff-loading-msg');
      if (loadingMsg) {
        loadingMsg.textContent = `Error loading diff: ${error.message}`;
        loadingMsg.className = 'error';
        loadingMsg.style.display = 'block';
      }
    }
  };

  // Fetch config content by ID
  const fetchConfigContent = async (configId) => {
    const cleanId = configId.replace('*', '');
    const response = await fetch(`/b/configs/${cleanId}`, { cache: 'no-cache' });
    if (!response.ok) {
      throw new Error(`Failed to fetch config ${configId}`);
    }
    const config = await response.json();
    return config.content || '';
  };

  // Display config diff with highlighting
  const displayConfigDiff = (fromContent, toContent, changes) => {
    const leftPanel = document.getElementById('diff-left-content');
    const rightPanel = document.getElementById('diff-right-content');
    const diffPanels = document.querySelector('.diff-panels');
    const placeholder = document.getElementById('diff-placeholder');
    
    // Show diff panels and hide placeholder/loading
    if (placeholder) placeholder.style.display = 'none';
    const loadingMsg = document.getElementById('diff-loading-msg');
    if (loadingMsg) loadingMsg.style.display = 'none';
    if (diffPanels) diffPanels.style.display = 'flex';

    // Check if panels exist
    if (!leftPanel || !rightPanel) {
      console.error('Diff panels not found:', { leftPanel: !!leftPanel, rightPanel: !!rightPanel });
      return;
    }

    // Restore side-by-side layout (in case it was changed by spruce diff view)
    rightPanel.style.display = 'block';
    leftPanel.style.width = '';
    rightPanel.style.width = '';

    // Process content into lines
    const fromLines = fromContent.split('\n');
    const toLines = toContent.split('\n');

    // Create diff display with line numbers and highlighting
    leftPanel.innerHTML = createDiffDisplay(fromLines, 'removed', changes);
    rightPanel.innerHTML = createDiffDisplay(toLines, 'added', changes);

    // Synchronize scrolling
    leftPanel.onscroll = () => {
      rightPanel.scrollTop = leftPanel.scrollTop;
      rightPanel.scrollLeft = leftPanel.scrollLeft;
    };
    rightPanel.onscroll = () => {
      leftPanel.scrollTop = rightPanel.scrollTop;
      leftPanel.scrollLeft = rightPanel.scrollLeft;
    };
  };

  // Create diff display with line numbers and highlighting
  const createDiffDisplay = (lines, type, changes) => {
    let html = '<div class="diff-lines">';
    
    lines.forEach((line, index) => {
      const lineNum = index + 1;
      let lineClass = 'diff-line';
      
      // Check if this line is part of a change
      // This is a simplified version - you might want to enhance this
      // based on the actual change data structure
      if (changes && changes.length > 0) {
        // Simple heuristic: if line contains changed content
        for (const change of changes) {
          if (change.type === type || change.type === 'changed') {
            // This is a very basic check - enhance as needed
            if (line.includes(change.path)) {
              lineClass += type === 'removed' ? ' diff-removed' : ' diff-added';
              break;
            }
          }
        }
      }
      
      html += `<div class="${lineClass}">`;
      html += `<span class="line-number">${lineNum}</span>`;
      html += `<span class="line-content">${escapeHtml(line)}</span>`;
      html += '</div>';
    });
    
    html += '</div>';
    return html;
  };

  // Display spruce diff output in a single formatted panel
  const displaySpruceConfigDiff = (diffString, fromVersion, toVersion) => {
    const leftPanel = document.getElementById('diff-left-content');
    const rightPanel = document.getElementById('diff-right-content');
    const diffPanels = document.querySelector('.diff-panels');
    const placeholder = document.getElementById('diff-placeholder');
    
    // Show diff panels and hide placeholder/loading
    if (placeholder) placeholder.style.display = 'none';
    const loadingMsg = document.getElementById('diff-loading-msg');
    if (loadingMsg) loadingMsg.style.display = 'none';
    if (diffPanels) diffPanels.style.display = 'flex';

    // Check if panels exist
    if (!leftPanel || !rightPanel) {
      console.error('Diff panels not found:', { leftPanel: !!leftPanel, rightPanel: !!rightPanel });
      return;
    }

    // Parse the spruce diff output and create formatted display
    const formattedDiff = formatSpruceDiff(diffString);
    
    // Display diff in left panel, hide right panel or show legend
    leftPanel.innerHTML = `
      <div class="spruce-diff-container">
        <div class="diff-header">
          <h4>Configuration Changes: ${fromVersion.id} → ${toVersion.id}</h4>
        </div>
        <div class="spruce-diff-output">
          ${formattedDiff}
        </div>
      </div>
    `;
    
    // Hide right panel for spruce diff display
    rightPanel.style.display = 'none';
    leftPanel.style.width = '100%';
  };

  // Display message when there are no changes
  const displayNoChangesMessage = (fromVersion, toVersion) => {
    const leftPanel = document.getElementById('diff-left-content');
    const rightPanel = document.getElementById('diff-right-content');
    const diffPanels = document.querySelector('.diff-panels');
    const placeholder = document.getElementById('diff-placeholder');
    
    // Show diff panels and hide placeholder/loading
    if (placeholder) placeholder.style.display = 'none';
    const loadingMsg = document.getElementById('diff-loading-msg');
    if (loadingMsg) loadingMsg.style.display = 'none';
    if (diffPanels) diffPanels.style.display = 'flex';

    if (!leftPanel || !rightPanel) {
      console.error('Diff panels not found');
      return;
    }

    leftPanel.innerHTML = `
      <div class="no-changes-message">
        <div class="diff-header">
          <h4>Configuration Comparison: ${fromVersion.id} → ${toVersion.id}</h4>
        </div>
        <div class="no-changes-content">
          <p><i class="fa fa-check-circle" style="color: #28a745; margin-right: 8px;"></i>No changes detected between these configuration versions.</p>
        </div>
      </div>
    `;
    
    rightPanel.style.display = 'none';
    leftPanel.style.width = '100%';
  };

  // Format spruce diff output with syntax highlighting
  const formatSpruceDiff = (diffString) => {
    if (!diffString || diffString.trim() === '') {
      return '<p>No diff content available</p>';
    }

    const lines = diffString.split('\n');
    let html = '<div class="spruce-diff-lines">';
    
    lines.forEach((line, index) => {
      let lineClass = 'spruce-diff-line';
      let lineContent = escapeHtml(line);
      
      // Highlight different types of changes based on spruce diff output
      if (line.startsWith('+ ')) {
        lineClass += ' addition';
        lineContent = '<span class="diff-indicator">+</span>' + lineContent.substring(1);
      } else if (line.startsWith('- ')) {
        lineClass += ' removal';
        lineContent = '<span class="diff-indicator">-</span>' + lineContent.substring(1);
      } else if (line.includes('⇆ order changed')) {
        lineClass += ' reorder';
      } else if (line.match(/^\w+$/)) {
        // Section headers (like "disk_types")
        lineClass += ' section-header';
      } else if (line.trim() === '') {
        lineClass += ' empty';
      }
      
      html += `<div class="${lineClass}">${lineContent}</div>`;
    });
    
    html += '</div>';
    return html;
  };

  // escapeHtml function is already defined earlier in the code

  // Set up task modal tab switching
  const setupTaskModalTabs = () => {
    const tabGroupId = 'task-tabs';
    const tabButtons = document.querySelectorAll(`.manifest-tab-btn[data-group="${tabGroupId}"]`);
    const tabPanes = document.querySelectorAll(`.manifest-tab-pane[data-group="${tabGroupId}"]`);

    tabButtons.forEach(button => {
      button.addEventListener('click', () => {
        const tabName = button.dataset.tab;

        // Update active states
        tabButtons.forEach(btn => btn.classList.remove('active'));
        button.classList.add('active');

        // Show/hide content panes
        tabPanes.forEach(pane => {
          pane.classList.remove('active');
          pane.style.display = 'none';
        });

        const targetPane = document.querySelector(`.manifest-tab-pane[data-tab="${tabName}"][data-group="${tabGroupId}"]`);
        if (targetPane) {
          targetPane.classList.add('active');
          targetPane.style.display = 'block';

          // Load content if not already loaded
          if (targetPane.innerHTML.includes('Loading') && window.currentTaskId) {
            loadTaskTabContent(window.currentTaskId, tabName);
          }
        }
      });
    });
  };

  // Load task details
  const loadTaskDetails = async (taskId) => {
    const loadingDiv = document.getElementById('task-details-loading');
    const contentDiv = document.getElementById('task-details-content');

    try {
      // Fetch task details
      const response = await fetch(`/b/tasks/${taskId}`, { cache: 'no-cache' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const taskData = await response.json();
      const task = taskData;

      // Update task summary and modal title
      document.getElementById('task-details-modal-title').textContent = `Task Details - Task # ${task.id}`;
      document.getElementById('task-description').textContent = task.description || '-';
      document.getElementById('task-deployment').textContent = task.deployment || '-';
      document.getElementById('task-user').textContent = task.user || '-';
      document.getElementById('task-context-id').textContent = task.context_id || '-';
      document.getElementById('task-started').textContent = formatTimestamp(task.started_at);
      document.getElementById('task-finished').textContent = task.ended_at ? formatTimestamp(task.ended_at) : '-';

      // Calculate and set duration
      let duration = '-';
      if (task.started_at) {
        const start = new Date(task.started_at);
        const end = task.ended_at ? new Date(task.ended_at) : new Date();
        const durationMs = end - start;
        if (durationMs > 0) {
          const minutes = Math.floor(durationMs / 60000);
          const seconds = Math.floor((durationMs % 60000) / 1000);
          duration = minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`;
        }
      }
      document.getElementById('task-duration').textContent = duration;

      // Set state badge
      const stateBadge = document.getElementById('task-state-badge');
      stateBadge.innerHTML = `<span class="task-state ${task.state}">${task.state}</span>`;

      // Show/hide cancel button
      const cancelBtn = document.getElementById('cancel-task-btn');
      if (task.state === 'processing' || task.state === 'queued') {
        cancelBtn.style.display = 'inline-block';
        cancelBtn.onclick = () => cancelTask(taskId);
      } else {
        cancelBtn.style.display = 'none';
      }

      // Hide loading and show content
      loadingDiv.style.display = 'none';
      contentDiv.style.display = 'block';

      // Load initial tab content (task)
      await loadTaskTabContent(taskId, 'task');

    } catch (error) {
      console.error('Failed to load task details:', error);
      loadingDiv.innerHTML = `<div class="error">Failed to load task details: ${error.message}</div>`;
    }
  };

  // Load specific tab content for task modal
  const loadTaskTabContent = async (taskId, tabType) => {
    const contentDiv = document.getElementById(`task-${tabType}-content`);
    if (!contentDiv) return;

    contentDiv.innerHTML = '<div class="loading">Loading...</div>';

    try {
      let content = '';

      if (tabType === 'events') {
        // Events are included in the task details response
        const response = await fetch(`/b/tasks/${taskId}`, { cache: 'no-cache' });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const taskData = await response.json();

        if (taskData.events && taskData.events.length > 0) {
          content = formatTaskEvents(taskData.events);
        } else {
          content = '<div class="no-data">No events available</div>';
        }

      } else if (tabType === 'task' || tabType === 'output' || tabType === 'debug' || tabType === 'cpi') {
        let outputType;
        if (tabType === 'task') {
          outputType = 'task';
        } else if (tabType === 'output') {
          outputType = 'result';
        } else if (tabType === 'debug') {
          outputType = 'debug';
        } else if (tabType === 'cpi') {
          outputType = 'cpi';
        }
        
        let fetchUrl = `/b/tasks/${taskId}/output`;
        if (outputType !== 'task') {
          fetchUrl += `?type=${outputType}`;
        }
        const response = await fetch(fetchUrl, { cache: 'no-cache' });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const outputData = await response.json();

        if (Array.isArray(outputData)) {
          content = formatTaskEvents(outputData);
        } else if (outputData.output) {
          // For 'task' type, parse JSONL format (JSON Lines) from BOSH EventOutput
          if (tabType === 'task' && typeof outputData.output === 'string') {
            const lines = outputData.output.trim().split('\n').filter(line => line.trim());
            const events = [];
            
            for (const line of lines) {
              try {
                const event = JSON.parse(line);
                // Convert Unix timestamp to readable format
                const timestamp = new Date(event.time * 1000);
                const timeStr = timestamp.toISOString().substring(11, 19); // HH:MM:SS format
                const dateStr = timestamp.toISOString().substring(0, 10); // YYYY-MM-DD format
                
                // Build display message from stage and task info
                let message = event.stage;
                if (event.task && event.task !== event.stage) {
                  message += `: ${event.task}`;
                }
                if (event.state !== 'finished' && event.state !== 'started') {
                  message += ` (${event.state})`;
                }
                if (event.progress !== undefined && event.progress > 0 && event.progress < 100) {
                  message += ` ${event.progress}%`;
                }
                if (event.data && event.data.status) {
                  message += ` - ${event.data.status}`;
                }
                
                events.push({
                  date: dateStr,
                  time: timeStr,
                  level: event.state.toUpperCase(),
                  message: message
                });
              } catch (e) {
                // Skip malformed JSON lines
                console.warn('Failed to parse event line:', line, e);
              }
            }
            
            // Format as task events table
            if (events.length > 0) {
              content = formatTaskEventsAsLogs(events, 'task');
            } else {
              content = formatTaskOutput('', outputType);
            }
          } else {
            content = formatTaskOutput(outputData.output, outputType);
          }
        } else {
          content = formatTaskOutput('', outputType);
        }

      } else if (tabType === 'raw') {
        const response = await fetch(`/b/tasks/${taskId}`, { cache: 'no-cache' });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const taskData = await response.json();
        const rawText = JSON.stringify(taskData, null, 2);

        // Store raw text for copying
        if (!window.taskRawTexts) window.taskRawTexts = {};
        window.taskRawTexts[taskId] = rawText;

        content = `
          <div class="manifest-container">
            <div class="manifest-header">
              <button class="copy-btn-manifest" onclick="window.copyTaskRaw('${taskId}', event)"
                      title="Copy raw data to clipboard">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
              </button>
            </div>
            <pre>${rawText.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</pre>
          </div>
        `;
      }

      contentDiv.innerHTML = content;

      // Initialize search and sorting functionality for tables
      if (tabType === 'task' || tabType === 'output' || tabType === 'debug' || tabType === 'cpi') {
        let tableClassSuffix;
        if (tabType === 'task') {
          tableClassSuffix = 'task';
        } else if (tabType === 'output') {
          tableClassSuffix = 'result';
        } else if (tabType === 'debug') {
          tableClassSuffix = 'debug';
        } else if (tabType === 'cpi') {
          tableClassSuffix = 'cpi';
        }
        const tableClass = `task-${tableClassSuffix}-table`;
        // Set a timeout to ensure DOM is ready
        setTimeout(() => {
          initializeSorting(tableClass);
          attachSearchFilter(tableClass);
        }, 100);
      } else if (tabType === 'events') {
        // Events table uses logs-table class
        setTimeout(() => {
          initializeSorting('logs-table');
          attachSearchFilter('logs-table');
        }, 100);
      }

    } catch (error) {
      console.error(`Failed to load ${tabType} content:`, error);
      contentDiv.innerHTML = `<div class="error">Failed to load ${tabType}: ${error.message}</div>`;
    }
  };

  // Format task events for modal display
  const formatTaskEvents = (events) => {
    if (!events || events.length === 0) {
      return `
        <div class="logs-table-container">
          <table class="logs-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>Stage</th>
                <th>Task</th>
                <th>State</th>
                <th>Progress</th>
                <th>Error</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colspan="6" class="no-data-row">No events recorded</td>
              </tr>
            </tbody>
          </table>
        </div>
      `;
    }

    return `
      <div class="logs-table-container">
        <table class="logs-table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Stage</th>
              <th>Task</th>
              <th>State</th>
              <th>Progress</th>
              <th>Error</th>
            </tr>
          </thead>
          <tbody>
            ${events.map(event => {
      const time = formatTimestamp(event.time);
      const progress = event.progress ? `${event.progress}%` : '-';
      const error = event.error ? event.error.message : '-';

      // Make task field clickable if it's a valid task ID
      let taskInfo = event.task || '-';
      if (event.task && !isNaN(event.task) && event.task !== '-') {
        taskInfo = `<a href="#" class="task-link" data-task-id="${event.task}" onclick="showTaskDetails(${event.task}, event); return false;">${event.task}</a>`;
      }

      return `
                <tr>
                  <td>${time}</td>
                  <td>${event.stage || '-'}</td>
                  <td>${taskInfo}</td>
                  <td><span class="event-state ${event.state}">${event.state || '-'}</span></td>
                  <td>${progress}</td>
                  <td class="event-error">${error}</td>
                </tr>
              `;
    }).join('')}
          </tbody>
        </table>
      </div>
    `;
  };

  // Format task events as logs table for the Task tab
  const formatTaskEventsAsLogs = (events, outputType) => {
    const tableClass = `task-${outputType}-table`;
    
    if (!events || events.length === 0) {
      return `
        <div class="logs-table-container">
          <div class="table-controls-container">
            <div class="search-filter-container">
              ${createSearchFilter(tableClass, 'Search task events...')}
            </div>
            <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableClass}', event)"
                    title="Copy filtered table rows">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2 2v1"></path></svg>
            </button>
          </div>
          <table class="${tableClass}">
            <thead>
              <tr>
                <th>Timestamp</th>
                <th>Level</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colspan="3" class="no-data-row">No task events available</td>
              </tr>
            </tbody>
          </table>
        </div>
      `;
    }

    const rows = events.map(event => {
      const timestamp = event.date && event.time ? `${event.date} ${event.time}` : (event.time || '-');
      const level = event.level || 'INFO';
      const message = event.message || '';
      
      return `
        <tr>
          <td class="timestamp">${timestamp}</td>
          <td class="level"><span class="log-level ${level.toLowerCase()}">${level}</span></td>
          <td class="message">${highlightPatterns(message)}</td>
        </tr>
      `;
    }).join('');

    return `
      <div class="logs-table-container">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter(tableClass, 'Search task events...')}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableClass}', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
        </div>
        <table class="${tableClass}">
          <thead>
            <tr>
              <th>Timestamp</th>
              <th>Level</th>
              <th>Message</th>
            </tr>
          </thead>
          <tbody>
            ${rows}
          </tbody>
        </table>
      </div>
    `;
  };

  // Format task output as table (like logs view)
  const formatTaskOutput = (output, outputType) => {
    const tableClass = `task-${outputType}-table`;

    if (!output || output.trim() === '') {
      return `
        <div class="logs-table-container">
          <div class="table-controls-container">
            <div class="search-filter-container">
              ${createSearchFilter(tableClass, `Search ${outputType}...`)}
            </div>
            <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableClass}', event)"
                    title="Copy filtered table rows">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
            </button>
          </div>
          <table class="${tableClass}">
            <thead>
              <tr>
                <th>Timestamp</th>
                <th>Level</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colspan="3" class="no-data-row">No ${outputType} output available</td>
              </tr>
            </tbody>
          </table>
        </div>
      `;
    }

    // Parse output lines similar to log parsing
    const lines = output.split('\n').filter(line => line.trim());
    const parsedLines = lines.map(line => parseLogLine(line));

    return `
      <div class="logs-table-container">
        <div class="table-controls-container">
          <div class="search-filter-container">
            ${createSearchFilter(tableClass, `Search ${outputType}...`)}
          </div>
          <button class="copy-btn-logs" onclick="window.copyTableRowsAsText('.${tableClass}', event)"
                  title="Copy filtered table rows">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
          </button>
        </div>
        <table class="${tableClass}">
          <thead>
            <tr>
              <th>Timestamp</th>
              <th>Level</th>
              <th>Message</th>
            </tr>
          </thead>
          <tbody>
            ${parsedLines.map(row => renderLogRow(row)).join('')}
          </tbody>
        </table>
      </div>
    `;
  };

  // Cancel task function
  const cancelTask = async (taskId) => {
    if (!confirm(`Are you sure you want to cancel task ${taskId}?`)) {
      return;
    }

    try {
      const response = await fetch(`/b/tasks/${taskId}/cancel`, {
        method: 'POST',
        cache: 'no-cache'
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const result = await response.json();
      console.log('Task cancelled:', result);

      // Refresh task details
      await loadTaskDetails(taskId);

      // Refresh tasks table if visible
      if (document.querySelector('.detail-tab[data-tab="tasks"].active')) {
        refreshTasksTable();
      }

    } catch (error) {
      console.error('Failed to cancel task:', error);
      alert(`Failed to cancel task: ${error.message}`);
    }
  };

  // Auto-refresh for task modal
  const startTaskModalAutoRefresh = () => {
    // Clear any existing interval
    if (window.taskModalAutoRefresh) {
      clearInterval(window.taskModalAutoRefresh);
    }

    // Set up new interval for running tasks
    window.taskModalAutoRefresh = setInterval(async () => {
      if (window.currentTaskId) {
        const stateBadge = document.querySelector('#task-state-badge .task-state');
        if (stateBadge && (stateBadge.classList.contains('processing') || stateBadge.classList.contains('queued'))) {
          await loadTaskDetails(window.currentTaskId);

          // Reload active tab content
          const activeTab = document.querySelector('.task-detail-tab.active');
          if (activeTab) {
            await loadTaskTabContent(window.currentTaskId, activeTab.dataset.tab);
          }
        }
      }
    }, 10000); // Refresh every 10 seconds for modal
  };

  // Cancel task and refresh modal
  window.cancelTaskAndRefresh = () => {
    if (window.currentTaskId) {
      cancelTask(window.currentTaskId);
    }
  };

  // Copy task raw data function
  window.copyTaskRaw = async (taskId, event) => {
    const text = window.taskRawTexts && window.taskRawTexts[taskId];
    if (!text) {
      console.error('Task raw text not found for ID:', taskId);
      return;
    }

    const button = event.currentTarget;
    try {
      await navigator.clipboard.writeText(text);
      // Visual feedback
      const originalTitle = button.title;
      const spanElement = button.querySelector('span');
      const originalText = spanElement ? spanElement.textContent : '';
      button.classList.add('copied');
      button.title = 'Copied!';
      if (spanElement) spanElement.textContent = 'Copied!';
      setTimeout(() => {
        button.classList.remove('copied');
        button.title = originalTitle;
        if (spanElement) spanElement.textContent = originalText;
      }, 2000);
    } catch (err) {
      console.error('Failed to copy task raw data:', err);
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
        setTimeout(() => {
          button.classList.remove('copied');
        }, 2000);
      } catch (fallbackErr) {
        console.error('Fallback copy also failed:', fallbackErr);
      } finally {
        document.body.removeChild(textarea);
      }
    }
  };

  // Fetch trusted certificates from VM certificate store
  window.fetchTrustedCertificates = async function () {
    const resultsContainer = document.getElementById('trusted-certificates-results');
    const listContainer = document.getElementById('trusted-certificates-list');
    const button = document.querySelector('[onclick="fetchTrustedCertificates()"]');

    if (!resultsContainer || !listContainer) {
      console.error('Trusted certificates containers not found');
      return;
    }

    // Disable button and show loading state
    const originalText = button ? button.textContent : '';
    if (button) {
      button.disabled = true;
      button.classList.add('loading');
      button.textContent = 'Loading...';
    }

    try {
      listContainer.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Loading BOSH trusted certificate files...</div>';
      resultsContainer.style.display = 'block';

      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 30000);

      const response = await fetch('/b/certificates/trusted', {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json'
        },
        signal: controller.signal
      });

      clearTimeout(timeoutId);

      if (!response.ok) {
        if (response.status === 401) {
          throw new Error('Authentication required. Please check your credentials.');
        } else if (response.status === 403) {
          throw new Error('Access denied. Insufficient permissions.');
        } else if (response.status === 404) {
          throw new Error('Certificates API not available.');
        } else if (response.status >= 500) {
          throw new Error(`Server error (${response.status}). Please try again later.`);
        } else {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to fetch trusted certificate files');
      }

      if (!data.data || !data.data.files) {
        throw new Error('Invalid response format received from server');
      }

      renderTrustedCertificateFileList(listContainer, data.data.files);

      // Success feedback
      if (button) {
        button.classList.add('success');
        button.textContent = 'Loaded!';
      }

    } catch (error) {
      console.error('Failed to fetch trusted certificate files:', error);

      const errorMessage = error.name === 'AbortError'
        ? 'Request timed out. The server may be busy.'
        : error.message;

      listContainer.innerHTML = `
        <div class="cert-error">
          <h4>Failed to Load Trusted Certificate Files</h4>
          <p>${errorMessage}</p>
          <button class="execute-btn" onclick="fetchTrustedCertificates()" style="margin-top: 10px;">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M1 4v6h6"/>
              <path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/>
            </svg>
            Retry
          </button>
        </div>
      `;

      // Error feedback on button
      if (button) {
        button.classList.add('error');
        button.textContent = 'Failed';
      }
    } finally {
      // Always restore button state
      if (button) {
        setTimeout(() => {
          button.disabled = false;
          button.classList.remove('loading', 'success', 'error');
          button.textContent = originalText;
        }, 2000);
      }
    }
  };

  // Render file list for trusted certificates
  const renderTrustedCertificateFileList = (container, files) => {
    if (!files || files.length === 0) {
      container.innerHTML = '<div class="cert-error">No BOSH trusted certificate files found</div>';
      return;
    }

    const fileListHTML = `
      <div class="cert-list-header">
        <h4>BOSH Trusted Certificate Files</h4>
        <span class="cert-list-count">${files.length} file${files.length === 1 ? '' : 's'}</span>
      </div>
      <div class="cert-file-list">
        ${files.map((file, index) => `
          <div class="cert-file-item" data-index="${index}">
            <div class="cert-file-header">
              <div class="cert-file-name">${file.name}</div>
              <button class="cert-file-load-btn" onclick="loadTrustedCertificateFile('${file.path}', '${file.name}', ${index})">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
                  <polyline points="14,2 14,8 20,8"/>
                  <line x1="16" y1="13" x2="8" y2="13"/>
                  <line x1="16" y1="17" x2="8" y2="17"/>
                  <line x1="10" y1="9" x2="8" y2="9"/>
                </svg>
                Load Certificate
              </button>
            </div>
            <div class="cert-file-path">${file.path}</div>
            <div class="cert-file-details" id="cert-file-details-${index}" style="display: none;"></div>
          </div>
        `).join('')}
      </div>
    `;

    container.innerHTML = fileListHTML;
  };

  // Load individual certificate file details
  window.loadTrustedCertificateFile = async function (filePath, fileName, index) {
    const button = document.querySelector(`.cert-file-item[data-index="${index}"] .cert-file-load-btn`);
    const detailsContainer = document.getElementById(`cert-file-details-${index}`);

    if (!button || !detailsContainer) {
      console.error('Certificate file elements not found');
      return;
    }

    // Show loading state
    const originalText = button.innerHTML;
    button.disabled = true;
    button.innerHTML = '<div class="cert-loading-spinner"></div>Loading...';

    try {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 15000);

      const response = await fetch('/b/certificates/trusted/file', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({ filePath }),
        signal: controller.signal
      });

      clearTimeout(timeoutId);

      if (!response.ok) {
        if (response.status === 401) {
          throw new Error('Authentication required. Please check your credentials.');
        } else if (response.status === 403) {
          throw new Error('Access denied. Insufficient permissions.');
        } else if (response.status === 404) {
          throw new Error('Certificate file not found.');
        } else if (response.status >= 500) {
          throw new Error(`Server error (${response.status}). Please try again later.`);
        } else {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to load certificate file');
      }

      if (!data.data || !data.data.certificates || data.data.certificates.length === 0) {
        throw new Error('No certificate data received');
      }

      // Display certificate details
      const certificate = data.data.certificates[0];
      detailsContainer.innerHTML = `
        <div class="cert-details-summary">
          <div class="cert-detail-row">
            <span class="cert-detail-label">Subject:</span>
            <span class="cert-detail-value">${formatCertificateSubject(certificate.details.subject)}</span>
          </div>
          <div class="cert-detail-row">
            <span class="cert-detail-label">Issuer:</span>
            <span class="cert-detail-value">${formatCertificateSubject(certificate.details.issuer)}</span>
          </div>
          <div class="cert-detail-row">
            <span class="cert-detail-label">Valid Until:</span>
            <span class="cert-detail-value">${new Date(certificate.details.notAfter).toLocaleDateString()}</span>
          </div>
          <div class="cert-actions">
            <button class="cert-inspect-btn">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="11" cy="11" r="8"/>
                <path d="M21 21l-4.35-4.35"/>
              </svg>
              Inspect Certificate
            </button>
          </div>
        </div>
      `;
      detailsContainer.style.display = 'block';

      // Add event listener to the inspect button
      const inspectButton = detailsContainer.querySelector('.cert-inspect-btn');
      if (inspectButton) {
        inspectButton.addEventListener('click', () => {
          console.log('Trusted cert inspect button clicked', certificate);
          window.inspectCertificateFromList(certificate);
        });
      } else {
        console.error('Could not find inspect button in details container');
      }

      // Update button to success state
      button.innerHTML = `
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <polyline points="20,6 9,17 4,12"/>
        </svg>
        Loaded
      `;
      button.classList.add('success');

    } catch (error) {
      console.error('Failed to load certificate file:', error);

      const errorMessage = error.name === 'AbortError'
        ? 'Request timed out. The server may be busy.'
        : error.message;

      detailsContainer.innerHTML = `
        <div class="cert-error">
          <h5>Failed to Load Certificate</h5>
          <p>${errorMessage}</p>
          <button class="execute-btn cert-retry-button" style="margin-top: 10px;">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M1 4v6h6"/>
              <path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/>
            </svg>
            Retry
          </button>
        </div>
      `;
      detailsContainer.style.display = 'block';

      // Add event listener to retry button
      const retryButton = detailsContainer.querySelector('.cert-retry-button');
      if (retryButton) {
        retryButton.addEventListener('click', () => {
          window.loadTrustedCertificateFile(filePath, fileName, index);
        });
      }

      // Update button to error state
      button.innerHTML = `
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="10"/>
          <line x1="15" y1="9" x2="9" y2="15"/>
          <line x1="9" y1="9" x2="15" y2="15"/>
        </svg>
        Failed
      `;
      button.classList.add('error');

    } finally {
      // Restore button after delay
      setTimeout(() => {
        button.disabled = false;
        button.classList.remove('success', 'error');
        button.innerHTML = originalText;
      }, 3000);
    }
  };

  // Fetch blacksmith configuration certificates
  window.fetchBlacksmithCertificates = async function () {
    const resultsContainer = document.getElementById('blacksmith-certificates-results');
    const listContainer = document.getElementById('blacksmith-certificates-list');
    const button = document.querySelector('[onclick="fetchBlacksmithCertificates()"]');

    if (!resultsContainer || !listContainer) {
      console.error('Blacksmith certificates containers not found');
      return;
    }

    // Disable button and show loading state
    const originalText = button ? button.textContent : '';
    if (button) {
      button.disabled = true;
      button.classList.add('loading');
      button.textContent = 'Loading...';
    }

    try {
      listContainer.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Loading configuration certificates...</div>';
      resultsContainer.style.display = 'block';

      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 30000);

      const response = await fetch('/b/certificates/blacksmith', {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json'
        },
        signal: controller.signal
      });

      clearTimeout(timeoutId);

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to fetch blacksmith certificates');
      }

      renderCertificateList(listContainer, data.data.certificates, 'blacksmith');

      // Success feedback
      if (button) {
        button.classList.add('success');
        button.textContent = 'Loaded!';

        // Reset button state after 3 seconds
        setTimeout(() => {
          button.classList.remove('success', 'loading');
          button.disabled = false;
          button.textContent = originalText;
        }, 3000);
      }

    } catch (error) {
      console.error('Failed to fetch blacksmith certificates:', error);
      listContainer.innerHTML = `<div class="cert-error">Failed to load configuration certificates: ${error.message}</div>`;

      // Error feedback
      if (button) {
        button.classList.add('error');
        button.textContent = 'Failed';

        // Reset button state after 3 seconds
        setTimeout(() => {
          button.classList.remove('error', 'loading');
          button.disabled = false;
          button.textContent = originalText;
        }, 3000);
      }
    }
  };

  // Fetch certificate from network endpoint
  window.fetchEndpointCertificate = async function () {
    const endpointInput = document.getElementById('endpoint-address');
    const resultsContainer = document.getElementById('endpoint-certificate-results');
    const detailsContainer = document.getElementById('endpoint-certificate-details');
    const button = document.querySelector('[onclick="fetchEndpointCertificate()"]');

    if (!endpointInput || !resultsContainer || !detailsContainer) {
      console.error('Endpoint certificate containers not found');
      return;
    }

    const endpoint = endpointInput.value.trim();
    if (!endpoint) {
      // Enhanced validation
      endpointInput.classList.add('error');
      endpointInput.focus();
      setTimeout(() => endpointInput.classList.remove('error'), 3000);

      detailsContainer.innerHTML = `
        <div class="cert-error">
          <h4>Invalid Input</h4>
          <p>Please enter a valid endpoint address (e.g., example.com:443 or https://example.com)</p>
        </div>
      `;
      resultsContainer.style.display = 'block';
      return;
    }

    // Validate endpoint format
    const endpointPattern = /^(https?:\/\/)?([-\w\.]+)(:(\d+))?\/?.*$/;
    if (!endpointPattern.test(endpoint)) {
      endpointInput.classList.add('error');
      detailsContainer.innerHTML = `
        <div class="cert-error">
          <h4>Invalid Endpoint Format</h4>
          <p>Please use format: hostname:port or https://hostname</p>
        </div>
      `;
      resultsContainer.style.display = 'block';
      return;
    }

    // Disable button and show loading state
    const originalText = button ? button.textContent : '';
    if (button) {
      button.disabled = true;
      button.classList.add('loading');
      button.textContent = 'Connecting...';
    }

    try {
      detailsContainer.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Connecting to endpoint...</div>';
      resultsContainer.style.display = 'block';

      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 45000); // Longer timeout for network connections

      const response = await fetch('/b/certificates/endpoint', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          endpoint: endpoint,
          timeout: 30
        }),
        signal: controller.signal
      });

      clearTimeout(timeoutId);

      if (!response.ok) {
        if (response.status === 400) {
          throw new Error('Invalid endpoint address or connection parameters.');
        } else if (response.status === 401) {
          throw new Error('Authentication required.');
        } else if (response.status === 403) {
          throw new Error('Access denied.');
        } else if (response.status === 404) {
          throw new Error('Endpoint certificates API not available.');
        } else if (response.status === 408) {
          throw new Error('Connection timeout. The endpoint may be unreachable.');
        } else if (response.status >= 500) {
          throw new Error(`Server error (${response.status}). Please try again later.`);
        } else {
          throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to fetch endpoint certificate');
      }

      // For endpoint certificates, we get a single certificate, not a list
      if (data.data && data.data.certificates && data.data.certificates.length > 0) {
        renderCertificateList(detailsContainer, data.data.certificates, 'endpoint');

        // Success feedback
        if (button) {
          button.classList.add('success');
          button.textContent = 'Connected!';
        }
      } else {
        detailsContainer.innerHTML = `
          <div class="cert-error">
            <h4>No Certificate Found</h4>
            <p>The endpoint did not present a certificate or is not using TLS.</p>
          </div>
        `;
      }

    } catch (error) {
      console.error('Failed to fetch endpoint certificate:', error);

      const errorMessage = error.name === 'AbortError'
        ? 'Connection timed out. The endpoint may be unreachable.'
        : error.message;

      detailsContainer.innerHTML = `
        <div class="cert-error">
          <h4>Connection Failed</h4>
          <p>${errorMessage}</p>
          <button class="execute-btn" onclick="fetchEndpointCertificate()" style="margin-top: 10px;">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M1 4v6h6"/>
              <path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/>
            </svg>
            Retry
          </button>
        </div>
      `;

      // Error feedback on button
      if (button) {
        button.classList.add('error');
        button.textContent = 'Failed';
      }
    } finally {
      // Always restore button state
      if (button) {
        setTimeout(() => {
          button.disabled = false;
          button.classList.remove('loading', 'success', 'error');
          button.textContent = originalText;
        }, 2000);
      }
    }
  };

  // Render a list of certificates
  const renderCertificateList = (container, certificates, source) => {
    if (!certificates || certificates.length === 0) {
      container.innerHTML = '<div class="cert-error">No certificates found</div>';
      return;
    }

    const sourceLabel = source === 'trusted' ? 'Trusted Store' :
      source === 'blacksmith' ? 'Configuration' :
        source === 'manifest' ? 'Manifest' :
          source === 'vm-trusted' ? 'VM Trusted Store' :
            source === 'service-endpoint' ? 'Service Endpoint' : 'Endpoint';

    const html = `
      <div class="cert-list-header">
        <h4 class="cert-list-title">${sourceLabel} Certificates</h4>
        <span class="cert-list-count">${certificates.length} certificate${certificates.length === 1 ? '' : 's'}</span>
      </div>
      <div class="cert-list">
        ${certificates.map((cert, index) => `
          <div class="cert-list-item">
            <div class="cert-item-info">
              <div class="cert-item-name">${cert.name || `Certificate ${index + 1}`}</div>
              ${cert.details && cert.details.subject ? `
                <div class="cert-item-subject">${formatCertificateSubject(cert.details.subject)}</div>
              ` : ''}
              ${cert.path ? `
                <div class="cert-item-path">${cert.path}</div>
              ` : ''}
            </div>
            <button class="cert-inspect-btn" data-cert-index="${index}">
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="margin-right: 4px;">
                <circle cx="11" cy="11" r="8"/>
                <path d="M21 21l-4.35-4.35"/>
              </svg>
              Inspect
            </button>
          </div>
        `).join('')}
      </div>
    `;

    container.innerHTML = html;

    // Store certificate data for reference and add event listeners
    container.certificateData = certificates;

    // Add event listeners to all inspect buttons
    const inspectButtons = container.querySelectorAll('.cert-inspect-btn');

    // Validate that buttons were created
    if (inspectButtons.length !== certificates.length) {
      console.error(`Expected ${certificates.length} inspect buttons, but found ${inspectButtons.length}`);
    }

    inspectButtons.forEach((button, index) => {
      button.addEventListener('click', (event) => {
        event.preventDefault();
        event.stopPropagation();

        const cert = certificates[index];
        console.log(`Inspect button clicked for certificate ${index}:`, cert);
        if (cert) {
          window.inspectCertificateFromList(cert);
        } else {
          console.error('No certificate found at index:', index);
        }
      });
    });
  };

  // Format certificate subject for display
  const formatCertificateSubject = (subject) => {
    if (typeof subject === 'string') return subject;
    if (!subject) return 'N/A';

    const parts = [];
    if (subject.commonName) parts.push(`CN=${subject.commonName}`);
    if (subject.organization && subject.organization.length > 0) parts.push(`O=${subject.organization.join(', ')}`);
    if (subject.country && subject.country.length > 0) parts.push(`C=${subject.country.join(', ')}`);

    return parts.length > 0 ? parts.join(', ') : 'N/A';
  };

  // Handle certificate inspection from list
  window.inspectCertificateFromList = function (certificateData) {
    if (!certificateData) {
      console.error('No certificate data to inspect');
      return;
    }

    console.log('Inspecting certificate data:', certificateData);

    // Use the certificate details if available, otherwise use the whole object
    let certToInspect = certificateData.details || certificateData;

    // If certToInspect still doesn't have the required fields, try to extract them
    if (!certToInspect.subject && !certToInspect.issuer && !certToInspect.pemEncoded) {
      // If we have a raw certificate object, try to find the parsed details
      if (certificateData.parsed) {
        certToInspect = certificateData.parsed;
      } else if (certificateData.certificate) {
        certToInspect = certificateData.certificate;
      } else if (certificateData.raw) {
        certToInspect = certificateData.raw;
      }
    }

    // Validate that we have usable certificate data
    if (!certToInspect || typeof certToInspect !== 'object') {
      console.error('Invalid certificate data structure:', certToInspect);
      alert('Certificate data is not in the expected format and cannot be inspected.');
      return;
    }

    console.log('Certificate to inspect:', certToInspect);

    // Use the global certificate inspector function
    window.inspectCertificate(certToInspect);
  };

  // Render certificates tab for service instances
  const renderServiceCertificatesTab = (instanceId) => {
    const tabGroupId = `service-cert-tabs-${instanceId}`;
    return `
      <div class="service-testing-container">
        <div class="manifest-details-container">
          <div class="manifest-tabs-nav">
            <button class="manifest-tab-btn active" data-tab="manifest" data-group="${tabGroupId}">Manifest</button>
            <button class="manifest-tab-btn" data-tab="trusted" data-group="${tabGroupId}">Trusted</button>
            <button class="manifest-tab-btn" data-tab="endpoint" data-group="${tabGroupId}">Endpoint</button>
          </div>

          <div class="manifest-tab-content">
            <!-- Manifest Certificates Tab -->
            <div class="manifest-tab-pane active" data-tab="manifest" data-group="${tabGroupId}">

              <div class="operation-content">
                <table class="operation-form">
                  <tr>
                    <td>
                      <h5>Deployment Manifest Certificates</h5>
                      <p>Scan the deployment manifest for certificate references and TLS configurations.</p>
                      <div class="form-row">
                        <button class="execute-btn" onclick="fetchManifestCertificates('${instanceId}')">
                          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
                            <polyline points="14,2 14,8 20,8"/>
                            <line x1="16" y1="13" x2="8" y2="13"/>
                            <line x1="16" y1="17" x2="8" y2="17"/>
                            <polyline points="10,9 9,9 8,9"/>
                          </svg>
                          Scan Manifest
                        </button>
                      </div>
                      <div id="manifest-certificates-results-${instanceId}" class="testing-history" style="display: none;">
                        <div class="history-header">
                          <h5>Manifest Certificates</h5>
                        </div>
                        <div id="manifest-certificates-list-${instanceId}" class="cert-list-container">
                          <!-- Certificate list will be populated here -->
                        </div>
                      </div>
                    </td>
                  </tr>
                </table>
              </div>
            </div>

            <!-- Trusted Certificates Tab -->
            <div class="manifest-tab-pane" data-tab="trusted" data-group="${tabGroupId}">
              <div class="operation-content">
                <table class="operation-form">
                  <tr>
                    <td>
                      <h5>Service VM Certificate Store</h5>
                      <p>Access the trusted certificate store on service instance virtual machines.</p>
                      <div class="form-row">
                        <button class="execute-btn" onclick="fetchServiceVMCertificates('${instanceId}')">
                          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>
                            <line x1="8" y1="21" x2="16" y2="21"/>
                            <line x1="12" y1="17" x2="12" y2="21"/>
                          </svg>
                          Fetch VM Certificates
                        </button>
                      </div>
                      <div id="vm-certificates-results-${instanceId}" class="testing-history" style="display: none;">
                        <div class="history-header">
                          <h5>VM Certificates</h5>
                        </div>
                        <div id="vm-certificates-list-${instanceId}" class="cert-list-container">
                          <!-- Certificate list will be populated here -->
                        </div>
                      </div>
                    </td>
                  </tr>
                </table>
              </div>
            </div>

            <!-- Service Endpoint Certificate Tab -->
            <div class="manifest-tab-pane" data-tab="endpoint" data-group="${tabGroupId}">

              <div class="operation-content">
                <table class="operation-form">
                  <tr>
                    <td>
                      <h5>Service Endpoint Certificate</h5>
                      <p>Connect to the service instance endpoint and retrieve its certificate.</p>
                      <div class="form-row">
                        <label>Service Endpoint:</label>
                        <input type="text" id="service-endpoint-address-${instanceId}"
                               placeholder="service-ip:port or service.domain.com:443"
                               value="" />
                      </div>
                      <div class="form-row">
                        <button class="execute-btn" onclick="fetchServiceEndpointCertificate('${instanceId}')">
                          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/>
                            <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>
                          </svg>
                          Fetch Service Certificate
                        </button>
                      </div>
                      <div id="service-endpoint-results-${instanceId}" class="testing-history" style="display: none;">
                        <div class="history-header">
                          <h5>Service Endpoint Certificate</h5>
                        </div>
                        <div id="service-endpoint-details-${instanceId}" class="cert-list-container">
                          <!-- Certificate details will be populated here -->
                        </div>
                      </div>
                    </td>
                  </tr>
                </table>
              </div>
            </div>
          </div>
        </div>
      </div>
    `;
  };

  // ================================================================
  // BROKER TAB FUNCTIONALITY - CF ENDPOINTS MANAGEMENT
  // ================================================================

  // Render the main broker tab content with CF endpoints management (matching Manifest view styling)
  const renderBrokerTab = () => {
    return `
      <div class="broker-details-container">
        <!-- CF Endpoint Selection Header Table -->
        <div class="broker-endpoint-header">
          <table class="endpoint-selector-table">
            <tbody>
              <tr>
                <td class="connection-status-cell">
                  <div class="connection-status" id="connection-status">
                    <span class="status-indicator" id="status-indicator"></span>
                    <button class="btn btn-sm btn-secondary" id="connection-btn" onclick="CFEndpointManager.toggleConnection()">Connect</button>
                  </div>
                </td>
                <td class="endpoint-dropdown-cell">
                  <select id="cf-endpoint-select" class="cf-endpoint-dropdown">
                    <option value="">Loading endpoints...</option>
                  </select>
                </td>
                <td class="endpoint-controls">
                  <button class="btn btn-icon btn-sm" onclick="CFEndpointManager.refreshEndpoints()" title="Refresh Endpoints">
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                      <path d="M21.5 2v6h-6M2.5 22v-6h6M2 11.5a10 10 0 0 1 18.8-4.3M22 12.5a10 10 0 0 1-18.8 4.2"/>
                    </svg>
                  </button>
                  <button class="btn btn-icon btn-sm" onclick="showCFEndpointForm()" title="Add Endpoint">
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                      <path d="M12 5v14M5 12h14"/>
                    </svg>
                  </button>
                  <button class="btn btn-sm btn-primary" onclick="CFEndpointManager.showRegistrationModal()" title="Register Broker" id="register-btn" style="display: none; margin-left: 8px;">
                    Register
                  </button>
                </td>
                <td>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Broker Details Table -->
        <div class="broker-details-table-container" id="broker-details-table-container" style="display: none;">
          <table class="broker-details-table">
            <tbody>
              <tr>
                <td><strong>Name:</strong></td>
                <td id="broker-detail-name">-</td>
                <td><strong>Endpoint:</strong></td>
                <td id="broker-detail-endpoint">-</td>
                <td><strong>Username:</strong></td>
                <td id="broker-detail-username">-</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- CF Detail Tabs (matching Manifest styling) -->
        <div class="broker-tabs-nav">
          <button class="broker-tab-btn active" data-cftab="marketplace">Marketplace</button>
          <button class="broker-tab-btn" data-cftab="services">Services</button>
        </div>

        <!-- CF Detail Content -->
        <div class="broker-tab-content">
          <div class="cf-detail-panel active" data-panel="marketplace">
            <div class="cf-marketplace-content" id="cf-marketplace-content">
              <div class="empty-state">
                <p>Select an endpoint from the dropdown above to view the service marketplace.</p>
              </div>
            </div>
          </div>

          <div class="cf-detail-panel" data-panel="services" style="display: none;">
            <div class="cf-services-content" id="cf-services-content">
              <div class="empty-state">
                <p>Select an endpoint from the dropdown above to view service instances.</p>
              </div>
            </div>
          </div>

        </div>
      </div>
    `;
  };

  // Show CF endpoint form for adding/editing endpoints
  const showCFEndpointForm = (endpoint = null) => {
    const modal = document.getElementById('cf-endpoint-modal');
    if (!modal) {
      // Create modal if it doesn't exist
      const modalHTML = `
        <div id="cf-endpoint-modal" class="modal-overlay" style="display: none;" role="dialog" aria-labelledby="cf-endpoint-modal-title" aria-hidden="true">
          <div class="modal">
            <div class="modal-content">
              <div class="modal-header">
                <h3 id="cf-endpoint-modal-title">${endpoint ? 'Edit' : 'Add'} CF Endpoint</h3>
                <button class="modal-close" onclick="hideCFEndpointForm()" aria-label="Close modal">&times;</button>
              </div>
            <div class="modal-body">
              <form id="cf-endpoint-form">
                <div class="form-group">
                  <label for="cf-endpoint-name">Name:</label>
                  <input type="text" id="cf-endpoint-name" name="name" required>
                </div>
                <div class="form-group">
                  <label for="cf-endpoint-url">CF API URL:</label>
                  <input type="url" id="cf-endpoint-url" name="endpoint" required>
                </div>
                <div class="form-group">
                  <label for="cf-endpoint-username">Username:</label>
                  <input type="text" id="cf-endpoint-username" name="username" required>
                </div>
                <div class="form-group">
                  <label for="cf-endpoint-password">Password:</label>
                  <input type="password" id="cf-endpoint-password" name="password" required>
                </div>
              </form>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" onclick="hideCFEndpointForm()">Cancel</button>
              <button type="button" class="btn btn-primary" onclick="saveCFEndpoint()">${endpoint ? 'Update' : 'Add'} Endpoint</button>
            </div>
            </div>
          </div>
        </div>
      `;
      document.body.insertAdjacentHTML('beforeend', modalHTML);
    }

    // Show the modal with proper overlay
    const newModal = document.getElementById('cf-endpoint-modal');
    newModal.style.display = 'flex';
    newModal.setAttribute('aria-hidden', 'false');

    // Pre-fill form if editing
    if (endpoint) {
      document.getElementById('cf-endpoint-name').value = endpoint.name || '';
      document.getElementById('cf-endpoint-url').value = endpoint.endpoint || '';
      document.getElementById('cf-endpoint-username').value = endpoint.username || '';
    }
  };

  // Hide CF endpoint form
  const hideCFEndpointForm = () => {
    const modal = document.getElementById('cf-endpoint-modal');
    if (modal) {
      modal.style.display = 'none';
      modal.setAttribute('aria-hidden', 'true');
    }
  };

  // Save CF endpoint
  const saveCFEndpoint = async () => {
    const form = document.getElementById('cf-endpoint-form');
    const formData = new FormData(form);

    const endpointData = {
      name: formData.get('name'),
      endpoint: formData.get('endpoint'),
      username: formData.get('username'),
      password: formData.get('password')
    };

    try {
      const response = await fetch('/b/cf/endpoints', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(endpointData)
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      // Reload endpoints list
      await CFEndpointManager.loadEndpoints();
      hideCFEndpointForm();

      // Show success notification
      if (window.showNotification) {
        window.showNotification('CF endpoint saved successfully', 'success');
      }
    } catch (error) {
      console.error('Failed to save CF endpoint:', error);
      alert('Failed to save CF endpoint: ' + error.message);
    }
  };

  // ================================================================
  // SERVICE INSTANCE CERTIFICATE FUNCTIONS
  // ================================================================

  // Fetch certificates from service instance manifest
  window.fetchManifestCertificates = async function (instanceId) {
    const resultsContainer = document.getElementById(`manifest-certificates-results-${instanceId}`);
    const listContainer = document.getElementById(`manifest-certificates-list-${instanceId}`);

    if (!resultsContainer || !listContainer) {
      console.error('Manifest certificates containers not found');
      return;
    }

    try {
      listContainer.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Scanning manifest for certificates...</div>';
      resultsContainer.style.display = 'block';

      const response = await fetch(`/b/certificates/services/${instanceId}`, {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json'
        }
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to fetch manifest certificates');
      }

      renderCertificateList(listContainer, data.data.certificates, 'manifest');

    } catch (error) {
      console.error('Failed to fetch manifest certificates:', error);
      listContainer.innerHTML = `<div class="cert-error">Failed to scan manifest certificates: ${error.message}</div>`;
    }
  };

  // Fetch trusted certificates from service instance VMs
  window.fetchServiceVMCertificates = async function (instanceId) {
    const resultsContainer = document.getElementById(`vm-certificates-results-${instanceId}`);
    const listContainer = document.getElementById(`vm-certificates-list-${instanceId}`);

    if (!resultsContainer || !listContainer) {
      console.error('VM certificates containers not found');
      return;
    }

    try {
      listContainer.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Listing certificate files on service VM...</div>';
      resultsContainer.style.display = 'block';

      // First, get the list of certificate files via SSH
      const response = await fetch(`/b/${instanceId}/certificates/trusted`, {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json'
        }
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to list VM certificate files');
      }

      // If no certificate files found
      if (!data.data.files || data.data.files.length === 0) {
        listContainer.innerHTML = '<div class="cert-error">No trusted certificate files found on the service VM</div>';
        return;
      }

      // Display the certificate files list for user selection
      renderCertificateFilesList(listContainer, data.data.files, instanceId);

    } catch (error) {
      console.error('Failed to fetch VM certificate files:', error);
      listContainer.innerHTML = `<div class="cert-error">Failed to list VM certificate files: ${error.message}</div>`;
    }
  };

  // Render certificate files list for selection
  const renderCertificateFilesList = (container, files, instanceId) => {
    const filesHtml = files.map(file => `
      <div class="cert-file-item" data-file-path="${file.path}">
        <div class="cert-file-header" onclick="toggleCertificateFile(this, '${instanceId}', '${file.path}')">
          <div class="cert-file-name">${file.name}</div>
          <div class="cert-file-path">${file.path}</div>
          <button class="load-cert-btn" onclick="loadCertificateFile(event, '${instanceId}', '${file.path}', '${file.name}')">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
              <polyline points="14,2 14,8 20,8"/>
            </svg>
            Load Certificate
          </button>
        </div>
        <div class="cert-file-content" style="display: none;">
          <div class="cert-loading-placeholder">Click "Load Certificate" to fetch certificate content...</div>
        </div>
      </div>
    `).join('');

    container.innerHTML = `
      <div class="cert-files-list">
        <div class="cert-files-header">
          <h6>Certificate Files (${files.length})</h6>
          <p>Select a certificate file to load its content and details</p>
        </div>
        ${filesHtml}
      </div>
    `;
  };

  // Toggle certificate file expansion
  window.toggleCertificateFile = function (header, instanceId, filePath) {
    const fileItem = header.closest('.cert-file-item');
    const content = fileItem.querySelector('.cert-file-content');

    if (content.style.display === 'none') {
      content.style.display = 'block';
    } else {
      content.style.display = 'none';
    }
  };

  // Load certificate file content via SSH
  window.loadCertificateFile = async function (event, instanceId, filePath, fileName) {
    event.stopPropagation(); // Prevent header click

    const button = event.target.closest('.load-cert-btn');
    const fileItem = button.closest('.cert-file-item');
    const contentDiv = fileItem.querySelector('.cert-file-content');

    try {
      // Show loading state
      button.innerHTML = `
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="spinning">
          <path d="M21 12a9 9 0 11-6.219-8.56"/>
        </svg>
        Loading...
      `;
      button.disabled = true;

      contentDiv.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Loading certificate content via SSH...</div>';
      contentDiv.style.display = 'block';

      // Fetch the certificate content via SSH
      const response = await fetch(`/b/${instanceId}/certificates/trusted/file`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          filePath: filePath
        })
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to load certificate file');
      }

      // Render the certificate details
      if (data.data.certificates && data.data.certificates.length > 0) {
        renderCertificateList(contentDiv, data.data.certificates, 'vm-trusted');
      } else {
        contentDiv.innerHTML = '<div class="cert-error">No certificate data found in file</div>';
      }

      // Update button state
      button.innerHTML = `
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <polyline points="20,6 9,17 4,12"/>
        </svg>
        Loaded
      `;

    } catch (error) {
      console.error('Failed to load certificate file:', error);
      contentDiv.innerHTML = `<div class="cert-error">Failed to load certificate: ${error.message}</div>`;

      // Reset button state
      button.innerHTML = `
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
          <polyline points="14,2 14,8 20,8"/>
        </svg>
        Load Certificate
      `;
    } finally {
      button.disabled = false;
    }
  };

  // Fetch certificate from service instance endpoint
  window.fetchServiceEndpointCertificate = async function (instanceId) {
    const endpointInput = document.getElementById(`service-endpoint-address-${instanceId}`);
    const resultsContainer = document.getElementById(`service-endpoint-results-${instanceId}`);
    const detailsContainer = document.getElementById(`service-endpoint-details-${instanceId}`);

    if (!endpointInput || !resultsContainer || !detailsContainer) {
      console.error('Service endpoint certificate containers not found');
      return;
    }

    const endpoint = endpointInput.value.trim();
    if (!endpoint) {
      alert('Please enter a service endpoint address');
      return;
    }

    try {
      detailsContainer.innerHTML = '<div class="cert-loading"><div class="cert-loading-spinner"></div>Connecting to service endpoint...</div>';
      resultsContainer.style.display = 'block';

      const response = await fetch('/b/certificates/endpoint', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          endpoint: endpoint,
          timeout: 30,
          serviceInstanceId: instanceId
        })
      });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const data = await response.json();

      if (!data.success) {
        throw new Error(data.error || 'Failed to fetch service endpoint certificate');
      }

      // For service endpoint certificates, we get a single certificate, not a list
      if (data.data && data.data.certificates && data.data.certificates.length > 0) {
        renderCertificateList(detailsContainer, data.data.certificates, 'service-endpoint');
      } else {
        detailsContainer.innerHTML = '<div class="cert-error">No certificate found for service endpoint</div>';
      }

    } catch (error) {
      console.error('Failed to fetch service endpoint certificate:', error);
      detailsContainer.innerHTML = `<div class="cert-error">Failed to connect to service endpoint: ${error.message}</div>`;
    }
  };

  // ================================================================
  // CERTIFICATE INSPECTOR MODAL FUNCTIONALITY
  // ================================================================

  // Modal Manager for handling certificate inspector and other modals
  const ModalManager = {
    currentModal: null,
    modalStack: [],

    // Open a modal with optional configuration
    openModal: function (modalElement, config = {}) {
      if (!modalElement) {
        console.error('ModalManager.openModal: modalElement is required');
        return false;
      }

      console.log('Opening modal:', modalElement);

      // Close existing modal if any
      if (this.currentModal) {
        this.closeModal(this.currentModal, false);
      }

      // Add to modal stack
      this.modalStack.push(modalElement);
      this.currentModal = modalElement;

      // Add modal to DOM if not already in the document
      if (!document.body.contains(modalElement)) {
        document.body.appendChild(modalElement);
        console.log('Modal added to DOM');
      }

      // Prevent body scroll
      document.body.style.overflow = 'hidden';

      // Force reflow to ensure CSS transitions work
      modalElement.offsetHeight;

      // Show modal with animation
      modalElement.classList.add('active');
      console.log('Modal active class added');

      // Focus management
      this.trapFocus(modalElement);

      // Add event listeners
      this.addModalListeners(modalElement);

      return true;
    },

    // Close a modal
    closeModal: function (modalElement, removeFromStack = true) {
      if (!modalElement) {
        modalElement = this.currentModal;
      }

      if (!modalElement) {
        return false;
      }

      // Remove from modal stack if requested
      if (removeFromStack) {
        const index = this.modalStack.indexOf(modalElement);
        if (index > -1) {
          this.modalStack.splice(index, 1);
        }
      }

      // Update current modal
      this.currentModal = this.modalStack.length > 0 ? this.modalStack[this.modalStack.length - 1] : null;

      // Remove active class to trigger close animation
      modalElement.classList.remove('active');

      // Remove modal from DOM after animation completes
      setTimeout(() => {
        if (modalElement.parentNode) {
          modalElement.parentNode.removeChild(modalElement);
        }
      }, 300); // Match CSS transition duration

      // Restore body scroll if no modals remaining
      if (this.modalStack.length === 0) {
        document.body.style.overflow = '';
      }

      // Remove event listeners
      this.removeModalListeners(modalElement);

      return true;
    },

    // Close all modals
    closeAllModals: function () {
      while (this.modalStack.length > 0) {
        this.closeModal(this.modalStack[this.modalStack.length - 1]);
      }
    },

    // Add event listeners to modal
    addModalListeners: function (modalElement) {
      // Close on overlay click
      modalElement.addEventListener('click', this.handleOverlayClick.bind(this));

      // Close on close button click
      const closeButton = modalElement.querySelector('.modal-close');
      if (closeButton) {
        closeButton.addEventListener('click', () => this.closeModal(modalElement));
      }

      // Keyboard navigation
      document.addEventListener('keydown', this.handleKeydown.bind(this));
    },

    // Remove event listeners from modal
    removeModalListeners: function (modalElement) {
      modalElement.removeEventListener('click', this.handleOverlayClick.bind(this));
      document.removeEventListener('keydown', this.handleKeydown.bind(this));
    },

    // Handle overlay click (close modal if clicked outside content)
    handleOverlayClick: function (event) {
      if (event.target.classList.contains('modal-overlay')) {
        this.closeModal(event.target);
      }
    },

    // Handle keyboard navigation
    handleKeydown: function (event) {
      if (!this.currentModal) return;

      switch (event.key) {
        case 'Escape':
          event.preventDefault();
          this.closeModal(this.currentModal);
          break;
        case 'Tab':
          this.handleTabKey(event);
          break;
      }
    },

    // Handle tab key for focus trapping
    handleTabKey: function (event) {
      if (!this.currentModal) return;

      const focusableElements = this.currentModal.querySelectorAll(
        'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])'
      );

      if (focusableElements.length === 0) return;

      const firstElement = focusableElements[0];
      const lastElement = focusableElements[focusableElements.length - 1];

      if (event.shiftKey) {
        // Shift + Tab
        if (document.activeElement === firstElement) {
          event.preventDefault();
          lastElement.focus();
        }
      } else {
        // Tab
        if (document.activeElement === lastElement) {
          event.preventDefault();
          firstElement.focus();
        }
      }
    },

    // Trap focus within modal
    trapFocus: function (modalElement) {
      const focusableElements = modalElement.querySelectorAll(
        'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])'
      );

      if (focusableElements.length > 0) {
        focusableElements[0].focus();
      }
    }
  };

  // Certificate Inspector Modal functionality
  const CertificateInspector = {
    // Create and show certificate inspector modal
    inspect: function (certificateData, options = {}) {
      if (!certificateData) {
        throw new Error('No certificate data provided');
      }

      console.log('CertificateInspector.inspect called with:', certificateData);

      // Validate that we have some certificate information to display
      if (!certificateData.subject && !certificateData.issuer && !certificateData.pemEncoded && !certificateData.textDetails) {
        console.warn('Certificate data missing expected fields:', certificateData);
        // Still try to create the modal, but with limited data
      }

      const modal = this.createModal(certificateData, options);
      console.log('Modal created:', modal);
      return ModalManager.openModal(modal);
    },

    // Create modal HTML structure
    createModal: function (certificateData, options) {
      const modalId = `cert-modal-${Date.now()}`;

      const modalHtml = `
        <div class="modal-overlay" id="${modalId}">
          <div class="modal">
            <div class="modal-header">
              <h2 class="modal-title">Certificate Inspector</h2>
              <button class="modal-close" aria-label="Close modal">
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                  <path d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                </svg>
              </button>
            </div>
            <div class="modal-body">
              <div class="cert-tabs-nav">
                <button class="cert-tab-btn active" data-tab="details" data-group="cert-tabs-${modalId}">Details</button>
                <button class="cert-tab-btn" data-tab="pem" data-group="cert-tabs-${modalId}">PEM</button>
                <button class="cert-tab-btn" data-tab="text" data-group="cert-tabs-${modalId}">Text</button>
              </div>
              <div class="cert-tab-content">
                <div class="cert-tab-pane active" data-tab="details" data-group="cert-tabs-${modalId}">
                  ${this.renderCertificateDetails(certificateData)}
                </div>
                <div class="cert-tab-pane" data-tab="pem" data-group="cert-tabs-${modalId}" style="display: none;">
                  ${this.renderCertificatePEM(certificateData)}
                </div>
                <div class="cert-tab-pane" data-tab="text" data-group="cert-tabs-${modalId}" style="display: none;">
                  ${this.renderCertificateText(certificateData)}
                </div>
              </div>
            </div>
          </div>
        </div>
      `;

      // Create modal element
      const modalElement = document.createElement('div');
      modalElement.innerHTML = modalHtml;
      const modal = modalElement.firstElementChild;

      // Add tab switching functionality
      this.setupTabSwitching(modal, modalId);

      return modal;
    },

    // Setup tab switching within the modal
    setupTabSwitching: function (modal, modalId) {
      const tabGroupId = `cert-tabs-${modalId}`;
      const tabs = modal.querySelectorAll(`.cert-tab-btn[data-group="${tabGroupId}"]`);
      const panes = modal.querySelectorAll(`.cert-tab-pane[data-group="${tabGroupId}"]`);

      tabs.forEach(tab => {
        tab.addEventListener('click', () => {
          const targetTab = tab.dataset.tab;

          // Update tab states
          tabs.forEach(t => t.classList.remove('active'));
          tab.classList.add('active');

          // Update pane states
          panes.forEach(pane => {
            pane.classList.remove('active');
            pane.style.display = 'none';
          });

          const targetPane = modal.querySelector(`.cert-tab-pane[data-tab="${targetTab}"][data-group="${tabGroupId}"]`);
          if (targetPane) {
            targetPane.classList.add('active');
            targetPane.style.display = 'block';
          }
        });
      });
    },

    // Render certificate details tab
    renderCertificateDetails: function (cert) {
      if (!cert) return '<div class="cert-error">No certificate data available</div>';

      // Check if we have minimal required data
      const hasBasicData = cert.subject || cert.issuer || cert.serialNumber || cert.version;
      if (!hasBasicData) {
        return `
          <div class="cert-error">
            <h4>Certificate Data Unavailable</h4>
            <p>The certificate data structure is not in the expected format.</p>
            <details>
              <summary>Raw Certificate Data</summary>
              <pre>${JSON.stringify(cert, null, 2)}</pre>
            </details>
          </div>
        `;
      }

      return `
        <table class="cert-details-table">
          <tr>
            <th>Version</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${cert.version || 'N/A'}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${cert.version || ''}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>Serial Number</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${cert.serialNumber || 'N/A'}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${cert.serialNumber || ''}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>Subject</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${this.formatSubject(cert.subject)}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${this.escapeQuotes(this.formatSubject(cert.subject))}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>Issuer</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${this.formatSubject(cert.issuer)}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${this.escapeQuotes(this.formatSubject(cert.issuer))}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>Valid From</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${this.formatDate(cert.notBefore)}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${this.formatDate(cert.notBefore)}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>Valid To</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${this.formatDate(cert.notAfter)}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${this.formatDate(cert.notAfter)}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          <tr>
            <th>Signature Algorithm</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${cert.signatureAlgorithm || 'N/A'}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${cert.signatureAlgorithm || ''}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          ${cert.subjectAltNames && cert.subjectAltNames.length > 0 ? `
          <tr>
            <th>Subject Alt Names</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text">${cert.subjectAltNames.join(', ')}</span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${cert.subjectAltNames.join(', ')}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          ` : ''}
          ${cert.fingerprints ? `
          <tr>
            <th>SHA256 Fingerprint</th>
            <td>
              <div class="cert-field-value">
                <span class="cert-field-text"><code>${cert.fingerprints.sha256 || 'N/A'}</code></span>
                <button class="cert-copy-btn" onclick="copyToClipboard('${cert.fingerprints.sha256 || ''}', this)">
                  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                    <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                  </svg>
                </button>
              </div>
            </td>
          </tr>
          ` : ''}
        </table>
      `;
    },

    // Render certificate PEM tab
    renderCertificatePEM: function (cert) {
      if (!cert || !cert.pemEncoded) {
        return '<div class="cert-error">No PEM data available</div>';
      }

      return `
        <div class="cert-pem-container">
          <div class="cert-pem-header">
            <h3 class="cert-pem-title">PEM Encoded Certificate</h3>
            <button class="cert-copy-btn" onclick="copyToClipboard('${this.escapeQuotes(cert.pemEncoded)}', this)">
              Copy PEM
              <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
              </svg>
            </button>
          </div>
          <div class="cert-pem-content">
            <pre class="cert-pem-text">${cert.pemEncoded}</pre>
          </div>
        </div>
      `;
    },

    // Render certificate text tab
    renderCertificateText: function (cert) {
      if (!cert || !cert.textDetails) {
        return '<div class="cert-error">No text details available</div>';
      }

      return `
        <div class="cert-text-container">
          <div class="cert-text-header">
            <h3 class="cert-text-title">OpenSSL Text Output</h3>
            <button class="cert-copy-btn" onclick="copyToClipboard('${this.escapeQuotes(cert.textDetails)}', this)">
              Copy Text
              <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
                <path d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
              </svg>
            </button>
          </div>
          <div class="cert-text-content">
            <pre class="cert-text-output">${cert.textDetails}</pre>
          </div>
        </div>
      `;
    },

    // Helper functions
    formatSubject: function (subject) {
      if (!subject) return 'N/A';
      if (typeof subject === 'string') return subject;

      const parts = [];
      if (subject.commonName) parts.push(`CN=${subject.commonName}`);
      if (subject.organization && subject.organization.length > 0) parts.push(`O=${subject.organization.join(', ')}`);
      if (subject.organizationalUnit && subject.organizationalUnit.length > 0) parts.push(`OU=${subject.organizationalUnit.join(', ')}`);
      if (subject.country && subject.country.length > 0) parts.push(`C=${subject.country.join(', ')}`);
      if (subject.locality && subject.locality.length > 0) parts.push(`L=${subject.locality.join(', ')}`);
      if (subject.province && subject.province.length > 0) parts.push(`ST=${subject.province.join(', ')}`);

      return parts.length > 0 ? parts.join(', ') : 'N/A';
    },

    formatDate: function (dateStr) {
      if (!dateStr) return 'N/A';
      try {
        const date = new Date(dateStr);
        return date.toLocaleString();
      } catch (e) {
        return dateStr;
      }
    },

    escapeQuotes: function (str) {
      if (!str) return '';
      return str.replace(/'/g, "\\'").replace(/"/g, '\\"');
    }
  };

  // CF Registration Management
  const CFRegistrationManager = {
    registrations: [],

    // Load all registrations
    async loadRegistrations() {
      try {
        const response = await fetch('/b/cf/registrations');
        const data = await response.json();

        if (data.registrations) {
          this.registrations = data.registrations;
          this.renderRegistrationsList();
        } else {
          console.error('Failed to load registrations:', data.error);
          this.showError('Failed to load registrations: ' + (data.error || 'Unknown error'));
        }
      } catch (error) {
        console.error('Error loading registrations:', error);
        this.showError('Failed to load registrations: ' + error.message);
      }
    },

    // Render registrations list
    renderRegistrationsList() {
      const container = document.querySelector('.registrations-content');
      if (!container) return;

      if (this.registrations.length === 0) {
        container.innerHTML = `
          <div class="registration-empty">
            <h3>No CF Registrations</h3>
            <p>No Cloud Foundry registrations have been created yet.</p>
            <button class="btn btn-primary" id="empty-new-registration-btn">Create First Registration</button>
          </div>
        `;

        // Add event listener to empty state button
        const emptyBtn = document.getElementById('empty-new-registration-btn');
        if (emptyBtn) {
          emptyBtn.addEventListener('click', () => this.showRegistrationModal());
        }
        return;
      }

      const html = `
        <div class="registrations-list">
          ${this.registrations.map(reg => this.renderRegistrationCard(reg)).join('')}
        </div>
      `;

      container.innerHTML = html;
      this.setupRegistrationHandlers();
    },

    // Render individual registration card
    renderRegistrationCard(registration) {
      const statusClass = (registration.status || 'created').toLowerCase();
      const createdDate = registration.created_at ? new Date(registration.created_at).toLocaleDateString() : 'Unknown';

      return `
        <div class="registration-card" data-registration-id="${registration.id}">
          <div class="registration-card-header">
            <h3 class="registration-name">${this.escapeHtml(registration.name || 'Unnamed')}</h3>
            <span class="registration-status ${statusClass}">${registration.status || 'created'}</span>
          </div>

          <div class="registration-details">
            <div class="registration-detail">
              <div class="registration-detail-label">API URL</div>
              <div class="registration-detail-value">${this.escapeHtml(registration.api_url || 'N/A')}</div>
            </div>
            <div class="registration-detail">
              <div class="registration-detail-label">Username</div>
              <div class="registration-detail-value">${this.escapeHtml(registration.username || 'N/A')}</div>
            </div>
            <div class="registration-detail">
              <div class="registration-detail-label">Broker Name</div>
              <div class="registration-detail-value">${this.escapeHtml(registration.broker_name || 'blacksmith')}</div>
            </div>
            <div class="registration-detail">
              <div class="registration-detail-label">Created</div>
              <div class="registration-detail-value">${createdDate}</div>
            </div>
          </div>

          <div class="registration-actions">
            <button class="btn btn-sm btn-primary sync-registration-btn" data-id="${registration.id}">Sync</button>
            <button class="btn btn-sm btn-success register-btn" data-id="${registration.id}"
              ${statusClass === 'registering' ? 'disabled' : ''}>
              ${statusClass === 'active' ? 'Re-register' : 'Register'}
            </button>
            <button class="btn btn-sm btn-info stream-btn" data-id="${registration.id}">View Progress</button>
            <button class="btn btn-sm btn-secondary edit-btn" data-id="${registration.id}">Edit</button>
            <button class="btn btn-sm btn-danger delete-btn" data-id="${registration.id}">Delete</button>
          </div>

          ${registration.last_error ? `
            <div class="registration-error" style="margin-top: 12px; padding: 8px; background: var(--error-bg); color: var(--error-text); border-radius: 4px; font-size: 12px;">
              <strong>Last Error:</strong> ${this.escapeHtml(registration.last_error)}
            </div>
          ` : ''}
        </div>
      `;
    },

    // Setup event handlers for registration cards
    setupRegistrationHandlers() {
      // Sync buttons
      document.querySelectorAll('.sync-registration-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const registrationId = e.target.dataset.id;
          this.syncRegistration(registrationId);
        });
      });

      // Register buttons
      document.querySelectorAll('.register-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const registrationId = e.target.dataset.id;
          this.startRegistration(registrationId);
        });
      });

      // Stream buttons
      document.querySelectorAll('.stream-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const registrationId = e.target.dataset.id;
          this.showProgressStream(registrationId);
        });
      });

      // Edit buttons
      document.querySelectorAll('.edit-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const registrationId = e.target.dataset.id;
          this.editRegistration(registrationId);
        });
      });

      // Delete buttons
      document.querySelectorAll('.delete-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const registrationId = e.target.dataset.id;
          this.deleteRegistration(registrationId);
        });
      });
    },

    // Show registration modal
    showRegistrationModal(registration = null) {
      const modal = document.getElementById('registration-modal');
      const form = document.getElementById('registration-form');
      const title = document.getElementById('registration-modal-title');

      if (!modal || !form) return;

      // Reset form and progress
      form.reset();
      document.getElementById('registration-progress').style.display = 'none';

      // Set title and populate form if editing
      if (registration) {
        title.textContent = 'Edit CF Registration';
        document.getElementById('reg-name').value = registration.name || '';
        document.getElementById('reg-api-url').value = registration.api_url || '';
        document.getElementById('reg-username').value = registration.username || '';
        document.getElementById('reg-broker-name').value = registration.broker_name || '';
        document.getElementById('reg-auto-register').checked = false; // Don't auto-register on edit
      } else {
        title.textContent = 'New CF Registration';
      }

      // Show modal
      modal.style.display = 'block';
      modal.setAttribute('aria-hidden', 'false');

      // Focus first input
      setTimeout(() => {
        document.getElementById('reg-name').focus();
      }, 100);
    },

    // Hide registration modal with proper cleanup
    hideRegistrationModal() {
      const modal = document.getElementById('registration-modal');
      if (modal) {
        modal.style.display = 'none';
        modal.setAttribute('aria-hidden', 'true');

        // Cleanup any active streaming connections
        if (this.cleanupStreaming) {
          this.cleanupStreaming();
        }

        // Reset modal content to default state
        const progressContainer = document.getElementById('registration-progress');
        if (progressContainer) {
          progressContainer.style.display = 'none';
        }

        const form = document.getElementById('registration-form');
        if (form) {
          form.reset();
          form.style.display = 'block';
        }
      }
    },

    // Test CF connection
    async testConnection() {
      const form = document.getElementById('registration-form');
      const formData = new FormData(form);

      const testData = {
        api_url: formData.get('api_url'),
        username: formData.get('username'),
        password: formData.get('password')
      };

      try {
        const response = await fetch('/b/cf/test-connection', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(testData)
        });

        const result = await response.json();

        if (result.success) {
          this.showSuccess('Connection test successful');
        } else {
          this.showError('Connection test failed: ' + (result.error || result.message));
        }
      } catch (error) {
        console.error('Connection test error:', error);
        this.showError('Connection test failed: ' + error.message);
      }
    },

    // Save registration
    async saveRegistration() {
      const form = document.getElementById('registration-form');
      const formData = new FormData(form);

      const registrationData = {
        name: formData.get('name'),
        api_url: formData.get('api_url'),
        username: formData.get('username'),
        password: formData.get('password'),
        broker_name: formData.get('broker_name') || 'blacksmith',
        auto_register: formData.get('auto_register') === 'on'
      };

      try {
        const response = await fetch('/b/cf/registrations', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(registrationData)
        });

        const result = await response.json();

        if (response.ok) {
          this.showSuccess('Registration created successfully');
          this.hideRegistrationModal();
          this.loadRegistrations();

          // If auto-register was enabled, show progress
          if (registrationData.auto_register) {
            setTimeout(() => {
              this.showProgressStream(result.id);
            }, 500);
          }
        } else {
          this.showError('Failed to create registration: ' + (result.error || 'Unknown error'));
        }
      } catch (error) {
        console.error('Save registration error:', error);
        this.showError('Failed to save registration: ' + error.message);
      }
    },

    // Start registration process
    async startRegistration(registrationId) {
      if (!registrationId) return;

      // Prompt for password
      const password = prompt('Enter CF password to start registration:');
      if (!password) return;

      try {
        const response = await fetch(`/b/cf/registrations/${registrationId}/register`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ password })
        });

        const result = await response.json();

        if (response.ok) {
          this.showSuccess('Registration process started');
          this.loadRegistrations(); // Refresh list
          this.showProgressStream(registrationId);
        } else {
          this.showError('Failed to start registration: ' + (result.error || 'Unknown error'));
        }
      } catch (error) {
        console.error('Start registration error:', error);
        this.showError('Failed to start registration: ' + error.message);
      }
    },

    // Sync registration with CF
    async syncRegistration(registrationId) {
      if (!registrationId) return;

      try {
        const response = await fetch(`/b/cf/registrations/${registrationId}/sync`, {
          method: 'POST'
        });

        const result = await response.json();

        if (response.ok && result.success) {
          this.showSuccess('Registration synced successfully');
          this.loadRegistrations(); // Refresh list
        } else {
          this.showError('Sync failed: ' + (result.error || result.message || 'Unknown error'));
        }
      } catch (error) {
        console.error('Sync registration error:', error);
        this.showError('Failed to sync registration: ' + error.message);
      }
    },

    // Show progress stream in modal
    showProgressStream(registrationId) {
      if (!registrationId) return;

      const modal = document.getElementById('registration-modal');
      const title = document.getElementById('registration-modal-title');
      const modalBody = modal.querySelector('.modal-body');
      const modalFooter = modal.querySelector('.modal-footer');

      // Update modal content
      title.textContent = 'Registration Progress';
      modalBody.innerHTML = `
        <div id="registration-progress" class="progress-container">
          <div class="progress-header">
            <h4>Registration Progress</h4>
          </div>
          <div class="progress-steps">
            <div class="progress-step" id="step-validating">
              <div class="step-icon"></div>
              <div class="step-content">
                <div class="step-title">Validating</div>
                <div class="step-message">Validating registration request</div>
              </div>
            </div>
            <div class="progress-step" id="step-connecting">
              <div class="step-icon"></div>
              <div class="step-content">
                <div class="step-title">Connecting</div>
                <div class="step-message">Connecting to Cloud Foundry</div>
              </div>
            </div>
            <div class="progress-step" id="step-authenticating">
              <div class="step-icon"></div>
              <div class="step-content">
                <div class="step-title">Authenticating</div>
                <div class="step-message">Authenticating with Cloud Foundry</div>
              </div>
            </div>
            <div class="progress-step" id="step-creating-broker">
              <div class="step-icon"></div>
              <div class="step-content">
                <div class="step-title">Creating Broker</div>
                <div class="step-message">Creating service broker</div>
              </div>
            </div>
            <div class="progress-step" id="step-enabling-services">
              <div class="step-icon"></div>
              <div class="step-content">
                <div class="step-title">Enabling Services</div>
                <div class="step-message">Enabling service access</div>
              </div>
            </div>
          </div>
        </div>
      `;

      modalFooter.innerHTML = `
        <button type="button" class="btn btn-secondary" onclick="CFRegistrationManager.hideRegistrationModal()">Close</button>
      `;

      // Show modal
      modal.style.display = 'block';
      modal.setAttribute('aria-hidden', 'false');

      // Start streaming progress
      this.streamProgress(registrationId);
    },

    // Stream progress updates
    streamProgress(registrationId) {
      let eventSource = null;
      let reconnectAttempts = 0;
      let maxReconnectAttempts = 3;
      let reconnectDelay = 2000; // Start with 2 seconds
      let pollingFallback = false;
      let pollingInterval = null;

      const connectEventSource = () => {
        try {
          eventSource = new EventSource(`/b/cf/registrations/${registrationId}/stream`);

          eventSource.onopen = () => {
            console.log('SSE connection opened for registration:', registrationId);
            reconnectAttempts = 0; // Reset on successful connection
            reconnectDelay = 2000; // Reset delay
          };

          eventSource.onmessage = (event) => {
            try {
              const progress = JSON.parse(event.data);
              this.updateProgressStep(progress);

              // Close stream when completed
              if (progress.step === 'completed' || progress.status === 'timeout') {
                this.cleanupStreaming();

                // Refresh registrations list after completion
                setTimeout(() => {
                  this.loadRegistrations();
                }, 1000);
              }
            } catch (error) {
              console.error('Error parsing progress event:', error);
              this.showError('Failed to parse progress update');
            }
          };

          eventSource.onerror = (error) => {
            console.error('EventSource error:', error);

            if (eventSource.readyState === EventSource.CLOSED) {
              // Connection closed, attempt reconnection
              if (reconnectAttempts < maxReconnectAttempts) {
                reconnectAttempts++;
                console.log(`Attempting SSE reconnection ${reconnectAttempts}/${maxReconnectAttempts}`);

                setTimeout(() => {
                  connectEventSource();
                }, reconnectDelay);

                // Exponential backoff
                reconnectDelay = Math.min(reconnectDelay * 2, 10000);
              } else {
                console.log('Max SSE reconnection attempts reached, falling back to polling');
                this.fallbackToPolling(registrationId);
              }
            }
          };

        } catch (error) {
          console.error('Failed to create EventSource:', error);
          this.fallbackToPolling(registrationId);
        }
      };

      // Cleanup function
      this.cleanupStreaming = () => {
        if (eventSource && eventSource.readyState !== EventSource.CLOSED) {
          eventSource.close();
          eventSource = null;
        }
        if (pollingInterval) {
          clearInterval(pollingInterval);
          pollingInterval = null;
        }
      };

      // Start SSE connection
      connectEventSource();

      // Overall timeout (2 minutes)
      setTimeout(() => {
        this.cleanupStreaming();
        this.showError('Progress monitoring timed out');
      }, 120000);
    },

    // Polling fallback when SSE fails
    fallbackToPolling(registrationId) {
      console.log('Starting polling fallback for registration:', registrationId);

      const pollProgress = async () => {
        try {
          const response = await fetch(`/b/cf/registrations/${registrationId}`);
          if (response.ok) {
            const registration = await response.json();

            // Create a progress event similar to SSE format
            const progress = {
              step: 'status_update',
              status: registration.status,
              message: `Status: ${registration.status}`,
              timestamp: new Date().toISOString()
            };

            if (registration.last_error) {
              progress.error = registration.last_error;
            }

            this.updateProgressStep(progress);

            // Stop polling if completed
            if (registration.status === 'active' || registration.status === 'failed') {
              if (pollingInterval) {
                clearInterval(pollingInterval);
                pollingInterval = null;
              }

              // Send completion event
              const completionProgress = {
                step: 'completed',
                status: registration.status,
                message: `Registration ${registration.status}`,
                timestamp: new Date().toISOString()
              };

              if (registration.status === 'failed' && registration.last_error) {
                completionProgress.error = registration.last_error;
              }

              this.updateProgressStep(completionProgress);

              // Refresh registrations list after completion
              setTimeout(() => {
                this.loadRegistrations();
              }, 1000);
            }
          } else {
            console.error('Failed to poll registration status:', response.status);
          }
        } catch (error) {
          console.error('Polling error:', error);
        }
      };

      // Poll every 3 seconds
      pollingInterval = setInterval(pollProgress, 3000);

      // Initial poll
      pollProgress();
    },

    // Enhanced progress step UI updates
    updateProgressStep(progress) {
      // Handle different progress step types
      if (progress.step === 'status' || progress.step === 'status_update') {
        this.updateOverallStatus(progress);
        return;
      }

      if (progress.step === 'completed') {
        this.handleProgressCompletion(progress);
        return;
      }

      if (progress.step === 'timeout') {
        this.handleProgressTimeout(progress);
        return;
      }

      // Handle specific registration steps
      const stepId = `step-${progress.step.replace('_', '-')}`;
      const stepElement = document.getElementById(stepId);

      if (stepElement) {
        const statusClass = progress.status.toLowerCase();

        // Remove existing status classes
        stepElement.classList.remove('running', 'success', 'error');

        // Add new status class
        if (statusClass === 'running' || statusClass === 'registering') {
          stepElement.classList.add('running');
        } else if (statusClass === 'success' || statusClass === 'active') {
          stepElement.classList.add('success');
        } else if (statusClass === 'error' || statusClass === 'failed') {
          stepElement.classList.add('error');
        }

        // Update message
        const messageElement = stepElement.querySelector('.step-message');
        if (messageElement) {
          messageElement.textContent = progress.message;

          // Add error details if available
          if (progress.error && statusClass === 'error') {
            messageElement.textContent += ` (${progress.error})`;
          }
        }
      }
    },

    // Update overall status display
    updateOverallStatus(progress) {
      const progressContainer = document.getElementById('registration-progress');
      if (!progressContainer) return;

      const header = progressContainer.querySelector('.progress-header h4');
      if (header) {
        header.textContent = `Registration Progress - ${progress.status.toUpperCase()}`;
      }

      // Add status indicator to progress container
      progressContainer.className = `progress-container status-${progress.status.toLowerCase()}`;

      // Update all steps to show current status
      const allSteps = progressContainer.querySelectorAll('.progress-step');
      allSteps.forEach(step => {
        step.classList.remove('running', 'success', 'error');

        if (progress.status === 'registering') {
          step.classList.add('running');
        } else if (progress.status === 'active') {
          step.classList.add('success');
        } else if (progress.status === 'failed') {
          step.classList.add('error');
        }
      });
    },

    // Handle progress completion
    handleProgressCompletion(progress) {
      const progressContainer = document.getElementById('registration-progress');
      if (!progressContainer) return;

      const header = progressContainer.querySelector('.progress-header h4');
      if (header) {
        const statusText = progress.status === 'active' ? 'COMPLETED' : 'FAILED';
        header.textContent = `Registration ${statusText}`;
      }

      // Update all steps to final status
      const allSteps = progressContainer.querySelectorAll('.progress-step');
      allSteps.forEach(step => {
        step.classList.remove('running');
        if (progress.status === 'active') {
          step.classList.add('success');
        } else {
          step.classList.add('error');
        }
      });

      // Show completion message
      if (progress.error) {
        this.showError(`Registration failed: ${progress.error}`);
      } else if (progress.status === 'active') {
        this.showSuccess('Registration completed successfully!');
      }
    },

    // Handle progress timeout
    handleProgressTimeout(progress) {
      const progressContainer = document.getElementById('registration-progress');
      if (!progressContainer) return;

      const header = progressContainer.querySelector('.progress-header h4');
      if (header) {
        header.textContent = 'Registration Progress - TIMEOUT';
      }

      this.showError('Registration progress monitoring timed out. Please check status manually.');
    },

    // Show success message
    showSuccess(message) {
      // Create or update success notification
      let notification = document.querySelector('.success-notification');
      if (!notification) {
        notification = document.createElement('div');
        notification.className = 'success-notification';
        notification.style.cssText = `
          position: fixed;
          top: 20px;
          right: 20px;
          background: var(--text-success);
          color: var(--bg-secondary);
          padding: 12px 20px;
          border-radius: 6px;
          z-index: 10000;
          font-weight: 500;
          box-shadow: var(--shadow-primary);
        `;
        document.body.appendChild(notification);
      }

      notification.textContent = message;
      notification.style.display = 'block';

      // Auto-hide after 5 seconds
      setTimeout(() => {
        if (notification && notification.parentNode) {
          notification.parentNode.removeChild(notification);
        }
      }, 5000);
    },

    // Delete registration
    async deleteRegistration(registrationId) {
      if (!registrationId) return;

      const registration = this.registrations.find(r => r.id === registrationId);
      const name = registration ? registration.name : 'this registration';

      if (!confirm(`Are you sure you want to delete ${name}?`)) {
        return;
      }

      try {
        const response = await fetch(`/b/cf/registrations/${registrationId}`, {
          method: 'DELETE'
        });

        const result = await response.json();

        if (response.ok) {
          this.showSuccess('Registration deleted successfully');
          this.loadRegistrations(); // Refresh list
        } else {
          this.showError('Failed to delete registration: ' + (result.error || 'Unknown error'));
        }
      } catch (error) {
        console.error('Delete registration error:', error);
        this.showError('Failed to delete registration: ' + error.message);
      }
    },

    // Edit registration (opens modal with existing data)
    editRegistration(registrationId) {
      const registration = this.registrations.find(r => r.id === registrationId);
      if (registration) {
        this.showRegistrationModal(registration);
      }
    },

    // Utility functions
    escapeHtml(text) {
      const div = document.createElement('div');
      div.textContent = text;
      return div.innerHTML;
    },

    showSuccess(message) {
      // Simple alert for now - could be enhanced with toast notifications
      alert('Success: ' + message);
    },

    showError(message) {
      // Simple alert for now - could be enhanced with toast notifications
      alert('Error: ' + message);
    }
  };

  // Initialize broker tab when it's selected
  const initBrokerTab = () => {
    // Setup broker tab switching (using new broker-tab-btn class)
    document.querySelectorAll('.broker-tab-btn').forEach(button => {
      button.addEventListener('click', (e) => {
        e.preventDefault();
        const tabName = button.dataset.cftab;
        if (tabName) {
          switchCFDetailTab(tabName);
        }
      });
    });

    // Load CF endpoints from configuration
    CFEndpointManager.loadEndpoints();
  };

  // Switch between CF detail tabs
  const switchCFDetailTab = (tabName) => {
    // Remove active class from all broker tab buttons
    document.querySelectorAll('.broker-tab-btn').forEach(btn => {
      btn.classList.remove('active');
    });

    // Hide all detail panels
    document.querySelectorAll('.cf-detail-panel').forEach(panel => {
      panel.classList.remove('active');
      panel.style.display = 'none';
    });

    // Add active class to target button and show target panel
    const targetButton = document.querySelector(`[data-cftab="${tabName}"]`);
    const targetPanel = document.querySelector(`[data-panel="${tabName}"]`);

    if (targetButton && targetPanel) {
      targetButton.classList.add('active');
      targetPanel.classList.add('active');
      targetPanel.style.display = 'block';

      // Load content for the selected tab if endpoint is connected
      const selectedEndpoint = CFEndpointManager.selectedEndpoint;
      if (selectedEndpoint && tabName !== 'details') {
        const connectionState = CFEndpointManager.connectionStates[selectedEndpoint] || { connected: false };
        if (connectionState.connected) {
          CFEndpointManager.loadTabContent(tabName, selectedEndpoint);
        } else {
          // Show message that connection is required
          targetPanel.innerHTML = `
            <div class="empty-state">
              <h3>Connection Required</h3>
              <p>Please connect to the ${selectedEndpoint} endpoint to view ${tabName} data.</p>
              <button class="btn btn-primary" onclick="CFEndpointManager.toggleConnection()">Connect Now</button>
            </div>
          `;
        }
      }
    }
  };

  // CF Endpoint Management
  const CFEndpointManager = {
    endpoints: {},
    selectedEndpoint: null,
    connectionStates: {}, // Persist connection status

    // Load CF endpoints from configuration
    async loadEndpoints() {
      // Load persistent connection states first
      this.loadConnectionStates();

      try {
        const response = await fetch('/b/cf/endpoints');
        const data = await response.json();

        if (data.endpoints) {
          this.endpoints = data.endpoints;
          this.renderEndpointsList();

          // Restore previously selected endpoint if it exists, otherwise auto-select first endpoint
          const endpointNames = Object.keys(this.endpoints);
          if (endpointNames.length > 0) {
            if (this.selectedEndpoint && endpointNames.includes(this.selectedEndpoint)) {
              // Restore previously selected endpoint
              this.selectEndpoint(this.selectedEndpoint);
            } else if (!this.selectedEndpoint) {
              // Auto-select first endpoint if none selected
              this.selectEndpoint(endpointNames[0]);
            }
          }
        } else {
          console.error('Failed to load CF endpoints:', data.error);
          this.showError('Failed to load CF endpoints: ' + (data.error || 'Unknown error'));
        }
      } catch (error) {
        console.error('Error loading CF endpoints:', error);
        this.showError('Failed to load CF endpoints: ' + error.message);
      }
    },

    // Refresh CF endpoints list
    async refreshEndpoints() {
      // Show loading in dropdown
      const dropdown = document.getElementById('cf-endpoint-select');
      if (dropdown) {
        dropdown.innerHTML = '<option value="">Refreshing endpoints...</option>';
      }

      // Reload endpoints from server
      await this.loadEndpoints();
    },

    // Render CF endpoints in the dropdown selector
    renderEndpointsList() {
      const dropdown = document.getElementById('cf-endpoint-select');
      if (!dropdown) return;

      const endpointNames = Object.keys(this.endpoints);

      if (endpointNames.length === 0) {
        dropdown.innerHTML = '<option value="">No CF endpoints configured</option>';
        return;
      }

      const options = ['<option value="">Select an endpoint...</option>'];
      endpointNames.forEach(name => {
        const endpoint = this.endpoints[name];
        const displayName = endpoint.name || name;
        const isSelected = this.selectedEndpoint === name ? 'selected' : '';
        options.push(`<option value="${name}" ${isSelected}>${displayName} (${endpoint.endpoint})</option>`);
      });

      dropdown.innerHTML = options.join('');

      // Setup change handler for dropdown
      dropdown.onchange = (event) => {
        const selectedEndpoint = event.target.value;
        if (selectedEndpoint) {
          this.selectEndpoint(selectedEndpoint);
        } else {
          this.clearSelection();
        }
      };
    },

    // Render a single endpoint item
    renderEndpointItem(endpointName) {
      const endpoint = this.endpoints[endpointName];
      const isSelected = this.selectedEndpoint === endpointName;

      return `
        <div class="endpoint-item ${isSelected ? 'selected' : ''}" data-endpoint="${endpointName}">
          <div class="endpoint-info">
            <h4>${endpoint.name || endpointName}</h4>
            <p class="endpoint-url">${endpoint.endpoint}</p>
            <small class="endpoint-user">User: ${endpoint.username}</small>
          </div>
        </div>
      `;
    },

    // Setup click handlers for endpoint selection
    setupEndpointClickHandlers() {
      document.querySelectorAll('.endpoint-item').forEach(item => {
        item.addEventListener('click', () => {
          const endpointName = item.dataset.endpoint;
          this.selectEndpoint(endpointName);
        });
      });
    },

    // Select an endpoint and load its details
    selectEndpoint(endpointName) {
      // Update selection state
      this.selectedEndpoint = endpointName;

      // Save the selection to localStorage
      this.saveConnectionStates();

      // Update dropdown selection
      const dropdown = document.getElementById('cf-endpoint-select');
      if (dropdown) {
        dropdown.value = endpointName;
      }

      // Load details for the selected endpoint
      this.loadEndpointDetails(endpointName);

      // Update connection status display
      this.updateConnectionStatus(endpointName);

      // Show/hide register button based on endpoint selection
      this.updateRegisterButtonVisibility(endpointName);

      // Auto-connect if not already connected
      const connectionState = this.connectionStates[endpointName] || { connected: false, testing: false };
      if (!connectionState.connected && !connectionState.testing) {
        this.toggleConnection();
      }

      // Reset to marketplace tab
      this.switchToBrokerTab('marketplace');
    },

    // Clear endpoint selection
    clearSelection() {
      this.selectedEndpoint = null;
      // Save the cleared selection to localStorage
      this.saveConnectionStates();

      // Hide broker details table
      const detailsTableContainer = document.getElementById('broker-details-table-container');
      if (detailsTableContainer) {
        detailsTableContainer.style.display = 'none';
      }

      // Clear all detail panels
      document.querySelectorAll('.cf-detail-panel').forEach(panel => {
        const contentDiv = panel.querySelector('div[id*="cf-"]');
        if (contentDiv) {
          contentDiv.innerHTML = `
            <div class="empty-state">
              <h3>Select an endpoint</h3>
              <p>Choose a Cloud Foundry endpoint from the dropdown above to view services and manage broker registration.</p>
            </div>
          `;
        }
      });

      // Reset to marketplace tab
      this.switchToBrokerTab('marketplace');

      // Hide register button
      this.updateRegisterButtonVisibility(null);
    },

    // Update register button visibility based on endpoint selection
    updateRegisterButtonVisibility(endpointName) {
      const registerBtn = document.getElementById('register-btn');
      if (registerBtn) {
        if (endpointName) {
          registerBtn.style.display = 'inline-block';
        } else {
          registerBtn.style.display = 'none';
        }
      }
    },

    // Show registration modal (delegate to CFRegistrationManager)
    async showRegistrationModal() {
      if (!this.selectedEndpoint) {
        alert('Please select a CF endpoint first.');
        return;
      }

      // First prefill the form with broker defaults
      await this.prefillRegistrationForm();

      // Then show the existing registration modal
      CFRegistrationManager.showRegistrationModal();
    },

    // Pre-fill registration form with broker defaults
    async prefillRegistrationForm() {
      try {
        // Get broker info from the blacksmith status
        const response = await fetch('/status');
        const data = await response.json();

        const endpoint = this.endpoints[this.selectedEndpoint];
        if (endpoint && data) {
          // Pre-fill name field
          const nameInput = document.getElementById('reg-name');
          if (nameInput) {
            nameInput.value = `blacksmith-${this.selectedEndpoint.toLowerCase()}`;
          }

          // Pre-fill API URL if we can extract it from the endpoint
          const apiInput = document.getElementById('reg-api-url');
          if (apiInput && endpoint.endpoint) {
            apiInput.value = endpoint.endpoint;
          }

          // Pre-fill broker name
          const brokerNameInput = document.getElementById('reg-broker-name');
          if (brokerNameInput) {
            brokerNameInput.value = 'blacksmith';
          }

          // Check auto-register by default
          const autoRegisterInput = document.getElementById('reg-auto-register');
          if (autoRegisterInput) {
            autoRegisterInput.checked = true;
          }
        }
      } catch (error) {
        console.error('Failed to prefill registration form:', error);
      }
    },

    // Switch to a specific broker tab
    switchToBrokerTab(tabName) {
      switchCFDetailTab(tabName);
    },

    // Load endpoint details
    loadEndpointDetails(endpointName) {
      const endpoint = this.endpoints[endpointName];
      const detailsTableContainer = document.getElementById('broker-details-table-container');
      const nameCell = document.getElementById('broker-detail-name');
      const endpointCell = document.getElementById('broker-detail-endpoint');
      const usernameCell = document.getElementById('broker-detail-username');

      if (!detailsTableContainer || !nameCell || !endpointCell || !usernameCell) return;

      // Show the details table
      detailsTableContainer.style.display = 'block';

      // Populate the table cells
      nameCell.textContent = endpoint.name || endpointName;
      endpointCell.innerHTML = `<a href="${endpoint.endpoint}" target="_blank">${endpoint.endpoint}</a>`;
      usernameCell.textContent = endpoint.username;
    },

    // Load content for specific tabs
    async loadTabContent(tabName, endpointName) {
      const panel = document.querySelector(`[data-panel="${tabName}"]`);
      if (!panel) return;

      // Show loading
      panel.innerHTML = '<div class="loading">Loading...</div>';

      try {
        switch (tabName) {
          case 'marketplace':
            await this.loadMarketplaceTab(panel, endpointName);
            break;
          case 'services':
            await this.loadServicesTab(panel, endpointName);
            break;
        }
      } catch (error) {
        panel.innerHTML = `<div class="error">Failed to load ${tabName}: ${error.message}</div>`;
      }
    },


    // Load marketplace tab
    async loadMarketplaceTab(panel, endpointName) {
      const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(endpointName)}/marketplace`);
      const data = await response.json();

      if (data.services) {
        const html = `
          <div class="marketplace-content">
            <div class="marketplace-services-table-container">
              <div class="logs-table-container">
                <table class="marketplace-services-table">
                  <thead>
                    <tr class="table-controls-row">
                      <th colspan="4" class="table-controls-header">
                        <div class="table-controls-container">
                          <div class="search-filter-container">
                            ${createSearchFilter('marketplace-services-table', 'Search marketplace...')}
                          </div>
                          <button class="copy-btn-logs" onclick="window.copyMarketplaceServicesTable(event)"
                                  title="Copy table data">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                          </button>
                          <button class="refresh-btn-logs" onclick="window.refreshMarketplaceServices('${endpointName}', event)"
                                  title="Refresh marketplace services">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
                          </button>
                        </div>
                      </th>
                    </tr>
                    <tr>
                      <th>Service</th>
                      <th>Description</th>
                      <th>Plans</th>
                      <th>Available</th>
                    </tr>
                  </thead>
                  <tbody>
                    ${data.services.map(service => `
                      <tr>
                        <td><strong>${service.name}</strong></td>
                        <td>${service.description || 'N/A'}</td>
                        <td>${service.plans.length} plan(s)</td>
                        <td>${service.available ? 'Yes' : 'No'}</td>
                      </tr>
                    `).join('')}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        `;
        panel.innerHTML = html;

        // Initialize table features
        attachSearchFilter('marketplace-services-table');
        initializeSorting('marketplace-services-table');
      } else {
        throw new Error(data.error || 'Failed to load marketplace services');
      }
    },

    // Load services tab
    async loadServicesTab(panel, endpointName) {
      panel.innerHTML = `
        <div class="services-content">
          <div class="org-space-selector">
            <table class="org-space-table">
              <tr>
                <td>Org:</td>
                <td>
                  <select id="org-selector">
                    <option value="">Loading organizations...</option>
                  </select>
                </td>
                <td>Space:</td>
                <td>
                  <select id="space-selector" disabled>
                    <option value="">Select organization first</option>
                  </select>
                </td>
                <td>
                  <button class="refresh-btn" onclick="window.CFEndpointManager && window.CFEndpointManager.refreshOrgsSpaces('${endpointName}')" title="Refresh organizations and spaces">
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                      <path d="M21.5 2v6h-6M2.5 22v-6h6M2 11.5a10 10 0 0 1 18.8-4.3M22 12.5a10 10 0 0 1-18.8 4.2"/>
                    </svg>
                  </button>
                </td>
              </tr>
            </table>
          </div>
          <div id="services-results">
            <p>Select an organization and space to view service instances.</p>
          </div>
        </div>
      `;

      // Load organizations
      this.loadOrganizations(endpointName);
    },


    // Test connection to a CF endpoint
    async testConnection(endpointName) {
      try {
        const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(endpointName)}/test`, {
          method: 'POST'
        });
        const result = await response.json();

        if (result.success) {
          alert(`Connection test successful!\n\nEndpoint: ${result.endpoint}\nDuration: ${result.duration_ms}ms\nCF Version: ${result.cf_info?.version || 'Unknown'}\nCF Name: ${result.cf_info?.name || 'Unknown'}`);
        } else {
          alert(`Connection test failed:\n\n${result.error}`);
        }
      } catch (error) {
        alert(`Connection test failed: ${error.message}`);
      }
    },

    // Load organizations for services tab
    async loadOrganizations(endpointName) {
      try {
        const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(endpointName)}/orgs`);
        const data = await response.json();

        const orgSelector = document.getElementById('org-selector');
        if (orgSelector && data.organizations) {
          orgSelector.innerHTML = '<option value="">Select organization...</option>' +
            data.organizations.map(org => `<option value="${org.guid}">${org.name}</option>`).join('');

          // Setup org selector change handler
          orgSelector.addEventListener('change', (e) => {
            const orgGuid = e.target.value;
            if (orgGuid) {
              this.loadSpaces(endpointName, orgGuid);
            } else {
              const spaceSelector = document.getElementById('space-selector');
              spaceSelector.innerHTML = '<option value="">Select organization first</option>';
              spaceSelector.disabled = true;
            }
          });

          // Auto-select if there's only one organization
          if (data.organizations.length === 1) {
            orgSelector.value = data.organizations[0].guid;
            orgSelector.dispatchEvent(new Event('change'));
          }
        }
      } catch (error) {
        console.error('Failed to load organizations:', error);
      }
    },

    // Load spaces for selected organization
    async loadSpaces(endpointName, orgGuid) {
      try {
        const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(endpointName)}/orgs/${orgGuid}/spaces`);
        const data = await response.json();

        const spaceSelector = document.getElementById('space-selector');
        if (spaceSelector && data.spaces) {
          spaceSelector.innerHTML = '<option value="">Select space...</option>' +
            data.spaces.map(space => `<option value="${space.guid}">${space.name}</option>`).join('');
          spaceSelector.disabled = false;

          // Setup space selector change handler
          spaceSelector.addEventListener('change', (e) => {
            const spaceGuid = e.target.value;
            if (spaceGuid) {
              this.loadServiceInstances(endpointName, orgGuid, spaceGuid);
            }
          });

          // Auto-select if there's only one space
          if (data.spaces.length === 1) {
            spaceSelector.value = data.spaces[0].guid;
            spaceSelector.dispatchEvent(new Event('change'));
          }
        }
      } catch (error) {
        console.error('Failed to load spaces:', error);
      }
    },

    // Refresh organizations and spaces while preserving selections
    async refreshOrgsSpaces(endpointName) {
      const orgSelector = document.getElementById('org-selector');
      const spaceSelector = document.getElementById('space-selector');
      const selectedOrg = orgSelector?.value;
      const selectedSpace = spaceSelector?.value;

      // Reload organizations
      await this.loadOrganizations(endpointName);

      // Restore selections if they still exist
      if (selectedOrg) {
        setTimeout(() => {
          const orgSelector = document.getElementById('org-selector');
          if (orgSelector && [...orgSelector.options].some(opt => opt.value === selectedOrg)) {
            orgSelector.value = selectedOrg;
            orgSelector.dispatchEvent(new Event('change'));

            if (selectedSpace) {
              setTimeout(() => {
                const spaceSelector = document.getElementById('space-selector');
                if (spaceSelector && [...spaceSelector.options].some(opt => opt.value === selectedSpace)) {
                  spaceSelector.value = selectedSpace;
                  spaceSelector.dispatchEvent(new Event('change'));
                }
              }, 100);
            }
          }
        }, 100);
      }
    },

    // Load service instances for selected space
    async loadServiceInstances(endpointName, orgGuid, spaceGuid) {
      try {
        const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(endpointName)}/orgs/${orgGuid}/spaces/${spaceGuid}/services`);
        const data = await response.json();

        const resultsContainer = document.getElementById('services-results');
        if (resultsContainer && data.services) {
          const html = `
            <div class="service-instances-table-container">
              <div class="logs-table-container">
                <table class="service-instances-table">
                  <thead>
                    <tr class="table-controls-row">
                      <th colspan="4" class="table-controls-header">
                        <div class="table-controls-container">
                          <div class="search-filter-container">
                            ${createSearchFilter('service-instances-table', 'Search services...')}
                          </div>
                          <button class="copy-btn-logs" onclick="window.copyServiceInstancesTable(event)"
                                  title="Copy table data">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Copy"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                          </button>
                          <button class="refresh-btn-logs" onclick="window.refreshServiceInstances('${endpointName}', '${orgGuid}', '${spaceGuid}', event)"
                                  title="Refresh service instances">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-label="Refresh"><polyline points="23 4 23 10 17 10"></polyline><polyline points="1 20 1 14 7 14"></polyline><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path></svg>
                          </button>
                        </div>
                      </th>
                    </tr>
                    <tr>
                      <th>Name</th>
                      <th>Type</th>
                      <th>Status</th>
                      <th>Bindings</th>
                    </tr>
                  </thead>
                  <tbody>
                    ${data.services.map(service => `
                      <tr>
                        <td><strong>${service.name}</strong></td>
                        <td>${service.type || 'N/A'}</td>
                        <td>${service.last_operation?.state || 'Unknown'}</td>
                        <td>
                          <span class="bindings-count" id="bindings-${service.guid}">Loading...</span>
                        </td>
                      </tr>
                    `).join('')}
                  </tbody>
                </table>
              </div>
            </div>
          `;
          resultsContainer.innerHTML = html;

          // Initialize table features
          attachSearchFilter('service-instances-table');
          initializeSorting('service-instances-table');

          // Load bindings for each service instance
          data.services.forEach(service => {
            this.loadServiceBindings(endpointName, orgGuid, spaceGuid, service.guid);
          });
        }
      } catch (error) {
        console.error('Failed to load service instances:', error);
        document.getElementById('services-results').innerHTML = `<div class="error">Failed to load services: ${error.message}</div>`;
      }
    },

    // Load bindings for a specific service instance
    async loadServiceBindings(endpointName, orgGuid, spaceGuid, serviceGuid) {
      try {
        const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(endpointName)}/orgs/${orgGuid}/spaces/${spaceGuid}/service_instances/${serviceGuid}/bindings`);
        const data = await response.json();

        const bindingsElement = document.getElementById(`bindings-${serviceGuid}`);
        if (bindingsElement) {
          const bindings = data.bindings || [];

          if (bindings.length > 0) {
            // Show binding names, preferring app names over GUIDs
            const bindingNames = bindings.map(binding => {
              if (binding.name) {
                return binding.name;
              } else if (binding.app_name) {
                return binding.app_name;
              } else if (binding.app_guid) {
                return `App:${binding.app_guid.substring(0, 8)}`;
              } else {
                return 'Service Binding';
              }
            });

            bindingsElement.innerHTML = bindingNames.join(', ');
            bindingsElement.title = `Service bindings: ${bindingNames.join(', ')}`;
          } else {
            bindingsElement.innerHTML = `<span class="text-muted">No Bindings</span>`;
            bindingsElement.title = 'No service bindings found';
          }
        }
      } catch (error) {
        console.error(`Failed to load bindings for service ${serviceGuid}:`, error);
        const bindingsElement = document.getElementById(`bindings-${serviceGuid}`);
        if (bindingsElement) {
          bindingsElement.innerHTML = `<span class="badge badge-warning">Error</span>`;
          bindingsElement.title = 'Failed to load binding information';
        }
      }
    },

    // Update connection status display
    updateConnectionStatus(endpointName) {
      const statusIndicator = document.getElementById('status-indicator');
      const connectionBtn = document.getElementById('connection-btn');

      if (!statusIndicator || !connectionBtn) return;

      const connectionState = this.connectionStates[endpointName] || { connected: false, testing: false };

      // Update status indicator
      statusIndicator.className = 'status-indicator';
      if (connectionState.testing) {
        statusIndicator.classList.add('status-connecting');
      } else if (connectionState.connected) {
        statusIndicator.classList.add('status-connected');
      } else {
        statusIndicator.classList.add('status-disconnected');
      }

      // Update button
      connectionBtn.textContent = connectionState.connected ? 'Disconnect' : 'Connect';
      connectionBtn.disabled = connectionState.testing;
    },

    // Toggle connection status
    async toggleConnection() {
      if (!this.selectedEndpoint) return;

      const currentState = this.connectionStates[this.selectedEndpoint] || { connected: false, testing: false };

      if (currentState.connected) {
        // Disconnect
        this.connectionStates[this.selectedEndpoint] = { connected: false, testing: false };
        this.updateConnectionStatus(this.selectedEndpoint);
        // Save state to localStorage
        this.saveConnectionStates();
      } else {
        // Connect - start testing
        this.connectionStates[this.selectedEndpoint] = { connected: false, testing: true };
        this.updateConnectionStatus(this.selectedEndpoint);

        try {
          const response = await fetch(`/b/cf/endpoints/${encodeURIComponent(this.selectedEndpoint)}/connect`, {
            method: 'POST'
          });
          const result = await response.json();

          this.connectionStates[this.selectedEndpoint] = {
            connected: response.ok && result.success,
            testing: false
          };
        } catch (error) {
          this.connectionStates[this.selectedEndpoint] = { connected: false, testing: false };
        }

        this.updateConnectionStatus(this.selectedEndpoint);
        // Save state to localStorage
        this.saveConnectionStates();

        // Refresh current tab if it was showing connection required message
        this.refreshCurrentTab();
      }
    },

    // Refresh current tab after connection state change
    refreshCurrentTab() {
      const activeTab = document.querySelector('.broker-tab-btn.active');
      if (activeTab) {
        const tabName = activeTab.dataset.cftab;
        if (tabName && tabName !== 'details') {
          // Re-trigger the tab switching to reload content
          switchCFDetailTab(tabName);
        }
      }
    },

    // Save connection states and selected endpoint to localStorage for persistence
    saveConnectionStates() {
      try {
        const persistentData = {
          connectionStates: this.connectionStates,
          selectedEndpoint: this.selectedEndpoint
        };
        localStorage.setItem('cf-endpoint-data', JSON.stringify(persistentData));
      } catch (error) {
        console.warn('Failed to save connection states:', error);
      }
    },

    // Load connection states and selected endpoint from localStorage
    loadConnectionStates() {
      try {
        const saved = localStorage.getItem('cf-endpoint-data');
        if (saved) {
          const persistentData = JSON.parse(saved);
          this.connectionStates = persistentData.connectionStates || {};
          this.selectedEndpoint = persistentData.selectedEndpoint || null;
        }
      } catch (error) {
        console.warn('Failed to load connection states:', error);
        this.connectionStates = {};
        this.selectedEndpoint = null;
      }
    },

    showError(message) {
      alert('Error: ' + message);
    }
  };

  // Sub-tab switching for broker tab
  const switchSubTab = (subtabId) => {
    // Remove active class from all sub-tab buttons and panels
    document.querySelectorAll('.sub-tab-button').forEach(btn => {
      btn.classList.remove('active');
      btn.setAttribute('aria-selected', 'false');
    });
    document.querySelectorAll('.sub-tab-panel').forEach(panel => {
      panel.classList.remove('active');
      panel.setAttribute('aria-hidden', 'true');
    });

    // Add active class to target button and panel
    const targetButton = document.querySelector(`[data-subtab="${subtabId}"]`);
    const targetPanel = document.getElementById(subtabId);

    if (targetButton && targetPanel) {
      targetButton.classList.add('active');
      targetButton.setAttribute('aria-selected', 'true');
      targetPanel.classList.add('active');
      targetPanel.setAttribute('aria-hidden', 'false');
    }
  };

  // Copy to clipboard functionality
  window.copyToClipboard = function (text, buttonElement) {
    if (!text) {
      console.warn('No text to copy');
      return;
    }

    navigator.clipboard.writeText(text).then(() => {
      // Visual feedback
      if (buttonElement) {
        const originalText = buttonElement.textContent;
        const originalClass = buttonElement.className;

        buttonElement.classList.add('copied');
        buttonElement.textContent = 'Copied!';

        setTimeout(() => {
          buttonElement.classList.remove('copied');
          buttonElement.className = originalClass;
          buttonElement.textContent = originalText;
        }, 1000);
      }
    }).catch(err => {
      console.error('Failed to copy to clipboard:', err);

      // Fallback for older browsers
      try {
        const textArea = document.createElement('textarea');
        textArea.value = text;
        textArea.style.position = 'fixed';
        textArea.style.opacity = '0';
        document.body.appendChild(textArea);
        textArea.select();
        document.execCommand('copy');
        document.body.removeChild(textArea);

        if (buttonElement) {
          buttonElement.classList.add('copied');
          setTimeout(() => buttonElement.classList.remove('copied'), 1000);
        }
      } catch (fallbackErr) {
        console.error('Clipboard fallback failed:', fallbackErr);
      }
    });
  };

  // Global functions for certificate inspection
  window.inspectCertificate = function (certificateData) {
    try {
      console.log('Opening certificate inspector with data:', certificateData);
      const result = CertificateInspector.inspect(certificateData);
      console.log('Certificate inspector result:', result);
      return result;
    } catch (error) {
      console.error('Error in CertificateInspector.inspect:', error, error.stack);
      alert('Failed to open certificate inspector: ' + error.message);
      return false;
    }
  };

  window.closeAllModals = function () {
    return ModalManager.closeAllModals();
  };

  // Make ModalManager and CertificateInspector available globally for debugging
  window.ModalManager = ModalManager;
  window.CertificateInspector = CertificateInspector;

  // Terminal Manager Implementation
  const TerminalManager = {
    sessions: new Map(),
    activeSession: null,
    maxSessions: 10,

    openWithContext(deploymentType, deploymentName, instanceId) {
      const sessionKey = `${deploymentType}-${deploymentName}-${instanceId}`;

      // Check if session already exists
      if (this.sessions.has(sessionKey)) {
        this.switchToSession(sessionKey);
        return;
      }

      // Check session limit
      if (this.sessions.size >= this.maxSessions) {
        alert(`Maximum number of terminal sessions (${this.maxSessions}) reached. Please close some sessions.`);
        return;
      }

      // Create new session
      this.createSession(sessionKey, {
        deploymentType,
        deploymentName,
        instanceId
      });
    },

    createSession(sessionKey, context) {
      // Show modal
      const modal = document.getElementById('terminal-modal');
      if (modal) {
        modal.style.display = 'flex';
        modal.classList.add('active');
        modal.setAttribute('aria-hidden', 'false');
      }

      // Create terminal instance
      const terminal = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: 'Menlo, Monaco, "Courier New", monospace',
        theme: {
          background: '#000000',
          foreground: '#d4d4d4',
          cursor: '#d4d4d4',
          selection: 'rgba(255, 255, 255, 0.3)',
          black: '#000000',
          red: '#cd3131',
          green: '#0dbc79',
          yellow: '#e5e510',
          blue: '#2472c8',
          magenta: '#bc3fbc',
          cyan: '#11a8cd',
          white: '#e5e5e5',
          brightBlack: '#666666',
          brightRed: '#f14c4c',
          brightGreen: '#23d18b',
          brightYellow: '#f5f543',
          brightBlue: '#3b8eea',
          brightMagenta: '#d670d6',
          brightCyan: '#29b8db',
          brightWhite: '#e5e5e5'
        }
      });

      // Create container for this terminal
      const terminalContainer = document.getElementById('terminal-container');
      const terminalDiv = document.createElement('div');
      terminalDiv.className = 'terminal-instance';
      terminalDiv.id = `terminal-${sessionKey}`;
      terminalContainer.appendChild(terminalDiv);

      // Open terminal in the div
      terminal.open(terminalDiv);

      // Focus the terminal after opening
      setTimeout(() => {
        terminal.focus();
      }, 100);

      // Fit terminal to container size
      const fitTerminal = () => {
        const containerWidth = terminalDiv.clientWidth;
        const containerHeight = terminalDiv.clientHeight;

        if (containerWidth > 0 && containerHeight > 0) {
          // Calculate cols and rows based on character size
          const charWidth = 9; // approximate character width
          const charHeight = 18; // approximate character height
          const cols = Math.floor(containerWidth / charWidth);
          const rows = Math.floor(containerHeight / charHeight);

          if (cols > 0 && rows > 0) {
            terminal.resize(cols, rows);
          }
        }
      };

      // Initial fit
      setTimeout(fitTerminal, 100);

      // Resize on window resize
      window.addEventListener('resize', fitTerminal);

      // Create tab
      this.createTab(sessionKey, context);

      // Create WebSocket connection with retry logic
      let retryCount = 0;
      const maxRetries = 3;
      let ws = null;
      let handshakeReceived = false;
      let sessionConnected = false;

      const connectWebSocket = () => {
        const wsUrl = this.buildWebSocketUrl(context);
        ws = new WebSocket(wsUrl);
        sessionConnected = false; // Reset connection state

        ws.onopen = () => {
          terminal.writeln(`Connecting to ${context.deploymentType === 'blacksmith' ? 'Blacksmith' : 'Service Instance'} ${context.deploymentName} :: ${context.instanceId}`);
          handshakeReceived = false; // Reset handshake flag
        };

        ws.onmessage = (event) => {
          const msg = JSON.parse(event.data);

          switch (msg.type) {
            case 'handshake':
              // Wait for handshake before sending start command
              handshakeReceived = true;
              // Don't show "Session Initialized" message to user - keep it clean

              // Small delay to ensure backend is ready
              setTimeout(() => {
                if (ws.readyState === WebSocket.OPEN) {
                  ws.send(JSON.stringify({
                    type: 'control',
                    meta: {
                      action: 'start',
                      width: terminal.cols,
                      height: terminal.rows,
                      term: 'xterm-256color'
                    },
                    timestamp: new Date().toISOString()
                  }));
                }
              }, 100);
              break;
            case 'output':
              terminal.write(msg.data);
              break;
            case 'error':
              // Check for specific "Session not connected" error
              if (msg.data.includes('Session not connected') && retryCount < maxRetries) {
                terminal.writeln(`\r\n[Connection Error] ${msg.data}`);
                terminal.writeln(`\r\n[Retrying connection... (${retryCount + 1}/${maxRetries})]`);
                retryCount++;

                // Close current connection and retry with exponential backoff
                ws.close();
                const delay = 1000 * Math.pow(2, retryCount - 1); // 1s, 2s, 4s
                setTimeout(connectWebSocket, delay);
                return;
              }
              // Only show error if it's not a session initialization issue
              if (!msg.data.includes('SSH session not started') && !msg.data.includes('Message handling error')) {
                terminal.writeln(`\r\n[ERROR] ${msg.data}`);
              }
              break;
            case 'status':
              if (msg.meta && msg.meta.status === 'connected') {
                sessionConnected = true; // Mark session as ready for input
                terminal.writeln('\r\n[Connected]\r\n');
                retryCount = 0; // Reset retry count on successful connection

                // Focus the terminal when connection is established
                setTimeout(() => {
                  terminal.focus();
                }, 100);
              }
              break;
          }
        };

        ws.onerror = (error) => {
          terminal.writeln(`\r\n[Connection Error] ${error.message || 'WebSocket connection failed'}`);
        };

        ws.onclose = (event) => {
          if (!handshakeReceived && retryCount < maxRetries && event.code !== 1000) {
            // Connection closed before handshake, likely a connection issue
            terminal.writeln(`\r\n[Connection closed unexpectedly - Retrying... (${retryCount + 1}/${maxRetries})]`);
            retryCount++;
            const delay = 1000 * Math.pow(2, retryCount - 1);
            setTimeout(connectWebSocket, delay);
          } else {
            terminal.writeln('\r\n[Disconnected]');
            if (retryCount >= maxRetries) {
              terminal.writeln('\r\n[Max retry attempts reached. Please try reconnecting manually.]');
            }
          }
        };

        return ws;
      };

      // Initial connection
      ws = connectWebSocket();

      // Handle terminal input - only send when session is fully connected
      terminal.onData((data) => {
        if (ws && ws.readyState === WebSocket.OPEN && sessionConnected) {
          ws.send(JSON.stringify({
            type: 'input',
            data: data,
            timestamp: new Date().toISOString()
          }));
        }
      });

      // Handle terminal resize - only send when session is fully connected
      terminal.onResize((size) => {
        if (ws && ws.readyState === WebSocket.OPEN && sessionConnected) {
          ws.send(JSON.stringify({
            type: 'control',
            meta: {
              action: 'resize',
              width: size.cols,
              height: size.rows
            },
            timestamp: new Date().toISOString()
          }));
        }
      });

      // Store session
      this.sessions.set(sessionKey, {
        terminal,
        websocket: ws,
        context,
        element: terminalDiv
      });

      // Make this the active session
      this.switchToSession(sessionKey);
    },

    buildWebSocketUrl(context) {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.host;

      if (context.deploymentType === 'blacksmith') {
        // For blacksmith VMs, we need to pass the specific instance and index
        // Parse instanceId (e.g., "blacksmith/1" -> instance="blacksmith", index=1)
        const instanceParts = context.instanceId.split('/');
        const instanceName = instanceParts[0] || 'blacksmith';
        const instanceIndex = instanceParts.length > 1 ? instanceParts[1] : '0';

        return `${protocol}//${host}/b/blacksmith/ssh/stream?instance=${encodeURIComponent(instanceName)}&index=${encodeURIComponent(instanceIndex)}`;
      } else {
        // For service instances
        return `${protocol}//${host}/b/${context.deploymentName}/ssh/stream`;
      }
    },

    createTab(sessionKey, context) {
      const tabsContainer = document.getElementById('terminal-tabs');
      const tab = document.createElement('button');
      tab.className = `terminal-tab px-4 py-2 bg-gray-100 dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-0 cursor-pointer hover:bg-gray-200 dark:hover:bg-gray-700 hover:text-gray-900 dark:hover:text-gray-100 transition-colors duration-200 flex items-center gap-2 whitespace-nowrap text-sm ${context.deploymentType === 'blacksmith' ? 'blacksmith-tab' : 'service-instance-tab'}`;
      tab.id = `tab-${sessionKey}`;
      tab.innerHTML = `
        <svg class="terminal-tab-icon w-4 h-4 flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <rect x="2" y="3" width="20" height="14" rx="2" ry="2"></rect>
          <line x1="8" y1="21" x2="16" y2="21"></line>
          <line x1="12" y1="17" x2="12" y2="21"></line>
        </svg>
        <span class="${context.deploymentType === 'blacksmith' ? 'blacksmith-prefix' : 'service-prefix'}">[${context.deploymentType === 'blacksmith' ? 'BK' : 'SI'}]</span>
        ${context.deploymentName}/${context.instanceId}
        <span class="tab-close" onclick="window.TerminalManager.closeSession('${sessionKey}', event)">&times;</span>
      `;
      tab.onclick = (e) => {
        if (!e.target.classList.contains('tab-close')) {
          this.switchToSession(sessionKey);
        }
      };
      tabsContainer.appendChild(tab);
    },

    switchToSession(sessionKey) {
      // Hide all terminals
      document.querySelectorAll('.terminal-instance').forEach(el => {
        el.classList.remove('active');
      });

      // Deactivate all tabs
      document.querySelectorAll('.terminal-tab').forEach(el => {
        el.classList.remove('active');
      });

      // Show selected terminal
      const session = this.sessions.get(sessionKey);
      if (session) {
        session.element.classList.add('active');
        document.getElementById(`tab-${sessionKey}`).classList.add('active');
        this.activeSession = sessionKey;

        // Focus the terminal after switching
        if (session.terminal) {
          setTimeout(() => {
            session.terminal.focus();
          }, 100);
        }
      }
    },

    closeSession(sessionKey, event) {
      if (event) {
        event.stopPropagation();
      }

      const session = this.sessions.get(sessionKey);
      if (session) {
        // Close WebSocket
        if (session.websocket) {
          session.websocket.close();
        }

        // Dispose terminal
        if (session.terminal) {
          session.terminal.dispose();
        }

        // Remove elements
        if (session.element) {
          session.element.remove();
        }
        const tab = document.getElementById(`tab-${sessionKey}`);
        if (tab) {
          tab.remove();
        }

        // Remove from sessions
        this.sessions.delete(sessionKey);

        // Switch to another session if this was active
        if (this.activeSession === sessionKey) {
          const remainingSessions = Array.from(this.sessions.keys());
          if (remainingSessions.length > 0) {
            this.switchToSession(remainingSessions[0]);
          } else {
            // No more sessions, hide modal
            const modal = document.getElementById('terminal-modal');
            if (modal) {
              modal.classList.remove('active');
              setTimeout(() => {
                modal.style.display = 'none';
              }, 300);
            }
          }
        }
      }
    },

    closeAll() {
      // Close all sessions
      Array.from(this.sessions.keys()).forEach(key => {
        this.closeSession(key);
      });

      // Hide modal
      const modal = document.getElementById('terminal-modal');
      if (modal) {
        modal.classList.remove('active');
        modal.setAttribute('aria-hidden', 'true');
        setTimeout(() => {
          modal.style.display = 'none';
        }, 300);
      }

      // Hide minimized bar
      this.hideMinimizedBar();
    },

    minimizeAll() {
      // Hide the main terminal modal
      const modal = document.getElementById('terminal-modal');
      if (modal) {
        modal.classList.remove('active');
        setTimeout(() => {
          modal.style.display = 'none';
        }, 300);
      }

      // Show minimized bar with all sessions
      this.showMinimizedBar();
    },

    restoreAll() {
      // Show the main terminal modal
      const modal = document.getElementById('terminal-modal');
      if (modal && this.sessions.size > 0) {
        modal.style.display = 'flex';
        modal.setAttribute('aria-hidden', 'false');
        setTimeout(() => {
          modal.classList.add('active');
        }, 10);
      }

      // Hide minimized bar
      this.hideMinimizedBar();
    },

    showMinimizedBar() {
      const minimizedBar = document.getElementById('minimized-terminal-bar');
      const minimizedTabs = document.getElementById('minimized-terminal-tabs');

      if (minimizedBar && minimizedTabs) {
        // Add body class for layout adjustment
        document.body.classList.add('terminal-minimized');

        // Clear existing tabs
        minimizedTabs.innerHTML = '';

        // Create minimized tabs for each session
        this.sessions.forEach((session, sessionKey) => {
          const tab = document.createElement('div');
          tab.className = `minimized-terminal-tab bg-gray-100 dark:bg-gray-700 border border-gray-200 dark:border-gray-600 rounded px-3 py-1 text-xs cursor-pointer transition-colors duration-200 flex items-center gap-2 hover:bg-gray-200 dark:hover:bg-gray-600 ${sessionKey === this.activeSession ? 'active bg-white dark:bg-gray-800 border-gray-300 dark:border-gray-500' : ''}`;
          tab.innerHTML = `
            <svg class="minimized-terminal-icon w-3.5 h-3.5 flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <rect x="2" y="3" width="20" height="14" rx="2" ry="2"></rect>
              <line x1="8" y1="21" x2="16" y2="21"></line>
              <line x1="12" y1="17" x2="12" y2="21"></line>
            </svg>
            <span class="${session.context.deploymentType === 'blacksmith' ? 'blacksmith-prefix' : 'service-prefix'}">[${session.context.deploymentType === 'blacksmith' ? 'BK' : 'SI'}]</span>
            ${session.context.deploymentName}/${session.context.instanceId}
            <span class="tab-close" onclick="window.TerminalManager.closeSession('${sessionKey}', event)">&times;</span>
          `;

          // Click to activate session and restore
          tab.onclick = (e) => {
            if (!e.target.classList.contains('tab-close')) {
              this.switchToSession(sessionKey);
              this.restoreAll();
            }
          };

          minimizedTabs.appendChild(tab);
        });

        // Show the bar
        minimizedBar.classList.add('active');
      }
    },

    hideMinimizedBar() {
      const minimizedBar = document.getElementById('minimized-terminal-bar');
      if (minimizedBar) {
        minimizedBar.classList.remove('active');
        document.body.classList.remove('terminal-minimized');
      }
    },

    reconnectActive() {
      if (!this.activeSession) return;

      const session = this.sessions.get(this.activeSession);
      if (!session) return;

      // Close existing websocket
      if (session.websocket) {
        session.websocket.close();
      }

      // Clear terminal and show reconnecting message
      session.terminal.clear();
      session.terminal.writeln('Reconnecting...');

      // Create new WebSocket connection
      const wsUrl = this.buildWebSocketUrl(session.context);
      const ws = new WebSocket(wsUrl);

      ws.onopen = () => {
        session.terminal.writeln(`Reconnecting to ${session.context.deploymentType === 'blacksmith' ? 'Blacksmith' : 'Service Instance'} ${session.context.deploymentName} :: ${session.context.instanceId}`);

        // Send start control message
        ws.send(JSON.stringify({
          type: 'control',
          meta: {
            action: 'start',
            width: session.terminal.cols,
            height: session.terminal.rows,
            term: 'xterm-256color'
          },
          timestamp: new Date().toISOString()
        }));
      };

      ws.onmessage = (event) => {
        const msg = JSON.parse(event.data);

        switch (msg.type) {
          case 'output':
            session.terminal.write(msg.data);
            break;
          case 'error':
            session.terminal.writeln(`\r\n[ERROR] ${msg.data}`);
            break;
          case 'status':
            if (msg.meta && msg.meta.status === 'connected') {
              session.terminal.writeln('\r\n[Reconnected]\r\n');
            }
            break;
        }
      };

      ws.onerror = (error) => {
        session.terminal.writeln(`\r\n[Reconnection Error] ${error.message || 'WebSocket connection failed'}`);
      };

      ws.onclose = () => {
        session.terminal.writeln('\r\n[Disconnected]');
      };

      // Handle terminal input
      session.terminal.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'input',
            data: data,
            timestamp: new Date().toISOString()
          }));
        }
      });

      // Handle terminal resize
      session.terminal.onResize((size) => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'control',
            meta: {
              action: 'resize',
              width: size.cols,
              height: size.rows
            },
            timestamp: new Date().toISOString()
          }));
        }
      });

      // Update session with new websocket
      session.websocket = ws;
    }
  };

  // Global function for SSH button onclick
  window.openTerminal = function (deploymentType, deploymentName, instanceId, event) {
    if (event) {
      event.preventDefault();
      event.stopPropagation();
    }
    TerminalManager.openWithContext(deploymentType, deploymentName, instanceId);
  };

  // Make TerminalManager available globally
  window.TerminalManager = TerminalManager;

  // Toggle resurrection for a service instance or blacksmith deployment
  window.toggleResurrection = async function (instanceId, event) {
    if (event) {
      event.preventDefault();
      event.stopPropagation();
    }

    const slider = document.getElementById(`resurrection-slider-${instanceId}`);
    if (!slider) {
      console.error('Resurrection toggle slider not found for:', instanceId);
      return;
    }

    // Determine current state based on toggle appearance
    const isCurrentlyEnabled = slider.classList.contains('toggle-on');
    const newState = !isCurrentlyEnabled;

    // Show loading state
    slider.classList.add('toggle-loading');

    try {
      const response = await fetch(`/b/${instanceId}/resurrection`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          enabled: newState
        }),
        cache: 'no-cache'
      });

      const result = await response.json();

      if (response.ok && result.success) {
        // Update toggle state
        if (newState) {
          slider.classList.add('toggle-on');
          slider.classList.remove('toggle-off');
        } else {
          slider.classList.add('toggle-off');
          slider.classList.remove('toggle-on');
        }

        // Show success message
        showNotification(`Resurrection ${newState ? 'enabled' : 'disabled'} successfully`, 'success');

        // Refresh VMs view to show updated resurrection status
        // No delay needed since we check config directly instead of waiting for VM field updates
        if (instanceId === 'blacksmith') {
          if (window.refreshBlacksmithVMs) {
            window.refreshBlacksmithVMs(event);
          }
        } else {
          if (window.refreshServiceInstanceVMs) {
            window.refreshServiceInstanceVMs(instanceId, event);
          }
        }
      } else {
        throw new Error(result.error || 'Failed to toggle resurrection');
      }
    } catch (error) {
      console.error('Error toggling resurrection:', error);
      showNotification(`Failed to toggle resurrection: ${error.message}`, 'error');
    } finally {
      // Remove loading state
      slider.classList.remove('toggle-loading');
    }
  };

  // Delete resurrection config for a service instance or blacksmith deployment
  window.deleteResurrectionConfig = async function (instanceId, event) {
    if (event) {
      event.preventDefault();
      event.stopPropagation();
    }

    const deleteBtn = document.getElementById(`delete-resurrection-btn-${instanceId}`);
    if (!deleteBtn) {
      console.error('Delete resurrection button not found for:', instanceId);
      return;
    }

    // Confirm deletion
    if (!confirm('Are you sure you want to delete the resurrection config? This will revert to BOSH default resurrection behavior.')) {
      return;
    }

    try {
      // Add loading state
      deleteBtn.disabled = true;
      deleteBtn.classList.add('loading');

      const response = await fetch(`/b/${instanceId}/resurrection`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json'
        }
      });

      const result = await response.json();

      if (response.ok && result.success) {
        showNotification(result.message || 'Resurrection config deleted successfully', 'success');

        // Hide the delete button since config no longer exists
        deleteBtn.style.display = 'none';

        // Refresh VMs view to show updated resurrection status
        if (window.refreshServiceInstanceVMs) {
          window.refreshServiceInstanceVMs(instanceId, event);
        } else if (window.loadVMsView) {
          window.loadVMsView();
        }
      } else {
        throw new Error(result.error || 'Failed to delete resurrection config');
      }
    } catch (error) {
      console.error('Error deleting resurrection config:', error);
      showNotification(`Failed to delete resurrection config: ${error.message}`, 'error');
    } finally {
      // Remove loading state
      deleteBtn.disabled = false;
      deleteBtn.classList.remove('loading');
    }
  };

  // Initialize resurrection toggle state based on VM data
  window.initializeResurrectionToggle = function (instanceId, vms) {
    const slider = document.getElementById(`resurrection-slider-${instanceId}`);
    if (!slider || !vms || !vms.length) {
      console.log('initializeResurrectionToggle: early return', { slider: !!slider, vms: vms?.length });
      return;
    }

    // Debug: Log resurrection status for each VM
    console.log('initializeResurrectionToggle: VM data', vms.map(vm => ({
      id: vm.id,
      resurrection_paused: vm.resurrection_paused,
      resurrection_config_exists: vm.resurrection_config_exists
    })));

    // Check if any VM has resurrection paused
    const anyPaused = vms.some(vm => vm.resurrection_paused);
    const allPaused = vms.every(vm => vm.resurrection_paused);

    // Check if resurrection config exists (should be same for all VMs in a deployment)
    const resurrectionConfigExists = vms.some(vm => vm.resurrection_config_exists);

    // Show/hide delete button based on config existence
    const deleteBtn = document.getElementById(`delete-resurrection-btn-${instanceId}`);
    if (deleteBtn) {
      deleteBtn.style.display = resurrectionConfigExists ? 'inline-flex' : 'none';
    }

    console.log('initializeResurrectionToggle: analysis', {
      instanceId,
      anyPaused,
      allPaused,
      resurrectionConfigExists,
      vmCount: vms.length
    });

    if (allPaused) {
      // All VMs have resurrection paused - toggle should be OFF
      slider.classList.add('toggle-off');
      slider.classList.remove('toggle-on');
      console.log('initializeResurrectionToggle: set to OFF (all paused)');
    } else if (!anyPaused) {
      // No VMs have resurrection paused - toggle should be ON
      slider.classList.add('toggle-on');
      slider.classList.remove('toggle-off');
      console.log('initializeResurrectionToggle: set to ON (none paused)');
    } else {
      // Mixed state - default to OFF for safety
      slider.classList.add('toggle-off');
      slider.classList.remove('toggle-on');
      console.log('initializeResurrectionToggle: set to OFF (mixed state)');
    }
  };

  // Helper function to show notifications
  function showNotification(message, type = 'info') {
    // Create notification element if it doesn't exist
    let notification = document.querySelector('.notification');
    if (!notification) {
      notification = document.createElement('div');
      notification.className = 'notification';
      document.body.appendChild(notification);
    }

    // Set message and type
    notification.textContent = message;
    notification.className = `notification notification-${type} notification-show`;

    // Auto-hide after 3 seconds
    setTimeout(() => {
      notification.classList.remove('notification-show');
    }, 3000);
  }

  // Copy service instances table to clipboard
  window.copyServiceInstancesTable = async (event) => {
    const button = event.currentTarget;
    const table = document.querySelector('.service-instances-table');
    if (!table) return;

    try {
      const rows = table.querySelectorAll('tbody tr:not([style*="display: none"])');
      const headers = ['Name', 'Type', 'Status', 'Bindings'];
      let text = headers.join('\t') + '\n';

      rows.forEach(row => {
        const cells = row.querySelectorAll('td');
        const rowData = Array.from(cells).map(cell => cell.textContent.trim());
        text += rowData.join('\t') + '\n';
      });

      await copyToClipboard(text, button);
    } catch (error) {
      console.error('Failed to copy service instances table:', error);
    }
  };

  // Copy marketplace services table to clipboard
  window.copyMarketplaceServicesTable = async (event) => {
    const button = event.currentTarget;
    const table = document.querySelector('.marketplace-services-table');
    if (!table) return;

    try {
      const rows = table.querySelectorAll('tbody tr:not([style*="display: none"])');
      const headers = ['Service', 'Description', 'Plans', 'Available'];
      let text = headers.join('\t') + '\n';

      rows.forEach(row => {
        const cells = row.querySelectorAll('td');
        const rowData = Array.from(cells).map(cell => cell.textContent.trim());
        text += rowData.join('\t') + '\n';
      });

      await copyToClipboard(text, button);
    } catch (error) {
      console.error('Failed to copy marketplace services table:', error);
    }
  };

  // Refresh service instances
  window.refreshServiceInstances = async (endpointName, orgGuid, spaceGuid, event) => {
    const button = event.currentTarget;
    try {
      // Visual feedback
      button.classList.add('refreshing');
      const spanElement = button.querySelector('span');
      const originalText = spanElement ? spanElement.textContent : '';
      if (spanElement) {
        spanElement.textContent = 'Refreshing...';
      }

      await CFEndpointManager.loadServiceInstances(endpointName, orgGuid, spaceGuid);
    } catch (error) {
      console.error('Failed to refresh service instances:', error);
    } finally {
      button.classList.remove('refreshing');
      const spanElement = button.querySelector('span');
      if (spanElement) {
        spanElement.textContent = 'Refresh';
      }
    }
  };

  // Refresh marketplace services
  window.refreshMarketplaceServices = async (endpointName, event) => {
    const button = event.currentTarget;
    try {
      // Visual feedback
      button.classList.add('refreshing');
      const spanElement = button.querySelector('span');
      const originalText = spanElement ? spanElement.textContent : '';
      if (spanElement) {
        spanElement.textContent = 'Refreshing...';
      }

      // Find the marketplace panel and reload it
      const brokerTabContent = document.querySelector('.broker-tab-content[data-cftab="marketplace"]');
      if (brokerTabContent) {
        await CFEndpointManager.loadMarketplaceTab(brokerTabContent, endpointName);
      }
    } catch (error) {
      console.error('Failed to refresh marketplace services:', error);
    } finally {
      button.classList.remove('refreshing');
      const spanElement = button.querySelector('span');
      if (spanElement) {
        spanElement.textContent = 'Refresh';
      }
    }
  };

  // Make CF endpoint management functions available globally
  window.CFEndpointManager = CFEndpointManager;
  window.showCFEndpointForm = showCFEndpointForm;
  window.hideCFEndpointForm = hideCFEndpointForm;
  window.saveCFEndpoint = saveCFEndpoint;

})(document, window);
