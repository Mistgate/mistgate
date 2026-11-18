import { Link } from "@tanstack/react-router";
import { useT } from "@/i18n";
import { useIsOwner } from "@/lib/updates";
import { Gl } from "../ai/bits";
import { TelegramCard, type LinkCode } from "./telegram";

/**
 * Integrations, as the board draws it (PnIntegrations): the Telegram alerts, where every admin links their own chat. MCP, the API tokens and the
 * approval inbox moved to the AI section (ADR 0014); the owner gets a card that says so, with the way there.
 */
export function IntegrationsScreen() {
  return <IntegrationsView />;
}

/** What a scene of the dev kit puts in place of what the page would find out by itself (the link code the chat waits on). */
export type IntegrationsScene = { code?: LinkCode };

export function IntegrationsView({ scene }: { scene?: IntegrationsScene }) {
  const t = useT();
  const owner = useIsOwner();
  return (
    <div className="aip igp">
      <header className="sb-h">
        <h1>{t("int.title")}</h1>
        <p>{t("igp.sum")}</p>
      </header>
      <TelegramCard
        owner={owner}
        initialCode={scene?.code}
        extra={
          owner && (
            <Link to="/ai" search={{ tab: "mcp" } as never} className="card ig-ai">
              <span>
                <b>{t("igp.ai.t")}</b>
                <em>{t("igp.ai.s")}</em>
              </span>
              <Gl d="M5 12h14M13 6l6 6-6 6" />
            </Link>
          )
        }
      />
    </div>
  );
}
