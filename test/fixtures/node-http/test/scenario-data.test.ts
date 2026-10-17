import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { quoteCart } from "../src/quote.js";
import { fixtureRoot } from "./paths.js";

test("scenario inputs match the cart quote", () => {
  const input = JSON.parse(readFileSync(join(fixtureRoot, "scenario/quote-input.json"), "utf8")) as {
    items: { sku: string; quantity: number }[];
  };
  const expected = JSON.parse(readFileSync(join(fixtureRoot, "scenario/expected.json"), "utf8")) as {
    scenarioId: string;
    scenarioVersion: string;
    currency: string;
    subtotalCents: number;
    taxCents: number;
    totalCents: number;
    lineCount: number;
  };
  const quote = quoteCart(input.items);
  assert.equal(expected.scenarioId, "node-http-quote");
  assert.equal(expected.scenarioVersion, "1");
  assert.equal(quote.currency, expected.currency);
  assert.equal(quote.lines.length, expected.lineCount);
  assert.equal(quote.subtotalCents, expected.subtotalCents);
  assert.equal(quote.taxCents, expected.taxCents);
  assert.equal(quote.totalCents, expected.totalCents);
  assert.equal(quote.totalCents, 1142);
});
