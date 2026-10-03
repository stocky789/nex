// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import React from "react";
import { bundleTestModule } from "./test-helpers.mjs";

const { repositoryHealth, repositoryTarget } = await import(bundleTestModule(new URL("./backup-types.ts", import.meta.url), "repository-health"));
const { BackupRepositories, backupSource, browsedFolderFields, forgetBody, hostAccess, validRepositoryID, validSMBDomain } = await import(bundleTestModule(new URL("./backup-repositories.tsx", import.meta.url), "repository-host-access", true));
const { browseSelection } = await import(bundleTestModule(new URL("./share-browser.tsx", import.meta.url), "share-browser-selection"));
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
assert.deepEqual(lights({ ...withMember, health: { status: "unknown" } })[1], ["member", "unknown", "Unknown"], "the master's stale report is never shown as member access");
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

for (const good of ["brepo1", "repo-1_a", "A".repeat(64)]) assert.ok(validRepositoryID(good), `${good} is a valid repository ID`);
for (const bad of ["", "-repo", "repo id", "repo/1", "A".repeat(65)]) assert.ok(!validRepositoryID(bad), `${bad} is not a valid repository ID`);
for (const good of ["", "EXAMPLE", "corp.example.test"]) assert.ok(validSMBDomain(good), `${good} is a valid SMB domain`);
for (const bad of ["EX,AMPLE", "EX=AMPLE", "EX\nAMPLE", "EX\\AMPLE", "EX AMPLE", "EX;AMPLE", "a".repeat(254)]) assert.ok(!validSMBDomain(bad), `${JSON.stringify(bad)} is refused before the credential is delivered`);

// Forgetting a repository confirms the name and the counts the operator was shown.
assert.deepEqual(forgetBody("bforget-1", "Primary", [{ id: "a", protected: true }, { id: "b" }, { id: "c", protected: false }]),
  { request_id: "bforget-1", confirmation: "Primary", recovery_points: 3, protected_points: 1 });

// Render the component with a deterministic hook dispatcher and invoke its actual form handlers.
// Network replies and polling remain asynchronous; no DOM or new test dependency is required.
const internals = React.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
function repositoryForm(api, hosts = []) {
  const slots = [], effects = [], timers = new Map();
  let cursor = 0, tree, props = { api, poolID: "pool-a", masterHostID: "master-a", hosts, busy: false, unavailable: false, onNotice() {} };
  globalThis.window = {
    setInterval(fn) { const id = timers.size + 1; timers.set(id, fn); return id; },
    clearInterval(id) { timers.delete(id); },
  };
  globalThis.document = { visibilityState: "visible" };
  const dispatcher = {
    useState(initial) {
      const i = cursor++;
      slots[i] ??= { value: typeof initial === "function" ? initial() : initial };
      return [slots[i].value, next => { slots[i].value = typeof next === "function" ? next(slots[i].value) : next; }];
    },
    useRef(initial) { const i = cursor++; slots[i] ??= { current: initial }; return slots[i]; },
    useEffect(fn, deps) {
      const i = cursor++, old = slots[i];
      if (!old || deps.some((value, index) => !Object.is(value, old.deps[index]))) {
        effects.push(() => { old?.cleanup?.(); slots[i] = { deps, cleanup: fn() }; });
      }
    },
  };
  const render = () => {
    cursor = 0;
    const previous = internals.H;
    internals.H = dispatcher;
    try { tree = BackupRepositories(props); } finally { internals.H = previous; }
    effects.splice(0).forEach(fn => fn());
    return tree;
  };
  const nodes = value => {
    if (Array.isArray(value)) return value.flatMap(item => nodes(item));
    if (!value || typeof value !== "object" || !value.props) return [];
    return [value, ...nodes(value.props.children), ...nodes(value.props.actions)];
  };
  const field = label => {
    const node = nodes(tree).find(n => n.type === "label" && nodes(n).some(child => ["input", "select", "textarea"].includes(child.type)) &&
      (Array.isArray(n.props.children) ? n.props.children[0] : n.props.children) === label);
    assert.ok(node, `field ${label} is visible`);
    return nodes(node).find(n => ["input", "select", "textarea"].includes(n.type));
  };
  render();
  return {
    render, field,
    button(label) {
      render();
      const node = nodes(tree).find(n => n.type === "button" && n.props.children === label);
      assert.ok(node, `button ${label} is visible`);
      return node;
    },
    async settle() { for (let i = 0; i < 6; i++) await Promise.resolve(); render(); },
    open() { render(); nodes(tree).find(n => n.type === "button" && n.props.className === "sm primary").props.onClick(); render(); },
    fill(label, value) { render(); field(label).props.onChange({ target: { value } }); render(); },
    submit() { render(); return nodes(tree).find(n => n.type === "form").props.onSubmit({ preventDefault() {} }); },
    poll() { for (const fn of timers.values()) fn(); },
    switchPool() { props = { ...props, poolID: "pool-b", masterHostID: "master-b" }; render(); },
    close() { for (const slot of slots) slot?.cleanup?.(); delete globalThis.window; delete globalThis.document; },
  };
}
const unavailableReply = () => Object.assign(new Error("Repository list unavailable"), { status: 503 });
const registrationCalls = [];
let pollingFailed = false;
const retryForm = repositoryForm(async (path, method, body) => {
  if (method === "GET") {
    if (pollingFailed) throw unavailableReply();
    return [{ id: "repo-existing", name: "Primary", kind: "nfs", source: { server: "backup.example.test", export: "/backups" } }];
  }
  registrationCalls.push(structuredClone(body));
  throw new Error("Accepted reply was lost");
});
await retryForm.settle();
retryForm.open();
retryForm.fill("Name", "Primary");
retryForm.fill("Repository ID", "repo-existing");
retryForm.fill("Type the repository ID to confirm", "repo-existing");
retryForm.fill("NFS server", "backup.example.test");
retryForm.fill("Export path", "/backups");
await retryForm.submit();
pollingFailed = true;
retryForm.poll();
await retryForm.settle();
assert.equal(retryForm.field("Name").props.disabled, true, "an uncertain reply freezes editable fields");
await retryForm.submit();
assert.equal(registrationCalls.length, 2, "registration remains retryable after polling fails");
assert.equal(registrationCalls[0].replace_confirmation, "repo-existing");
assert.deepEqual(registrationCalls[1], registrationCalls[0], "lost reply and list 503 retain the entire accepted payload");
retryForm.close();

