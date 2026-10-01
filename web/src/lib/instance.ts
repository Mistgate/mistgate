import { queryOptions } from "@tanstack/react-query";
import { applyInstanceLang } from "@/i18n";
import { applyInstanceAccent } from "./accent";
import { assetUrl, auth, instance } from "./api";
import { setBrand } from "./brand";
import { plain } from "./plain";
import { queryClient } from "./session";

// What makes this installation look like itself: wordmark, logo, default accent and language. GetLoginInfo
// is public, so the sign-in and setup pages wear the brand before anyone is signed in.

export const loginInfoQuery = queryOptions({
  queryKey: ["login-info"],
  queryFn: () => auth.getLoginInfo({}),
  staleTime: Infinity,
  retry: false,
});

/** GetInstance: the owner-visible settings with the logo version. Used by Settings -> Interface. */
export const instanceQuery = queryOptions({
  queryKey: ["instance"],
  queryFn: async () => plain(await instance.getInstance({})),
  staleTime: 30_000,
});

type BrandFields = { brandHead: string; brandTail: string; accent: string; language: string; hasLogo: boolean };

/**
 * Puts the instance's brand on screen. The logo is fetched as text (it is sanitized server side and again
 * here) and inlined, because its tones follow the accent, which an <img> cannot do.
 */
export async function applyBrand(b: BrandFields, logoVersion = "") {
  setBrand({ wordmark: [b.brandHead, b.brandTail] });
  applyInstanceAccent(b.accent);
  applyInstanceLang(b.language);
  if (!b.hasLogo) {
    setBrand({ logoSvg: null });
    return;
  }
  try {
    const res = await fetch(assetUrl("brand/logo.svg") + (logoVersion ? `?v=${encodeURIComponent(logoVersion)}` : ""), {
      cache: "no-cache", // revalidated by ETag: a changed logo shows at once, an unchanged one costs a 304
    });
    if (res.ok) setBrand({ logoSvg: await res.text() });
  } catch {
    // the built-in mark stays
  }
}

let booted: Promise<void> | null = null;

/** Once per page load, before the first screen: fetch the public brand info and apply it. Never throws. */
export function bootBrand(): Promise<void> {
  booted ??= queryClient
    .ensureQueryData(loginInfoQuery)
    .then((info) => applyBrand(info))
    .catch(() => {}); // panel unreachable: the neutral brand stays, the screens show their own error
  return booted;
}
