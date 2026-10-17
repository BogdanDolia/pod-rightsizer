import express from "express";
import { createServer } from "node:http";

const app = express();
app.post("/v1/quote", (_req, res) => {
  res.status(200).json({
    currency: "USD",
    lines: [],
    subtotalCents: 1,
    taxCents: 1,
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
