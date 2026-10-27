// Functional scenario: the customer's whole path in the bakery profile against a running server
// (JOBLOX_ENV=dev, so the email code is read from /dev/outbox).
//
//   k6 run -e BASE_URL=http://127.0.0.1:8080 -e JUNIT=reports/checkout.xml k6/functional/checkout.js
import http from "k6/http";
import { sleep } from "k6";
import { BASE, step, field, clientIP, bakeryOrder, junit } from "../lib/shop.js";

export const options = {
  iterations: 1,
  thresholds: { checks: ["rate==1"] },
};

export default function () {
  const shop = step("storefront opens", http.get(BASE + "/"), (r) => r.status === 200 && r.body.includes("Батон"));
  step("requisites are not on the public page", shop.body, (b) => !b.includes("Расчётный счёт"));
  sleep(3.5); // the anti-fraud holds forms filled faster than a person can

  const email = `k6-${Date.now()}@example.ru`;
  const created = step("order is created", bakeryOrder(shop.body, email, clientIP()), (r) => r.status === 303);
  const link = created.headers.Location;
  const page = step("order page asks for the code", http.get(BASE + link),
    (r) => r.status === 200 && r.body.includes("Подтвердите email") && !r.body.includes("Назначение платежа"));

  const outbox = http.get(BASE + "/dev/outbox").json();
  const letter = outbox.find((m) => m.to === email);
  const code = step("code letter is sent", letter && (letter.subject.match(/(\d{6})/) || [])[1], (c) => !!c);

  const wrong = http.post(BASE + link + "/code", { csrf: field(page.body, "csrf"), code: "000000" }, { redirects: 0 });
  step("wrong code is refused", wrong, (r) => r.status === 422);
  const right = http.post(BASE + link + "/code", { csrf: field(page.body, "csrf"), code: code }, { redirects: 0 });
  step("right code is accepted", right, (r) => r.status === 303);

  const paid = step("requisites appear after the code", http.get(BASE + link),
    (r) => r.body.includes("Назначение платежа") && /Оплата заказа KL-\d+/.test(r.body));
  const state = step("status endpoint answers", http.get(BASE + link + "/state.json"), (r) => r.json("state") === "new");

  const cancel = http.post(BASE + link + "/t/cancel", { csrf: field(paid.body, "csrf") }, { redirects: 0 });
  step("customer can cancel", cancel, (r) => r.status === 303);
  step("cancelled order hides requisites", http.get(BASE + link),
    (r) => r.body.includes("Заказ отменён") && !r.body.includes("Назначение платежа"));
  void state;
}

export function handleSummary(data) {
  const out = { stdout: "" };
  if (__ENV.JUNIT) out[__ENV.JUNIT] = junit(data, "checkout");
  return out;
}
