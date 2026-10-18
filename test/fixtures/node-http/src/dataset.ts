export const CURRENCY = "USD" as const;
export const TAX_BASIS_POINTS = 800;

export const PRODUCTS: ReadonlyMap<string, number> = new Map([
  ["apple", 125],
  ["bread", 240],
  ["milk", 189],
  ["cheese", 640],
]);

export function taxCents(subtotalCents: number): number {
  return Math.floor((subtotalCents * TAX_BASIS_POINTS + 5_000) / 10_000);
}
