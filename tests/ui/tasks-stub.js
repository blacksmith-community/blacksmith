// A stand-in for the broker's task list. It answers as the broker does:
// states are applied before the page is cut, so a task of a pressed state is
// found however many newer tasks sit in front of it. Each state is counted
// through the same filter that lists its rows, up to 200, and a state that
// reaches 200 is named as capped. The list reaches 2000 rows deep.
const LISTED_MAX = 2000;
const WINDOW = 200;

const row = (id, state) => ({ id, state, description: `task ${id}`, started_at: '2026-10-31T10:00:00Z', timestamp: 1793000000 + id });

// The director's history, newest first. Every tenth task failed, or the ids in failed when it is given.
const history = (total, failed) => Array.from({ length: total }, (_, i) => {
  const id = total - i;
  return row(id, (failed ? failed.includes(id) : id % 10 === 0) ? 'error' : 'done');
});

const serve = (all, shift = () => 0, deep = LISTED_MAX) => (u) => {
  const q = new URL(u, 'http://localhost').searchParams;
  const wanted = (q.get('states') || q.get('state') || '').split(',').filter(Boolean);
  const limit = Number(q.get('limit')) || 50;
  const offset = Number(q.get('offset')) || 0;
  const list = wanted.length ? all.filter((task) => wanted.includes(task.state)) : all;
  const room = Math.min(limit, deep - offset);
  if (room <= 0) return { tasks: [], offset, limit: 0, has_more: true, exact: true, ceiling: true };
  const hasMore = list.length > offset + room;
  const counts = {};
  const capped = [];
  ['processing', 'queued', 'done', 'error', 'cancelled'].forEach((state) => {
    const n = all.filter((task) => task.state === state).length;
    counts[state] = Math.min(n, WINDOW);
    if (n > WINDOW) capped.push(state);
  });
  const start = Math.max(0, offset - shift());
  return {
    tasks: list.slice(start, offset + room), offset, limit: room, has_more: hasMore, exact: true,
    ceiling: hasMore && offset + room >= deep, state_counts: counts, state_counts_capped: capped,
  };
};

module.exports = { row, history, serve };
