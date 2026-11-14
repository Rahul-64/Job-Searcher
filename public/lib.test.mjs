import { test } from "node:test";
import assert from "node:assert/strict";
import {
  label, safeHref, stateFromSearch, searchFromState, apiQuery, pageInfo,
  formatSalary, formatExp, formatLocation, timeAgo, formatCount, splitHighlights,
  VIEWS, activeView, applyView, removeFilter, activeFilters, ROLES, roleLabel,
} from "./lib.mjs";

test("label", () => {
  assert.equal(label("full_time"), "Full-time");
  assert.equal(label("onsite"), "On-site");
  assert.equal(label("senior"), "Senior");
  assert.equal(label("mid"), "Mid-level");
  assert.equal(label(""), "");
  assert.equal(label(null), "");
});

test("safeHref allows only http and https", () => {
  assert.equal(safeHref("https://boards.greenhouse.io/x/jobs/1"), "https://boards.greenhouse.io/x/jobs/1");
  assert.equal(safeHref("http://example.com/a"), "http://example.com/a");
  for (const bad of ["javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,<script>1</script>",
    "vbscript:x", "file:///etc/passwd", "//evil.com", "not a url", "", undefined, null]) {
    assert.equal(safeHref(bad), "", `should reject ${bad}`);
  }
});

test("state round-trips through the URL", () => {
  const state = stateFromSearch("?q=go+backend&city=Pune&remote=hybrid&exp=5&page=3&junk=1");
  assert.deepEqual(state, { page: 3, q: "go backend", city: "Pune", remote: "hybrid", exp: "5" });
  assert.equal(searchFromState(state), "?q=go+backend&city=Pune&remote=hybrid&exp=5&page=3");
  assert.equal(searchFromState({ page: 1 }), "");
  assert.equal(searchFromState({ page: 1, q: "a b&c" }), "?q=a+b%26c");
});

test("stateFromSearch ignores nonsense pages and blank values", () => {
  assert.equal(stateFromSearch("?page=0").page, 1);
  assert.equal(stateFromSearch("?page=-4").page, 1);
  assert.equal(stateFromSearch("?page=abc").page, 1);
  assert.equal(stateFromSearch("?page=999999").page, 1);
  assert.equal(stateFromSearch("?q=%20%20").q, undefined);
});

test("apiQuery sets limit and offset from the page", () => {
  const p = new URLSearchParams(apiQuery({ q: "go", page: 3, has_salary: "1" }));
  assert.equal(p.get("q"), "go");
  assert.equal(p.get("limit"), "20");
  assert.equal(p.get("offset"), "40");
  assert.equal(p.get("has_salary"), "1");
  assert.equal(p.get("page"), null, "page is a UI concept, not an API parameter");
  assert.equal(new URLSearchParams(apiQuery({ page: 1 })).get("offset"), "0");
});

test("pageInfo", () => {
  assert.deepEqual(pageInfo(0, 1), { from: 0, to: 0, pages: 1, hasPrev: false, hasNext: false });
  assert.deepEqual(pageInfo(45, 1), { from: 1, to: 20, pages: 3, hasPrev: false, hasNext: true });
  assert.deepEqual(pageInfo(45, 3), { from: 41, to: 45, pages: 3, hasPrev: true, hasNext: false });
});

test("formatSalary", () => {
  assert.equal(formatSalary({ min: 2400000, max: 3600000, currency: "INR", period: "year" }), "₹24L–36L / yr");
  assert.equal(formatSalary({ min: 1200000, max: 1800000, currency: "INR", period: "year" }), "₹12L–18L / yr");
  assert.equal(formatSalary({ min: 15000000, max: 20000000, currency: "INR", period: "year" }), "₹1.5Cr–2Cr / yr");
  assert.equal(formatSalary({ min: 168000, max: 203000, currency: "USD", period: "year" }), "$168K–203K / yr");
  assert.equal(formatSalary({ min: 48000, max: 56000, currency: "EUR", period: "year" }), "€48K–56K / yr");
  assert.equal(formatSalary({ min: 40, max: 60, currency: "USD", period: "hour" }), "$40–60 / hr");
  assert.equal(formatSalary({ min: 40000, max: 60000, currency: "INR", period: "month" }), "₹40K–60K / mo");
  assert.equal(formatSalary({ min: 50000, max: 50000, currency: "SGD", period: "year" }), "SGD 50K / yr");
  assert.equal(formatSalary(null), "");
  assert.equal(formatSalary({ min: 0, max: 0, currency: "INR", period: "year" }), "");
});

