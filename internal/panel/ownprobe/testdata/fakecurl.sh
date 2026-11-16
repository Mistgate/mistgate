#!/bin/sh
# A stand-in for curl in the probe script tests. It records every call (one argument per line in $FAKE_DIR/calls/N, the time in
# calls/N.t, the -K file in auth.N, the request body in body.N) and answers from files in $FAKE_DIR:
#   ans.K, ans.K.code, ans.K.hdr   the K-th sync (1-based) answer body, status and header lines; ans.default* when there is no K
#   enroll.resp, enroll.code       the answer to enroll (the token it received is in enroll.token)
#   probe.sh                       what GET .../probe.sh serves
#   t/IP_PORT                      "time_connect time_appconnect size_download time_total exitcode errormsg" for a target
# FAKE_CURL_VERSION is the version it reports (default 8.5.0); FAKE_DELAY makes a target check last that many seconds and
# writes "S n" / "E n" lines to $FAKE_DIR/events, to see how many run at once.
D=${FAKE_DIR:?FAKE_DIR is not set}
# The script runs two curls at once, so the number of a call is taken with mkdir (atomic: no two calls get one number); seq only
# tells where to start looking. A call is written whole and moved into place, so a reader never sees half of one.
mkdir -p "$D/calls/.lk"
n=0
[ ! -f "$D/seq" ] || read -r n <"$D/seq"
case $n in '' | *[!0-9]*) n=0 ;; esac
while n=$((n + 1)); ! mkdir "$D/calls/.lk/$n" 2>/dev/null; do :; done
echo "$n" >"$D/seq.$$" && mv "$D/seq.$$" "$D/seq"
clock=0
[ ! -f "$D/clock" ] || read -r clock <"$D/clock"
echo "$clock" >"$D/calls/$n.t"
: >"$D/calls/.w$n"
[ $# -eq 0 ] || printf '%s\n' "$@" >"$D/calls/.w$n"
mv "$D/calls/.w$n" "$D/calls/$n"
if [ "${1:-}" = --version ]; then
	echo "curl ${FAKE_CURL_VERSION:-8.5.0} (x86_64-pc-linux-gnu) libcurl/${FAKE_CURL_VERSION:-8.5.0} OpenSSL/3.0.13"
	exit 0
fi
fmt='' out='' hdr='' cfg='' data='' url='' resolve=''
while [ $# -gt 0 ]; do
	case $1 in
	-w) fmt=$2; shift ;;
	-o | -fsSo) out=$2; shift ;;
	-D) hdr=$2; shift ;;
	-K) cfg=$2; shift ;;
	--data-binary) data=$2; shift ;;
	--resolve) resolve=$2; shift ;;
	--connect-timeout | -m | --max-filesize | --limit-rate | --speed-limit | --speed-time | -H | -X | --proto | --noproxy) shift ;;
	https://*) url=$1 ;;
	esac
	shift
done
case $url in
*/sync)
	[ -z "$cfg" ] || cp "$cfg" "$D/auth.$n"
	case $data in
	@-) cat >"$D/body.$n" ;;
	@*) cp "${data#@}" "$D/body.$n" ;;
	esac
	k=0
	[ ! -f "$D/syncn" ] || read -r k <"$D/syncn"
	k=$((k + 1))
	echo "$k" >"$D/syncn"
	a="$D/ans.$k"
	[ -f "$a" ] || a="$D/ans.default"
	code=200
	[ ! -f "$a.code" ] || read -r code <"$a.code"
	[ -z "$hdr" ] || { printf 'HTTP/1.1 %s X\r\n' "$code"; [ ! -f "$a.hdr" ] || cat "$a.hdr"; printf '\r\n'; } >"$hdr"
	[ -z "$out" ] || { if [ -f "$a" ]; then cat "$a"; else :; fi; } >"$out"
	printf '%s' "$code"
	exit 0
	;;
*/enroll)
	cat >"$D/enroll.token"
	code=200
	[ ! -f "$D/enroll.code" ] || read -r code <"$D/enroll.code"
	[ -z "$out" ] || { if [ -f "$D/enroll.resp" ]; then cat "$D/enroll.resp"; else :; fi; } >"$out"
	printf '%s' "$code"
	exit 0
	;;
*/probe.sh)
	[ -z "$out" ] || cp "$D/probe.sh" "$out"
	exit 0
	;;
esac
# a target: the address is in the URL, or in --resolve host:port:address
hp=${url#https://}
hp=${hp%%/*}
port=${hp##*:}
ip=${hp%:*}
case $resolve in ?*) ip=${resolve##*:} ;; esac
line='0.040000 0.110000 70000 0.150000 0 '
[ ! -f "$D/t/${ip}_${port}" ] || read -r line <"$D/t/${ip}_${port}"
if [ -n "${FAKE_DELAY:-}" ]; then
	echo "S $n" >>"$D/events"
	/bin/sleep "$FAKE_DELAY"
	echo "E $n" >>"$D/events"
fi
set -f
# shellcheck disable=SC2086
set -- $line
tc=$1 ta=$2 sz=$3 tt=$4 ec=$5
shift 5 2>/dev/null || shift $#
msg=$*
case $fmt in
*exitcode*) printf '%s %s %s %s %s %s' "$tc" "$ta" "$sz" "$tt" "$ec" "$msg" ;;
*) printf '%s %s %s %s' "$tc" "$ta" "$sz" "$tt" ;;
esac
exit "${ec:-0}"
