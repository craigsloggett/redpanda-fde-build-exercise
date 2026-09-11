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
    const selector = `tr[data-rev-id="${tr.dataset.revId}"]`;
    for (const old of rows.querySelectorAll(selector)) old.remove();
    tr.classList.add("new");
  }
  rows.prepend(fragment.content);
  after = rows.querySelector("tr[data-reasoned-at]").dataset.reasonedAt;
  document.getElementById("empty").hidden = true;
  rows.parentElement.hidden = false;
}

let pending = null;
function refresh() {
  if (pending) return;
  pending = setTimeout(() => {
    pending = null;
    poll().catch(() => {});
  }, 250);
}

const events = new EventSource("/events");
events.onopen = refresh;
events.addEventListener("verdict", refresh);

const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

function since(iso) {
  const s = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000));
  if (s < 60) return relative.format(-s, "second");
  if (s < 3600) return relative.format(-Math.floor(s / 60), "minute");
  if (s < 86400) return relative.format(-Math.floor(s / 3600), "hour");
  return relative.format(-Math.floor(s / 86400), "day");
}

setInterval(() => {
  for (const tr of rows.querySelectorAll("tr[data-reasoned-at]")) {
    const cell = tr.cells[0];
    const text = since(tr.dataset.reasonedAt);
    if (cell.textContent !== text) cell.textContent = text;
  }
}, 1000);
