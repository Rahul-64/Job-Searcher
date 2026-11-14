import { test } from "node:test";
import assert from "node:assert/strict";
import {
  label, safeHref, stateFromSearch, searchFromState, apiQuery, pageInfo,
  formatSalary, formatExp, formatLocation, timeAgo, formatCount, splitHighlights,
} from "./lib.mjs";

test("label", () => {
  assert.equal(label("full_time"), "Full-time");
  assert.equal(label("onsite"), "On-site");
  assert.equal(label("senior"), "Senior");
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
