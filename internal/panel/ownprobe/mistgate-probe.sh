#!/bin/sh
# mistgate-probe: availability probe of a Mistgate panel; POSIX sh and curl (busybox ash, dash).
#   sh mgp.sh install <base-url> <token> | upgrade       (the downloaded file; the panel prints the command)
#   mistgate-probe uninstall | run | supervise | status | start | stop | restart
# The panel can only make it run curl against targets of an answer whose every line matched a strict pattern. No
# self-update, no eval, no sourcing; the limits below are fixed here. MGP_ROOT (tests only) puts every path under it.
# shellcheck disable=SC2249,SC2312
set -u
umask 077
set -f
LC_ALL=C
export LC_ALL
unset ENV BASH_ENV CDPATH

HAVE_LOGGER=''
if command -v logger >/dev/null 2>&1; then HAVE_LOGGER=1; fi
log() { [ -z "${HAVE_LOGGER}" ] || logger -t mistgate-probe "$1" >/dev/null 2>&1; return 0; }

MAX_TARGETS=32 MIN_CYCLE=1800 PAR=2 BATCH_SECS=6 MAX_DAY=600 MAX_IPS=64
NEXT_MIN=600 NEXT_MAX=3600 NEXT_DEF=900 MUTE_SECS=21600 SYNC_BYTES=32768

die() { printf 'mistgate-probe: %s\n' "$*" >&2; log "$*"; exit 1; }

