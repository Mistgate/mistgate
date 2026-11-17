import { useQueries, useQuery } from "@tanstack/react-query";
import { useBlocker, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";
import { useAddNode } from "@/components/add-node";
import { Button, buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { GenerateMode } from "@/gen/mistgate/admin/v1/awg_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { awg as awgApi, profiles } from "@/lib/api";
import { asVersion } from "@/lib/awg";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { errorText } from "@/lib/errors";
import { checksQuery, countryColumn } from "@/lib/health";
import { agentLinked } from "@/lib/node-status";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { hintOf } from "@/lib/ways";
import { useIsOwner } from "@/lib/updates";
import { settingsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import { asMode } from "@/screens/node/awg-backend";
import { wayOf } from "@/screens/users/groups";
import { AppLink, useGo } from "@/screens/users/nav";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import { ConfirmModal, useDebounced } from "@/screens/users/ui";
import { byProtocol, telegramOnly, trustTunnelOnly } from "./protocol-meta";
import { changedFields, allFields, getAt, parseSchema, setAt, type Settings } from "./schema";
import type { Problem } from "./schema-form";
import { askNode, rowOf, type Asked, type Fix, type NodeIn } from "./wizard-check";
import { Gl, P } from "./wizard-bits";
import { DoneStep } from "./wizard-done";
import { autoNameOf, glyphOf, portValid, taskById, taskOfProtocol, taskSettings, tasks, toneOf, withCdnHost, type TaskId } from "./wizard-model";
import { NodesStep, type NodeVM } from "./wizard-nodes";
import { PeopleStep, type GroupVM } from "./wizard-people";
import { runDone, useRun, type RunState } from "./wizard-run";
import { SettingsStep } from "./wizard-settings";
import { TaskStep } from "./wizard-task";
import "./profile-new.css";

/** What a scene (dev-kit) starts the wizard from; the real screen never gets one. */
export type WizardDemo = {
  step?: 1 | 2 | 3 | 4 | 5;
  task?: TaskId;
  protocol?: string;
  name?: string;
  port?: string;
  cdn?: string;
  all?: boolean;
  picked?: Record<string, boolean>;
  fixes?: Record<string, Fix>;
  groups?: Record<string, boolean>;
  /** The last step as it stands at some moment: the scene does not make anything. */
  run?: RunState;
  /** A scene puts the settings through this before drawing (the full form open, an edited field). */
  settings?: (s: Settings) => Settings;
};

type Cfg = { task: TaskId; protocol: string; settings: Settings; base: Settings; typed: string | null; portEdit: string | null };

/** /profiles/new: Task, Settings, Nodes, People, Done; only the owner makes a profile (policy.go). */
export function ProfileNewScreen({ demo }: { demo?: WizardDemo }) {
  const t = useTx();
  const go = useGo();
  const owner = useIsOwner();
  const protocols = useQuery(protocolsQuery);
  if (!owner)
    return (
      <EmptyState
        title={t("profiles.new")}
        action={
          <Button variant="secondary" size="lg" onClick={() => go("/profiles")}>
            {t("profiles.back")}
          </Button>
        }
      >
        {t("profiles.ownerOnly")}
      </EmptyState>
    );
  if (protocols.isPending) return <Pending />;
  if (protocols.isError) return <QueryError error={protocols.error} onRetry={() => void protocols.refetch()} />;
  const list = [...protocols.data].sort((a, b) => byProtocol(a.id, b.id));
  if (list.length === 0) return <EmptyState title={t("profiles.noSchema")} />;
  return <Wizard protocols={list} demo={demo} />;
}

function Wizard({ protocols, demo }: { protocols: ProtocolInfo[]; demo?: WizardDemo }) {
  const t = useTx();
  const go = useGo();
  const navigate = useNavigate();
  const addNode = useAddNode();
  const fx = useEffectsEnabled();
  // "?node=<id>": made from that node's page, the node is ticked and Done goes back there; "?protocol=" skips the first step
  const { node: fromNode = "", protocol: urlProtocol = "" } = useSearch({ strict: false }) as { node?: string; protocol?: string };
  const known = demo ? undefined : protocols.find((p) => p.id === urlProtocol);

  const shown = tasks.filter((k) => k.protocol === "" || protocols.some((p) => p.id === k.protocol));
  const [pick, setPick] = useState<TaskId>(demo?.task ?? (known ? taskOfProtocol(known.id) : "fast"));
  const infoOf = (task: TaskId, id?: string) => protocols.find((p) => p.id === (id ?? taskById(task).protocol)) ?? protocols[0]!;
  const begin = (task: TaskId, id?: string, cdn = ""): Cfg => {
    const info = infoOf(task, id);
    const settings = taskSettings(task, info, cdn);
    return { task, protocol: info.id, settings, base: settings, typed: null, portEdit: null };
  };
  const [step, setStep] = useState<1 | 2 | 3 | 4 | 5>(demo?.step ?? (known ? 2 : 1));
  const [cfg, setCfg] = useState<Cfg | null>(() => {
    if (!(demo ? (demo.step ?? 1) > 1 || demo.task : known)) return null;
    const task = demo?.task ?? taskOfProtocol(known!.id);
    const c = begin(task, known && task === "own" ? known.id : demo?.protocol, demo?.cdn ?? "");
    const edited = demo?.settings ? demo.settings(c.settings) : c.settings;
    return { ...c, settings: edited, typed: demo?.name ?? null, portEdit: demo?.port ?? null };
  });
  const [all, setAll] = useState(demo?.all ?? cfg?.task === "own");
  const [saveProblems, setSaveProblems] = useState<Problem[]>([]);
  const [picked, setPicked] = useState<Record<string, boolean>>(demo?.picked ?? {});
  const [fixes, setFixes] = useState<Record<string, Fix>>(demo?.fixes ?? {});
  const [groupPick, setGroupPick] = useState<Record<string, boolean>>(demo?.groups ?? {});
  const [leaving, setLeaving] = useState(false);
  const [busyOld, setBusyOld] = useState(false);
  const [cdn, setCdn] = useState(demo?.cdn ?? "");

  // ---- what the URL says: a protocol there is step 2 of it, none is step 1 (Back from the settings lands on the tasks)
  useEffect(() => {
    if (demo || step === 5) return;
    if (!urlProtocol && step > 1) setStep(1);
    else if (urlProtocol && step === 1 && known) {
      const task = taskOfProtocol(known.id);
      setPick(task);
      setCfg(begin(task, known.id));
      setAll(task === "own");
      setStep(2);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- when the URL changes
  }, [urlProtocol]);
  const url = (protocol?: string) => void navigate({ to: "/profiles/new", search: { ...(fromNode && { node: fromNode }), ...(protocol && { protocol }) } } as never);

  // ---- step 2
  const info = cfg ? infoOf(cfg.task, cfg.protocol) : undefined;
  const task = cfg ? taskById(cfg.task) : taskById(pick);
  const settings = cfg?.settings ?? {};
  const old = info?.id === "awg" && asVersion(settings.version) === "2.0";
  const name = cfg && info ? (cfg.typed ?? autoNameOf(cfg.task, info, settings, old)) : "";
  const portText = cfg ? (cfg.portEdit ?? String(getAt(settings, ["port"]) ?? "")) : "";
  const portErr = !portValid(portText);
  const cdnErr = cfg?.task === "cdn" && cdn.trim() === "";
  const settingsJson = JSON.stringify(settings);
  const debounced = useDebounced(settingsJson, 350);
  // gcTime 0: a typed secret sits in the query key, so nothing keeps it after the screen moves on
  const preview = useQuery({
    queryKey: ["profiles", "preview", info?.id, debounced],
    queryFn: ({ signal }) => profiles.previewProfile({ protocol: info!.id, settingsJson: debounced }, { signal }),
    enabled: !!info && step <= 4,
    gcTime: 0,
    staleTime: 0,
  });
  const problems: Problem[] = saveProblems.length > 0 ? saveProblems : (preview.data?.errors ?? []);
  const taken = (useQuery(profileListQuery).data ?? []).some((p) => p.name === name.trim());
  const nameError: "" | "empty" | "taken" = !name.trim() ? "empty" : taken ? "taken" : "";
  const errs2 = portErr || nameError !== "" || !!cdnErr || problems.length > 0;
  const fields = useMemo(() => (info ? allFields(parseSchema(info.settingsSchemaJson)) : []), [info]);
  const touched = !!cfg && (cfg.typed !== null || changedFields(fields, settings, cfg.base).length > 0);

  const edit = (next: Settings, patch: Partial<Cfg> = {}) => {
    setSaveProblems([]);
    setCfg((c) => (c ? { ...c, settings: next, portEdit: null, ...patch } : c));
  };
  const setPort = (v: string) => {
    setSaveProblems([]);
    setCfg((c) => (c ? { ...c, portEdit: v, settings: portValid(v) ? setAt(c.settings, ["port"], Number(v)) : c.settings } : c));
  };
  const setCdnHost = (v: string) => {
    setCdn(v);
    setSaveProblems([]);
    setCfg((c) => (c ? { ...c, settings: withCdnHost(c.settings, v) } : c));
  };
  /** AmneziaWG's version is a whole new block of numbers: the server makes it (the same call as the version cards of the full form). */
  async function setOld(v: boolean) {
    if (!cfg || busyOld) return;
    setBusyOld(true);
    try {
      const cur = (cfg.settings.obfuscation ?? {}) as Record<string, unknown>;
      const preset = typeof cur.preset === "string" && cur.preset ? cur.preset : "dns";
      const r = await awgApi.generateObfuscation({ version: v ? "2.0" : "3.1", preset, mtu: Number(cfg.settings.mtu) || 0, domain: typeof cur.domain === "string" ? cur.domain : "", mode: GenerateMode.FULL });
      const next = JSON.parse(r.obfuscationJson) as Record<string, unknown>;
      next.preset = preset;
      if (cur.per_device_signature !== undefined) next.per_device_signature = cur.per_device_signature;
      edit({ ...cfg.settings, version: v ? "2.0" : "3.1", obfuscation: next });
    } catch {
      /* the cards of the full form say what went wrong */
    } finally {
      setBusyOld(false);
    }
  }

  // ---- step 3: what the check says about each node
  const nodesQ = useQuery(nodesQuery);
  const relaysQ = useQuery({ ...relaysQuery(""), enabled: true });
  const checksQ = useQuery(checksQuery(""));
  const live = (nodesQ.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED);
  const entries = new Set((relaysQ.data ?? []).map((r) => r.viaNodeId));
  const column = countryColumn(checksQ.data?.countryChecks);
  const nodeIn = (n: (typeof live)[number]): NodeIn => ({ id: n.id, name: n.name, address: n.address, linked: agentLinked(n.status), entry: entries.has(n.id), firstAwg: info?.id === "awg" && !n.protocols.includes("awg"), awgMode: n.awgBackend });
  const asks = useQueries({
    queries: live.map((n) => ({
      queryKey: ["profiles", "new-check", n.id, info?.id, debounced, fixes[n.id]?.port ?? "", fixes[n.id]?.sni ?? "", !!fixes[n.id]?.anyway],
      queryFn: ({ signal }: { signal: AbortSignal }) => askNode({ protocol: info!.id, settingsJson: debounced, nodeId: n.id, port: fixes[n.id]?.port ?? "", sni: fixes[n.id]?.sni ?? "", anyway: !!fixes[n.id]?.anyway }, signal),
      enabled: !!info && !demo?.run && step >= 3 && step <= 4 && problems.length === 0,
      retry: false,
      gcTime: 0,
      staleTime: 5_000,
    })),
  });
  const mtls = getAt(settings, ["tls_mode"]);
  const altKind: "" | "self" | "site" =
    info?.id === "hysteria2" || info?.id === "trusttunnel"
      ? mtls === "self_signed"
        ? ""
        : "self"
      : info?.id === "vless"
        ? getAt(settings, ["security"]) === "tls"
          ? mtls !== "self_signed" && String(getAt(settings, ["cdn", "host"]) ?? "").trim() !== ""
            ? "self"
            : ""
          : getAt(settings, ["reality", "target_kind"]) === "self"
            ? "site"
            : ""
        : info?.id === "mtproto"
          ? getAt(settings, ["fake_tls", "kind"]) === "site"
            ? ""
            : "site"
          : "";
  const altDone: "" | "self" | "selfTt" | "site" =
    mtls === "self_signed" && (info?.id === "hysteria2" || info?.id === "trusttunnel" || info?.id === "vless")
      ? info.id === "trusttunnel"
        ? "selfTt"
        : "self"
      : (info?.id === "vless" && getAt(settings, ["reality", "target_kind"]) === "site") || (info?.id === "mtproto" && getAt(settings, ["fake_tls", "kind"]) === "site")
        ? "site"
        : "";
  const vms: NodeVM[] = live.map((n, i) => {
    const a: Asked = asks[i]?.data ?? { state: "pending" };
    const nin = nodeIn(n);
    const fix = fixes[n.id] ?? {};
    const row = rowOf(a, nin, { t, protocol: info?.id ?? "", fix, alt: altDone });
    const need = row.need && !(row.port && fix.port);
    const hint = hintOf(column?.byNode.get(n.id));
    const kind = hint?.verdict === "ok" ? "ok" : hint?.verdict === "partial" ? "part" : hint?.verdict === "blocked" || hint?.verdict === "listed" ? "bad" : "none";
    const label = nin.entry ? (kind === "ok" ? t("wiz.reach.seen") : kind === "part" && hint?.dark.length ? t("wiz.reach.dark", { nets: hint.dark.join(", ") }) : kind === "bad" ? t("wiz.reach.closed") : t("wiz.reach.none")) : kind === "none" ? t("wiz.reach.none") : t("wiz.reach.direct");
    const on = !need && (picked[n.id] ?? row.on);
    return { id: n.id, name: n.name, countryCode: n.countryCode, linked: nin.linked, entry: nin.entry, reach: { kind, label }, row: { ...row, need }, on, fix, awg: nin.firstAwg ? { mode: asMode(n.awgBackend), auto: !!n.awgPrepare?.supported } : undefined, alt: altKind };
  });
  // "?node=": the node the profile is made for is the only one ticked at first
  const defaultOn = (v: NodeVM) => (fromNode ? v.id === fromNode && !v.row.need : v.row.on);
  const nodesOn = vms.map((v) => (picked[v.id] === undefined && !v.row.need ? { ...v, on: defaultOn(v) } : v));
  const candidates = nodesOn.filter((v) => !v.row.need && !v.entry);
  const allOn = candidates.length > 0 && candidates.every((v) => v.on);
  const chosen = nodesOn.filter((v) => v.on);
  const pickAll = () => setPicked(Object.fromEntries([...nodesOn.map((v) => [v.id, false] as const), ...candidates.map((v) => [v.id, !allOn] as const)]));
  const onAlt = () => {
    if (altKind === "self") edit(setAt(settings, ["tls_mode"], "self_signed"));
    else if (altKind === "site") {
      // another site needs its address: back to the settings, where its field is
      edit(info?.id === "mtproto" ? setAt(settings, ["fake_tls", "kind"], "site") : setAt(settings, ["reality", "target_kind"], "site"));
      setAll(true);
      setStep(2);
    }
  };

  // ---- step 4: the groups, and how the profile reaches the people in them
  const groupsQ = useQuery(groupsQuery);
  const plist = useQuery(profileListQuery).data ?? [];
  const sub = useQuery(settingsQuery).data;
  const linkApps = useLinkAppNames();
  const gs = groupsQ.data ?? [];
  const hasProto = (g: (typeof gs)[number]) => g.profileIds.some((id) => plist.find((p) => p.id === id)?.protocol === info?.id);
  const lacking = gs.filter((g) => !hasProto(g));
  const having = gs.filter(hasProto);
  const groupOn = (id: string) => groupPick[id] ?? (id === "" ? true : lacking.some((g) => g.id === id));
  const groupVMs: GroupVM[] = gs.length === 0 ? [{ id: "", name: t("users.everyone"), color: "sky", people: 0, on: groupOn("") }] : gs.map((g) => ({ id: g.id, name: g.name, color: g.color, people: g.userCount, on: groupOn(g.id) }));
  const chosenGroups = gs.filter((g) => groupOn(g.id));
  const everyone = gs.length === 0 && groupOn("");
  const people = chosenGroups.reduce((a, g) => a + g.userCount, 0);
  const way = info ? wayOf(info.id, [{ id: info.id, apps: info.apps }]) : "sub";
  const deliv = !info ? "" : telegramOnly(info.apps) ? t("wiz.deliv.tg") : trustTunnelOnly(info.apps) ? t("wiz.deliv.tt") : way === "awg" ? t("wiz.deliv.awg") : t("wiz.deliv.sub", { h: sub?.settings.updateIntervalHours || 12 });
  const touchedGroups = Object.keys(groupPick).length > 0;
  const appWords = !info ? "" : telegramOnly(info.apps) ? t("profiles.appTg") : trustTunnelOnly(info.apps) ? t("profiles.appTt") : way === "awg" ? t("profiles.appAwg") : t("profiles.appSub", { apps: linkApps });
  const quoted = (list: typeof gs) => list.map((g) => t("common.quoted", { name: g.name })).join(", ");
  const notes = touchedGroups || !info ? [] : [...(lacking.length > 0 && gs.length > 0 ? [t.n("profiles.afterFirst", lacking.length, { groups: quoted(lacking), proto: info.displayName, app: appWords })] : []), ...(having.length > 0 ? [t.n("profiles.afterSecond", having.length, { groups: quoted(having), proto: info.displayName })] : [])];

  // ---- step 5
  const created = useRef(false);
  const real = useRun(() => {
    created.current = true;
  });
  const run = demo?.run ?? real.run;
  const start = () => {
    if (!info) return;
    setStep(5);
    void real.start({
      protocol: info.id,
      name,
      settingsJson,
      groups: chosenGroups.map((g) => ({ id: g.id, name: g.name, profileIds: g.profileIds })),
      everyone,
      nodes: chosen.map((v) => {
        const n = live.find((x) => x.id === v.id)!;
        const fix: Fix = { ...v.fix, ...(v.fix.port === undefined && v.row.port?.free ? { port: v.row.port.free } : {}) };
        return { id: v.id, name: v.name, address: n.address, linked: v.linked, awgMode: n.awgBackend, firstAwg: !!v.awg, canPrepare: !!n.awgPrepare?.supported, fix };
      }),
    });
  };
  const done = step === 5 && runDone(run);

  // ---- leaving: any way out of the wizard (the sidebar, Back, a reload) asks first while something is typed or chosen
  const holding = !demo && !created.current && (touched || (step >= 3 && step < 5));
  const held = useRef(false);
  useEffect(() => {
    held.current = holding;
  }, [holding]);
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) => held.current && !created.current && next.pathname !== current.pathname,
    enableBeforeUnload: () => held.current && !created.current,
    withResolver: true,
  });
  const leave = () => (fromNode ? void navigate({ to: "/nodes/$id", params: { id: fromNode }, search: { tab: "profiles" } } as never) : go("/profiles"));

  const next = () => {
    if (step === 1) {
      const c = begin(pick);
      setCfg(c);
      setAll(pick === "own");
      setCdn("");
      setSaveProblems([]);
      setPicked({});
      setFixes({});
      setStep(2);
      url(c.protocol);
    } else if (step === 2) {
      if (!errs2) setStep(3);
    } else if (step === 3) setStep(4);
    else if (step === 4) start();
  };
  const prev = () => {
    if (step === 2) {
      setStep(1);
      url();
    } else setStep((step - 1) as 1 | 2 | 3 | 4);
  };

  const sumTitle = step === 1 ? "" : `${t(`wiz.t.${task.id}.t`)} · ${task.id === "own" ? (info?.displayName ?? "") : t(`wiz.t.${task.id}.p`)}`;
  const nodesN = chosen.length;
  const bar =
    step === 1
      ? `${t(`wiz.t.${taskById(pick).id}.t`)}${pick === "own" ? "" : ` · ${t(`wiz.t.${pick}.p`)}`}`
      : step === 2
        ? errs2
          ? t("wiz.fix")
          : name
        : step === 3
          ? nodesN > 0
            ? t.n("wiz.bar.nodes", nodesN)
            : t("wiz.bar.noNodes")
          : step === 4
            ? nodesN === 0
              ? t("wiz.bar.noNodes")
              : people > 0
                ? `${t.n("wiz.ppl", people)} · ${t.n("wiz.nodesN", nodesN)}`
                : t("wiz.bar.noGroups", { nodes: t.n("wiz.nodesN", nodesN) })
            : done
              ? t("wiz.bar.done")
              : t("wiz.bar.running");
  const nextText = step === 4 ? (nodesN > 0 ? t.n("wiz.create", nodesN) : t("wiz.createBare")) : t("wiz.go");
  const nextOff = step === 2 && errs2;
  const strip = [1, 2, 3, 4, 5].map((n) => ({ n, cls: step > n || (n === 5 && done) ? "pw-done" : step === n ? "pw-cur" : "" }));

  return (
    <div className={cx("pw", fx && "pw-fx")}>
      <AppLink to="/profiles" className="pw-back" onClick={(e) => (holding ? (e.preventDefault(), setLeaving(true)) : fromNode ? (e.preventDefault(), leave()) : undefined)}>
        <Gl d={P.back} />
        {t("wiz.back")}
      </AppLink>
      <header className="pw-h">
        <h1>{t("profiles.new")}</h1>
        <span>{sumTitle}</span>
        <em className="mono">{t("wiz.stepN", { n: step, m: 5 })}</em>
      </header>
      <ol className="pw-steps" aria-label={t("wiz.steps")}>
        {strip.map((s) => (
          <li key={s.n} className={s.cls} aria-current={step === s.n ? "step" : undefined}>
            <i>
              <Gl d={P.check} />
              <span className="mono">{s.n}</span>
            </i>
            {t(`wiz.step.${s.n as 1 | 2 | 3 | 4 | 5}`)}
          </li>
        ))}
      </ol>

      {step === 1 && <TaskStep tasks={shown} value={pick} onValue={setPick} t={t} />}
      {step === 2 && cfg && info && (
        <SettingsStep
          task={task}
          glyph={cfg.task === "own" ? glyphOf(info.id) : task.glyph}
          tone={cfg.task === "own" ? toneOf(info.id) : task.tone}
          info={info}
          choose={cfg.task === "own" ? protocols : undefined}
          name={name}
          nameError={nameError}
          portText={portText}
          portErr={portErr}
          cdn={cdn}
          cdnErr={!!cdnErr}
          all={all}
          settings={settings}
          base={cfg.base}
          problems={problems}
          advice={preview.data && preview.data.errors.length === 0 ? { warnings: preview.data.warnings, score: preview.data.obfuscationScore } : undefined}
          preview={{ text: preview.data?.clientPreview ?? "", errors: (preview.data?.errors.length ?? 0) > 0, failed: preview.isError ? errorText(preview.error, t) : "", busy: preview.isPending, formats: preview.data?.formats ?? [] }}
          onName={(v) => setCfg((c) => (c ? { ...c, typed: v } : c))}
          onPort={setPort}
          onCdn={setCdnHost}
          onOld={(v) => void setOld(v)}
          onAll={setAll}
          onSettings={(s) => edit(s)}
          onProtocol={(id) => {
            const c = begin("own", id);
            setCfg(c);
            setSaveProblems([]);
            setPicked({});
            setFixes({});
          }}
          onChangeTask={prev}
        />
      )}
      {step === 3 && info && <NodesStep t={t} name={name} nodes={nodesOn} allOn={allOn} onAll={pickAll} onPick={(id) => !nodesOn.find((v) => v.id === id)?.row.need && setPicked((p) => ({ ...p, [id]: !nodesOn.find((v) => v.id === id)!.on }))} onFix={(id, f) => setFixes((x) => ({ ...x, [id]: { ...x[id], ...f } }))} onAlt={onAlt} onAdd={() => addNode()} />}
      {step === 4 && info && <PeopleStep t={t} name={name} groups={groupVMs} sum={people} deliv={deliv} notes={notes} onPick={(id) => setGroupPick((p) => ({ ...p, [id]: !groupOn(id) }))} />}
      {step === 5 && (
        <DoneStep
          t={t}
          name={name}
          run={run}
          deliv={deliv}
          onRetry={(id, f) => void real.retry(id, f)}
          onFix={() => {
            // the profile was refused: its fields are marked on the settings step
            setSaveProblems([]);
            setStep(2);
          }}
        />
      )}

      <div className="pw-bar">
        <span>{bar}</span>
        {step > 1 && step < 5 && (
          <Button size="lg" onClick={prev}>
            <Gl d={P.back} />
            {t("wiz.prev")}
          </Button>
        )}
        {done && run.profile.id && (
          <AppLink to="/profiles/$id" params={{ id: run.profile.id }} className={buttonClass("secondary", "lg")}>
            {t("wiz.open")}
          </AppLink>
        )}
        {done && (
          <Button variant="primary" size="lg" onClick={leave}>
            {t("wiz.done")}
          </Button>
        )}
        {step < 5 && (
          <Button variant="primary" size="lg" className="pw-go" aria-disabled={nextOff} onClick={next}>
            {nextText}
          </Button>
        )}
      </div>

      <ConfirmModal
        open={leaving || blocker.status === "blocked"}
        onOpenChange={(o) => {
          if (o) return;
          setLeaving(false);
          if (!created.current) blocker.reset?.();
        }}
        title={t("profiles.discard.title")}
        description={t("profiles.discard.body")}
        confirmLabel={t("profiles.discard.ok")}
        danger
        onConfirm={async () => {
          created.current = true;
          if (blocker.status === "blocked") blocker.proceed();
          else leave();
        }}
      />
    </div>
  );
}
