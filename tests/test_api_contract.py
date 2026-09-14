# SPDX-License-Identifier: AGPL-3.0-only
"""Offline contract/reference coverage; runtime semantics are tested by Go suites."""
import ast
import importlib.util
import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('generate_api', ROOT / 'scripts/generate_api.py')
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)


class APIContractTests(unittest.TestCase):
    contract_file = "openapi.json"
    reference_file = "index.html"
    source_dir = "internal/controller"

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api' / cls.contract_file).read_text())

    def test_references_and_operation_metadata(self):
        self.assertEqual(self.api['openapi'], '3.1.0')
        ids = []

        def visit(value):
            if isinstance(value, dict):
                if '$ref' in value:
                    target = self.api
                    self.assertTrue(value['$ref'].startswith('#/'))
                    for part in value['$ref'][2:].split('/'):
                        target = target[part]
                for child in value.values():
                    visit(child)
            elif isinstance(value, list):
                for child in value:
                    visit(child)

        visit(self.api)
        for path, methods in self.api['paths'].items():
            for method, operation in methods.items():
                ids.append(operation['operationId'])
                self.assertTrue(operation['summary'])
                self.assertTrue(operation['description'])
                self.assertIn('security', operation)
                self.assertIn('x-introduced-version', operation)
                self.assertIn('x-audience', operation)
                self.assertTrue(any(code.startswith('2') for code in operation['responses']))
                parameters = operation.get('parameters', [])
                self.assertEqual(set(re.findall(r'{(\w+)}', path)),
                                 {p['name'] for p in parameters if p['in'] == 'path'})
        self.assertEqual(len(ids), len(set(ids)))

    def test_all_runtime_api_routes_are_documented_and_implemented(self):
        source = '\n'.join(p.read_text() for p in (ROOT / self.source_dir).glob('*.go')
                           if not p.name.endswith('_test.go'))
        implemented = {(method.lower(), path) for method, path in
                       re.findall(r'"(GET|POST|PUT|PATCH|DELETE) (/[^" ]*)"', source)}
        documented = {(method, path) for path, methods in self.api['paths'].items()
                      for method in methods}
        self.assertTrue(implemented, 'No literal runtime routes found: update the coverage adapter if registration changes')
        self.assertEqual(documented, implemented)

    def test_session_cookie_and_lifetime_contract_matches_runtime(self):
        source = '\n'.join(p.read_text() for p in (ROOT / self.source_dir).glob('*.go')
                           if not p.name.endswith('_test.go'))
        scheme = self.api['components']['securitySchemes'][
            'AdminSession' if self.source_dir == 'internal/controller' else 'LocalSession']
        self.assertEqual(set(re.findall(r'\.Cookie\("([^"]+)"\)', source)), {scheme['name']})
        self.assertEqual(set(re.findall(r'http\.Cookie\{Name:\s*"([^"]+)"', source)), {scheme['name']})
        self.assertTrue(scheme['name'].startswith('__Host-'))
        policy = scheme['x-session-policy']
        shared = '\n'.join(p.read_text() for p in (ROOT / 'internal/websecurity').glob('*.go')
                           if not p.name.endswith('_test.go'))
        for constant, key in [('IdleLifetime', 'idle_timeout_seconds'),
                              ('AbsoluteLifetime', 'absolute_timeout_seconds')]:
            amount, unit = re.search(r'const ' + constant + r' = (\d+) \* time\.(\w+)', shared).groups()
            self.assertEqual(policy[key], int(amount) * {'Minute': 60, 'Hour': 3600}[unit])
        self.assertEqual(policy['maximum_sessions'],
                         int(re.search(r'const SessionLimit = (\d+)', shared)[1]))
        self.assertFalse(policy['legacy_cookies_accepted'])

    def test_protocol_fields_types_and_required_members_match(self):
        source = '\n'.join(p.read_text() for p in sorted((ROOT / 'internal/protocol').glob('*.go'))
                           if not p.name.endswith('_test.go'))
        structs = dict(re.findall(r'type (\w+) struct\s*{(.*?)}', source, re.S))
        schemas = self.api['components']['schemas']
        self.assertEqual(set(structs), {name for name, schema in schemas.items() if 'x-go-type' in schema})

        def check_type(go_type, schema):
            if go_type.startswith('*'):
                self.assertIn({'type': 'null'}, schema['anyOf'])
                check_type(go_type[1:], schema['anyOf'][0])
            elif go_type.startswith('[]'):
                self.assertEqual(schema['type'], ['array', 'null'])
                check_type(go_type[2:], schema['items'])
            elif go_type == 'time.Time':
                self.assertEqual(schema['type'], 'string')
                self.assertEqual(schema['format'], 'date-time')
            elif go_type in ('string', 'bool', 'int', 'int64', 'uint16', 'uint32', 'uint64'):
                self.assertEqual(schema['type'], {'bool': 'boolean', 'int': 'integer',
                                                 'int64': 'integer', 'uint16': 'integer',
                                                 'uint32': 'integer', 'uint64': 'integer'}.get(go_type, go_type))
                if go_type in ('uint16', 'uint32', 'uint64'):
                    self.assertGreaterEqual(schema['minimum'], 0)
            else:
                self.assertEqual(schema['$ref'], '#/components/schemas/' + go_type)

        for name, body in structs.items():
            fields = re.findall(r'\w+\s+([\w.*\[\]]+)\s+`json:"([^"]+)"`', body)
            schema = schemas[name]
            self.assertEqual(set(schema['properties']), {tag.split(',')[0] for _, tag in fields}, name)
            self.assertEqual(set(schema.get('x-go-emitted-fields', schema['required'])), {tag.split(',')[0] for _, tag in fields if 'omitempty' not in tag}, name)
            for go_type, tag in fields:
                check_type(go_type, schema['properties'][tag.split(',')[0]])

    def test_controller_request_fields_match(self):
        source = '\n'.join(p.read_text() for p in (ROOT / 'internal/controller').glob('*.go') if not p.name.endswith('_test.go'))
        schemas = self.api['components']['schemas']
        for handler, name in [('login', 'LoginRequest'), ('sites', 'CreateSiteRequest'),
                              ('token', 'TokenRequest'),
                              ('operation', 'OperationRequest'),
                              ('bootstrapAppliance', 'ApplianceBootstrapRequest')]:
            body = re.search(r'func\s*\(c \*Controller\)\s*' + handler + r'\(.*?(?=\nfunc |\Z)', source, re.S)
            self.assertIsNotNone(body, handler)
            request = re.search(r'var v struct\s*{(.*?)}', body[0], re.S)
            self.assertIsNotNone(request, handler)
            fields = set(re.findall(r'`json:"([^",]+)', request[1]))
            self.assertEqual(set(schemas[name]['properties']), fields, name)

    def test_generated_reference_is_current_and_offline(self):
        html = (ROOT / 'docs/api' / self.reference_file).read_text()
        self.assertEqual(html, generator.render(self.api, self.contract_file))
        self.assertNotIn('<script', html)
        self.assertNotRegex(html, r'(?:src|href)="https?://')
        self.assertIn('aria-label="API operations"', html)


    def test_appliance_dto_fields_types_and_required_members(self):
        self.check_dtos('internal/controller/appliance.go', {
            'applianceRequest': 'ApplianceAdoptionRequest', 'managedAppliance': 'ManagedAppliance'})

    def test_addhost_dto_fields_types_and_required_members(self):
        self.check_dtos('internal/controller/addhost.go', {
            'probeRequest': 'HostProbeRequest', 'probeResponse': 'HostProbeResponse',
            'addHostRequest': 'AddSiteHostRequest', 'addHostResponse': 'AddSiteHostResponse'})

    def check_dtos(self, path, names):
        source = (ROOT / path).read_text()
        structs = dict(re.findall(r'type (\w+) struct\s*{(.*?)}', source, re.S))
        for name, schema_name in names.items():
            fields = re.findall(r'\w+\s+([\w.*\[\]]+)\s+`json:"([^"]+)"`', structs[name])
            schema = self.api['components']['schemas'][schema_name]
            self.assertTrue(fields, name)
            self.assertEqual(set(schema['properties']), {tag.split(',')[0] for _, tag in fields}, name)
            self.assertEqual(set(schema.get('x-go-emitted-fields', schema['required'])),
                             {tag.split(',')[0] for _, tag in fields if 'omitempty' not in tag}, name)
            for go_type, tag in fields:
                prop = schema['properties'][tag.split(',')[0]]
                if go_type == '[]string':
                    self.assertEqual(prop['type'], ['array', 'null'])
                    self.assertEqual(prop['items']['type'], 'string')
                elif go_type == 'time.Time':
                    self.assertEqual(prop['type'], 'string')
                    self.assertEqual(prop['format'], 'date-time')
                elif go_type.startswith('*protocol.'):
                    self.assertEqual(prop['anyOf'], [
                        {'$ref': '#/components/schemas/' + go_type.removeprefix('*protocol.')},
                        {'type': 'null'}])
                else:
                    self.assertEqual(prop['type'], {'int': 'integer', 'bool': 'boolean',
                                                    'string': 'string'}[go_type])


