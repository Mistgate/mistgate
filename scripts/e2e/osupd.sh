#!/usr/bin/env bash
# The OS update job (mistgate-node host os-update), end to end with the real apt, dpkg and unattended-upgrade, and no reboot.
#
#   sudo scripts/e2e/osupd.sh /path/to/linux/mistgate-node
#   MG_OSUPD_ROOT=/path/to/rootfs sudo scripts/e2e/osupd.sh /path/to/linux/mistgate-node
#
# Build the binary first: CGO_ENABLED=0 GOOS=linux go build -o mistgate-node ./cmd/mistgate-node
#
# WHERE IT RUNS. In a sandbox that cannot reach the machine it runs on. Everything below happens in mount, PID, network and
# UTS namespaces of its own (unshare), and ends with them:
#   - the root of the sandbox is an overlay. Its lower layer is a read-only bind of the root of this machine (or of the
#     rootfs in MG_OSUPD_ROOT, e.g. one made by debootstrap for jammy, noble, bookworm or trixie), the upper layer is a tmpfs.
#     Every write of apt and dpkg lands in the tmpfs; the lower layer cannot be written.
#   - /run, /tmp and /dev are new (only null, zero, random, urandom are bound), /proc is the proc of the new PID namespace,
#     there is no network and no D-Bus or systemd socket: nothing in the sandbox can talk to the init of this machine.
#   - systemctl in the sandbox is a stand-in that logs its arguments. "reboot" sends SIGTERM to its parent, the job, the way
#     the shutdown stops the job's unit. Nothing is ever rebooted.
#   - the process that runs the scenario is PID 1 of its namespace and is called "systemd" (the job checks /proc/1/comm).
#   - the sources are two local file: repositories of fake packages (a library, a tool, a held package, a kernel with boot
#     files, a package whose configuration fails once). No mirror is contacted.
# Before and after, this script compares the package database, /boot, os-release and the mounts of THIS machine, and fails when
# any of them changed. It runs apt-get and dpkg only through the chroot. It does nothing when the sandbox cannot be set up.
#
# WHAT IT PROVES: refresh; security mode installs the security library and leaves the updates-only tool and the held package;
# all mode installs the tool and still leaves the held one; an interrupted package is repaired before an install; a kernel that
# arrives makes security + if_required end at the "rebooting" status with the reboot command as the last thing the job did;
# the owner's case (an interrupted package, a loaded AmneziaWG module not built for the new kernel, a reboot required) is
# refused by reboot-only with every reason, without any flag, and nothing is rebooted; a DKMS failure of ANOTHER module leaves
# AmneziaWG alone; a failure of AmneziaWG's own build stops the job (failed/awg_module_build_failed, also with reboot never) in the
# repair, with its package still there and nothing installed after; a loaded module without a flag holds the reboot; a kernel that
# linux-version cannot tell (kernel_unknown) holds it; a GRUB default that is not entry 0, a next_entry or no grub.cfg hold it
# (boot_default_custom); the second reboot of a panel job within the hour is refused (reboot_limit) and a manual one is not; an
# unsupported system and a second job are refused; bad requests are refused by the command line.
# WHAT IT DOES NOT: the real reboot and the return (Go tests with a fake host cover them), AmneziaWG itself (the module is
# "loaded" by a directory under /sys/module of the sandbox), the linux-version of the distribution (a stand-in is used), DEBIAN
# (the fixture repositories carry Origin Ubuntu, which does not match the origins unattended-upgrade allows on Debian: Debian 12
# and 13 are not covered here), and Ubuntu 22.04 and 26.04 unless MG_OSUPD_ROOT points at such a rootfs.
set -euo pipefail

BIN=${1:-${MG_NODE_BIN:-}}
[ -n "$BIN" ] && [ -x "$BIN" ] || { echo "usage: $0 /path/to/linux/mistgate-node" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "$0: must run as root (it makes namespaces and mounts, all of them private)" >&2; exit 2; }
for tool in unshare mount chroot md5sum; do command -v "$tool" >/dev/null || { echo "$0: needs $tool" >&2; exit 2; }; done
grep -qw overlay /proc/filesystems || { echo "$0: the kernel has no overlayfs" >&2; exit 2; }
HERE=$(cd "$(dirname "$0")" && pwd)
BIN=$(readlink -f "$BIN")
ROOT_SRC=${MG_OSUPD_ROOT:-/}

