import { PRODUCTS, CURRENCY, taxCents } from "./dataset.js";
import { MAX_QUOTE_ITEMS, MAX_QUOTE_QUANTITY } from "./limits.js";

export interface QuoteItem {
  sku: string;
  quantity: number;
}

export interface QuoteLine {
  sku: string;
  quantity: number;
  unitCents: number;
  lineCents: number;
}

export interface Quote {
  currency: typeof CURRENCY;
  lines: QuoteLine[];
  subtotalCents: number;
  taxCents: number;
  totalCents: number;
}

export class QuoteError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "QuoteError";
  }
}

export function quoteCart(items: readonly QuoteItem[]): Quote {
  if (items.length < 1 || items.length > MAX_QUOTE_ITEMS) {
    throw new QuoteError(`items must contain 1 to ${MAX_QUOTE_ITEMS} entries`);
  }
  const seen = new Set<string>();
  const lines: QuoteLine[] = [];
  let subtotalCents = 0;
  for (const item of items) {
    if (seen.has(item.sku)) {
      throw new QuoteError(`duplicate sku ${item.sku}`);
    }
    seen.add(item.sku);
    const unitCents = PRODUCTS.get(item.sku);
    if (unitCents === undefined) {
      throw new QuoteError(`unknown sku ${item.sku}`);
    }
    if (!Number.isSafeInteger(item.quantity) || item.quantity < 1 || item.quantity > MAX_QUOTE_QUANTITY) {
      throw new QuoteError(`quantity must be an integer from 1 to ${MAX_QUOTE_QUANTITY}`);
    }
    const lineCents = unitCents * item.quantity;
    subtotalCents += lineCents;
    lines.push({ sku: item.sku, quantity: item.quantity, unitCents, lineCents });
  }
  const tax = taxCents(subtotalCents);
  return {
    currency: CURRENCY,
    lines,
    subtotalCents,
    taxCents: tax,
    totalCents: subtotalCents + tax,
  };
}

export function parseQuoteBody(body: unknown): QuoteItem[] {
  if (body === null || typeof body !== "object" || Array.isArray(body)) {
    throw new QuoteError("body must be an object with items");
  }
  const record = body as Record<string, unknown>;
  const keys = Object.keys(record);
  if (keys.length !== 1 || keys[0] !== "items") {
    throw new QuoteError("body must contain only items");
  }
  const items = record.items;
  if (!Array.isArray(items)) {
    throw new QuoteError("items must be an array");
  }
  return items.map((item) => {
    if (item === null || typeof item !== "object" || Array.isArray(item)) {
      throw new QuoteError("each item must be an object");
    }
    const entry = item as Record<string, unknown>;
    const itemKeys = Object.keys(entry);
    if (itemKeys.length !== 2 || !itemKeys.includes("sku") || !itemKeys.includes("quantity")) {
      throw new QuoteError("each item must contain only sku and quantity");
    }
    if (typeof entry.sku !== "string" || !/^[a-z][a-z0-9-]{0,31}$/.test(entry.sku)) {
      throw new QuoteError("sku must be a short lowercase identifier");
    }
    if (typeof entry.quantity !== "number" || !Number.isInteger(entry.quantity)) {
      throw new QuoteError("quantity must be an integer");
    }
    return { sku: entry.sku, quantity: entry.quantity };
  });
}