class LocalAPIContractTests(APIContractTests):
    contract_file = 'local-openapi.json'
    reference_file = 'local.html'
    source_dir = 'internal/localcore'

    def test_service_cli_flags_are_documented(self):
        source = (ROOT / 'cmd/nex-core-local/main.go').read_text()
        flags = set(re.findall(r'flag\.\w+Var\([^,]+,\s*"([^"]+)"', source))
        documented = set(re.findall(r'`--?([a-z][a-z-]+)`', (ROOT / 'docs/API.md').read_text()))
        self.assertTrue(flags)
        self.assertFalse(flags - documented, f'Undocumented local service flags: {flags - documented}')

    def test_protocol_fields_types_and_required_members_match(self):
        # Local HTTP DTOs do not extend the outbound Core report protocol.
        self.check_dtos('internal/vmhost/engine.go', {
            'Operation': 'Operation', 'VM': 'VM', 'Preflight': 'Preflight'})

    def test_controller_request_fields_match(self):
        self.check_dtos('internal/localcore/service.go', {'DeployRequest': 'DeployRequest'})
        self.check_dtos('internal/localcore/service.go', {'DeployRequest': 'RecoveryPreparationRequest'})

    def test_enrollment_dto_fields_types_and_required_members(self):
        self.check_dtos('internal/localcore/enroll.go', {'enrollmentRequest': 'EnrollmentRequest'})

    def test_addhost_dto_fields_types_and_required_members(self):
        pass  # Host push (add-host) DTOs are controller-only; no local-API equivalent to check here.

    def test_deployment_network_and_password_contract(self):
        for name in ('DeployRequest', 'RecoveryPreparationRequest'):
            schema = self.api['components']['schemas'][name]
            self.assertEqual(schema['properties']['network_mode']['default'], 'static')
            self.assertEqual(set(schema['properties']['network_mode']['enum']), {'', 'static', 'dhcp'})
            branch = schema['allOf'][0]
            self.assertEqual(branch['if']['properties']['network_mode']['const'], 'dhcp')
            self.assertEqual(set(branch['else']['required']), {'address_cidr', 'gateway', 'dns'})
            self.assertEqual(branch['then']['properties']['address_cidr']['const'], '')
            self.assertEqual(branch['then']['properties']['gateway']['const'], '')
            self.assertEqual(branch['then']['properties']['dns']['maxItems'], 0)
            self.assertEqual(schema['properties']['admin_password']['minLength'], 1)
            self.assertEqual(schema['properties']['admin_password']['x-max-utf8-bytes'], 1024)
        self.assertNotIn('confirmation', self.api['components']['schemas']['PowerRequest']['required'])

    def test_appliance_dto_fields_types_and_required_members(self):
        source = (ROOT / 'internal/localcore/service.go').read_text()
        body = re.search(r'func\s*\(s \*Service\)\s*power\(.*?(?=\nfunc |\Z)', source, re.S)
        request = re.search(r'var v struct\s*{(.*?)}', body[0], re.S)
        self.assertEqual(set(re.findall(r'`json:"([^",]+)', request[1])),
                         set(self.api['components']['schemas']['PowerRequest']['properties']))