ROOT=${MGP_ROOT:-}
case ${ROOT} in
	"") PATH=/opt/bin:/opt/sbin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin ;;
	/*) case ${ROOT} in *[!A-Za-z0-9._/-]*|*..*) die 'bad MGP_ROOT' ;; esac
			PATH=${ROOT}/bin:/opt/bin:/opt/sbin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin ;;
	*) die 'bad MGP_ROOT' ;;
esac
export PATH
MODE=''
[ -z "${ROOT}" ] || MODE=${MGP_MODE:-entware}
if [ -z "${MODE}" ]; then
	if [ -d /run/systemd/system ]; then MODE=systemd; elif [ -d /opt/etc/init.d ]; then MODE=entware; else MODE=none; fi
fi
case ${MODE} in
	entware) BIN=${ROOT}/opt/sbin/mistgate-probe CDIR=${ROOT}/opt/etc/mistgate-probe INIT=${ROOT}/opt/etc/init.d/S99mistgate-probe
					 S=${ROOT}/tmp/mistgate-probe ;;
	systemd) BIN=${ROOT}/usr/local/sbin/mistgate-probe CDIR=${ROOT}/etc/mistgate-probe INIT=${ROOT}/etc/systemd/system/mistgate-probe.service
					 S=${ROOT}/run/mistgate-probe ;;
	*) BIN=/nonexistent/mistgate-probe CDIR=/nonexistent/mistgate-probe INIT=/nonexistent/init S=/nonexistent/state ;;
esac
CONF=${CDIR}/conf
if [ "${MODE}" = systemd ] && [ -n "${CREDENTIALS_DIRECTORY:-}" ]; then
	case ${CREDENTIALS_DIRECTORY} in *[!A-Za-z0-9._/-]*) ;; /*) [ ! -f "${CREDENTIALS_DIRECTORY}/conf" ] || CONF=${CREDENTIALS_DIRECTORY}/conf ;; esac
fi
SELF=$0

# ---------------------------------------------------------------- strict patterns

uint() { # digits, no leading zero, at most $2 of them
	case $1 in ""|*[!0-9]*|0?*) return 1 ;; esac
	[ ${#1} -le "$2" ]
}
ip_ok() { # a public unicast IPv4 in canonical form
	case $1 in ""|*[!0-9.]*|.*|*.|*..*) return 1 ;; esac
	ip_ifs=${IFS}; IFS=.
	# shellcheck disable=SC2086
	set -- $1; IFS=${ip_ifs}
	[ $# -eq 4 ] || return 1
	for ip_o in "$1" "$2" "$3" "$4"; do
		case ${ip_o} in 0|[1-9]|[1-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5]) ;; *) return 1 ;; esac
	done
	case $1 in 0|10|127) return 1 ;; esac
	[ "$1" -lt 224 ] || return 1
	case $1.$2 in 169.254|192.168|198.18|198.19) return 1 ;; esac
	case $1.$2.$3 in 192.0.0) return 1 ;; esac
	if [ "$1" = 172 ] && [ "$2" -ge 16 ] && [ "$2" -le 31 ]; then return 1; fi
	if [ "$1" = 100 ] && [ "$2" -ge 64 ] && [ "$2" -le 127 ]; then return 1; fi
	return 0
}
port_ok() { uint "$1" 5 && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }
sni_ok() { # a host name, not an address
	case $1 in ""|*[!A-Za-z0-9.-]*|.*|*.|*..*) return 1 ;; esac
	[ ${#1} -le 253 ] || return 1
	case $1 in *[!0-9.]*) ;; *) return 1 ;; esac
	sn_ifs=${IFS}; IFS=.
	# shellcheck disable=SC2086
	set -- $1; IFS=${sn_ifs}
	for sn_l in "$@"; do
		[ ${#sn_l} -le 63 ] || return 1
		case ${sn_l} in -*|*-) return 1 ;; esac
	done
	return 0
}
path_ok() {
	case $1 in /*) ;; *) return 1 ;; esac
	case $1 in *[!A-Za-z0-9._~/-]*) return 1 ;; esac
	[ ${#1} -le 129 ]
}
base_ok() { # https://host[:port]/<24 characters of a-z2-7>, nothing else
	case $1 in https://?*/?*) ;; *) return 1 ;; esac
	[ ${#1} -le 200 ] || return 1
	bo_b=${1#https://}; bo_h=${bo_b%%/*}; bo_p=${bo_b#*/}
	case ${bo_h} in ""|*[!A-Za-z0-9.:-]*|-*) return 1 ;; esac
	case ${bo_p} in *[!a-z2-7]*) return 1 ;; esac
	[ ${#bo_p} -eq 24 ]
}
token_ok() { case $1 in mgp_?*) ;; *) return 1 ;; esac; case ${1#mgp_} in *[!a-z2-7]*) return 1 ;; esac; [ ${#1} -eq 56 ]; }
id_ok() { case $1 in prb_?*) ;; *) return 1 ;; esac; case ${1#prb_} in *[!0-9a-f]*) return 1 ;; esac; [ ${#1} -eq 28 ]; }
secret_ok() { case $1 in mgps_?*) ;; *) return 1 ;; esac; case ${1#mgps_} in *[!A-Za-z0-9_-]*) return 1 ;; esac; [ ${#1} -eq 48 ]; }
hex_ok() { case $2 in *[!0-9a-f]*) return 1 ;; esac; [ ${#2} -eq "$1" ]; }
fields_ok() { # the line is $1: single spaces, no edge spaces
	fo_line=$1; shift
	[ "$*" = "${fo_line}" ]
}
ms_of() { # curl's seconds ("0.041123") to whole milliseconds in MS
	MS=0
	case $1 in [0-9]*.[0-9]*) ;; *) return 0 ;; esac
	case $1 in *[!0-9.]*) return 0 ;; esac
	ms_i=${1%%.*}; ms_f=${1#*.}000
	[ ${#ms_i} -le 3 ] || { MS=120000; return 0; }
	ms_f=${ms_f%"${ms_f#???}"}
	MS=$((${ms_i:-0} * 1000 + 1${ms_f} - 1000))
	[ "${MS}" -le 120000 ] || MS=120000
	return 0
}

# ---------------------------------------------------------------- small helpers

now() { date +%s; }
rnd() { # a number below $1 in RND, from the kernel's uuid (busybox has no $RANDOM and no fractional sleep)
	RND=0
	{ read -r rn_u </proc/sys/kernel/random/uuid; } 2>/dev/null || return 0
	rn_h=${rn_u%"${rn_u#????}"}
	case ${rn_h} in ""|*[!0-9a-f]*) return 0 ;; esac
	RND=$((0x${rn_h} % $1))
}
NAPPID=''
nap() { # a sleep a signal can interrupt
	sleep "$1" &
	NAPPID=$!
	wait "${NAPPID}"
	NAPPID=''
}
getn() { # file, default
	gn_v=$2
	[ ! -f "$1" ] || read -r gn_v <"$1" || gn_v=$2
	case ${gn_v} in ""|*[!0-9]*) gn_v=$2 ;; esac
	GN=${gn_v}
}
setn() { printf '%s\n' "$2" >"$1"; }
mem_low() { # MemAvailable under 16 MB
	[ -r "${ROOT}/proc/meminfo" ] || return 1
	while read -r ml_k ml_v _; do
		if [ "${ml_k}" = 'MemAvailable:' ]; then [ "${ml_v}" -lt 16384 ]; return; fi
	done <"${ROOT}/proc/meminfo"
	return 1
}
pid_is() { # pid file $1 names a live process whose command line is "... mistgate-probe $2 ..."
	[ -f "$1" ] || return 1
	read -r pi_p <"$1" || return 1
	case ${pi_p} in ""|*[!0-9]*) return 1 ;; esac
	[ -r "/proc/${pi_p}/cmdline" ] || return 1
	pi_c=$(tr '\000' ' ' <"/proc/${pi_p}/cmdline")
	case ${pi_c} in *"mistgate-probe $2 "*) PIDOF=${pi_p}; return 0 ;; esac
	return 1
}
stop_pid() { # SIGTERM to the process of pid file $1 when it is ours (kind $2); never to anything else
	if pid_is "$1" "$2"; then kill -TERM "${PIDOF}" 2>/dev/null; fi
	rm -f "$1"
}
version_of() { # first 12 characters of the sha256 of file $1
	VER=000000000000
	command -v sha256sum >/dev/null 2>&1 || return 0
	vo_h=$(sha256sum <"$1" 2>/dev/null) || return 0
	vo_h=${vo_h%%[!0-9a-f]*}
	[ ${#vo_h} -eq 64 ] && VER=${vo_h%"${vo_h#????????????}"}
	return 0
}
curl_mode() { # CMODE=rich when curl has %{exitcode} and %{errormsg} (7.75), coarse before
	CMODE=coarse
	command -v curl >/dev/null 2>&1 || return 1
	cm_v=$(curl --version 2>/dev/null) || return 1
	cm_v=${cm_v#curl }; cm_v=${cm_v%%[ ]*}
	cm_maj=${cm_v%%.*}; cm_r=${cm_v#*.}; cm_min=${cm_r%%[!0-9]*}
	case ${cm_maj} in ""|*[!0-9]*) return 0 ;; esac
	case ${cm_min} in ""|*[!0-9]*) return 0 ;; esac
	if [ "${cm_maj}" -gt 7 ] || { [ "${cm_maj}" -eq 7 ] && [ "${cm_min}" -ge 75 ]; }; then CMODE=rich; fi
	return 0
}

# ---------------------------------------------------------------- the configuration: read as text, never run

load_conf() {
	U='' FB='' ID='' SEC='' REF=on
	[ -f "${CONF}" ] && [ ! -L "${CONF}" ] || return 1
	lc_n=$(wc -c <"${CONF}") || return 1
	[ "${lc_n}" -le 1024 ] || return 1
	while IFS='=' read -r lc_k lc_v || [ -n "${lc_k}" ]; do
		case ${lc_k} in
			""|'#'*) ;;
			url) base_ok "${lc_v}" || return 1; U=${lc_v} ;;
			fallback) [ -z "${lc_v}" ] || base_ok "${lc_v}" || return 1; FB=${lc_v} ;;
			id) id_ok "${lc_v}" || return 1; ID=${lc_v} ;;
			secret) secret_ok "${lc_v}" || return 1; SEC=${lc_v} ;;
			ref) case ${lc_v} in on|off) REF=${lc_v} ;; *) return 1 ;; esac ;;
			*) return 1 ;;
		esac
	done <"${CONF}"
	[ -n "${U}" ] && [ -n "${ID}" ] && [ -n "${SEC}" ]
}

