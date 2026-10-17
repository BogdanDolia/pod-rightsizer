import express from "express";
import { createServer } from "node:http";

const app = express();
app.get("/load-phases", (_req, res) => {
  res.status(200).json({ measuredOpen: true, warmupInFlight: 0 });
});

app.post("/load-phases/open-measured", (_req, res) => {
  res.status(200).json({ measuredOpen: true, warmupInFlight: 0 });
});

app.post("/v1/quote", (_req, res) => {
  res.status(200).json({
    currency: "USD",
    lines: [
      { sku: "fixture-a", quantity: 1, unitCents: 1, lineCents: 1 },
      { sku: "fixture-b", quantity: 1, unitCents: 1, lineCents: 1 },
      { sku: "fixture-c", quantity: 1, unitCents: 1, lineCents: 1 },
    ],
    subtotalCents: 1057,
    taxCents: 85,
    totalCents: 1,
    regression: { mode: "baseline", intensity: 0 },
  });
});

const port = Number(process.env.PORT ?? "0");
const server = createServer(app);
server.listen(port, "127.0.0.1", () => {
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("expected a TCP port");
  }
  console.log(`wrong-answer-stub listening port=${address.port}`);
});
