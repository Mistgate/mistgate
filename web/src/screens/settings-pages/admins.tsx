import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Avatar } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { meQuery } from "@/lib/session";
import { roleKey } from "@/screens/shell";
import { passwordLoginQuery } from "./password";
import { passkeysQuery } from "./security";

/** Settings -> Admins: who is signed in and how they sign in. There is no second admin yet, so it says so. */
export function AdminsPage() {
  const t = useT();
  const admin = useQuery(meQuery).data?.admin;
  const passkeys = useQuery(passkeysQuery).data?.passkeys.length ?? 0;
  const pw = useQuery(passwordLoginQuery).data;
  const methods = [passkeys ? t.n("set.admins.passkeys", passkeys) : "", pw?.enabled ? t("set.admins.password", { login: pw.login }) : ""]
    .filter(Boolean)
    .join(", ");
  const link = "font-bold text-fg underline-offset-2 hover:underline";
  return (
    <>
      <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
        <div className="flex items-center gap-3">
          <Avatar name={admin?.displayName ?? ""} size={40} />
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <b className="truncate text-[15px]">{admin?.displayName}</b>
            <span className="text-xs text-muted">{t(roleKey[admin?.role ?? Role.UNSPECIFIED])}</span>
          </div>
        </div>
        {methods && (
          <p className="border-t border-line pt-3 text-xs leading-normal text-pretty text-muted">
            {t("set.admins.methods", { methods })}
            {" · "}
            <Link to="/settings/$section" params={{ section: "security" }} className={link}>
              {t("settings.security")}
            </Link>
          </p>
        )}
      </section>
      <p className="text-xs leading-normal text-pretty text-muted">
        {t("set.admins.more")}{" "}
        <Link to="/integrations" className={link}>
          {t("set.admins.tokens")}
        </Link>
      </p>
    </>
  );
}
