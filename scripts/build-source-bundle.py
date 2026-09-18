#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build the retained source bundle for one exact Nex ISO."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile


def digest(path):
    value = hashlib.sha256()
    with Path(path).open('rb') as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def source_name_version(field, binary_version, package=None):
    """Return Debian source name/version from a binary package Source field."""
    field = field.strip()
    if not field:
        # Debian omits Source when it is the binary package name. A binNMU suffix
        # is not part of the source package version.
        if not package:
            return None, None
        return package, binary_version.split('+b', 1)[0]
    if ' (' not in field:
        return field, binary_version
    name, version = field[:-1].split(' (', 1)
    return name, version


def deb_component(path, origin, display_path=None):
    fields = subprocess.check_output(
        ['dpkg-deb', '-f', str(path), 'Package', 'Version', 'Architecture', 'Source'], text=True).splitlines()
    package, version, arch, source = (fields + [''] * 4)[:4]
    # dpkg-deb prints labels when the final requested field is absent.
    package = package.removeprefix('Package: ')
    version = version.removeprefix('Version: ')
    arch = arch.removeprefix('Architecture: ')
    source = source.removeprefix('Source: ')
    if source.startswith('Package: '):
        source = ''
    source_package, source_version = source_name_version(source, version, package)
    return {'kind': 'deb', 'path': display_path or path.name, 'origin': origin, 'package': package,
            'version': version, 'architecture': arch, 'sha256': digest(path),
            'source_package': source_package, 'source_version': source_version}


def deb_member(path, suffix):
    """Read one member without unpacking a potentially large package."""
    process = subprocess.Popen(['dpkg-deb', '--fsys-tarfile', str(path)], stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL)
    matched = False
    try:
        with tarfile.open(fileobj=process.stdout, mode='r|*') as archive:
            for member in archive:
                if member.name.endswith(suffix):
                    handle = archive.extractfile(member)
                    matched = True
                    return handle.read() if handle else None
    finally:
        if process.stdout:
            process.stdout.close()
        # Closing after a matching member intentionally gives dpkg-deb SIGPIPE.
        if process.wait() not in (0, -13, 141) and not matched:
            raise RuntimeError(f'cannot read {path}')
    return None


def iso_files(root):
    return [{'path': str(path.relative_to(root)), 'sha256': digest(path), 'size': path.stat().st_size}
            for path in sorted(root.rglob('*')) if path.is_file()]


def components_for_tree(repository, tree):
    debs = sorted({*repository.glob('*.deb'), *tree.rglob('*.deb'), *tree.rglob('*.udeb')})
    components = []
    for path in debs:
        added = path.parent == repository
        display_path = str(Path('nex/repo') / path.name) if added else str(path.relative_to(tree))
        components.append(deb_component(
            path, 'Nex ISO repository' if added else 'Debian installer media', display_path))
    for deb, component in zip(debs, components):
        if component['package'] != 'nex-core-local':
            continue
        contents = deb_member(deb, '/usr/share/nex/appliance/components.tsv')
        if not contents:
            continue
        for line in contents.decode().splitlines():
            fields = line.split('\t')
            if fields[0] == 'package':
                continue
            if len(fields) == 5:
                package, version, arch, source_package, source_version = fields
            else:
                package, version, arch, source = fields
                source_package, source_version = source_name_version(source, version, package)
            components.append({'kind': 'package-record', 'path': 'nex/appliance/components.tsv', 'origin': 'Nex appliance qcow2',
                               'package': package, 'version': version, 'architecture': arch,
                               'sha256': None, 'source_package': source_package, 'source_version': source_version})
    return debs, components


def retained_dsc(directory, source, version):
    """Return a complete matching Debian source descriptor, if one is retained."""
    for dsc in Path(directory).rglob('*.dsc'):
        fields = {}
        key = None
        for line in dsc.read_text(errors='replace').splitlines():
            if line == '-----BEGIN PGP SIGNATURE-----':
                break
            if line[:1].isspace() and key:
                fields[key] += '\n' + line.strip()
            elif ':' in line:
                key, value = line.split(':', 1)
                fields[key] = value.lstrip()
        if fields.get('Source') != source or fields.get('Version') != version:
            continue
        checksums = [row for row in fields.get('Checksums-Sha256', '').splitlines() if row]
        if not checksums:
            continue
        for row in checksums:
            expected, size, name = row.split(maxsplit=2)
            artifact = dsc.parent / name
            if not artifact.is_file() or artifact.stat().st_size != int(size) or digest(artifact) != expected:
                break
        else:
            return dsc
    return None