# ---------------------------------------------------------------- the answer of the panel

# parse_answer FILE: on success writes ${S}/job.new (the validated lines) and sets A_NEXT, A_STATE, A_JOB, A_COUNT.
# Any line that is not exactly what the protocol allows returns 1, and nothing of the answer is used.
parse_answer() {
	pa_f=$1
	pa_n=$(wc -c <"${pa_f}") || return 1
	[ "${pa_n}" -gt 0 ] && [ "${pa_n}" -le 8192 ] || return 1
	pa_c=$(tr -d '\000\r\t' <"${pa_f}" | wc -c) || return 1
	[ "${pa_c}" = "${pa_n}" ] || return 1
	: >"${S}/job.new"
	pa_stage=1 A_NEXT='' A_STATE='' A_JOB='' A_COUNT=0 pa_end=''
	while IFS= read -r pa_line; do
		[ -z "${pa_end}" ] || return 1
		# shellcheck disable=SC2086
		set -- ${pa_line}
		fields_ok "${pa_line}" "$@" || return 1
		case ${pa_stage} in
			1) [ "${pa_line}" = 'mgp 1' ] || return 1 ;;
			2) [ $# -eq 2 ] && [ "$1" = next ] && uint "$2" 4 || return 1
				 A_NEXT=$2
				 [ "${A_NEXT}" -ge "${NEXT_MIN}" ] || A_NEXT=${NEXT_MIN}
				 [ "${A_NEXT}" -le "${NEXT_MAX}" ] || A_NEXT=${NEXT_MAX} ;;
			3) [ $# -eq 2 ] && [ "$1" = state ] || return 1
				 case $2 in active|paused) A_STATE=$2 ;; *) return 1 ;; esac ;;
			4) [ $# -eq 2 ] && [ "$1" = job ] && hex_ok 16 "$2" || return 1
				 A_JOB=$2
				 printf 'job %s\n' "$2" >>"${S}/job.new" ;;
			5) [ $# -eq 4 ] && [ "$1" = ctl ] && ip_ok "$2" && port_ok "$3" && sni_ok "$4" || return 1
				 printf 'ctl %s %s %s\n' "$2" "$3" "$4" >>"${S}/job.new" ;;
			6) [ $# -eq 3 ] && [ "$1" = ref ] && ip_ok "$2" && port_ok "$3" || return 1
				 printf 'ref %s %s\n' "$2" "$3" >>"${S}/job.new" ;;
			*) if [ "${pa_line}" = end ]; then pa_end=1; continue; fi
				 [ $# -eq 7 ] && [ "$1" = t ] && uint "$2" 2 && [ "$2" -eq $((A_COUNT + 1)) ] && [ "$2" -le "${MAX_TARGETS}" ] || return 1
				 ip_ok "$3" && port_ok "$4" || return 1
				 case $5 in
					 tcp) [ "$6" = - ] && [ "$7" = - ] || return 1 ;;
					 tls|fetch) sni_ok "$6" && { [ "$7" = - ] || path_ok "$7"; } || return 1 ;;
					 *) return 1 ;;
				 esac
				 A_COUNT=$2
				 printf 't %s %s %s %s %s %s\n' "$2" "$3" "$4" "$5" "$6" "$7" >>"${S}/job.new" ;;
		esac
		[ "${pa_stage}" -ge 7 ] || pa_stage=$((pa_stage + 1))
	done <"${pa_f}"
	[ -n "${pa_end}" ] && [ "${pa_stage}" -ge 7 ] || return 1
	if [ "${A_STATE}" = paused ] || [ "${A_JOB}" = 0000000000000000 ]; then
		[ "${A_COUNT}" -eq 0 ] && [ "${A_JOB}" = 0000000000000000 ] || return 1
	else
		[ "${A_COUNT}" -ge 1 ] || return 1
	fi
	return 0
}

# ---------------------------------------------------------------- measuring

budget_load() { # connections and distinct addresses of the last 24 hours, from ${S}/budget ("epoch ip" lines)
	bl_now=$(now); bl_cut=$((bl_now - 86400))
	NCONN=0 NIP=0 SEEN=' '
	: >"${S}/budget.new"
	if [ -f "${S}/budget" ]; then
		while read -r bl_t bl_ip; do
			case ${bl_t} in ""|*[!0-9]*) continue ;; esac
			[ "${bl_t}" -gt "${bl_cut}" ] || continue
			ip_ok "${bl_ip}" || continue
			printf '%s %s\n' "${bl_t}" "${bl_ip}" >>"${S}/budget.new"
			NCONN=$((NCONN + 1))
			case ${SEEN} in *" ${bl_ip} "*) ;; *) SEEN="${SEEN}${bl_ip} "; NIP=$((NIP + 1)) ;; esac
		done <"${S}/budget"
	fi
	mv "${S}/budget.new" "${S}/budget"
}
budget_take() { # one connection to $1, when the day's budget allows it
	[ "${NCONN}" -lt "${MAX_DAY}" ] || return 1
	case ${SEEN} in
		*" $1 "*) ;;
		*) [ "${NIP}" -lt "${MAX_IPS}" ] || return 1; SEEN="${SEEN}$1 "; NIP=$((NIP + 1)) ;;
	esac
	NCONN=$((NCONN + 1))
	printf '%s %s\n' "$(now)" "$1" >>"${S}/budget"
}

