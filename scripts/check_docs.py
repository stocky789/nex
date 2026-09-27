#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Check guide structure and explicit documentation decisions; not prose correctness."""
import argparse
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
from urllib.parse import unquote, urlsplit

SECTIONS = ('Status', 'Prerequisites', 'Steps', 'Expected result', 'Troubleshooting', 'Verification')


def git(root, *args, input=None):
    return subprocess.run(['git', '-C', str(root), *args], input=input,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True).stdout


def local_path(root, value):
    if not isinstance(value, str) or not value or value.startswith('/'):
        raise ValueError(f'invalid repository path: {value!r}')
    path = PurePosixPath(value)
    if '..' in path.parts or str(path) != value or '\\' in value:
        raise ValueError(f'path must be an exact repository-relative path: {value!r}')
    resolved = (root / value).resolve()
    if not resolved.is_relative_to(root.resolve()):
        raise ValueError(f'path escapes repository: {value!r}')
    return resolved


def markdown(text):
    # Guide authoring contract uses inline Markdown links; code examples are ignored.
    text = re.sub(r'^(`{3,}|~{3,}).*?^\1\s*$', '', text, flags=re.M | re.S)
    return re.sub(r'`[^`\n]*`', '', text)


def links(root, page):
    text = markdown(page.read_text())
    if re.search(r'^\s*\[[^]\n]+\]:|\][ \t]*\[', text, re.M):
        raise ValueError(f'{page.relative_to(root)}: use inline Markdown links, not reference links')
    targets = set()
    for raw in re.findall(r'!?\[[^\]\n]*\]\(([^)\n]+)\)', text):
        raw = raw.strip()
        if not raw:
            raise ValueError(f'{page.relative_to(root)}: empty link target')
        target = raw[1:raw.index('>')] if raw.startswith('<') and '>' in raw else raw.split()[0]
        url = urlsplit(target)
        if url.scheme or url.netloc:
            continue
        if url.path.startswith('/'):
            raise ValueError(f'{page.relative_to(root)}: use relative links: {target}')
        path = (page.parent / unquote(url.path)).resolve() if url.path else page.resolve()
        if not path.is_relative_to(root.resolve()) or not path.exists():
            raise ValueError(f'{page.relative_to(root)}: broken local link: {target}')
        if url.fragment and path.suffix == '.md':
            headings = re.findall(r'^#{1,6}\s+(.+?)\s*#*$', markdown(path.read_text()), re.M)
            anchors, counts = set(), {}
            for heading in headings:
                anchor = re.sub(r'[^\w\- ]', '', heading.lower()).replace(' ', '-')
                count = counts.get(anchor, 0)
                counts[anchor] = count + 1
                anchors.add(f'{anchor}-{count}' if count else anchor)
            if unquote(url.fragment) not in anchors:
                raise ValueError(f'{page.relative_to(root)}: broken heading link: {target}')
        targets.add(path)
    return targets


def check_guide(root):
    index = root / 'docs/guide/README.md'
    if not index.is_file():
        raise ValueError('missing docs/guide/README.md')
    pages = set((root / 'docs/guide').rglob('*.md'))
    navigation = links(root, index)
    for page in pages:
        text = markdown(page.read_text())
        if not re.match(r'^# [^\n]+', text.lstrip()) or len(re.findall(r'^# ', text, re.M)) != 1:
            raise ValueError(f'{page.relative_to(root)}: missing opening H1 title')
        if page != index:
            if page.resolve() not in navigation:
                raise ValueError(f'{page.relative_to(root)}: missing from guide navigation')
            headings = re.findall(r'^## ([^\n]+)', text, re.M)
            if [h for h in headings if h in SECTIONS] != list(SECTIONS):
                raise ValueError(f'{page.relative_to(root)}: required sections must appear once in order')
            for section in SECTIONS:
                match = re.search(r'^## ' + re.escape(section) + r'\s*\n(.*?)(?=^## |\Z)', text, re.M | re.S)
                if not match or not match.group(1).strip():
                    raise ValueError(f'{page.relative_to(root)}: missing/empty {section} section')
        links(root, page)


def nonempty(value, label):
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f'{label}: nonempty text required')


def path_list(root, values, label, exists=False):
    if not isinstance(values, list):
        raise ValueError(f'{label}: list required')
    for value in values:
        path = local_path(root, value)
        if exists and not path.is_file():
            raise ValueError(f'{label}: file does not exist: {value}')
    if len(set(values)) != len(values):
        raise ValueError(f'{label}: duplicate paths')
    return set(values)


# Where each assessment is allowed to point. The roadmap area is the published milestone
# board: the website is how the project states what is built, so a completed feature that
# never reaches it leaves the site claiming something untrue. Declaring the impact is what
# the gate can enforce; whether "none" was honest is a reviewer's judgement, exactly as it
# is for guide and API impact.
AREAS = {
    'guide': lambda p: p.startswith('docs/guide/') and p.endswith('.md'),
    'api': lambda p: p == 'docs/API.md' or p.startswith('docs/api/'),
    'roadmap': lambda p: p.startswith('website/src/routes/roadmap/'),
}

