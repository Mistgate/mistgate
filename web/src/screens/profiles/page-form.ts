import { Code } from "@connectrpc/connect";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import type { ProfileImpact } from "@/gen/mistgate/admin/v1/profile_pb";
import { useToast } from "@/components/ui/toast";
import { profiles } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { lossyRefusal } from "@/screens/node/port-lossy";
import { fieldErrors, isCode } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import type { PageData } from "./page-types";
import { mtprotoGroups } from "./mtproto-form";
import { allFields, changedFields, parseSchema, parseSettings, type Field, type Settings } from "./schema";
import type { Problem } from "./schema-form";

/**
 * The form of a saved profile, kept above the tabs so that what was typed survives a look at the Servers tab: the name and the settings
 * (from the plugin's schema), the problems the server finds while it is typed (a debounced preview) and when it is saved, what the save
 * would change for people (the window "Save changes?"), and the answers a save can get: a version saved elsewhere, a port the delivery
 * check refuses. `count` is the number of changed fields (the name is one); the page shows it on the tab and in the bar.
 */
/** What the dev kit starts the form with: a changed setting and name, and the window "Save changes?" open with an answer made up for it. */
export type FormSeed = { settings?: (base: Settings) => Settings; name?: string; impact?: ProfileImpact };

export function useProfileForm(data: PageData, readOnly: boolean, onStale: () => void, onSaved?: (on: boolean) => void, seed?: FormSeed) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const { profile, info, initial } = data;
  const groups = useMemo(() => (info.id === "mtproto" ? mtprotoGroups(parseSchema(info.settingsSchemaJson)) : parseSchema(info.settingsSchemaJson)), [info.id, info.settingsSchemaJson]);
  const fields = useMemo(() => allFields(groups), [groups]);
  const base = useMemo(() => parseSettings(initial.settingsJson), [initial.settingsJson]);
  const [settings, setSettings] = useState<Settings>(() => seed?.settings?.(base) ?? base);
  const [name, setName] = useState(seed?.name ?? initial.name);
  const [nameError, setNameError] = useState(false);
  const [saveProblems, setSaveProblems] = useState<Problem[]>([]);
  // the delivery check refused the new port on a node (port_lossy), and the admin's "save anyway" (allow_lossy_port)
  const [lossy, setLossy] = useState<Record<string, string> | null>(null);
  const [anyway, setAnyway] = useState(false);
  // kept after the window closes, so its text does not go blank while it fades out
  const [impact, setImpact] = useState<{ impact: ProfileImpact; fields: Field[] } | null>(() => (seed?.impact ? { impact: seed.impact, fields: allFields(parseSchema(info.settingsSchemaJson)).filter((f) => f.critical).slice(0, 1) } : null));
  const [impactOpen, setImpactOpen] = useState(!!seed?.impact);
  const [busy, setBusy] = useState(false);

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
  const nameChanged = name !== initial.name;
  const count = changed.length + (nameChanged ? 1 : 0);
  const nameProblem = !name.trim() ? "empty" : "";

  const edit = (next: Settings) => {
    setSettings(next);
    setSaveProblems([]);
    setLossy(null);
    setAnyway(false);
  };
  const rename = (v: string) => {
    setName(v);
    setNameError(false);
  };
  const discard = () => {
    setSettings(base);
    setName(initial.name);
    setNameError(false);
    setSaveProblems([]);
    setLossy(null);
    setAnyway(false);
  };

  async function save(confirmed = false, allowLossy = anyway) {
    if (readOnly) return;
    if (nameProblem) {
      setNameError(true);
      return;
    }
    setBusy(true);
    setLossy(null);
    try {
      const request = {
        profileId: profile.id,
        expectedVersion: profile.version,
        ...(nameChanged && { name: name.trim() }),
        ...(changed.length > 0 && { settingsJson }),
        ...(allowLossy && { allowLossyPort: true }),
      };
      const critical = changed.filter((f) => f.critical);
      if (critical.length > 0 && !confirmed) {
        const res = await profiles.updateProfile({ ...request, dryRun: true });
        if (res.impact) {
          setImpact({ impact: res.impact, fields: critical });
          setImpactOpen(true);
          return;
        }
      }
      await profiles.updateProfile(request);
      setImpactOpen(false);
      onSaved?.(true); // saved: the form takes the new version when it arrives, and the bar says so
      await qc.invalidateQueries({ queryKey: ["profiles"] });
      toast(profile.nodeCount > 0 ? t.n("profiles.savedRollout", profile.nodeCount) : t("profiles.saved"));
    } catch (e) {
      setImpactOpen(false);
      const fe = fieldErrors(e);
      const refusedPort = lossyRefusal(e);
      if (refusedPort) setLossy(refusedPort);
      else if (isCode(e, Code.Aborted)) onStale();
      else if (fe.length > 0) setSaveProblems(fe.map((x) => ({ pointer: x.pointer, code: x.code, message: x.message })));
      else toast.error(errorText(e, t));
    } finally {
      setBusy(false);
    }
  }

  return {
    groups,
    fields,
    base,
    settings,
    name,
    nameError,
    nameProblem,
    problems,
    preview,
    changed,
    count,
    edit,
    rename,
    discard,
    save,
    busy,
    lossy,
    setAnyway,
    impact,
    impactOpen,
    closeImpact: () => setImpactOpen(false),
  };
}

export type ProfileForm = ReturnType<typeof useProfileForm>;
