import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useState, type FormEvent } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { PrepareAwgOutcome, type GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { groups as groupsApi, nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { codedError } from "@/lib/coded-error";
import { cx } from "@/lib/cx";
import { errorCode, errorText, errorVars } from "@/lib/errors";
import type { Plain } from "@/lib/plain";
import { protocolName } from "@/lib/series";
import { groupsQuery, profileListQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { asMode, AwgBackendChoice, canPrepare, useSaveAwgBackend, type AwgMode } from "./awg-backend";
import { portOk, sniPlaceholder, useInboundCheck, useInboundRefresh, WarpWarnings } from "./inbound-check";

type Node = NonNullable<Plain<GetNodeResponse>["node"]>;

const isIp = (address: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(address) || address.includes(":");

/** A profile in the list: its name, and under it the line that tells it apart ("Hysteria2 · UDP 443 · Salamander · Let's Encrypt · exit via WARP"). */
export function profileLine(t: Tx, p: Pick<ProfileSummary, "protocol" | "summary">): string {
  const words: Record<string, string> = { "Self-signed": t("node.profiles.sum.selfSigned"), "No obfuscation": t("node.profiles.sum.noObfs"), WARP: t("node.profiles.sum.warp") };
  const parts = (p.summary ?? "")
    .split(" · ")
    .filter(Boolean)
    .map((s) => words[s] ?? s.replace(/^hop (\d+)-(\d+)$/, (_, a: string, b: string) => t("node.profiles.sum.hop", { range: `${a}–${b}` })));
  return [protocolName(p.protocol), ...parts].join(" · ");
}

/**
 * "Add a profile to de1". The check before the click says, next to the field it is about, what the server would refuse
 * (a Let's Encrypt profile on an IP node, a port another profile holds, ...) and what the node lacks (WARP); a port that
 * is taken is replaced by a free one at once. With no profile in the panel yet, the dialog makes one and puts it here.
 */
export function AddInbound({ open, onOpenChange, data, initialProfile }: { open: boolean; onOpenChange: (o: boolean) => void; data: Plain<GetNodeResponse>; initialProfile?: string }) {
  const t = useTx();
  const node = data.node!;
  const list = useQuery({ ...profileListQuery, enabled: open });
  const [selfSigned, setSelfSigned] = useState(false); // the self-signed maker, opened from the Let's Encrypt block
  const all = list.data ?? [];
  const here = new Set(data.inbounds.map((i) => i.profileId));
  const available = all.filter((p) => !here.has(p.id));
  const close = () => {
    onOpenChange(false);
    setSelfSigned(false);
  };

  return (
    <Modal
      open={open}
      onOpenChange={(o) => (o ? onOpenChange(true) : close())}
      title={t("node.profiles.addTitle", { name: node.name })}
      description={t("node.profiles.addBody")}
    >
      {list.isPending ? (
        <p className="text-sm text-muted">{t("common.loading")}</p>
      ) : all.length === 0 || selfSigned ? (
        <QuickProfile node={node} taken={all.map((p) => p.name)} selfSigned={selfSigned} onDone={close} onBack={selfSigned ? () => setSelfSigned(false) : undefined} />
      ) : available.length === 0 ? (
        <div className="flex flex-col gap-3">
          <Notice>{t("node.profiles.allDeployed")}</Notice>
          <div className="flex justify-end">
            <Button variant="ghost" size="md" onClick={close}>
              {t("common.close")}
            </Button>
          </div>
        </div>
      ) : (
        <AddForm data={data} available={available} initialProfile={initialProfile} onDone={close} onSelfSigned={() => setSelfSigned(true)} />
      )}
    </Modal>
  );
}

function AddForm({
  data,
  available,
  initialProfile,
  onDone,
  onSelfSigned,
}: {
  data: Plain<GetNodeResponse>;
  available: ProfileSummary[];
  initialProfile?: string;
  onDone: () => void;
  onSelfSigned: () => void;
}) {
  const t = useTx();
  const toast = useToast();
  const node = data.node!;
  const refresh = useInboundRefresh(node.id);
  const saveBackend = useSaveAwgBackend(node.id);
  const [profileId, setProfileId] = useState("");
  const [port, setPort] = useState("");
  const [sni, setSni] = useState("");
  const [backend, setBackend] = useState<AwgMode | null>(null);
  // the port the check found taken, and the free one put into the field instead (decision: say it, offer a free port)
  const [auto, setAuto] = useState<Record<string, string> | null>(null);
  const sniId = useId();
  const chosen = profileId || available.find((p) => p.id === initialProfile)?.id || available[0]?.id || "";
  const profile = available.find((p) => p.id === chosen);
  const isAwg = profile?.protocol === "awg";
  const savedBackend = asMode(node.awgBackend);
  const mode = backend ?? savedBackend;

  const check = useInboundCheck(chosen && portOk(port) ? { kind: "create", nodeId: node.id, profileId: chosen, port, sni: isAwg ? "" : sni.trim() } : null);
  const refused = check.state === "refused" ? check : null;
  if (refused?.code === "port_taken" && port === "" && !auto && Number(refused.vars.free) > 0) {
    setAuto(refused.vars);
    setPort(refused.vars.free!);
  }
  // a Let's Encrypt profile on an IP node: the domain field is where it gets fixed, so the cursor goes there (unless the
  // admin is typing in another field)
  const acme = refused?.code === "acme_needs_domain";
  useEffect(() => {
    if (acme && chosen && !(document.activeElement instanceof HTMLInputElement)) document.getElementById(sniId)?.focus();
  }, [acme, chosen, sniId]);

  const add = useMutation({
    mutationFn: async () => {
      // the node has one AmneziaWG backend: a new choice is saved first, so the profile starts on it
      if (isAwg && mode !== savedBackend) {
        // The kernel module on a node that builds it by itself: the setting becomes "kernel" only when the module is already
        // there; otherwise the profile is not added with a mode that would stop it (the module is prepared in the node's settings).
        if (mode === "kernel" && canPrepare(node)) {
          const plan = await nodesApi.prepareAwgKernel({ nodeId: node.id, confirm: false });
          if (plan.outcome !== PrepareAwgOutcome.READY) throw new ConnectError("kernel_not_ready", Code.FailedPrecondition);
        }
        await saveBackend(mode);
      }
      return profilesApi.createInbound({ profileId: chosen, nodeId: node.id, portOverride: port ? Number(port) : 0, tlsServerNameOverride: isAwg ? "" : sni.trim() });
    },
    onSuccess: () => {
      onDone();
      toast(t("node.profiles.added", { name: node.name }));
      refresh();
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (chosen && portOk(port) && !refused) add.mutate();
  }

  const taken = refused?.code === "port_taken" ? refused.vars : null;
  const free = Number(taken?.free ?? 0);
  const portError = !portOk(port)
    ? t("node.profiles.portError")
    : taken
      ? taken.hop
        ? t("node.check.port.hop", { port: taken.port!, node: node.name, profile: taken.profile!, hop: taken.hop })
        : t("err.port_taken", taken)
      : refused?.code === "port_in_hop"
        ? t("err.port_in_hop", refused.vars)
        : undefined;
  const sniError = refused?.code === "sni_needs_domain" || refused?.code === "sni_invalid" ? t(`err.${refused.code}`, refused.vars) : undefined;
  const effective = check.state === "ok" ? check.inbound : undefined;
  const blocking = refused && ["hop_taken", "already_on_node", "node_retired"].includes(refused.code) ? refused : null;

  return (
    <form onSubmit={submit} className="flex flex-col gap-3.5">
      <div className="flex flex-col gap-1.5">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.profiles.profile")}</span>
        <Select
          value={chosen}
          onValueChange={(v) => {
            setProfileId(v);
            setPort("");
            setAuto(null);
          }}
          aria-label={t("node.profiles.profile")}
          options={available.map((p) => ({ value: p.id, label: p.name, hint: profileLine(t, p) }))}
        />
        {profile && <span className="text-xs text-muted">{profileLine(t, profile)}</span>}
      </div>
      <div className="flex flex-col gap-2">
        <TextField
          label={t("node.profiles.portOverride")}
          value={port}
          onChange={(e) => {
            setPort(e.target.value.replace(/\D/g, "").slice(0, 5));
            setAuto(null);
          }}
          inputMode="numeric"
          placeholder={effective?.port && !port ? t("node.profiles.portFrom", { port: effective.port }) : t("node.profiles.fromProfile")}
          mono
          error={portError}
        />
        {auto && <p className="text-xs leading-snug text-muted">{t("node.check.port.auto", { from: auto.port!, profile: auto.profile!, to: auto.free! })}</p>}
        {taken && (
          <div className="flex flex-wrap items-center gap-2">
            {free > 0 ? (
              <Button variant="secondary" size="sm" onClick={() => setPort(String(free))}>
                {t("node.check.port.take", { port: free })}
              </Button>
            ) : (
              <span className="text-xs text-muted">{t("node.check.port.none")}</span>
            )}
          </div>
        )}
      </div>
      {isAwg ? (
        <AwgBackendChoice data={data} mode={mode} onChange={setBackend} />
      ) : (
        <TextField
          id={sniId}
          label={t("node.profiles.tlsOverride")}
          value={sni}
          onChange={(e) => setSni(e.target.value)}
          placeholder={acme ? "vpn.example.com" : sniPlaceholder(t, effective?.tlsServerName, node.address)}
          hint={acme ? undefined : t("node.profiles.tlsHint")}
          error={sniError}
          mono
          autoCapitalize="off"
          spellCheck={false}
        />
      )}
      {acme && profile && <AcmeBlock vars={refused.vars} profile={profile} nodeName={node.name} onEnter={() => document.getElementById(sniId)?.focus()} onSelfSigned={onSelfSigned} />}
      {blocking && (
        <Notice tone="danger" className="items-start">
          <span className="flex flex-col items-start gap-1.5">
            {t(`err.${blocking.code}`, blocking.vars)}
            {blocking.code === "hop_taken" && (
              <Link to="/profiles/$id" params={{ id: chosen }} className="font-bold underline decoration-dotted underline-offset-2">
                {t("node.check.openProfile")}
              </Link>
            )}
          </span>
        </Notice>
      )}
      <WarpWarnings check={check} nodeId={node.id} nodeName={node.name} onLeave={onDone} />
      {add.isError && (
        <Notice tone="danger" className="items-start">
          {codedError(add.error, t, "awg.err")}
        </Notice>
      )}
      <div className="flex justify-end gap-2 pt-1">
        <Button variant="ghost" size="md" onClick={onDone}>
          {t("common.cancel")}
        </Button>
        <Button type="submit" variant="primary" size="md" disabled={add.isPending || !portOk(port) || !!refused || !chosen}>
          {t("node.profiles.addDo")}
        </Button>
      </div>
    </form>
  );
}

/**
 * Let's Encrypt and a node without a domain: enter one (the field is right above), or take a self-signed certificate. The
 * profile itself switches when it runs nowhere yet; otherwise a self-signed profile is made for this node, so the nodes
 * that have a domain keep Let's Encrypt. Said honestly: the pinned certificate is verified with two clients, not with Happ.
 */
function AcmeBlock({ vars, profile, nodeName, onEnter, onSelfSigned }: { vars: Record<string, string>; profile: ProfileSummary; nodeName: string; onEnter: () => void; onSelfSigned: () => void }) {
  const t = useTx();
  const qc = useQueryClient();
  const switchIt = useMutation({
    mutationFn: () => profilesApi.updateProfile({ profileId: profile.id, settingsJson: JSON.stringify({ tls_mode: "self_signed" }), expectedVersion: profile.version }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["profiles"] });
      void qc.invalidateQueries({ queryKey: ["inbound-check"] });
    },
  });
  const unused = profile.nodeCount === 0;
  return (
    <section className="screen-enter flex flex-col gap-3 rounded-field border border-warn-line bg-warn-soft px-3.5 py-3">
      <div className="flex flex-col gap-1">
        <b className="text-[13px] leading-snug">{t("node.check.acme.title")}</b>
        <p className="text-xs leading-snug text-pretty">{t("node.check.acme.body", { node: vars.node ?? nodeName, address: vars.address ?? "" })}</p>
        <div className="pt-1">
          <Button variant="secondary" size="sm" onClick={onEnter}>
            {t("node.check.acme.enter")}
          </Button>
        </div>
      </div>
      <div className="flex flex-col gap-1 border-t border-warn-line pt-3">
        <b className="text-[13px] leading-snug">{t("node.check.self.title")}</b>
        <p className="text-xs leading-snug text-pretty">{t("node.check.self.body")}</p>
        <div className="flex flex-col items-start gap-1 pt-1">
          {/* a long profile name wraps instead of running out of the sheet */}
          {unused ? (
            <Button variant="secondary" size="sm" className="h-auto! min-h-7 py-1 text-left whitespace-normal!" disabled={switchIt.isPending} onClick={() => switchIt.mutate()}>
              {t("node.check.self.switch", { profile: profile.name })}
            </Button>
          ) : (
            <Button variant="secondary" size="sm" className="h-auto! min-h-7 py-1 text-left whitespace-normal!" onClick={onSelfSigned}>
              {t("node.check.self.create", { node: nodeName })}
            </Button>
          )}
          <span className="text-[11px] leading-snug text-muted">{unused ? t("node.check.self.switchHint") : t("node.check.self.createHint", { profile: profile.name })}</span>
        </div>
        {switchIt.isError && (
          <Notice tone="danger" className="mt-1 items-start">
            {errorText(switchIt.error, t)}
          </Notice>
        )}
      </div>
    </section>
  );
}

/** "Все" (made at setup), or the one group there is: where everybody is. */
export function defaultGroup(groups: readonly Group[]): Group | undefined {
  return groups.find((g) => /^(все|everyone|all)$/i.test(g.name.trim())) ?? (groups.length === 1 ? groups[0] : undefined);
}

/** name, or name 2, name 3, ... when it is taken. */
export function freeName(name: string, taken: readonly string[]): string {
  const used = new Set(taken.map((n) => n.toLowerCase()));
  for (let n = 1; ; n++) {
    const c = n === 1 ? name : `${name} ${n}`;
    if (!used.has(c.toLowerCase())) return c;
  }
}

/**
 * A Hysteria2 profile made and put on the node in one go: the usual settings, Let's Encrypt (with the node's domain, or the
 * domain typed here for an IP node) or, chosen explicitly, a self-signed certificate. The groups it goes into follow the
 * owner's rule: "Все" is ticked only while it has no Hysteria2 profile yet.
 */
function QuickProfile({ node, taken, selfSigned, onDone, onBack }: { node: Node; taken: string[]; selfSigned: boolean; onDone: () => void; onBack?: () => void }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const ip = isIp(node.address);
  const [cert, setCert] = useState<"acme" | "self">(selfSigned ? "self" : "acme");
  const [domain, setDomain] = useState("");
  const groups = useQuery(groupsQuery);
  const profiles = useQuery(profileListQuery);
  const [picked, setPicked] = useState<string[] | null>(null); // null: the owner's default, until a chip is clicked
  const hy2 = new Set((profiles.data ?? []).filter((p) => p.protocol === "hysteria2").map((p) => p.id));
  const def = defaultGroup(groups.data ?? []);
  const defHas = !!def && def.profileIds.some((id) => hy2.has(id));
  const chosen = picked ?? (def && !defHas ? [def.id] : []);
  const self = cert === "self";
  const needDomain = !self && ip;
  const port = 443;

  const create = useMutation({
    mutationFn: async () => {
      const name = freeName(t(self ? "node.quick.nameSelf" : "node.quick.name", { port }), taken);
      const made = (await profilesApi.createProfile({ protocol: "hysteria2", name, settingsJson: JSON.stringify({ tls_mode: self ? "self_signed" : "acme_domain" }) })).profile!;
      const sni = needDomain ? domain.trim() : "";
      // 443 unless another profile of the node holds it: then the free port the server picks, and the name says it.
      // A step that fails leaves the profile made: the dialog then lists it, with the reason next to it.
      let final = name;
      try {
        await profilesApi.createInbound({ profileId: made.id, nodeId: node.id, tlsServerNameOverride: sni, validateOnly: true });
      } catch (e) {
        const msg = ConnectError.from(e).rawMessage;
        const free = Number(errorVars(msg).free);
        if (errorCode(msg) !== "port_taken" || !(free > 0)) throw e;
        final = freeName(t(self ? "node.quick.nameSelf" : "node.quick.name", { port: free }), taken);
        await profilesApi.updateProfile({ profileId: made.id, name: final, settingsJson: JSON.stringify({ port: free }), expectedVersion: made.version });
      }
      await profilesApi.createInbound({ profileId: made.id, nodeId: node.id, tlsServerNameOverride: sni });
      for (const g of groups.data ?? []) {
        if (chosen.includes(g.id)) await groupsApi.updateGroup({ groupId: g.id, profileIds: { values: [...g.profileIds, made.id] } });
      }
      return final;
    },
    onSuccess: (name) => {
      onDone();
      toast(t("node.quick.done", { profile: name, node: node.name }));
    },
    onSettled: () => {
      for (const k of ["node", "nodes", "profiles", "groups", "users", "inbound-check"]) void qc.invalidateQueries({ queryKey: [k] });
    },
  });

  const toggle = (id: string) => setPicked(chosen.includes(id) ? chosen.filter((x) => x !== id) : [...chosen, id]);
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-col gap-1">
        <b className="text-[15px] tracking-[-0.01em]">{selfSigned ? t("node.check.self.create", { node: node.name }) : t("node.quick.title")}</b>
        {!selfSigned && <p className="text-[13px] leading-snug text-muted">{t("node.quick.body", { node: node.name })}</p>}
      </div>

      {!ip && <p className="text-xs leading-snug text-muted">{t("node.quick.acmeOn", { domain: node.address })}</p>}
      {ip && (
        <div className="flex flex-col gap-2">
          {!selfSigned && <p className="text-xs leading-snug text-muted">{t("node.quick.ip", { node: node.name, address: node.address })}</p>}
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.quick.cert")}</span>
          <Segmented
            variant="thumb"
            aria-label={t("node.quick.cert")}
            value={cert}
            onValueChange={setCert}
            options={[
              { value: "acme", label: t("node.quick.acme"), title: t("node.quick.acmeHint") },
              { value: "self", label: t("node.quick.self"), title: t("node.quick.selfHint") },
            ]}
          />
          {needDomain ? (
            <TextField
              label={t("node.profiles.tlsOverride")}
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
              placeholder="vpn.example.com"
              hint={t("node.quick.domainNeeded", { address: node.address })}
              mono
              autoCapitalize="off"
              spellCheck={false}
            />
          ) : (
            <p className="rounded-field border border-warn-line bg-warn-soft px-3 py-2 text-xs leading-snug text-pretty">{t("node.check.self.body")}</p>
          )}
        </div>
      )}

      <div className="flex flex-col gap-2">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.quick.groups")}</span>
        {(groups.data ?? []).length === 0 ? (
          <p className="text-xs text-muted">{groups.isPending ? t("common.loading") : t("node.quick.noGroups")}</p>
        ) : (
          <>
            <div className="flex flex-wrap gap-1.5">
              {groups.data!.map((g) => {
                const on = chosen.includes(g.id);
                return (
                  <button
                    key={g.id}
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggle(g.id)}
                    className={cx(
                      "flex h-[30px] items-center gap-1.5 rounded-[15px] border px-3 text-xs font-bold transition-colors duration-200",
                      on ? "border-accent-line bg-accent-soft text-fg" : "border-line bg-surface text-muted",
                    )}
                  >
                    {on && <Icon name="check" size={12} />}
                    {g.name}
                  </button>
                );
              })}
            </div>
            {def && (
              <p className="text-[11px] leading-snug text-muted">
                {t(defHas ? "node.quick.groupMore" : "node.quick.groupFirst", { group: def.name, protocol: "Hysteria2" })}
              </p>
            )}
          </>
        )}
      </div>

      {create.isError && (
        <Notice tone="danger" className="items-start">
          {errorText(create.error, t)}
        </Notice>
      )}
      {/* the phone stacks the two, the main one on top and as wide as the sheet: its label is long */}
      <div className="flex justify-end gap-2 pt-1 max-md:flex-col-reverse">
        {onBack ? (
          <Button variant="ghost" size="md" onClick={onBack}>
            {t("auth.back")}
          </Button>
        ) : (
          <Link to="/profiles/new" search={{ node: node.id } as never} onClick={onDone} className={buttonClass("secondary", "md")}>
            {t("node.quick.manual")}
          </Link>
        )}
        <Button
          variant="primary"
          size="md"
          className="h-auto! min-h-9 py-2 text-center whitespace-normal!"
          disabled={create.isPending || groups.isPending || (needDomain && !domain.trim())}
          onClick={() => create.mutate()}
        >
          {t("node.quick.create", { port, node: node.name })}
        </Button>
      </div>
    </div>
  );
}
