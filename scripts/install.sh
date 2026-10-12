#!/usr/bin/env bash
set -euo pipefail

readonly RELEASES_URL="https://github.com/Mistgate/mistgate"
readonly RELEASE_PUBLIC_KEY_BASE64="6ylEpIbbde2c7e7FcaC/Wc5DAmHfOHsPsOqiB8HaU3c="
readonly RELEASE_PUBLIC_KEY_SSH_BASE64="AAAAC3NzaC1lZDI1NTE5AAAAIOspRKSG23XtnO3uxXGgv1nOQwJh3zh7D7DqogfB2lN3"
readonly PANEL_SIGNATURE_CONTEXT="mistgate panel manifest v1"
readonly PANEL_SSHSIG_NAMESPACE="mistgate-panel-manifest-v1"
readonly DEFAULT_DATA_DIR="/var/lib/mistgate"
readonly UNIT_FILE="/etc/systemd/system/mistgate.service"

public_url=""
admin_host=""
admin_listen=""
version=""
data_dir="$DEFAULT_DATA_DIR"
acme_domains=()
dry_run=0
yes=0
skip_signature=0
test_mode=0
test_base_url=""
test_key=""
test_ssh_public_key=""
test_arch=""
test_os=""
test_bin_dir="/usr/local/bin"
test_skip_systemd=0
test_skip_port_check=0

usage() {
  cat <<'USAGE'
Usage: install.sh --public-url URL [options]

Install the latest Mistgate panel on a fresh Linux server with systemd.

Required:
  --public-url URL       Public HTTPS URL, for example https://panel.example.com

Options:
  --admin-host HOST      Reach the admin on a separate host name
  --admin-listen ADDR    Reach the admin on a separate listener
  --acme-domain DOMAIN   Request a Let's Encrypt certificate (repeatable; defaults to the URL host)
  --version vX.Y.Z       Install a specific release (default: latest)
  --data-dir DIR         Panel data directory (default: /var/lib/mistgate)
  --dry-run              Download and verify, then print the planned changes
  --yes                  Do not ask for confirmation
  --insecure-skip-signature
                         Skip Ed25519 signature verification (expert use only)
  --help                 Show this help

Example:
  curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com
USAGE
}

fail() {
  printf 'mistgate installer: %s\n' "$1" >&2
  exit 1
}

