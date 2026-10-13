# Decoy site per node (design/vless.md 7.9) for scripts/e2e-wsl.sh. Sourced by it, never run alone: it uses the helpers and
# variables of the main script ($WORK, $BIN, $NODE_ID, $NODE_BIN, $TCP_MASQ, $HY2_PLAIN, api, say, pass, note, die, step,
# wait_for, stop_pid, start_node, start_panel, node_online, inbounds_active).
#
# The base steps leave one node with two Hysteria2 inbounds: HY2_PLAIN (no obfuscation, so its UDP port answers HTTP/3 to
# anyone) and the TCP site that every Hysteria2 inbound of the node shares (TCP_MASQ, 443 when it is free).

decoy_node() { api NodeService/GetNode "{\"nodeId\":\"$NODE_ID\"}"; }

# decoy_applied <mode>: the node reports what is configured, and (for own) the hash of the saved bundle, with no pending reason.
decoy_applied() {
  decoy_node | jq -e --arg m "$1" '.node as $n | $n.decoyMode == $m and ($n.decoyAgentMode // "") == $m
    and (if $m == "own" then ($n.decoyAgentSha256 // "") == ($n.decoyBundleSha256 // "") and ($n.decoyBundleSha256 // "") != ""
         elif $m == "panel" then ($n.decoyAgentSha256 // "") != "" else ($n.decoyAgentSha256 // "") == "" end)
    and ($n.decoyReasonCode // "") == ""' >/dev/null
}

# decoy_serves <text> [path]: the TCP site and the HTTP/3 site both answer with that text.
decoy_serves() {
  local text=$1 path=${2:-/}
  if [ -n "$TCP_MASQ" ]; then
    curl -sk -m 10 "https://127.0.0.1:$TCP_MASQ$path" 2>/dev/null | grep -q -- "$text" || return 1
  fi
  "$BIN/h3get" "https://127.0.0.1:$HY2_PLAIN$path" 2>/dev/null | grep -q -- "$text"
}

decoy_upload() { # <tar.gz file> -> the admin response (or its error on stdout)
  api NodeService/UploadNodeDecoy "$(jq -nc --arg id "$NODE_ID" --arg b "$(base64 -w0 "$1")" '{nodeId:$id, bundleArchive:$b}')" 2>&1
}

decoy_site() {
  step "7b. decoy site per node: an own bundle on the Hysteria2 sites, back to built-in, and a restart"
  local dir=$WORK/decoy-site mark
  mkdir -p "$dir/good" "$dir/bad"
  mark="e2e-own-site-$(openssl rand -hex 4)"
  printf '<!doctype html><title>%s</title><h1>%s</h1>\n' "$mark" "$mark" >"$dir/good/index.html"
  printf '<!doctype html><title>Gone</title><p>%s: nothing here</p>\n' "$mark" >"$dir/good/404.html"
  tar czf "$dir/good.tgz" -C "$dir/good" .
  printf '<!doctype html><title>x</title>\n' >"$dir/bad/index.html"
  ln -s /etc/hostname "$dir/bad/link.html"
  tar czf "$dir/bad.tgz" -C "$dir/bad" .

  decoy_node | jq -e '.node.decoySupported == true and .node.decoyMode == "builtin"' >/dev/null \
    || die "a node made while the panel has no decoy dir must be on the built-in site and support decoy/1: $(decoy_node | jq -c '.node | {decoyMode, decoySupported, decoyReasonCode}')"
  decoy_serves 'Coming soon' || die "the built-in site is not served before the change"

  # A bundle with a symbolic link is refused with a coded reason and changes nothing.
  local out
  out=$(decoy_upload "$dir/bad.tgz") && die "a bundle with a symbolic link was accepted"
  grep -q 'decoy_bundle_not_static' <<<"$out" || die "the refusal of a symbolic link has no coded reason: $out"
  decoy_applied builtin || die "a refused upload changed the node's decoy state"

  decoy_upload "$dir/good.tgz" >/dev/null || die "the own bundle was refused"
  wait_for 30 "the node to report the own site" decoy_applied own \
    || die "the node did not apply the own site: $(decoy_node | jq -c '.node | {decoyMode, decoyAgentMode, decoyAgentSha256, decoyBundleSha256, decoyReasonCode}')"
  wait_for 15 "the sites to serve the own page" decoy_serves "$mark" || die "the own page is not served on the TCP site and HTTP/3"
  decoy_serves 'nothing here' /missing.html || die "the own 404.html is not served for an unknown path"
  pass "own bundle (tar.gz, 404.html included) uploaded: the node reports it, the TCP site${TCP_MASQ:+ ($TCP_MASQ)} and HTTP/3 (UDP $HY2_PLAIN) serve it; a bundle with a symlink is refused with decoy_bundle_not_static"

  api NodeService/SetNodeDecoyMode "$(jq -nc --arg id "$NODE_ID" '{nodeId:$id, mode:"builtin"}')" >/dev/null || die "could not switch to the built-in site"
  wait_for 30 "the node to report the built-in site" decoy_applied builtin || die "the node did not return to the built-in site"
  wait_for 15 "the sites to serve the built-in page" decoy_serves 'Coming soon' || die "the built-in page is not served after switching back"
  ! decoy_serves "$mark" || die "the own page is still served after switching back"
  pass "switched back to built-in: both sites serve the built-in page again (no listener restarted: the inbounds stay ACTIVE)"
  inbounds_active 2 >/dev/null || die "an inbound is not ACTIVE after switching sites: $(decoy_node | jq -c '.inbounds[] | {port, state, lastError}')"

  api NodeService/SetNodeDecoyMode "$(jq -nc --arg id "$NODE_ID" '{nodeId:$id, mode:"own"}')" >/dev/null || die "could not select the saved own bundle again"
  wait_for 30 "the node to report the own site again" decoy_applied own || die "the node did not apply the saved own bundle"
  wait_for 15 "the sites to serve the own page again" decoy_serves "$mark" || die "the own page is not served after selecting it again"

  # A restart of the node with the panel down: the saved site is served before anything reconnects.
  stop_pid "$PANEL_PID"
  stop_pid "$NODE_PID"
  start_node
  NODE_PID=$LAST_PID
  wait_for 40 "the restarted node to serve the own page without the panel" decoy_serves "$mark" \
    || die "the restarted node does not serve its saved site while the panel is down"
  pass "node restarted while the panel was down: it served the saved own site from disk"
  start_panel
  wait_for 90 "the node to reconnect" node_online || die "the node did not reconnect after the restart"
  wait_for 60 "both inbounds ACTIVE" inbounds_active 2 || die "the inbounds did not come back after the restart"
  wait_for 30 "the node to report the own site after the restart" decoy_applied own || die "the node's reported site differs after the restart: $(decoy_node | jq -c '.node | {decoyMode, decoyAgentMode, decoyReasonCode}')"
  decoy_serves "$mark" || die "the own page is gone after the node reconnected to the panel"
  pass "after the reconnect the panel still sees the own site applied and it keeps being served"

  # Leave the node as the other steps expect it.
  api NodeService/SetNodeDecoyMode "$(jq -nc --arg id "$NODE_ID" '{nodeId:$id, mode:"builtin"}')" >/dev/null || die "could not restore the built-in site"
  wait_for 30 "the node to report the built-in site" decoy_applied builtin || die "the node did not restore the built-in site"
  wait_for 15 "the sites to serve the built-in page" decoy_serves 'Coming soon' || die "the built-in page is not back"

  # The panel's own site (--decoy-dir): copied to the node, dot-files left out, and gone again with a switch back.
  mkdir -p "$dir/panel/.git"
  echo "<!doctype html><title>$mark-panel</title>" >"$dir/panel/index.html"
  echo secret >"$dir/panel/.git/config"
  stop_pid "$PANEL_PID"
  PANEL_DECOY_DIR=$dir/panel
  start_panel
  wait_for 90 "the node to reconnect to the panel with a decoy dir" node_online || die "the node did not reconnect to the panel"
  api NodeService/SetNodeDecoyMode "$(jq -nc --arg id "$NODE_ID" '{nodeId:$id, mode:"panel"}')" >/dev/null || die "could not select the panel's site"
  wait_for 30 "the node to report the panel's site" decoy_applied panel || die "the node did not apply the panel's site: $(decoy_node | jq -c '.node | {decoyMode, decoyAgentMode, decoyReasonCode}')"
  wait_for 15 "the sites to serve the panel's page" decoy_serves "$mark-panel" || die "the panel's page is not served on the node"
  ! decoy_serves secret /.git/config || die "a dot-file of the panel's site is served by the node"
  pass "the panel's --decoy-dir is copied to the node and served on both sites; its dot-files are not"
  api NodeService/SetNodeDecoyMode "$(jq -nc --arg id "$NODE_ID" '{nodeId:$id, mode:"builtin"}')" >/dev/null || die "could not leave the panel's site"
  wait_for 30 "the node to report the built-in site" decoy_applied builtin || die "the node did not return to the built-in site"
  stop_pid "$PANEL_PID"
  PANEL_DECOY_DIR=""
  start_panel
  wait_for 90 "the node to reconnect to the panel" node_online || die "the node did not reconnect to the panel"
  wait_for 15 "the sites to serve the built-in page" decoy_serves 'Coming soon' || die "the built-in page is not back"
}
