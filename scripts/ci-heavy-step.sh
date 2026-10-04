#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Custom Actions shell. A separate supervisor owns the mutex until the workload
# is stopped and reaped, even when the runner kills the outer wrapper with SIGKILL.
set -euo pipefail
# Async launchers must inherit our group, not become group leaders: otherwise
# setsid forks and $! no longer names the supervisor/workload we must wait and stop.
set +m
if [[ ${1:-} != --supervise ]]; then
    script=${1:?expected the Actions run script}
    setsid --wait bash "${BASH_SOURCE[0]}" --supervise "$script" "$$" &
    supervisor=$!
    signal_status=0
    trap 'signal_status=130; kill -TERM "$supervisor" 2>/dev/null || true' INT
    trap 'signal_status=143; kill -TERM "$supervisor" 2>/dev/null || true' TERM
    set +e
    while true; do
        wait "$supervisor"
        status=$?
        kill -0 "$supervisor" 2>/dev/null || break
    done
    if ((signal_status)); then
        exit "$signal_status"
    fi
    exit "$status"
fi
script=${2:?expected the Actions run script}
owner=${3:?expected the wrapper PID}
lock_pid=
lock_input=
local_lock=
helper_name=
child=
child_start=
watchdog=
acquired=false
signal_status=0
lock_lost=false

stop_requested() {
    if ((signal_status)); then exit "$signal_status"; fi
    if [[ $lock_lost == true ]]; then
        if [[ $acquired == true ]]; then
            echo 'ci-heavy-step: shared Docker-host lock was lost while the script was running' >&2
        else
            echo 'ci-heavy-step: shared Docker-host lock could not be acquired' >&2
        fi
        exit 1
    fi
}

signal_child() {
    local line
    local -a fields
    [[ -n $child_start ]] || return 0
    { IFS= read -r line < "/proc/$child/stat"; } 2>/dev/null || return 0
    read -r -a fields <<< "${line##*) }"
    [[ ${fields[1]} == "$supervisor_pid" && ${fields[19]} == "$child_start" ]] || return 0
    kill -s "$1" "$child" 2>/dev/null || true
}

