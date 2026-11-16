#!/usr/bin/env bash
# Fetches the official TrustTunnel client and, as a reference server, their endpoint, for the TrustTunnel stand
# (scripts/e2e/trusttunnel.sh). The stand compares our server with what their client really does; nothing of this is ever
# shipped, installed on a node or run outside the stand's network namespaces.
#
#   scripts/e2e/tt-fetch.sh [client|endpoint|all]      (default: all; run it in WSL, as the user that runs the stand)
#
# What it does for each archive (the owner allowed exactly these two, 2026-11-15):
#   1. downloads it from the release page of github.com/TrustTunnel into a NEW empty directory of its own,
#   2. checks its sha256 against the value below (the digest GitHub shows for the release asset),
#   3. lists it (tar -tzf) and refuses an absolute path, a ".." component or a link, then unpacks without taking the owner or
#      the modes of the archive,
#   4. verifies the GPG signatures that sit inside the archive, in a temporary GNUPGHOME, against the key with the
#      fingerprint of their VERIFY_RELEASES.md. When gpg is missing or the key cannot be fetched it stays at sha256 and says so
#      (VERIFIED: "gpg=skipped: ..."): a deviation, never a silent pass. Nothing is installed to make it work.
# The result is $TT_CACHE/<name>/ with a VERIFIED file. A directory that exists without VERIFIED is left alone and the script
# fails: it never reuses a half-made or foreign directory.
set -euo pipefail

TT_CACHE="${TT_CACHE:-$HOME/.cache/mistgate-tests/trusttunnel}"
GPG_FPR="28645AC9776EC4C00BCE2AFC0FE641E7235E2EC6"
GPG_SERVER="${TT_GPG_SERVER:-hkps://keys.openpgp.org}"

CLIENT_NAME="client-v1.1.12"
CLIENT_URL="https://github.com/TrustTunnel/TrustTunnelClient/releases/download/v1.1.12/trusttunnel_client-v1.1.12-linux-x86_64.tar.gz"
CLIENT_SHA256="75b7df59351e26462487464de5f3462153e00195f2d40677872fb93a5b44c488"
CLIENT_MAX=$((16 * 1024 * 1024))

ENDPOINT_NAME="endpoint-v1.1.0"
ENDPOINT_URL="https://github.com/TrustTunnel/TrustTunnel/releases/download/v1.1.0/trusttunnel-v1.1.0-linux-x86_64.tar.gz"
ENDPOINT_SHA256="91c2ea3db7416a01b5258a4c047ec22890490bc55e1b194206031aa75144f0e7"
ENDPOINT_MAX=$((32 * 1024 * 1024))

CLEAN=()
trap 'for d in "${CLEAN[@]:-}"; do [ -n "$d" ] && rm -rf "$d"; done' EXIT
die() { echo "tt-fetch: $*" >&2; exit 1; }

fetch_one() {
  local name="$1" url="$2" want="$3" max="$4"
  local dest="$TT_CACHE/$name" work gnupg
  if [ -e "$dest" ]; then
    [ -f "$dest/VERIFIED" ] || die "$dest exists but has no VERIFIED file; remove it by hand and run again (it is never reused)"
    echo "tt-fetch: $name already there: $(cat "$dest/VERIFIED")"
    return 0
  fi
  mkdir -p "$TT_CACHE"
  work="$(mktemp -d "$TT_CACHE/.fetch-$name.XXXXXX")"
  gnupg="$(mktemp -d "$TT_CACHE/.gnupg-$name.XXXXXX")"
  chmod 700 "$gnupg"
  CLEAN+=("$work" "$gnupg")

  local archive="$work/archive.tar.gz" tree="$work/tree"
  echo "tt-fetch: downloading $url"
  curl -fsSL --proto '=https' --tlsv1.2 --max-filesize "$max" -o "$archive" "$url"
  local got
  got="$(sha256sum "$archive" | cut -d' ' -f1)"
  [ "$got" = "$want" ] || die "$name: sha256 $got, expected $want"
  echo "tt-fetch: $name sha256 ok"

  # Refuse unsafe members before anything is written: absolute paths, "..", links and device files.
  local listing
  listing="$(tar -tzf "$archive")" || die "$name: tar cannot list the archive"
  if grep -Eq '(^/|(^|/)\.\.(/|$))' <<<"$listing"; then die "$name: the archive has an absolute path or a '..' component"; fi
  if tar -tvzf "$archive" | grep -Eq '^[lhbcp]'; then die "$name: the archive has a link or a special file"; fi
  mkdir "$tree"
  tar --no-same-owner --no-same-permissions -xzf "$archive" -C "$tree"

  local gpg_status="skipped: gpg is not installed"
  if command -v gpg >/dev/null 2>&1; then
    gpg_status="skipped: the key could not be fetched from $GPG_SERVER"
    if GNUPGHOME="$gnupg" gpg --batch --quiet --keyserver "$GPG_SERVER" --recv-keys "$GPG_FPR" >/dev/null 2>&1; then
      # The key we got has to be the one whose fingerprint is written above, not just any key that answers.
      GNUPGHOME="$gnupg" gpg --batch --with-colons --fingerprint "$GPG_FPR" 2>/dev/null | grep -q "^fpr:::::::::$GPG_FPR:" ||
        die "$name: the fetched key has another fingerprint"
      local sig bin n=0
      while IFS= read -r -d '' sig; do
        bin="${sig%.sig}"
        [ -f "$bin" ] || die "$name: $sig has no file to check"
        local out
        out="$(GNUPGHOME="$gnupg" gpg --batch --status-fd 1 --verify "$sig" "$bin" 2>/dev/null)" || die "$name: bad signature of ${bin#"$tree"/}"
        grep -q "^\[GNUPG:\] VALIDSIG $GPG_FPR " <<<"$out" || die "$name: ${bin#"$tree"/} is not signed by $GPG_FPR"
        grep -q '^\[GNUPG:\] GOODSIG ' <<<"$out" || die "$name: no GOODSIG for ${bin#"$tree"/}"
        n=$((n + 1))
      done < <(find "$tree" -type f -name '*.sig' -print0)
      [ "$n" -ge 1 ] || die "$name: the archive has no .sig file to verify"
      gpg_status="ok ($n signatures by $GPG_FPR)"
    fi
  fi
  [ "${gpg_status%% *}" = "ok" ] || echo "tt-fetch: WARNING $name: gpg $gpg_status; only sha256 was checked" >&2

  find "$tree" -type f -exec chmod u+rw,go+r-w {} +
  find "$tree" -type f \( -name 'trusttunnel_client' -o -name 'setup_wizard' -o -name 'trusttunnel_endpoint' \) -exec chmod 0755 {} +
  printf 'sha256=ok url=%s gpg=%s\n' "$url" "$gpg_status" >"$tree/VERIFIED"
  mv "$tree" "$dest"
  echo "tt-fetch: $name -> $dest ($(cat "$dest/VERIFIED"))"
}

what="${1:-all}"
case "$what" in
  client | all) fetch_one "$CLIENT_NAME" "$CLIENT_URL" "$CLIENT_SHA256" "$CLIENT_MAX" ;;
esac
case "$what" in
  endpoint | all) fetch_one "$ENDPOINT_NAME" "$ENDPOINT_URL" "$ENDPOINT_SHA256" "$ENDPOINT_MAX" ;;
esac
case "$what" in
  client | endpoint | all) ;;
  *) die "usage: tt-fetch.sh [client|endpoint|all]" ;;
esac
