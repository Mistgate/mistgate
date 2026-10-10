#!/bin/bash
# Removes what the benchmark created: its containers, the images it pulled or imported, its Docker networks and volumes,
# its systemd slices and units, and $BENCH_ROOT (binaries, data and all results under out/ - copy them first).
# Names are the ones the harness uses (bench*, the pinned image tags of compose/*.yml). Read it before you run it, and
# compare with `docker ps -a`, `docker images`, `docker volume ls` and `docker network ls` on a machine that has other containers.
BENCH_ROOT=${BENCH_ROOT:-/root/bench}
for id in $(docker ps -a --filter label=com.docker.compose.project --format '{{.ID}} {{.Label "com.docker.compose.project"}}' | awk '$2 ~ /^bench/ {print $1}'); do docker rm -f "$id" >/dev/null 2>&1; done
for n in remnawave remnawave-db remnawave-redis remnanode; do docker rm -f "$n" >/dev/null 2>&1; done
for n in $(docker ps -a --format '{{.Names}}' | grep -E '^bench_'); do docker rm -f "$n" >/dev/null 2>&1; done
for u in $(systemctl list-units --all --plain --no-legend 'bench-*.service' | awk '{print $1}'); do systemctl stop "$u" 2>/dev/null; systemctl reset-failed "$u" 2>/dev/null; done
for i in bench/mgnode-prof:0.1.32 bench/mgnode:0.1.32 remnawave/backend:3.4.5 remnawave/node:3.4.2 ghcr.io/mhsanaei/3x-ui:v3.9.0 valkey/valkey:9-alpine pasarguard/panel:v5.4.1 pasarguard/node:v0.5.4 postgres:18.4; do docker rmi "$i" 2>&1 | tail -1; done
docker network rm bench-box bench-iso 2>&1
for v in remnawave-db-data valkey-socket; do docker volume rm "$v" 2>&1 | tail -1; done
systemctl stop bench-box.slice bench-panel.slice bench-client.slice 2>/dev/null
rm -f /etc/systemd/system/bench-panel.slice /etc/systemd/system/bench-box.slice
rm -rf /run/systemd/system.control/bench-*.d
systemctl daemon-reload
rm -rf "$BENCH_ROOT"
rm -f /tmp/auths.txt /tmp/hy.log /tmp/hy.yaml /tmp/ipbatch.txt /tmp/suburls.txt /tmp/pg_user1.json /tmp/rw_key.txt /tmp/rw_user1.json /tmp/mgnode.tar; rm -rf /tmp/mgn
