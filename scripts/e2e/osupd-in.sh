#!/bin/bash
# The scenario of scripts/e2e/osupd.sh. It runs INSIDE the sandbox as PID 1 of its PID namespace (the file is called "systemd"
# so that the job's /proc/1/comm check passes), on an overlay whose writes go to a tmpfs. Never run it by hand.
set -uo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/root LC_ALL=C DEBIAN_FRONTEND=noninteractive

[ -e /.mg-sandbox ] || { echo "FAIL: not inside the sandbox" >&2; exit 3; }
[ "$(cat /proc/1/comm)" = systemd ] || { echo "FAIL: PID 1 is not called systemd" >&2; exit 3; }

fails=0
ok() { echo "ok    $*"; }
bad() {
  echo "FAIL  $*"; fails=$((fails + 1))
  echo "      last job output:"; tail -n 12 /tmp/job.out 2> /dev/null | sed 's/^/      | /'
  echo "      status file: $(cat "$ST" 2> /dev/null)"
}
check() { # description, command...
  local d=$1; shift
  if "$@"; then ok "$d"; else bad "$d"; fi
}

NODE=/usr/local/bin/mistgate-node
STATE=/var/lib/mistgate-node
ST=$STATE/os-update.json
mkdir -p "$STATE"
job() { "$NODE" host os-update --yes --status-file "$ST" "$@" > /tmp/job.out 2>&1; echo $? > /tmp/job.rc; cat /tmp/job.rc > /dev/null; return "$(cat /tmp/job.rc)"; }
# jq is not assumed: the fields of the status file are read with sed.
field() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" "$ST"; }
num() { sed -n "s/.*\"$1\":\\([0-9]*\\).*/\\1/p" "$ST"; }
ver() { dpkg-query -W -f='${Version}' "$1" 2>/dev/null; }
stat3() { dpkg-query -W -f='${db:Status-Abbrev}' "$1" 2>/dev/null | tr -d ' '; }
reboots() { local n; n=$(grep -c '^reboot' /var/log/mgtest-systemctl.log 2>/dev/null); echo "${n:-0}"; }

# ---------------------------------------------------------------- the sandbox's own stand-ins
# systemctl: logs, and "reboot" stops the job the way a shutdown stops its unit. It never reaches a real init.
cat > /usr/local/bin/systemctl <<'SC'
#!/bin/sh
echo "$*" >> /var/log/mgtest-systemctl.log
[ "$1" = reboot ] && kill -TERM "$PPID"
exit 0
SC
chmod 0755 /usr/local/bin/systemctl
# linux-version (package linux-base) may be missing on the system under the sandbox, and a scenario needs it to fail: a stand-in
# (always used here, it comes first in PATH) lists /boot/vmlinuz-*, sorts, and fails while /var/lib/mgtest-lv-fail exists.
cat > /usr/local/bin/linux-version <<'LV'
#!/bin/sh
[ -e /var/lib/mgtest-lv-fail ] && exit 1
case "$1" in
  list) for f in /boot/vmlinuz-*; do [ -e "$f" ] && echo "${f#/boot/vmlinuz-}"; done ;;
  sort) shift; [ "$1" = --reverse ] && shift; printf '%s\n' "$@" | sort -V -r ;;
