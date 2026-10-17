// scenarioId: node-http-quote
// scenarioVersion: 1
//
// Latency uses k6 http_req_duration: sending + waiting + receiving the body.
// That is not the legacy pod-rightsizer builtin-get measurement, which stops
// when response headers arrive and before the body is read.
//
// dropped_iterations counts arrival-rate iterations the runner could not start.
// A non-zero count means the planned load did not run. It is not a performance proof.

import http from "k6/http";
import { check } from "k6";

const input = JSON.parse(open("./quote-input.json"));
const expected = JSON.parse(open("./expected.json"));
const mode = __ENV.EXPECTED_MODE || "baseline";
const intensity = Number(__ENV.EXPECTED_INTENSITY || "0");

export const options = {
  scenarios: {
    warmup: {
      executor: "constant-arrival-rate",
      rate: 5,
      timeUnit: "1s",
      duration: "3s",
      preAllocatedVUs: 4,
      maxVUs: 10,
      exec: "quote",
      tags: { phase: "warmup" },
    },
    measured: {
      executor: "constant-arrival-rate",
      rate: 5,
      timeUnit: "1s",
      duration: "10s",
      preAllocatedVUs: 4,
      maxVUs: 10,
      startTime: "3s",
      exec: "quote",
      tags: { phase: "measured" },
    },
  },
  thresholds: {
    "checks{scenario:measured}": ["rate==1"],
    "http_req_failed{scenario:measured}": ["rate==0"],
    "dropped_iterations{scenario:measured}": ["count==0"],
    "http_req_duration{scenario:measured}": ["p(95)<2000"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(95)", "p(99)", "max", "count"],
};

export function quote() {
  const response = http.post(`${__ENV.BASE_URL}/v1/quote`, JSON.stringify(input), {
    headers: { "Content-Type": "application/json" },
    timeout: "2s",
    tags: { name: "POST /v1/quote" },
  });
  let body = null;
  try {
    body = response.json();
  } catch (_error) {
    body = null;
  }
  check(response, {
    "status is 200": (result) => result.status === 200,
    "currency is USD": () => body && body.currency === expected.currency,
    "line count matches": () => body && Array.isArray(body.lines) && body.lines.length === expected.lineCount,
    "subtotal matches": () => body && body.subtotalCents === expected.subtotalCents,
    "tax matches": () => body && body.taxCents === expected.taxCents,
    "total matches": () => body && body.totalCents === expected.totalCents,
    "mode matches": () => body && body.regression && body.regression.mode === mode,
    "intensity matches": () => body && body.regression && body.regression.intensity === intensity,
  });
}
