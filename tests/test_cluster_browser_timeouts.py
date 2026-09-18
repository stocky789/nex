# SPDX-License-Identifier: AGPL-3.0-only
"""Keep standard browser deadlines for storage CI actions and navigation."""
import ast
from pathlib import Path
import unittest


class ClusterBrowserTimeoutTests(unittest.TestCase):
    def test_actions_and_navigation_keep_standard_budget_before_first_visit(self):
        for name in ('cluster_storage_browser.py', 'storage_repositories_browser.py'):
            with self.subTest(harness=name):
                self.check_browser_budgets(Path(__file__).parent / 'integration' / name)

    def check_browser_budgets(self, source):
        tree = ast.parse(source.read_text())
        calls = sorted(
            (node for node in ast.walk(tree)
             if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
             and isinstance(node.func.value, ast.Name) and node.func.value.id == 'page'),
            key=lambda node: (node.lineno, node.col_offset),
        )
        action_timeout, navigation_timeout = 30000, None
        for call in calls:
            if call.func.attr == 'set_default_timeout':
                action_timeout = ast.literal_eval(call.args[0])
            elif call.func.attr == 'set_default_navigation_timeout':
                navigation_timeout = ast.literal_eval(call.args[0])
            elif call.func.attr == 'goto':
                # Navigation inherits the action timeout unless explicitly separated.
                effective = navigation_timeout if navigation_timeout is not None else action_timeout
                self.assertEqual(effective, 30000, 'Slow initial asset loading needs the normal navigation budget')
                self.assertEqual(action_timeout, 30000, 'Input dispatch needs the normal action budget on shared CI')
                return
        self.fail('No initial page navigation found')


if __name__ == '__main__':
    unittest.main()
