(function ($,document,window,undefined) {
  $.empty = function (o) {
    // null and undefined are "empty"
    if (o == null) return true;

    // Assume if it has a length property with a non-zero value
    // that that property is correct.
    if (o.length > 0)    return false;
    if (o.length === 0)  return true;

    // If it isn't an object at this point
    // it is empty, but it can't be anything *but* empty
    // Is it empty?  Depends on your application.
    if (typeof o !== "object") return true;

    // Otherwise, does it have any properties of its own?
    // Note that this doesn't handle
    // toString and valueOf enumeration bugs in IE < 9
    for (var key in o) {
      if (hasOwnProperty.call(o, key)) return false;
    }

    return true;
  };

  // Set up jQuery AJAX to always send the Broker API version header
  $.ajaxSetup({
    beforeSend: function(xhr, settings) {
      // Add the broker API version header to all requests to /v2/* endpoints
      if (settings.url && settings.url.indexOf('/v2/') !== -1) {
        xhr.setRequestHeader('X-Broker-API-Version', '2.16');
        console.log('Setting X-Broker-API-Version header for', settings.url);
      }
    },
    cache: false  // Prevent caching
  });

  $(function () {
    console.log('Blacksmith UI initializing...');
    
    // First, try to fetch the catalog
    $.ajax({
      type: 'GET',
      url:  '/v2/catalog',
      success: function (catalog) {
        console.log('Catalog response:', catalog);
        
        // Check if catalog has services
        if (!catalog || !catalog.services || catalog.services.length === 0) {
          console.warn('No services found in catalog');
          $('#plans .content').html('<div class="no-data">No services configured. Please ensure service directories are provided when starting blacksmith.</div>');
        }
        
        // Then fetch the status
        $.ajax({
          type: 'GET',
          url:  '/b/status',
          success: function (data) {
            console.log('Status response:', data);
            console.log('Plans from status:', data.plans);
            
            // Initialize instances count
            var instances = {};
            if (data.instances && typeof data.instances === 'object') {
              $.each(data.instances, function (_, instance) {
                if (instance && instance.plan_id) {
                  instances[instance.plan_id] = (instances[instance.plan_id] || 0) + 1;
                }
              });
            }

            // Build plan mapping and add blacksmith data to catalog
            var plans = {};
            if (catalog.services && catalog.services.length > 0) {
              $.each(catalog.services, function (i, service) {
                if (service && service.plans) {
                  $.each(service.plans, function (j, plan) {
                    if (plan && plan.id) {
                      var key = service.id + '/' + plan.id;
                      plans[plan.id] = key;
                      console.log("Processing service ["+service.id+"] plan ["+plan.id+"] (as '"+plan.name+"') using key {"+key+"}");

                      // Add blacksmith-specific data with proper error handling
                      var planData = {
                        instances: instances[plan.id] || 0,
                        limit: 0
                      };
                      
                      if (data.plans && typeof data.plans === 'object' && data.plans[key]) {
                        planData.limit = data.plans[key].limit || 0;
                        console.log("Found plan data for key " + key + ", limit: " + planData.limit);
                      } else {
                        console.warn("Plan not found in status data for key: " + key);
                      }
                      
                      catalog.services[i].plans[j].blacksmith = planData;
                    }
                  });
                }
              });
            }

            // Process instances and attach plan data
            if (data.instances && typeof data.instances === 'object') {
              $.each(data.instances, function (i, instance) {
                if (instance && instance.plan_id && plans[instance.plan_id]) {
                  var planKey = plans[instance.plan_id];
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
            $('#ident').html(data.env || 'Unknown Environment');
            
            // Render plans if we have services
            if (catalog.services && catalog.services.length > 0) {
              $('#plans .content').html($.template('plans', catalog));
            } else {
              $('#plans .content').html('<div class="no-data">No services configured</div>');
            }
            
            // Render services
            if (data.instances && Object.keys(data.instances).length > 0) {
              $('#services .content').html($.template('services', data.instances));
            } else {
              $('#services .content').html('<div class="no-data">No service instances provisioned yet</div>');
            }
            
            // Render log
            if (data.log) {
              $('#log code').html(data.log.replace(/</g, '&lt;')
                                          .replace(/>/g, '&gt;')
                                          .replace(/^([\d-]+\s\S+)/mg, '<span class="d">$1</span>')
                                          .replace(/\[([^\]]+)\]/g, '[<span class="i">$1</span>]')
                                          .replace(/ (ERROR|INFO|DEBUG) /g, ' <span class="l $1">$1</span> '));
            } else {
              $('#log code').html('No log entries available');
            }
          },
          error: function(xhr, status, error) {
            console.error('Error fetching /b/status:', status, error, xhr.responseText);
            $('#plans .content').html('<div class="error">Failed to load status data: ' + error + '</div>');
            $('#services .content').html('<div class="error">Failed to load status data: ' + error + '</div>');
            $('#log code').html('Failed to load log data: ' + error);
          }
        });
      },
      error: function(xhr, status, error) {
        console.error('Error fetching /v2/catalog:', status, error, xhr.responseText);
        if (xhr.status === 401) {
          $('#plans .content').html('<div class="error">Authentication required. Please check credentials.</div>');
          $('#services .content').html('<div class="error">Authentication required</div>');
          $('#log code').html('Authentication required');
        } else {
          $('#plans .content').html('<div class="error">Failed to load catalog: ' + error + '</div>');
          $('#services .content').html('<div class="error">Service unavailable</div>');
          $('#log code').html('Service unavailable');
        }
      }
    });
  });
})(jQuery, document, window);