class PeerAPIContractTests(APIContractTests):
    contract_file = 'peer-openapi.json'
    reference_file = 'peer.html'
    source_dir = 'internal/agent'

    def go_source(self, *dirs):
        return '\n'.join(p.read_text() for d in dirs for p in sorted((ROOT / d).glob('*.go'))
                         if not p.name.endswith('_test.go'))

    def test_all_runtime_api_routes_are_documented_and_implemented(self):
        # Peer routes use prefix/suffix dispatch: path shapes come from handlers, methods from Core callers.
        source = self.go_source('internal/agent', 'internal/localcore')
        consts = dict(re.findall(r'const (\w+Path) = "([^"]+)"', source))
        handled = set()
        for line in source.splitlines():
            if 'r.URL.Path' not in line or '|| strings.HasPrefix(r.URL.Path' in line:
                continue  # Listener-level prefix dispatch forwards; the handler owns the route.
            handled |= {consts[c] + s + ('{}' if s.endswith('/') else '')
                        for c, s in re.findall(r'(\w+Path)\s*\+\s*"([\w/-]+)"', line)}
            handled |= {consts[c] + '{}' + s for c, s in
                        re.findall(r'TrimSuffix\(strings\.TrimPrefix\(r\.URL\.Path, (\w+Path)\), "([\w/-]+)"\)', line)}
            handled |= {consts[c] + '{}' for c in re.findall(r':= strings\.TrimPrefix\(r\.URL\.Path, (\w+Path)\)$', line)}
            handled |= {consts[c] for c in re.findall(r'r\.URL\.Path [!=]= (\w+Path)\b(?!\s*\+)', line)}
            handled |= {consts[c].rstrip('/') for c in re.findall(r'r\.URL\.Path [!=]= strings\.TrimSuffix\((\w+Path), "/"\)', line)}
            handled |= set(re.findall(r'r\.URL\.Path [!=]= "(/api/v1/pool/[^"]*)"', line))
        called = set()
        for method, expr in re.findall(r'NewRequestWithContext\(\w+, http\.Method(\w+), (.+?), (?:nil\)|bytes\.NewReader|strings\.NewReader)', source):
            if re.fullmatch(r'\w+', expr):
                expr = re.search(expr + r' := (.+)', source)[1]
            path = ''
            for token in re.sub(r'strings\.TrimRight\([\w.]+, "/"\)\s*\+', '', expr).split('+'):
                token = token.strip()
                if token.startswith('"'):
                    path += token.strip('"').split('?')[0]
                    if '?' in token:
                        break
                elif token in consts:
                    path += consts[token]
                elif m := re.fullmatch(r'strings\.TrimSuffix\((\w+), "/"\)', token):
                    path += consts[m[1]].rstrip('/')
                elif path:
                    path += '{}'  # Identifier segment; a leading origin variable adds nothing.
            if path.startswith('/api/v1/pool/'):
                called.add((method.lower(), path))
        documented = {(method, re.sub(r'{\w+}', '{}', path)) for path, methods in self.api['paths'].items()
                      for method in methods}
        self.assertTrue(handled and called, 'No peer routes found: update the coverage adapter if dispatch changes')
        self.assertEqual({path for _, path in documented}, handled)
        self.assertEqual(documented, called)

    def test_protocol_fields_types_and_required_members_match(self):
        # Protocol schemas are verbatim Controller-contract copies (checked against Go there);
        # peer DTOs are checked for member names and emitted fields.
        controller = json.loads((ROOT / 'docs/api/openapi.json').read_text())['components']['schemas']
        inline = {'PeerAdvertiseRequest': ('internal/agent/peer.go', r'var v struct')}
        for name, schema in self.api['components']['schemas'].items():
            if name in controller:
                self.assertEqual(schema, controller[name], name)
                continue
            if name in inline:
                path, pattern = inline[name]
                body = re.search(pattern + r'\s*{(.*?)}', (ROOT / path).read_text(), re.S)[1]
            else:
                package, go = schema['x-go-type'].split('.')
                body = re.search(r'type ' + go + r' struct\s*{(.*?)}\n', self.go_source('internal/' + package), re.S)[1]
            tags = re.findall(r'`json:"([^"]+)"`', body)
            if tags:
                fields = {tag.split(',')[0] for tag in tags}
                emitted = {tag.split(',')[0] for tag in tags if 'omitempty' not in tag}
            else:  # Untagged structs marshal with Go field names.
                fields = emitted = set(re.findall(r'(?<![.\w\[\]*])[A-Z]\w*', body))
            self.assertEqual(set(schema['properties']), fields, name)
            self.assertEqual(set(schema['required']), emitted, name)

    def test_session_cookie_and_lifetime_contract_matches_runtime(self):
        pass  # Peer routes authenticate with pool-CA mutual TLS, never cookies.

    def test_controller_request_fields_match(self):
        pass  # Peer request DTOs are covered by test_protocol_fields_types_and_required_members_match.

    def test_appliance_dto_fields_types_and_required_members(self):
        pass  # Appliance DTOs are not part of the peer contract.

    def test_addhost_dto_fields_types_and_required_members(self):
        pass  # Add-host DTOs are Controller-only.


