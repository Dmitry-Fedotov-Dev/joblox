// Helpers shared by the functional scenarios and the load test.
import http from "k6/http";
import { check, fail } from "k6";

export const BASE = __ENV.BASE_URL || "http://127.0.0.1:8080";

// step is a named check that stops the scenario when it fails: the report shows the first broken step.
export function step(name, value, predicate) {
  const ok = check(value, { [name]: predicate });
  if (!ok) fail(name);
  return value;
}

export function field(html, name) {
  const m = html.match(new RegExp('name="' + name + '" value="([^"]+)"'));
  return m ? m[1] : "";
}

// A random documentation-range address per order: the server believes X-Forwarded-For only from
// JOBLOX_TRUSTED_PROXIES, which CI sets to the load generator's loopback.
export function clientIP() {
  return "198.51." + Math.floor(Math.random() * 100) + "." + (1 + Math.floor(Math.random() * 250));
}

export function bakeryOrder(page, email, ip) {
  return http.post(BASE + "/order", {
    csrf: field(page, "csrf"), stamp: field(page, "stamp"),
    item_baton: "2", item_croissant: "1",
    f_name: "Анна", f_phone: "+7 900 123-45-67", f_email: email, f_pickup_at: "2026-10-28T10:00",
    method: "transfer",
  }, { redirects: 0, headers: { "X-Forwarded-For": ip }, tags: { name: "POST /order" } });
}

// JUnit for Dmitry-Fedotov-Dev/ci k6-summary: every check is a testcase, every threshold too.
export function junit(data, suite) {
  const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
  const cases = [];
  const walk = (group) => {
    for (const c of group.checks || []) {
      cases.push(c.fails > 0
        ? `<testcase name="${esc(c.name)}"><failure message="${c.fails} of ${c.passes + c.fails} failed"/></testcase>`
        : `<testcase name="${esc(c.name)}"/>`);
    }
    for (const g of group.groups || []) walk(g);
  };
  walk(data.root_group);
  for (const [metric, m] of Object.entries(data.metrics)) {
    for (const [expr, t] of Object.entries(m.thresholds || {})) {
      const name = `threshold ${metric} ${expr}`;
      cases.push(t.ok ? `<testcase name="${esc(name)}"/>` : `<testcase name="${esc(name)}"><failure message="crossed"/></testcase>`);
    }
  }
  return `<?xml version="1.0" encoding="UTF-8"?>\n<testsuites><testsuite name="${esc(suite)}" tests="${cases.length}">` +
    cases.join("") + "</testsuite></testsuites>\n";
}
