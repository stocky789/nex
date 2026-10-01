#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
set -eu

root=${NEX_PACKAGE_ROOT:-}
policy=${root}/usr/sbin/policy-rc.d
saved=${root}/usr/sbin/.policy-rc.d.nex-package-save
no_policy=${root}/run/.nex-package-no-policy

check_no_stale_policy() {
    [ ! -e "$saved" ] && [ ! -L "$saved" ] && [ ! -e "$no_policy" ] || {
        echo 'stale package service policy state exists' >&2
        return 1
    }
}

restore_policy() {
    if [ -e "$saved" ] || [ -L "$saved" ]; then
        rm -f "$policy"
        mv "$saved" "$policy"
    elif [ -e "$no_policy" ]; then
        rm -f "$policy" "$no_policy"
    else
        echo 'package service policy state is missing; refusing to continue' >&2
        return 1
    fi
}

case ${1:-} in
    install)
        [ "$#" -gt 1 ] || exit 2
        shift
        policy_helper=$0
        check_no_stale_policy
        trap 'sh "$policy_helper" restore' EXIT
        trap 'exit 1' HUP INT TERM
        sh "$policy_helper" guard
        export DEBIAN_FRONTEND=noninteractive
        apt-get update
        apt-get install -y --no-install-recommends "$@"
        sh "$policy_helper" runtime-qnetd
        sh "$policy_helper" disable qnetd
        sh "$policy_helper" restore
        trap - EXIT HUP INT TERM
        ;;
    guard)
        [ "$#" -eq 1 ] || exit 2
        check_no_stale_policy
        install -d -m755 "${root}/usr/sbin" "${root}/run"
        if [ -e "$policy" ] || [ -L "$policy" ]; then
            cp -a "$policy" "$saved"
        else
            : > "$no_policy"
        fi
        temporary=${policy}.nex-$$
        cat > "$temporary" <<POLICY
#!/bin/sh
service=\${1##*/}
service=\${service%.service}
case \$service in
    sanlock|wdmd|corosync-qdevice|corosync-qnetd) exit 101 ;;
esac
if [ -x "$saved" ]; then
    exec "$saved" "\$@"
fi
exit 101
POLICY
        chmod 755 "$temporary"
        mv -f "$temporary" "$policy"
        ;;
    restore)
        [ "$#" -eq 1 ] || exit 2
        restore_policy
        ;;
    disable)
        [ "$#" -le 2 ] || exit 2
        if [ "${2:-}" = qnetd ]; then
            units='corosync-qnetd.service'
            services='corosync-qnetd'
        elif [ "$#" -eq 2 ]; then
            exit 2
        else
            units='sanlock.service wdmd.service corosync-qdevice.service'
            services='sanlock wdmd corosync-qdevice'
        fi
        for unit in $units; do
            systemctl --root="$root/" disable "$unit"
            state=$(systemctl --root="$root/" is-enabled "$unit" 2>&1 || :)
            case $state in
                disabled|static|indirect|masked|masked-runtime) ;;
                *) echo "cannot prove $unit is disabled ($state)" >&2; exit 1 ;;
            esac
        done
        for service in $services; do
            if [ -f "${root}/etc/init.d/$service" ]; then
                update-rc.d "$service" disable
                for link in "${root}"/etc/rc[0-6S].d/S[0-9][0-9]"$service"; do
                    [ ! -L "$link" ] || {
                        echo "SysV startup link remains enabled: $link" >&2
                        exit 1
                    }
                done
            fi
        done
        ;;
    runtime-qnetd)
        [ "$#" -eq 1 ] || exit 2
        qnetd_db=${root}/etc/corosync/qnetd/nssdb
        runtime_db=${root}/run/nex-witness/nssdb
        tmpfiles=${root}/etc/tmpfiles.d/nex-qnetd.conf
        qnetd_owner=${NEX_QNETD_OWNER:-root}
        qnetd_group=${NEX_QNETD_GROUP:-coroqnetd}
        install -d -m 0755 "${root}/etc/tmpfiles.d" "${root}/run/nex-witness" "${root}/etc/corosync/qnetd"
        printf 'd /run/nex-witness/nssdb 0770 root coroqnetd -\n' > "$tmpfiles.nex-$$"
        chmod 0644 "$tmpfiles.nex-$$"
        mv -f "$tmpfiles.nex-$$" "$tmpfiles"
        install -d -o "$qnetd_owner" -g "$qnetd_group" -m 0770 "$runtime_db"
        if [ -L "$qnetd_db" ]; then
            [ "$(readlink "$qnetd_db")" = /run/nex-witness/nssdb ] || {
                echo "unexpected qnetd NSS database symlink: $qnetd_db" >&2
                exit 1
            }
        else
            rm -rf "$qnetd_db"
            ln -s /run/nex-witness/nssdb "$qnetd_db"
        fi
        ;;
    *)
        echo 'usage: package-service-policy.sh {install packages...|guard|restore|disable [qnetd]|runtime-qnetd}' >&2
        exit 2
        ;;
esac
