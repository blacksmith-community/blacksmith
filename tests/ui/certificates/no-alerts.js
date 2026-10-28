// The failure for a dialog call that reached the page. It says how many calls
// there were and never prints their text, because a command suite's dialogs
// can hold the command an operator typed.
const expectNoAlerts=(env)=>{
 const count=(env.alerts||[]).length;
 if(count) throw new Error(`${count} dialog call(s) reached the page; their text is not printed.`);
};
module.exports={expectNoAlerts};
