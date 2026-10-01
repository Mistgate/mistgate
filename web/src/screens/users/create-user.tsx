import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { QuotaReset } from "@/gen/mistgate/admin/v1/user_pb";
import { Icon } from "@/components/ui/icons";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { Infinite, Stepper } from "@/components/ui/stepper";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { GB } from "./format";
import { big, userN, type UserN as User } from "./model";
import { DnsSelect, useInheritedDns } from "./dns-select";
import { defaultGroup, GroupEditModal, GroupForm, PillToggle, ReachLine } from "./groups";
import { groupsQuery, profileListQuery } from "./rpc";
import { useTx } from "./t";
import { SettingRow, SwitchRow } from "./ui";

export { GroupEditModal, PillToggle } from "./groups";

const Box = ({ children }: { children: ReactNode }) => <div className="rounded-card bg-surface-2 px-3.5">{children}</div>;

/** The create-user modal (a bottom sheet on the phone). The form is mounted only while it is open, so it starts clean every time. */
export function CreateUserModal({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (user: User, url: string, password: string) => void }) {
  const t = useTx();
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={t("users.new")} closeLabel={t("common.close")}>
      <CreateForm onCreated={onCreated} />
    </Modal>
  );
}

/**
 * New user. The group comes first ("Everyone" when there is one) and right under it what the person will get: the
 * subscription and AmneziaVPN keys, on how many nodes, in words; the app switches follow it until they are touched, so a
 * person never gets a way that gives nothing. The group form shows inline only when there is no group at all.
 */
