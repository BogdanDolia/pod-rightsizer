export class InFlightGate {
  private current = 0;

  constructor(readonly limit: number) {
    if (!Number.isSafeInteger(limit) || limit < 1) {
      throw new Error("in-flight limit must be a positive integer");
    }
  }

  tryEnter(): boolean {
    if (this.current >= this.limit) {
      return false;
    }
    this.current += 1;
    return true;
  }

  leave(): void {
    if (this.current > 0) {
      this.current -= 1;
    }
  }

  get inFlight(): number {
    return this.current;
  }
}
