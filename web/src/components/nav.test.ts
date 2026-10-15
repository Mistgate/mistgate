import { describe, expect, it } from "vitest";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { router } from "@/router";
import { navPaths } from "./ui/icons";
import { navKey, sections } from "./nav";

describe("the Reachability section", () => {
  it("sits right after Health in the menu, with its own icon and name in every language", () => {
    const ids = sections.map((s) => s.id);
    expect(ids.indexOf("reachability")).toBe(ids.indexOf("health") + 1);
    expect(sections.find((s) => s.id === "reachability")?.to).toBe("/reachability");
    expect(navPaths.reachability[0]).not.toBe(navPaths.health[0]);
    expect([en, ru, zh].map((d) => d[navKey("reachability")])).toEqual(["Reachability", "Доступность", "可达性"]);
  });

  it("is in the phone's More sheet, which holds every section after the first three", () => {
    expect(sections.slice(3).map((s) => s.id)).toContain("reachability");
  });

  it("has a route of its own, not the placeholder of an unbuilt section", () => {
    const route = (router.routesByPath as Record<string, { options: { component?: unknown } }>)["/reachability"];
    expect(route).toBeDefined();
    expect(String(route!.options.component)).not.toContain("SectionScreen");
  });
});
