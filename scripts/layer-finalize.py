#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Turn a package-install delta tree into a Nex sysext layer tree (plan: Layer format and lifecycle).

    scripts/layer-finalize.py --layer-tree DIR --base-tree DIR --spec image/mkosi.images/nex-zfs/layer.json \
        --build-id ID [--source-date-epoch N] [--report FILE]

LAYER-TREE is only what the layer adds on top of BASE-TREE (an overlay upper, or a tree diff). A sysext
merges /usr and /opt only, so everything else Debian's maintainer scripts left behind is normalised or
the build fails:

* /etc payload      moves to usr/share/factory/etc plus a tmpfiles `C+` rule per top-level entry (merged in on use,
                    never over an operator's file; plain `C` would skip a shared directory such as /etc/default
                    that already holds files); /etc/passwd, group, shadow and gshadow additions become sysusers.d
                    entries with the exact UID/GID the install chose, then the databases are removed.
* /var, /run, /srv  must be empty or be listed in the spec's `allow_var`; dpkg state is exported as
                    usr/share/nex/layers/<name>.packages (name, version, arch) and removed.
* base collisions   a layer file that exists in the base tree hides the base's file (and a sysext cannot
                    replace a verity file anyway); only the spec's `allow_collisions` globs may.
* unit ordering     no unit may order before local-fs.target/sysinit.target/basic.target or be wanted by
                    them (merge happens after those); drop-ins are applied before checking, so a layer
                    fixes a stock unit with a `Before=` reset drop-in. .mount/.automount units are refused.
* identity          usr/lib/extension-release.d/extension-release.<name> (ID=nex-core, SYSEXT_LEVEL=<build>)
                    and usr/lib/nex/layers/<name>.json are written, replacing anything the packages shipped.
* modules           when the spec lists `depmod`, modules.* for base+layer are regenerated into the layer so
                    `modprobe` finds the layer's modules and still finds the base's (phase 0, S6).