net_curl() { curl -q -s --proto '=https' --noproxy '*' -4 --http1.1 "$@"; }
# curl_check OUT URL-ARGS...: one measurement. OUT.c gets curl's -w line, OUT.x its exit status.
curl_check() {
	cc_out=$1; shift
	if [ "${CMODE}" = rich ]; then cc_w='%{time_connect} %{time_appconnect} %{size_download} %{time_total} %{exitcode} %{errormsg}'
	else cc_w='%{time_connect} %{time_appconnect} %{size_download} %{time_total}'; fi
	net_curl -k -o /dev/null -w "${cc_w}" "$@" >"${cc_out}.c" 2>/dev/null
	printf '%s\n' "$?" >"${cc_out}.x"
	cc_tc=0 cc_ta=0 cc_sz=0 cc_tt=0 cc_ec='' cc_msg=''
	read -r cc_tc cc_ta cc_sz cc_tt cc_ec cc_msg <"${cc_out}.c" || :
	read -r cc_rc <"${cc_out}.x" || cc_rc=2
	case ${cc_ec} in ""|*[!0-9]*) cc_ec=${cc_rc} ;; esac
	case ${cc_sz} in ""|*[!0-9]*) cc_sz=0 ;; esac
	rm -f "${cc_out}.c" "${cc_out}.x"
	CONNECTED=1
	case ${cc_tc} in ""|0|0.0|0.000000) CONNECTED='' ;; esac
	ms_of "${cc_tc}"; MS_C=${MS}
	ms_of "${cc_ta}"; MS_A=${MS}
	ms_of "${cc_tt}"; MS_T=${MS}
	TLSDONE=1
	case ${cc_ta} in ""|0|0.0|0.000000) TLSDONE='' ;; esac
}

# check_ctl/check_ref write one word to $1.
check_ctl() { # out ip port sni
	curl_check "$1" --connect-timeout 5 -m 8 --resolve "$4:$3:$2" "https://$4:$3/"
	if [ -n "${TLSDONE}" ]; then printf 'ok %s\n' "${MS_A}" >"$1"; else printf 'fail %s\n' "${MS_T}" >"$1"; fi
}
check_ref() { # out ip port
	curl_check "$1" --connect-timeout 5 -m 5 "https://$2:$3/"
	if [ -n "${CONNECTED}" ]; then printf 'open\n' >"$1"
	elif [ "${cc_ec}" = 28 ]; then printf 'blocked\n' >"$1"
	elif [ "${cc_ec}" = 7 ]; then case ${cc_msg} in *nreachable*|*"No route"*) printf 'fail\n' >"$1" ;; *) printf 'blocked\n' >"$1" ;; esac
	else printf 'fail\n' >"$1"; fi
}
# check_target out kind ip port sni path: the result line is "tcp ms tls ms fetch bytes" in $1
check_target() {
	ct_out=$1 ct_kind=$2 ct_ip=$3 ct_port=$4 ct_sni=$5 ct_path=$6
	ip_ok "${ct_ip}" && port_ok "${ct_port}" || return 1
	[ "${ct_path}" != - ] || ct_path=/
	case ${ct_kind} in
		tcp) curl_check "${ct_out}.k" --connect-timeout 5 -m 6 "https://${ct_ip}:${ct_port}/" ;;
		tls|fetch) sni_ok "${ct_sni}" && path_ok "${ct_path}" || return 1
			 if [ "${ct_kind}" = fetch ] && [ "${CMODE}" = rich ]; then
				 curl_check "${ct_out}.k" --connect-timeout 5 -m 20 --max-filesize 65536 --limit-rate 200k --speed-limit 1 --speed-time 6 \
					 --resolve "${ct_sni}:${ct_port}:${ct_ip}" "https://${ct_sni}:${ct_port}${ct_path}"
			 else
				 # without the 7.75 curl there is no reliable size or exit status: a fetch is a plain tls check, no freeze
				 ct_kind=tls
				 curl_check "${ct_out}.k" --connect-timeout 5 -m 8 --resolve "${ct_sni}:${ct_port}:${ct_ip}" "https://${ct_sni}:${ct_port}${ct_path}"
			 fi ;;
		*) return 1 ;;
	esac
	ct_tcp=ok ct_tms=${MS_C} ct_tls=- ct_lms=0 ct_fe=- ct_by=0
	if [ -z "${CONNECTED}" ]; then
		ct_tms=${MS_T}
		case ${cc_ec} in
			28) ct_tcp=timeout ;;
			7) case ${cc_msg} in *nreachable*|*"No route"*) ct_tcp=unreach ;; *) ct_tcp=refused ;; esac ;;
			*) ct_tcp=unreach ;;
		esac
	elif [ "${ct_kind}" != tcp ]; then
		if [ -n "${TLSDONE}" ]; then
			ct_tls=ok ct_lms=${MS_A}
		else
			ct_lms=${MS_T}
			case ${cc_ec} in
				28) ct_tls=timeout ;;
				*) case ${cc_msg} in
						 *lert*) ct_tls=alert ;;
						 *eset*|*EOF*|*eof*|*SYSCALL*|*"Broken pipe"*) ct_tls=reset ;;
						 *) if [ -z "${cc_msg}" ] && [ "${cc_ec}" = 56 ]; then ct_tls=reset; else ct_tls=fail; fi ;;
					 esac ;;
			esac
		fi
	fi
	if [ "${ct_tls}" = ok ] && [ "${ct_kind}" = fetch ]; then
		ct_by=${cc_sz}
		[ "${ct_by}" -le 65536 ] || ct_by=65536
		if [ "${cc_ec}" = 63 ] || [ "${ct_by}" -ge 65536 ]; then ct_fe=full ct_by=65536
		elif [ "${cc_ec}" = 0 ]; then
			if [ "${ct_by}" -ge 32768 ]; then ct_fe=full; else ct_fe=short; fi
		else ct_fe=freeze; fi
	fi
	printf '%s %s %s %s %s %s\n' "${ct_tcp}" "${ct_tms}" "${ct_tls}" "${ct_lms}" "${ct_fe}" "${ct_by}" >"${ct_out}"
}

pace() { # a batch of PAR checks lasts at least BATCH_SECS: at most 20 connections a minute
	pc_el=$(($(now) - BSTART))
	[ "${pc_el}" -ge "${BATCH_SECS}" ] || nap $((BATCH_SECS - pc_el))
	BSTART=$(now)
}

