import { Code, ConnectError } from "@connectrpc/connect";
import { subscriptions } from "@/lib/api";
import { setHxDemo, type HxDemo } from "./subscriptions/demo";
import type { RoutingProfile } from "./subscriptions/model";
import { fakeRoutingPreview, incyLink, pasted, rp, rtBody, rtHas, rtMax, rtOne, saved } from "./dev-kit-subs-rt-data";
import type { SceneDef, World } from "./dev-kit-subs-page";

// Development only. The scenes of the tab "Routing" (screens/subscriptions/routing.tsx): the list as the panel stores it, an empty one,
// a profile open or folded, a text pasted (a link that says "add", one the panel refuses, one with fields it leaves out, one too long for
// the header), the mistakes of the form (a name used twice, a group in two profiles, two profiles for all groups, a profile for nobody),
// a save in flight, saved, not taken, refused at a profile or as a whole, the question before leaving, the preview that is loading, and
// the same page on the phone, in the light theme and in Persian. Registered in dev-kit-subs-page.tsx. The panel's reading of a pasted
// text is answered here (dev-kit-subs-rt-data.ts); nothing leaves the page.

type Make = { demo?: HxDemo; tune?: (w: World) => void; w?: number; h?: number; params?: string };

const hung = () => new Promise<never>(() => {});

function profiles(state: string, title: string, rows: RoutingProfile[], o: Make = {}): SceneDef {
  return {
    state,
    tab: "routing",
    title,
    h: 1100,
    ...(o.w && { w: o.w }),
    ...(o.h && { h: o.h }),
    ...(o.params && { params: o.params }),
    setup: (w) => {
      setHxDemo(o.demo ?? {});
      w.settings.routingProfiles = rows;
      w.patch(subscriptions, { previewRoutingProfile: fakeRoutingPreview as (r: never) => unknown });
      o.tune?.(w);
    },
  };
}

const looks = (s: SceneDef): SceneDef[] => [
  { ...s, state: `${s.state}-light`, title: `${s.title}, light`, params: "&theme=light" },
  { ...s, state: `${s.state}-phone`, title: `${s.title}, phone 400`, w: 400, h: 1700 },
  { ...s, state: `${s.state}-fa`, title: `${s.title}, Persian`, params: "&lang=fa" },
];

const edited = saved(rtHas).map((r, i) => (i === 0 ? { ...r, text: r.text.replace("Для семьи", "Для семьи и гостей"), onlyAdd: true } : r));
const refusedAll = { text: "routing_profiles: at most 20" };
const dupName = [rp({ Name: "Для семьи", DirectSites: [] }, { onlyGroups: true, groupIds: ["grp_fm"] }), rp({ Name: "для СЕМЬИ", DirectSites: ["domain:a.example"] }, { onlyGroups: true, groupIds: ["grp_fr"] })];
const twoGroups = [rp({ Name: "A", DirectSites: [] }, { onlyGroups: true, groupIds: ["grp_fm"] }), rp({ Name: "B", DirectSites: [] }, { onlyGroups: true, groupIds: ["grp_fm", "grp_fr"] })];
const twoAll = [rp({ Name: "A", DirectSites: [] }), rp({ Name: "B", DirectSites: [] })];
const nobody = [rp({ Name: "A", DirectSites: [] }, { onlyGroups: true, groupIds: [] })];
const gone = [rp({ Name: "A", DirectSites: [] }, { onlyGroups: true, groupIds: ["grp_gone"] })];

const rtHasScene = profiles("rt", "Routing: two profiles, the first open", rtHas, { demo: { open: 0 } });

export const rtScenes: SceneDef[] = [
  rtHasScene,
  profiles("rt-empty", "Routing: no profile", []),
  profiles("rt-closed", "Routing: all folded", rtHas, { demo: { open: null } }),
  profiles("rt-second", "Routing: the second open (add only, for Друзья)", rtHas, { demo: { open: 1 } }),
  profiles("rt-one", "Routing: one profile for all groups", rtOne, { demo: { open: 0 } }),
  profiles("rt-new", "Routing: a new profile, nothing pasted yet", rtHas, { demo: { open: 2, routing: [...saved(rtHas), pasted("")] } }),
  profiles("rt-pasted", "Routing: a link that says add, with fields the panel leaves out", rtHas, { demo: { open: 2, routing: [...saved(rtHas), pasted(incyLink, { onlyAdd: true })] } }),
  profiles("rt-garbage", "Routing: a text the panel cannot read", rtHas, { demo: { open: 2, routing: [...saved(rtHas), pasted("hello, world!")] } }),
  profiles("rt-http-geo", "Routing: a geo file over http, which the panel refuses", rtHas, { demo: { open: 2, routing: [...saved(rtHas), pasted('{"Name":"x","Geoipurl":"http://geo.example.com/geoip.dat"}')] } }),
  profiles("rt-body", "Routing: a profile whose link is too long for the header", rtBody, { demo: { open: 0 } }),
  profiles("rt-dirty", "Routing: changed, not saved (Discard beside Save)", rtHas, { demo: { open: 0, routing: edited } }),
  profiles("rt-busy", "Routing: the save is on its way", rtHas, { demo: { busy: true, routing: edited } }),
  profiles("rt-saved", "Routing: saved", rtHas, { demo: { saved: true } }),
  profiles("rt-fail", "Routing: the panel did not take the save", rtHas, { demo: { fail: "the panel does not answer", routing: edited } }),
  profiles("rt-refused-row", "Routing: the panel refuses the save at the second profile", rtHas, { demo: { open: 1, refusal: { text: "Geoipurl: an https address (https://host/path)", k: 1 }, routing: saved(rtHas) } }),
  profiles("rt-refused-all", "Routing: the panel refuses the whole list", rtHas, { demo: { refusal: refusedAll, routing: edited } }),
  profiles("rt-dup-name", "Routing: two profiles of one name", dupName, { demo: { open: 1 } }),
  profiles("rt-two-groups", "Routing: a group in two profiles", twoGroups, { demo: { open: 1 } }),
  profiles("rt-two-all", "Routing: two profiles for all groups", twoAll, { demo: { open: 1 } }),
  profiles("rt-nobody", "Routing: a profile for chosen groups with none chosen", nobody, { demo: { open: 0 } }),
  profiles("rt-gone", "Routing: the groups of a profile are gone", gone, { demo: { open: 0 } }),
  profiles("rt-max", "Routing: 20 of 20", rtMax, { demo: { open: 0 } }),
  profiles("rt-leave", "Routing: the question «Уйти без сохранения?» (a change, then another tab)", rtHas, { demo: { open: 0, leave: true, routing: edited } }),
  profiles("rt-reading", "Routing: the panel is reading a pasted text", rtHas, { demo: { open: 2, routing: [...saved(rtHas), pasted(incyLink)] }, tune: (w) => w.patch(subscriptions, { previewRoutingProfile: hung }) }),
  profiles("rt-unavailable", "Routing: the panel does not answer the reading", rtHas, {
    demo: { open: 2, routing: [...saved(rtHas), pasted(incyLink)] },
    tune: (w) => w.patch(subscriptions, { previewRoutingProfile: () => Promise.reject(new ConnectError("the panel does not answer", Code.Unavailable)) }),
  }),
  ...looks(rtHasScene),
  ...looks(profiles("rt-garbage-look", "Routing: a text the panel cannot read", rtHas, { demo: { open: 2, routing: [...saved(rtHas), pasted("hello, world!")] } })),
  ...looks(profiles("rt-body-look", "Routing: a long profile", rtBody, { demo: { open: 0 } })),
];
