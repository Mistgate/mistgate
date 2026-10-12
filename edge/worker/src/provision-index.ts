import { connect } from "cloudflare:sockets";
import { getServePanel } from "./panel";
import { Provisioner, provisionEntry, wireProvisioner } from "./provisioner";
import { openSocket } from "./shell";

// The entry of the second script, mistgate-edge-provision (wrangler.provision.example.toml): the Provisioner object and
// nothing else. It has no route and no workers.dev name, and its own fetch handler answers 404 without starting Go, so the
// wasm of this isolate serves only the Provisioner (design §4.1).
wireProvisioner((env, kick) => getServePanel(env, { kick, sshConnect: (host, port, timeoutMs) => openSocket(connect, host, port, timeoutMs) }));

export { Provisioner };
export default provisionEntry;
