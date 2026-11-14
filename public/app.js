// India Tech Jobs dashboard. Everything from the API is untrusted (it originates on
// third-party career sites), so it is only ever inserted with textContent / text nodes,
// or as an attribute after validation, never as HTML. The page's CSP also forbids inline
// scripts and styles, so there are none here: visibility is toggled with `hidden` and
// classes only.
import {
  label, safeHref, stateFromSearch, searchFromState, apiQuery, pageInfo,
  formatSalary, formatExp, formatLocation, timeAgo, formatCount, splitHighlights,
  VIEWS, activeView, applyView, removeFilter, activeFilters,
} from "./lib.mjs";

const $ = (id) => document.getElementById(id);

const SELECTS = ["city", "company", "seniority", "employment", "posted_within"];
const SEGMENTS = {
  exp: [["", "Any"], ["0", "0"], ["1", "1"], ["2", "2"], ["3", "3"], ["5", "5"]],
  remote: [["", "Any"], ["remote", "Remote"], ["hybrid", "Hybrid"], ["onsite", "On-site"]],
};

let state = stateFromSearch(location.search);
let companyNames = {};        // slug -> display name, filled from the facets
let viewCounts = {};          // quick view id -> number of jobs
let selectedId = null;
let listSeq = 0;
let detailSeq = 0;
let searchTimer;

// ---- small helpers -----------------------------------------------------------------

/** Element builder: text is always set via textContent. */
function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function icon(id) {
  const ns = "http://www.w3.org/2000/svg";
  const s = document.createElementNS(ns, "svg");
  s.setAttribute("class", "i");
  const u = document.createElementNS(ns, "use");
  u.setAttribute("href", "#" + id);
  s.appendChild(u);
  return s;
}

async function api(path, params = "") {
  const res = await fetch(`/api/${path}${params ? "?" + params : ""}`, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let msg = `Request failed (${res.status})`;
    try { msg = (await res.json()).error || msg; } catch { /* keep the default */ }
    throw new Error(msg);
  }
  return res.json();
}

const initials = (name) => {
  const w = name.replace(/[^A-Za-z0-9 ]/g, "").split(/\s+/).filter(Boolean);
  return (w.length > 1 ? w[0][0] + w[1][0] : name.slice(0, 2)).toUpperCase();
};

function highlighted(snippet) {
  const frag = document.createDocumentFragment();
  for (const s of splitHighlights(snippet)) {
    frag.append(s.hit ? el("mark", "", s.text) : document.createTextNode(s.text));
  }
  return frag;
}

// ---- state <-> controls <-> URL ----------------------------------------------------

/** Applies a new filter state: URL, controls and results all follow it. */
function setState(next) {
  state = next;
  for (const k of Object.keys(state)) if (state[k] === "" || state[k] == null) delete state[k];
  history.replaceState(null, "", location.pathname + searchFromState(state) + location.hash);
  syncControls();
  loadJobs();
}

const change = (patch) => setState({ ...state, ...patch, page: 1 });

function buildControls() {
  for (const [id, items] of Object.entries(SEGMENTS)) {
    const box = $(id);
    for (const [value, text] of items) {
      const b = el("button", "", text);
      b.type = "button";
      b.dataset.value = value;
      b.addEventListener("click", () => {
        const patch = { [id]: value };
        if (id === "exp" && value === "") patch.hide_unknown_exp = "";
        change(patch);
      });
      box.append(b);
    }
  }
  const views = $("views");
  for (const v of VIEWS) {
    const b = el("button", "view");
    b.type = "button";
    b.dataset.view = v.id;
    b.append(icon({ all: "i-list", fresher: "i-cap", intern: "i-briefcase", remote: "i-home", new: "i-clock" }[v.id]));
    b.append(el("span", "", v.label), el("span", "n tnum", ""));
    b.addEventListener("click", () => setState(applyView(state, v.id)));
    views.append(b);
    if (v.note) {
      const n = el("div", "view-note", v.note);
      n.dataset.noteFor = v.id;
      views.append(n);
    }
  }
}

