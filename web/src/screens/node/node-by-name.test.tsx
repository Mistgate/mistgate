import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { NodeScreen } from "./index";

// An address with the node's name where its id belongs: the page finds the node by that exact name instead of saying there is none.

const navigate = vi.fn();
const getNode = vi.fn();
const listNodes = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { getNode: (...a: unknown[]) => getNode(...a), listNodes: (...a: unknown[]) => listNodes(...a) },
  health: { getDoctor: () => Promise.resolve({ nodes: [] }) },
  isUnauthenticated: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children?: unknown }) => <a>{children as never}</a>,
  useNavigate: () => navigate,
  useSearch: () => ({ tab: "ways" }),
  useParams: () => ({ id: "be" }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

async function show(nodeList: { id: string; name: string }[]) {
  getNode.mockRejectedValue(new Error("not_found"));
  listNodes.mockResolvedValue({ nodes: nodeList });
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root.render(<QueryClientProvider client={qc}><NodeScreen /></QueryClientProvider>));
  await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
  return { text: host.textContent ?? "", done: () => { act(() => root.unmount()); host.remove(); } };
}

describe("a node page opened by name", () => {
  it("goes to the node whose exact name it is, on the same tab", async () => {
    const v = await show([{ id: "nod_de1", name: "de1" }, { id: "nod_be", name: "be" }]);
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_be" }, search: { tab: "ways" }, replace: true });
    expect(v.text).not.toContain("No such node");
    v.done();
  });

  it("still says there is no such node when no name matches", async () => {
    navigate.mockClear();
    const v = await show([{ id: "nod_de1", name: "de1" }]);
    expect(navigate).not.toHaveBeenCalled();
    expect(v.text).toContain("No such node");
    v.done();
  });
});
