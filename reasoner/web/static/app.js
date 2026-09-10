// New verdicts are inserted above the existing rows, so scrolling and open diffs are left alone.
const rows = document.getElementById("rows");
const params = new URLSearchParams(location.search);
let after = rows.dataset.after;

async function poll() {
  const query = new URLSearchParams(params);
  if (after) query.set("after", after);
  const [rowsRes, statsRes] = await Promise.all([fetch("/fragments/rows?" + query), fetch("/fragments/stats")]);
  if (!rowsRes.ok || !statsRes.ok) return;
  document.getElementById("stats").innerHTML = await statsRes.text();
  const fragment = document.createElement("template");
  fragment.innerHTML = await rowsRes.text();
  const fresh = fragment.content.querySelectorAll("tr");
  if (fresh.length === 0) return;
  for (const tr of fresh) {
    // A replayed verdict for a revision already shown replaces its old row, matching the upsert.
    rows.querySelector(`tr[data-rev-id="${tr.dataset.revId}"]`)?.remove();
    tr.classList.add("new");
  }
  rows.prepend(fragment.content);
  after = rows.firstElementChild.dataset.reasonedAt;
  document.getElementById("empty").hidden = true;
  rows.parentElement.hidden = false;
}

setInterval(() => poll().catch(() => {}), 20000);
