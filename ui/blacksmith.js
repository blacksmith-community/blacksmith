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

  $(function () {
    $.ajax({
      type: 'GET',
      url:  '/v2/catalog',
      success: function (catalog) {
        $.ajax({
          type: 'GET',
          url:  '/b/status',
          success: function (data) {
            var instances = {}
            $.each(data.instances, function (_, instance) {
              instances[instance.plan_id] = (instances[instance.plan_id] || 0) + 1;
            });

            var plans = {};
            $.each(catalog.services, function (i, service) {
              $.each(service.plans, function (j, plan) {
                var key = service.id + '/' + plan.id;
                plans[plan.id] = key;
                console.log("looking up service ["+service.id+"] plan ["+plan.id+"] (as '"+plan.name+"') using key {"+key+"}");

                catalog.services[i].plans[j].blacksmith = {
                  instances: instances[plan.id] || 0,
                  limit:     data.plans[key].limit || 0
                };
              });
            });

            $.each(data.instances, function (i, instance) {
              data.instances[i].plan = data.plans[plans[instance.plan_id]];
            });

            $('#ident').html(data.env);
            $('#plans .content').html($.template('plans', catalog));
            $('#services .content').html($.template('services', data.instances));
            $('#log code').html(data.log.replace(/</g, '&lt;')
                                        .replace(/>/g, '&gt;')
                                        .replace(/^([\d-]+\s\S+)/mg, '<span class="d">$1</span>')
                                        .replace(/\[([^\]]+)\]/g, '[<span class="i">$1</span>]')
                                        .replace(/ (ERROR|INFO|DEBUG) /g, ' <span class="l $1">$1</span> '));
          }
        });
      }
    });
  });
})(jQuery, document, window);
