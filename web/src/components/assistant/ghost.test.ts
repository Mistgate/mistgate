import { expect, it } from "vitest";
import { suggest } from "./ghost";

it("finishes a started question from the pool, first match wins, case does not matter", () => {
  const pool = ["Why can alice not connect?", "Which nodes had problems today?", "Which nodes are not on the latest version?"];
  expect(suggest("whi", pool)).toBe("ch nodes had problems today?");
  expect(suggest("Which nodes a", pool)).toBe("re not on the latest version?");
  expect(suggest("W", pool)).toBe("");
  expect(suggest(" wh", pool)).toBe("");
  expect(suggest("why\n", pool)).toBe("");
  expect(suggest("Why can alice not connect?", pool)).toBe("");
  expect(suggest("how", pool)).toBe("");
});
