import { useEffect, useImperativeHandle, useRef, useState, type ReactNode, type Ref } from "react";
import { Icon } from "@/components/ui/icons";
import { Tick } from "@/components/windows";
import { useLang, useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useTheme } from "@/lib/theme";

// Cloudflare Turnstile for the sign-in and setup pages. The script is fetched only when the component is
// mounted (the decoy site and the subscription page never load anything from Cloudflare, and neither does
// the signed-in admin), and the widget is rendered explicitly, so we control theme, language and reset.

const scriptSrc = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

type Api = {
  render: (el: HTMLElement, options: Record<string, unknown>) => string;
  reset: (id: string) => void;
  remove: (id: string) => void;
};
declare global {
  interface Window {
    turnstile?: Api;
  }
}

let loading: Promise<Api> | null = null;

/** Loads Cloudflare's script once; a failure (blocked, offline) is forgotten so that "try again" really tries again. */
export function loadTurnstile(): Promise<Api> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  loading ??= new Promise<Api>((resolve, reject) => {
    const s = document.createElement("script");
    s.src = scriptSrc;
    s.async = true;
    s.onload = () => (window.turnstile ? resolve(window.turnstile) : reject(new Error("turnstile missing")));
    s.onerror = () => reject(new Error("turnstile script failed"));
    document.head.append(s);
  }).catch((e: unknown) => {
    loading = null;
    document.querySelector(`script[src="${scriptSrc}"]`)?.remove();
    throw e;
  });
  return loading;
}

export type FrameStatus = "idle" | "busy" | "ok" | "error";

const indicator: Record<FrameStatus, ReactNode> = {
  idle: <span className="size-6 flex-none rounded-[4px] border-2 border-faint bg-canvas" />,
  busy: <span className="size-6 flex-none animate-[mg-ui-spin_0.9s_linear_infinite] rounded-full border-[3px] border-surface-2 border-t-ok" />,
  ok: (
    <span className="pop-in grid size-6 flex-none place-items-center rounded-full bg-ok">
      <span className="-mt-[3px] h-2.5 w-[5px] rotate-45 border-r-[2.5px] border-b-[2.5px] border-[#0c0c0e]" />
    </span>
  ),
  error: <span className="grid size-6 flex-none place-items-center rounded-full bg-danger text-sm leading-none font-extrabold text-[#0c0c0e]">!</span>,
};

/**
 * A 65 px block: checkbox, spinner or green check, a label and the "CLOUDFLARE" sign. It fills
 * the space before Cloudflare's own widget paints over it, and stands in when the script cannot load.
 */
export function TurnstileFrame({ status, label, onClick }: { status: FrameStatus; label: string; onClick?: () => void }) {
  const t = useT();
  const cls = "flex h-[65px] w-full items-center gap-3 rounded-[6px] border border-line bg-surface px-3.5 text-start select-none";
  const body = (
    <>
      {indicator[status]}
      <span className="min-w-0 flex-1 text-sm">{label}</span>
      <span className="flex flex-none flex-col items-end gap-[3px]">
        <span className="text-[11px] font-extrabold tracking-[0.06em] text-muted">CLOUDFLARE</span>
        <span className="text-[9px] text-muted">{t("cf.links")}</span>
      </span>
    </>
  );
  return onClick ? (
    <button type="button" onClick={onClick} className={cx(cls, "cursor-pointer")}>
      {body}
    </button>
  ) : (
    <div role="status" className={cls}>
      {body}
    </div>
  );
}

/**
 * The check as the sign-in page of the board draws it (.lg-ck): a row 44 high with the mark of a step (a ring while Cloudflare
 * works, the tick when it is done), what is going on in words, and "Cloudflare Turnstile" at the end. Cloudflare's own
 * widget shows only when it has to be clicked (see `quiet` of Turnstile), and then it takes the row's place.
 */
export function TurnstileRow({ status, label, onClick }: { status: FrameStatus; label: string; onClick?: () => void }) {
  const cls = cx(
    "grid h-11 w-full grid-cols-[16px_minmax(0,1fr)_auto] items-center gap-2.5 rounded-xl border border-line bg-surface px-3.5 text-start text-[13px] transition-[border-color] duration-300 ease-out select-none",
    status === "ok" ? "text-fg" : "text-sec",
  );
  const body = (
    <>
      <Tick state={status === "ok" ? "done" : status === "error" ? "bad" : status === "busy" ? "now" : ""} />
      <span className="min-w-0 truncate">{label}</span>
      <span className="flex items-center text-[11px] text-muted">
        <Icon name="cloud" size={16} className="me-1.5" />
        Cloudflare Turnstile
      </span>
    </>
  );
  return onClick ? (
    <button type="button" onClick={onClick} className={cx(cls, "cursor-pointer")}>
      {body}
    </button>
  ) : (
    <div role="status" className={cls}>
      {body}
    </div>
  );
}

export type TurnstileHandle = {
  /** A token works once: call this after every attempt that used it (failed or not). */
  reset: () => void;
};

