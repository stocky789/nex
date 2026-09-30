// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";

const { repositoryHealth, repositoryTarget } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "repository-health"));
const { backupSource, forgetBody, hostAccess, validSMBDomain } = await import(bundleTestModule(new URL("./backup-repositories.tsx", import.meta.url), "repository-host-access", true));
assert.equal(repositoryHealth({ health: { status: "online", observed_at: "2026-09-28T09:59:00Z" } }), "online");
assert.equal(repositoryHealth({ health: { status: "offline", observed_at: "2026-09-28T09:59:00Z" } }), "offline");
assert.equal(repositoryHealth({ health: { status: "online", observed_at: new Date(Date.now() + 3_600_000).toISOString() } }), "online", "a master clock ahead of the browser does not hide Core's health");
assert.equal(repositoryHealth({ health: { status: "unknown" } }), "unknown", "Core and the Controller mark stale health unknown");
assert.equal(repositoryHealth({}), "unknown");
assert.equal(repositoryTarget({ kind: "s3", s3_source: { endpoint: "https://objects.example.test", bucket: "backups", prefix: "pool-a" } }), "https://objects.example.test/backups/pool-a");

const hosts = [
  { id: "master", hostname: "core-a", pool_id: "pool-1", connection: "fresh" },
  { id: "member", hostname: "core-b", pool_id: "pool-1", connection: "fresh" },
];
const repo = { id: "repo-1", pool_id: "pool-1", health: { status: "online" }, access: { status: "ok", readable: true, writable: true, observed_at: new Date().toISOString() }, member_access: [] };
const lights = value => hostAccess(value, hosts, "master").map(({ host, tone, label }) => [host.id, tone, label]);
assert.deepEqual(lights(repo), [["master", "ok", "Accessible"], ["member", "unknown", "Unknown"]], "local access does not imply member access");
assert.deepEqual(lights({ ...repo, access: { ...repo.access, status: "failed", error: "probe failed" } })[0], ["master", "bad", "No access"]);
assert.deepEqual(lights({ ...repo, health: { status: "unknown" } })[0], ["master", "unknown", "Unknown"], "stale Core health makes master access unknown");
assert.deepEqual(hostAccess(repo, [{ ...hosts[0], connection: "stale" }], "master").map(({ tone, label }) => [tone, label]), [["unknown", "Unknown"]]);
const member = { repository_id: repo.id, host_id: "member", readable: true, writable: true, observed_at: new Date().toISOString() };
const withMember = { ...repo, member_access: [member] };
assert.deepEqual(lights(withMember)[1], ["member", "ok", "Accessible"], "backup probe reports read/write without storage identity, QEMU or locking fields");
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, writable: false }] })[1], ["member", "bad", "No access"]);
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, error: "probe failed" }] })[1], ["member", "bad", "Error"]);
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, repository_id: "repo-other" }] })[1], ["member", "warn", "Pending"]);
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, observed_at: new Date(Date.now() + 3_600_000).toISOString() }] })[1], ["member", "ok", "Accessible"], "browser clock skew does not hide Core-bounded member access");

