#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Render the authoritative OpenAPI document without a network or dependencies."""
import argparse
from html import escape
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def render(contract, contract_name='openapi.json'):
    info = contract['info']
    title = f"{info['title']} — {info['version']}"
    parts = ['<!doctype html><html lang="en"><meta charset="utf-8">',
             '<meta name="viewport" content="width=device-width,initial-scale=1">',
             f'<title>{escape(title)}</title>',
             '<link rel="stylesheet" href="reference.css">',
             f'<main><h1>{escape(title)}</h1><p>{escape(info.get("description", ""))}</p>',
             f'<p><a href="{escape(contract_name)}">Download OpenAPI JSON</a>. This reference works offline; examples do not execute requests.</p>',
             '<nav aria-label="API operations"><ul>']
    operations = [(method, path, operation) for path, item in contract['paths'].items()
                  for method, operation in item.items() if method in ('get', 'post', 'put', 'patch', 'delete')]
    for method, path, operation in operations:
        parts.append(f'<li><a href="#{escape(operation["operationId"])}">{method.upper()} {escape(path)}</a></li>')
    parts.append('</ul></nav>')
    for group in ('x-commands', 'x-static-resources'):
        for name, contract_item in contract.get(group, {}).items():
            parts.append(f'<section><h2>{escape(name)}</h2><pre>{escape(json.dumps(contract_item, indent=2))}</pre></section>')
    for method, path, operation in operations:
        parts.extend([f'<section id="{escape(operation["operationId"])}"><h2>{method.upper()} <code>{escape(path)}</code></h2>',
                      f'<p>{escape(operation["summary"])}</p><p>{escape(operation["description"])}</p>',
                      '<details><summary>Complete operation contract (parameters, authentication, responses)</summary>',
                      f'<pre>{escape(json.dumps(operation, indent=2))}</pre></details></section>'])
    parts.append('<section id="schemas"><h2>Schemas and authentication</h2>')
    for group, components in contract['components'].items():
        parts.append(f'<h3>{escape(group)}</h3>')
        for name, schema in components.items():
            parts.append(f'<details id="{escape(name)}"><summary>{escape(name)}</summary><pre>{escape(json.dumps(schema, indent=2))}</pre></details>')
    parts.append('</section></main></html>\n')
    return '\n'.join(parts)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='Fail if committed HTML is stale')
    args = parser.parse_args()
    for source, target in [('openapi.json', 'index.html'), ('local-openapi.json', 'local.html'),
                           ('peer-openapi.json', 'peer.html')]:
        contract = json.loads((ROOT / 'docs/api' / source).read_text())
        output = ROOT / 'docs/api' / target
        rendered = render(contract, source)
        if args.check:
            if not output.exists() or output.read_text() != rendered:
                parser.exit(1, f'{target} is stale: run python3 scripts/generate_api.py\n')
            print(f'{target} is current')
        else:
            output.write_text(rendered)


if __name__ == '__main__':
    main()
