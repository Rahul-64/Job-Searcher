// India Tech Jobs dashboard. All data from the API is untrusted (it originates
// on third-party career sites), so it is only ever inserted with textContent or
// as an attribute after validation, never as HTML.
import {
  label, safeHref, stateFromSearch, searchFromState, apiQuery, pageInfo,
  formatSalary, formatExp, formatLocation, timeAgo, formatCount, splitHighlights,
} from "./lib.mjs";

const $ = (id) => document.getElementById(id);
const FILTER_IDS = ["city", "company", "remote", "seniority", "employment", "posted_within", "exp", "sort"];
const CHECK_IDS = ["hide_unknown_exp", "has_salary"];

let state = stateFromSearch(location.search);
let requestSeq = 0;
let searchTimer;

/** Small element builder: text is always set via textContent. */
function el(tag, props = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "text") node.textContent = v;
    else if (k === "class") node.className = v;
    else node.setAttribute(k, v);
  }
  for (const c of [].concat(children)) if (c) node.append(c);
  return node;
}

async function api(path, params = "") {
  const res = await fetch(`/api/${path}${params ? "?" + params : ""}`, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let msg = `Request failed (${res.status})`;
    try { msg = (await res.json()).error || msg; } catch { /* keep default */ }
    throw new Error(msg);
  }
  return res.json();
}

// ---- state <-> controls <-> URL ------------------------------------------------

function applyStateToControls() {
  $("q").value = state.q || "";
  for (const id of FILTER_IDS) $(id).value = state[id] || "";
  for (const id of CHECK_IDS) $(id).checked = state[id] === "1";
  updateSortOptions();
}

function readControls() {
  const next = { page: 1 };
  const q = $("q").value.trim();
  if (q) next.q = q;
  for (const id of FILTER_IDS) {
    const v = $(id).value.trim();
    if (v) next[id] = v;
  }
  for (const id of CHECK_IDS) if ($(id).checked) next[id] = "1";
  return next;
}

function updateSortOptions() {
  // "Best match" only makes sense with a text query.
  $("sort").options[0].textContent = $("q").value.trim() ? "Best match" : "Newest first";
}

function pushState() {
  history.replaceState(null, "", location.pathname + searchFromState(state) + location.hash);
}

function onFiltersChanged() {
  state = readControls();
  pushState();
  updateSortOptions();
  loadJobs();
}

// ---- jobs list ------------------------------------------------------------------

async function loadJobs() {
  const seq = ++requestSeq;
  $("count").textContent = "Searching…";
  try {
    const data = await api("jobs", apiQuery(state));
    if (seq !== requestSeq) return; // a newer search superseded this one
    renderJobs(data);
  } catch (err) {
    if (seq !== requestSeq) return;
    $("results").replaceChildren();
    $("pager").hidden = true;
    $("count").textContent = "";
    showEmpty(`Couldn't load jobs: ${err.message}`, true);
  }
}

function showEmpty(text, isError = false) {
  const empty = $("empty");
  empty.textContent = text;
  empty.className = "empty" + (isError ? " error" : "");
  empty.hidden = !text;
}

function renderJobs(data) {
  const info = pageInfo(data.total, state.page);
  $("count").textContent = data.total === 0
    ? ""
    : `Showing ${formatCount(info.from)}–${formatCount(info.to)} of ${formatCount(data.total)} jobs`;

  const list = $("results");
  list.replaceChildren(...data.jobs.map(jobCard));
  showEmpty(data.total === 0 ? "No jobs match these filters. Try removing one, or search for something broader." : "");

  $("pager").hidden = info.pages <= 1;
  $("prev").disabled = !info.hasPrev;
  $("next").disabled = !info.hasNext;
  $("page-label").textContent = `Page ${state.page} of ${info.pages}`;
}

function chip(text, cls = "") {
  return text ? el("li", { class: "chip " + cls, text }) : null;
}

function jobCard(job) {
  const title = el("button", { type: "button", text: job.title });
  title.addEventListener("click", () => openJob(job.id));

  const salary = formatSalary(job.salary);
  const chips = el("ul", { class: "chips", "aria-label": "Job details" }, [
    chip(label(job.remote_type)),
    chip(label(job.seniority)),
    chip(label(job.employment_type)),
    chip(formatExp(job.exp_min, job.exp_max)),
    salary ? chip(salary, "money") : null,
  ]);

  return el("li", { class: "card" }, [
    el("h3", {}, title),
    el("div", { class: "line", text: `${job.company.name} · ${formatLocation(job)} · ${timeAgo(job.posted_at || job.first_seen_at)}` }),
    job.snippet ? el("p", { class: "snippet" }, splitHighlights(job.snippet).map((s) =>
      s.hit ? el("mark", { text: s.text }) : document.createTextNode(s.text))) : null,
    chips,
  ]);
}

// ---- job detail -------------------------------------------------------------------

