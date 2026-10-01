import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { FilterChips } from "@/components/ui/chips";
import { SectionLabel } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { adminUrl, clients, mcpUrl, snippet, type Client } from "@/lib/integrations";
import { card, CodeBlock } from "./parts";

const labels: Record<Client, MessageKey> = {
  code: "int.mcp.client.code",
  desktop: "int.mcp.client.desktop",
  other: "int.mcp.client.other",
  stdio: "int.mcp.client.stdio",
};
const hints: Record<Client, MessageKey> = {
  code: "int.mcp.hint.code",
  desktop: "int.mcp.hint.desktop",
  other: "int.mcp.hint.other",
  stdio: "int.mcp.hint.stdio",
};
// what the snippet is written in, shown in its header
const langs: Record<Client, string> = { code: "bash", desktop: "json", other: "json", stdio: "bash" };

/** MCP: the endpoint, a ready snippet for each kind of client (the token is a placeholder), and what the agent can and cannot do. */
export function McpCard() {
  const t = useT();
  const [client, setClient] = useState<Client>("code");
  const mcp = mcpUrl();
  const text = snippet(client, mcp, adminUrl());

  return (
    <section className={card}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5">
        <SectionLabel as="h2" icon="plug" tone="sky">
          {t("int.mcp.title")}
        </SectionLabel>
        <span className="text-xs text-muted">{t("int.mcp.sub")}</span>
      </div>
      <p className="text-xs leading-normal text-pretty text-muted">{t("int.mcp.lead")}</p>
      <div className="flex flex-col gap-1.5">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("int.mcp.url")}</span>
        <div className="flex items-center gap-2 rounded-field border border-line bg-canvas py-1.5 pr-1.5 pl-3">
          <span data-testid="mcp-url" className="min-w-0 flex-1 font-mono text-xs leading-snug break-all select-all">
            {mcp}
          </span>
          <CopyButton value={mcp} />
        </div>
      </div>
      <FilterChips aria-label={t("int.mcp.client")} value={client} onValueChange={setClient} options={clients.map((c) => ({ value: c, label: t(labels[c]) }))} />
      <p className="text-xs leading-normal text-pretty text-muted">{t(hints[client])}</p>
      <CodeBlock text={text} lang={langs[client]} />
      <div className="flex gap-2.5 rounded-field bg-surface-2 px-3 py-2.5">
        <Icon name="shield" size={16} className="mt-px block flex-none text-muted" />
        <div className="flex flex-col gap-1 text-xs leading-normal text-pretty text-muted">
          <p>{t("int.mcp.safe")}</p>
          <p>
            {t("int.mcp.audit")}{" "}
            <Link to="/settings/$section" params={{ section: "audit" }} className="font-bold text-fg underline-offset-2 hover:underline">
              {t("int.mcp.auditLink")}
            </Link>
          </p>
        </div>
      </div>
    </section>
  );
}
