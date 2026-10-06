import { check } from "./watch";
import type { WatchEnv } from "./env";

// A cron Worker with no public function: every minute it checks the panel and tells the owner in Telegram when the
// panel stops answering and when it is back.
export default {
  scheduled: (_controller, env, ctx) => {
    ctx.waitUntil(check(env));
  },
  fetch: () => new Response("Not Found", { status: 404 }),
} satisfies ExportedHandler<WatchEnv>;
