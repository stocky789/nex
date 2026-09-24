# qnetd runtime NSS CA spike

Date: 2026-09-24. This isolated spike tested native Debian qnetd certificate handling with all test NSS databases under `/run`. It did not exercise Nex Controller/Core registration, a real qdevice handshake, a Corosync cluster, or any live host.

## Environment and method

Used an ephemeral Debian 13 container on the development workstation. The `corosync-qnetd` package was version `3.0.3-2`; `libnss3-tools` supplied `certutil`. The initial certificate test downloaded and extracted qnetd rather than installing it. A separate package lifecycle check installed the package with services blocked, exercised its configured post-install path with `dpkg-reconfigure`, and removed the container afterward.

A temporary unprivileged `coroqnetd` user owned `/run/nex-witness/nssdb` (directory mode `0700`; NSS database, password and noise files mode `0600`). The qdevice-side test database and private key were owned by a separate unprivileged user. All CA private key material, qnetd server keys, qdevice private keys, CSRs and certificates existed only in the container's `/run` or `/tmp` and were destroyed with it.

Native `certutil` initialized the qnetd SQL NSS database and made a qnetd CA using the package wrapper's CA-constraints interaction:

```sh
certutil -N -d sql:/run/nex-witness/nssdb -f /run/nex-witness/nssdb/pwdfile.txt
printf 'y\n0\ny\n' | certutil -S -n 'QNet CA' -s 'CN=QNet CA' \
  -x -2 -t 'CT,,' -m 100 -v 1200 \
  -d sql:/run/nex-witness/nssdb \
  -z /run/nex-witness/nssdb/noise.txt \
  -f /run/nex-witness/nssdb/pwdfile.txt
certutil -S -n 'QNetd Cert' -s 'CN=Qnetd Server' -c 'QNet CA' \
  -t 'u,u,u' -m 101 -v 1200 \
  -d sql:/run/nex-witness/nssdb \
  -z /run/nex-witness/nssdb/noise.txt \
  -f /run/nex-witness/nssdb/pwdfile.txt
```

The `-2` option is required for certutil's CA basic-constraints prompt. Omitting it produced a certificate OpenSSL correctly rejected as an invalid CA. With `-2`, qnetd loaded the CA and `QNetd Cert` from the runtime NSS DB.

A qdevice-side `certutil -R` made CSRs with `CN=nex-pool-demo` and `CN=nex-pool-second`; native `certutil -C -c 'QNet CA' -d sql:/run/nex-witness/nssdb` signed both from the same qnetd CA. The test imported the CA with NSS trust `CT,,`, imported the matching signed certificate as `u,u,u`, and `certutil -V -u V` reported the client certificate valid.

The daemon ran as the `coroqnetd` user with the exact tested command:

```sh
corosync-qnetd -f -s req -c on -p 5403 \
  -S nss_db_dir=/run/nex-witness/nssdb
```

`corosync-qnetd-tool -s` reported address `*:5403`, TLS `Required (client certificate required)`, and zero connected clients/clusters. The daemon exited and restarted after the runtime NSS database was deleted and recreated. The replacement CA SHA-256 differed from the original. OpenSSL rejected the old pool certificate chain against the replacement CA and accepted a newly signed certificate; importing the new CA and certificate restored `certutil -V` validation. This demonstrates that Core must re-register and receive the replacement CA/certificate after Controller runtime state is lost.

Two package constraints were confirmed by inspecting the Debian package. Its `corosync-qnetd-certutil` front-end hardcodes `/etc/corosync/qnetd/nssdb` and has no database-directory option. Debian's qnetd package post-install script initializes a CA when `$db/cert9.db` is absent ([Debian source](https://sources.debian.org/src/corosync-qdevice/3.0.3-2/debian/corosync-qnetd.postinst)). A bare symlink to a missing `/run` target fails: the wrapper's `mkdir` and `chmod` cannot follow the dangling symlink. A tmpfiles rule must recreate the target before package configuration.

