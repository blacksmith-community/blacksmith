const {test,expect}=require('bun:test');
const {main:{boot,sleep}}=require('./certificates/harness.js');
test('logs keep node selection in toolbar and details behind toolbar info',async()=>{
 const {w,d}=boot();await sleep(100);
 const root=d.createElement('section');root.className='entity-main';root.innerHTML='<div class="entity-section-toolbar"><h3>Logs</h3></div><div class="detail-content"></div>';d.body.appendChild(root);
 const content=root.querySelector('.detail-content');
 content.innerHTML=w.__T.formatInstanceLogs({'node/a':{'a.log':'INFO first'},'node/b':{'b.log':'INFO second'}},{cut_count:6,cut_files:[]});
 w.__T.mountLogNotice(content);
 const info=root.querySelector('.table-controls-container .log-info-toggle');expect(info).not.toBe(null);
 const notice=root.querySelector('.instance-logs-notice');expect(notice.hidden).toBe(true);
 info.click();expect(notice.hidden).toBe(false);expect(info.getAttribute('aria-expanded')).toBe('true');info.click();expect(notice.hidden).toBe(true);
 const node=root.querySelector('[data-instance-log="select-job"]');expect(node.tagName).toBe('SELECT');expect(node.closest('.table-controls-container')).not.toBe(null);
 node.value='node/b';node.dispatchEvent(new w.Event('change',{bubbles:true}));
 expect(root.querySelector('[data-instance-log="select-job"]').value).toBe('node/b');expect(content.textContent).toContain('second');
 expect(root.querySelector('.instance-log-job-tabs')).toBe(null);
 w.instanceLogsData['node/b']={error:'Unavailable'};w.selectInstanceJob('node/b');
 expect(root.querySelector('[data-instance-log="select-job"]')).not.toBe(null);
 w.selectInstanceJob('node/a');expect(content.textContent).toContain('first');

 content.innerHTML='Other view';w.__T.mountLogNotice(content);expect(root.querySelector('.log-info-toggle')).toBe(null);
 w.close();
});

test('switching nodes carries the chosen file and explains fallback',async()=>{
 const {w,d}=boot();await sleep(50);w.currentInstanceInfo={id:'one'};
 const content=d.createElement('div');d.body.append(content);
 content.innerHTML=w.__T.formatInstanceLogs({'node/a':{'app.log':'INFO first','debug.log':'INFO debug a'},'node/b':{'app.log':'INFO second','debug.log':'INFO debug b'},'node/c':{'app.log':'INFO third'}});
 const file=content.querySelector('[data-instance-log="select-file"]');file.value='debug.log';file.dispatchEvent(new w.Event('change',{bubbles:true}));
 w.selectInstanceJob('node/b');expect(content.querySelector('[data-instance-log="select-file"]').value).toBe('debug.log');expect(content.textContent).toContain('debug b');
 w.selectInstanceJob('node/c');expect(content.querySelector('[data-instance-log="select-file"]').value).toBe('app.log');expect(content.querySelector('[role=status]').textContent).toContain('debug.log');expect(content.querySelector('[role=status]').textContent).toContain('not available');w.close();
});
test('one node is a plain named label instead of a redundant select',async()=>{
 const {w,d}=boot();await sleep(50);const content=d.createElement('div');content.innerHTML=w.__T.formatInstanceLogs({'node/a':{'app.log':'INFO first'}});
 expect(content.querySelector('[data-instance-log="select-job"]')===null).toBe(true);expect(content.querySelector('.log-node-label').textContent).toContain('node/a');w.close();
});
