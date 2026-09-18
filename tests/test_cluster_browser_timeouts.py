# SPDX-License-Identifier: AGPL-3.0-only
"""Keep browser navigation independent of the clustered-storage action deadline."""
import ast
from pathlib import Path
import unittest


class ClusterBrowserTimeoutTests(unittest.TestCase):
    def test_navigation_keeps_full_load_budget_before_first_visit(self):
        source = Path(__file__).parent / 'integration/cluster_storage_browser.py'
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
                self.assertEqual(action_timeout, 3000, 'Keep interactive failure detection bounded')
                return
        self.fail('No initial page navigation found')


if __name__ == '__main__':
    unittest.main()
