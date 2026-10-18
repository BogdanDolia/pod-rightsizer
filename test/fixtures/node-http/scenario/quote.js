// scenarioId: node-http-quote
// scenarioVersion: 2
//
// Latency uses k6 http_req_duration: sending + waiting + receiving the body.
// That is not the legacy pod-rightsizer builtin-get measurement, which stops
// when response headers arrive and before the body is read.
//
// dropped_iterations counts arrival-rate iterations the runner could not start.
// A non-zero count means the planned load did not run. It is not a performance proof.
//
// Configured schedule, not observed phase boundaries:
// - warmup issues iterations for 3s and keeps in-flight iterations for up to 2s
// - drain starts when warmup stops issuing iterations and polls until the server
//   reports that warmup work has finished, for at most 2s
// - measured starts at 6s, after that drain budget and after warmup's gracefulStop
//   deadline, then issues iterations for 10s
// k6 1.8.2 defaults gracefulStop to 30s (lib/executor/base_config.go). Leaving it
// unset lets warmup HTTP work continue after measured has started. gracefulStop
// 0s interrupts the client and does not finish server work. The 2s budget matches
// the quote HTTP timeout and is longer than the fixture latency cap of 320ms.
// Observed boundaries come from the server's /load-phases ledger.

import http from "k6/http";
import { check, sleep } from "k6";

const input = JSON.parse(open("./quote-input.json"));
const expected = JSON.parse(open("./expected.json"));
const mode = __ENV.EXPECTED_MODE || "baseline";
const intensity = Number(__ENV.EXPECTED_INTENSITY || "0");
const drainBudgetMs = 2000;

export const options = {
  scenarios: {
    warmup: {
      executor: "constant-arrival-rate",
      rate: 5,
      timeUnit: "1s",
      duration: "3s",
      gracefulStop: "2s",
      preAllocatedVUs: 4,
      maxVUs: 10,
      exec: "warmupQuote",
      tags: { phase: "warmup" },
    },
    drain: {
      executor: "shared-iterations",
      vus: 1,
      iterations: 1,
      startTime: "3s",
      maxDuration: "3s",
      gracefulStop: "1s",
      exec: "drainWarmup",
    },
    measured: {
      executor: "constant-arrival-rate",
      rate: 5,
      timeUnit: "1s",
      duration: "10s",
      startTime: "6s",
      gracefulStop: "2s",
      preAllocatedVUs: 4,
      maxVUs: 10,
      exec: "measuredQuote",
      tags: { phase: "measured" },
    },
  },
  thresholds: {
    "checks{scenario:drain}": ["rate==1"],
    "checks{scenario:measured}": ["rate==1"],
    "http_req_failed{scenario:measured}": ["rate==0"],
    "dropped_iterations{scenario:measured}": ["count==0"],
    "http_req_duration{scenario:measured}": ["p(95)<2000"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(95)", "p(99)", "max", "count"],
};

let measuredReady = false;

function phaseOpen(response) {
  try {
    const body = response.json();
    return Boolean(body && body.measuredOpen === true && body.warmupInFlight === 0);
  } catch (_error) {
    return false;
  }
}

export function drainWarmup() {
  const started = Date.now();
  let opened = false;
  while (Date.now() - started < drainBudgetMs) {
    const response = http.post(`${__ENV.BASE_URL}/load-phases/open-measured`, null, {
      headers: { "Content-Type": "application/json" },
      timeout: "1s",
      tags: { name: "POST /load-phases/open-measured" },
    });
    if (response.status === 200 && phaseOpen(response)) {
      opened = true;
      break;
    }
    sleep(0.05);
  }
  check({ opened }, {
    "warmup work finished before measured requests": (state) => state.opened === true,
  });
}

export function warmupQuote() {
  quote("warmup");
}

export function measuredQuote() {
  if (!measuredReady) {
    const gate = http.get(`${__ENV.BASE_URL}/load-phases`, {
      timeout: "1s",
      tags: { name: "GET /load-phases" },
    });
    const open = gate.status === 200 && phaseOpen(gate);
    check(gate, {
      "measured opens only after warmup work": () => open,
    });
    if (!open) {
      return;
    }
    measuredReady = true;
  }
  quote("measured");
}

function quote(phase) {
  const response = http.post(`${__ENV.BASE_URL}/v1/quote`, JSON.stringify(input), {
    headers: { "Content-Type": "application/json", "X-Load-Phase": phase },
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
