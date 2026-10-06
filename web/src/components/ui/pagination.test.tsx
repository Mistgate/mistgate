import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { clampPage, pageCount, pageItems, rangeOf, useCursorPaging, usePaging, validatePaging } from "@/lib/paging";
import { memoryRouter } from "@/test/router";
import { CursorPagination, Pagination, type Cursor } from "./pagination";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(ui));
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const byLabel = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
const text = () => host?.textContent ?? "";

describe("page maths", () => {
  it("counts pages, never fewer than one, and clamps a page the URL got wrong", () => {
    expect(pageCount(0, 20)).toBe(1);
    expect(pageCount(20, 20)).toBe(1);
    expect(pageCount(21, 20)).toBe(2);
    expect(pageCount(137, 20)).toBe(7);
    expect(clampPage(99, 137, 20)).toBe(7);
    expect(clampPage(0, 137, 20)).toBe(1);
    expect(clampPage(-3, 137, 20)).toBe(1);
    expect(clampPage(3, 0, 20)).toBe(1);
  });

  it("says which rows a page holds", () => {
    expect(rangeOf(1, 20, 137)).toEqual({ from: 1, to: 20 });
    expect(rangeOf(7, 20, 137)).toEqual({ from: 121, to: 137 });
    expect(rangeOf(1, 20, 0)).toEqual({ from: 0, to: 0 });
  });

  it("shows every page up to seven, then always seven slots with the ends and the neighbours of the current page", () => {
    expect(pageItems(1, 1)).toEqual([1]);
    expect(pageItems(3, 7)).toEqual([1, 2, 3, 4, 5, 6, 7]);
    expect(pageItems(1, 20)).toEqual([1, 2, 3, 4, 5, "gap", 20]);
    expect(pageItems(4, 20)).toEqual([1, 2, 3, 4, 5, "gap", 20]);
    expect(pageItems(5, 20)).toEqual([1, "gap", 4, 5, 6, "gap", 20]);
    expect(pageItems(10, 20)).toEqual([1, "gap", 9, 10, 11, "gap", 20]);
    expect(pageItems(17, 20)).toEqual([1, "gap", 16, 17, 18, 19, 20]);
    expect(pageItems(20, 20)).toEqual([1, "gap", 16, 17, 18, 19, 20]);
    for (let p = 1; p <= 20; p++) expect(pageItems(p, 20)).toHaveLength(7);
  });

  it("keeps only well-formed paging params in the URL", () => {
    expect(validatePaging({ page: 3, size: 50, before: 900 })).toEqual({ page: 3, size: 50, before: 900 });
    expect(validatePaging({ page: 1 })).toEqual({}); // the first page is the bare URL
    expect(validatePaging({ page: "3", size: 0, before: -5 })).toEqual({});
    expect(validatePaging({ page: 2.5, size: 1000 })).toEqual({});
  });
});