async function openJob(id, { updateHash = true } = {}) {
  const dialog = $("job");
  $("job-title").textContent = "Loading…";
  for (const k of ["job-meta", "job-chips", "job-desc", "job-postings"]) $(k).replaceChildren();
  $("job-places").replaceChildren();
  $("job-apply").hidden = true;
  if (!dialog.open) dialog.showModal();
  if (updateHash) history.replaceState(null, "", location.pathname + location.search + "#job=" + id);

  try {
    const j = await api("job", `id=${encodeURIComponent(id)}`);
    $("job-title").textContent = j.title;
    $("job-meta").textContent = `${j.company.name} · ${formatLocation(j)}` +
      (j.closed_at ? " · No longer listed" : ` · Posted ${timeAgo(j.posted_at || j.first_seen_at)}`);

    const salary = formatSalary(j.salary);
    $("job-chips").replaceChildren(...[
      chip(label(j.remote_type)), chip(label(j.seniority)), chip(label(j.employment_type)),
      chip(formatExp(j.exp_min, j.exp_max)), salary ? chip(salary, "money") : null,
    ].filter(Boolean));

    const href = safeHref(j.url);
    const apply = $("job-apply");
    if (href && !j.closed_at) { apply.href = href; apply.hidden = false; } else { apply.removeAttribute("href"); apply.hidden = true; }

    if (j.places.length > 1) {
      const items = j.places.map((p) => el("li", { text: [p.city, p.region, p.country].filter(Boolean).join(", ") }));
      $("job-places").replaceChildren(el("h3", { text: "Locations" }), el("ul", {}, items));
    }
    $("job-desc").textContent = j.description || "No description available.";
    const sources = j.postings.map((p) => p.source).join(", ");
    $("job-postings").textContent = sources ? `Listed on: ${sources}` : "";
  } catch (err) {
    $("job-title").textContent = "Couldn't load this job";
    $("job-desc").textContent = err.message;
  }
}

function closeJob() {
  if ($("job").open) $("job").close();
}

// ---- source health ----------------------------------------------------------------

async function loadSources() {
  const body = $("sources-body");
  body.replaceChildren(el("tr", {}, el("td", { colspan: "6", text: "Loading…" })));
  try {
    const rows = await api("sources");
    body.replaceChildren(...rows.map((s) => {
      const state = !s.enabled ? el("td", { class: "off", text: "Off" }) : el("td", { class: "ok", text: "On" });
      const result = el("td", { class: s.last_status === "ok" ? "ok" : s.last_status === "partial" ? "partial" : s.last_status ? "off" : "", text: s.last_status || "–" });
      return el("tr", {}, [
        el("td", { text: s.company }), el("td", { text: s.kind }), state,
        el("td", { class: "num", text: formatCount(s.open_jobs) }),
        el("td", { text: s.last_run ? timeAgo(s.last_run) : "never" }), result,
      ]);
    }));
  } catch (err) {
    body.replaceChildren(el("tr", {}, el("td", { colspan: "6", class: "off", text: err.message })));
  }
}

// ---- facets, stats, routing ---------------------------------------------------------

function fillSelect(id, values, labelFn = (v) => v.label || label(v.value)) {
  const select = $(id);
  const keep = select.options[0];
  select.replaceChildren(keep, ...values.map((v) => el("option", { value: v.value, text: `${labelFn(v)} (${formatCount(v.count)})` })));
  if (state[id]) select.value = state[id];
}

async function loadFacets() {
  try {
    const f = await api("facets");
    fillSelect("city", f.cities, (v) => v.value);
    fillSelect("company", f.companies);
    fillSelect("remote", f.remote_types);
    fillSelect("seniority", f.seniorities);
    fillSelect("employment", f.employment_types);
  } catch { /* filters stay usable as typed values; the list shows its own error */ }
}

async function loadStats() {
  try {
    const s = await api("stats");
    const updated = s.last_scrape ? ` · updated ${timeAgo(s.last_scrape)}` : "";
    $("stats").textContent = `${formatCount(s.open_jobs)} open jobs at ${formatCount(s.companies)} companies · ${formatCount(s.new_last_7_days)} new this week${updated}`;
  } catch {
    $("stats").textContent = "";
  }
}

function setCurrent(link, isCurrent) {
  if (isCurrent) link.setAttribute("aria-current", "page");
  else link.removeAttribute("aria-current");
}

function showView() {
  const sources = location.hash === "#sources";
  $("view-jobs").hidden = sources;
  $("view-sources").hidden = !sources;
  setCurrent($("nav-jobs"), !sources);
  setCurrent($("nav-sources"), sources);
  if (sources) loadSources();
}

function routeFromHash() {
  const m = /^#job=(\d{1,12})$/.exec(location.hash);
  if (m) openJob(m[1], { updateHash: false });
  else showView();
}

function init() {
  applyStateToControls();

  $("search").addEventListener("submit", (e) => { e.preventDefault(); clearTimeout(searchTimer); onFiltersChanged(); });
  $("q").addEventListener("input", () => { updateSortOptions(); clearTimeout(searchTimer); searchTimer = setTimeout(onFiltersChanged, 350); });
  for (const id of [...FILTER_IDS, ...CHECK_IDS]) $(id).addEventListener("change", onFiltersChanged);
  $("exp").addEventListener("input", () => { clearTimeout(searchTimer); searchTimer = setTimeout(onFiltersChanged, 450); });
  $("clear").addEventListener("click", () => { state = { page: 1 }; applyStateToControls(); pushState(); loadJobs(); });
  $("prev").addEventListener("click", () => { state.page = Math.max(1, state.page - 1); pushState(); loadJobs(); scrollTo({ top: 0 }); });
  $("next").addEventListener("click", () => { state.page += 1; pushState(); loadJobs(); scrollTo({ top: 0 }); });

  $("job-close").addEventListener("click", closeJob);
  $("job").addEventListener("click", (e) => { if (e.target === $("job")) closeJob(); }); // backdrop click
  $("job").addEventListener("close", () => {
    if (/^#job=/.test(location.hash)) history.replaceState(null, "", location.pathname + location.search);
  });
  addEventListener("hashchange", routeFromHash);

  loadStats();
  loadFacets();
  loadJobs();
  routeFromHash();
}

init();
