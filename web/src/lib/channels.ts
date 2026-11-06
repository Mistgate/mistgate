// Release channels (design/adr/0011-release-channels.md): a panel follows one channel and sees it and every more stable one.
// The channel of a release is its tag: vX.Y.Z is stable, vX.Y.Z-testing.N testing, vX.Y.Z-dev.N dev.

export type Channel = "stable" | "testing" | "dev";

export const channels: readonly Channel[] = ["stable", "testing", "dev"];

/** The server's string as a channel; anything else (an unstamped build, an empty field) reads as stable. */
export const asChannel = (s: string | undefined): Channel => (s === "testing" || s === "dev" ? s : "stable");

/** How early a channel is: stable 0, testing 1, dev 2. A panel on a channel sees the releases of every lower rank too. */
export const channelRank: Record<Channel, number> = { stable: 0, testing: 1, dev: 2 };

/** The colour of a channel's dot (the same tone classes as the status dots): stable calm, testing amber, dev lavender. */
export const channelTone: Record<Channel, string> = { stable: "tone-ok", testing: "tone-warn", dev: "tone-busy" };