session_processes() {
    local stat line pid start found=false
    local -a fields
    # A surviving/reused leader PID must still be the child we launched. An
    # exited leader's SID remains reserved while any original session member lives.
    if { IFS= read -r line < "/proc/$child/stat"; } 2>/dev/null; then
        read -r -a fields <<< "${line##*) }"
        [[ -n $child_start && ${fields[19]} == "$child_start" ]] || return 1
    fi
    for stat in /proc/[0-9]*/stat; do
        { IFS= read -r line < "$stat"; } 2>/dev/null || continue
        # The command name is parenthesized and can contain spaces or ')'.
        read -r -a fields <<< "${line##*) }"
        [[ ${fields[3]} == "$child" && ${fields[0]} != Z && ${fields[0]} != X ]] || continue
        found=true
        if [[ -n ${1:-} ]]; then
            pid=${stat%/stat}
            pid=${pid##*/}
            start=${fields[19]}
            # Revalidate the individual PID immediately before signalling: the
            # first /proc snapshot can disappear and its PID can be reused.
            { IFS= read -r line < "$stat"; } 2>/dev/null || continue
            read -r -a fields <<< "${line##*) }"
            [[ ${fields[3]} == "$child" && ${fields[19]} == "$start" ]] || continue
            kill -s "$1" "$pid" 2>/dev/null || true
        fi
    done
    [[ $found == true ]]
}

cleanup() {
    local status=$? attempt
    trap - EXIT
    trap '' INT TERM USR1
    if [[ -n $watchdog ]]; then
        kill "$watchdog" 2>/dev/null || true
        wait "$watchdog" 2>/dev/null || true
    fi
    if [[ -n $child ]]; then
        signal_child TERM
        # Job control can put descendants in other groups of our workload SID.
        # Never wait unboundedly on TERM: workloads may deliberately ignore it.
        for ((attempt=0; attempt<20; attempt++)); do
            session_processes TERM || break
            sleep 0.05
        done
        signal_child KILL
        # Keep the mutex while any member of the owned session can still work.
        # Repeat KILL to cover descendants forked during the TERM grace period.
        while session_processes KILL; do sleep 0.01; done
        wait "$child" 2>/dev/null || true
    fi
    if [[ -n $local_lock ]]; then
        exec {local_lock}>&-
    fi
    if [[ -n $lock_input ]]; then
        exec {lock_input}>&-
    fi
    if [[ -n $lock_pid ]]; then
        # Allow an acquired helper to consume EOF and exit normally. A helper
        # queued inside flock cannot notice EOF, so remove its unique container.
        for ((attempt=0; attempt<20; attempt++)); do
            kill -0 "$lock_pid" 2>/dev/null || break
            sleep 0.05
        done
        if kill -0 "$lock_pid" 2>/dev/null && [[ -n $helper_name ]]; then
            docker rm --force "$helper_name" >/dev/null 2>&1 || true
        fi
        wait "$lock_pid" 2>/dev/null || true
    fi
    exit "$status"
}
trap cleanup EXIT
# Signal handlers only record interruption: cleanup must not run between spawning
# a child and recording its PID, or it could release the mutex ahead of that child.
trap 'signal_status=130' INT
trap 'signal_status=143' TERM
trap 'lock_lost=true' USR1

# This watchdog is started before the helper pipe exists, so it cannot retain
# the pipe. PPid changes on wrapper death; no stale PID or lease-file polling.
supervisor_pid=$BASHPID
bash -c '
    while [[ -r /proc/$1/status ]]; do
        parent=
        while read -r key value rest; do
            if [[ $key == PPid: ]]; then parent=$value; break; fi
        done < "/proc/$1/status" 2>/dev/null || exit 0
        if [[ $parent != "$2" ]]; then
            kill -TERM "$1" 2>/dev/null || true
            exit 0
        fi
        sleep 0.05
    done
' -- "$supervisor_pid" "$owner" &
watchdog=$!

# Exclusive (default): QEMU, image and ISO labs, one at a time and alone. Shared
# (NEX_CI_LOCK=shared): CPU-bound checks that may overlap each other but never an
# exclusive holder. The gate is held only while waiting, so a queued exclusive
# waiter blocks later shared arrivals instead of being starved by them.
lock_mode=--exclusive
[[ ${NEX_CI_LOCK:-} == shared ]] && lock_mode=--shared
image=
if [[ -n ${NEX_CI_LOCK_DIR:-} ]] || ! image=$(docker inspect --format '{{.Image}}' "${HOSTNAME:-$(hostname)}" 2>/dev/null); then
    if [[ -z ${NEX_CI_LOCK_DIR:-} && -e /.dockerenv ]]; then
        echo 'ci-heavy-step: cannot inspect the job container on its Docker host' >&2
        exit 1
    fi
    lock_dir=${NEX_CI_LOCK_DIR:-/tmp/nex-ci-heavy}
    mkdir -p "$lock_dir"
    stop_requested
    # The supervisor retains the same open file description as the lock acquirer,
    # so a killed workload cannot release flock before cleanup has reaped it.
    exec {local_lock}>"$lock_dir/heavy.lock"
    setsid --wait bash -c '
        set +m
        exec 8>"$3/gate.lock"
        flock --exclusive 8 || exit
        flock "$4" "$1" || exit
        exec 8>&-
        exec bash --noprofile --norc -e -o pipefail "$2"
    ' -- "$local_lock" "$script" "$lock_dir" "$lock_mode" &
    child=$!
else
    # Container /tmp is private: reuse the job's existing image and a daemon-owned
    # volume so every job sees the same flock inode without bootstrap dependencies.
    [[ -n $image ]] || { echo 'ci-heavy-step: cannot identify the job container image' >&2; exit 1; }
    read -r uuid < /proc/sys/kernel/random/uuid
    helper_name=nex-ci-heavy-$uuid
    lock_owner=$BASHPID
    stop_requested
    coproc NEX_LOCK {
        set +e
        docker run --rm -i --name "$helper_name" --network none \
            --volume nex-ci-heavy-lock:/nex-ci --entrypoint sh "$image" -c '
                exec 8>/nex-ci/gate.lock 9>/nex-ci/heavy.lock
                flock --exclusive 8 || exit
                flock '"$lock_mode"' 9 || exit
                exec 8>&-
                printf "locked\n"
                cat >/dev/null
            '
        kill -USR1 "$lock_owner" 2>/dev/null
    }
    lock_pid=$!
    if [[ -z ${NEX_LOCK[1]:-} || -z ${NEX_LOCK[0]:-} ]]; then
        echo 'ci-heavy-step: shared Docker-host lock could not be acquired' >&2
        exit 1
    fi
    coproc_input=${NEX_LOCK[1]}
    coproc_output=${NEX_LOCK[0]}
    exec {lock_input}>&"$coproc_input"
    exec {lock_output}<&"$coproc_output"
    exec {coproc_input}>&-
    exec {coproc_output}<&-
    # A queued helper has no output yet. Poll instead of depending on a trapped
    # signal to abort a blocking read; retain bytes if Docker splits the line.
    ready=
    while true; do
        stop_requested
        chunk=
        if IFS= read -r -t 0.1 chunk <&"$lock_output"; then
            ready+=$chunk
            break
        else
            read_status=$?
            ready+=$chunk
            stop_requested
            if ((read_status <= 128)); then
                echo 'ci-heavy-step: shared Docker-host lock could not be acquired' >&2
                exit 1
            fi
        fi
    done
    if [[ $ready != locked ]]; then
        echo 'ci-heavy-step: shared Docker-host lock could not be acquired' >&2
        exit 1
    fi
    acquired=true
    exec {lock_output}<&-
    stop_requested
    setsid --wait bash --noprofile --norc -e -o pipefail "$script" {lock_input}>&- &
    child=$!
fi
# Record the direct child identity before any wait can reap it. A process that
# already exited needs no direct-PID signal; its surviving session is still swept.
if { IFS= read -r child_stat < "/proc/$child/stat"; } 2>/dev/null; then
    read -r -a child_fields <<< "${child_stat##*) }"
    if [[ ${child_fields[1]} == "$supervisor_pid" ]]; then
        child_start=${child_fields[19]}
    fi
fi
stop_requested
set +e
wait "$child"
status=$?
set -e
stop_requested
exit "$status"
