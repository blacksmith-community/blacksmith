const {test,expect}=require('bun:test');
const {expectNoAlerts}=require('./certificates/no-alerts.js');

test('a recorded dialog call fails without printing its text',()=>{
 let failure=null;
 try{ expectNoAlerts({alerts:['alert:echo SECRET-COMMAND-TEXT']}); }catch(error){ failure=error; }
 expect(failure).not.toBeNull();
 expect(String(failure.message).includes('SECRET-COMMAND-TEXT')).toBe(false);
 expect(failure.message.startsWith('1 dialog call')).toBe(true);
});

test('no recorded dialog call passes',()=>{
 expect(()=>expectNoAlerts({alerts:[]})).not.toThrow();
 expect(()=>expectNoAlerts({})).not.toThrow();
});
