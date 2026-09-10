// New verdicts are inserted above the existing rows, so scrolling and open diffs are left alone.
const rows = document.getElementById("rows");
const params = new URLSearchParams(location.search);
let after = rows.dataset.after;

async function poll() {
  const query = new URLSearchParams(params);
  if (after) query.set("after", after);
  const [rowsRes, statsRes] = await Promise.all([
    fetch("/fragments/rows?" + query),
    fetch("/fragments/stats?" + params),
  ]);
  if (!rowsRes.ok || !statsRes.ok) return;
  document.getElementById("stats").innerHTML = await statsRes.text();
  const fragment = document.createElement("template");
  fragment.innerHTML = await rowsRes.text();
  const fresh = fragment.content.querySelectorAll("tr");
  if (fresh.length === 0) return;
  for (const tr of fresh) {
    // A verdict is a row plus an optional diff row. A replayed revision replaces both, matching the upsert.
    const selector = `tr[data-rev-id="${tr.dataset.revId}"]`;
    for (const old of rows.querySelectorAll(selector)) old.remove();
    tr.classList.add("new");
  }
  rows.prepend(fragment.content);
  after = rows.querySelector("tr[data-reasoned-at]").dataset.reasonedAt;
  document.getElementById("empty").hidden = true;
  rows.parentElement.hidden = false;
}

// The sink writes in batches and Postgres announces every row, so a burst collapses into one fetch.
let pending = null;
function refresh() {
  if (pending) return;
  pending = setTimeout(() => {
    pending = null;
    poll().catch(() => {});
  }, 250);
}

const events = new EventSource("/events");
// A reconnect may have missed rows, and the first open costs one harmless fetch.
events.onopen = refresh;
events.addEventListener("verdict", refresh);
