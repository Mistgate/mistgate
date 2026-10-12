// The little of Node this suite uses (the package has no @types/node; the tests of the other projects run in workerd).
declare module "node:fs" {
  export function readFileSync(path: string, encoding: "utf8"): string;
}
declare module "node:path" {
  export function join(...parts: string[]): string;
}
declare const process: { env: Record<string, string | undefined> };