test("formatExp", () => {
  assert.equal(formatExp(null, null), "");
  assert.equal(formatExp(5, null), "5+ yrs");
  assert.equal(formatExp(3, 5), "3–5 yrs");
  assert.equal(formatExp(4, 4), "4 yrs");
  assert.equal(formatExp(0, null), "Entry level");
  assert.equal(formatExp(0, 2), "0–2 yrs");
});

test("formatLocation", () => {
  assert.equal(formatLocation({ city: "Pune", region: "Maharashtra", location_count: 1 }), "Pune, Maharashtra");
  assert.equal(formatLocation({ city: "Pune", region: null, location_count: 3 }), "Pune +2 more");
  assert.equal(formatLocation({ city: null, location_raw: "Remote - India", location_count: 0 }), "Remote - India");
  assert.equal(formatLocation({ city: null, location_raw: "", country: "IN", location_count: 1 }), "IN");
  assert.equal(formatLocation({}), "");
});

test("timeAgo", () => {
  const now = Date.parse("2026-06-30T12:00:00Z");
  const ago = (days) => new Date(now - days * 86400000).toISOString();
  assert.equal(timeAgo(ago(0.2), now), "today");
  assert.equal(timeAgo(ago(1), now), "yesterday");
  assert.equal(timeAgo(ago(5), now), "5 days ago");
  assert.equal(timeAgo(ago(21), now), "3 weeks ago");
  assert.equal(timeAgo(ago(100), now), "3 months ago");
  assert.equal(timeAgo(null, now), "");
  assert.equal(timeAgo("garbage", now), "");
});

test("formatCount uses Indian digit grouping", () => {
  assert.equal(formatCount(1067), "1,067");
  assert.equal(formatCount(123456), "1,23,456");
});

test("splitHighlights turns markers into segments", () => {
  assert.deepEqual(splitHighlights("We use ⟦Go⟧ and ⟦Postgres⟧ daily"), [
    { text: "We use ", hit: false }, { text: "Go", hit: true }, { text: " and ", hit: false },
    { text: "Postgres", hit: true }, { text: " daily", hit: false },
  ]);
  assert.deepEqual(splitHighlights("no markers here"), [{ text: "no markers here", hit: false }]);
  assert.deepEqual(splitHighlights("⟦all⟧"), [{ text: "all", hit: true }]);
  assert.deepEqual(splitHighlights(""), []);
  assert.deepEqual(splitHighlights(null), []);
  // markup in the data stays plain text for the caller to insert via textContent
  assert.equal(splitHighlights("⟦<img src=x onerror=alert(1)>⟧")[0].text, "<img src=x onerror=alert(1)>");
});

test("entry_level travels through the URL and to the API", () => {
  const state = stateFromSearch("?entry_level=1&q=go");
  assert.deepEqual(state, { page: 1, q: "go", entry_level: "1" });
  assert.equal(searchFromState(state), "?q=go&entry_level=1");
  assert.equal(new URLSearchParams(apiQuery(state)).get("entry_level"), "1");
});

test("a quick view is pressed exactly when the filters it owns match", () => {
  assert.equal(activeView({ page: 1 }), "all");
  assert.equal(activeView({ page: 1, entry_level: "1" }), "fresher");
  assert.equal(activeView({ page: 1, seniority: "intern" }), "intern");
  assert.equal(activeView({ page: 1, remote: "remote" }), "remote");
  assert.equal(activeView({ page: 1, posted_within: "7" }), "new");
  // filters that no view owns do not unpress "all" or a view
  assert.equal(activeView({ page: 1, city: "Pune", q: "go", exp: "1" }), "all");
  assert.equal(activeView({ page: 1, entry_level: "1", city: "Pune" }), "fresher");
  // a combination, or a value no view uses, presses nothing
  assert.equal(activeView({ page: 1, entry_level: "1", remote: "remote" }), null);
  assert.equal(activeView({ page: 1, seniority: "senior" }), null);
  assert.equal(activeView({ page: 1, remote: "hybrid" }), null);
  assert.equal(activeView({ page: 1, posted_within: "30" }), null);
});

