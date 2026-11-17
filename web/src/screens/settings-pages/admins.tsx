import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { meQuery } from "@/lib/session";
import { roleKey } from "@/screens/shell";
import { passwordLoginQuery } from "./password";
import { passkeysQuery } from "./security";
import { ICONS, Otg, Sec, slots, SxGate } from "./sx-bits";

/**
 * Settings -> Admins (the board's sxIs_adm): who is signed in and how they sign in, as a row with a letter, the role and the way in, and
 * a link to Security. There is no second admin yet, so it says so and points scripts to the API tokens.
 */
export function AdminsPage() {
  const t = useT();
  const me = useQuery(meQuery);
  const admin = me.data?.admin;
  const keys = useQuery(passkeysQuery);
  const pw = useQuery(passwordLoginQuery).data;
  const passkeys = keys.data?.passkeys.length ?? 0;
  const methods = [passkeys ? t.n("sx.adm.pk", passkeys) : "", pw?.enabled ? t("sx.adm.pw", { login: pw.login }) : ""].filter(Boolean).join(", ");
  return (
    <SxGate pending={me.isPending || keys.isPending} error={me.isError || keys.isError} onRetry={() => void Promise.all([me.refetch(), keys.refetch()])}>
      <div className="sx-one">
        <Sec
          tone="lav"
          d={ICONS.people}
          title={t("settings.admins")}
          foot={slots(t("sx.adm.foot"), {
            link: (
              <Link to="/ai" search={{ tab: "mcp" } as never} className="an-lk">
                {t("sx.adm.tokens")}
              </Link>
            ),
          })}
        >
          <div className="sx-list">
            <div className="sx-it sx-se">
              <span className="ai-mono" aria-hidden>
                {(admin?.displayName ?? "").slice(0, 1).toUpperCase()}
              </span>
              <div>
                <b>
                  {admin?.displayName}
                  <Otg>{t(roleKey[admin?.role ?? Role.UNSPECIFIED])}</Otg>
                </b>
                {methods && <em>{t("sx.adm.methods", { methods })}</em>}
              </div>
              <Link to="/settings/$section" params={{ section: "security" }} className="an-lk ig-lk">
                {t("settings.security")}
              </Link>
            </div>
          </div>
        </Sec>
      </div>
    </SxGate>
  );
}
