# SPDX-License-Identifier: AGPL-3.0-only
"""Native browser labs have no simulator control dictionary."""
import ast
from pathlib import Path
import unittest


class ManagedVMBrowserModeTests(unittest.TestCase):
    def test_simulator_controls_are_guarded_in_native_mode(self):
        source = Path(__file__).parent / 'integration/managed_vm_browser.py'
        tree = ast.parse(source.read_text())
        parents = {child: parent for parent in ast.walk(tree)
                   for child in ast.iter_child_nodes(parent)}
        guard = ast.dump(ast.parse("mode == 'simulated'", mode='eval').body)
        controls = [node for node in ast.walk(tree)
                    if isinstance(node, ast.Subscript)
                    and isinstance(node.value, ast.Name) and node.value.id == 'config'
                    and isinstance(node.slice, ast.Constant) and node.slice.value == 'control']
        self.assertTrue(controls, 'Keep simulator fault coverage in the browser acceptance')
        for control in controls:
            with self.subTest(line=control.lineno):
                node = control
                while node in parents:
                    parent = parents[node]
                    if (isinstance(parent, ast.If) and node in parent.body
                            and ast.dump(parent.test) == guard):
                        break
                    node = parent
                else:
                    self.fail('Native configuration has no control dictionary; guard simulator access')


if __name__ == '__main__':
    unittest.main()
