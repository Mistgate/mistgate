// The entry module of the Durable Object tests: only the class under test (the real Worker's entry also pulls in the wasm).
export { Limiter } from "../../src/limiter";

export default {
  fetch: () => new Response("limiter test worker"),
} satisfies ExportedHandler;
