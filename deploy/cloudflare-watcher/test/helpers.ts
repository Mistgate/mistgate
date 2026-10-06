import type { KVBinding } from "../src/env";

export class MemoryKV implements KVBinding {
  readonly values = new Map<string, string>();
  readonly writes: Array<{ key: string; value: string }> = [];
  readonly deletes: string[] = [];
  reads = 0;

  async get(key: string): Promise<string | null> {
    this.reads += 1;
    return this.values.get(key) ?? null;
  }

  async put(key: string, value: string): Promise<void> {
    this.writes.push({ key, value });
    this.values.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.deletes.push(key);
    this.values.delete(key);
  }
}