function CreateForm({ onCreated }: { onCreated: (user: User, url: string, password: string) => void }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const groupList = useQuery(groupsQuery);
  const nodeList = useQuery(nodesQuery);
  const profileList = useQuery(profileListQuery);
  const linkApps = useLinkAppNames();

  const [name, setName] = useState("");
  const [pickedGroup, setGroupId] = useState("");
  const [quotaGb, setQuotaGb] = useState(100);
  const [termDays, setTermDays] = useState(30);
  const [devices, setDevices] = useState(5);
  const [apps, setApps] = useState<{ happ: boolean; amnezia: boolean } | null>(null); // null = what the group gives
  const [allNodes, setAllNodes] = useState(true);
  const [picked, setPicked] = useState<string[]>([]);
  const [editingGroup, setEditingGroup] = useState(false);
  const [dnsPreset, setDnsPreset] = useState(""); // "" = inherit
  const [error, setError] = useState<string | null>(null);

  const groups = groupList.data ?? [];
  const groupOptions = groups.map((g) => ({ value: g.id, label: g.name }));
  const nodeItems = nodeList.data?.nodes ?? [];
  const allIds = nodeItems.map((n) => n.id);
  const groupId = pickedGroup || defaultGroup(groups)?.id || "";
  const group = groups.find((g) => g.id === groupId);
  const noProfiles = profileList.data?.length === 0;
  const inheritedDns = useInheritedDns(groupId);
  // The switches follow the group until one is flipped: a way the group gives nothing for starts off (with the reason
  // under it); a group that gives nothing at all leaves Happ on, and the red line above says why it is empty.
  const auto = { happ: !group || group.happNodes > 0 || group.amneziaNodes === 0, amnezia: !!group && group.amneziaNodes > 0 };
  const { happ, amnezia } = apps ?? auto;

  const create = useMutation({
    mutationFn: () =>
      users.createUser({
        name: name.trim(),
        groupId,
        quotaBytes: big(quotaGb * GB),
        quotaReset: QuotaReset.MONTH,
        termDays,
        deviceLimit: devices,
        apps: { happ, amnezia },
        nodes: { all: allNodes, nodeIds: allNodes ? [] : picked.filter((id) => allIds.includes(id)) },
        dnsPresetId: dnsPreset,
      }),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["users"] });
      void qc.invalidateQueries({ queryKey: ["groups"] });
      if (res.user) onCreated(userN(res.user), res.subscriptionUrl, res.pagePassword);
    },
    onError: (e) => setError(errorText(e, t)),
  });

  const toggleNode = (id: string) => {
    const cur = allNodes ? allIds : picked;
    setAllNodes(false);
    setPicked(cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]);
  };
  const flipAll = () => {
    if (allNodes) {
      setAllNodes(false);
      setPicked(allIds);
    } else setAllNodes(true);
  };
  // at least one app stays on: the switch that would turn the last one off does nothing
  const flipApp = (which: "happ" | "amnezia", on: boolean) => {
    const next = { happ, amnezia, [which]: on };
    if (!next.happ && !next.amnezia) return;
    setApps(next);
  };

  const step = (what: string, value: number, set: (v: number) => void, by: number, min: number, max: number) => ({
    decrementLabel: t("users.less", { what }),
    incrementLabel: t("users.more", { what }),
    onDecrement: () => set(Math.max(min, value - by)),
    onIncrement: () => set(Math.min(max, value + by)),
    decrementDisabled: value <= min,
    incrementDisabled: value >= max,
  });
  const sel = allNodes ? allIds.length : picked.filter((id) => allIds.includes(id)).length;
  const ready = name.trim() !== "" && groupId !== "" && !create.isPending;
  const offHint = (which: "happ" | "amnezia") => {
    if (apps || !group) return undefined;
    if (which === "happ" && !happ) return t("users.happOff");
    if (which === "amnezia" && !amnezia) return t("users.awgOff");
    return undefined;
  };

  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (ready) {
          setError(null);
          create.mutate();
        }
      }}
    >
      <TextField icon="person" tone="lavender" label={t("users.name")} value={name} onChange={(e) => setName(e.target.value)} placeholder={t("users.namePh")} autoFocus autoComplete="off" maxLength={64} />

      <Box>
        {groupList.data && groups.length === 0 ? (
          <GroupForm
            first
            dns={false}
            quiet
            profiles={(profileList.data ?? []).map((p) => ({ id: p.id, name: p.name }))}
            onCancel={() => {}}
            onCreated={(g) => {
              setGroupId(g.id);
              toast(t("users.groupCreated", { name: g.name }));
            }}
          />
        ) : (
          <>
            <SettingRow label={t("users.group")}>
              {groupOptions.length > 0 && (
                <div className="w-[200px] max-w-[55vw] [&>button>span:first-child]:min-w-0 [&>button>span:first-child]:truncate">
                  <Select
                    aria-label={t("users.group")}
                    value={groupId}
                    onValueChange={(v) => {
                      setGroupId(v);
                      setApps(null); // a new group: the switches follow what it gives again
                    }}
                    options={groupOptions}
                  />
                </div>
              )}
            </SettingRow>
            {group && (
              <div className="border-t border-line">
                <ReachLine
                  group={group}
                  action={
                    <button type="button" onClick={() => setEditingGroup(true)} className="text-xs font-bold text-accent-text transition-colors hover:text-fg">
                      {t("users.groupEdit")}
                    </button>
                  }
                />
              </div>
            )}
          </>
        )}
        {noProfiles && (
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-line py-2.5 text-xs leading-snug text-warn-text">
            {t("users.reachNoProfiles")}
            <Link to="/profiles" className="inline-flex items-center gap-1 font-bold text-accent-text hover:underline">
              {t("nav.profiles")}
              <Icon name="chevronRight" size={12} />
            </Link>
          </p>
        )}
      </Box>

      <Box>
        <SettingRow label={t("users.quota")} hint={t("users.quotaHint")}>
          <div className="w-[130px]">
            <Stepper {...step(t("users.quota"), quotaGb, setQuotaGb, 10, 0, 10000)}>
              {quotaGb === 0 ? <Infinite label={t("users.unlimited")} /> : `${quotaGb} ${t("users.unitGb")}`}
            </Stepper>
          </div>
        </SettingRow>
        <SettingRow label={t("users.term")} hint={t("users.termHint")}>
          <div className="w-[130px]">
            <Stepper {...step(t("users.term"), termDays, setTermDays, 5, 0, 3650)}>
              {termDays === 0 ? <Infinite label={t("users.never")} /> : `${termDays} ${t("users.unitD")}`}
            </Stepper>
          </div>
        </SettingRow>
        <SettingRow label={t("users.devices")} hint={t("users.devicesHint")}>
          <div className="w-[130px]">
            <Stepper {...step(t("users.devices"), devices, setDevices, 1, 1, 100)}>{devices}</Stepper>
          </div>
        </SettingRow>
      </Box>

      <Box>
        <SettingRow label={t("users.apps")} hint={t("users.appsHint")} className="min-h-14 py-2" />
        <SwitchRow label={linkApps} hint={offHint("happ") ?? t("users.happHint")} checked={happ} onCheckedChange={(on) => flipApp("happ", on)} className="min-h-12" />
        <SwitchRow label="AmneziaVPN" hint={offHint("amnezia") ?? t("users.awgHint")} checked={amnezia} onCheckedChange={(on) => flipApp("amnezia", on)} className="min-h-12" />
      </Box>

      <div className="rounded-card bg-surface-2 px-3.5 pb-3">
        <SwitchRow
          label={t("users.nodesAll")}
          hint={nodeItems.length === 0 ? t("users.nodesNone") : allNodes ? t("users.nodesAllHint") : t("users.nodesSelHint", { a: sel, b: allIds.length })}
          checked={allNodes}
          onCheckedChange={flipAll}
          className="min-h-14"
        />
        {nodeItems.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {nodeItems.map((n) => (
              <PillToggle key={n.id} mono on={allNodes || picked.includes(n.id)} onClick={() => toggleNode(n.id)}>
                {n.name}
                {n.countryCode && <span className="font-sans text-[11px] font-semibold text-muted">{n.countryCode}</span>}
              </PillToggle>
            ))}
          </div>
        )}
      </div>

      <details className="group rounded-card bg-surface-2 px-3.5">
        <summary className="flex h-12 cursor-pointer items-center gap-2 text-[13px] font-bold select-none [&::-webkit-details-marker]:hidden">
          <Icon name="chevronRight" size={14} className="text-muted transition-transform duration-200 group-open:rotate-90" />
          {t("users.advanced")}
        </summary>
        <div className="flex flex-col gap-2 border-t border-line py-3">
          <span className="text-[13px] font-bold">{t("subs.dns.field")}</span>
          <DnsSelect value={dnsPreset} inherited={inheritedDns} onChange={setDnsPreset} />
          <span className="text-[11px] leading-snug text-muted">{t("subs.dns.inheritHint")}</span>
        </div>
      </details>

      {error && <Notice tone="danger">{error}</Notice>}
      <Button type="submit" variant="primary" size="lg" full disabled={!ready}>
        {t("users.createBtn")}
      </Button>
      <GroupEditModal group={group} open={editingGroup} onOpenChange={setEditingGroup} />
    </form>
  );
}
