import { h } from "./dom";
import { dict } from "./i18n";
import { icon } from "./icons";
import { logoMark } from "./mark";
import { ApiError } from "./api";
import { note as noteBox } from "./ui";
import { pageHeader } from "./view";
import type { Lang, MgData } from "./types";

// The password form: what the page shows instead of itself while the browser has no cookie for it (subs/unlock.go). The
// password is typed once; the server answers with a cookie and the page is simply loaded again.

/** `note` is the line under the field that is not an error: "type it in Latin letters". */
export type LockState = { lang: Lang; pw: string; busy: boolean; error: string; note: string };
export type LockActions = { lang(l: Lang): void; input(v: string): void; submit(): void };

// A Russian keyboard is the default one of many friends: the Cyrillic letters that look like Latin ones of the password's
// alphabet are taken for those (а→a, е→e, к→k, м→m, р→p, с→c, у→y, х→x), upper case included.
const lookalike: Record<string, string> = { а: "a", е: "e", к: "k", м: "m", р: "p", с: "c", у: "y", х: "x" };
const latin = (v: string) => v.toLowerCase().replace(/[аекмрсух]/g, (c) => lookalike[c] ?? c);

/** What the field shows for what was typed: lower case, letters and digits only, a dash after the fourth: "k3m9-x7pq". */
export function formatPassword(v: string): string {
  const s = latin(v).replace(/[^a-z0-9]/g, "").slice(0, 8);
  return s.length > 4 ? `${s.slice(0, 4)}-${s.slice(4)}` : s;
}

/** Something was typed that a password never has and that is not a look-alike: a letter of another keyboard. */
export const foreignLetters = (v: string) => /[^a-z0-9\s-]/.test(latin(v));

const typed = (v: string) => v.replace("-", "").length;
const complete = (v: string) => typed(v) === 8;

// The page is built again on every change (an error, the language); the mark's intro plays on the first build only.
let introPlayed = false;

export function lockView(d: MgData, s: LockState, a: LockActions): HTMLElement {
  const t = dict[s.lang];
  // the brand's mark with the padlock on its corner
  const mark = h("span", { class: "lock-mark" }, logoMark(d, 72, !introPlayed), h("span", { class: "lock-ico" }, icon("lock")));
  introPlayed = true;
  // never disabled: a button that does nothing and says nothing is the dead end this form must not have
  const go = h("button", { class: `btn pri${s.busy ? " busy" : ""}`, type: "submit", "data-k": "pw-go", disabled: s.busy }, s.busy ? t.lockBusy : t.lockGo);
  const note = h("p", { class: "pw-note", id: "pw-note", role: "status" }, s.note);
  const input = h("input", {
    class: `inp pw${s.error ? " err" : ""}`,
    id: "pw",
    "data-k": "pw",
    "data-autofocus": "",
    type: "text",
    name: "password",
    value: s.pw,
    placeholder: "xxxx-xxxx",
    maxlength: 24,
    inputmode: "text",
    enterkeyhint: "go",
    autocomplete: "off",
    autocapitalize: "none",
    autocorrect: "off",
    spellcheck: "false",
    "aria-invalid": s.error ? "true" : false,
    "aria-describedby": s.error ? "pw-err pw-note" : "pw-note",
    readonly: s.busy, // not disabled: a disabled field drops the focus the keyboard is on
    on: {
      input: (e) => {
        const el = e.target as HTMLInputElement;
        const foreign = foreignLetters(el.value);
        el.value = formatPassword(el.value);
        // no redraw while typing: it would drop the cursor
        note.textContent = foreign ? t.lockLatin : "";
        a.input(el.value);
        s.note = note.textContent;
      },
    },
  });
  return h(
    "main",
    { class: "wrap" },
    pageHeader(d, s.lang, a.lang),
    h(
      "section",
      { class: "card lock", "aria-labelledby": "pw-t" },
      mark,
      h("div", { class: "lock-head" }, h("h1", { class: "h1 ct", id: "pw-t" }, t.lockT), h("p", { class: "txt mut" }, t.lockH)),
      h(
        "form",
        {
          class: "lock-f",
          on: {
            submit: (e) => {
              e.preventDefault();
              if (!s.busy) a.submit();
            },
          },
        },
        h("div", { class: "fld" }, h("label", { for: "pw" }, t.lockLabel), input, note, s.error && noteBox("bad", "warn", s.error, { id: "pw-err" })),
        go,
      ),
      h("p", { class: "hint lock-none" }, t.lockNone),
    ),
  );
}

type Deps = {
  st: LockState;
  endpoint: string;
  unlock: (url: string, password: string) => Promise<void>;
  render: () => void;
  /** The cookie is set: load the page again, which now shows itself. */
  reload: () => void;
  save: (l: Lang) => void;
};

export function lockActions({ st, endpoint, unlock, render, reload, save }: Deps): LockActions {
  return {
    lang(l) {
      st.lang = l;
      save(l);
      render();
    },
    input(v) {
      st.pw = v;
      if (st.error) {
        st.error = "";
        document.getElementById("pw-err")?.remove();
        document.getElementById("pw")?.classList.remove("err");
      }
    },
    async submit() {
      if (st.busy) return;
      const t = dict[st.lang];
      if (!complete(st.pw)) {
        // said, not silently refused: how many characters are missing
        st.error = t.lockShort(typed(st.pw));
        render();
        document.getElementById("pw")?.focus();
        return;
      }
      st.busy = true;
      st.error = "";
      render();
      try {
        await unlock(endpoint, st.pw);
        reload();
        return;
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) st.error = t.lockWrong(e.left);
        else if (e instanceof ApiError && e.status === 429) st.error = t.lockLater(Math.ceil(e.retryAfter / 60));
        else if (e instanceof ApiError && e.code === "network") st.error = t.lockNet;
        else st.error = t.lockFail;
      }
      st.busy = false;
      render();
      document.getElementById("pw")?.focus();
    },
  };
}
