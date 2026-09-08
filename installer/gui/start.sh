# SPDX-License-Identifier: AGPL-3.0-only
# Sourced after Xorg by /sbin/debian-installer; do not exit the parent shell.
if [ "$DEBIAN_FRONTEND" = gtk ] && [ "$(debconf-get rescue/enable)" != true ]; then
    nex_saved_umask=$(umask)
    umask 077
    mkdir -p /run/nex-installer
    touch /run/nex-installer/active
    while :; do
        if ! /usr/bin/nex-wizard; then
            printf '%s\n' 'Setup did not finish. Review the settings and try again.' > /run/nex-installer/error
            sleep 1
            continue
        fi
        if [ ! -f /run/nex-installer/prepared ]; then
            printf '%s\n' 'Hardware preparation must finish before installation can begin.' > /run/nex-installer/error
            continue
        fi
        rm -rf /run/nex-installer/target
        if ! /usr/bin/nex-config --apply /run/nex-installer/config.ini >> /var/log/nex-installer-prepare.log 2>&1; then
            printf '%s\n' 'Configuration could not be applied. Review the settings and try again.' > /run/nex-installer/error
            continue
        fi
        # Native seed import logs every input line, including password hashes.
        # Divert only this import, then preserve the earlier diagnostic log.
        mkdir -p /var/lib/preseed
        if [ -e /var/lib/preseed/log ]; then
            mv /var/lib/preseed/log /run/nex-installer/preseed-log
        fi
        ln -s /dev/null /var/lib/preseed/log
        nex_import_status=0
        debconf-set-selections /run/nex-installer/answers.cfg >> /var/log/nex-installer-prepare.log 2>&1 || nex_import_status=$?
        rm -f /var/lib/preseed/log
        if [ -e /run/nex-installer/preseed-log ]; then
            mv /run/nex-installer/preseed-log /var/lib/preseed/log
        fi
        if [ "$nex_import_status" != 0 ]; then
            rm -f /run/nex-installer/answers.cfg
            printf '%s\n' 'Installation settings could not be loaded. Review the settings and try again.' > /run/nex-installer/error
            continue
        fi
        rm -f /run/nex-installer/answers.cfg /run/nex-installer/error
        break
    done
    umask "$nex_saved_umask"
fi