const blank = { kind: "nfs", share: "", endpoint: "", bucket: "", prefix: "", region: "", root: "", port: "", hostKey: "", username: "", secret: "", sshKey: "", server: "", exportPath: "", directory: "" };
const hostKey = "SHA256:" + "A".repeat(43);
assert.deepEqual(backupSource({ ...blank, kind: "s3", endpoint: "https://objects.example.test", bucket: "backups", prefix: "pool-a" }), { s3_source: { endpoint: "https://objects.example.test", bucket: "backups", prefix: "pool-a" } });
assert.deepEqual(backupSource({ ...blank, kind: "s3", endpoint: "https://objects.example.test", bucket: "backups", region: "eu-west-2" }), { s3_source: { endpoint: "https://objects.example.test", bucket: "backups", region: "eu-west-2" } });
assert.deepEqual(backupSource({ ...blank, kind: "sftp", server: "sftp.example.test", root: "/backups", hostKey, port: "2222" }), { sftp_source: { host: "sftp.example.test", root: "/backups", host_key_sha256: hostKey, port: 2222 } });
for (const [label, fields] of [
  ["S3 endpoint path", { kind: "s3", endpoint: "https://objects.example.test/path", bucket: "backups" }],
  ["S3 nested bucket", { kind: "s3", endpoint: "https://objects.example.test", bucket: "a/b" }],
  ["S3 invalid region", { kind: "s3", endpoint: "https://objects.example.test", bucket: "backups", region: "US East" }],
  ["S3 escaping prefix", { kind: "s3", endpoint: "https://objects.example.test", bucket: "backups", prefix: "../x" }],
  ["SMB share with slash", { kind: "smb3", server: "files.example.test", share: "a/b" }],
  ["SMB absolute directory", { kind: "smb3", server: "files.example.test", share: "backups", directory: "/x" }],
  ["SFTP relative root", { kind: "sftp", server: "sftp.example.test", root: "backups", hostKey }],
  ["SFTP unclean root", { kind: "sftp", server: "sftp.example.test", root: "/backups/", hostKey }],
  ["SFTP short host key", { kind: "sftp", server: "sftp.example.test", root: "/backups", hostKey: "SHA256:abc" }],
  ["SFTP port range", { kind: "sftp", server: "sftp.example.test", root: "/backups", hostKey, port: "70000" }],
  ["SFTP user in host", { kind: "sftp", server: "user@sftp.example.test", root: "/backups", hostKey }],
]) assert.equal(backupSource({ ...blank, ...fields }), null, `${label} is refused before the credential is delivered`);

const source = readFileSync(new URL("./backup-repositories.tsx", import.meta.url), "utf8");
const create = source.split("const onCreate = ")[1]?.split("const onTest = ")[0];
assert.ok(create, "repository create handler exists");
assert.ok(create.includes('createID.current ||= rid("brepo")'), "a lost reply retains the repository ID");
assert.ok(create.includes('const registrationID = requestID(createKey, "brepo")'), "a lost reply retains the request ID");
assert.ok(create.includes('const credentialID = remote.kind === "nfs" ? "" : requestID(credentialKey, "bcred")'), "credential handoff has a stable retry ID");
assert.ok(create.indexOf('"storage/credentials", "POST"') < create.indexOf('"POST", {\n        request_id: registrationID'), "secret handoff precedes registration");
for (const good of ["", "EXAMPLE", "corp.example.test"]) assert.ok(validSMBDomain(good), `${good} is a valid SMB domain`);
for (const bad of ["EX,AMPLE", "EX=AMPLE", "EX\nAMPLE", "EX\\AMPLE", "EX AMPLE", "EX;AMPLE", "a".repeat(254)]) assert.ok(!validSMBDomain(bad), `${JSON.stringify(bad)} is refused before the credential is delivered`);
assert.ok(create.includes('remote.kind === "smb3" && remote.domain.trim() ? { domain: remote.domain.trim() }'), "the SMB domain travels with the credential, never with the registration");

assert.ok(create.includes('credential_ref: credentialReceipt.current, credential_request_id: credentialID'), "registration carries only the opaque receipt");
assert.ok(create.includes("credentials, repository: { name: name.trim(), ...source }"), "the Controller validates the destination before offering the credential");
assert.ok(create.includes('setRemote(current => ({ ...current, secret: "", sshKey: "" }))'), "acknowledged secrets leave form state before registration");
assert.ok(create.indexOf("resetForm();") > create.indexOf("await api<"), "successful submission settles held IDs");
assert.ok(source.includes("if (working || createUncertain) return;"), "uncertain registration cannot be closed and retried with a different body");
assert.ok(source.includes("const fieldsDisabled = Boolean(working) || createUncertain;"), "all destination and secret fields share the uncertain-retry freeze");
assert.ok(!source.includes("localStorage"), "credentials are not persisted in browser storage");

// Forgetting a repository confirms the name and the counts the operator was shown.
assert.deepEqual(forgetBody("bforget-1", "Primary", [{ id: "a", protected: true }, { id: "b" }, { id: "c", protected: false }]),
  { request_id: "bforget-1", confirmation: "Primary", recovery_points: 3, protected_points: 1 });
assert.ok(source.includes("disabled={busy || !points || !confirmMatches(typed, repo.name)}"), "forget stays disabled until the counts load and the name is typed");
assert.ok(source.includes("useEffect(() => { requestID.current = rid(\"bforget\"); }, [typed, points]);"), "a changed confirmation is a new request");
assert.ok(source.includes("Nothing is deleted from the destination itself"), "the dialog says destination data is untouched");