function syncControls() {
  if (document.activeElement !== $("q")) $("q").value = state.q || ""; // never rewrite what is being typed
  for (const id of SELECTS) $(id).value = state[id] || "";
  for (const id of Object.keys(SEGMENTS)) {
    for (const b of $(id).children) b.setAttribute("aria-pressed", String(b.dataset.value === (state[id] || "")));
  }
  const hasExp = state.exp !== undefined && state.exp !== "";
  $("include_unknown").checked = !state.hide_unknown_exp;
  $("include_unknown").disabled = !hasExp; // only meaningful once an experience is chosen
  $("has_salary").checked = !!state.has_salary;

  // With a text search the server ranks by best match unless told otherwise.
  const sort = $("sort");
  const searching = !!state.q;
  sort.options[0].textContent = searching ? "Best match" : "Newest first";
  sort.options[1].hidden = !searching;
  sort.value = searching ? (state.sort || "") : "";

  const active = activeView(state);
  for (const b of $("views").querySelectorAll(".view")) b.setAttribute("aria-pressed", String(b.dataset.view === active));
  for (const n of $("views").querySelectorAll(".view-note")) n.hidden = n.dataset.noteFor !== active;
}

function renderViewCounts() {
  for (const b of $("views").querySelectorAll(".view")) {
    const n = viewCounts[b.dataset.view];
    b.querySelector(".n").textContent = n == null ? "" : formatCount(n);
  }
}

function renderChips() {
  const chips = activeFilters(state, (slug) => companyNames[slug] || slug);
  const box = $("chips");
  box.replaceChildren();
  for (const c of chips) {
    const b = el("button", "chip");
    b.type = "button";
    b.setAttribute("aria-label", `Remove filter: ${c.text}`);
    b.append(document.createTextNode(c.text), icon("i-x"));
    b.addEventListener("click", () => setState(removeFilter(state, c.key)));
    box.append(b);
  }
  $("filter-n").textContent = chips.length ? `(${chips.length})` : "";
  $("clear").disabled = chips.length === 0 && !state.sort;
}

// ---- jobs list ---------------------------------------------------------------------

async function loadJobs() {
  const seq = ++listSeq;
  $("summary").textContent = "Searching…";
  renderChips();
  try {
    const data = await api("jobs", apiQuery(state));
    if (seq !== listSeq) return; // a newer search superseded this one
    if (data.jobs.length === 0 && data.total > 0 && (state.page || 1) > 1) {
      return setState({ ...state, page: 1 }); // a link to a page that no longer exists
    }
    renderJobs(data);
  } catch (err) {
    if (seq !== listSeq) return;
    $("list").replaceChildren();
    $("pager").hidden = true;
    $("summary").textContent = "";
    $("foot-count").textContent = "";
    showEmpty("Couldn't load jobs", err.message, true);
  }
}

function showEmpty(title, text, isError = false) {
  $("empty-title").textContent = title;
  $("empty-text").textContent = text;
  $("empty").className = "empty" + (isError ? " error" : "");
  $("empty-clear").hidden = isError;
  $("empty").hidden = false;
}

function renderJobs(data) {
  const info = pageInfo(data.total, state.page || 1);
  const noun = data.total === 1 ? "job" : "jobs";
  $("summary").textContent = `${formatCount(data.total)} ${noun}`;
  $("foot-count").textContent = data.total ? `Showing ${formatCount(info.from)}–${formatCount(info.to)}` : "No matches";

  $("list").replaceChildren(...data.jobs.map(jobRow));
  if (data.total === 0) showEmpty("No jobs match these filters", "Try a broader search, or remove a filter.");
  else $("empty").hidden = true;

  $("pager").hidden = info.pages <= 1;
  $("prev").disabled = !info.hasPrev;
  $("next").disabled = !info.hasNext;
  $("page-label").textContent = `Page ${state.page || 1} of ${info.pages}`;
  $("list-wrap").scrollTop = 0;
}

