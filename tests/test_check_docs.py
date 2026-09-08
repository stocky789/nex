# SPDX-License-Identifier: AGPL-3.0-only
"""Run with python3 -m unittest discover -s tests -p test_check_docs.py -v."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('check_docs', Path(__file__).resolve().parents[1] / 'scripts/check_docs.py')
docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs)


class DocumentationGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.write('docs/guide/README.md', '# Guide\n\n[Install](install.md)\n')
        self.write('docs/guide/install.md', '# Install\n\n' + '\n'.join(f'## {s}\n\nSpecific instructions.\n' for s in docs.SECTIONS))
        self.write('docs/API.md', '# API\n\nNo API yet.\n')
        self.write('check.txt', 'Local check evidence\n')
        self.write('app.py', 'print("first")\n')

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def record(self, paths, name='change', guide=None, api=None):
        data = {'summary': 'Test change', 'paths': paths,
                'guide': guide or {'impact': 'none', 'reason': 'Internal change', 'pages': []},
                'api': api or {'impact': 'none', 'reason': 'No API behavior changes', 'pages': []},
                'verification': {'method': 'Run focused local check', 'evidence': ['check.txt']}}
        file = f'docs/changes/{name}.json'
        self.write(file, json.dumps(data))
        return file

    def git(self, *args):
        return docs.git(self.root, *args).decode().strip()

    def init(self):
        self.git('init', '-q')
        self.git('config', 'user.email', 'test@example.invalid')
        self.git('config', 'user.name', 'Test')
        return self.commit()

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def test_missing_impact_record_fails(self):
        with self.assertRaisesRegex(ValueError, 'missing documentation assessment'):
            docs.check(self.root, {'app.py'})

    def test_updated_guide_and_api_must_change_in_range(self):
        record = self.record(['app.py'], guide={'impact': 'updated', 'reason': 'New workflow', 'pages': ['docs/guide/install.md']},
                             api={'impact': 'updated', 'reason': 'Changed contract', 'pages': ['docs/API.md']})
        with self.assertRaisesRegex(ValueError, 'guide updated requires'):
            docs.check(self.root, {'app.py', record})
        with self.assertRaisesRegex(ValueError, 'api updated requires'):
            docs.check(self.root, {'app.py', record, 'docs/guide/install.md'})
        self.record(['app.py', 'docs/guide/install.md', 'docs/API.md'],
                    guide={'impact': 'updated', 'reason': 'New workflow', 'pages': ['docs/guide/install.md']},
                    api={'impact': 'updated', 'reason': 'Changed contract', 'pages': ['docs/API.md']})
        docs.check(self.root, {'app.py', record, 'docs/guide/install.md', 'docs/API.md'})

    def test_reasons_and_evidence_required(self):
        file = self.record(['app.py'], api={'impact': 'none', 'reason': '', 'pages': []})
        with self.assertRaisesRegex(ValueError, 'api.reason'):
            docs.check(self.root, {'app.py', file})
        self.record(['app.py'])
        (self.root / 'check.txt').unlink()
        with self.assertRaisesRegex(ValueError, 'evidence: file does not exist'):
            docs.check(self.root, {'app.py', file})

    def test_structure_navigation_links_and_anchors(self):
        page = self.root / 'docs/guide/install.md'
        original = page.read_text()
        for bad, message in [
            (original.replace('## Steps', '## Wrong'), 'required sections'),
            (original + '\n[Bad](missing.md)', 'broken local link'),
            (original + '\n[Bad](install.md#absent)', 'broken heading link'),
            (original + '\n[Bad](../../../outside)', 'broken local link')]:
            page.write_text(bad)
            with self.assertRaisesRegex(ValueError, message):
                docs.check(self.root)
        page.write_text(original + '\n[Good](install.md#steps)\n[External](https://example.invalid/)\n')
        docs.check(self.root)
        self.write('docs/guide/README.md', '# Guide\n')
        with self.assertRaisesRegex(ValueError, 'missing from guide navigation'):
            docs.check(self.root)

    def test_single_title_and_section_order(self):
        page = self.root / 'docs/guide/install.md'
        original = page.read_text()
        page.write_text(original + '\n# Another title\n')
        with self.assertRaisesRegex(ValueError, 'H1 title'):
            docs.check(self.root)
        page.write_text(original.replace('## Status', '## TEMP').replace('## Steps', '## Status').replace('## TEMP', '## Steps'))
        with self.assertRaisesRegex(ValueError, 'once in order'):
            docs.check(self.root)

    def test_historical_record_can_reference_removed_page(self):
        file = self.record(['app.py'], guide={'impact': 'updated', 'reason': 'Former workflow', 'pages': ['docs/guide/removed.md']})
        docs.check(self.root)  # Tree mode checks historical schema, not stale references.
        with self.assertRaisesRegex(ValueError, 'file does not exist'):
            docs.check(self.root, {'app.py', file})

    def test_first_push_and_multiple_commits(self):
        initial = [str(p.relative_to(self.root)) for p in self.root.rglob('*') if p.is_file()]
        self.record(initial, 'initial')
        base = self.init()
        docs.check(self.root, docs.changed_paths(self.root, '0' * 40, base))
        self.write('app.py', 'print("second")\n')
        self.commit()
        self.record(['app.py'])
        head = self.commit()
        docs.check(self.root, docs.changed_paths(self.root, base, head))
        # A later unrelated record must not hide an uncovered change in the range.
        self.write('new.py', 'print("new")\n')
        head = self.commit()
        with self.assertRaisesRegex(ValueError, 'new.py'):
            docs.check(self.root, docs.changed_paths(self.root, base, head))

    def test_pr_merge_base_ignores_base_branch_only_change(self):
        base = self.init()
        self.git('checkout', '-qb', 'feature')
        self.write('app.py', 'print("feature")\n')
        self.commit()
        self.record(['app.py'])
        head = self.commit()
        self.git('checkout', '-qb', 'target', base)
        self.write('target-only.txt', 'not part of PR\n')
        target = self.commit()
        self.git('checkout', '-q', 'feature')
        changed = docs.changed_paths(self.root, target, head, merge_base=True)
        self.assertNotIn('target-only.txt', changed)
        docs.check(self.root, changed)

    def test_workflow_new_branch_checks_divergence_not_historical_records(self):
        self.record(['old.py'], 'historical', guide={'impact': 'updated', 'reason': 'Old feature removed', 'pages': ['docs/guide/removed.md']})
        source = Path(__file__).resolve().parents[1]
        self.write('scripts/check_docs.py', (source / 'scripts/check_docs.py').read_text())
        base = self.init()
        self.git('update-ref', 'refs/remotes/origin/main', base)
        self.git('checkout', '-qb', 'feature')
        self.write('app.py', 'new feature\n')
        self.commit()
        self.record(['app.py'])
        head = self.commit()
        env = dict(os.environ, DOCS_EVENT='push', DOCS_BEFORE='0' * 40,
                   DOCS_AFTER=head, DOCS_REF='refs/heads/feature', DOCS_DEFAULT_BRANCH='main')
        for host in ('.github', '.gitea'):
            workflow = (source / host / 'workflows/documentation.yml').read_text()
            # Last run block is plain shell; exercise the exact shipped selection logic.
            block = workflow.rsplit('        run: |\n', 1)[1]
            script = '\n'.join(line[10:] for line in block.splitlines())
            result = subprocess.run(['bash', '-e', '-c', script], cwd=self.root, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('2 changed paths', result.stdout)
            missing = subprocess.run(['bash', '-e', '-c', script], cwd=self.root,
                                     env=dict(env, DOCS_DEFAULT_BRANCH='missing'), capture_output=True, text=True)
            self.assertNotEqual(missing.returncode, 0)

    def test_missing_refs_and_wrong_checkout_fail(self):
        base = self.init()
        with self.assertRaises(subprocess.CalledProcessError):
            docs.changed_paths(self.root, '1' * 40, base)
        self.write('app.py', 'changed\n')
        self.commit()
        with self.assertRaisesRegex(ValueError, 'checkout must be at'):
            docs.changed_paths(self.root, base, base)

    def test_range_rejects_uncommitted_docs_but_ignores_cache(self):
        self.write('.gitignore', '.cache/\n')
        head = self.init()
        self.write('.cache/local.txt', 'ignored\n')
        docs.changed_paths(self.root, head, head)
        self.write('docs/guide/install.md', 'uncommitted change\n')
        with self.assertRaisesRegex(ValueError, 'clean worktree'):
            docs.changed_paths(self.root, head, head)
        self.git('checkout', '--', 'docs/guide/install.md')
        self.write('untracked.md', 'not in commit\n')
        with self.assertRaisesRegex(ValueError, 'clean worktree'):
            docs.changed_paths(self.root, head, head)

    def test_record_directory_is_not_a_coverage_escape(self):
        for path in ('docs/changes/helper.py', 'docs/changes/nested/record.json'):
            with self.assertRaisesRegex(ValueError, 'missing documentation assessment'):
                docs.check(self.root, {path})

    def test_deleted_path_requires_coverage(self):
        file = self.record(['deleted.py'])
        docs.check(self.root, {'deleted.py', file})
        with self.assertRaisesRegex(ValueError, 'missing documentation assessment'):
            docs.check(self.root, {'deleted.py'})


if __name__ == '__main__':
    unittest.main()