need_value() {
  [[ $# -ge 2 && -n "$2" ]] || fail "$1 needs a value"
}

is_pre_sshsig_release() {
  local release_version="$1" major minor patch
  if [[ ! "$release_version" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    return 1
  fi
  major=${BASH_REMATCH[1]}
  minor=${BASH_REMATCH[2]}
  patch=${BASH_REMATCH[3]}
  while [[ "$major" == 0* && ${#major} -gt 1 ]]; do major=${major#0}; done
  while [[ "$minor" == 0* && ${#minor} -gt 1 ]]; do minor=${minor#0}; done
  while [[ "$patch" == 0* && ${#patch} -gt 1 ]]; do patch=${patch#0}; done
  [[ "$major" == "0" && "$minor" == "0" ]] && return 0
  [[ "$major" == "0" && "$minor" == "1" ]] || return 1
  [[ "$patch" =~ ^[0-9]$ || "$patch" =~ ^[12][0-9]$ || "$patch" =~ ^3[0-3]$ ]]
}

openssl_ed25519ctx_supported() {
  local openssl_version openssl_major openssl_minor
  command -v openssl >/dev/null 2>&1 || return 1
  openssl_version=$(openssl version 2>/dev/null | awk '{print $2}')
  [[ "$openssl_version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] || return 1
  openssl_major=${BASH_REMATCH[1]}
  openssl_minor=${BASH_REMATCH[2]}
  ((openssl_major > 3 || (openssl_major == 3 && openssl_minor >= 2)))
}

ssh_keygen_y_supported() {
  local usage
  command -v ssh-keygen >/dev/null 2>&1 || return 1
  usage=$(ssh-keygen -Y 2>&1 || true)
  [[ "$usage" == *verify* ]]
}

verify_panel_manifest_with_openssl() {
  local openssl_version
  if ! openssl_ed25519ctx_supported; then
    return 1
  fi
  openssl_version=$(openssl version 2>/dev/null | awk '{print $2}')
  sig_size=$(wc -c < "$tmp_dir/panel-manifest.sig" | tr -d '[:space:]')
  [[ "$sig_size" == "64" ]] || fail "panel-manifest.sig is not a raw 64-byte Ed25519 signature"
  printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00' > "$tmp_dir/release-key.der"
  if ! printf '%s' "$release_public_key" | base64 --decode >> "$tmp_dir/release-key.der" 2>/dev/null; then
    fail "the release public key is not valid base64"
  fi
  raw_key_size=$(wc -c < "$tmp_dir/release-key.der" | tr -d '[:space:]')
  [[ "$raw_key_size" == "44" ]] || fail "the release public key must encode 32 Ed25519 bytes"
  if ! openssl pkey -pubin -inform DER -in "$tmp_dir/release-key.der" -out "$tmp_dir/release-key.pem" >/dev/null 2>&1; then
    fail "could not read the embedded Ed25519 public key"
  fi
  if ! openssl pkeyutl -verify -pubin -inkey "$tmp_dir/release-key.pem" -rawin \
    -in "$tmp_dir/panel-manifest.json" -sigfile "$tmp_dir/panel-manifest.sig" \
    -pkeyopt instance:Ed25519ctx -pkeyopt "context-string:$PANEL_SIGNATURE_CONTEXT" >/dev/null 2>&1; then
    fail "panel-manifest.sig did not verify with the official release key"
  fi
  printf 'Verified panel manifest with OpenSSL %s.\n' "$openssl_version"
}

verify_panel_manifest_with_ssh_keygen() {
  printf 'mistgate-release ssh-ed25519 %s\n' "$release_ssh_public_key" > "$tmp_dir/allowed_signers"
  if ! ssh-keygen -Y verify -f "$tmp_dir/allowed_signers" -I mistgate-release \
    -n "$PANEL_SSHSIG_NAMESPACE" -s "$tmp_dir/panel-manifest.sshsig" < "$tmp_dir/panel-manifest.json" >/dev/null 2>&1; then
    fail "panel-manifest.sshsig did not verify with the official release key"
  fi
  printf 'Verified panel manifest with ssh-keygen.\n'
}

while (($#)); do
  case "$1" in
    --public-url)
      need_value "$1" "${2:-}"
      public_url=$2
      shift 2
      ;;
    --admin-host)
      need_value "$1" "${2:-}"
      admin_host=$2
      shift 2
      ;;
    --admin-listen)
      need_value "$1" "${2:-}"
      admin_listen=$2
      shift 2
      ;;
    --acme-domain)
      need_value "$1" "${2:-}"
      acme_domains+=("$2")
      shift 2
      ;;
    --version)
      need_value "$1" "${2:-}"
      version=$2
      shift 2
      ;;
    --data-dir)
      need_value "$1" "${2:-}"
      data_dir=$2
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    --yes)
      yes=1
      shift
      ;;
    --insecure-skip-signature)
      skip_signature=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1 (use --help)"
      ;;
  esac
done

if [[ "${MISTGATE_INSTALL_TEST:-}" == "1" ]]; then
  test_mode=1
  test_base_url=${MISTGATE_INSTALL_BASE_URL:-}
  test_key=${MISTGATE_INSTALL_TEST_KEY:-}
  test_ssh_public_key=${MISTGATE_INSTALL_TEST_SSH_PUBLIC_KEY_BASE64:-}
  test_arch=${MISTGATE_INSTALL_TEST_ARCH:-}
  test_os=${MISTGATE_INSTALL_TEST_OS:-}
  test_bin_dir=${MISTGATE_INSTALL_TEST_BIN_DIR:-/usr/local/bin}
  test_skip_systemd=${MISTGATE_INSTALL_SKIP_SYSTEMD:-0}
  test_skip_port_check=${MISTGATE_INSTALL_SKIP_PORT_CHECK:-0}
else
  for name in MISTGATE_INSTALL_BASE_URL MISTGATE_INSTALL_TEST_KEY MISTGATE_INSTALL_TEST_ARCH \
    MISTGATE_INSTALL_TEST_SSH_PUBLIC_KEY_BASE64 MISTGATE_INSTALL_TEST_OS MISTGATE_INSTALL_TEST_BIN_DIR MISTGATE_INSTALL_SKIP_SYSTEMD \
    MISTGATE_INSTALL_SKIP_PORT_CHECK; do
    [[ -z "${!name:-}" ]] || fail "$name is only available when MISTGATE_INSTALL_TEST=1"
  done
fi

if [[ -z "$public_url" ]]; then
  fail "--public-url is required. Run: curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url https://panel.example.com"
fi
if [[ ! "$public_url" =~ ^https://([A-Za-z0-9.-]+)(/[^#]*)?$ ]]; then
  fail "--public-url must be an HTTPS URL with a host and no fragment"
fi
public_host=${BASH_REMATCH[1]}
if [[ "$public_host" == .* || "$public_host" == *..* || "$public_host" == *- || "$public_host" == *.-* ]]; then
  fail "--public-url must contain a valid public host name"
fi
if [[ -n "$admin_host" && -n "$admin_listen" ]]; then
  fail "--admin-host and --admin-listen cannot be combined"
fi
if [[ -n "$version" && ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "--version must be a release tag such as v1.2.3"
fi
if [[ ! "$data_dir" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
  fail "--data-dir must be an absolute path made of letters, digits and . _ - /"
fi
if [[ "$data_dir" == /home || "$data_dir" == /home/* || "$data_dir" == /root || "$data_dir" == /root/* ]]; then
  fail "--data-dir cannot be under /home or /root because the service protects home directories"
fi
if [[ -z "${acme_domains[*]}" ]]; then
  acme_domains=("$public_host")
fi
for domain in "${acme_domains[@]}"; do
  [[ "$domain" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ && "$domain" != *..* && "$domain" != *.-* && "$domain" != *-.* ]] ||
    fail "invalid --acme-domain: $domain"
done
printf '%s\n' "${acme_domains[@]}" | grep -Fxq -- "$public_host" ||
  fail "--acme-domain must include the host from --public-url ($public_host)"
if [[ -n "$admin_host" ]]; then
  [[ "$admin_host" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ && "$admin_host" != *..* ]] ||
    fail "invalid --admin-host: $admin_host"
  printf '%s\n' "${acme_domains[@]}" | grep -Fxq -- "$admin_host" ||
    fail "--acme-domain must include --admin-host ($admin_host) so its admin URL has a certificate"
fi

if [[ "$test_mode" == "1" ]]; then
  [[ "$test_skip_systemd" == "0" || "$test_skip_systemd" == "1" ]] || fail "invalid test systemd setting"
  [[ "$test_skip_port_check" == "0" || "$test_skip_port_check" == "1" ]] || fail "invalid test port-check setting"
  if [[ -n "$test_base_url" && ! "$test_base_url" =~ ^http://(127\.0\.0\.1|localhost):[0-9]+(/.*)?$ ]]; then
    fail "test base URL must use localhost over HTTP or HTTPS"
  fi
fi

printf '1. Check the host and required tools.\n'
host_os=$(uname -s)
[[ -z "$test_os" ]] || host_os=$test_os
[[ "$host_os" == "Linux" ]] || fail "Linux is required (found $host_os)"
machine=$(uname -m)
[[ -z "$test_arch" ]] || machine=$test_arch
case "$machine" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) fail "unsupported architecture $machine; use x86_64 (amd64) or aarch64 (arm64)" ;;
esac
if [[ "$test_mode" != "1" && "$(id -u)" != "0" ]]; then
  fail "run as root (for example, add sudo before bash)"
fi
if [[ "$test_skip_systemd" != "1" ]]; then
  [[ -d /run/systemd/system ]] || fail "systemd is required"
  command -v systemctl >/dev/null 2>&1 || fail "systemctl is required"
fi
for tool in curl sha256sum base64 awk grep install mktemp sed wc tr date; do
  command -v "$tool" >/dev/null 2>&1 || fail "required command not found: $tool"
done
if [[ "$test_skip_port_check" != "1" ]]; then
  command -v ss >/dev/null 2>&1 || fail "required command not found: ss (install iproute2)"
fi
if [[ "$test_skip_systemd" != "1" ]]; then
  command -v journalctl >/dev/null 2>&1 || fail "journalctl is required"
  command -v sleep >/dev/null 2>&1 || fail "sleep is required"
fi
if [[ "$skip_signature" == "1" ]]; then
  printf 'WARNING: signature verification is disabled by --insecure-skip-signature.\n' >&2
fi

if [[ -n "$test_key" && "$test_mode" == "1" ]]; then
  release_public_key=$test_key
else
  release_public_key=$RELEASE_PUBLIC_KEY_BASE64
fi
if [[ -n "$test_ssh_public_key" && "$test_mode" == "1" ]]; then
  release_ssh_public_key=$test_ssh_public_key
else
  release_ssh_public_key=$RELEASE_PUBLIC_KEY_SSH_BASE64
fi
if [[ "$skip_signature" != "1" ]]; then
  [[ "$release_public_key" =~ ^[A-Za-z0-9+/]{43}=$ ]] || fail "the embedded release public key is not valid base64"
  [[ "$release_ssh_public_key" =~ ^[A-Za-z0-9+/]+={0,2}$ ]] || fail "the embedded SSH release public key is not valid base64"
fi

if [[ "$test_mode" == "1" ]]; then
  base_url=${test_base_url:-$RELEASES_URL}
  install_bin_dir=$test_bin_dir
else
  base_url=$RELEASES_URL
  install_bin_dir=/usr/local/bin
fi
if [[ -n "$version" ]]; then
  release_path="$base_url/releases/download/$version"
else
  release_path="$base_url/releases/latest/download"
fi
binary_name="mistgate-linux-$arch"
unit_file="$UNIT_FILE"
if [[ "$test_mode" == "1" && -n "${MISTGATE_INSTALL_TEST_SYSTEMD_DIR:-}" ]]; then
  unit_file="${MISTGATE_INSTALL_TEST_SYSTEMD_DIR%/}/mistgate.service"
fi

printf '2. Refuse existing installs and check the public ports.\n'
[[ ! -e "$install_bin_dir/mistgate" ]] || fail "${install_bin_dir}/mistgate already exists; update the panel from its admin page"
if [[ -d "$data_dir" ]]; then
  shopt -s nullglob dotglob
  data_entries=("$data_dir"/*)
  shopt -u nullglob dotglob
  ((${#data_entries[@]} == 0)) || fail "$data_dir already contains data; setup must not run twice. Use the panel's update page to update Mistgate"
fi
if [[ "$test_skip_systemd" != "1" ]]; then
  systemctl cat mistgate.service >/dev/null 2>&1 && fail "mistgate.service already exists; use the panel's update page to update Mistgate"
  [[ ! -e "$unit_file" ]] || fail "$unit_file already exists; use the panel's update page to update Mistgate"
fi
if [[ "$test_skip_port_check" != "1" ]]; then
  for port in 443 80; do
    if ss -H -ltn "sport = :$port" | grep -q .; then
      fail "TCP port $port is already in use; free it before installing Mistgate"
    fi
  done
fi
if [[ "$test_skip_systemd" != "1" ]]; then
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
    printf 'Firewall: UFW is active. Allow TCP 443 and 80 for the panel and ACME: ufw allow 443/tcp; ufw allow 80/tcp\n'
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    printf 'Firewall: firewalld is active. Allow TCP 443 and 80 for the panel and ACME: firewall-cmd --permanent --add-service=https; firewall-cmd --permanent --add-service=http; firewall-cmd --reload\n'
  fi
fi
if [[ "$dry_run" != "1" && "$yes" != "1" ]]; then
  if [[ -t 0 ]]; then
    prompt_fd=0
  elif [[ -r /dev/tty ]]; then
    exec 3<>/dev/tty
    prompt_fd=3
  else
    fail "no terminal is available; run: curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --public-url $public_url --yes"
  fi
  printf 'Install Mistgate on this server and request TLS certificates for %s? [y/N] ' "${acme_domains[*]}" >&"$prompt_fd"
  IFS= read -r answer <&"$prompt_fd" || answer=""
  [[ "$answer" == "y" || "$answer" == "Y" || "$answer" == "yes" || "$answer" == "YES" ]] || fail "cancelled"
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/mistgate-install.XXXXXX")
trap 'rm -rf -- "$tmp_dir"' EXIT

printf '3. Download and verify %s.\n' "${version:-the latest release}"
curl_args=(--fail --silent --show-error --location --retry 5 --retry-delay 1)
if [[ "$test_mode" == "1" && "$base_url" == http://* ]]; then
  curl_args+=(--proto '=http,https')
else
  curl_args+=(--proto '=https' --tlsv1.2)
fi
for asset in "$binary_name" SHA256SUMS panel-manifest.json panel-manifest.sig; do
  if ! curl "${curl_args[@]}" --output "$tmp_dir/$asset" "$release_path/$asset"; then
    fail "could not download release asset $asset"
  fi
done
optional_curl_args=(--silent --show-error --location --retry 5 --retry-delay 1)
if [[ "$test_mode" == "1" && "$base_url" == http://* ]]; then
  optional_curl_args+=(--proto '=http,https')
else
  optional_curl_args+=(--proto '=https' --tlsv1.2)
fi
if ! sshsig_http_status=$(curl "${optional_curl_args[@]}" --output "$tmp_dir/panel-manifest.sshsig" \
  --write-out '%{http_code}' "$release_path/panel-manifest.sshsig"); then
  fail "could not request panel-manifest.sshsig"
fi
sshsig_present=0
case "$sshsig_http_status" in
  200) sshsig_present=1 ;;
  404) rm -f -- "$tmp_dir/panel-manifest.sshsig" ;;
  *) fail "could not download panel-manifest.sshsig (HTTP $sshsig_http_status)" ;;
esac

sum_count=$(awk -v file="$binary_name" '$2 == file || $2 == "*" file { count++ } END { print count + 0 }' "$tmp_dir/SHA256SUMS")
[[ "$sum_count" == "1" ]] || fail "SHA256SUMS must contain exactly one entry for $binary_name"
expected_sha=$(awk -v file="$binary_name" '$2 == file || $2 == "*" file { print $1 }' "$tmp_dir/SHA256SUMS")
[[ "$expected_sha" =~ ^[a-fA-F0-9]{64}$ ]] || fail "SHA256SUMS has an invalid hash for $binary_name"
actual_sha=$(sha256sum -- "$tmp_dir/$binary_name" | awk '{ print $1 }')
[[ "${actual_sha,,}" == "${expected_sha,,}" ]] || fail "SHA256SUMS verification failed for $binary_name"

untrusted_manifest_version=$(awk -F'"' '/"version"[[:space:]]*:/ { version=$4; versions++ } END { if (versions == 1) print version }' "$tmp_dir/panel-manifest.json")
[[ "$untrusted_manifest_version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$ ]] || fail "panel-manifest.json has an invalid version"

if [[ "$skip_signature" != "1" ]]; then
  ssh_keygen_supported=0
  if ssh_keygen_y_supported; then
    ssh_keygen_supported=1
  fi
  if [[ "$sshsig_present" == "1" && "$ssh_keygen_supported" == "1" ]]; then
    verify_panel_manifest_with_ssh_keygen
  else
    legacy_release=0
    if [[ "$sshsig_present" == "0" ]]; then
      if is_pre_sshsig_release "$untrusted_manifest_version"; then
        legacy_release=1
      else
        fail "panel-manifest.sshsig is missing from release $untrusted_manifest_version (required since v0.1.34)"
      fi
    fi
    if ! openssl_ed25519ctx_supported; then
      openssl_version="not installed"
      if command -v openssl >/dev/null 2>&1; then
        openssl_version=$(openssl version 2>/dev/null | awk '{print $2}')
        [[ -n "$openssl_version" ]] || openssl_version="unknown version"
      fi
      if [[ "$legacy_release" == "1" ]]; then
        fail "release $untrusted_manifest_version predates panel-manifest.sshsig; OpenSSL 3.2 or newer is required to verify panel-manifest.sig (found $openssl_version)"
      fi
      fail "ssh-keygen with -Y verify or OpenSSL 3.2 or newer is required to verify the release signature (found OpenSSL $openssl_version)"
    fi
    if [[ "$legacy_release" == "1" ]]; then
      printf 'Release %s predates panel-manifest.sshsig; using OpenSSL 3.2 or newer.\n' "$untrusted_manifest_version"
    fi
    verify_panel_manifest_with_openssl
  fi
fi

IFS=$'\t' read -r manifest_schema manifest_version manifest_built manifest_expires entry_count entry_os entry_arch entry_size entry_sha < <(
  awk -F'"' -v wanted="$binary_name" '
    /"schema"[[:space:]]*:/ { x=$3; sub(/^[[:space:]]*:[[:space:]]*/, "", x); sub(/,.*/, "", x); schema=x; schemas++ }
    /"version"[[:space:]]*:/ { version=$4; versions++ }
    /"built"[[:space:]]*:/ { x=$3; sub(/^[[:space:]]*:[[:space:]]*/, "", x); sub(/,.*/, "", x); built=x; builts++ }
    /"expires"[[:space:]]*:/ { x=$3; sub(/^[[:space:]]*:[[:space:]]*/, "", x); sub(/,.*/, "", x); expires=x; expiries++ }
    /"os"[[:space:]]*:/ { os=$4 }
    /"arch"[[:space:]]*:/ { arch=$4 }
    /"name"[[:space:]]*:/ { name=$4 }
    /"size"[[:space:]]*:/ { x=$3; sub(/^[[:space:]]*:[[:space:]]*/, "", x); sub(/,.*/, "", x); size=x }
    /"sha256"[[:space:]]*:/ { sha=$4; if (name == wanted) { entries++; found_os=os; found_arch=arch; found_size=size; found_sha=sha } }
    END { printf "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", schema, version, built, expires, entries, found_os, found_arch, found_size, found_sha }
  ' "$tmp_dir/panel-manifest.json"
)
[[ "$manifest_schema" == "1" && "$manifest_version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$ ]] || fail "panel-manifest.json has an invalid schema or version"
[[ "$manifest_built" =~ ^[0-9]+$ && "$manifest_expires" =~ ^[0-9]+$ ]] || fail "panel-manifest.json has invalid timestamps"
((manifest_expires > manifest_built)) || fail "panel-manifest.json expires before it was built"
if [[ -n "$version" && "$manifest_version" != "$version" ]]; then
  fail "panel-manifest.json is for $manifest_version, not the requested $version"
fi
now=$(date +%s)
((now <= manifest_expires)) || fail "panel-manifest.json expired at Unix time $manifest_expires"
[[ "$entry_count" == "1" && "$entry_os" == "linux" && "$entry_arch" == "$arch" ]] ||
  fail "panel-manifest.json does not list $binary_name for linux/$arch"
[[ "$entry_size" =~ ^[0-9]+$ && "$entry_sha" =~ ^[a-fA-F0-9]{64}$ ]] || fail "panel-manifest.json has invalid metadata for $binary_name"
binary_size=$(wc -c < "$tmp_dir/$binary_name" | tr -d '[:space:]')
[[ "$entry_size" == "$binary_size" ]] || fail "$binary_name size does not match panel-manifest.json"
[[ "${entry_sha,,}" == "${actual_sha,,}" ]] || fail "$binary_name SHA-256 does not match panel-manifest.json"

if [[ "$dry_run" == "1" ]]; then
  printf '4. Dry run: would install %s as %s/mistgate with mode 0755.\n' "$binary_name" "$install_bin_dir"
  printf '5. Dry run: would run mistgate setup, install the Mistgate systemd unit, enable it, and wait for ports 80 and 443.\n'
  printf '6. Dry run complete; the host was not changed and no setup link was created.\n'
  exit 0
fi

printf '4. Install the verified panel binary.\n'
if [[ "$test_mode" == "1" ]]; then
  mkdir -p -- "$install_bin_dir"
fi
install -D -m 0755 -- "$tmp_dir/$binary_name" "$install_bin_dir/mistgate"

printf '5. Run setup and install the service.\n'
setup_args=(setup --public-url "$public_url" --data-dir "$data_dir")
[[ -z "$admin_host" ]] || setup_args+=(--admin-host "$admin_host")
[[ -z "$admin_listen" ]] || setup_args+=(--admin-listen "$admin_listen")
if ! "$install_bin_dir/mistgate" "${setup_args[@]}" > "$tmp_dir/setup-output" 2>&1; then
  fail "mistgate setup failed. To start over, remove $install_bin_dir/mistgate and $data_dir, then run the installer again"
fi
admin_url=$(sed -n 's/^Admin URL:[[:space:]]*//p' "$tmp_dir/setup-output")
setup_link=$(sed -n 's/^Setup link:[[:space:]]*//p' "$tmp_dir/setup-output")
[[ -n "$admin_url" && -n "$setup_link" ]] || fail "mistgate setup did not return the admin URL and one-time setup link"

if [[ "$test_skip_systemd" == "1" ]]; then
  printf 'Systemd actions skipped by the test harness.\n'
else
  service_command="$install_bin_dir/mistgate serve --listen :443"
  [[ "$data_dir" == "$DEFAULT_DATA_DIR" ]] || service_command+=" --data-dir $data_dir"
  for domain in "${acme_domains[@]}"; do
    service_command+=" --acme-domain $domain"
  done
  cat > "$tmp_dir/mistgate.service" <<UNIT
[Unit]
Description=Mistgate panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$service_command
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

# The panel writes only its data directory and only needs to bind ports 443 and 80.
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
ProtectSystem=strict
ReadWritePaths=$data_dir
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectClock=yes
ProtectControlGroups=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectHostname=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
  install -D -m 0644 -- "$tmp_dir/mistgate.service" "$unit_file"
  systemctl daemon-reload
  systemctl enable --now mistgate
  printf '6. Wait for the panel to answer on ports 80 and 443.\n'
  healthy=0
  for ((attempt = 1; attempt <= 60; attempt++)); do
    # Port 80 answers (a redirect to HTTPS) without a certificate. The 443 listener only has to accept connections:
    # a certificate for a name that does not point here yet cannot be issued, and that is not a broken install.
    if curl --silent --connect-timeout 2 --max-time 4 -H "Host: $public_host" http://127.0.0.1/ -o /dev/null &&
      (exec 3<>/dev/tcp/127.0.0.1/443) 2>/dev/null; then
      healthy=1
      break
    fi
    sleep 2
  done
  if [[ "$healthy" != "1" ]]; then
    journalctl -u mistgate -n 50 --no-pager >&2 || true
    fail "the panel did not answer within 120 seconds; the service logs are above. Setup is done: fix the cause, then run: systemctl restart mistgate. For a fresh setup link run: $install_bin_dir/mistgate setup --data-dir $data_dir"
  fi
fi

printf '7. Mistgate is ready.\n'
printf 'Admin URL: %s\n' "$admin_url"
printf 'Setup link (valid for 30 minutes): %s\n' "$setup_link"
printf 'Next: open the setup link, create the owner account, then add a node from Nodes → Add node.\n'
