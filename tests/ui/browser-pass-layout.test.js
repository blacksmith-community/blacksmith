const {test,expect}=require('bun:test');
const fs=require('fs');
const path=require('path');

const UI=path.resolve(__dirname,'..','..','ui');
const read=(f)=>fs.readFileSync(path.join(UI,f),'utf8');
const strip=(css)=>css.replace(/\/\*[\s\S]*?\*\//g,'');
// The body of every rule whose selector list contains exactly this selector.
const bodies=(file,selector)=>{
 const out=[];
 strip(read(file)).replace(/([^{}]+)\{([^{}]*)\}/g,(m,sel,body)=>{
  if(sel.split(',').map((s)=>s.replace(/\s+/g,' ').trim()).includes(selector)) out.push(body);
  return m;
 });
 return out.join(';');
};

// The bare header rule in blacksmith.css was written for the page header and
// also reaches the Appearance dialog's header, so the dialog resets it.
test('the Appearance dialog header is one plain row with no strip styling',()=>{
 const b=bodies('action-help.css','.theme-choice-dialog header');
 expect(b).toMatch(/flex-direction:\s*row/);
 expect(b).toMatch(/height:\s*auto/);
 expect(b).toMatch(/box-shadow:\s*none/);
 expect(b).toMatch(/position:\s*static/);
});
test('the Appearance dialog drops the padding Oat adds around its paragraph and list',()=>{
 const b=bodies('action-help.css','.theme-choice-dialog > div');
 expect(b).toMatch(/padding:\s*0/);
});

// Task headers carry a .sort-icon span, and the th pseudo-element drew a
// second icon beside it.
test('a header with its own sort icon does not draw the th icon too',()=>{
 const b=bodies('blacksmith.css','table thead th:has(> .sort-icon)::after');
 expect(b).toMatch(/content:\s*none/);
 expect(bodies('blacksmith.css','table thead th:has(> .sort-icon)')).toMatch(/white-space:\s*nowrap/);
});
test('tables without the span keep the th sort icon',()=>{
 expect(bodies('blacksmith.css','table thead th.sortable::after')).toMatch(/mask-image:\s*var\(--icon-arrow-up-down\)/);
});

// The generic filter-row label is full width, which pushed the help button
// of the pause checkbox to the far edge of its row.
test('the pause checkbox label is only as wide as its text',()=>{
 const b=bodies('forge-theme.css','#upgrade .upgrade-filters .filter-row label.batch-pause-option');
 expect(b).toMatch(/width:\s*auto/);
 expect(b).toMatch(/flex:\s*0 1 auto/);
});

// Only #upgrade styled .btn-secondary, so every modal footer button read as
// a filled primary button.
test('.btn-secondary and .btn-success have global styles',()=>{
 const sec=bodies('blacksmith.css','.btn-secondary');
 expect(sec).toMatch(/background-color:\s*var\(--bg-hover\)/);
 expect(sec).toMatch(/border:\s*1px solid var\(--border-primary\)/);
 expect(sec).toMatch(/color:\s*var\(--text-primary\)/);
 expect(bodies('blacksmith.css','.btn-secondary:hover')).toMatch(/background-color:\s*var\(--bg-secondary\)/);
 const ok=bodies('blacksmith.css','.btn-success');
 expect(ok).toMatch(/background-color:\s*var\(--primary\)/);
 expect(ok).toMatch(/color:\s*var\(--primary-foreground\)/);
});
test('a modal footer groups its actions on the right',()=>{
 expect(bodies('blacksmith.css','.modal-footer .modal-actions')).toMatch(/display:\s*flex/);
});
test('the registration footer dismisses on the left and groups its actions',()=>{
 const html=read('index.html');
 const footer=html.slice(html.indexOf('<div class="modal-footer">',html.indexOf('id="registration-modal"')));
 const end=footer.slice(0,footer.indexOf('</dialog>'));
 const ids=[...end.matchAll(/<button[^>]*id="([^"]+)"/g)].map((m)=>m[1]);
 expect(ids).toEqual(['cancel-registration','test-connection-btn','save-registration-btn']);
 expect(end).toMatch(/<div class="modal-actions">\s*<button[^>]*id="test-connection-btn"[\s\S]*id="save-registration-btn"[\s\S]*<\/div>\s*<\/div>/);
 expect(end).toMatch(/class="btn btn-secondary" id="test-connection-btn"/);
 expect(end).toMatch(/class="btn btn-success" id="save-registration-btn"/);
 expect(end).toMatch(/class="btn btn-secondary" id="cancel-registration"/);
});
test('the endpoint footer dismisses on the left and groups its actions',()=>{
 const js=read('blacksmith.js');
 const i=js.indexOf('<div class="modal-footer">',js.indexOf('id="cf-endpoint-modal"'));
 const footer=js.slice(i,js.indexOf('</dialog>',i));
 expect(footer).toMatch(/Close<\/button>\s*<div class="modal-actions">\s*<button[^>]*id="discard-endpoint-draft">Discard draft<\/button>\s*<button[^>]*btn-primary[^>]*saveCFEndpoint/);
});

// The Logs toolbar's info button was a filled square. Every other info button
// is the outlined circle that view-guidance.css draws.
test('the Logs info button is an outlined circle like the guidance buttons',()=>{
 const guide=bodies('view-guidance.css','.view-guidance-button');
 const b=bodies('forge-theme.css','.log-info-toggle');
 for(const prop of ['width','height','padding','background','color','border','border-radius']){
  const get=(css)=>(css.match(new RegExp('(?:^|[;\\s])'+prop+':\\s*([^;]+)'))||[])[1];
  expect(get(b)).toBeDefined();
  expect(get(b).trim()).toBe(get(guide).trim());
 }
 expect(b).toMatch(/margin-left:\s*auto/);
 expect(bodies('forge-theme.css','.log-info-toggle:hover')).toMatch(/background:\s*var\(--secondary\)/);
});
