import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const siteRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const dist = join(siteRoot, "dist");

function page(lang, key) {
  const prefix = lang === "en" ? "" : `${lang}/`;
  const path = key === "index" ? `${prefix}index.html` : `${prefix}${key}/index.html`;
  return readFileSync(join(dist, path), "utf8");
}

test("M2 roadmap pages are built in both languages", () => {
  const en = page("en", "roadmap/m2-ssh-provisioning");
  const ru = page("ru", "roadmap/m2-ssh-provisioning");

  assert.ok(en.includes("M2: SSH provisioning and recovery"));
  assert.ok(en.includes("M2 implementation has not started"));
  assert.ok(ru.includes("M2: установка по SSH и восстановление"));
  assert.ok(ru.includes("Реализация M2 ещё не начата"));
});

test("M2 roadmap pages appear in both documentation sidebars", () => {
  assert.ok(page("en", "index").includes("/roadmap/m2-ssh-provisioning/"));
  assert.ok(page("ru", "index").includes("/ru/roadmap/m2-ssh-provisioning/"));
});

test("M2 pages link to the matching add-node guide and GitHub source", () => {
  const en = page("en", "roadmap/m2-ssh-provisioning");
  const ru = page("ru", "roadmap/m2-ssh-provisioning");

  assert.ok(en.includes('href="/getting-started/add-node/"'));
  assert.ok(ru.includes('href="/ru/getting-started/add-node/"'));
  assert.ok(en.includes("github.com/Mistgate/mistgate/edit/main/docs/en/roadmap/m2-ssh-provisioning.md"));
  assert.ok(ru.includes("github.com/Mistgate/mistgate/edit/main/docs/ru/roadmap/m2-ssh-provisioning.md"));
});
