// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { bundleTestModule } from "./test-helpers.mjs";

const { availableBackupKinds, repositoryHealth, repositoryTarget } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "repository-health"));
const { hostAccess } = await import(bundleTestModule(new URL("./backup-repositories.tsx", import.meta.url), "repository-host-access", true));
const now = Date.parse("2026-09-28T10:00:00Z");
assert.equal(repositoryHealth({ health: { status: "online", observed_at: "2026-09-28T09:59:00Z" } }, now), "online");
assert.equal(repositoryHealth({ health: { status: "offline", observed_at: "2026-09-28T09:59:00Z" } }, now), "offline");
assert.equal(repositoryHealth({ health: { status: "online", observed_at: "2026-09-28T09:57:00Z" } }, now), "unknown", "old success is not current health");
assert.equal(repositoryHealth({ health: { status: "online", observed_at: "2026-09-28T10:01:00Z" } }, now), "unknown", "future observations cannot prove health");
assert.equal(repositoryHealth({}, now), "unknown");
assert.deepEqual(availableBackupKinds("fresh", []), ["nfs"], "unsupported Core exposes only NFS");
assert.deepEqual(availableBackupKinds("stale", ["backup-destination-s3-v1"]), ["nfs"], "stale capability cannot enable a destination");
assert.deepEqual(availableBackupKinds("fresh", ["backup-destination-s3-v1", "backup-destination-sftp-v1"]), ["nfs", "s3", "sftp"]);
assert.equal(repositoryTarget({ kind: "s3", s3_source: { endpoint: "https://objects.example.test", bucket: "backups", prefix: "pool-a" } }), "https://objects.example.test/backups/pool-a");

const hosts = [
  { id: "master", hostname: "core-a", pool_id: "pool-1", connection: "fresh" },
  { id: "member", hostname: "core-b", pool_id: "pool-1", connection: "fresh" },
];
const repo = { id: "repo-1", pool_id: "pool-1", access: { status: "ok", readable: true, writable: true, observed_at: new Date().toISOString() }, member_access: [] };
const lights = value => hostAccess(value, hosts, "master").map(({ host, tone, label }) => [host.id, tone, label]);
assert.deepEqual(lights(repo), [["master", "ok", "Accessible"], ["member", "unknown", "Unknown"]], "local access does not imply member access");
assert.deepEqual(lights({ ...repo, access: { ...repo.access, status: "failed", error: "probe failed" } })[0], ["master", "bad", "No access"]);
assert.deepEqual(lights({ ...repo, access: { ...repo.access, observed_at: new Date(Date.now() - 121_000).toISOString() } })[0], ["master", "unknown", "Unknown"]);
assert.deepEqual(hostAccess(repo, [{ ...hosts[0], connection: "stale" }], "master").map(({ tone, label }) => [tone, label]), [["unknown", "Unknown"]]);
const member = { repository_id: repo.id, host_id: "member", generation: 1, readable: true, writable: true, observed_at: new Date().toISOString() };
const withMember = { ...repo, generation: 1, member_access: [member] };
assert.deepEqual(lights(withMember)[1], ["member", "ok", "Accessible"], "backup probe reports read/write without storage identity, QEMU or locking fields");
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, writable: false }] })[1], ["member", "bad", "No access"]);
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, error: "probe failed" }] })[1], ["member", "bad", "Error"]);
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, generation: 0 }] })[1], ["member", "warn", "Pending"]);
assert.deepEqual(lights({ ...withMember, member_access: [{ ...member, observed_at: new Date(Date.now() - 121_000).toISOString() }] })[1], ["member", "unknown", "Unknown"]);

const source = readFileSync(new URL("./backup-repositories.tsx", import.meta.url), "utf8");
const create = source.split("const onCreate = ")[1]?.split("const onTest = ")[0];
assert.ok(create, "repository create handler exists");
assert.ok(create.includes('createID.current ||= rid("brepo")'), "a lost reply retains the repository ID");
assert.ok(create.includes('const registrationID = requestID(createKey, "brepo")'), "a lost reply retains the request ID");
assert.ok(create.includes('const credentialID = remote.kind === "nfs" ? "" : requestID(credentialKey, "bcred")'), "credential handoff has a stable retry ID");
assert.ok(create.indexOf('"storage/credentials", "POST"') < create.indexOf('"POST", {\n        request_id: registrationID'), "secret handoff precedes registration");
assert.ok(create.includes('credential_ref: credentialReceipt.current, credential_request_id: credentialID'), "registration carries only the opaque receipt");
assert.ok(create.includes('setRemote(current => ({ ...current, secret: "", sshKey: "" }))'), "acknowledged secrets leave form state before registration");
assert.ok(create.indexOf("resetForm();") > create.indexOf("await api<"), "successful submission settles held IDs");
assert.ok(source.includes("if (working || createUncertain) return;"), "uncertain registration cannot be closed and retried with a different body");
assert.ok(source.includes("const fieldsDisabled = Boolean(working) || createUncertain;"), "all destination and secret fields share the uncertain-retry freeze");
assert.ok(!source.includes("localStorage"), "credentials are not persisted in browser storage");