type WidgetProps = {
  siteKey: string;
  onToken: (token: string | null) => void;
  ref?: Ref<TurnstileHandle>;
  /** The challenge gave up, with Cloudflare's last error code ("110200": the site key does not allow this domain). */
  onFail?: (code: string) => void;
  /**
   * The sign-in page's way: a row of the board (TurnstileRow) says what is going on, and Cloudflare's widget is shown only
   * when it needs a click (appearance "interaction-only"). Without it the 65 px frame and the widget over it, as before.
   */
  quiet?: boolean;
};

/**
 * Renders the widget and reports its token: a string when the visitor is verified, null when there is no
 * valid token (not solved yet, expired, reset, error). Re-renders itself when the theme or language changes.
 */
export function Turnstile(props: WidgetProps) {
  const [attempt, setAttempt] = useState(0);
  // "try again" after a failed load starts a fresh widget
  return <Widget key={attempt} {...props} onRetry={() => setAttempt((n) => n + 1)} />;
}

function Widget({ siteKey, onToken, ref, onRetry, onFail, quiet }: WidgetProps & { onRetry: () => void }) {
  const t = useT();
  const lang = useLang();
  const theme = useTheme();
  const box = useRef<HTMLDivElement>(null);
  const widget = useRef<string | null>(null);
  const report = useRef(onToken);
  const fail = useRef(onFail);
  // "error": the script did not load; "failed": it loaded, but the challenge keeps failing (a wrong site key, a blocked visitor)
  const [state, setState] = useState<"loading" | "ready" | "error" | "failed">("loading");
  // quiet mode: a token is in hand (the row says "passed"), and Cloudflare asks for a click (its widget shows)
  const [passed, setPassed] = useState(false);
  const [asking, setAsking] = useState(false);

  useEffect(() => {
    report.current = onToken;
    fail.current = onFail;
  });
  useImperativeHandle(ref, () => ({
    reset() {
      setPassed(false);
      report.current(null);
      if (widget.current) window.turnstile?.reset(widget.current);
    },
  }));

  useEffect(() => {
    let cancelled = false;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let errors = 0;
    loadTurnstile().then(
      (api) => {
        if (cancelled || !box.current) return;
        const id = api.render(box.current, {
          sitekey: siteKey,
          theme,
          language: lang === "zh" ? "zh-cn" : lang, // Turnstile spells the Chinese widget "zh-cn"
          size: "flexible",
          ...(quiet ? { appearance: "interaction-only" } : {}),
          callback: (token: string) => {
            errors = 0;
            setPassed(true);
            setAsking(false);
            report.current(token);
          },
          "before-interactive-callback": () => setAsking(true),
          "after-interactive-callback": () => setAsking(false),
          "expired-callback": () => {
            setPassed(false);
            report.current(null); // the widget refreshes itself and calls back again
          },
          "timeout-callback": () => {
            setPassed(false);
            report.current(null);
          },
          "error-callback": (code?: unknown) => {
            setPassed(false);
            report.current(null);
            // a transient hiccup heals with a reset; a wrong site key or a blocked challenge does not, so give up after two
            if (++errors <= 2) retry = setTimeout(() => api.reset(id), 1000);
            else {
              setState("failed");
              fail.current?.(String(code ?? ""));
            }
            return true; // handled: Cloudflare need not log it
          },
        });
        widget.current = id;
        setState("ready");
      },
      () => {
        if (cancelled) return;
        report.current(null);
        setState("error");
      },
    );
    return () => {
      cancelled = true;
      clearTimeout(retry);
      if (widget.current) window.turnstile?.remove(widget.current);
      widget.current = null;
      report.current(null);
      // the next widget starts from nothing: no token, no click asked for
      setPassed(false);
      setAsking(false);
    };
  }, [siteKey, theme, lang, quiet]);

  const dead = state === "error" || state === "failed";
  if (quiet) {
    const label = dead ? `${t(state === "failed" ? "cf.failed" : "cf.error")} ${t("cf.retry")}` : passed ? t("win.cf.passed") : t("win.cf.checking");
    return (
      <div className="w-full">
        {!asking && <TurnstileRow status={dead ? "error" : passed ? "ok" : "busy"} label={label} onClick={dead ? onRetry : undefined} />}
        {/* Cloudflare's iframe lives here: hidden until it has to be clicked, then 65 px tall in the row's place */}
        <div ref={box} hidden={dead} className="w-full" />
      </div>
    );
  }
  return (
    <div className="relative h-[65px] w-full">
      <TurnstileFrame
        status={dead ? "error" : "busy"}
        label={dead ? `${t(state === "failed" ? "cf.failed" : "cf.error")} ${t("cf.retry")}` : t("cf.busy")}
        onClick={dead ? onRetry : undefined}
      />
      {/* Cloudflare's iframe (65 px tall, as wide as this box) paints over the frame once it is ready */}
      <div ref={box} hidden={dead} className="absolute inset-0" />
    </div>
  );
}