# run_job: executes ${S}/job, leaves the report in ${S}/report
run_job() {
	rj_jid=0000000000000000 rj_cip='' rj_cport='' rj_csni='' rj_rip='' rj_rport='' rj_ns=' ' rj_started='' rj_inflight=0
	budget_load
	BSTART=$(now)
	rm -rf "${S}/res"; mkdir "${S}/res"
	while read -r rj_tag rj_a rj_b rj_c rj_d rj_e rj_f; do
		case ${rj_tag} in
			job) rj_jid=${rj_a} ;;
			ctl) rj_cip=${rj_a} rj_cport=${rj_b} rj_csni=${rj_c} ;;
			ref) rj_rip=${rj_a} rj_rport=${rj_b} ;;
			t)
				if [ -z "${rj_started}" ]; then
					rj_started=1
					if budget_take "${rj_cip}"; then ( check_ctl "${S}/res/ctl" "${rj_cip}" "${rj_cport}" "${rj_csni}" ) & fi
					if [ "${REF}" = on ] && budget_take "${rj_rip}"; then ( check_ref "${S}/res/ref" "${rj_rip}" "${rj_rport}" ) & fi
					wait; pace
				fi
				budget_take "${rj_b}" || continue
				( check_target "${S}/res/${rj_a}" "${rj_d}" "${rj_b}" "${rj_c}" "${rj_e}" "${rj_f}" ) &
				rj_ns="${rj_ns}${rj_a} "
				rj_inflight=$((rj_inflight + 1))
				if [ "${rj_inflight}" -ge "${PAR}" ]; then wait; rj_inflight=0; pace; fi ;;
		esac
	done <"${S}/job"
	wait
	rj_cs=fail rj_cm=0 rj_rs=fail
	[ ! -f "${S}/res/ctl" ] || read -r rj_cs rj_cm <"${S}/res/ctl" || :
	[ ! -f "${S}/res/ref" ] || read -r rj_rs <"${S}/res/ref" || :
	case ${rj_cs} in ok|fail) ;; *) rj_cs=fail ;; esac
	case ${rj_rs} in blocked|open|fail) ;; *) rj_rs=fail ;; esac
	case ${rj_cm} in ""|*[!0-9]*) rj_cm=0 ;; esac
	{
		printf 'mgp 1\nv %s\njob %s\nctl %s %s\nref %s\n' "${VER}" "${rj_jid}" "${rj_cs}" "${rj_cm}" "${rj_rs}"
		for rj_n in ${rj_ns}; do
			[ -f "${S}/res/${rj_n}" ] || continue
			read -r rj_tcp rj_tms rj_tls rj_lms rj_fe rj_by <"${S}/res/${rj_n}" || continue
			printf 'r %s %s %s %s %s %s %s\n' "${rj_n}" "${rj_tcp}" "${rj_tms}" "${rj_tls}" "${rj_lms}" "${rj_fe}" "${rj_by}"
		done
		printf 'end\n'
	} >"${S}/report.new"
	rm -rf "${S}/res"
	rj_sz=$(wc -c <"${S}/report.new") || rj_sz=99999
	if [ "${rj_sz}" -le "${SYNC_BYTES}" ]; then mv "${S}/report.new" "${S}/report"; else rm -f "${S}/report.new"; fi
	setn "${S}/last_job" "${rj_jid}"
	now >"${S}/last_exec"
}

# ---------------------------------------------------------------- talking to the panel