function tag(text, cls = "") {
  return text ? el("span", "tag " + cls, text) : null;
}

function jobRow(job) {
  const li = el("li");
  const b = el("button", "row");
  b.type = "button";
  b.dataset.id = String(job.id);
  if (job.id === selectedId) b.setAttribute("aria-current", "true");

  b.append(el("span", "avatar", initials(job.company.name)));

  const main = el("span", "r-main");
  main.append(
    el("span", "r-title", job.title),
    el("span", "r-meta", `${job.company.name} · ${formatLocation(job)} · ${timeAgo(job.posted_at || job.first_seen_at)}`),
  );
  if (state.q && job.snippet) {
    const sn = el("span", "r-snip");
    sn.append(highlighted(job.snippet));
    main.append(sn);
  }
  b.append(main);

  const exp = formatExp(job.exp_min, job.exp_max);
  const tags = el("span", "r-tags");
  for (const t of [
    job.entry_level ? tag("Entry level", "entry") : null,
    job.seniority && job.seniority !== "junior" ? tag(label(job.seniority)) : null,
    exp && !(job.entry_level && exp === "Entry level") ? tag(exp, "tnum") : null,
    tag(label(job.remote_type)),
    job.salary ? tag(formatSalary(job.salary), "tnum") : null,
  ]) if (t) tags.append(t);
  b.append(tags);

  b.addEventListener("click", () => openJob(job.id));
  li.append(b);
  return li;
}

function markSelectedRow() {
  for (const b of $("list").querySelectorAll(".row")) {
    if (b.dataset.id === String(selectedId)) b.setAttribute("aria-current", "true");
    else b.removeAttribute("aria-current");
  }
}

// ---- job detail pane ---------------------------------------------------------------

function renderPaneEmpty() {
  const e = el("div", "pane-empty");
  e.append(el("strong", "", "Select a job"), el("span", "", "Its description and details open here."));
  $("pane").replaceChildren(e);
}

function paneBar() {
  const bar = el("div", "pane-bar");
  const back = el("button", "btn-text");
  back.type = "button";
  back.append(icon("i-back"), document.createTextNode(" Back to jobs"));
  back.addEventListener("click", closePane);
  bar.append(back);
  return bar;
}

async function openJob(id, { updateHash = true } = {}) {
  const seq = ++detailSeq;
  selectedId = Number(id);
  markSelectedRow();
  $("app").classList.add("pane-open");
  if (updateHash) history.replaceState(null, "", location.pathname + location.search + "#job=" + id);

  const loading = el("div", "pane-in");
  loading.append(paneBar(), el("h2", "", "Loading…"));
  $("pane").replaceChildren(loading);

  try {
    const j = await api("job", `id=${encodeURIComponent(id)}`);
    if (seq !== detailSeq) return;
    renderPane(j);
  } catch (err) {
    if (seq !== detailSeq) return;
    const box = el("div", "pane-in");
    box.append(paneBar(), el("h2", "", "Couldn't load this job"), el("p", "sub", err.message));
    $("pane").replaceChildren(box);
  }
}

