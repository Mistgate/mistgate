#!/bin/bash
# Prepares $BENCH_ROOT for the harness (run as root, in WSL2 or on a Linux host with systemd, Docker and cgroup v2).
# usage: setup.sh MISTGATE_BIN MISTGATE_NODE_BIN REMNAWAVE_ENV_SAMPLE PASARGUARD_ENV_EXAMPLE
#   MISTGATE_BIN, MISTGATE_NODE_BIN   the two binaries of the release under test (see README.md)
#   REMNAWAVE_ENV_SAMPLE              .env.sample from the Remnawave release named in compose/remna.yml
#   PASARGUARD_ENV_EXAMPLE            env.example from the PasarGuard release named in compose/pasar.yml
# It creates the 1 vCPU / 1 GB slice, copies the harness and compose files, builds loadgen and hyload (Go required) and writes the two
# .env files with random secrets. The one-server slice (bench-box.slice) is written by onebox.py itself.
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
BENCH_ROOT=${BENCH_ROOT:-/root/bench}
mkdir -p "$BENCH_ROOT"/{bin,data,out,3xui,remna,pasar}

cat > /etc/systemd/system/bench-panel.slice <<'U'
[Unit]
Description=bench panel host 1 vCPU 1 GB
[Slice]
CPUQuota=100%
MemoryMax=1G
MemoryHigh=infinity
MemorySwapMax=0
TasksMax=infinity
U
systemctl daemon-reload
systemctl start bench-panel.slice

cp "$HERE"/py/*.py "$BENCH_ROOT"/
cp "$1" "$BENCH_ROOT/bin/mistgate"; cp "$2" "$BENCH_ROOT/bin/mistgate-node"
(cd "$HERE/loadgen" && go build -o "$BENCH_ROOT/bin/loadgen" .)
(cd "$HERE/hyload" && go build -o "$BENCH_ROOT/bin/hyload" .)
chmod +x "$BENCH_ROOT"/bin/*

cp "$HERE/compose/3xui.yml" "$BENCH_ROOT/3xui/compose.yml";  cp "$HERE/compose/3xui_box.yml" "$BENCH_ROOT/3xui/box.yml"
cp "$HERE/compose/remna.yml" "$BENCH_ROOT/remna/compose.yml"; cp "$HERE/compose/remna_box.yml" "$BENCH_ROOT/remna/box.yml"
cp "$HERE/compose/pasar.yml" "$BENCH_ROOT/pasar/compose.yml"; cp "$HERE/compose/pasar_box.yml" "$BENCH_ROOT/pasar/box.yml"

cd "$BENCH_ROOT/remna"
cp "$3" .env
sed -i "s/^APP_SECRET=.*/APP_SECRET=$(openssl rand -hex 64)/" .env
sed -i "s/^METRICS_PASS=.*/METRICS_PASS=$(openssl rand -hex 12)/" .env
sed -i "s/^WEBHOOK_SECRET_HEADER=.*/WEBHOOK_SECRET_HEADER=$(openssl rand -hex 32)/" .env
pw=$(openssl rand -hex 24)
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$pw/" .env
sed -i "s|^\(DATABASE_URL=\"postgresql://postgres:\)[^@]*\(@.*\)|\1$pw\2|" .env
sed -i 's|^PANEL_DOMAIN=.*|PANEL_DOMAIN=panel.bench.test|; s|^SUB_PUBLIC_DOMAIN=.*|SUB_PUBLIC_DOMAIN=panel.bench.test/api/sub|' .env
chmod 600 .env

cd "$BENCH_ROOT/pasar"
cp "$4" .env
sed -i 's|^UVICORN_HOST = .*|UVICORN_HOST = "127.0.0.1"|' .env
sed -i 's|^# \(SQLALCHEMY_DATABASE_URL = "sqlite+aiosqlite:///db.sqlite3"\)|\1|' .env
sed -i 's|^SQLALCHEMY_DATABASE_URL = .*|SQLALCHEMY_DATABASE_URL = "sqlite+aiosqlite:////var/lib/pasarguard/db.sqlite3"|' .env
chmod 600 .env
echo "ready: cd $BENCH_ROOT && python3 bench.py --help"