# What is compared before and after: the machine this runs on must come out as it went in.
fingerprint() {
  {
    md5sum /var/lib/dpkg/status /etc/os-release
    ls -la /boot 2>/dev/null | md5sum
    awk '{print $2, $3}' /proc/self/mounts | md5sum
    dpkg-query -W -f='${Package} ${Version} ${db:Status-Abbrev}\n' 2>/dev/null | md5sum
    ls /var/log/apt 2>/dev/null | md5sum
  } | md5sum | cut -d' ' -f1
}
BEFORE=$(fingerprint)
HOST_BOOT_ID=$(cat /proc/sys/kernel/random/boot_id)

T=$(mktemp -d /tmp/mg-osupd.XXXXXX)
cleanup() { rm -rf -- "$T"; }
trap cleanup EXIT

# The outer script of the sandbox runs as PID 1 of the new PID namespace, in a private mount namespace.
cp -- "$BIN" "$T/mistgate-node"
cp -- "$HERE/osupd-in.sh" "$T/systemd"
cat > "$T/outer.sh" <<'OUTER'
#!/bin/bash
set -euo pipefail
T=$1 ROOT_SRC=$2
mount --make-rprivate /
mkdir -p "$T/lower" "$T/up" "$T/work" "$T/merged"
mount -t tmpfs -o size=3g tmpfs "$T/up"
mkdir -p "$T/up/u" "$T/up/w"
mount --bind "$ROOT_SRC" "$T/lower"          # not recursive: the mounts below the root are not part of it
mount -o remount,bind,ro "$T/lower"
mount -t overlay overlay -o "lowerdir=$T/lower,upperdir=$T/up/u,workdir=$T/up/w" "$T/merged"
R=$T/merged
# The canary: this file exists in the upper layer only. Nothing runs unless the chroot shows it.
echo sandbox > "$R/.mg-sandbox"
test ! -e "$ROOT_SRC/.mg-sandbox"
mount -t tmpfs tmpfs "$R/run"
mount -t tmpfs tmpfs "$R/tmp"
mount -t tmpfs tmpfs "$R/dev"
for d in null zero random urandom; do : > "$R/dev/$d"; mount --bind "/dev/$d" "$R/dev/$d"; done
mount -t proc proc "$R/proc"
# The job refuses to reboot a host that booted less than ten minutes ago; this machine may have: the sandbox sees a long uptime.
echo "86400.00 80000.00" > "$T/uptime"
mount --bind "$T/uptime" "$R/proc/uptime"
mkdir -p "$R/usr/lib/mgtest" "$R/usr/local/bin"
cp "$T/systemd" "$R/usr/lib/mgtest/systemd"
cp "$T/mistgate-node" "$R/usr/local/bin/mistgate-node"
chmod 0755 "$R/usr/lib/mgtest/systemd" "$R/usr/local/bin/mistgate-node"
chroot "$R" /bin/sh -c 'test -e /.mg-sandbox' || { echo "the sandbox root is not the sandbox: stopping" >&2; exit 3; }
hostname mg-osupd
exec chroot "$R" /usr/lib/mgtest/systemd
OUTER
chmod 0755 "$T/outer.sh"

set +e
unshare --mount --pid --fork --net --uts --propagation private -- "$T/outer.sh" "$T" "$ROOT_SRC"
RC=$?
set -e

AFTER=$(fingerprint)
if [ "$BEFORE" != "$AFTER" ] || [ "$HOST_BOOT_ID" != "$(cat /proc/sys/kernel/random/boot_id)" ]; then
  echo "FAIL: the machine the test ran on changed (package database, /boot, os-release or mounts)" >&2
  exit 1
fi
echo "ok    this machine is as it was: package database, /boot, os-release, mounts, boot id"
exit "$RC"
