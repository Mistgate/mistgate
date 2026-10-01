import { Code } from "@connectrpc/connect";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import type { Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileImpact, ProfileSummary, ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { Avatar, Card, DangerZone, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { groups as groupsApi, profiles } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { wayOf } from "@/screens/users/groups";
import { useGo } from "@/screens/users/nav";
import { fieldErrors, isCode, protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { randomPort } from "@/lib/awg";
import { avatarIndex } from "@/screens/users/format";
import { ConfirmModal, Panel, useDebounced } from "@/screens/users/ui";
import { Pending, QueryError } from "@/components/ui/query-error";
import { allFields, changedFields, getAt, makeSecret, parseSchema, parseSettings, setAt, withGeneratedSecrets, type Field, type Settings } from "./schema";
import { AfterCreate, autoName, useAfterCreate } from "./after-create";
import { AwgPanel, awgHidden, handTyped } from "./awg-panel";
import { ConfLine } from "./conf-preview";
import { DeployDialog, type DeployProfile } from "./deploy";
import { EgressNote } from "./egress-note";
import { SchemaForm, type Problem } from "./schema-form";
import { WherePanel } from "./where";

/** /profiles/new and /profiles/$id: one editor, the same form; a new profile starts from the plugin's defaults. */
export function ProfileEditorScreen() {
  const { id } = useParams({ strict: false }) as { id?: string };
  return !id || id === "new" ? <NewProfile /> : <EditProfile id={id} />;
}

function NewProfile() {
  const t = useTx();
  const protocols = useQuery(protocolsQuery);
  // "?node=<id>": made from that node's page; the profile goes on it and the editor returns there
  const { node = "" } = useSearch({ strict: false }) as { node?: string };
  const [choice, setChoice] = useState("");
  if (protocols.isPending) return <Pending />;
  if (protocols.isError) return <QueryError error={protocols.error} onRetry={() => void protocols.refetch()} />;
  const info = protocols.data.find((p) => p.id === choice) ?? protocols.data[0];
  if (!info) return <EmptyState title={t("profiles.noSchema")} />;
  return <NewEditor key={info.id} info={info} choices={protocols.data} onProtocol={setChoice} fromNode={node} />;
}

/** A new profile starts from the plugin defaults with every secret already made here (visible, never empty). */
function NewEditor({ info, choices, onProtocol, fromNode }: { info: ProtocolInfo; choices: ProtocolInfo[]; onProtocol: (id: string) => void; fromNode: string }) {
  const initial = useMemo(() => {
    const fields = allFields(parseSchema(info.settingsSchemaJson));
    const settings = withGeneratedSecrets(parseSettings(info.defaultSettingsJson), fields, makeSecret);
    return { name: "", settingsJson: JSON.stringify(settings) };
  }, [info.settingsSchemaJson, info.defaultSettingsJson]);
  return <Editor info={info} initial={initial} choices={choices} onProtocol={onProtocol} fromNode={fromNode} />;
}

function EditProfile({ id }: { id: string }) {
  const t = useTx();
  const go = useGo();
  const protocols = useQuery(protocolsQuery);
  const q = useQuery({
    queryKey: ["profiles", "detail", id],
    queryFn: ({ signal }) => profiles.getProfile({ profileId: id }, { signal }),
  });
  if (q.isPending || protocols.isPending) return <Pending />;
  if (q.isError) {
    if (isCode(q.error, Code.NotFound)) {
      return (
        <EmptyState
          title={t("profiles.notFound")}
          action={
            <Button variant="secondary" size="lg" onClick={() => go("/profiles")}>
              {t("profiles.back")}
            </Button>
          }
        >
          {t("profiles.notFoundBody")}
        </EmptyState>
      );
    }
    return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  }
  if (protocols.isError) return <QueryError error={protocols.error} onRetry={() => void protocols.refetch()} />;
  const profile = q.data.profile!;
  const info = protocols.data.find((p) => p.id === profile.protocol);
  if (!info) return <EmptyState title={t("profiles.notFound")}>{profile.protocol}</EmptyState>;
  // remounted when the saved version (or name) changes, so the form always starts from what the server holds
  return (
    <Editor
      key={`${profile.id}:${profile.version}:${profile.name}`}
      info={info}
      profile={profile}
      inbounds={q.data.inbounds}
      initial={{ name: profile.name, settingsJson: q.data.settingsJson }}
    />
  );
}

type EditorProps = {
  info: ProtocolInfo;
  initial: { name: string; settingsJson: string };
  profile?: ProfileSummary;
  inbounds?: Inbound[];
  /** New profile: the protocols to choose from. */
  choices?: ProtocolInfo[];
  onProtocol?: (id: string) => void;
  /** New profile made from this node's page: it goes on that node, and the editor goes back there. */
  fromNode?: string;
};

function Editor({ info, initial, profile, inbounds = [], choices, onProtocol, fromNode = "" }: EditorProps) {
  const t = useTx();
  const toast = useToast();
  const go = useGo();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const isNew = !profile;

  const groups = useMemo(() => parseSchema(info.settingsSchemaJson), [info.settingsSchemaJson]);
  const fields = useMemo(() => allFields(groups), [groups]);
  const base = useMemo(() => parseSettings(initial.settingsJson), [initial.settingsJson]);
  const [settings, setSettings] = useState<Settings>(base);
  // a new profile's name follows the port ("Hysteria2 · 443") until it is typed
  const [typed, setTyped] = useState<string | null>(isNew ? null : initial.name);
  const name = typed ?? autoName(info, settings);
  const [nameError, setNameError] = useState(false);
  const [saveProblems, setSaveProblems] = useState<Problem[]>([]);
  const [notice, setNotice] = useState<"stale" | null>(null);
  // kept after the dialog closes, so its text does not go blank while it fades out
  const [impact, setImpact] = useState<{ impact: ProfileImpact; fields: string[] } | null>(null);
  const [impactOpen, setImpactOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const after = useAfterCreate(info, fromNode, isNew);
  // the new profile, while it is put on the nodes chosen in "Right after it is created"
  const [deploy, setDeploy] = useState<{ profile: DeployProfile; nodeIds: string[] } | null>(null);

  const settingsJson = JSON.stringify(settings);
  const debounced = useDebounced(settingsJson, 350);
  // gcTime 0: a typed secret sits in the query key, so nothing keeps it after the screen moves on
  const preview = useQuery({
    queryKey: ["profiles", "preview", info.id, debounced],
    queryFn: ({ signal }) => profiles.previewProfile({ protocol: info.id, settingsJson: debounced }, { signal }),
    placeholderData: keepPreviousData,
    gcTime: 0,
    staleTime: 0,
  });
  const problems: Problem[] = saveProblems.length > 0 ? saveProblems : (preview.data?.errors ?? []);

  const changed = changedFields(fields, settings, base);
  const nameChanged = !isNew && name !== initial.name;
  const count = changed.length + (nameChanged ? 1 : 0);
  const titleOf = (key: string) => {
    const f = fields.find((x) => x.key === key);
    return f ? (t.opt(`profiles.f.${info.id}.${f.id}.title`) ?? f.title) : key;
  };

  const edit = (next: Settings) => {
    setSettings(next);
    setSaveProblems([]);
    setNotice(null);
  };
  const discard = () => {
    setSettings(base);
    setTyped(isNew ? null : initial.name);
    setNameError(false);
    setSaveProblems([]);
    setNotice(null);
  };

  /** Where a new profile's page is, or the node it was made from. */
  const leave = (id: string) =>
    fromNode ? void navigate({ to: "/nodes/$id", params: { id: fromNode }, search: { tab: "profiles" } } as never) : go("/profiles/$id", { params: { id }, replace: true });

  /** A new profile: made, then into the chosen groups (cheap, and they decide who gets it), then onto the chosen nodes. */
  async function create() {
    const res = await profiles.createProfile({ protocol: info.id, name: name.trim(), settingsJson });
    const p = res.profile!;
    const plan = after.plan;
    if (plan.groupsOn) {
      try {
        if (after.groups.length === 0) {
          if (plan.everyone) await groupsApi.createGroup({ name: t("users.everyone"), profileIds: [p.id] });
        } else {
          for (const g of after.groups.filter((x) => plan.groupIds.includes(x.id))) await groupsApi.updateGroup({ groupId: g.id, profileIds: { values: [...g.profileIds, p.id] } });
        }
      } catch (e) {
        toast.error(errorText(e, t)); // the profile is there: the rest of the way goes on
      }
    }
    await Promise.all(["profiles", "groups", "users", "subs"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    const nodeIds = plan.nodesOn ? plan.nodeIds.filter((id) => after.nodes.some((n) => n.id === id)) : [];
    if (nodeIds.length > 0) {
      setDeploy({ profile: { id: p.id, name: p.name, protocol: p.protocol, tlsMode: String(getAt(settings, ["tls_mode"]) ?? ""), sni: String(getAt(settings, ["sni"]) ?? "") }, nodeIds });
      return;
    }
    toast(t("profiles.created"));
    leave(p.id);
  }

  async function save(confirmed = false) {
    if (!name.trim()) {
      setNameError(true);
      return;
    }
    setBusy(true);
    setNotice(null);
    try {
      if (!profile) return await create();
      const request = {
        profileId: profile.id,
        expectedVersion: profile.version,
        ...(nameChanged && { name: name.trim() }),
        ...(changed.length > 0 && { settingsJson }),
      };
      const critical = changed.filter((f) => f.critical);
      if (critical.length > 0 && !confirmed) {
        const res = await profiles.updateProfile({ ...request, dryRun: true });
        if (res.impact) {
          setImpact({ impact: res.impact, fields: critical.map((f) => titleOf(f.key)) });
          setImpactOpen(true);
          return;
        }
      }
      await profiles.updateProfile(request);
      setImpactOpen(false);
      await qc.invalidateQueries({ queryKey: ["profiles"] });
      toast(profile.nodeCount > 0 ? t.n("profiles.savedRollout", profile.nodeCount) : t("profiles.saved"));
    } catch (e) {
      setImpactOpen(false);
      const fe = fieldErrors(e);
      if (isCode(e, Code.Aborted)) setNotice("stale");
      else if (fe.length > 0) setSaveProblems(fe.map((x) => ({ pointer: x.pointer, code: x.code, message: x.message })));
      else toast.error(errorText(e, t));
    } finally {
      setBusy(false);
    }
  }

  // AmneziaWG: its own cards for the version and the look; the client networks are fixed once the profile is on a node
  const isAwg = info.id === "awg";
  const locked = isAwg && inbounds.length > 0;
  const readOnly = useMemo<ReadonlySet<string>>(() => (locked ? new Set(["subnet4", "subnet6"]) : new Set()), [locked]);
  const extra = (f: Field): ReactNode => {
    if (isAwg && f.id === "port") {
      return (
        <button type="button" className="w-fit text-[11px] font-bold text-accent-text" onClick={() => edit(setAt(settings, ["port"], randomPort()))}>
          {t("awg.port.random")}
        </button>
      );
    }
    if (locked && (f.id === "subnet4" || f.id === "subnet6")) return <span className="text-[11px] leading-snug text-warn-text">{t("awg.subnet.locked")}</span>;
    if (f.id === "egress" && getAt(settings, f.path) === "warp") return <EgressNote nodeIds={[...new Set(inbounds.map((i) => i.nodeId))]} />;
    return null;
  };

  // an empty name does not grey the button out: the click says what is missing, at the field
  const canSave = !busy && problems.length === 0 && (isNew || count > 0);
  const nodes = profile ? `${profile.nodeCount} ${t.n("profiles.nodes", profile.nodeCount)} · ${profile.userCount} ${t.n("profiles.users", profile.userCount)}` : info.displayName;
  const certOf = fields.some((f) => f.id === "tls_mode") ? { tlsMode: String(getAt(base, ["tls_mode"]) ?? ""), sni: String(getAt(base, ["sni"]) ?? "") } : undefined;

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex items-center gap-3">
        <IconButton aria-label={t("profiles.back")} onClick={() => go("/profiles")}>
          <Icon name="back" />
        </IconButton>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <PageTitle className="truncate">{initial.name || t("profiles.new")}</PageTitle>
          <span className="text-xs text-muted">{nodes}</span>
        </div>
      </div>

      {notice === "stale" && (
        <Notice tone="danger">
          {t("profiles.stale")}
          <Button className="ml-3" size="sm" onClick={() => void qc.invalidateQueries({ queryKey: ["profiles"] })}>
            {t("profiles.reload")}
          </Button>
        </Notice>
      )}

      {profile && (
        <WherePanel
          profile={profile}
          inbounds={inbounds}
          cert={certOf}
          twin={fields.some((f) => f.id === "egress") ? { egress: getAt(base, ["egress"]) === "warp" ? "warp" : "direct", dirty: count > 0 } : undefined}
        />
      )}

      <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-3.5">
          <Card lg className="flex flex-col gap-3 p-4">
            {choices && choices.length > 1 && (
              <div className="flex flex-col gap-1.5">
                <SectionLabel icon="layers" tone="sky">
                  {t("profiles.protocol")}
                </SectionLabel>
                <Segmented aria-label={t("profiles.protocol")} value={info.id} onValueChange={(v) => onProtocol?.(v)} options={choices.map((c) => ({ value: c.id, label: c.displayName }))} />
              </div>
            )}
            <TextField
              icon="tag"
              tone="lavender"
              label={t("profiles.name")}
              hint={t("profiles.nameHint")}
              error={nameError && !name.trim() ? t("profiles.nameRequired") : undefined}
              value={name}
              onChange={(e) => {
                setTyped(e.target.value);
                setNameError(false);
              }}
              placeholder={t("profiles.namePh")}
              autoComplete="off"
              maxLength={64}
            />
          </Card>
          {isNew && <AfterCreate a={after} info={info} settings={settings} />}
          {isAwg && <AwgPanel settings={settings} onChange={edit} advice={preview.data && preview.data.errors.length === 0 ? { warnings: preview.data.warnings, score: preview.data.obfuscationScore } : undefined} />}
          <SchemaForm protocol={info.id} groups={groups} settings={settings} base={base} problems={problems} onChange={isAwg ? (n) => edit(handTyped(settings, n)) : edit} hidden={isAwg ? awgHidden : undefined} readOnly={readOnly} extra={extra} />
          {profile && <DeleteSection profile={profile} inbounds={inbounds} />}
        </div>

        <Panel
          title={t("profiles.preview")}
          icon="code"
          tone="mint"
          aside={<span className="text-[11px] text-muted">{preview.data?.clientLabel}</span>}
          className="md:sticky md:top-[72px]"
        >
          {preview.isError ? (
            <Notice tone="danger">{errorText(preview.error, t)}</Notice>
          ) : (preview.data?.errors.length ?? 0) > 0 ? (
            <Notice>{t("profiles.previewErrors")}</Notice>
          ) : (
            <div className="rounded-xl border border-line bg-canvas p-3 font-mono text-[11px] leading-[1.65]">
              {preview.data ? preview.data.clientPreview.split("\n").map((line, i) => <ConfLine key={i} text={line} />) : <span className="text-muted">{t("profiles.previewBusy")}</span>}
            </div>
          )}
          <span className="text-xs leading-normal text-pretty text-muted">{t("profiles.previewHint")}</span>
        </Panel>
      </div>

      {(count > 0 || isNew) && (
        <div className="screen-enter sticky bottom-[84px] z-[3] flex flex-wrap items-center gap-2 rounded-card border border-accent-line bg-surface py-2 pr-2 pl-4 md:bottom-4">
          <span className="min-w-[140px] flex-1 text-[13px] font-bold">{count > 0 ? t("profiles.dirty", { n: count }) : info.displayName}</span>
          {count > 0 && (
            <Button variant="ghost" onClick={discard}>
              {t("profiles.reset")}
            </Button>
          )}
          <Button variant="primary" disabled={!canSave} onClick={() => void save()}>
            {isNew ? t("profiles.createBtn") : t("profiles.save")}
          </Button>
        </div>
      )}

      <ImpactModal impact={impact} userCount={profile?.userCount ?? 0} protocol={info} open={impactOpen} onClose={() => setImpactOpen(false)} onConfirm={() => save(true)} t={t} />
      {deploy && (
        <DeployDialog
          open
          onOpenChange={(o) => {
            if (o) return;
            const id = deploy.profile.id;
            setDeploy(null);
            leave(id);
          }}
          profile={deploy.profile}
          inbounds={[]}
          preselect={deploy.nodeIds}
          autorun
          onFinished={(s) => {
            if (s.failed > 0) return; // the window stays: the reasons and the fields are in it
            const id = deploy.profile.id;
            setDeploy(null);
            toast(t.n("profiles.createdOn", s.ok));
            leave(id);
          }}
        />
      )}
    </div>
  );
}

/**
 * "Apply critical changes?" with what they really do. A key-based profile (AmneziaWG) names the keys that stop working. A
 * profile the subscription carries (Hysteria2) breaks for everyone who has it until their app refreshes the subscription,
 * so it says that, with the two ways around it; otherwise the people online reconnect.
 */
function ImpactModal({
  impact,
  userCount,
  protocol,
  open,
  onClose,
  onConfirm,
  t,
}: {
  impact: { impact: ProfileImpact; fields: string[] } | null;
  userCount: number;
  protocol: ProtocolInfo;
  open: boolean;
  onClose: () => void;
  onConfirm: () => Promise<unknown>;
  t: Tx;
}) {
  const i = impact?.impact;
  const bySub = wayOf(protocol.id, [{ id: protocol.id, apps: protocol.apps }]) === "sub";
  const subs = useQuery({ ...settingsQuery, enabled: open && bySub });
  const hours = subs.data?.settings.updateIntervalHours || 12;
  const broken = !!i && bySub && i.criticalFields.length > 0;
  const head: ReactNode = !i
    ? ""
    : i.devicesNeedReissue > 0
      ? t.n("profiles.crit.headDevices", i.devicesNeedReissue)
      : broken
        ? i.inboundsRestarted > 0
          ? t.n("profiles.crit.headSub", Math.max(userCount, i.affectedUserNames.length), { h: hours })
          : t("profiles.crit.headNowhere")
        : i.usersOnline > 0
          ? t.n("profiles.crit.headOnline", i.usersOnline)
          : t("profiles.crit.headNone");
  return (
    <ConfirmModal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={t("profiles.crit.title")}
      confirmLabel={i && i.devicesNeedReissue > 0 ? t("profiles.crit.applyReissue") : t("profiles.crit.apply")}
      onConfirm={onConfirm}
    >
      {i && (
        <>
          <Notice title={head} className="items-start">
            {t("profiles.crit.body", { fields: impact!.fields.join(", ") })} {i.inboundsRestarted > 0 && t.n("profiles.crit.inbounds", i.inboundsRestarted)}
            {broken && i.inboundsRestarted > 0 && <span className="mt-1 block">{t("profiles.crit.subAdvice")}</span>}
          </Notice>
          {i.affectedUserNames.length > 0 && (
            <div className="flex max-h-56 flex-col overflow-y-auto">
              <span className="pb-1.5 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("profiles.crit.users")}</span>
              {i.affectedUserNames.map((n) => (
                <div key={n} className="flex min-h-[38px] items-center gap-2.5 border-t border-line">
                  <Avatar name={n} index={avatarIndex(n)} size={24} />
                  <b className="min-w-0 flex-1 truncate text-[13px]">{n}</b>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </ConfirmModal>
  );
}

function DeleteSection({ profile, inbounds }: { profile: ProfileSummary; inbounds: Inbound[] }) {
  const t = useTx();
  const toast = useToast();
  const go = useGo();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [deployed, setDeployed] = useState(false);
  const nodeNames = [...new Set(inbounds.map((i) => i.nodeName))];

  async function remove() {
    try {
      await profiles.deleteProfile({ profileId: profile.id });
    } catch (e) {
      // FAILED_PRECONDITION: still deployed. The explanation names the nodes; anything else is a toast.
      if (isCode(e, Code.FailedPrecondition)) setDeployed(true);
      else toast.error(errorText(e, t));
      throw e;
    }
    await qc.invalidateQueries({ queryKey: ["profiles"] });
    toast(t("profiles.deleted"));
    go("/profiles", { replace: true });
  }

  return (
    <DangerZone title={t("profiles.danger")}>
      <div className="flex items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[13px] font-bold">{t("profiles.delete")}</span>
          <span className="text-[11px] leading-snug text-muted">{t("profiles.deleteHint")}</span>
        </div>
        <Button
          variant="danger"
          onClick={() => {
            setDeployed(false);
            setOpen(true);
          }}
        >
          {t("users.delete")}
        </Button>
      </div>
      <ConfirmModal
        open={open}
        onOpenChange={setOpen}
        title={t("profiles.deleteT", { name: profile.name })}
        description={t("profiles.deleteBody")}
        confirmLabel={t("users.delete")}
        danger
        onConfirm={remove}
      >
        {(deployed || nodeNames.length > 0) && <Notice tone={deployed ? "danger" : "warn"}>{t("profiles.deployed", { nodes: nodeNames.join(", ") || "—" })}</Notice>}
      </ConfirmModal>
    </DangerZone>
  );
}