const credentialCalls = [], poolRegistrations = [];
const scopedForm = repositoryForm(async (path, method, body) => {
  if (method === "GET") return [];
  if (path === "storage/credentials") {
    credentialCalls.push(structuredClone(body));
    return { receipt: `receipt-${body.pool_id}` };
  }
  poolRegistrations.push({ path, body: structuredClone(body) });
  throw new Error("Accepted reply was lost");
});
await scopedForm.settle();
function fillSMB(form, name) {
  form.open();
  form.fill("Name", name);
  form.fill("Destination type", "smb3");
  form.fill("SMB server", "files.example.test");
  form.fill("Share", "backups");
  form.fill("Username", "backup-user");
  form.fill("Domain", "EXAMPLE");
  form.fill("Password", "fixture-secret");
}
fillSMB(scopedForm, "Pool A");
scopedForm.fill("Repository ID", "pool-a-repository");
await scopedForm.submit();
scopedForm.render();
assert.equal(scopedForm.field("Password").props.value, "", "acknowledged secret is cleared before retry");
scopedForm.switchPool();
await scopedForm.settle();
scopedForm.open();
assert.equal(scopedForm.field("Name").props.value, "", "pool switches discard the old draft");
assert.equal(scopedForm.field("Repository ID").props.value, "", "pool switches discard operator-selected IDs");
scopedForm.fill("Name", "Pool B");
scopedForm.fill("Destination type", "smb3");
scopedForm.fill("SMB server", "files.example.test");
scopedForm.fill("Share", "backups");
scopedForm.fill("Username", "backup-user");
assert.equal(scopedForm.field("Password").props.required, true, "pool B requires its own credential");
await scopedForm.submit();
assert.equal(poolRegistrations.length, 1, "pool B cannot register using pool A's acknowledged receipt");
scopedForm.fill("Password", "fixture-secret-b");
await scopedForm.submit();
assert.equal(credentialCalls[1].pool_id, "pool-b");
assert.equal(credentialCalls[1].host_id, "master-b");
assert.notEqual(credentialCalls[1].request_id, credentialCalls[0].request_id);
assert.notEqual(poolRegistrations[1].body.id, poolRegistrations[0].body.id);
assert.notEqual(poolRegistrations[1].body.request_id, poolRegistrations[0].body.request_id);
assert.equal(poolRegistrations[1].body.credential_ref, "receipt-pool-b");
assert.equal(credentialCalls[0].credentials.domain, "EXAMPLE", "SMB domain belongs to the secret handoff");
assert.ok(!("credentials" in poolRegistrations[1].body) && !("secret" in poolRegistrations[1].body), "registration carries the opaque receipt, not a secret");
scopedForm.close();