describe("Pagination", () => {
  const sizes = [20, 50, 100] as const;
  const props = { size: 20, sizes, onSize: () => {} };

  it("is a labelled landmark that says what is on show and marks the current page", () => {
    mount(<Pagination total={137} page={3} onPage={() => {}} {...props} />);
    const nav = document.querySelector("nav")!;
    expect(nav.getAttribute("aria-label")).toBe("Pages");
    expect(text()).toContain("Showing 41–60 of 137");
    expect(nav.querySelector("[aria-live=polite]")?.textContent).toContain("41–60");
    const current = nav.querySelectorAll("[aria-current=page]");
    expect(current).toHaveLength(1);
    expect(current[0]!.textContent).toBe("3");
    expect(current[0]!.getAttribute("aria-label")).toBe("Page 3");
  });

  it("shows seven slots with an ellipsis, and a compact 'n of N' for the phone", () => {
    mount(<Pagination total={400} page={10} onPage={() => {}} {...props} />);
    const items = [...document.querySelectorAll("ul > li")].map((li) => li.textContent);
    expect(items).toEqual(["", "1", "…", "9", "10", "11", "…", "20", ""]); // prev, 1 … 9 10 11 … 20, next
    expect(text()).toContain("10 of 20");
  });

  it("disables back on the first page and next on the last", () => {
    mount(<Pagination total={137} page={1} onPage={() => {}} {...props} />);
    expect(byLabel("Previous page")!.disabled).toBe(true);
    expect(byLabel("Next page")!.disabled).toBe(false);
    act(() => root!.render(<Pagination total={137} page={7} onPage={() => {}} {...props} />));
    expect(byLabel("Previous page")!.disabled).toBe(false);
    expect(byLabel("Next page")!.disabled).toBe(true);
  });

  it("goes to the page that was clicked, one step either way", async () => {
    const onPage = vi.fn();
    mount(<Pagination total={137} page={4} onPage={onPage} {...props} />);
    await click(byLabel("Page 6"));
    expect(onPage).toHaveBeenLastCalledWith(6);
    await click(byLabel("Previous page"));
    expect(onPage).toHaveBeenLastCalledWith(3);
    await click(byLabel("Next page"));
    expect(onPage).toHaveBeenLastCalledWith(5);
  });

  it("offers the page size only when it has sizes, and shows no pager for a list that fits the smallest", () => {
    mount(<Pagination total={137} page={1} onPage={() => {}} {...props} />);
    expect(document.querySelector("button[aria-label='Rows per page']")).not.toBeNull();
    act(() => root!.render(<Pagination total={137} page={1} onPage={() => {}} size={10} />));
    expect(document.querySelector("button[aria-label='Rows per page']")).toBeNull();
    act(() => root!.render(<Pagination total={20} page={1} onPage={() => {}} {...props} />));
    expect(document.querySelector("nav")).toBeNull();
    act(() => root!.render(<Pagination total={10} page={1} onPage={() => {}} size={10} />));
    expect(document.querySelector("nav")).toBeNull();
  });

  it("keeps the size picker but no page buttons when everything fits one page of a larger size", () => {
    mount(<Pagination total={60} page={1} onPage={() => {}} size={100} sizes={sizes} onSize={() => {}} />);
    expect(text()).toContain("Showing 1–60 of 60");
    expect(document.querySelector("ul")).toBeNull();
    expect(document.querySelector("button[aria-label='Rows per page']")).not.toBeNull();
  });

  it("marks itself busy while the next page loads", () => {
    mount(<Pagination total={137} page={2} onPage={() => {}} busy {...props} />);
    expect(document.querySelector("nav")!.getAttribute("aria-busy")).toBe("true");
  });
});

describe("CursorPagination", () => {
  const cursor = (over: Partial<Cursor> = {}): Cursor => ({ before: 0, count: 50, hasOlder: true, hasNewer: false, toNewest: false, index: 1, onOlder: () => {}, onNewer: () => {}, ...over });

  it("is Newer / Older with no total: Older is open and Newer is not on the first page", () => {
    mount(<CursorPagination size={50} cursor={cursor()} />);
    expect(text()).toContain("Entries 1–50");
    expect(byLabel("Newer entries")!.disabled).toBe(true);
    expect(byLabel("Older entries")!.disabled).toBe(false);
  });

  it("numbers the page it knows and the rows on it", () => {
    mount(<CursorPagination size={50} cursor={cursor({ index: 3, hasNewer: true, count: 50, before: 700 })} />);
    expect(text()).toContain("Entries 101–150");
    expect(text()).toContain("Page 3");
  });

  it("says only how many rows it holds when the page came from a link, and Newer becomes 'To newest'", () => {
    const onNewer = vi.fn();
    mount(<CursorPagination size={50} cursor={cursor({ index: 0, hasNewer: true, toNewest: true, count: 37, hasOlder: false, before: 700, onNewer })} />);
    expect(text()).toContain("37 entries");
    expect(text()).not.toContain("Page ");
    expect(byLabel("Back to the newest entries")!.textContent).toBe("To newest");
    expect(byLabel("Older entries")!.disabled).toBe(true);
    act(() => byLabel("Back to the newest entries")!.click());
    expect(onNewer).toHaveBeenCalledTimes(1);
  });

  it("shows nothing for a log that fits one page", () => {
    mount(<CursorPagination size={50} cursor={cursor({ hasOlder: false })} />);
    expect(document.querySelector("nav")).toBeNull();
  });
});

// the position of a list lives in the URL: a real router on a memory history
function Probe({ sizes = [20, 50, 100] as const }: { sizes?: readonly number[] }) {
  const paging = usePaging({ sizes, defaultSize: 20 });
  return (
    <div>
      <output data-testid="state">{`${paging.page}/${paging.size}`}</output>
      <button onClick={() => paging.setPage(paging.page + 1)}>next</button>
      <button onClick={() => paging.setPage(1)}>first</button>
      <button onClick={() => paging.setSize(50)}>size50</button>
      <button onClick={() => paging.setSize(20)}>size20</button>
    </div>
  );
}
const search = (r: ReturnType<typeof memoryRouter>["router"]) => r.state.location.search as Record<string, unknown>;
const label = (name: string) => [...document.querySelectorAll("button")].find((b) => b.textContent === name);
const state = () => document.querySelector("[data-testid=state]")?.textContent;