# Records written before the roadmap assessment existed are not retrofitted. Inventing
# assessments for a hundred merged changes - and for the batch still pending in the tree -
# would be guesswork dressed as review. The date every record already carries in its name is
# the boundary, so the requirement starts cleanly and needs no migration commit. A record
# whose name does not carry a date must answer, otherwise skipping the date would skip the gate.
ROADMAP_REQUIRED_FROM = '2026-09-12'
DATED_RECORD = re.compile(r'docs/changes/(\d{4}-\d{2}-\d{2})-.+\.json')


def roadmap_required(relative):
    dated = DATED_RECORD.fullmatch(relative)
    return dated is None or dated.group(1) >= ROADMAP_REQUIRED_FROM

# The closed list of Controller-owned data (AGENTS.md, architecture invariant). A controller
# owner must name one of these in its record; anything else is a design defect, not a record.
CONTROLLER_EXCEPTIONS = ('sites', 'administrator accounts and roles', 'audit history', 'enrollment tokens',
                         'the Controller CA')
CONTROLLER_RECORD = re.compile(r'\b(sites?|administrators?|admins?|audit|enrollment|tokens?|controller ca|ca)\b', re.I)
POOL_RECOVERY_RECORD = re.compile(
    r'\b(?:backup|restore)[ /-](?:jobs?|definitions?|polic(?:y|ies)|schedules?|history|'
    r'catalog(?:ue)?|repositor(?:y|ies)|recovery points?|execution|verification results?|run history)\b', re.I)


def check_state(file, data):
    """Force each change to say where its durable state lives.

    Nex Controller is replaceable, so pool state belongs on the pool master; a feature that
    parks it in the Controller breaks the product model (AGENTS.md, architecture invariant).
    Prose alone did not stop that, so every record answers the question explicitly, merged
    ones included: the records are the reviewable map of where state lives, and a gap in it
    is a gap in the map.
    """
    state = data.get('state')
    if not isinstance(state, dict) or state.get('owner') not in ('none', 'core', 'controller'):
        raise ValueError(f'{file}: state.owner must be none, core, or controller')
    nonempty(state.get('reason'), 'state.reason')
    if state['owner'] == 'none':
        # An empty record is a hedge, not an answer: none means there is nothing to name.
        if 'record' in state:
            raise ValueError(f'{file}: state none must omit record')
        return
    # core: the host-side record and the path a fresh Controller re-adopts it by.
    # controller: which closed Controller-scoped exception it is, and its backup path.
    nonempty(state.get('record'), 'state.record')
    if state['owner'] == 'controller' and (
            POOL_RECOVERY_RECORD.search(state['record']) or not CONTROLLER_RECORD.search(state['record'])):
        raise ValueError(f'{file}: state.record for owner controller must name one of the closed '
                         'Controller-scoped exceptions: ' + ', '.join(CONTROLLER_EXCEPTIONS))


def check_record(root, file, changed=None, new=True):
    data = json.loads(file.read_text())
    if not isinstance(data, dict):
        raise ValueError(f'{file}: object required')
    nonempty(data.get('summary'), 'summary')
    covered = path_list(root, data.get('paths'), 'paths')
    if not covered:
        raise ValueError(f'{file}: paths cannot be empty')
    for kind in ('guide', 'api', 'roadmap'):
        decision = data.get(kind)
        if kind == 'roadmap' and decision is None and not roadmap_required(
                file.relative_to(root).as_posix()):
            continue
        allowed_impacts = ('updated', 'none')
        if kind == 'api':
            # "updated" remains valid only for tracked historical records. New records
            # must use the architecture contract vocabulary in AGENTS.md.
            allowed_impacts = ('none', 'existing contract', 'changed contract', 'new contract')
            if not new:
                allowed_impacts = ('updated',) + allowed_impacts
        if not isinstance(decision, dict) or decision.get('impact') not in allowed_impacts:
            expected = 'updated or none' if kind != 'api' else \
                ('updated, none, existing contract, changed contract, or new contract'
                 if not new else 'none, existing contract, changed contract, or new contract')
            raise ValueError(f'{file}: {kind}.impact must be {expected}')
        nonempty(decision.get('reason'), f'{kind}.reason')
        # An absent pages key reads as "no pages", which is what impact none means.
        # Requiring the empty list added nothing but a confusing "list required" error.
        pages = path_list(root, decision.get('pages') or [], f'{kind}.pages', exists=changed is not None)
        allowed = AREAS[kind]
        if any(not allowed(p) for p in pages):
            raise ValueError(f'{file}: {kind}.pages outside reference area')
        if decision['impact'] != 'none':
            if not pages:
                raise ValueError(f'{file}: {kind} {decision["impact"]} requires at least one page')
            # Only a record introduced by this range has to prove its pages changed
            # alongside it. A record added earlier already did so in the push that
            # introduced it; re-checking it against a later diff would demand editing
            # pages that have nothing to do with the later change, so any edit to a
            # merged record - fixing a reason, adding a missed path - would fail until
            # its pages were touched again for no reason.
            if new and changed is not None and not pages <= changed:
                raise ValueError(f'{file}: {kind} {decision["impact"]} requires pages changed in this range')
        elif pages:
            raise ValueError(f'{file}: {kind} none requires empty pages')
    check_state(file, data)
    verification = data.get('verification')
    if not isinstance(verification, dict):
        raise ValueError(f'{file}: verification object required')
    nonempty(verification.get('method'), 'verification.method')
    evidence = path_list(root, verification.get('evidence'), 'verification.evidence', exists=changed is not None)
    if not evidence:
        raise ValueError(f'{file}: verification evidence required')
    return covered


