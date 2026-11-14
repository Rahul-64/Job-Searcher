// Pure helpers for the dashboard. No DOM access here, so they can be unit-tested
// with `node --test public` (see lib.test.js).

export const PAGE_SIZE = 20;

const LABELS = {
  full_time: "Full-time",
  part_time: "Part-time",
  contract: "Contract",
  intern: "Intern",
  temporary: "Temporary",
  remote: "Remote",
  hybrid: "Hybrid",
  onsite: "On-site",
};

/** "full_time" -> "Full-time"; unknown values are capitalised. */
export function label(value) {
  if (!value) return "";
  if (LABELS[value]) return LABELS[value];
  return value.charAt(0).toUpperCase() + value.slice(1);
}

/** Only http(s) links may be followed: job data comes from third-party sites. */
export function safeHref(url) {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : "";
  } catch {
    return "";
  }
}

/** Filter state <-> URL query string. Keys match the API parameters. */
const FILTER_KEYS = [
  "q", "city", "company", "remote", "seniority", "employment",
  "exp", "hide_unknown_exp", "posted_within", "has_salary", "sort",
];

export function stateFromSearch(search) {
  const p = new URLSearchParams(search);
  const state = { page: 1 };
  for (const k of FILTER_KEYS) {
    const v = (p.get(k) || "").trim();
    if (v) state[k] = v;
  }
  const page = parseInt(p.get("page") || "1", 10);
  if (Number.isInteger(page) && page >= 1 && page <= 250) state.page = page;
  return state;
}

/** Query string for the browser URL (omits defaults to keep links short). */
export function searchFromState(state) {
  const p = new URLSearchParams();
  for (const k of FILTER_KEYS) if (state[k]) p.set(k, state[k]);
  if (state.page > 1) p.set("page", String(state.page));
  const s = p.toString();
  return s ? "?" + s : "";
}

/** Query string for GET /api/jobs. */
export function apiQuery(state, pageSize = PAGE_SIZE) {
  const p = new URLSearchParams();
  for (const k of FILTER_KEYS) if (state[k]) p.set(k, state[k]);
  p.set("limit", String(pageSize));
  p.set("offset", String(((state.page || 1) - 1) * pageSize));
  return p.toString();
}

export function pageInfo(total, page, pageSize = PAGE_SIZE) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(total, page * pageSize);
  return { from, to, pages, hasPrev: page > 1, hasNext: page < pages };
}

const CURRENCY_SYMBOL = { INR: "₹", USD: "$", EUR: "€", GBP: "£" };

function compact(n, currency) {
  if (currency === "INR") {
    if (n >= 1e7) return `${trim(n / 1e7)}Cr`;
    if (n >= 1e5) return `${trim(n / 1e5)}L`;
  }
  if (n >= 1e6) return `${trim(n / 1e6)}M`;
  if (n >= 1e3) return `${trim(n / 1e3)}K`;
  return String(Math.round(n));
}

function trim(x) {
  return String(Math.round(x * 10) / 10);
}

/** {min,max,currency,period} -> "₹24L–36L / yr". */
export function formatSalary(s) {
  if (!s || !(s.max > 0)) return "";
  const sym = CURRENCY_SYMBOL[s.currency] || s.currency + " ";
  const lo = compact(s.min, s.currency);
  const hi = compact(s.max, s.currency);
  const range = lo === hi ? `${sym}${hi}` : `${sym}${lo}–${hi}`;
  const per = { year: "yr", month: "mo", hour: "hr" }[s.period];
  return per ? `${range} / ${per}` : range;
}

/** Years of experience asked for. */
export function formatExp(min, max) {
  if (min == null) return "";
  if (max != null) return min === max ? `${min} yrs` : `${min}–${max} yrs`;
  return min === 0 ? "Entry level" : `${min}+ yrs`;
}

/** "Bengaluru, Karnataka" (+2 more); falls back to the source's own text. */
export function formatLocation(job) {
  let text = "";
  if (job.city) text = job.region ? `${job.city}, ${job.region}` : job.city;
  else if (job.location_raw) text = job.location_raw;
  else if (job.country) text = job.country;
  if (job.location_count > 1) text += ` +${job.location_count - 1} more`;
  return text;
}

/** Relative age: "today", "3 days ago", "2 weeks ago". */
export function timeAgo(iso, now = Date.now()) {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const days = Math.floor((now - t) / 86400000);
  if (days < 1) return "today";
  if (days === 1) return "yesterday";
  if (days < 14) return `${days} days ago`;
  if (days < 60) return `${Math.floor(days / 7)} weeks ago`;
  return `${Math.floor(days / 30)} months ago`;
}

export function formatCount(n) {
  return new Intl.NumberFormat("en-IN").format(n);
}

/**
 * Splits a snippet containing ⟦matched⟧ markers into [{text, hit}] segments.
 * The caller renders them with text nodes and <mark>, never as HTML.
 */
export function splitHighlights(snippet) {
  const out = [];
  if (!snippet) return out;
  const re = /[⟦⟧]/g;
  let inside = false;
  let last = 0;
  let m;
  while ((m = re.exec(snippet)) !== null) {
    const chunk = snippet.slice(last, m.index);
    if (chunk) out.push({ text: chunk, hit: inside });
    inside = m[0] === "⟦";
    last = m.index + 1;
  }
  const tail = snippet.slice(last);
  if (tail) out.push({ text: tail, hit: inside });
  return out;
}