# sync_once: sends ${S}/report (or an empty report); sets HTTP, answer in ${S}/resp
sync_once() {
	so_base=${U}
	getn "${S}/via" 0
	if [ -n "${FB}" ] && [ "${GN}" -gt "$(now)" ]; then so_base=${FB}; fi
	if [ -s "${S}/report" ]; then cp "${S}/report" "${S}/out"
	else printf 'mgp 1\nv %s\njob 0000000000000000\nctl fail 0\nref fail\nend\n' "${VER}" >"${S}/out"; fi
	printf 'header = "Authorization: Bearer %s"\n' "${SEC}" >"${S}/auth.cfg"
	rm -f "${S}/resp" "${S}/hdr"
	# the secret goes through -K from a 0600 file, never through the command line
	HTTP=$(net_curl --connect-timeout 10 -m 30 --max-filesize "${SYNC_BYTES}" --limit-rate 100k -K "${S}/auth.cfg" -X POST \
		-H 'Content-Type: text/plain' --data-binary "@${S}/out" -D "${S}/hdr" -o "${S}/resp" -w '%{http_code}' "${so_base}/sync" 2>/dev/null)
	rm -f "${S}/auth.cfg" "${S}/out"
	case ${HTTP} in [1-5][0-9][0-9]) ;; *) HTTP=000 ;; esac
	SO_RA=0
	if [ -f "${S}/hdr" ]; then
		tr '[:upper:]' '[:lower:]' <"${S}/hdr" | tr -d '\r' >"${S}/hdr.l"
		while read -r so_k so_v; do
			if [ "${so_k}" = 'retry-after:' ] && uint "${so_v}" 5; then SO_RA=${so_v}; fi
		done <"${S}/hdr.l"
		rm -f "${S}/hdr" "${S}/hdr.l"
	fi
}
failed() { # a sync that did not work: back off 60 s, 120 s ... 3600 s; the fallback address for an hour after two
	getn "${S}/fails" 0
	fl_n=$((GN + 1)); [ "${fl_n}" -le 20 ] || fl_n=20
	setn "${S}/fails" "${fl_n}"
	WAIT=60 fl_i=1
	while [ "${fl_i}" -lt "${fl_n}" ] && [ "${WAIT}" -lt "${NEXT_MAX}" ]; do WAIT=$((WAIT * 2)); fl_i=$((fl_i + 1)); done
	[ "${WAIT}" -le "${NEXT_MAX}" ] || WAIT=${NEXT_MAX}
	if [ "${fl_n}" -ge 2 ] && [ -n "${FB}" ]; then setn "${S}/via" $(($(now) + 3600)); fi
	log "sync failed ($1)"
}
cycle() {
	WAIT=${NEXT_DEF} EXECUTED=''
	cy_now=$(now)
	getn "${S}/mute" 0
	if [ "${GN}" -gt "${cy_now}" ]; then WAIT=$((GN - cy_now)); [ "${WAIT}" -le "${NEXT_MAX}" ] || WAIT=${NEXT_MAX}; return 0; fi
	if mem_low; then WAIT=${NEXT_MIN}; log 'low memory: cycle skipped'; return 0; fi
	sync_once
	case ${HTTP} in
		200) ;;
		400) rm -f "${S}/report"; WAIT=${NEXT_MIN}; log 'report refused (400)'; return 0 ;;
		401|403|404)
			getn "${S}/n404" 0
			setn "${S}/n404" $((GN + 1))
			if [ "${GN}" -ge 1 ]; then setn "${S}/mute" $((cy_now + MUTE_SECS)); WAIT=${NEXT_MAX}; log 'denied twice: silent for 6 h'; else failed "http ${HTTP}"; fi
			return 0 ;;
		429|503)
			WAIT=${SO_RA}; [ "${WAIT}" -ge 60 ] || WAIT=900; [ "${WAIT}" -le "${NEXT_MAX}" ] || WAIT=${NEXT_MAX}
			log "panel asked to wait (${HTTP})"; return 0 ;;
		*) failed "http ${HTTP}"; return 0 ;;
	esac
	rm -f "${S}/fails" "${S}/n404" "${S}/via" "${S}/report"
	: >"${S}/synced"
	if ! parse_answer "${S}/resp"; then
		rm -f "${S}/job.new" "${S}/job"
		log 'answer rejected'
		return 0
	fi
	WAIT=${A_NEXT}
	mv "${S}/job.new" "${S}/job"
	[ "${A_STATE}" = active ] && [ "${A_JOB}" != 0000000000000000 ] || return 0
	cy_last=''
	[ ! -f "${S}/last_job" ] || read -r cy_last <"${S}/last_job" || :
	if [ "${A_JOB}" = "${cy_last}" ]; then log 'job already done'; return 0; fi
	getn "${S}/last_exec" 0
	if [ $((cy_now - GN)) -lt "${MIN_CYCLE}" ] && [ "${GN}" -gt 0 ]; then log 'job skipped: too soon'; return 0; fi
	run_job
	EXECUTED=1
	# the report goes at once; the answer to it only gives the next poll time, a job in it waits for the next cycle
	sync_once
	case ${HTTP} in
		200) rm -f "${S}/report"; if parse_answer "${S}/resp"; then WAIT=${A_NEXT}; fi; rm -f "${S}/job.new" ;;
		400) rm -f "${S}/report" ;;
	esac
	return 0
}
locked() { # a lock directory with the pid of its holder; a stale one is taken over
	if mkdir "${S}/lock" 2>/dev/null; then printf '%s\n' "$$" >"${S}/lock/pid"; return 0; fi
	if pid_is "${S}/lock/pid" run || pid_is "${S}/lock/pid" worker; then return 1; fi
	rm -rf "${S}/lock"
	mkdir "${S}/lock" 2>/dev/null && printf '%s\n' "$$" >"${S}/lock/pid"
}
unlock() {
	[ -f "${S}/lock/pid" ] && read -r ul_p <"${S}/lock/pid" && [ "${ul_p}" = "$$" ] && rm -rf "${S}/lock"
	return 0
}
prepare() {
	load_conf || die 'no usable configuration (refusing to run)'
	curl_mode || die 'curl is not installed'
	version_of "${SELF}"
	mkdir -p "${S}" || die 'cannot make the state directory'
	chmod 700 "${S}" 2>/dev/null
}

# ---------------------------------------------------------------- long-running parts

worker_term() {
	[ -z "${NAPPID}" ] || kill "${NAPPID}" 2>/dev/null
	unlock
	[ ! -f "${S}/worker.pid" ] || { read -r wt_p <"${S}/worker.pid" && [ "${wt_p}" = "$$" ] && rm -f "${S}/worker.pid"; }
	exit 0
}
cmd_worker() {
	prepare
	# the worker and its curls at the lowest priority, where renice and ionice exist (a failure is no error)
	renice -n 10 -p "$$" >/dev/null 2>&1; ionice -c 3 -p "$$" >/dev/null 2>&1
	printf '%s\n' "$$" >"${S}/worker.pid"
	trap worker_term TERM INT HUP
	cw_sup=${PPID}
	log "worker started (${VER})"
	while :; do
		if locked; then cycle; unlock; else WAIT=60; fi
		[ -d "/proc/${cw_sup}" ] || worker_term
		rnd $((WAIT * 2 / 5 + 1))
		cw_t=$(now)
		nap $((WAIT - WAIT / 5 + RND))
		[ $(($(now) - cw_t)) -ge 20 ] || exit 1 # no busy loop on a sleep that fails: the supervisor backs off
	done
}
SUP_CHILD=''
sup_term() {
	[ -z "${SUP_CHILD}" ] || kill -TERM "${SUP_CHILD}" 2>/dev/null
	[ -z "${NAPPID}" ] || kill "${NAPPID}" 2>/dev/null
	[ ! -f "${S}/supervisor.pid" ] || { read -r st_p <"${S}/supervisor.pid" && [ "${st_p}" = "$$" ] && rm -f "${S}/supervisor.pid"; }
	exit 0
}
drop_ok() { # root, and a user nobody; under MGP_ROOT only when MGP_DROP is set (tests)
	[ "$(id -u)" = 0 ] && id nobody >/dev/null 2>&1 && { [ -z "${ROOT}" ] || [ -n "${MGP_DROP:-}" ]; }
}
can_drop() { # the worker runs as nobody when there is su and the configuration can be read by it
	drop_ok && command -v su >/dev/null 2>&1 || return 1
	su -s /bin/sh -c "[ -r '${CONF}' ] && [ -w '${S}' ]" nobody >/dev/null 2>&1
}
cmd_supervise() {
	load_conf || die 'no usable configuration (refusing to run)'
	mkdir -p "${S}" || die 'cannot make the state directory'
	chmod 700 "${S}" 2>/dev/null
	printf '%s\n' "$$" >"${S}/supervisor.pid"
	trap sup_term TERM INT HUP
	sp_pause=10
	while :; do
		sp_t=$(now)
		if can_drop; then
			su -s /bin/sh -c "MGP_ROOT='${ROOT}' MGP_MODE='${MODE}' exec sh '${BIN}' worker" nobody </dev/null >/dev/null 2>&1 &
		else
			sh "${SELF}" worker </dev/null >/dev/null 2>&1 &
		fi
		SUP_CHILD=$!
		wait "${SUP_CHILD}"
		SUP_CHILD=''
		[ $(($(now) - sp_t)) -lt 300 ] || sp_pause=10 # a worker that lived 5 minutes was fine: start over
		log "worker ended; restarting in ${sp_pause} s"
		nap "${sp_pause}"
		[ "${sp_pause}" -ge 1800 ] || sp_pause=$((sp_pause * 2)) # quick deaths: 10, 20, 40 ... 2560 s
	done
}
cmd_run() {
	if [ -z "${MGP_DROPPED:-}" ] && [ "${MODE}" = entware ] && can_drop; then # the state belongs to nobody: run as nobody
		exec su -s /bin/sh -c "MGP_DROPPED=1 MGP_ROOT='${ROOT}' MGP_MODE='${MODE}' exec sh '${BIN}' run" nobody
	fi
	prepare
	locked || die 'another cycle is running'
	trap unlock EXIT
	cycle
	printf 'run: http %s, executed %s, next %s\n' "${HTTP:-none}" "${EXECUTED:-0}" "${WAIT}"
}