def changed_paths(root, base, head, merge_base=False):
    if not re.fullmatch(r'[0-9a-fA-F]{40}|[0-9a-fA-F]{64}', head):
        raise ValueError('head must be a full commit SHA')
    actual = git(root, 'rev-parse', 'HEAD').decode().strip()
    resolved = git(root, 'rev-parse', '--verify', f'{head}^{{commit}}').decode().strip()
    if actual != resolved:
        raise ValueError('checkout must be at --head before checking documentation')
    if git(root, 'status', '--porcelain', '--untracked-files=all'):
        raise ValueError('range checks require a clean worktree; use --all for uncommitted structure checks')
    if base and set(base) == {'0'} and len(base) in (40, 64):
        if merge_base:
            raise ValueError('PR base cannot be zero')
        base = git(root, 'hash-object', '-t', 'tree', '--stdin', input=b'').decode().strip()
    else:
        if not re.fullmatch(r'[0-9a-fA-F]{40}|[0-9a-fA-F]{64}', base):
            raise ValueError('base must be a full commit SHA (all zeros for first push)')
        base = git(root, 'rev-parse', '--verify', f'{base}^{{commit}}').decode().strip()
        if merge_base:
            base = git(root, 'merge-base', base, head).decode().strip()
    # Disable rename detection: both removed and added paths need assessment.
    return name_status(git(root, 'diff', '--no-renames', '--name-status', '-z', base, head).decode())


def name_status(output):
    """Split a -z --name-status diff into (all paths, paths this range adds)."""
    fields = [field for field in output.split('\0') if field]
    changed, added = set(), set()
    for status, path in zip(fields[::2], fields[1::2]):
        changed.add(path)
        if status.startswith('A'):
            added.add(path)
    return changed, added


def worktree_paths(root):
    """Uncommitted and untracked paths for checking changed guide pages and records."""
    output = git(root, 'status', '--porcelain', '-z', '--untracked-files=all',
                 '--no-renames').decode()
    changed, added = set(), set()
    for entry in filter(None, output.split('\0')):
        status, path = entry[:2], entry[3:]
        if not path:
            continue
        changed.add(path)
        # Untracked, or added to the index: not present in the last commit.
        if status == '??' or status.startswith('A'):
            added.add(path)
    return changed, added


def check(root, changed=None, added=None):
    check_guide(root)
    record_paths = set() if changed is None else {p for p in changed if re.fullmatch(r'docs/changes/[^/]+\.json', p)}
    records = sorted((root / 'docs/changes').glob('*.json')) if changed is None else [
        root / p for p in sorted(record_paths) if (root / p).is_file()]
    for record in records:
        relative = record.relative_to(root).as_posix()
        # added=None means the caller could not tell new records from edited ones.
        # Treat every record as new, so an unknown caller gets the strict check.
        check_record(root, record, changed, new=added is None or relative in added)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--all', action='store_true', help='validate current guide and records')
    parser.add_argument('--worktree', action='store_true',
                        help='check uncommitted and untracked work; run this before committing')
    parser.add_argument('--base')
    parser.add_argument('--head')
    parser.add_argument('--merge-base', action='store_true', help='compare PR head with its common ancestor with base')
    args = parser.parse_args()
    selected = sum((args.all, args.worktree, bool(args.base or args.head)))
    if selected != 1:
        parser.error('use exactly one of --all, --worktree, or --base SHA --head SHA [--merge-base]')
    if (args.base or args.head) and not (args.base and args.head):
        parser.error('--base and --head are both required')
    if args.merge_base and not args.base:
        parser.error('--merge-base applies to --base/--head')
    try:
        root = Path.cwd().resolve()
        added = frozenset()
        if args.all:
            changed = None
        elif args.worktree:
            changed, added = worktree_paths(root)
        else:
            changed, added = changed_paths(root, args.base, args.head, args.merge_base)
        check(root, changed, added)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f'Documentation check failed: {error}', file=sys.stderr)
        return 1
    if args.all:
        print('Documentation structure passed; historical references not resolved (--all).')
    elif not changed:
        print('No uncommitted or untracked changes to assess.')
    else:
        scope = 'uncommitted' if args.worktree else 'changed'
        print(f'Documentation structure and supplied impact records passed for {len(changed)} {scope} paths; '
              'semantic review still required.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
