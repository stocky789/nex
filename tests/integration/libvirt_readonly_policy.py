#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Check virt-aa-helper preserves read-only backing-chain policy."""

import argparse
import os
import re
import subprocess
import sys
import tempfile
import uuid
from pathlib import Path
from xml.sax.saxutils import quoteattr


def domain_xml(backing: Path, overlay: Path, readonly: Path, writable: Path, vm_uuid: str) -> str:
    return f"""<domain type='kvm'>
  <name>recovery-readonly-policy</name>
  <uuid>{vm_uuid}</uuid>
  <memory>524288</memory>
  <currentMemory>524288</currentMemory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='pc'>hvm</type></os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file={quoteattr(str(overlay))}/>
      <backingStore type='file'>
        <format type='qcow2'/>
        <source file={quoteattr(str(backing))}/>
        <backingStore/>
      </backingStore>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file={quoteattr(str(readonly))}/>
      <target dev='vdb' bus='virtio'/>
      <readonly/>
    </disk>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file={quoteattr(str(writable))}/>
      <target dev='vdc' bus='virtio'/>
    </disk>
  </devices>
</domain>
"""


def partial_chain_xml(intermediate: Path, overlay: Path, vm_uuid: str, *, terminate: bool = False) -> str:
    return f"""<domain type='kvm'>
  <name>recovery-partial-chain</name>
  <uuid>{vm_uuid}</uuid>
  <memory>524288</memory>
  <currentMemory>524288</currentMemory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='pc'>hvm</type></os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file={quoteattr(str(overlay))}/>
      <backingStore type='file'>
        <format type='qcow2'/>
        <source file={quoteattr(str(intermediate))}/>
        {"<backingStore/>" if terminate else ""}
      </backingStore>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices>
</domain>
"""


def file_permissions(profile: str, path: Path) -> list[str]:
    quoted = re.escape(f'"{path}"')
    return re.findall(rf'^\s*{quoted}\s+([^,]+),\s*$', profile, re.MULTILINE)


def deny_write(profile: str, path: Path) -> bool:
    quoted = re.escape(f'"{path}"')
    return re.search(rf'^\s*deny\s+{quoted}\s+w,\s*$', profile, re.MULTILINE) is not None


def run_helper(helper: Path, xml: str, vm_uuid: str) -> str:
    result = subprocess.run(
        [helper.as_posix(), "--dryrun", "-c", "-u", f"libvirt-{vm_uuid}"],
        input=xml,
        text=True,
        capture_output=True,
        timeout=20,
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"virt-aa-helper failed ({result.returncode}):\n{result.stderr}")
    return result.stdout + result.stderr


def expect_permissions(profile: str, path: Path, expected: str) -> None:
    permissions = file_permissions(profile, path)
    if permissions != [expected]:
        raise AssertionError(f"expected exactly {expected!r} for {path}, got {permissions!r}; helper output:\n{profile}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--helper", required=True, type=Path, help="virt-aa-helper executable to check")
    parser.add_argument(
        "--expect-legacy-deny",
        action="store_true",
        help="expect the original explicit deny, for a baseline comparison",
    )
    args = parser.parse_args()
    helper = args.helper.resolve()
    if not helper.is_file() or not os.access(helper, os.X_OK):
        parser.error(f"helper is not executable: {helper}")

    vm_uuid = str(uuid.uuid4())
    with tempfile.TemporaryDirectory(prefix="nex-aa-policy-") as temp:
        root = Path(temp)
        backing = root / "base.qcow2"
        overlay = root / "overlay.qcow2"
        readonly = root / "readonly.raw"
        writable = root / "writable.raw"
        for path in (backing, overlay, readonly, writable):
            path.touch()

        try:
            profile = run_helper(helper, domain_xml(backing, overlay, readonly, writable, vm_uuid), vm_uuid)
        except (RuntimeError, subprocess.TimeoutExpired) as exc:
            print(exc, file=sys.stderr)
            return 1
        expected_deny = args.expect_legacy_deny
        for readonly_path in (backing, readonly):
            if deny_write(profile, readonly_path) != expected_deny:
                state = "present" if expected_deny else "absent"
                print(
                    f"expected readonly-file write-deny for {readonly_path} to be {state}; "
                    f"helper output:\n{profile}",
                    file=sys.stderr,
                )
                return 1

        expectations = {
            backing: "rk",
            readonly: "rk",
            overlay: "rwk",
            writable: "rwk",
        }
        for path, expected in expectations.items():
            try:
                expect_permissions(profile, path, expected)
            except AssertionError as exc:
                print(exc, file=sys.stderr)
                return 1

        chain_root = root / "chain-base.qcow2"
        intermediate = root / "chain-root.qcow2"
        chain_top = root / "chain-top.qcow2"
        subprocess.run(["qemu-img", "create", "-f", "qcow2", chain_root.as_posix(), "64M"], check=True, capture_output=True)
        subprocess.run(
            ["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", chain_root.as_posix(), intermediate.as_posix()],
            check=True,
            capture_output=True,
        )
        subprocess.run(
            ["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", intermediate.as_posix(), chain_top.as_posix()],
            check=True,
            capture_output=True,
        )
        partial_uuid = str(uuid.uuid4())
        partial = run_helper(
            helper,
            partial_chain_xml(intermediate, chain_top, partial_uuid),
            partial_uuid,
        )
        expect_permissions(partial, chain_top, "rwk")
        expect_permissions(partial, intermediate, "rk")
        expect_permissions(partial, chain_root, "rk")

        terminated_uuid = str(uuid.uuid4())
        terminated_xml = partial_chain_xml(intermediate, chain_top, terminated_uuid, terminate=True)
        terminated = run_helper(helper, terminated_xml, terminated_uuid)
        expect_permissions(terminated, chain_top, "rwk")
        expect_permissions(terminated, intermediate, "rk")
        if file_permissions(terminated, chain_root):
            raise AssertionError(f"empty backingStore terminator did not suppress probing; helper output:\n{terminated}")

        if args.expect_legacy_deny:
            print("baseline check passed: readonly backing has an explicit write-deny")
        else:
            print("patched check passed: readonly backing has no write grant or explicit deny")
        print("partial-chain check passed: omitted backingStore suffix was probed; empty terminator suppressed probing")
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