# ---------------------------------------------------------------- the service

svc_start() {
	case ${MODE} in
		systemd) systemctl start mistgate-probe ;;
		*)
			if pid_is "${S}/supervisor.pid" supervise; then echo 'already running'; return 0; fi
			mkdir -p "${S}" || return 1
			if drop_ok; then chown nobody "${S}" 2>/dev/null; fi
			( trap '' HUP; MGP_ROOT=${ROOT}; MGP_MODE=${MODE}; MGP_DROP=${MGP_DROP:-}; export MGP_ROOT MGP_MODE MGP_DROP; exec sh "${BIN}" supervise </dev/null >/dev/null 2>&1 ) &
			;;
	esac
}
svc_stop() {
	case ${MODE} in
		systemd) systemctl stop mistgate-probe 2>/dev/null ;;
		*) stop_pid "${S}/supervisor.pid" supervise; stop_pid "${S}/worker.pid" worker ;;
	esac
	return 0
}
cmd_status() {
	version_of "${SELF}"; curl_mode
	printf 'mistgate-probe v=%s mode=%s curl=%s\n' "${VER}" "${MODE}" "${CMODE}"
	if load_conf; then printf 'conf: ok, probe %s\n' "${ID}"; else echo 'conf: missing or invalid'; fi
	if [ "${MODE}" = systemd ]; then printf 'service: %s\n' "$(systemctl is-active mistgate-probe 2>/dev/null)"
	elif pid_is "${S}/supervisor.pid" supervise; then echo "supervisor: running (pid ${PIDOF})"
	else echo 'supervisor: not running'; fi
	getn "${S}/last_exec" 0; printf 'last measured (epoch): %s\n' "${GN}"
	if [ -f "${S}/synced" ]; then echo 'synced: yes'; else echo 'synced: no'; fi
	getn "${S}/mute" 0
	if [ "${GN}" -gt "$(now)" ]; then echo 'silent: the panel denied this probe'; fi
}

# ---------------------------------------------------------------- install, upgrade, uninstall