def fetch_sources(sources, destination, apt_sources, batch_size=25):
    options = ['-o', 'Acquire::Retries=3', '-o', 'Acquire::https::Timeout=30']
    if apt_sources:
        options += ['-o', f'Dir::Etc::sourcelist={apt_sources}', '-o', 'Dir::Etc::sourceparts=-',
                    '-o', 'Acquire::Check-Valid-Until=false']
    for offset in range(0, len(sources), batch_size):
        batch = sources[offset:offset + batch_size]
        command = ['apt-get', '-y', *options, 'source', '--download-only',
                   *[f'{source}={version}' for source, version in batch]]
        subprocess.run(command, cwd=destination, check=True)


def update_sources(apt_sources):
    if not apt_sources:
        return
    subprocess.run(['apt-get', 'update', '-o', f'Dir::Etc::sourcelist={apt_sources}',
                    '-o', 'Dir::Etc::sourceparts=-',
                    '-o', 'Acquire::Check-Valid-Until=false'], check=True)


def archive_directory(source, output):
    def public_metadata(info):
        info.uid = info.gid = 0
        info.uname = info.gname = ''
        info.pax_headers = {}
        return info
    with tarfile.open(output, 'w:gz') as archive:
        for path in sorted(source.rglob('*')):
            archive.add(path, arcname=str(Path('nex-iso-source') / path.relative_to(source)),
                        recursive=False, filter=public_metadata)