test("applyView swaps the view's filters and keeps the others", () => {
  const start = { page: 4, q: "go", city: "Pune", remote: "remote", posted_within: "7" };
  const fresher = applyView(start, "fresher");
  assert.deepEqual(fresher, { page: 1, q: "go", city: "Pune", entry_level: "1" });
  assert.equal(activeView(fresher), "fresher");
  assert.deepEqual(applyView(fresher, "intern"), { page: 1, q: "go", city: "Pune", seniority: "intern" });
  assert.deepEqual(applyView(fresher, "all"), { page: 1, q: "go", city: "Pune" });
  assert.deepEqual(start, { page: 4, q: "go", city: "Pune", remote: "remote", posted_within: "7" }, "input is not mutated");
  assert.ok(VIEWS.every((v) => applyView({ page: 1 }, v.id) && activeView(applyView({ page: 1 }, v.id)) === v.id));
});

test("removeFilter", () => {
  assert.deepEqual(removeFilter({ page: 3, q: "go", city: "Pune" }, "city"), { page: 1, q: "go" });
  assert.deepEqual(removeFilter({ page: 2, entry_level: "1", q: "go" }, "view"), { page: 1, q: "go" });
  assert.deepEqual(removeFilter({ page: 1, exp: "2", hide_unknown_exp: "1" }, "exp"), { page: 1 }, "exp takes its companion with it");
});

test("activeFilters lists removable chips", () => {
  assert.deepEqual(activeFilters({ page: 1 }), []);
  assert.deepEqual(activeFilters({ page: 1, entry_level: "1" }), [{ key: "view", text: "Fresher and entry level" }]);
  assert.deepEqual(
    activeFilters({ page: 1, q: "go backend", city: "Pune", company: "okta", exp: "1", hide_unknown_exp: "1", employment: "full_time", has_salary: "1" },
      (slug) => ({ okta: "Okta" })[slug]),
    [
      { key: "q", text: "“go backend”" },
      { key: "city", text: "Pune" },
      { key: "company", text: "Okta" },
      { key: "exp", text: "Up to 1 year, stated only" },
      { key: "employment", text: "Full-time" },
      { key: "has_salary", text: "Pay shown" },
    ]);
  assert.equal(activeFilters({ page: 1, exp: "3" })[0].text, "Up to 3 years");
  assert.equal(activeFilters({ page: 1, exp: "0" })[0].text, "Up to 0 years", "zero is a real choice, not 'unset'");
  // filters that are not a quick view show as their own chips
  assert.deepEqual(activeFilters({ page: 1, seniority: "senior", remote: "hybrid", posted_within: "30" }), [
    { key: "seniority", text: "Senior" }, { key: "remote", text: "Hybrid" }, { key: "posted_within", text: "Posted in 30 days" },
  ]);
  assert.deepEqual(activeFilters({ page: 1, posted_within: "1", entry_level: "1", remote: "remote" }).map((c) => c.key),
    ["entry_level", "remote", "posted_within"]);
});

test("role filter: URL, API query, labels and chip", () => {
  const state = stateFromSearch("?role=tech&entry_level=1");
  assert.deepEqual(state, { page: 1, entry_level: "1", role: "tech" });
  assert.equal(searchFromState(state), "?entry_level=1&role=tech");
  assert.equal(new URLSearchParams(apiQuery(state)).get("role"), "tech");
  assert.equal(roleLabel("tech"), "Software & AI roles");
  assert.equal(roleLabel("ai_data"), "AI, ML & data");
  assert.equal(roleLabel("infra_security"), "DevOps, cloud & security");
  assert.equal(roleLabel(null), "");
  assert.equal(roleLabel("nonsense"), "");
  assert.deepEqual(ROLES.map(([v]) => v), ["tech", "software", "ai_data", "infra_security", "qa"]);
  // a role is not a quick view: it combines with one, and keeps the view pressed
  assert.equal(activeView({ page: 1, role: "tech", entry_level: "1" }), "fresher");
  assert.deepEqual(activeFilters({ page: 1, role: "software", q: "go" }), [
    { key: "q", text: "“go”" }, { key: "role", text: "Software engineering" },
  ]);
  assert.deepEqual(removeFilter({ page: 2, role: "qa", city: "Pune" }, "role"), { page: 1, city: "Pune" });
  // applying a quick view keeps the chosen role
  assert.deepEqual(applyView({ page: 1, role: "tech" }, "fresher"), { page: 1, role: "tech", entry_level: "1" });
});