The package lifecycle check ran in Debian 13 with qnetd `3.0.3-2` and systemd tools. After package installation, it removed the package-created database, created `/run/nex-witness/nssdb` owned by `root:coroqnetd` mode `0770`, installed `/etc/tmpfiles.d/nex-qnetd.conf` containing `d /run/nex-witness/nssdb 0770 root coroqnetd -`, and symlinked `/etc/corosync/qnetd/nssdb` to `/run/nex-witness/nssdb`. The actual configured package post-install was exercised with `dpkg-reconfigure -fnoninteractive corosync-qnetd`. It initialized the database under `/run`; a second configure preserved the CA fingerprint. After deleting the runtime directory to simulate `/run` loss, `systemd-tmpfiles --create /etc/tmpfiles.d/nex-qnetd.conf` recreated the target and package configure generated a different CA fingerprint. No qnetd service was started in this lifecycle check.

The package policy helper now creates this tmpfiles rule and target before installing the symlink, and uses the same idempotent action after package installation on Nex appliance and Core paths. Later package configure scripts will follow the symlink if tmpfiles has recreated the target. The test did not execute an upgrade from a different qnetd package version or a reboot with systemd; it invoked the installed configure script through `dpkg-reconfigure` and simulated `/run` recreation explicitly.

The appliance's native qnetd startup helper was also run in a disposable Debian 13 container after package installation and runtime redirection. It initialized the runtime CA through Debian's `corosync-qnetd-certutil -i -G`, validated both native NSS nicknames, and set the NSS directory to `root:coroqnetd 0770` with all database, key, password, noise, serial and CA files at `root:coroqnetd 0660`. Running the daemon as `coroqnetd` with the configured `-S nss_db_dir=/run/nex-witness/nssdb` command produced qnetd status at `*:5403` with TLS required and client certificates required. The daemon was stopped after the check. The systemd drop-in was installed at its unit path; `systemd-analyze verify` reported `Command 'man corosync-qnetd' failed`, so unit verification is incomplete and systemd itself was not booted there.

## Safe systemd configuration indicated by the test

The extracted Debian unit runs as `coroqnetd`, uses `RuntimeDirectory=corosync-qnetd`, and builds `ExecStart` from `/etc/default/corosync-qnetd`. The appliance drop-in clears the environment file and replaces the start command so qnetd cannot fall back to its `/etc` NSS default:

```ini
[Service]
EnvironmentFile=
ExecStart=
ExecStart=/usr/bin/corosync-qnetd -f -s req -c on -S nss_db_dir=/run/nex-witness/nssdb
```

The Controller appliance enables this unit at boot so the runtime CA exists before Core asks for witness candidates. It is a stateless qnetd listener with TLS client certificates required; it has no pool registrations until an authenticated current pool master submits a CSR. The Controller signs only the deterministic cluster CSR and keeps no pool witness record. The unit's root `ExecStartPre` runs the native runtime initializer before qnetd starts. Package installation removes the package-created default database after package configuration and replaces it with the runtime symlink; tmpfiles recreates the target before later package maintainer scripts. This leaves one volatile CA under `/run`, which rotates after runtime loss.

The Controller service uses `SupplementaryGroups=coroqnetd` and `ReadWritePaths=/run/nex-witness/nssdb` under `ProtectSystem=strict`. In a disposable Debian 13 container, a `nex-controller` user with that supplementary group read the runtime CA and ran the native `corosync-qnetd-certutil -s` signer. OpenSSL verified the resulting cluster certificate against the runtime CA. The qnetd command from the drop-in ran as `coroqnetd`; `corosync-qnetd-tool -s` reported `*:5403` with TLS required and client certificates required. The qnetd service unit's `systemd-analyze verify` still exits on its installed `man corosync-qnetd` documentation command, and systemd itself was not booted in this container; the service lifecycle through systemd remains unverified.

## Limits

- No actual qdevice-to-qnetd TLS session or cluster membership was started. The runtime qnetd status, CSR signing, NSS certificate validation and old/new CA chain behavior were exercised separately.
- The Go Controller tests cover host authentication, stale-generation refusal, and CSR common-name validation; they do not execute a complete Core-to-qnetd registration or qdevice TLS handshake.
- The disposable check verified the native signer as the `nex-controller` user, without starting the Controller under systemd's mount namespace. Unit parsing covers its narrow group and runtime-path settings, but not the live sandbox.
- No live host or external service was contacted. The generated test CA and credentials were removed with the disposable container.

The daemon option is documented in the [Debian qnetd manual](https://manpages.debian.org/testing/corosync-qnetd/corosync-qnetd.8.en.html); the NSS certificate front-end is described in the [Debian qnetd certutil manual](https://manpages.debian.org/testing/corosync-qnetd/corosync-qnetd-certutil.8.en.html).