esac
exit 0
LV
chmod 0755 /usr/local/bin/linux-version
# systemd-detect-virt: WSL, where this runs, is a container to systemd, and the job treats a container as one whose kernel is not its
# own. The sandbox plays a machine of its own: the stand-in answers "none" the way the real command does outside a container.
cat > /usr/local/bin/systemd-detect-virt <<'SV'
#!/bin/sh
echo none
exit 1
SV
chmod 0755 /usr/local/bin/systemd-detect-virt
# GRUB: the job refuses to reboot unless GRUB starts its first entry. The sandbox has a grub.cfg, GRUB_DEFAULT=0 and a grub-editenv
# stand-in that prints the content of /var/lib/mgtest-grubenv (nothing while the file is absent).
mkdir -p /boot/grub
echo "# grub.cfg of the sandbox" > /boot/grub/grub.cfg
: > /boot/grub/grubenv
printf 'GRUB_DEFAULT=0\nGRUB_TIMEOUT=5\n' > /etc/default/grub
rm -f /etc/default/grub.d/*.cfg 2> /dev/null
cat > /usr/local/bin/grub-editenv <<'GE'
#!/bin/sh
[ "$1" = list ] && [ -e /var/lib/mgtest-grubenv ] && cat /var/lib/mgtest-grubenv
exit 0
GE
chmod 0755 /usr/local/bin/grub-editenv

# ---------------------------------------------------------------- fake packages and two local repositories
mkdeb() { # name version [postinst-body]  -> /srv/mg/debs/name_version.deb; files come from /srv/mg/build/name
  local n=$1 v=$2 d=/srv/mg/build/$1_$2
  rm -rf "$d"; mkdir -p "$d/DEBIAN" /srv/mg/debs
  printf 'Package: %s\nVersion: %s\nArchitecture: all\nMaintainer: Test <test@example.invalid>\nDescription: fake %s\n' "$n" "$v" "$n" > "$d/DEBIAN/control"
  [ -n "${4:-}" ] && printf 'Depends: %s\n' "$4" >> "$d/DEBIAN/control"
  if [ -n "${3:-}" ]; then printf '#!/bin/sh\nset -e\n%s\nexit 0\n' "$3" > "$d/DEBIAN/postinst"; chmod 0755 "$d/DEBIAN/postinst"; fi
  [ -d "/srv/mg/files/$n" ] && cp -a "/srv/mg/files/$n/." "$d/"
  dpkg-deb --build "$d" "/srv/mg/debs/${n}_${v}.deb" > /dev/null
}
mkrepo() { # dir suite deb...
  local dir=$1 suite=$2; shift 2
  rm -rf "$dir"; mkdir -p "$dir"; cp "$@" "$dir/"
  (cd "$dir" && apt-ftparchive packages . > Packages && apt-ftparchive \
    -o "APT::FTPArchive::Release::Origin=Ubuntu" -o "APT::FTPArchive::Release::Label=Ubuntu" \
    -o "APT::FTPArchive::Release::Suite=$suite" -o "APT::FTPArchive::Release::Codename=$suite" \
    -o "APT::FTPArchive::Release::Architectures=all" release . > Release)
}
CODENAME=$(sed -n 's/^VERSION_CODENAME=//p' /etc/os-release)
CODENAME=${CODENAME:-noble}
SUITE_SEC=$CODENAME-security SUITE_UPD=$CODENAME-updates

mkdir -p /srv/mg/files/linux-image-9.9.9-mgfake/boot
echo kernel > /srv/mg/files/linux-image-9.9.9-mgfake/boot/vmlinuz-9.9.9-mgfake
head -c 2097152 /dev/zero > /srv/mg/files/linux-image-9.9.9-mgfake/boot/initrd.img-9.9.9-mgfake
mkdeb mglib 1.0; mkdeb mglib 1.1
mkdeb mgtool 1.0; mkdeb mgtool 1.1
mkdeb mgheld 1.0; mkdeb mgheld 1.1
mkdeb linux-image-9.9.9-mgfake 9.9.9
mkdeb linux-generic-mgfake 1.0
mkdeb linux-generic-mgfake 1.1 "" linux-image-9.9.9-mgfake
mkdeb otherdkms 1.0 '[ -e /var/lib/otherdkms.ok ] || { echo "Error! Bad return status for module build on kernel: 6.6" >&2; echo "Consult /var/lib/dkms/otherdkms/1.0/build/make.log for more information." >&2; exit 1; }'
mkdeb amneziawg-dkms 1.0 '[ -e /var/lib/awgdkms.ok ] || { echo "Error! Bad return status for module build on kernel: 6.6" >&2; echo "Consult /var/lib/dkms/amneziawg/1.0/build/make.log for more information." >&2; exit 1; }'
mkdeb mgbroken 1.0 '[ -e /var/lib/mgbroken.ok ] || { echo "mgbroken: not ready yet" >&2; exit 1; }'
mkrepo /srv/mg/sec "$SUITE_SEC" /srv/mg/debs/mglib_1.1.deb
mkrepo /srv/mg/upd "$SUITE_UPD" /srv/mg/debs/mgtool_1.1.deb /srv/mg/debs/mgheld_1.1.deb
mkrepo /srv/mg/kernel "$SUITE_SEC" /srv/mg/debs/linux-image-9.9.9-mgfake_9.9.9.deb /srv/mg/debs/linux-generic-mgfake_1.1.deb

# Only the local repositories are sources; the lists of the system underneath are gone.
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/* 2> /dev/null
rm -rf /var/lib/apt/lists/* 2> /dev/null
cat > /etc/apt/sources.list.d/mg.list <<SRC
deb [trusted=yes] file:/srv/mg/sec ./
deb [trusted=yes] file:/srv/mg/upd ./
SRC
mkdir -p /var/lib/apt/lists/partial

dpkg -i /srv/mg/debs/mglib_1.0.deb /srv/mg/debs/mgtool_1.0.deb /srv/mg/debs/mgheld_1.0.deb /srv/mg/debs/linux-generic-mgfake_1.0.deb > /dev/null 2>&1
apt-mark hold mgheld > /dev/null 2>&1
check "baseline: library 1.0, tool 1.0, held package 1.0 installed" test "$(ver mglib)/$(ver mgtool)/$(ver mgheld)" = 1.0/1.0/1.0

# ---------------------------------------------------------------- 1. refresh only
job --action refresh --reboot never
check "refresh: exit 0" test "$(cat /tmp/job.rc)" = 0
check "refresh: status done, reboot not asked" test "$(field state)/$(field reboot)" = done/not_asked
check "refresh: the local lists were fetched" sh -c 'ls /var/lib/apt/lists | grep -q Packages'
check "refresh: no package changed" test "$(ver mglib)/$(ver mgtool)" = 1.0/1.0

# ---------------------------------------------------------------- 2. an interrupted package is repaired before the install
dpkg -i /srv/mg/debs/mgbroken_1.0.deb > /dev/null 2>&1
check "an interrupted install exists (mgbroken half configured)" test "$(stat3 mgbroken)" = iF
touch /var/lib/mgbroken.ok
job --action upgrade --mode security --reboot never
check "security: exit 0" test "$(cat /tmp/job.rc)" = 0
check "security: status done, reboot not asked" test "$(field state)/$(field reboot)" = done/not_asked
check "security: the interrupted package was configured first" test "$(stat3 mgbroken)" = ii
check "security: the security library was installed" test "$(ver mglib)" = 1.1
check "security: the tool that is in updates only was not" test "$(ver mgtool)" = 1.0
check "security: at least one package counted as upgraded" test "$(num upgraded)" -ge 1
check "security: no reboot command" test "$(reboots)" = 0

# ---------------------------------------------------------------- 3. all mode: the tool is installed, the held package is not
job --action upgrade --mode all --reboot never
check "all: exit 0" test "$(cat /tmp/job.rc)" = 0
check "all: the tool was installed" test "$(ver mgtool)" = 1.1
check "all: the held package was left alone" test "$(ver mgheld)" = 1.0

# ---------------------------------------------------------------- 4. the owner's case: refused with every reason, nothing rebooted
# The AmneziaWG module is LOADED (a directory under /sys/module is all the job looks for) and no flag is given: a hand-run command
# has none, and the module's use count says nothing about its tunnels.
dpkg -r mgbroken > /dev/null 2>&1; rm -f /var/lib/mgbroken.ok
dpkg -i /srv/mg/debs/linux-image-9.9.9-mgfake_9.9.9.deb > /dev/null 2>&1
dpkg -i /srv/mg/debs/mgbroken_1.0.deb > /dev/null 2>&1
mkdir -p /sys/module/amneziawg
check "the owner's case is set up: a new kernel is installed, mgbroken is interrupted, the module is loaded" test "$(stat3 mgbroken)/$(stat3 linux-image-9.9.9-mgfake)" = iF/ii
before=$(reboots)
job --action reboot --reboot if_required
check "reboot-only is refused (exit 1)" test "$(cat /tmp/job.rc)" = 1
check "reboot-only: failed, reboot held, code dpkg_interrupted" test "$(field state)/$(field reboot)/$(field code)" = failed/held/dpkg_interrupted
check "reboot-only: the reasons name the interrupted package and the missing AmneziaWG module" sh -c 'grep -q mgbroken "$0" && grep -q awg_module_missing "$0" && grep -q dpkg_interrupted "$0"' "$ST"
check "reboot-only: nothing was rebooted" test "$(reboots)" = "$before"
job --action upgrade --mode security --reboot if_required
check "security + reboot is refused while the package stays interrupted" test "$(cat /tmp/job.rc)" = 1
check "security + reboot: nothing was rebooted" test "$(reboots)" = "$before"
check "security + reboot: the code is dpkg_broken" test "$(field code)" = dpkg_broken
touch /var/lib/mgbroken.ok
dpkg --configure -a > /dev/null 2>&1
# Repaired, but the module for the new kernel does not exist: the module alone holds the reboot, flag or no flag.
job --action reboot --reboot if_required
check "after the repair the loaded module alone holds the reboot (no flag)" test "$(field code)/$(field held_reason)" = awg_module_missing/awg_module_missing
check "after the repair: nothing was rebooted" test "$(reboots)" = "$before"
rmdir /sys/module/amneziawg
job --action reboot --reboot if_required --awg-required
check "the flag alone (module not loaded) holds the reboot too" test "$(field code)" = awg_module_missing
check "the flag: nothing was rebooted" test "$(reboots)" = "$before"

# ---------------------------------------------------------------- 5. a clean reboot: the reboot command is the job's last act
dpkg -r linux-image-9.9.9-mgfake > /dev/null 2>&1
printf 'deb [trusted=yes] file:/srv/mg/kernel ./\n' > /etc/apt/sources.list.d/mg-kernel.list
rm -f /var/lib/mgtest-systemctl-mark; : > /var/log/mgtest-systemctl.log
job --action upgrade --mode security --reboot if_required
check "security + if_required with a new kernel: the job ended (stopped by the stand-in's shutdown), exit 0" test "$(cat /tmp/job.rc)" = 0
check "the kernel was installed with its boot files" sh -c 'test -s /boot/vmlinuz-9.9.9-mgfake && test "$(stat -c %s /boot/initrd.img-9.9.9-mgfake)" -ge 1048576'
check "the status a rebooted host would find: running, phase rebooting, kernel_to 9.9.9-mgfake" test "$(field state)/$(field phase)/$(field kernel_to)" = running/rebooting/9.9.9-mgfake
check "the status has the boot id of the boot the job started on" test -n "$(field boot_id)"
check "systemctl reboot was the only systemctl call, made once" test "$(cat /var/log/mgtest-systemctl.log)" = reboot
rm -f /etc/apt/sources.list.d/mg-kernel.list

# ---------------------------------------------------------------- 6. refusals that run nothing
# The job above is "running, rebooting" on this boot: a new job is refused as busy until the host really restarts (the
# agent settles it then). The scenario plays that restart by removing the file.
job --action refresh --reboot never
check "while a job waits for its reboot, a new one is refused (busy)" test "$(cat /tmp/job.rc)" = 1
rm -f "$ST"
sum_before=$(md5sum /var/lib/dpkg/status | cut -d' ' -f1)
cp /etc/os-release /tmp/os-release.keep
printf 'ID=alpine\nVERSION_ID=3.20\n' > /etc/os-release
job --action upgrade --mode all --reboot never
check "unsupported system: exit 1, code unsupported_os" test "$(cat /tmp/job.rc)/$(field code)" = 1/unsupported_os
cp /tmp/os-release.keep /etc/os-release
check "unsupported system: the package database is untouched" test "$(md5sum /var/lib/dpkg/status | cut -d' ' -f1)" = "$sum_before"

now=$(date +%s)
printf '{"job_id":"osj_other","action":"upgrade","reboot_policy":"never","state":"running","started":%s,"updated":%s,"boot_id":"%s"}' "$now" "$now" "$(cat /proc/sys/kernel/random/boot_id)" > "$ST"
job --action refresh --reboot never
check "another live job: refused with code busy" test "$(cat /tmp/job.rc)" = 1
check "another live job: its status file is left as it was" grep -q osj_other "$ST"
rm -f "$ST"

# ---------------------------------------------------------------- 7. DKMS failures, the loaded module and an unknown kernel
# The kernel of scenario 5 is installed and the AmneziaWG module is loaded again. Nothing below may reboot.
rm -f "$ST"; mkdir -p /sys/module/amneziawg
before=$(reboots)

# 7a. ANOTHER module's DKMS build breaks dpkg: the repair fails (dpkg_broken) and AmneziaWG's packages are left alone.
touch /var/lib/awgdkms.ok
dpkg -i /srv/mg/debs/amneziawg-dkms_1.0.deb > /dev/null 2>&1
dpkg -i /srv/mg/debs/otherdkms_1.0.deb > /dev/null 2>&1
check "7a is set up: amneziawg-dkms is fine, another DKMS package is interrupted" test "$(stat3 amneziawg-dkms)/$(stat3 otherdkms)" = ii/iF
job --action upgrade --mode security --reboot if_required
check "7a: another module's failure is dpkg_broken, exit 1" test "$(cat /tmp/job.rc)/$(field code)" = 1/dpkg_broken
check "7a: AmneziaWG's packages were not touched" test "$(stat3 amneziawg-dkms)" = ii
check "7a: nothing was rebooted" test "$(reboots)" = "$before"
touch /var/lib/otherdkms.ok; dpkg --configure -a > /dev/null 2>&1

# 7b. the module's OWN build breaks dpkg: the job stops (awg_module_build_failed), removes nothing and installs nothing more.
rm -f /var/lib/awgdkms.ok
dpkg -r amneziawg-dkms > /dev/null 2>&1
dpkg -i /srv/mg/debs/amneziawg-dkms_1.0.deb > /dev/null 2>&1
check "7b is set up: amneziawg-dkms is half configured" test "$(stat3 amneziawg-dkms)" = iF
rm -f "$ST"
job --action upgrade --mode security --reboot if_required
check "7b: exit 1, failed, code awg_module_build_failed" test "$(cat /tmp/job.rc)/$(field state)/$(field code)" = 1/failed/awg_module_build_failed
check "7b: it stopped in the repair, before any refresh or install" test "$(field phase)" = repairing
check "7b: the package named is the headers of the kernel dkms named, and the reboot is held" sh -c 'grep -q "\"package\":\"linux-headers-6.6\"" "$0" && grep -q "\"reboot\":\"held\"" "$0"' "$ST"
check "7b: the reason names the kernel and does not send the owner to prepare-kernel" sh -c 'grep -q "kernel 6.6" "$0" && ! grep -q prepare-kernel "$0"' "$ST"
check "7b: NOTHING was removed: the module's package is still there, half configured" test "$(stat3 amneziawg-dkms)" = iF
check "7b: nothing was rebooted" test "$(reboots)" = "$before"
rm -f "$ST"
job --action upgrade --mode security --reboot never
check "7b': with reboot never the job is failed/awg_module_build_failed too (never done)" test "$(cat /tmp/job.rc)/$(field code)" = 1/awg_module_build_failed
touch /var/lib/awgdkms.ok
dpkg --configure -a > /dev/null 2>&1
check "7b: after the owner's fix the package configures" test "$(stat3 amneziawg-dkms)" = ii

# 7c. the module is loaded, no flag, a clean dpkg, a kernel the module is not built for: refused.
rm -f "$ST"
job --action reboot --reboot if_required
check "7c: a loaded module without a flag holds the reboot (awg_module_missing)" test "$(cat /tmp/job.rc)/$(field code)" = 1/awg_module_missing
check "7c: nothing was rebooted" test "$(reboots)" = "$before"

# 7d. linux-version fails: the kernel of the next boot is unknown, and that refuses the reboot.
rm -f "$ST"; touch /var/lib/mgtest-lv-fail
job --action reboot --reboot if_required
check "7d: an unknown kernel is refused (kernel_unknown)" test "$(cat /tmp/job.rc)/$(field code)" = 1/kernel_unknown
check "7d: nothing was rebooted" test "$(reboots)" = "$before"
rm -f /var/lib/mgtest-lv-fail "$ST"; rmdir /sys/module/amneziawg

# 7e. GRUB may not start the first entry: refused, whatever the kernels are.
rm -f "$ST"
printf 'GRUB_DEFAULT=saved\n' > /etc/default/grub
job --action reboot --reboot always
check "7e: GRUB_DEFAULT=saved is refused (boot_default_custom)" test "$(cat /tmp/job.rc)/$(field code)" = 1/boot_default_custom
printf 'GRUB_DEFAULT=0\n' > /etc/default/grub
printf 'GRUB_DEFAULT=2\n' > /etc/default/grub.d/90-custom.cfg 2> /dev/null || { mkdir -p /etc/default/grub.d; printf 'GRUB_DEFAULT=2\n' > /etc/default/grub.d/90-custom.cfg; }
rm -f "$ST"
job --action reboot --reboot always
check "7e: a drop-in that sets another default is refused" test "$(cat /tmp/job.rc)/$(field code)" = 1/boot_default_custom
rm -f /etc/default/grub.d/90-custom.cfg
echo "next_entry=1" > /var/lib/mgtest-grubenv
rm -f "$ST"
job --action reboot --reboot always
check "7e: a next_entry in grub-editenv is refused" test "$(cat /tmp/job.rc)/$(field code)" = 1/boot_default_custom
rm -f /var/lib/mgtest-grubenv
mv /boot/grub/grub.cfg /boot/grub/grub.cfg.away
rm -f "$ST"
job --action reboot --reboot always
check "7e: no grub.cfg is refused" test "$(cat /tmp/job.rc)/$(field code)" = 1/boot_default_custom
mv /boot/grub/grub.cfg.away /boot/grub/grub.cfg
check "7e: nothing was rebooted" test "$(reboots)" = "$before"

# 7f. the reboot history of the panel's jobs: one an hour. (A manual- job is not counted; scenario 5 used one.)
rm -f "$ST" "$STATE/os-reboots.json"
job --action reboot --reboot always --job-id osj_e2e1
check "7f: the first panel job reboots (the stand-in stops it), exit 0" test "$(cat /tmp/job.rc)" = 0
check "7f: the reboot command was given once" test "$(reboots)" = "$((before + 1))"
check "7f: the history is on disk next to the status file" test -s "$STATE/os-reboots.json"
rm -f "$ST"
job --action reboot --reboot always --job-id osj_e2e2
check "7f: the second one within the hour is refused (reboot_limit)" test "$(cat /tmp/job.rc)/$(field code)" = 1/reboot_limit
check "7f: and nothing more was rebooted" test "$(reboots)" = "$((before + 1))"
rm -f "$ST"
job --action reboot --reboot always --job-id manual-e2e3
check "7f: a manual job is not limited" test "$(cat /tmp/job.rc)" = 0
check "7f: a manual reboot was given" test "$(reboots)" = "$((before + 2))"
rm -f "$ST" "$STATE/os-reboots.json"

for bad in "--action dist-upgrade" "--action upgrade --mode everything" "--action reboot --reboot never" "--action refresh --reboot always" "--job-id x;reboot"; do
  # shellcheck disable=SC2086
  job $bad
  check "bad request refused by the command line: $bad" test "$(cat /tmp/job.rc)" = 2
done

echo
if [ "$fails" = 0 ]; then echo "PASS: the OS update job, end to end, nothing rebooted"; else echo "FAILED: $fails checks"; fi
exit "$fails"