function renderPane(j) {
  const box = el("div", "pane-in");
  box.append(paneBar(), el("h2", "", j.title), el("p", "sub", `${j.company.name} · ${formatLocation(j)}`));
  if (j.entry_level) {
    const tags = el("div", "tags");
    tags.append(tag("Entry level", "entry"));
    box.append(tags);
  }
  if (j.closed_at) box.append(el("p", "closed-note", "This job is no longer listed by the company."));

  const href = safeHref(j.url);
  if (href && !j.closed_at) {
    const a = el("a", "btn primary", "Apply on company site");
    a.href = href;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.append(icon("i-ext"));
    const actions = el("div", "actions");
    actions.append(a);
    box.append(actions);
  }

  const facts = el("dl", "facts");
  for (const [k, v] of [
    ["Experience", formatExp(j.exp_min, j.exp_max)],
    ["Level", label(j.seniority)],
    ["Work arrangement", label(j.remote_type)],
    ["Employment", label(j.employment_type)],
    ["Posted", timeAgo(j.posted_at || j.first_seen_at)],
    ["Pay", formatSalary(j.salary)],
  ]) {
    const d = el("div");
    d.append(el("dt", "", k), el("dd", "", v || "Not stated"));
    facts.append(d);
  }
  box.append(facts);

  if (j.places.length > 1) {
    const places = el("div", "places");
    const ul = el("ul");
    for (const p of j.places) ul.append(el("li", "", [p.city, p.region, p.country].filter(Boolean).join(", ")));
    places.append(el("h3", "", "Locations"), ul);
    box.append(places);
  }

  box.append(el("h3", "", "About the role"), el("div", "desc", j.description || "No description available."));
  const sources = j.postings.map((p) => p.source).join(", ");
  if (sources) box.append(el("p", "src", `Listed on ${sources}`));

  $("pane").replaceChildren(box);
  $("pane").scrollTop = 0;
}

function closePane() {
  detailSeq++;
  selectedId = null;
  $("app").classList.remove("pane-open");
  if (/^#job=/.test(location.hash)) history.replaceState(null, "", location.pathname + location.search);
  markSelectedRow();
  renderPaneEmpty();
}

// ---- facets, stats, sources --------------------------------------------------------

function fillSelect(id, values, text) {
  const select = $(id);
  const keep = select.options[0];
  select.replaceChildren(keep, ...values.map((v) => {
    const o = el("option", "", `${text(v)} (${formatCount(v.count)})`);
    o.value = v.value;
    return o;
  }));
}

async function loadFacets() {
  try {
    const f = await api("facets");
    companyNames = Object.fromEntries(f.companies.map((c) => [c.value, c.label || c.value]));
    fillSelect("city", f.cities, (v) => v.value);
    fillSelect("company", f.companies, (v) => v.label || v.value);
    fillSelect("seniority", f.seniorities, (v) => label(v.value));
    fillSelect("employment", f.employment_types, (v) => label(v.value));
    const remote = f.remote_types.find((r) => r.value === "remote");
    Object.assign(viewCounts, {
      fresher: f.quick.entry_level, intern: f.quick.internships, new: f.quick.new_7_days, remote: remote ? remote.count : 0,
    });
    renderViewCounts();
    syncControls();   // the options exist now, so the saved selection can show
    renderChips();    // company chips can use display names
  } catch { /* filters stay usable; the list shows its own error */ }
}

async function loadStats() {
  try {
    const s = await api("stats");
    viewCounts.all = s.open_jobs;
    renderViewCounts();
    $("nav-jobs-n").textContent = formatCount(s.open_jobs);
    $("updated").textContent = s.last_scrape
      ? "Updated " + new Date(s.last_scrape).toLocaleString("en-IN", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false })
      : "Not scraped yet";
  } catch {
    $("updated").textContent = "";
  }
}

async function loadSources() {
  const body = $("src-body");
  const cell = (text, cls = "") => el("td", cls, text);
  const loadingRow = el("tr");
  loadingRow.append(cell("Loading…"));
  body.replaceChildren(loadingRow);
  try {
    const rows = await api("sources");
    body.replaceChildren(...rows.map((s) => {
      const tr = el("tr");
      const stateCell = cell("");
      stateCell.append(el("span", "dot" + (s.enabled ? "" : " off")), document.createTextNode(s.enabled ? "On" : "Off"));
      const result = cell(s.last_status || "–", s.last_status === "ok" ? "ok" : s.last_status ? "bad" : "");
      tr.append(cell(s.company), cell(s.kind), stateCell, cell(formatCount(s.open_jobs), "num tnum"),
        cell(s.last_run ? timeAgo(s.last_run) : "never"), result);
      return tr;
    }));
    $("src-summary").textContent = `${rows.length} sources`;
    $("nav-sources-n").textContent = String(rows.length);
  } catch (err) {
    const tr = el("tr");
    tr.append(cell(err.message, "bad"));
    body.replaceChildren(tr);
  }
}