need_root() { [ -n "${ROOT}" ] || [ "$(id -u)" = 0 ] || die 'run as root'; }
copy_self() { # this script to BIN, atomically, after sh -n
	sh -n "${SELF}" || die 'the script does not parse'
	mkdir -p "${BIN%/*}" || die 'cannot make the program directory'
	cp "${SELF}" "${BIN}.new" || die 'cannot copy the script'
	chmod 755 "${BIN}.new"
	mv "${BIN}.new" "${BIN}" || die 'cannot install the script'
}
write_service() {
	case ${MODE} in
		entware)
			mkdir -p "${INIT%/*}" || die 'no init.d'
			{
				printf '#!/bin/sh\n# Mistgate probe: written by mistgate-probe install\nPATH=%s\nMGP_ROOT=%s\nMGP_MODE=%s\nexport PATH MGP_ROOT MGP_MODE\n' "'${PATH}'" "'${ROOT}'" "'${MODE}'"
				# shellcheck disable=SC2016
				printf '[ -f %s ] || exit 0\ncase "${1:-}" in\n  start|stop|restart|status) exec sh %s "$1" ;;\n  *) echo "usage: $0 start|stop|restart|status"; exit 1 ;;\nesac\n' "${BIN}" "${BIN}"
			} >"${INIT}.new"
			chmod 755 "${INIT}.new"; mv "${INIT}.new" "${INIT}" ;;
		systemd)
			mkdir -p "${INIT%/*}" || die 'no unit directory'
			{
				printf '[Unit]\nDescription=Mistgate availability probe\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\n'
				printf 'ExecStart=/bin/sh %s supervise\nRestart=always\nRestartSec=10\nDynamicUser=yes\nLoadCredential=conf:%s\n' "${BIN#"${ROOT}"}" "${CONF#"${ROOT}"}"
				printf 'RuntimeDirectory=mistgate-probe\nRuntimeDirectoryMode=0700\nUMask=0077\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\n'
				printf 'PrivateTmp=yes\nPrivateDevices=yes\nCapabilityBoundingSet=\nAmbientCapabilities=\nRestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\n'
				printf 'ProtectKernelTunables=yes\nProtectControlGroups=yes\nRestrictNamespaces=yes\nLockPersonality=yes\nMemoryMax=64M\nTasksMax=16\n\n'
				printf '[Install]\nWantedBy=multi-user.target\n'
			} >"${INIT}.new"
			mv "${INIT}.new" "${INIT}"
			[ -n "${ROOT}" ] || systemctl daemon-reload || die 'systemctl daemon-reload failed'
			[ -n "${ROOT}" ] || systemctl enable mistgate-probe >/dev/null 2>&1 ;;
	esac
}
enroll() { # $1 base, $2 token: writes the configuration
	mkdir -p "${S}" || die 'cannot make the state directory'
	rm -f "${S}/resp"
	# the token goes in on stdin, not on a command line
	en_http=$(printf '%s' "$2" | net_curl --connect-timeout 10 -m 30 --max-filesize 4096 --limit-rate 100k -X POST --data-binary @- \
		-o "${S}/resp" -w '%{http_code}' "$1/enroll" 2>/dev/null)
	[ "${en_http}" = 200 ] || return 1
	en_l=0 en_id='' en_sec=''
	while IFS= read -r en_line; do
		en_l=$((en_l + 1))
		case ${en_l} in
			1) [ "${en_line}" = 'mgp 1' ] || return 1 ;;
			2) en_id=${en_line#id }; [ "${en_line}" != "${en_id}" ] && id_ok "${en_id}" || return 1 ;;
			3) en_sec=${en_line#secret }; [ "${en_line}" != "${en_sec}" ] && secret_ok "${en_sec}" || return 1 ;;
			4) [ "${en_line}" = end ] || return 1 ;;
			*) return 1 ;;
		esac
	done <"${S}/resp"
	[ "${en_l}" -eq 4 ] || return 1
	mkdir -p "${CDIR}" || die 'cannot make the configuration directory'
	chmod 700 "${CDIR}"
	printf 'url=%s\nfallback=\nid=%s\nsecret=%s\n' "$1" "${en_id}" "${en_sec}" >"${CONF}.new"
	chmod 600 "${CONF}.new"
	mv "${CONF}.new" "${CONF}" || die 'cannot write the configuration'
	rm -f "${S}/resp"
}
share_conf() { # the worker may be nobody: it reads the configuration through the group, and nobody else does
	[ "${MODE}" = entware ] && drop_ok || return 0
	sc_g=$(id -g nobody 2>/dev/null) || return 0
	if chown "0:${sc_g}" "${CDIR}" "${CONF}" 2>/dev/null; then chmod 750 "${CDIR}"; chmod 640 "${CONF}"; fi
}
tools_ok() {
	to_miss=''
	for to_c in date sleep tr wc id mkdir cp mv rm chmod; do command -v "${to_c}" >/dev/null 2>&1 || to_miss="${to_miss} ${to_c}"; done
	[ -z "${to_miss}" ] || die "missing commands:${to_miss}"
	curl_mode || die 'curl is not installed (opkg install curl ca-bundle)'
	command -v sha256sum >/dev/null 2>&1 || die 'sha256sum is not installed (opkg install coreutils-sha256sum)'
	[ "${MODE}" != none ] || die 'unsupported system: needs Entware (/opt/etc/init.d) or systemd'
}
cmd_install() {
	[ $# -eq 2 ] || die 'usage: install <base-url> <token>'
	base_ok "$1" || die 'bad panel address'
	token_ok "$2" || die 'bad token'
	need_root; tools_ok
	version_of "${SELF}"
	svc_stop
	mkdir -p "${S}" || die 'cannot make the state directory'
	copy_self
	if enroll "$1" "$2"; then ins_new=1
	elif load_conf && [ "${U}" = "$1" ]; then ins_new=''
	else die 'the panel did not accept the token (it is one-time and valid 24 h: make a new one)'; fi
	load_conf || die 'bad configuration'
	share_conf
	write_service
	rm -f "${S}/synced" "${S}/mute" "${S}/n404" "${S}/fails" "${S}/via"
	svc_start || die 'cannot start the service'
	ins_i=0
	while [ "${ins_i}" -lt 30 ] && [ ! -f "${S}/synced" ]; do nap 1; ins_i=$((ins_i + 1)); done
	if [ -f "${S}/synced" ]; then
		if [ -n "${ins_new}" ]; then echo "OK: probe ${ID} registered and running"; else echo "OK: probe ${ID} was already registered; refreshed and running"; fi
	else
		echo "WARNING: probe ${ID} is installed and running, but the panel has not answered yet; it keeps trying"
	fi
}
cmd_upgrade() {
	need_root; tools_ok
	load_conf || die 'not installed (no usable configuration): use install with a token'
	[ "${SELF}" != "${BIN}" ] || die 'run the downloaded script, not the installed copy'
	svc_stop
	copy_self
	share_conf
	write_service
	svc_start || die 'cannot start the service'
	version_of "${BIN}"
	echo "OK: upgraded to ${VER}"
}
cmd_uninstall() {
	need_root
	svc_stop
	if [ "${MODE}" = systemd ] && [ -z "${ROOT}" ]; then systemctl disable mistgate-probe >/dev/null 2>&1; fi
	rm -f "${INIT}" "${BIN}"
	[ "${MODE}" != systemd ] || [ -n "${ROOT}" ] || systemctl daemon-reload >/dev/null 2>&1
	case ${CDIR} in */mistgate-probe) rm -rf "${CDIR}" ;; esac
	case ${S} in */mistgate-probe) rm -rf "${S}" ;; esac
	echo 'OK: mistgate-probe removed'
	exit 0
}

cmd=${1:-}
[ $# -eq 0 ] || shift
case ${cmd} in
	install) cmd_install "$@" ;;
	upgrade) cmd_upgrade ;;
	uninstall) cmd_uninstall ;;
	run) cmd_run ;;
	worker) cmd_worker ;;
	supervise) cmd_supervise ;;
	status) cmd_status ;;
	start) svc_start ;;
	stop) svc_stop ;;
	restart) svc_stop; svc_start ;;
	*) die 'usage: install <base-url> <token> | upgrade | uninstall | run | supervise | status | start | stop | restart' ;;
esac