describe("usePaging (the page in the URL)", () => {
  async function start(url: string, ui = <Probe />) {
    const r = memoryRouter(ui, url);
    mount(r.element);
    await settle();
    await settle();
    return r.router;
  }

  it("reads the page and the size from the URL, so a reload or a link lands on the same page", async () => {
    await start("/?page=4&size=50");
    expect(state()).toBe("4/50");
  });

  it("falls back to the defaults for a size that is not on offer", async () => {
    await start("/?size=7");
    expect(state()).toBe("1/20");
  });

  it("writes the page to the URL, and drops it for the first page, keeping the rest of the query", async () => {
    const router = await start("/?tab=alerts");
    await click(label("next"));
    await settle();
    expect(search(router)).toMatchObject({ tab: "alerts", page: 2 });
    expect(state()).toBe("2/20");
    await click(label("first"));
    await settle();
    expect(search(router)).toEqual({ tab: "alerts" });
  });

  it("a new size starts from the first page and is dropped from the URL when it is the default", async () => {
    const router = await start("/?page=3");
    await click(label("size50"));
    await settle();
    expect(search(router)).toEqual({ size: 50 });
    expect(state()).toBe("1/50");
    await click(label("size20"));
    await settle();
    expect(search(router)).toEqual({});
  });

  it("goes back through the browser's history, page by page", async () => {
    const router = await start("/");
    await click(label("next"));
    await settle();
    await click(label("next"));
    await settle();
    expect(state()).toBe("3/20");
    act(() => router.history.back());
    await settle();
    await settle();
    expect(state()).toBe("2/20");
  });
});

function CursorProbe({ rows }: { rows: number[] }) {
  const p = useCursorPaging({ sizes: [25, 50], defaultSize: 50 });
  return (
    <div>
      <output data-testid="state">{`${p.before}|${p.index}|${p.hasNewer}|${p.newerToFirst}`}</output>
      <button onClick={() => p.older(rows[0]!)}>older</button>
      <button onClick={p.newer}>newer</button>
      <button onClick={p.reset}>reset</button>
    </div>
  );
}

describe("useCursorPaging (the place in a log, in the URL)", () => {
  async function start(url: string) {
    const r = memoryRouter(<CursorProbe rows={[900, 800, 700]} />, url);
    mount(r.element);
    await settle();
    await settle();
    return r.router;
  }

  it("starts on the newest page", async () => {
    await start("/");
    expect(state()).toBe("0|1|false|false");
  });

  it("goes older with the cursor in the URL and numbers the pages it came through", async () => {
    const router = await start("/");
    await click(label("older"));
    await settle();
    expect(search(router)).toEqual({ before: 900 });
    expect(state()).toBe("900|2|true|false");
  });

  it("returns to exactly the page it came from with Newer", async () => {
    const router = await start("/");
    await click(label("older")); // page 2 under 900
    await settle();
    // page 3 would be under 800: emulate by moving the cursor through the hook again
    await click(label("older"));
    await settle();
    expect(state()!.startsWith("900|3")).toBe(true); // the same cursor is offered again by the probe: the trail is what counts
    await click(label("newer"));
    await settle();
    expect(search(router)).toEqual({ before: 900 });
    expect(state()).toBe("900|2|true|false");
    await click(label("newer"));
    await settle();
    expect(search(router)).toEqual({});
    expect(state()).toBe("0|1|false|false");
  });

  it("does not know the page number of a link, and Newer jumps to the newest", async () => {
    const router = await start("/?before=500");
    expect(state()).toBe("500|0|true|true");
    await click(label("newer"));
    await settle();
    expect(search(router)).toEqual({});
    expect(state()).toBe("0|1|false|false");
  });

  it("goes back to a page opened from a link when it was left through Older", async () => {
    const router = await start("/?before=500");
    await click(label("older"));
    await settle();
    expect(state()!.startsWith("900|0")).toBe(true); // still not numbered: the trail does not start at the newest page
    await click(label("newer"));
    await settle();
    expect(search(router)).toEqual({ before: 500 });
  });

  it("a filter change starts the log again from the newest page", async () => {
    const router = await start("/?before=500&size=25");
    await click(label("reset"));
    await settle();
    expect(search(router)).toEqual({ size: 25 });
  });
});
