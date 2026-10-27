// Functional scenario: the protections a customer never sees.
//
//   k6 run -e BASE_URL=http://127.0.0.1:8080 -e JUNIT=reports/antifraud.xml k6/functional/antifraud.js
import http from "k6/http";
import { sleep } from "k6";
import { BASE, step, field, clientIP, bakeryOrder, junit } from "../lib/shop.js";

export const options = {
  iterations: 1,
  thresholds: { checks: ["rate==1"] },
};

export default function () {
  const shop = http.get(BASE + "/");
  const headers = shop.headers;
  step("CSP forbids foreign scripts", headers["Content-Security-Policy"] || "", (h) => h.includes("script-src 'self'"));
  step("pages may not be framed", headers["X-Frame-Options"] || "", (h) => h === "DENY");

  const forged = http.post(BASE + "/order", { csrf: "forged", f_email: "x@example.ru" }, { redirects: 0 });
  step("forged CSRF token is refused", forged, (r) => r.status === 403);

  sleep(3.5);
  const bot = http.post(BASE + "/order", {
    csrf: field(shop.body, "csrf"), stamp: field(shop.body, "stamp"), website: "http://spam.example",
    item_baton: "2", item_croissant: "1", f_name: "Бот", f_phone: "+79000000000",
    f_email: `bot-${Date.now()}@example.ru`, f_pickup_at: "2026-10-28T10:00", method: "transfer",
  }, { redirects: 0, headers: { "X-Forwarded-For": clientIP() } });
  step("honeypot submit is refused without reasons", bot, (r) => r.status === 403 && !r.body.includes("скрытое"));

  const disposable = bakeryOrder(shop.body, `x${Date.now()}@mailinator.com`, clientIP());
  step("disposable mailbox is held or refused", disposable, (r) => r.status === 303 || r.status === 403);

  const fast = http.get(BASE + "/");
  const quick = bakeryOrder(fast.body, `fast-${Date.now()}@example.ru`, clientIP());
  step("instant submit is accepted", quick, (r) => r.status === 303);
  step("instant submit is held for review", http.get(BASE + quick.headers.Location),
    (r) => r.body.includes("на проверке у оператора"));

  const ip = clientIP();
  let last;
  for (let i = 0; i < 6; i++) {
    last = bakeryOrder(shop.body, `burst-${i}-${Date.now()}@example.ru`, ip);
  }
  step("burst from one address is stopped", last, (r) => r.status === 403 || r.status === 429);
}

export function handleSummary(data) {
  const out = { stdout: "" };
  if (__ENV.JUNIT) out[__ENV.JUNIT] = junit(data, "antifraud");
  return out;
}
