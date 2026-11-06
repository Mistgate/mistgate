import { Link } from "@tanstack/react-router";
import { buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { updatesQuery } from "@/lib/updates";
import { page, Shot, upNode } from "./dev-kit-redesign";
import { UpdatesScreen } from "./updates";
import { ChannelDialog } from "./updates/channel";
import { UpdatePanelDialog, type BackupView } from "./updates/update-dialog";

// Development only, /dev-kit?only=release&state=…: release channels (ADR 0011) and the window before a panel update
// (ADR 0012), one state per address so an open window is alone on its page (for screenshots).

const NOW = Math.floor(Date.now() / 1000);
const H = 3600;

const note = (version: string, ago: number, channel: string, en: string, ru: string) => ({
  version,
  url: `https://github.com/Mistgate/mistgate/releases/tag/${version}`,
  publishedUnix: NOW - ago,
  en,
  ru,
  zh: "",
  security: false,
  channel,
});

const offer = (version: string, ago = H) => ({
  version,
  url: `https://github.com/Mistgate/mistgate/releases/tag/${version}`,
  publishedUnix: NOW - ago,
  checkedUnix: NOW - 120,
  available: true,
  supported: true,
  installable: true,
  installing: false,
  errorKey: "",
  built: NOW - ago,
  sha256: "77ab10123456789abcdef0123456789abcdef0123456789abcdef0123456789a",
});

const nodes = [upNode({ nodeId: "nod_de1", name: "de1" }), upNode({ nodeId: "nod_nl1", name: "nl1", address: "nl1.example.com" })];

const stable = page({
  updateChannel: "stable",
  runningVersionChannel: "stable",
  offeredReleaseChannel: "stable",
  runningNewerThanChannelOffers: false,
  panel: { version: "v0.1.79", built: NOW - 30 * H, hasReleaseKey: true, releaseKeyFingerprint: "7f7c1ee4840b94d7", update: offer("v0.1.80") },
  nodes,
  panelNotes: [
    note("v0.1.80", H, "stable", "- **A node only through an entry or a CDN.** Keep a node's own address out of subscriptions.\n- **IPv6 measured only where it helps.**", "- **Нода только через вход или CDN.** Собственный адрес ноды можно убрать из подписок.\n- **IPv6 меряется только там, где поможет.**"),
  ],
});

const testing = page({
  updateChannel: "testing",
  runningVersionChannel: "stable",
  offeredReleaseChannel: "testing",
  runningNewerThanChannelOffers: false,
  panel: { version: "v0.1.80", built: NOW - 26 * H, hasReleaseKey: true, releaseKeyFingerprint: "7f7c1ee4840b94d7", update: offer("v0.1.81-testing.2", 3 * H) },
  nodes,
  panelNotes: [
    note("v0.1.81-testing.2", 3 * H, "testing", "- **Release channels.** Pick Stable, Testing or Development on the Updates page.", "- **Каналы релизов.** Стабильный, Тестовый или Разработка — на странице «Обновления»."),
    note("v0.1.81-testing.1", 28 * H, "testing", "- **A backup before the update.** The window offers R2 and a file on this computer.", "- **Бэкап перед обновлением.** Окно предлагает R2 и файл на компьютер."),
  ],
});

const ahead = page({
  updateChannel: "stable",
  runningVersionChannel: "dev",
  offeredReleaseChannel: "stable",
  runningNewerThanChannelOffers: true,
  panel: {
    version: "v0.1.81-dev.3",
    built: NOW - 5 * H,
    hasReleaseKey: true,
    releaseKeyFingerprint: "7f7c1ee4840b94d7",
    update: { ...offer("v0.1.80", 26 * H), available: false },
  },
  nodes,
  panelNotes: [],
});

const r2 = (over: Partial<BackupView>): BackupView => ({ r2: true, recipient: true, lastSuccessUnix: NOW - 3 * H, lastErrorCode: "", running: false, downloadedUnix: 0, ...over });
const ok = () => Promise.resolve(true);

function SetUp() {
  const t = useT();
  return (
    <Link to="/settings/$section" params={{ section: "backups" }} className={buttonClass("ghost", "sm")}>
      {t("up.dlg.r2.setUp")}
      <Icon name="chevronRight" size={12} />
    </Link>
  );
}

function Update({ backup }: { backup: BackupView }) {
  return (
    <UpdatePanelDialog
      open
      onOpenChange={() => {}}
      from={{ version: "v0.1.79", channel: "stable" }}
      to={{ version: "v0.1.80", channel: "stable" }}
      backup={backup}
      fileName="mistgate-backup-panel-example-com-v0.1.79-2026-11-06-1542.tar.gz.age"
      now={NOW}
      onBackupR2={ok}
      setUp={<SetUp />}
      onDownload={ok}
      onUpdate={ok}
    />
  );
}

export function ReleaseKit() {
  const state = new URLSearchParams(location.search).get("state") ?? "bars";
  switch (state) {
    case "channel":
      return <ChannelDialog open onOpenChange={() => {}} current="stable" running={{ version: "v0.1.80", channel: "stable" }} onPick={ok} />;
    case "channel-dev":
      return <ChannelDialog open onOpenChange={() => {}} current="dev" running={{ version: "v0.1.81-dev.3", channel: "dev" }} onPick={ok} />;
    case "update-r2":
      return <Update backup={r2({})} />;
    case "update-r2-running":
      return <Update backup={r2({ running: true })} />;
    case "update-done":
      return <Update backup={r2({ lastSuccessUnix: NOW - 60, downloadedUnix: NOW - 90 })} />;
    case "update-file":
      return <Update backup={r2({ r2: false, recipient: false, lastSuccessUnix: 0 })} />;
  }
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1100px)] -translate-x-1/2 flex-col gap-10">
      <Shot id="release-stable" title="Stable: a stable release is out" seed={[[updatesQuery.queryKey, stable]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="release-testing" title="Testing: the panel follows Testing, a testing release is out, What's new marks it" seed={[[updatesQuery.queryKey, testing]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="release-ahead" title="Back on Stable from a dev build: the panel stays until Stable catches up" seed={[[updatesQuery.queryKey, ahead]]}>
        <UpdatesScreen />
      </Shot>
    </div>
  );
}