* timestamps        every mtime is clamped to SOURCE_DATE_EPOCH so the packed erofs is reproducible.
"""
import argparse
import fnmatch
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

# Targets a layer must not order before or hang off. Layers merge in the main boot after these.
EARLY_TARGETS = {
    "local-fs.target", "local-fs-pre.target", "sysinit.target", "basic.target", "systemd-sysext.service",
    "systemd-tmpfiles-setup.service", "systemd-sysusers.service", "cryptsetup.target", "swap.target",
}
UNIT_SUFFIXES = (".service", ".socket", ".target", ".timer", ".path", ".mount", ".automount", ".swap", ".slice")
# Debian's maintainer scripts and dpkg write these; none can live in a sysext (and none are payload).
ETC_DATABASES = {"passwd", "group", "shadow", "gshadow", "passwd-", "group-", "shadow-", "gshadow-",
                 "subuid", "subgid", "ld.so.cache", "machine-id", "alternatives", "mailcap",
                 "hostid"}  # hostid is per host: a shared factory copy would give every host the same ZFS host id
NAME = re.compile(r"^nex-[a-z0-9]+(-[a-z0-9]+)*$")


class Failure(Exception):
    pass


def rel(root, path):
    return str(path.relative_to(root))


def walk_files(root):
    """Every non-directory entry as a path relative to root, sorted."""
    out = []
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for f in sorted(files):
            out.append(Path(base, f))
    return out


def parse_passwd(path):
    out = {}
    if path.is_file():
        for line in path.read_text().splitlines():
            p = line.split(":")
            if len(p) >= 7 and p[0]:
                out[p[0]] = p
    return out


def parse_group(path):
    out = {}
    if path.is_file():
        for line in path.read_text().splitlines():
            p = line.split(":")
            if len(p) >= 3 and p[0]:
                out[p[0]] = p
    return out


def sysusers(layer, base):
    """sysusers.d text for accounts the layer added; None when it added none."""
    users, groups = parse_passwd(layer / "etc/passwd"), parse_group(layer / "etc/group")
    base_users, base_groups = parse_passwd(base / "etc/passwd"), parse_group(base / "etc/group")
    gid_name = {g[2]: n for n, g in groups.items()}
    gid_name.update({g[2]: n for n, g in base_groups.items()})
    lines = []
    for name, g in sorted(groups.items()):
        if name in base_groups:
            if base_groups[name][2] != g[2]:
                raise Failure(f"group {name} gid {g[2]} differs from the base's {base_groups[name][2]}")
            continue
        lines.append(f"g {name} {g[2]}")
    for name, u in sorted(users.items()):
        if name in base_users:
            if base_users[name][2] != u[2]:
                raise Failure(f"user {name} uid {u[2]} differs from the base's {base_users[name][2]}")
            continue
        gecos = u[4].replace('"', "'") or "-"
        home = u[5] or "-"
        shell = u[6]
        if shell and shell not in ("/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"):
            lines.append(f'# {name} has login shell {shell}; sysusers.d cannot set it')
        gname = gid_name.get(u[3])
        spec = f"{u[2]}:{gname}" if gname else u[2]
        lines.append(f'u {name} {spec} "{gecos}" {home}')
    for name, g in sorted(groups.items()):
        if name in base_groups:
            continue
        for member in [m for m in g[3].split(",") if m]:
            lines.append(f"m {member} {name}")
    return "\n".join(lines) + "\n" if lines else None


def read_status(path):
    """dpkg status file -> [(package, version, arch)] of installed packages."""
    out, cur = [], {}
    for block in path.read_text().split("\n\n"):
        cur = {}
        for line in block.splitlines():
            if line[:1] in " \t" or ":" not in line:
                continue
            k, v = line.split(":", 1)
            cur[k] = v.strip()
        if cur.get("Package") and "installed" in cur.get("Status", "").split():
            out.append((cur["Package"], cur.get("Version", ""), cur.get("Architecture", "")))
    return sorted(out)


def parse_unit(path, dropins):
    """Effective values of one unit: dict section/key -> list, with an empty assignment resetting the list."""
    values = {}
    for f in [path] + dropins:
        section = ""
        for raw in f.read_text(errors="replace").splitlines():
            line = raw.strip()
            if not line or line[0] in "#;":
                continue
            if line.startswith("[") and line.endswith("]"):
                section = line[1:-1]
                continue
            if "=" not in line:
                continue
            k, v = line.split("=", 1)
            key = (section, k.strip())
            if v.strip() == "":
                values[key] = []
            else:
                values.setdefault(key, []).extend(v.split())
    return values


def unit_order_problems(layer):
    problems = []
    for sysdir in ("usr/lib/systemd/system", "usr/lib/systemd/user"):
        root = layer / sysdir
        if not root.is_dir():
            continue
        for unit in sorted(p for p in root.iterdir() if p.name.endswith(UNIT_SUFFIXES) and (p.is_file() or p.is_symlink())):
            if unit.is_symlink() and not unit.resolve().is_file():
                continue  # alias or mask; the target is checked by its own name
            if unit.suffix in (".mount", ".automount"):
                problems.append(f"{rel(layer, unit)}: mount units order before local-fs.target; remove it or mount from Core")
                continue
            dropdir = root / (unit.name + ".d")
            dropins = sorted(dropdir.glob("*.conf")) if dropdir.is_dir() else []
            vals = parse_unit(unit if unit.is_file() else unit.resolve(), dropins)
            for sect, key, what in (("Unit", "Before", "orders before"), ("Install", "WantedBy", "is wanted by"),
                                    ("Install", "RequiredBy", "is required by"), ("Install", "UpheldBy", "is upheld by")):
                bad = sorted(set(vals.get((sect, key), [])) & EARLY_TARGETS)
                if bad:
                    problems.append(f"{rel(layer, unit)} {what} {', '.join(bad)}; reset it with a drop-in ({unit.name}.d/*.conf)")
        # Preset-style enablement links created by the packages (wants/requires/upholds directories) count as WantedBy.
        for wants in sorted(root.glob("*.wants")) + sorted(root.glob("*.requires")) + sorted(root.glob("*.upholds")):
            target = wants.name.rsplit(".", 1)[0]
            if target in EARLY_TARGETS and any(wants.iterdir()):
                problems.append(f"{rel(layer, wants)}: units hang off {target}")
    return problems


def drop_enablement(layer):
    """Remove the enablement links maintainer scripts created. Core enables a layer's units (units_enable);
    a link left in /etc/systemd/system would start a stack at boot that the operator never asked for."""
    dropped = []
    for pat in ("etc/rc[0-6S].d",):
        for p in sorted(layer.glob(pat)):
            dropped.append(rel(layer, p))
            shutil.rmtree(p)
    sysdir = layer / "etc/systemd/system"
    if sysdir.is_dir():
        for p in sorted(sysdir.rglob("*"), key=lambda x: -len(x.parts)):
            if p.is_symlink():
                target = os.readlink(p)
                if target.startswith(("/usr/lib/systemd/", "/lib/systemd/", "/dev/null")):
                    dropped.append(rel(layer, p))
                    p.unlink()
            elif p.is_dir() and not any(p.iterdir()):
                p.rmdir()
        left = [rel(layer, p) for p in sorted(sysdir.rglob("*")) if not p.is_dir()]
        if left:
            raise Failure("files under /etc/systemd/system are not read from a layer's factory copy: " + ", ".join(left))
        if not any(sysdir.iterdir()):
            sysdir.rmdir()
        sd = layer / "etc/systemd"
        if sd.is_dir() and not any(sd.iterdir()):
            sd.rmdir()
    return dropped


def move_etc(layer, name):
    """etc/** -> usr/share/factory/etc/** plus a tmpfiles.d C+ rule per top-level payload entry."""
    etc = layer / "etc"
    if not etc.exists():
        return [], []
    moved, dropped = [], drop_enablement(layer)
    for top in sorted(etc.iterdir()):
        if top.name in ETC_DATABASES:
            dropped.append("etc/" + top.name)
            if top.is_dir() and not top.is_symlink():
                shutil.rmtree(top)
            else:
                top.unlink()
            continue
        dest = layer / "usr/share/factory/etc" / top.name
        dest.parent.mkdir(parents=True, exist_ok=True)
        if dest.exists() or dest.is_symlink():
            raise Failure(f"usr/share/factory/etc/{top.name} already exists in the layer")
        shutil.move(str(top), str(dest))
        moved.append("/etc/" + top.name)
    # Debian ships symlinks into /etc/alternatives and /etc/systemd units; those would dangle in factory.
    shutil.rmtree(etc)
    return moved, dropped


def clamp(root, epoch):
    for base, dirs, files in os.walk(root, topdown=False):
        for n in files + dirs:
            p = os.path.join(base, n)
            os.utime(p, (epoch, epoch), follow_symlinks=False)
    os.utime(root, (epoch, epoch))


def regen_modules(layer, base, spec):
    """depmod over base+layer; only the index files are kept in the layer."""
    mods = layer / "usr/lib/modules"
    if not mods.is_dir():
        raise Failure("spec requests depmod but the layer has no usr/lib/modules")
    kvers = sorted(p.name for p in mods.iterdir() if p.is_dir())
    if len(kvers) != 1:
        raise Failure(f"layer carries modules for {len(kvers)} kernels {kvers}; exactly one is allowed")
    kver = kvers[0]
    if not (base / "usr/lib/modules" / kver).is_dir():
        raise Failure(f"layer modules are for kernel {kver}, which the base does not carry")
    with tempfile.TemporaryDirectory(prefix="nex-depmod-") as tmp:
        tree = Path(tmp)
        dest = tree / "usr/lib/modules" / kver
        dest.parent.mkdir(parents=True)
        # A reflink copy keeps this fast where the file system has them and leaves the base untouched (modules are only read by depmod).
        subprocess.run(["cp", "-a", "--reflink=auto", str(base / "usr/lib/modules" / kver), str(dest)], check=True)
        for f in walk_files(mods / kver):
            if f.name.startswith("modules."):
                continue
            target = dest / f.relative_to(mods / kver)
            target.parent.mkdir(parents=True, exist_ok=True)
            if target.exists():
                target.unlink()
            shutil.copy2(f, target)
        # depmod -b wants <basedir>/lib/modules; a usr-merged tree needs the compat link.
        os.symlink("usr/lib", tree / "lib")
        subprocess.run(["depmod", "-b", str(tree), kver], check=True, stdout=subprocess.DEVNULL)
        for f in sorted(dest.glob("modules.*")):
            if f.name in ("modules.order", "modules.builtin", "modules.builtin.modinfo", "modules.builtin.bin",
                          "modules.builtin.alias.bin"):
                continue  # kernel build products of the base; depmod does not generate them
            shutil.copy2(f, mods / kver / f.name)
    return kver


def finalize(layer, base, spec, build_id, epoch, report):
    name = spec["name"]
    if not NAME.match(name):
        raise Failure(f"invalid layer name {name!r}")
    if not build_id:
        raise Failure("a build id is required")
    if not layer.is_dir() or not base.is_dir():
        raise Failure("layer tree and base tree must be directories")

    # 1. Package list, then remove dpkg and package-manager state.
    status = layer / "var/lib/dpkg/status"
    pkgs = []
    if status.is_file():
        base_status = base / "var/lib/dpkg/status"
        known = {p for p in read_status(base_status)} if base_status.is_file() else set()
        pkgs = [p for p in read_status(status) if p not in known]
    elif (layer / "usr/share/doc").is_dir():
        # mkosi's overlay output carries no dpkg database; every installed package still leaves its
        # copyright file (the build keeps usr/share/doc/*/copyright), which names it. Versions are unknown.
        pkgs = [(d.name, "-", "-") for d in sorted((layer / "usr/share/doc").iterdir()) if (d / "copyright").exists()]
    for stale in ("var/lib/dpkg", "var/cache", "var/log", "var/lib/apt", "var/lib/systemd", "run", "tmp"):
        p = layer / stale
        if p.is_symlink() or p.is_file():
            p.unlink()
        elif p.is_dir():
            shutil.rmtree(p)
    # var/lib/dpkg may be where the base ships its own database; an overlay upper can contain only differences.

    # 2. Accounts.
    users = sysusers(layer, base)
    if users:
        d = layer / "usr/lib/sysusers.d"
        d.mkdir(parents=True, exist_ok=True)
        (d / f"{name}.conf").write_text("# Accounts the packages of this layer created (from the install chroot).\n" + users)

    # 3. /etc.
    moved, dropped = move_etc(layer, name)
    if moved:
        d = layer / "usr/lib/tmpfiles.d"
        d.mkdir(parents=True, exist_ok=True)
        (d / f"{name}-etc.conf").write_text(
            "# Package defaults of this layer; merged in only where the operator has no file of that name.\n"
            + "".join(f"C+ {m} - - - -\n" for m in sorted(moved)))

    # 4. Anything outside /usr and /opt.
    allow_var = spec.get("allow_var", [])
    stray = []
    for f in walk_files(layer):
        r = rel(layer, f)
        top = r.split("/", 1)[0]
        if top in ("usr", "opt"):
            continue
        if not any(fnmatch.fnmatch(r, g) for g in allow_var):
            stray.append(r)
    # directories that remain empty are harmless; only files and symlinks carry state.
    if stray:
        raise Failure("unclassified files outside /usr and /opt (add to allow_var or fix the install): " + ", ".join(stray[:20]))
    for top in sorted(layer.iterdir()):
        if top.name not in ("usr", "opt") and top.is_dir() and not top.is_symlink() and not walk_files(top):
            shutil.rmtree(top)

    # 5. Collisions with the base.
    allow = spec.get("allow_collisions", [])
    collide = []
    for f in walk_files(layer):
        r = rel(layer, f)
        b = base / r
        if (b.is_file() or b.is_symlink()) and not any(fnmatch.fnmatch(r, g) for g in allow):
            if r.startswith("usr/lib/modules/") and Path(r).name.startswith("modules."):
                continue  # regenerated below
            collide.append(r)
    if collide:
        raise Failure("layer files shadow base files: " + ", ".join(collide[:20]) + (f" (+{len(collide) - 20} more)" if len(collide) > 20 else ""))

    # 6. Boot ordering.
    problems = unit_order_problems(layer)
    if problems:
        raise Failure("unit ordering: " + "; ".join(problems))

    # 7. Identity files.
    rd = layer / "usr/lib/extension-release.d"
    rd.mkdir(parents=True, exist_ok=True)
    for old in rd.iterdir():
        old.unlink()
    (rd / f"extension-release.{name}").write_text(f"ID=nex-core\nSYSEXT_LEVEL={build_id}\n")
    meta = {
        "name": name,
        "requires_base": build_id,
        "units_enable": spec.get("units_enable", []),
        "units_never_enable": spec.get("units_never_enable", []),
        "provides": spec.get("provides", []),
        "modules": spec.get("modules", []),
        "busy_units": spec.get("busy_units", []),
        "busy_commands": spec.get("busy_commands", []),
    }
    md = layer / "usr/lib/nex/layers"
    md.mkdir(parents=True, exist_ok=True)
    (md / f"{name}.json").write_text(json.dumps(meta, indent=2, sort_keys=True) + "\n")
    pl = layer / "usr/share/nex/layers"
    pl.mkdir(parents=True, exist_ok=True)
    (pl / f"{name}.packages").write_text("".join(f"{p} {v} {a}\n" for p, v, a in pkgs))

    # 8. Kernel module indexes.
    kver = regen_modules(layer, base, spec) if spec.get("depmod") else None

    # 9. Reproducible timestamps.
    clamp(layer, epoch)

    result = {"layer": name, "build_id": build_id, "packages": [p for p, _, _ in pkgs], "etc_to_factory": sorted(moved),
              "etc_dropped": sorted(dropped), "sysusers": bool(users), "kernel": kver}
    if report:
        Path(report).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    return result


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--layer-tree", required=True, type=Path)
    ap.add_argument("--base-tree", required=True, type=Path)
    ap.add_argument("--spec", required=True, type=Path)
    ap.add_argument("--build-id", required=True)
    ap.add_argument("--source-date-epoch", type=int, default=int(os.environ.get("SOURCE_DATE_EPOCH", "1790000000")))
    ap.add_argument("--report", type=Path)
    a = ap.parse_args(argv)
    try:
        spec = json.loads(a.spec.read_text())
        result = finalize(a.layer_tree, a.base_tree, spec, a.build_id, a.source_date_epoch, a.report)
    except Failure as e:
        print(f"layer-finalize: {e}", file=sys.stderr)
        return 1
    print(f"layer-finalize: {result['layer']} ok ({len(result['packages'])} packages)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
