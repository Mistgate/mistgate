import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { assistant, telegram, warp } from "@/lib/api";
import { assistantErrorKey, providerName } from "@/lib/assistant";
import { errorText } from "@/lib/errors";
import { markAction, readMarks, type SecretAction } from "@/lib/ui-actions";
import { SecureField } from "./secure-field";

// A secret the assistant asked for (ui_secret_form): a secure field in the chat whose value goes straight to the panel's own
// call (the provider key, the Telegram bot, the WARP import), with a step-up where the call asks for one. The assistant chose
// only the kind and its context; it never sees the value, and the card forgets it the moment it is sent. All that is kept is
// this browser's note that it was entered, with the hint the panel itself shows ("…Q7wA").

const providerIds: Record<string, AssistantProvider> = { anthropic: AssistantProvider.ANTHROPIC, deepseek: AssistantProvider.DEEPSEEK, zai: AssistantProvider.ZAI, ollama_cloud: AssistantProvider.OLLAMA_CLOUD };
const providerKeys: Record<string, MessageKey> = { anthropic: "ai.prov.anthropic", deepseek: "ai.prov.deepseek", zai: "ai.prov.zai", ollama_cloud: "ai.prov.ollama" };

const HINTS_KEY = "assistant-ui-hints";
const readHints = (): Record<string, string> => {
  try {
    return JSON.parse(localStorage.getItem(HINTS_KEY) ?? "{}") as Record<string, string>;
  } catch {
    return {};
  }
};

export function SecretCard({ toolId, action, owner }: { toolId: string; action: SecretAction; owner: boolean }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [value, setValue] = useState("");
  const [done, setDone] = useState<{ hint: string } | undefined>(() => (readMarks()[toolId] === "used" ? { hint: readHints()[toolId] ?? "" } : undefined));
  const provider = action.provider ? t(providerKeys[action.provider] ?? providerName(AssistantProvider.ANTHROPIC)) : "";

  const submit = useMutation({
    // the secret is only ever in this closure and the request; the call is the panel's own
    mutationFn: async (secret: string) => {
      switch (action.secret) {
        case "provider_key": {
          const r = await guard(() => assistant.setProviderKey({ provider: providerIds[action.provider]!, apiKey: secret.trim() }));
          return r.status?.keyHint ?? "";
        }
        case "telegram_token":
          await guard(() => telegram.setTelegramBot({ token: secret.trim() }));
          return "";
        case "warp_license":
          await guard(() => warp.importWarp({ nodeId: action.node, profileConf: secret }));
          return "";
      }
    },
    gcTime: 0,
    onSuccess: (hint) => {
      const hints = readHints();
      hints[toolId] = hint ? `●●●● ${hint}` : "●●●●";
      localStorage.setItem(HINTS_KEY, JSON.stringify(hints));
      markAction(toolId, "used");
      setDone({ hint: hints[toolId] });
      void qc.invalidateQueries({ queryKey: ["assistant"] });
      void qc.invalidateQueries({ queryKey: ["telegram"] });
      void qc.invalidateQueries({ queryKey: ["warp"] });
    },
    onError: (e) => {
      if (isStepUpCancelled(e)) return;
      const k = assistantErrorKey(e);
      toast.error(k ? t(k) : errorText(e, t));
    },
    // whatever happened, the typed value is gone from this card
    onSettled: () => setValue(""),
  });

  const label = action.secret === "provider_key" ? `${t("ai.secret.provider_key")} · ${provider}` : t(`ai.secret.${action.secret}` as MessageKey);

  return (
    <div data-slot="secret" data-state={done ? "done" : "ready"} className="chat-in flex w-full flex-col gap-3 rounded-2xl bg-fg/[0.04] p-4 ring-1 ring-line/70">
      <div className="flex items-start gap-3">
        <span aria-hidden className="grid size-8 flex-none place-items-center rounded-[10px] bg-accent-soft text-accent-text">
          <Icon name="lock" size={15} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <b className="text-[14px] leading-snug font-semibold">{label}</b>
        </div>
      </div>
      {!owner ? (
        <p className="text-xs leading-snug text-muted">{t("ai.secret.ownerOnly")}</p>
      ) : done ? (
        <p className="flex items-center gap-2 text-[13px] text-ok-text">
          <Icon name="check" size={14} className="flex-none" />
          {t("ai.secret.entered", { hint: done.hint })}
        </p>
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (value.trim() && !submit.isPending) submit.mutate(value);
          }}
        >
          <SecureField label={label} value={value} onChange={setValue} multiline={action.secret === "warp_license"} maxLength={action.secret === "warp_license" ? 20_000 : 512} disabled={submit.isPending} />
          <div className="flex items-center gap-2">
            <Button type="submit" size="sm" variant="primary" disabled={!value.trim() || submit.isPending}>
              {t("ai.secret.save")}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