def archive_notices(source_bundle):
    with tarfile.open(source_bundle, 'r:*') as archive:
        return sorted(member.name.removeprefix('nex-iso-source/') for member in archive.getmembers()
                      if member.isfile() and member.name.startswith('nex-iso-source/notices/'))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--iso', required=True, type=Path)
    parser.add_argument('--repository', required=True, type=Path,
                        help='directory containing every added binary .deb')
    parser.add_argument('--tree', type=Path,
                        help='assembled ISO tree; inventories base firmware and installer files before mastering')
    parser.add_argument('--version', required=True)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--compliance-manifest', type=Path,
                        help='write final ISO/source-bundle binding after mastering')
    parser.add_argument('--source-bundle', type=Path)
    parser.add_argument('--apt-sources', type=Path, help='pinned deb-src list for Debian source retrieval')
    parser.add_argument('--source-input', action='append', default=[], type=Path,
                        help='already retained source tree/archive, repeated for rebuilt packages')
    args = parser.parse_args(argv)
    if not args.iso.is_file() or not args.repository.is_dir():
        raise SystemExit('ISO and repository must exist')
    if not args.output and not args.compliance_manifest:
        raise SystemExit('provide --output or --compliance-manifest')
    if args.compliance_manifest:
        if not args.tree or not args.source_bundle or not args.source_bundle.is_file():
            raise SystemExit('--compliance-manifest needs --tree and an existing --source-bundle')
        _, components = components_for_tree(args.repository, args.tree)
        data = {'version': args.version,
                'iso': {'file': args.iso.name, 'sha256': digest(args.iso)},
                'source_bundle': {'file': args.source_bundle.name, 'sha256': digest(args.source_bundle)},
                'components': components,
                'notices': ['nex/docs/LICENSE', 'nex/docs/NOTICE', 'nex/docs/LICENSING.md', 'nex/docs/ARTWORK-LICENSE.md'] +
                           archive_notices(args.source_bundle)}
        args.compliance_manifest.parent.mkdir(parents=True, exist_ok=True)
        args.compliance_manifest.write_text(json.dumps(data, indent=2, sort_keys=True) + '\n')
        return
    if args.tree is None and shutil.which('xorriso') is None:
        raise SystemExit('xorriso is required to inventory the ISO')

    with tempfile.TemporaryDirectory(prefix='nex-iso-source-') as temporary:
        temporary = Path(temporary)
        extracted = args.tree or temporary / 'iso'
        if args.tree is None:
            subprocess.run(['xorriso', '-osirrox', 'on', '-indev', str(args.iso), '-extract', '/', str(extracted)], check=True,
                           stdout=subprocess.DEVNULL)
        debs, components = components_for_tree(args.repository, extracted)
        manifest = {'version': args.version,
                    'input_iso': {'file': args.iso.name, 'sha256': digest(args.iso)},
                    'staged_files': iso_files(extracted), 'components': components,
                    'notice_inputs': [p.name for p in args.source_input]}
        bundle = temporary / 'bundle'
        (bundle / 'sources').mkdir(parents=True)
        for source_input in args.source_input:
            if source_input.is_dir():
                shutil.copytree(source_input, bundle / 'provided-sources' / source_input.name, dirs_exist_ok=True)
            else:
                target = bundle / 'provided-sources' / source_input.name
                target.parent.mkdir(exist_ok=True)
                shutil.copy2(source_input, target)
        notices = bundle / 'notices'
        notices.mkdir()
        components_by_digest = {row['sha256']: row for row in components if row['kind'] == 'deb'}
        for deb in debs:
            component = components_by_digest[digest(deb)]
            if component['package'].startswith('nex-'):
                continue
            copyright_notice = deb_member(deb, '/copyright')
            if copyright_notice:
                (notices / f"{component['package']}_{component['version']}.copyright").write_bytes(copyright_notice)
        needed = sorted({(row['source_package'], row['source_version']) for row in components
                         if row['source_package'] and not row['package'].startswith('nex-')})
        fetched = bundle / 'debian-sources'
        fetched.mkdir()
        update_sources(args.apt_sources)
        provided = bundle / 'provided-sources'
        missing = [(source, version) for source, version in needed
                   if not provided.exists() or not retained_dsc(provided, source, version)]
        if missing:
            fetch_sources(missing, fetched, args.apt_sources)
        incomplete = [(source, version) for source, version in needed
                      if not retained_dsc(provided, source, version)
                      and not retained_dsc(fetched, source, version)]
        if incomplete:
            raise RuntimeError(f'incomplete retained sources: {incomplete}')
        manifest['retained_source_files'] = iso_files(bundle)
        (bundle / 'COMPONENTS.json').write_text(json.dumps(manifest, indent=2, sort_keys=True) + '\n')
        (bundle / 'SOURCE-DELIVERY.txt').write_text(
            'Distribute this exact archive beside the ISO. Verify SHA256SUMS before use.\n'
            'The Debian source directory retains exact source inputs for every binary and udeb on the media;\n'
            'component notices and the recorded firmware/artwork review accompany the release.\n'
            'When compressed nex/media/virtio-win.iso.gz is on the media, provided sources retain\n'
            'the matching Fedora virtio-win source RPM and nested corresponding source.\n')
        virtio_media = extracted / 'nex' / 'media'
        if (virtio_media / 'virtio-win.iso.gz').is_file() or (virtio_media / 'virtio-win.iso').is_file():
            virtio_stage = provided / 'third-party' / 'virtio-win'
            virtio_root = provided / 'virtio-win-retained-source'
            virtio_src = virtio_stage / 'source' / 'virtio-win-0.1.302-1.src.rpm'
            virtio_notice = virtio_stage / 'NOTICE'
            if not virtio_src.is_file() or not virtio_notice.is_file():
                raise RuntimeError(
                    'virtio-win.iso is staged without retained Fedora corresponding source and NOTICE')
            virtio_source = virtio_root / 'source' if virtio_root.is_dir() else virtio_stage / 'source'
            for required in (
                    'virtio-win-prewhql-0.1-302-sources.zip',
                    'mingw-qemu-ga-win-110.2.3-2.el10.src.rpm',
                    'spice-vdagent-win-0.10.0-5-sources.zip',
                    'spice-qxl-wddm-dod-0.21-2-sources.zip',
                    'virtio-win-guest-tools-installer-0.1.302.tar.gz',
                    'winfsp-master-sources.zip'):
                if not (virtio_source / required).is_file():
                    raise RuntimeError(f'virtio-win corresponding source missing {required}')
            if not (virtio_source / 'mingw-qemu-ga-win').is_dir():
                raise RuntimeError('virtio-win nested qemu-ga source missing')
        checksums = ''.join(f'{digest(path)}  {path.relative_to(bundle)}\n' for path in sorted(bundle.rglob('*')) if path.is_file())
        (bundle / 'SHA256SUMS').write_text(checksums)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        archive_directory(bundle, args.output)
        args.output.with_suffix(args.output.suffix + '.sha256').write_text(f'{digest(args.output)}  {args.output.name}\n')


if __name__ == '__main__':
    main()