// ---- views, drawer, theme, routing -------------------------------------------------

function showView(sources) {
  $("view-jobs").hidden = sources;
  $("view-sources").hidden = !sources;
  $("app").classList.toggle("no-pane", sources);
  $("nav-jobs").setAttribute("aria-current", sources ? "false" : "page");
  $("nav-sources").setAttribute("aria-current", sources ? "page" : "false");
  closeSide();
  if (sources) loadSources();
}

function route() {
  const m = /^#job=(\d{1,12})$/.exec(location.hash);
  showView(location.hash === "#sources");
  if (m) openJob(m[1], { updateHash: false });
  else if (selectedId !== null) closePane();
}

const openSide = () => { $("app").classList.add("side-open"); $("scrim").hidden = false; };
function closeSide() { $("app").classList.remove("side-open"); $("scrim").hidden = true; }

function themeIsDark() {
  const t = document.documentElement.getAttribute("data-theme");
  return t ? t === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
}

function paintThemeIcon() {
  $("theme").querySelector("use").setAttribute("href", themeIsDark() ? "#i-sun" : "#i-moon");
}

// ---- start -------------------------------------------------------------------------

function init() {
  buildControls();
  syncControls();
  paintThemeIcon();
  renderPaneEmpty();

  $("filters").addEventListener("submit", (e) => e.preventDefault());
  $("q").addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => change({ q: $("q").value.trim() }), 300);
  });
  $("q").addEventListener("focus", () => { $("kbd").hidden = true; });
  $("q").addEventListener("blur", () => { $("kbd").hidden = !!$("q").value; });
  for (const id of SELECTS) $(id).addEventListener("change", () => change({ [id]: $(id).value }));
  $("sort").addEventListener("change", () => change({ sort: $("sort").value }));
  $("include_unknown").addEventListener("change", () => change({ hide_unknown_exp: $("include_unknown").checked ? "" : "1" }));
  $("has_salary").addEventListener("change", () => change({ has_salary: $("has_salary").checked ? "1" : "" }));
  $("clear").addEventListener("click", () => setState({ page: 1 }));
  $("empty-clear").addEventListener("click", () => setState({ page: 1 }));
  $("prev").addEventListener("click", () => setState({ ...state, page: Math.max(1, (state.page || 1) - 1) }));
  $("next").addEventListener("click", () => setState({ ...state, page: (state.page || 1) + 1 }));

  $("open-side").addEventListener("click", openSide);
  $("open-side-2").addEventListener("click", openSide);
  $("side-close").addEventListener("click", closeSide);
  $("scrim").addEventListener("click", closeSide);

  $("theme").addEventListener("click", () => {
    const next = themeIsDark() ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    try { localStorage.setItem("itj-theme", next); } catch { /* storage unavailable */ }
    paintThemeIcon();
  });

  addEventListener("hashchange", route);
  addEventListener("keydown", (e) => {
    const tag = document.activeElement ? document.activeElement.tagName : "";
    if (e.key === "/" && tag !== "INPUT" && tag !== "SELECT" && tag !== "TEXTAREA") {
      e.preventDefault();
      if (matchMedia("(max-width: 859px)").matches) openSide();
      $("q").focus();
    }
    if (e.key === "Escape") {
      if ($("app").classList.contains("side-open")) closeSide();
      else if (selectedId !== null) closePane();
    }
  });

  loadStats();
  loadFacets();
  loadJobs();
  route();
}

init();
