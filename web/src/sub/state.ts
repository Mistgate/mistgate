import type { Dict } from "./i18n";
import type { AwgConfig, Lang, MgData, Platform, Theme } from "./types";

// What the page remembers while it is open, and what its parts may do. The data (MgData) is the server's; this is the person's.

export type Confirm = { id: string; kind: "remove" | "rotate" } | null;

/** The panes of the "add a device" dialog: "pick" asks how the device will connect, "link" is the app-with-the-link branch, "key" the AmneziaVPN key form. */
export type AddPane = "pick" | "link" | "key";

export type AmzState = {
  /** The device whose key the dialog shows ("Show key"). */
  open: string | null;
  /** The "add a device" dialog is open. */
  adding: boolean;
  /** What the open "add a device" dialog shows: the choice of the way (link app or key), the link branch, or the key form. */
  pane: AddPane;
  /** The device this dialog just created: the dialog then shows its key instead of the form. */
  created: string | null;
  /** The stale device whose new key the dialog shows ("new key needed"). */
  renew: string | null;
  /** "add", a device id, "rename:<id>" or "renew:<id>" while a call is in flight; "" otherwise. */
  busy: string;
  /** The last failure, as the panel's code; "" when none. `errorAt` is the busy key of the call that failed. */
  error: string;
  errorAt: string;
  retryMin: number;
  confirm: Confirm;
  /** The device whose "more" menu is open. */
  menu: string | null;
  /** The device being renamed, with what is typed. */
  rename: { id: string; value: string } | null;
  configs: Record<string, AwgConfig[]>;
  node: Record<string, number>;
  /** Where each key is being added ("this", "phone", "other"): the dialog's switch; absent = the default for the device. */
  where: Record<string, string>;
  form: { profile: string; platform: string; label: string };
  /** The platform this page is open on ("" unknown): a key for this very device is copied or downloaded, not scanned. */
  here: string;
};

export const newAmzState = (platform: string, profile: string, here = ""): AmzState => ({
  open: null,
  adding: false,
  pane: "key",
  created: null,
  renew: null,
  busy: "",
  error: "",
  errorAt: "",
  retryMin: 0,
  confirm: null,
  menu: null,
  rename: null,
  configs: {},
  node: {},
  where: {},
  form: { profile, platform, label: "" },
  here,
});

export type DnsState = {
  /** The server whose DNS picker is open. */
  open: string | null;
  /** The preset picked in it ("" = the server's default). */
  pick: string;
  /** The server whose choice is being saved. */
  busy: string;
  /** The last failure, where it happened. */
  error: { server: string; code: string; retryMin: number; preset: string } | null;
  /** Servers whose choice was just saved: the card says what happens next. */
  done: Record<string, true>;
};

export const newDnsState = (): DnsState => ({ open: null, pick: "", busy: "", error: null, done: {} });

export type AmzActions = {
  /** Opens (true) the "add a device" dialog, at `pane` or where the person's ways say (the choice, or the only way), or closes (false) whichever dialog is open. */
  add(open: boolean, pane?: AddPane): void;
  /** Moves the open "add a device" dialog to another pane (a choice made, "Back"). */
  pane(pane: AddPane): void;
  /** Form fields change without a re-render: the page would drop what is being typed. */
  form(patch: Partial<AmzState["form"]>, draw?: boolean): void;
  create(): void;
  /** "Show key": fetches the key of a device and shows it in the dialog (null closes it). */
  show(id: string | null): void;
  /** "New key needed": fetches the key of a stale device and shows it in the dialog. */
  renew(id: string): void;
  node(id: string, i: number): void;
  where(id: string, w: string): void;
  ask(c: Confirm): void;
  rotate(id: string): void;
  remove(id: string): void;
  menu(id: string | null): void;
  renameStart(id: string): void;
  /** What is typed: no re-render (it would drop the cursor). */
  renameInput(value: string): void;
  renameSave(): void;
  renameCancel(): void;
};

export type DnsActions = {
  open(server: string | null): void;
  pick(preset: string): void;
  apply(): void;
  /** Saves a choice again after a failure. */
  retry(): void;
};

/** What the page offers its parts: copy (with a callback for the button's own "copied" state) and download. */
export type Tools = { copy(text: string, message: string, done?: () => void): void; download(filename: string, text: string): void };

export type State = {
  lang: Lang;
  /** The device step: the chosen platform. */
  platform: Platform;
  /** What the browser says it is (null = could not tell). */
  detected: Platform | null;
  /** The chosen app of the platform (appKey), "" = the first. */
  app: string;
  theme: Theme;
  /** The "connect another device" row (the QR code of the link) is open. */
  qrOpen: boolean;
  /** The done step shows its three steps again. */
  stepsAgain: boolean;
  annClosed: boolean;
  /** A returning visitor (computed once when the page opens: the page does not reorder itself while it is read). */
  returning: boolean;
  /** This device pressed "add" or "copy" before. */
  marked: boolean;
  amz: AmzState;
  dns: DnsState;
};

/** What every section of the page draws from: the data, the person's state, what it may do, the words, and the support link ("" = none). */
export type Ctx = { d: MgData; s: State; a: Actions; t: Dict; support: string };

export type Actions = {
  lang(l: Lang): void;
  platform(p: Platform): void;
  app(key: string): void;
  theme(t: Theme): void;
  qrOpen(open: boolean): void;
  stepsAgain(): void;
  closeAnn(): void;
  /** This device set a connection up: remembered (the "returning" mark). */
  mark(): void;
  amz: AmzActions;
  dns: DnsActions;
} & Tools;