class ApplianceReadinessContractTests(unittest.TestCase):
    def test_marker_producer_consumer_and_reference_match(self):
        api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        schema = api['components']['schemas']['ApplianceReadiness']
        source = (ROOT / 'appliance/firstboot.py').read_text()
        marker = ast.literal_eval(re.search(r'json.dumps\((\{[^}]+\})\)', source)[1])
        self.assertEqual(marker, {key: value['const'] for key, value in schema['properties'].items()})
        consumer = (ROOT / 'internal/localcore/handoff.go').read_text()
        fields = re.search(r'var marker struct\s*{(.*?)}', consumer, re.S)[1]
        self.assertEqual(set(re.findall(r'`json:"([^",]+)', fields)), set(schema['properties']))
        resource = '/appliance-ready.json'
        self.assertIn(resource, source)
        self.assertIn('"' + resource + '"', consumer)
        self.assertIn('GET ' + resource, api['x-static-resources'])


class ManagedVMContractTests(unittest.TestCase):
    def test_managed_request_fields_and_command_payloads_match_runtime(self):
        api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        schemas = api['components']['schemas']
        source = ((ROOT / 'internal/controller/managed.go').read_text() + '\n' +
                  (ROOT / 'internal/controller/storage.go').read_text())
        for name, struct_name in [('StorageRegistrationRequest', 'storageRegisterRequest'),
                                  ('NetworkRegistrationRequest', 'registrationRequest')]:
            registration = re.search(r'type ' + struct_name + r' struct\s*{(.*?)}', source, re.S)[1]
            self.assertEqual(set(schemas[name]['properties']),
                             set(re.findall(r'`json:"([^",]+)', registration)))
        request = re.search(r'type createVMRequest struct\s*{(.*?)}', source, re.S)[1]
        self.assertIn('protocol.VMCreateSpec', request)
        self.assertEqual(set(schemas['CreateVMRequest']['properties']),
                         set(schemas['VMCreateSpec']['properties']) |
                         set(re.findall(r'`json:"([^",]+)', request)))
        worker = (ROOT / 'internal/agent/managed.go').read_text()
        dispatch = re.search(r'func managedCommand\(.*?(?=\nfunc |\n//)', worker, re.S)[0]
        kinds = set(re.findall(r'"([a-z-]+)"', dispatch))
        # Core admits managed kinds plus the read-only kinds named beside managedCommand at admission.
        admitted = kinds | set(re.findall(r'cmd\.Kind != "([a-z-]+)" && !managedCommand\(cmd\.Kind\)',
                                          (ROOT / 'internal/agent/run.go').read_text()))
        self.assertGreater(len(admitted), len(kinds), 'Core command admission adapter found no read-only kinds')
        self.assertEqual(set(api['x-commands']), admitted)
        self.assertEqual(set(schemas['Command']['properties']['kind']['enum']), admitted)
        self.assertEqual(schemas['Command']['properties']['kind']['enum'],
                         schemas['Operation']['properties']['kind']['enum'])
        vm = schemas['ManagedAppliance']
        self.assertEqual(set(vm['properties']['role']['enum']), {'controller', 'guest'})
        self.assertNotIn('const', vm['properties']['protected'])
        self.assertTrue(vm['allOf'][0]['then']['properties']['protected']['const'])
        self.assertFalse(vm['allOf'][0]['else']['properties']['protected']['const'])
        engine = (ROOT / 'internal/vmhost/managed.go').read_text()
        shared = (ROOT / 'internal/protocol/managed.go').read_text()
        # Minimums and per-disk size come from the shared validator; the ceilings on vCPUs and
        # memory are the Controller's and are enforced identically by the host-side engine.
        for schema, field, pattern, body in [
                ('VMCPU', 'count', r'v\.CPU\.Count < (\d+)', shared),
                ('VMMemory', 'size_mib', r'v\.Memory\.SizeMiB < (\d+)', shared),
                ('VMDisk', 'size_gib', r'd\.SizeGiB < (\d+)', shared)]:
            self.assertEqual(schemas[schema]['properties'][field]['minimum'],
                             int(re.search(pattern, body)[1]))
        self.assertEqual(schemas['VMDisk']['properties']['size_gib']['maximum'],
                         int(re.search(r'd\.SizeGiB > (\d+)', shared)[1]))
        for schema, field, pattern in [('VMCPU', 'count', r'CPU\.Count > (\d+)'),
                                       ('VMMemory', 'size_mib', r'Memory\.SizeMiB > (\d+)')]:
            for body in (source, engine):
                self.assertEqual({int(m) for m in re.findall(pattern, body)},
                                 {schemas[schema]['properties'][field]['maximum']})
        for field, member in [('disks', 'Disks'), ('nics', 'NICs')]:
            upper = int(re.search(r'len\(v\.' + member + r'\)[^\n]*> (\d+)', shared)[1])
            for name in ('VMCreateSpec', 'CreateVMRequest'):
                self.assertEqual(schemas[name]['properties'][field]['maxItems'], upper)
        self.assertEqual(schemas['VMCreateSpec']['properties']['disks']['minItems'],
                         int(re.search(r'len\(v\.Disks\) < (\d+)', shared)[1]))


if __name__ == '__main__':
    unittest.main()