let acknowledgeCredential;
const lateRegistrations = [];
const lateForm = repositoryForm(async (path, method, body) => {
  if (method === "GET") return [];
  if (path === "storage/credentials") return await new Promise(resolve => { acknowledgeCredential = resolve; });
  lateRegistrations.push({ path, body: structuredClone(body) });
  return {};
});
await lateForm.settle();
fillSMB(lateForm, "Old pool");
const oldSubmission = lateForm.submit();
lateForm.switchPool();
await lateForm.settle();
acknowledgeCredential({ receipt: "receipt-old-pool" });
await oldSubmission;
lateForm.open();
assert.equal(lateForm.field("Name").props.value, "", "late credential acknowledgement cannot restore the old draft");
lateForm.fill("Destination type", "smb3");
assert.equal(lateForm.field("Password").props.required, true, "late pool A acknowledgement cannot authorize pool B");
assert.deepEqual(lateRegistrations, [], "an old-scope credential reply cannot submit a registration after switching pools");
lateForm.close();

// Folder discovery: the same transient Core request the VM storage picker sends, scoped to the pool master and the backup purpose.
const master = { id: "master-a", connection: "fresh", revoked: false, capabilities: ["storage-repositories-v1", "storage-discovery-v1", "storage-discovery-purpose-v1"] };
const discoveries = [];
const browseForm = repositoryForm(async (path, method, body) => {
  if (method === "GET") return [];
  discoveries.push({ path, method, body: structuredClone(body) });
  return { status: "succeeded", candidates: [] };
}, [master]);
await browseForm.settle();
browseForm.open();
assert.equal(browseForm.button("Browse exports").props.disabled, true, "NFS browsing waits for a server");
browseForm.fill("NFS server", "nas.example.test");
assert.equal(browseForm.button("Browse exports").props.disabled, false);
browseForm.button("Browse exports").props.onClick();
await browseForm.settle();
assert.deepEqual(discoveries.map(({ path, method, body: { request_id, ...rest } }) => [path, method, rest]), [
  ["storage/discover", "POST", { pool_id: "pool-a", host_id: "master-a", kind: "nfs", purpose: "backup", server: "nas.example.test" }],
]);
browseForm.fill("Destination type", "smb3");
assert.equal(browseForm.button("Browse shared folders").props.disabled, true, "SMB browsing waits for an account");
browseForm.fill("Username", "backup-user");
browseForm.fill("Domain", "EXAMPLE");
browseForm.fill("Password", "fixture-secret");
browseForm.button("Browse shared folders").props.onClick();
await browseForm.settle();
assert.deepEqual(discoveries[1].body.credentials, { username: "backup-user", secret: "fixture-secret", domain: "EXAMPLE" });
assert.equal(discoveries[1].body.kind, "smb");
assert.equal(discoveries[1].body.purpose, "backup");
browseForm.close();

const staleForm = repositoryForm(async () => [], [{ ...master, connection: "stale" }]);
await staleForm.settle();
staleForm.open();
staleForm.fill("NFS server", "nas.example.test");
assert.equal(staleForm.button("Browse exports").props.disabled, true, "an unreachable pool master cannot browse");
staleForm.close();

// An older Core ignores the purpose field, so it would list ordinary directories: the Controller refuses, and the form does not offer the picker.
const oldCoreForm = repositoryForm(async () => [], [{ ...master, capabilities: ["storage-repositories-v1", "storage-discovery-v1"] }]);
await oldCoreForm.settle();
oldCoreForm.open();
oldCoreForm.fill("NFS server", "nas.example.test");
assert.equal(oldCoreForm.button("Browse exports").props.disabled, true, "backup browsing needs the purpose capability");
oldCoreForm.close();

// A folder picked in the browser fills the fields of its protocol.
assert.deepEqual(browsedFolderFields("nfs", "/exports/backups", "team/vms"), { exportPath: "/exports/backups", directory: "team/vms" });
assert.deepEqual(browsedFolderFields("smb", "backups", ""), { share: "backups", directory: "" });
assert.deepEqual(browseSelection("smb", "/backups/team/vms", "", undefined), { container: "backups", directory: "team/vms" });
assert.deepEqual(browseSelection("nfs", "/exports/backups/team", "/exports/backups", undefined), { container: "/exports/backups", directory: "team" });
assert.deepEqual(browseSelection("nfs", "/exports/backups", "", "/exports/backups"), { container: "/exports/backups", directory: "" });
