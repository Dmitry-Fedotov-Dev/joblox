// Load test: visitors browse the storefront and look at order pages, some of them order.
// The thresholds decide the outcome; --summary-export feeds the CI summary.
//
//   k6 run -e BASE_URL=http://127.0.0.1:8080 -e RATE=60 -e DURATION=1m --summary-export reports/summary.json k6/load.js
import http from "k6/http";
import { check } from "k6";
import { BASE, clientIP, bakeryOrder } from "./lib/shop.js";

const RATE = Number(__ENV.RATE || 60); // visits per second at the plateau
const DURATION = __ENV.DURATION || "1m";
const ORDER_SHARE = Number(__ENV.ORDER_SHARE || 0.1);

export const options = {
  scenarios: {
    visitors: {
      executor: "ramping-arrival-rate",
      startRate: 1,
      timeUnit: "1s",
      preAllocatedVUs: 50,
      maxVUs: 300,
      stages: [
        { target: RATE, duration: "15s" },
        { target: RATE, duration: DURATION },
        { target: 0, duration: "5s" },
      ],
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    "http_req_duration{name:GET /}": ["p(95)<300"],
    "http_req_duration{name:POST /order}": ["p(95)<800"],
    "http_req_duration{name:GET /o/{token}}": ["p(95)<300"],
    checks: ["rate>0.99"],
  },
};

const links = [];

export default function () {
  const ip = clientIP();
  const shop = http.get(BASE + "/", { headers: { "X-Forwarded-For": ip }, tags: { name: "GET /" } });
  check(shop, { "storefront 200": (r) => r.status === 200 });
  if (Math.random() < ORDER_SHARE) {
    const r = bakeryOrder(shop.body, `load-${__VU}-${__ITER}-${Date.now()}@example.ru`, ip);
    if (check(r, { "order created": (x) => x.status === 303 })) {
      links.push(r.headers.Location);
      if (links.length > 50) links.shift();
    }
  } else if (links.length > 0) {
    const link = links[Math.floor(Math.random() * links.length)];
    const page = http.get(BASE + link, { headers: { "X-Forwarded-For": ip }, tags: { name: "GET /o/{token}" } });
    check(page, { "order page 200": (r) => r.status === 200 });
  }
}
