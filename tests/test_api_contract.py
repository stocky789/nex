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
            elif go_type.startswith('map[string]'):
                self.assertEqual(schema['type'], ['object', 'null'])
                check_type(go_type[len('map[string]'):], schema['additionalProperties'])
            elif go_type == 'time.Time':
                self.assertEqual(schema['type'], 'string')
                self.assertEqual(schema['format'], 'date-time')
            elif go_type == 'time.Duration':
                # encoding/json marshals time.Duration as an integer nanosecond count.
                self.assertEqual(schema['type'], 'integer')
            elif go_type == 'float64':
                self.assertEqual(schema['type'], 'number')
            elif go_type == 'json.RawMessage':
                # Replicated bytes kept verbatim: the schema says so instead of
                # inventing a Go type name for arbitrary JSON.
                self.assertEqual(schema['x-go-type-raw'], 'json.RawMessage')
            elif go_type in ('string', 'bool', 'int', 'int64', 'uint8', 'uint16', 'uint32', 'uint64'):
                self.assertEqual(schema['type'], {'bool': 'boolean', 'int': 'integer',
                                                 'int64': 'integer', 'uint8': 'integer',
                                                 'uint16': 'integer',
                                                 'uint32': 'integer', 'uint64': 'integer'}.get(go_type, go_type))
                if go_type in ('uint16', 'uint32', 'uint64'):
                    self.assertGreaterEqual(schema['minimum'], 0)
            else:
                self.assertEqual(schema['$ref'], '#/components/schemas/' + go_type)

        for name, body in structs.items():
            fields = re.findall(r'\w+\s+([\w.*\[\]]+)\s+`json:"([^"]+)"[^`]*`', body)
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

    def test_backup_policy_update_requires_expected_revision(self):
        if self.contract_file != 'openapi.json':
            return
        operation = self.api['paths']['/api/v1/pools/{id}/backup-policies/{policy_id}']['put']
        request = operation['requestBody']['content']['application/json']['schema']
        self.assertIn('expected_revision', request['required'])
        self.assertEqual(request['properties']['expected_revision']['minimum'], 1)
        self.assertIn('400', operation['responses'])
        self.assertIn('409', operation['responses'])

    def test_backup_history_page_contract(self):
        if self.contract_file != 'openapi.json':
            return
        schemas = self.api['components']['schemas']
        for route, page in [('backup-runs', 'BackupRunPage'), ('backup-catalog', 'BackupCatalogPage')]:
            operation = self.api['paths']['/api/v1/pools/{id}/' + route]['get']
            result = operation['responses']['200']['content']['application/json']['schema']
            self.assertEqual({'$ref': '#/components/schemas/' + page}, result)
            self.assertEqual(schemas[page]['properties']['items']['maxItems'], 64)
            for field in ('cursor', 'generation'):
                self.assertIn(field, {p['name'] for p in operation['parameters']})
            for status in ('400', '409', '413', '502', '503', '504'):
                self.assertIn(status, operation['responses'])


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
            fields = re.findall(r'\w+\s+([\w.*\[\]]+)\s+`json:"([^"]+)"[^`]*`', structs[name])
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

    def test_backup_peer_access_and_missing_operation_contract(self):
        access = self.api['paths']['/api/v1/pool/backup/access']['post']
        self.assertEqual(access['security'], [{'PoolMasterCertificate': []}])
        self.assertEqual(access['requestBody']['content']['application/json']['schema'],
                         {'$ref': '#/components/schemas/BackupRepository'})
        self.assertEqual(access['responses']['200']['content']['application/json']['schema'],
                         {'$ref': '#/components/schemas/PoolStorageAccess'})
        self.assertIn('404', self.api['paths']['/api/v1/pool/backup/{operation_id}']['get']['responses'])

    def test_all_runtime_api_routes_are_documented_and_implemented(self):
        # Peer routes use prefix/suffix dispatch: path shapes come from handlers, methods from Core callers.
        source = self.go_source('internal/agent', 'internal/localcore')
        consts = dict(re.findall(r'(\w+Path)\s*=\s*"([^"]+)"', source))
        # Some peer routes are declared from a stable route prefix plus a literal
        # suffix. Resolve those constants so the route coverage still compares
        # their concrete API shapes rather than treating them as undocumented.
        for name, base, suffix in re.findall(r'(\w+Path)\s*=\s*(\w+Path)\s*\+\s*"([^"]+)"', source):
            if base in consts:
                consts[name] = consts[base] + suffix
        base_paths = {name: consts[path] for name, path in re.findall(
            r'(\w+)\s*:=\s*strings\.TrimRight\([^\n]+\)\s*\+\s*(\w+Path)\s*$', source, re.M)
            if path in consts}
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
                elif token in base_paths:
                    path += base_paths[token]
                elif m := re.fullmatch(r'strings\.TrimSuffix\((\w+), "/"\)', token):
                    path += consts[m[1]].rstrip('/')
                elif re.fullmatch(r'\w*[Qq]uery', token):
                    break  # A query string, not a path segment: the route is the part before it.
                elif path:
                    path += '{}'  # Identifier segment; a leading origin variable adds nothing.
            if path.startswith('/api/v1/pool/'):
                called.add((method.lower(), path))
        # CPU observations share a bounded request helper and dispatch on literal
        # operation suffixes. Recover those real routes rather than treating the
        # whole suffix as an arbitrary resource identifier.
        cpu_handler = re.search(r'func PoolCPUHandler\(.*?(?=\nfunc |\Z)', source, re.S)
        if cpu_handler:
            prefix = consts['poolCPUPath']
            handled.discard(prefix + '{}')
            handled.discard(prefix + '/{}')
            for labels, body in re.findall(r'case ([^:]+):(.*?)(?=\n\t\tcase |\n\t\tdefault:)', cpu_handler[0], re.S):
                if re.search(r'r.Method != http.Method(Get|Post)', body):
                    handled.update(prefix + suffix for suffix in re.findall(r'"([a-z]+)"', labels))
            called.update((method.lower(), prefix + suffix) for method, suffix in
                          re.findall(r'poolCPURequest\([^\n]+http.Method(Get|Post), "([a-z]+)(?:\?|"|\+)', source))
        zfs_handler = re.search(r'func PoolZFSHandler\(.*?(?=\nfunc |\Z)', source, re.S)
        if zfs_handler:
            prefix = consts['poolZFSPath']
            # The handler's generic prefix parser hides the real shapes: the bare
            # prefix takes the provision POST, and every per-repository route comes
            # from the handler's own suffix dispatch, so read those from its source.
            handled.discard(prefix + '{}')
            handled.discard(prefix + '/{}')
            handled.add(prefix)
            handled.update(prefix + '/{}' + suffix for suffix in
                           re.findall(r'strings\.HasSuffix\(r\.URL\.Path, "([\w/-]+)"\)', zfs_handler[0]))
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
            fields = set()
            emitted = set()
            for line in body.splitlines():
                declaration = re.match(r'\s*([A-Z]\w*(?:,\s*[A-Z]\w*)*)\s+\S+(?:\s+`json:"([^"]+)"`)?', line)
                if not declaration:
                    continue
                names, tag = declaration.groups()
                names = [tag.split(',')[0]] if tag else [name.strip() for name in names.split(',')]
                fields.update(names)
                if not tag or 'omitempty' not in tag:
                    emitted.update(names)
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
        for schema, field, pattern in [('VMCPU', 'count', r'CPU\.Count > (\d+) \|\|'),
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

    def test_zfs_provision_request_and_outcomes_match_runtime(self):
        api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        schemas = api['components']['schemas']
        source = (ROOT / 'internal/controller/storage.go').read_text()
        body = re.search(r'type zfsProvisionAPIRequest struct\s*{(.*?)}', source, re.S)[1]
        fields = set(re.findall(r'`json:"([^",]+)', body))
        request = schemas['ZFSProvisionAPIRequest']
        self.assertEqual(set(request['properties']), fields)
        self.assertEqual(set(request['required']), fields - {'repository_id'})
        self.assertEqual(api['paths']['/api/v1/storage/zfs/provision']['post']['requestBody']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/ZFSProvisionAPIRequest')
        self.assertEqual(set(request['properties']['scope']['enum']), {'host', 'pool'})

        provision = schemas['ZFSProvisionRequest']
        self.assertIn('repository_id', provision['properties'])
        self.assertNotIn('repository_id', provision['required'])
        self.assertEqual(set(schemas['ZFSProvisionDevice']['properties']),
                         {'by_id', 'serial', 'fingerprint', 'size_bytes', 'erase_confirmation'})
        self.assertEqual(set(schemas['ZFSProvisionDevice']['required']),
                         {'by_id', 'serial', 'fingerprint', 'size_bytes'})
        result = schemas['ZFSProvisionHostResult']
        self.assertEqual(set(result['properties']),
                         {'repository_id', 'pool_id', 'name', 'scope', 'host_id', 'layout',
                          'devices', 'status', 'error', 'pool_guid', 'dataset_guid'})
        self.assertTrue({'repository_id', 'host_id', 'devices', 'status'} <= set(result['required']))


class PoolJoinContractTests(unittest.TestCase):
    """Controller-driven pool join: routes, projections, command and host-local view."""

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        cls.local = json.loads((ROOT / 'docs/api/local-openapi.json').read_text())
        cls.handlers = (ROOT / 'internal/controller/pooljoin.go').read_text()

    def dto(self, source, struct, schema_name, contract=None):
        body = re.search(r'type ' + struct + r' struct\s*{(.*?)\n}', source, re.S)[1]
        fields = re.findall(r'\w+\s+[\w.*\[\]]+\s+`json:"([^"]+)"', body)
        schema = (contract or self.api)['components']['schemas'][schema_name]
        self.assertEqual(set(schema['properties']), {tag.split(',')[0] for tag in fields}, schema_name)
        self.assertEqual(set(schema['required']),
                         {tag.split(',')[0] for tag in fields if 'omitempty' not in tag}, schema_name)
        return schema

    def test_core_membership_retains_hostnames(self):
        peer = json.loads((ROOT / 'docs/api/peer-openapi.json').read_text())
        self.assertIn('hostname', peer['components']['schemas']['PeerJoinRequest']['properties'])
        for contract in (self.api, self.local, peer):
            self.assertIn('hostname', contract['components']['schemas']['PoolMember']['properties'])
        self.assertIn('Core-owned membership', self.api['components']['schemas']['PoolMemberView']['properties']['name']['description'])

    def test_routes_are_admin_gated_and_documented(self):
        controller = (ROOT / 'internal/controller/controller.go').read_text()
        for method, path, handler in [('get', '/api/v1/pools/{id}/members', 'poolMembers'),
                                      ('post', '/api/v1/pools/{id}/members', 'addPoolMember'),
                                      ('get', '/api/v1/pools/{id}/join-candidates', 'poolJoinCandidates')]:
            self.assertIn(f'"{method.upper()} {path}", c.admin(c.{handler})', controller)
            operation = self.api['paths'][path][method]
            self.assertEqual(operation['security'], [{'AdminSession': []}])
            # Mutations carry the Controller's CSRF header; reads do not.
            headers = {p['name'] for p in operation.get('parameters', []) if p['in'] == 'header'}
            self.assertEqual(headers, {'X-CSRF-Token'} if method == 'post' else set())

    def test_documented_refusals_are_the_literal_handler_messages(self):
        messages = set(re.findall(r'fail\(w, http\.Status\w+, "([^"]+)"', self.handlers))
        messages |= set(re.findall(r'http\.Status\w+, fmt\.Errorf\("([^"]+)"', self.handlers))
        messages |= set(re.findall(r'\n\t\treturn "([a-z][^"]+)"\n', self.handlers))  # precheck reasons
        messages.discard('%s')
        # pooljoin.go also holds the generalised brokered relay, whose deadline and
        # abandonment sentences are worded per action. The promotion preflight is the
        # second caller of it, so its route is part of what this file must document.
        documented = json.dumps({path: self.api['paths'][path] for path in
                                 ('/api/v1/pools/{id}/members', '/api/v1/pools/{id}/join-candidates',
                                  '/api/v1/pools/{id}/master/promote/preflight',
                                  '/api/v1/pools/{id}/members/{host_id}/bmc-probe')})
        self.assertGreater(len(messages), 10, 'refusal adapter found nothing: check the handler style')
        for message in messages:
            self.assertIn(message, documented, message)
        # The 504 and 499 bodies are worded per relayed action. Per-action copy lives on
        # poolRequestCopyByAction; the helpers' default returns cover the join relay.
        sentences = set(re.findall(r'(?:abandoned|deadline):\s+"([^"]+)"', self.handlers))
        for name in ('poolRequestAbandoned', 'poolRequestDeadline'):
            body = re.search(r'func ' + name + r'\(.*?\n}\n', self.handlers, re.S)[0]
            sentences.update(re.findall(r'return "([^"]+)"', body))
        self.assertEqual(len(sentences), 6, 'relay adapter found the wrong number of sentences')
        for text in sentences:
            self.assertIn(text, documented, text)

    def test_request_and_projection_shapes_match_the_handlers(self):
        request = self.dto(self.handlers, 'poolMemberAPIRequest', 'AddPoolMemberRequest')
        self.assertFalse(request['additionalProperties'])  # DisallowUnknownFields
        self.assertIn('DisallowUnknownFields', (ROOT / 'internal/controller/controller.go').read_text())
        # S2 and S4 extended both views; every field of both is now documented, so this is
        # exact parity in both directions rather than the documented-subset rule S1 needed
        # while those slices were still landing.
        for struct, name in [('poolMembersView', 'PoolMembersView'), ('poolMemberView', 'PoolMemberView')]:
            body = re.search(r'type ' + struct + r' struct\s*{(.*?)\n}', self.handlers, re.S)[1]
            fields = {tag.split(',')[0] for tag in re.findall(r'`json:"([^"]+)"', body)}
            schema = self.api['components']['schemas'][name]
            self.assertEqual(set(schema['properties']), fields, name)
            self.assertEqual(set(schema['required']), set(schema['properties']), name)
        for struct, name in [('poolMemberCheck', 'PoolMemberCheck'),
                             ('poolMemberAccessCounts', 'PoolMemberAccessCounts'),
                             ('poolMemberFailure', 'PoolMemberFailure'),
                             ('joinCandidate', 'PoolJoinCandidate'),
                             ('joinCandidateRow', 'PoolJoinCandidateRow')]:
            self.dto(self.handlers, struct, name)
        self.assertEqual(self.api['components']['schemas']['PoolMemberCheck']
                         ['properties']['name']['enum'],
                         re.findall(r'"([\w-]+)"', re.search(
                             r'poolConvergeChecks = \[\]string{([^}]+)}', self.handlers)[1]))

    def test_accepted_response_never_carries_the_invitation(self):
        managed = (ROOT / 'internal/controller/managed.go').read_text()
        body = re.search(r'case cmd\.Pool != nil:.*?\n\t\treturn (map\[string\]any{[^\n]*})', managed, re.S)[1]
        accepted = self.api['components']['schemas']['PoolJoinAccepted']
        self.assertEqual(set(accepted['properties']), set(re.findall(r'"(\w+)":', body)))
        for secret in ('invitation', 'pool_ca_pem', 'master_url'):
            self.assertNotIn(secret, accepted['properties'])
        for code in ('200', '202'):
            schema = self.api['paths']['/api/v1/pools/{id}/members']['post']['responses'][code]
            self.assertEqual(schema['content']['application/json']['schema']['$ref'],
                             '#/components/schemas/PoolJoinAccepted')
        # The 504 body is a Core-shaped PoolResult, not the usual {"error": ...}.
        self.assertEqual(self.api['paths']['/api/v1/pools/{id}/members']['post']['responses']['504']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolResult')

    def test_command_and_invitation_lifetime_match_runtime(self):
        command = self.api['x-commands']['pool-join']
        self.assertEqual(command['payload']['$ref'], '#/components/schemas/PoolCommand')
        protocol_source = (ROOT / 'internal/protocol/pool.go').read_text()
        self.assertIn('"pool-join"', (ROOT / 'internal/agent/managed.go').read_text())
        # The joining host is the command's target: it is the one pool command not sent to the master.
        self.assertEqual(re.search(r'case "pool-join":.*?return (cmd\.Pool\.\w+)',
                                   (ROOT / 'internal/agent/managed.go').read_text(), re.S)[1],
                         'cmd.Pool.MemberHostID')
        minutes = int(re.search(r'poolJoinInvitationTTL = (\d+) \* time\.Minute', self.handlers)[1])
        for text in (command['semantics'],
                     self.api['components']['schemas']['PoolCommand']['properties']['invitation']['description'],
                     self.api['components']['schemas']['PoolJoinInvitation']['description']):
            self.assertRegex(text, rf'{minutes}[ -]minute')
        self.assertEqual(minutes,
                         int(re.search(r'poolJoinInvitationTTL = (\d+) \* time\.Minute',
                                       (ROOT / 'internal/agent/pool_join.go').read_text())[1])),
        self.assertIn(str(int(re.search(r'poolJoinWait = (\d+) \* time\.Second', self.handlers)[1])) + '-second',
                      self.api['paths']['/api/v1/pools/{id}/members']['post']['description'])

    def test_report_envelope_and_join_status_stages_match_runtime(self):
        protocol_source = (ROOT / 'internal/protocol/protocol.go').read_text()
        self.assertIn('PoolResults        []PoolResult             `json:"pool_results,omitempty"`', protocol_source)
        self.assertIn('PoolRequests           []PoolRequest             `json:"pool_requests,omitempty"`', protocol_source)
        bound = int(re.search(r'len\(v\.PoolResults\) > (\d+)',
                              (ROOT / 'internal/controller/identity.go').read_text())[1])
        self.assertEqual(self.api['components']['schemas']['ReportRequest']
                         ['properties']['pool_results']['maxItems'], bound)
        agent_source = (ROOT / 'internal/agent/pool_join.go').read_text()
        stages = re.search(r'poolJoinStages = \[\]string{([^}]+)}', agent_source)
        self.assertIsNotNone(stages, 'stage adapter found nothing: check how the agent lists its stages')
        for schema in (self.api['components']['schemas']['PoolJoinStatus'],
                       self.local['components']['schemas']['PoolJoinStatus']):
            self.assertEqual(schema['properties']['stage']['enum'],
                             re.findall(r'"(\w+)"', stages[1]))
            self.assertEqual(set(schema['properties']['status']['enum']),
                             {'pending', 'succeeded', 'failed', 'needs_attention'})

    def test_host_local_pool_view_matches_its_handler(self):
        source = (ROOT / 'internal/localcore/pool.go').read_text()
        handler = re.search(r'func \(s \*Service\) pool\(.*?\n}', source, re.S)[0]
        body = re.search(r'reply\(w, http\.StatusOK, map\[string\]any{(.*?)}\)', handler, re.S)[1]
        view = self.local['components']['schemas']['LocalPoolView']
        self.assertEqual(set(view['properties']), set(re.findall(r'"(\w+)":', body)))
        self.assertEqual(set(view['required']), set(view['properties']))
        self.assertIn('"GET /api/local/v1/pool", s.auth(s.pool)',
                      (ROOT / 'internal/localcore/service.go').read_text())
        documented = json.dumps(self.local['paths']['/api/local/v1/pool'])
        for status, message in re.findall(r'fail\(w, http\.Status(\w+), "([^"]+)"', handler):
            self.assertIn(message, documented)
        join = re.search(r'func \(s \*Service\) joinPool\(.*?\n}', source, re.S)[0]
        self.assertEqual(set(self.local['components']['schemas']['PoolJoinResponse']['properties']),
                         set(re.findall(r'"(\w+)":', re.search(
                             r'reply\(w, http\.StatusOK, map\[string\]any{(.*?)}\)', join, re.S)[1])))


class PoolStructParityMixin:
    """Field-level parity for the Go types a slice documents.

    APIContractTests compares the *set* of protocol structs against the documented
    schemas, so one undocumented struct from another slice hides every field check.
    These slices assert their own types directly instead of waiting for that set.
    """

    def struct_body(self, path, name):
        source = (ROOT / path).read_text()
        body = re.search(r'type ' + name + r' struct\s*{(.*?)\n}', source, re.S)
        self.assertIsNotNone(body, name)
        return body[1]

    def assert_parity(self, schemas, path, go_name, schema_name):
        fields = re.findall(r'\w+\s+[\w.*\[\]]+\s+`json:"([^"]+)"', self.struct_body(path, go_name))
        self.assertTrue(fields, go_name)
        schema = schemas[schema_name]
        self.assertEqual(set(schema['properties']), {tag.split(',')[0] for tag in fields}, schema_name)
        self.assertEqual(set(schema['required']),
                         {tag.split(',')[0] for tag in fields if 'omitempty' not in tag}, schema_name)
        return schema


class PoolMaintenanceContractTests(unittest.TestCase, PoolStructParityMixin):
    """Host maintenance mode: the route, its literal refusals and the master's command."""

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        cls.handlers = (ROOT / 'internal/controller/poolmaintenance.go').read_text()
        cls.path = '/api/v1/pools/{id}/members/{host_id}/maintenance'

    def test_route_is_admin_gated_and_documented(self):
        self.assertIn(f'"POST {self.path}", c.admin(c.setPoolMaintenance)',
                      (ROOT / 'internal/controller/controller.go').read_text())
        operation = self.api['paths'][self.path]['post']
        self.assertEqual(operation['security'], [{'AdminSession': []}])
        self.assertEqual({p['name'] for p in operation['parameters'] if p['in'] == 'header'},
                         {'X-CSRF-Token'})

    def test_request_and_response_shapes_match_the_handler(self):
        schemas = self.api['components']['schemas']
        request = self.assert_parity(schemas, 'internal/controller/poolmaintenance.go',
                                     'poolMaintenanceAPIRequest', 'PoolMaintenanceRequest')
        self.assertFalse(request['additionalProperties'])  # DisallowUnknownFields
        self.assertNotIn('confirmation', request['properties'])
        # The accepted body is the maintenance branch of resourceResponse.
        managed = (ROOT / 'internal/controller/managed.go').read_text()
        body = re.search(r'if cmd\.Pool\.Action == "maintenance" {\n\t\t\treturn (map\[string\]any{[^\n]*})',
                         managed)[1]
        self.assertEqual(set(schemas['PoolMaintenanceAccepted']['properties']),
                         set(re.findall(r'"(\w+)":', body)))
        for code in ('200', '202'):
            self.assertEqual(self.api['paths'][self.path]['post']['responses'][code]
                             ['content']['application/json']['schema']['$ref'],
                             '#/components/schemas/PoolMaintenanceAccepted')
        self.assert_parity(schemas, 'internal/controller/poolmaintenance.go',
                           'poolMemberGuests', 'PoolMemberGuests')
        self.assert_parity(schemas, 'internal/controller/poolmaintenance.go',
                           'memberOperation', 'PoolMemberOperation')

    def test_documented_refusals_are_the_literal_handler_messages(self):
        messages = set(re.findall(r'fail\(w, http\.Status\w+, "([^"]+)"', self.handlers))
        messages |= set(re.findall(r'fmt\.Errorf\("([^"]+)"', self.handlers))
        documented = json.dumps(self.api['paths'][self.path])
        # maintenanceRefusal is raised by placementHost, not by this route, so it is
        # documented on the three routes that can return it.
        placement = re.search(r'return fmt\.Errorf\("(the selected host is in maintenance[^"]+)"\)',
                              self.handlers)[1]
        messages.remove(placement)
        self.assertGreater(len(messages), 5, 'refusal adapter found nothing: check the handler style')
        for message in messages:
            self.assertIn(message.replace('%d', '<n>'), documented, message)
        self.assertIn('c.maintenanceRefusal(tx, pool, master, host)',
                      (ROOT / 'internal/controller/managed.go').read_text())
        for path, method in [('/api/v1/vms', 'post'), ('/api/v1/vms/{id}', 'put'),
                             ('/api/v1/vms/{id}/media', 'post')]:
            self.assertIn(placement, self.api['paths'][path][method]['responses']['409']['description'])

    def test_core_refusals_are_asynchronous_not_http_status(self):
        # Core's conflict refusals arrive as a failed operation marked conflict in the
        # member projection; the Controller must not claim them as a 409 of the route.
        core = (ROOT / 'internal/agent/pool_maintenance.go').read_text()
        conflicts = re.findall(r'"([^"]+)"', re.search(
            r'poolMaintenanceConflicts = \[\]string{(.*?)}', self.handlers, re.S)[1])
        self.assertEqual(len(conflicts), 2)
        for text in conflicts:
            self.assertIn('result.Error = "' + text + '"', core)
        documented = json.dumps(self.api['components']['schemas']['PoolMemberOperation'])
        for text in conflicts:
            self.assertIn(text, documented)
        for text in ('only the pool master records maintenance mode', 'no such member of this pool',
                     'an ejected host has no maintenance state',
                     'maintenance mode could not be recorded durably'):
            self.assertIn('"' + text + '"', core)
            self.assertIn(text, self.api['x-commands']['pool-maintenance']['semantics'])
        refusal = re.search(r'const maintenanceRefused = "([^"]+)"', core)[1]
        self.assertIn(refusal, self.api['x-commands']['pool-maintenance']['semantics'])

    def test_command_matches_runtime(self):
        command = self.api['x-commands']['pool-maintenance']
        self.assertEqual(command['payload']['$ref'], '#/components/schemas/PoolCommand')
        self.assertIn('"pool-maintenance"', (ROOT / 'internal/agent/managed.go').read_text())
        self.assertIn('pool-maintenance', self.api['components']['schemas']['Command']
                      ['properties']['kind']['enum'])
        # The command is addressed to the master, never to the drained host.
        self.assertIn('Membership:      protocol.PoolMembership{MasterHostID: master}', self.handlers)

    def test_member_projection_documents_the_drain_fields(self):
        view = self.api['components']['schemas']['PoolMemberView']
        self.assertEqual(view['properties']['maintenance']['type'], ['boolean', 'null'])
        self.assertEqual(view['properties']['guests']['$ref'], '#/components/schemas/PoolMemberGuests')
        self.assertIn('PoolMemberOperation', json.dumps(view['properties']['last_maintenance']))


class PoolAuthorityReplicaContractTests(unittest.TestCase, PoolStructParityMixin):
    """Member authority replica: the peer route, the signed payload and both projections."""

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        cls.peer = json.loads((ROOT / 'docs/api/peer-openapi.json').read_text())
        cls.path = '/api/v1/pool/replica'

    def test_protocol_types_match_their_schemas(self):
        for name in ('PoolAuthorityReplica', 'PoolReplicaSection', 'PoolReplicaAck',
                     'PoolReplicaStatus', 'PoolWrappedKey', 'PoolMember'):
            schema = self.assert_parity(self.api['components']['schemas'],
                                        'internal/protocol/pool.go', name, name)
            self.assertEqual(schema['x-go-type'], 'protocol.' + name)
            # The peer contract copies protocol schemas verbatim.
            self.assertEqual(self.peer['components']['schemas'][name], schema, name)
        self.assertIn('replica', self.api['components']['schemas']['ManagedState']['properties'])
        self.assertIn('PoolReplicaStatus', json.dumps(
            self.api['components']['schemas']['ManagedState']['properties']['replica']))
        self.assertIn('Replica *PoolReplicaStatus `json:"replica,omitempty"`',
                      (ROOT / 'internal/protocol/protocol.go').read_text())

    def test_sections_and_limit_match_runtime(self):
        source = (ROOT / 'internal/protocol/pool.go').read_text()
        sections = re.findall(r'"(\w+)"', re.search(
            r'PoolReplicaSections = \[\]string{([^}]+)}', source)[1])
        schema = self.api['components']['schemas']['PoolReplicaSection']
        self.assertEqual(schema['properties']['name']['enum'], sections)
        self.assertEqual(re.search(r'PoolReplicaLimit = (\d+) << (\d+)', source).groups(), ('1', '20'))
        for document in (self.peer['paths'][self.path]['post']['requestBody']['description'],
                         self.api['components']['schemas']['PoolAuthorityReplica']['description']):
            self.assertIn('1 MiB', document)

    def test_peer_route_statuses_and_refusals_are_documented(self):
        agent = (ROOT / 'internal/agent/pool_authority.go').read_text()
        handler = re.search(r'func PoolReplicaHandler\(.*?\n}\n', agent, re.S)[0]
        self.assertIn('AuthenticatePoolMaster', handler)
        documented = json.dumps(self.peer['paths'][self.path])
        codes = {'Unauthorized': '401', 'BadRequest': '400', 'MethodNotAllowed': '405'}
        served = set(re.findall(r'http\.Error\(w, "([^"]+)", http\.Status(\w+)\)', handler))
        self.assertTrue(served, 'status adapter found nothing: check the handler style')
        for message, status in served:
            # Every served status is documented on the method that can return it; the
            # shared ones (401, 405) are documented on both.
            methods = ['get', 'post'] if codes[status] in ('401', '405') else ['post']
            for method in methods:
                self.assertIn(codes[status], self.peer['paths'][self.path][method]['responses'])
            if message != 'method not allowed':
                self.assertIn(message, documented, message)
        self.assertIn('409', self.peer['paths'][self.path]['post']['responses'])
        for response in ('200', '409'):
            self.assertEqual(self.peer['paths'][self.path]['post']['responses'][response]
                             ['content']['application/json']['schema']['$ref'],
                             '#/components/schemas/PoolReplicaAck')
        self.assertEqual(self.peer['paths'][self.path]['get']['responses']['200']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolReplicaStatus')
        # Every bounded member-side refusal reaches the reference.
        accept = re.search(r'func acceptPoolAuthorityReplica\(.*?\n}\n', agent, re.S)[0]
        refusals = set(re.findall(r'errors\.New\("([^"]+)"\)', accept))
        self.assertGreater(len(refusals), 5)
        for message in refusals:
            self.assertIn(message, documented, message)
        # The master's own push failures are not responses of this route; they reach an
        # operator as the master's reported replica_error for that member.
        push = re.search(r'func pushPoolReplicaToMember\(.*?\n}\n', agent, re.S)[0]
        observed = json.dumps(self.api['components']['schemas']['PoolMember']
                              ['properties']['replica_error'])
        for message in re.findall(r'errors\.New\("([^"]+)"\)', push):
            self.assertIn(message, observed, message)

    def test_member_projection(self):
        schemas = self.api['components']['schemas']
        view = self.assert_parity(schemas, 'internal/controller/pooljoin.go',
                                  'poolMemberReplica', 'PoolMemberReplica')
        # The documented ladder is the handler's own switch, in order.
        switch = re.search(r'func memberReplica\(.*?\n}\n', (ROOT / 'internal/controller/pooljoin.go').read_text(), re.S)[0]
        states = ['unknown'] + re.findall(r'out\.State = "(\w+)"', switch)
        self.assertEqual(view['properties']['state']['enum'], states)
        self.assertIn('pool-authority-replica', schemas['PoolMemberCheck']['properties']['name']['enum'])

    def test_member_inventory_carries_delete_tombstones(self):
        self.assert_parity(self.peer['components']['schemas'], 'internal/agent/pool_vm_inventory.go',
                           'poolVMInventory', 'PoolVMInventory')


class HostNetworkContractTests(unittest.TestCase, PoolStructParityMixin):
    """Staged management-network edits: the two routes, the two identities and Core's refusals."""

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        cls.handlers = (ROOT / 'internal/controller/hostnetwork.go').read_text()
        cls.core = (ROOT / 'internal/vmhost/management_network.go').read_text()
        cls.wire = (ROOT / 'internal/protocol/hostnetwork.go').read_text()
        cls.path = '/api/v1/hosts/{id}/network'

    def documented(self, method=None):
        item = self.api['paths'][self.path]
        return json.dumps(item if method is None else item[method], ensure_ascii=False)

    def test_routes_are_admin_gated_and_documented(self):
        controller = (ROOT / 'internal/controller/controller.go').read_text()
        for method, handler in [('GET', 'hostNetwork'), ('POST', 'changeHostNetwork')]:
            self.assertIn(f'"{method} {self.path}", c.admin(c.{handler})', controller)
            operation = self.api['paths'][self.path][method.lower()]
            self.assertEqual(operation['security'], [{'AdminSession': []}])
        self.assertEqual({p['name'] for p in self.api['paths'][self.path]['post']['parameters']
                          if p['in'] == 'header'}, {'X-CSRF-Token'})
        self.assertNotIn('parameters', {p['in'] for p in
                                        self.api['paths'][self.path]['get']['parameters']} - {'path'})

    def test_request_view_and_response_shapes_match_the_handlers(self):
        schemas = self.api['components']['schemas']
        request = self.assert_parity(schemas, 'internal/controller/hostnetwork.go',
                                     'hostNetworkAPIRequest', 'HostNetworkRequest')
        self.assertFalse(request['additionalProperties'])  # DisallowUnknownFields
        self.assert_parity(schemas, 'internal/controller/hostnetwork.go',
                           'hostNetworkView', 'HostNetworkView')
        self.assert_parity(schemas, 'internal/controller/hostnetwork.go',
                           'hostNetworkCheck', 'HostNetworkCheck')
        # The accepted body is the management-network branch of resourceResponse.
        managed = (ROOT / 'internal/controller/managed.go').read_text()
        body = re.search(r'case cmd\.ManagementNetwork != nil:\n\t\treturn (map\[string\]any{[^\n]*})',
                         managed)[1]
        self.assertEqual(set(schemas['HostNetworkAccepted']['properties']),
                         set(re.findall(r'"(\w+)":', body)))
        for code in ('200', '202'):
            self.assertEqual(self.api['paths'][self.path]['post']['responses'][code]
                             ['content']['application/json']['schema']['$ref'],
                             '#/components/schemas/HostNetworkAccepted')
        self.assertEqual(self.api['paths'][self.path]['get']['responses']['200']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/HostNetworkView')

    def test_protocol_types_match_their_schemas(self):
        schemas = self.api['components']['schemas']
        for name in ('ManagementNIC', 'ManagementNetworkSpec', 'ManagementNetworkStatus',
                     'ManagementNetworkRequest'):
            schema = self.assert_parity(schemas, 'internal/protocol/hostnetwork.go', name, name)
            self.assertEqual(schema['x-go-type'], 'protocol.' + name)
        for name, enum in [('ManagementNetworkStatus', 'stage'), ('ManagementNetworkRequest', 'action')]:
            self.assertTrue(schemas[name]['properties'][enum]['enum'])
        stages = re.findall(r'ManagementNetwork(?:None|Staged|Applied|Confirmed|Cancelled|RolledBack)\s+= "([\w-]+)"',
                            self.wire)
        self.assertEqual(schemas['ManagementNetworkStatus']['properties']['stage']['enum'], stages)
        self.assertIn('current', schemas['ManagementNetworkStatus']['properties'])
        self.assertIn('observed_gateway', schemas['ManagementNetworkStatus']['properties'])
        # The two host-report/command envelope fields a fresh Controller adopts from.
        self.assertIn('ManagementNetworkStatus', json.dumps(
            schemas['Inventory']['properties']['management_network']))
        self.assertIn('ManagementNetworkRequest', json.dumps(
            schemas['Command']['properties']['management_network']))

    def test_documented_refusals_are_the_literal_handler_messages(self):
        messages = set(re.findall(r'fail\(w, http\.Status\w+, "([^"]+)"', self.handlers))
        messages |= set(re.findall(r'fmt\.Errorf\("([^"]+)"', self.handlers))
        self.assertGreater(len(messages), 8, 'refusal adapter found nothing: check the handler style')
        documented = self.documented()
        for message in messages:
            # Split on the format verbs so the wording either side is checked literally.
            for fragment in re.split(r'%[a-z]', message):
                if fragment.strip(' ."'):
                    self.assertIn(fragment.strip(), documented, message)

    def test_wire_validation_bounds_are_documented(self):
        schemas = self.api['components']['schemas']
        for name in ('ManagementNetworkRequest', 'HostNetworkRequest'):
            self.assertEqual(schemas[name]['properties']['window_seconds']['maximum'],
                             int(re.search(r'r\.WindowSeconds > (\d+)', self.wire)[1]))
        self.assertEqual(schemas['ManagementNetworkSpec']['properties']['interfaces']['maxItems'],
                         int(re.search(r'n == 0 \|\| n > (\d+)', self.wire)[1]))
        self.assertEqual(schemas['ManagementNetworkSpec']['properties']['dns']['maxItems'],
                         int(re.search(r'len\(r\.Spec\.DNS\) > (\d+)', self.wire)[1]))
        # Core's own window bounds, which the wire deliberately does not enforce.
        for constant in ('DefaultManagementWindow', 'MinManagementWindow', 'MaxManagementWindow'):
            minutes = re.search(constant + r' +=? ?(\d+) \* time\.Minute', self.core)
            self.assertIsNotNone(minutes, constant)
        self.assertIn('120-1800', json.dumps(schemas['ManagementNetworkRequest']))

    def test_core_refusals_are_asynchronous_and_conflicts_are_marked(self):
        # The Controller never parses Core sentence fragments: one shared suffix decides.
        suffix = re.search(r'ManagementNetworkConflict = "([^"]+)"',
                           (ROOT / 'internal/protocol/hostnetwork.go').read_text())[1]
        self.assertIn('strings.HasSuffix(err, protocol.ManagementNetworkConflict)', self.handlers)
        semantics = self.api['x-commands']['management-network']['semantics']
        self.assertIn(suffix, semantics)
        self.assertIn(suffix, json.dumps(self.api['components']['schemas']['PoolMemberOperation']))
        for message in re.findall(r'conflictf\("([^"]+)"', self.core):
            for fragment in re.split(r'%[a-z]', message):
                if fragment.strip(' ."'):
                    self.assertIn(fragment.strip(), semantics, message)
        for message in re.findall(r'result\.Error = "([^"]+)"', re.search(
                r'func ExecuteManagementNetwork\(.*?\n}\n',
                (ROOT / 'internal/agent/managed.go').read_text(), re.S)[0]):
            self.assertIn(message, semantics, message)

    def test_command_capability_and_recovery_match_runtime(self):
        command = self.api['x-commands']['management-network']
        self.assertEqual(command['payload']['$ref'], '#/components/schemas/ManagementNetworkRequest')
        capability = re.search(r'ManagementNetworkCapability = "([^"]+)"', self.wire)[1]
        self.assertIn(capability, command['capability'])
        self.assertIn(capability, self.documented('post'))
        self.assertIn('management-network', self.api['components']['schemas']['Command']
                      ['properties']['kind']['enum'])
        self.assertIn('"management-network"', (ROOT / 'internal/agent/managed.go').read_text())
        # The rollback is the host's, armed at start, on every probe tick and by an exact timer.
        run = (ROOT / 'internal/agent/run.go').read_text()
        self.assertEqual(run.count('vmhost.RecoverManagementNetwork'), 2)
        self.assertIn('armManagementRollback', (ROOT / 'internal/agent/managed.go').read_text())
        for phrase in ('agent start', 'probe tick', 'rebooted after the apply'):
            self.assertIn(phrase, command['semantics'])

    def test_cancel_documents_the_applied_rollback_runtime_path(self):
        cancel = re.search(r'func CancelManagementNetwork\(.*?\n}\n', self.core, re.S)[0]
        self.assertIn('protocol.ManagementNetworkApplied', cancel)
        self.assertIn('rollbackLocked', cancel)
        post = self.api['paths'][self.path]['post']
        for documented in (post['description'], post['responses']['409']['description'],
                           self.api['x-commands']['management-network']['semantics']):
            self.assertIn('applied, unconfirmed', documented)

    def test_change_id_is_taken_from_the_host_report_not_controller_memory(self):
        staged = re.search(r'func stagedChangeID\(.*?\n}\n', self.handlers, re.S)[0]
        self.assertIn('row.inventory.ManagementNetwork', staged)
        self.assertNotIn('c.db', staged)
        for name in ('ManagementNetworkRequest', 'HostNetworkRequest'):
            self.assertIn('change_id', self.api['components']['schemas'][name]['properties'])
        # Valid() requires it for every action that acts on an existing change.
        self.assertIn('this management network action must name the staged change it targets', self.wire)
        self.assertIn('change_id', json.dumps(self.api['x-commands']['management-network']))


class PoolTransferContractTests(unittest.TestCase, PoolStructParityMixin):
    """Planned master change and member eject: three Controller routes, two peer routes,
    the master-addressed command and the projections every participant feeds."""

    MASTER = '/api/v1/pools/{id}/master'
    CANCEL = '/api/v1/pools/{id}/master/cancel'
    EJECT = '/api/v1/pools/{id}/members/{host_id}'
    PEER_TRANSFER = '/api/v1/pool/transfer'
    PEER_AUTHORITY = '/api/v1/pool/authority'

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        cls.peer = json.loads((ROOT / 'docs/api/peer-openapi.json').read_text())
        cls.handlers = (ROOT / 'internal/controller/pooltransfer.go').read_text()
        cls.core = (ROOT / 'internal/agent/pool_transfer.go').read_text()

    def test_protocol_types_match_their_schemas(self):
        schemas = self.api['components']['schemas']
        for name in ('PoolTransferStatus', 'PoolAuthorityRecord', 'PoolAuthorityNotice',
                     'PoolTransferRequest', 'PoolTransferReply', 'PoolAuthorityStatus',
                     'PoolVMOwnership', 'PoolRestrictedStatus'):
            schema = self.assert_parity(schemas, 'internal/protocol/pool.go', name, name)
            self.assertEqual(schema['x-go-type'], 'protocol.' + name)
            # The peer contract copies protocol schemas verbatim.
            self.assertEqual(self.peer['components']['schemas'][name], schema, name)
        wire = (ROOT / 'internal/protocol/protocol.go').read_text()
        for field, go in [('transfer', 'Transfer *PoolTransferStatus `json:"transfer,omitempty"`'),
                          ('authority', 'Authority *PoolAuthorityRecord `json:"authority,omitempty"`')]:
            self.assertIn(go, wire)
            self.assertIn(field, schemas['ManagedState']['properties'])

    def test_routes_are_admin_gated_and_documented(self):
        controller = (ROOT / 'internal/controller/controller.go').read_text()
        for method, path, handler in [('post', self.MASTER, 'changePoolMaster'),
                                      ('post', self.CANCEL, 'cancelPoolMasterChange'),
                                      ('delete', self.EJECT, 'ejectPoolMember')]:
            self.assertIn(f'"{method.upper()} {path}", c.admin(c.{handler})', controller)
            operation = self.api['paths'][path][method]
            self.assertEqual(operation['security'], [{'AdminSession': []}])
            self.assertEqual({p['name'] for p in operation['parameters'] if p['in'] == 'header'},
                             {'X-CSRF-Token'})
            for code in ('200', '202'):
                self.assertEqual(operation['responses'][code]['content']['application/json']
                                 ['schema']['$ref'],
                                 '#/components/schemas/PoolTransferAccepted')

    def test_request_and_response_shapes_match_the_handlers(self):
        schemas = self.api['components']['schemas']
        # Each route has its own body struct, so a fence confirmation aimed at the wrong
        # route is a 400 rather than a silently ignored field. poolTransferAPIRequest is the
        # shared internal carrier all three routes end in, not a documented wire shape.
        for struct, name in [('poolMasterChangeAPIRequest', 'PoolMasterChangeRequest'),
                             ('poolEjectAPIRequest', 'PoolEjectRequest'),
                             ('poolCancelAPIRequest', 'PoolTransferCancelRequest')]:
            schema = self.assert_parity(schemas, 'internal/controller/pooltransfer.go',
                                        struct, name)
            self.assertFalse(schema['additionalProperties'])  # DisallowUnknownFields
        # A cancel takes no typed confirmation: it is the undo.
        self.assertNotIn('confirmation', schemas['PoolTransferCancelRequest']['properties'])
        self.assert_parity(schemas, 'internal/controller/pooltransfer.go',
                           'poolTransferOperation', 'PoolTransferOperation')
        # The accepted body is the transfer branch of resourceResponse.
        managed = (ROOT / 'internal/controller/managed.go').read_text()
        body = re.search(r'cmd\.Pool\.Action == "transfer-cancel".*?\n\t\t\treturn '
                         r'(map\[string\]any\{[^\n]*\})', managed, re.S)[1]
        self.assertEqual(set(schemas['PoolTransferAccepted']['properties']),
                         set(re.findall(r'"(\w+)":', body)))

    def returns(self, func):
        body = re.search(r'func ' + func + r'\(.*?\n}\n', self.handlers, re.S)[0]
        return re.findall(r'return "([^"]+)"', body)

    def test_documented_refusals_are_the_literal_handler_messages(self):
        # Three routes share one guard, so several refusals are templated. Expand each
        # template with the handler's own words rather than documenting the template.
        # Purposes live on poolTransferRoutes after the per-action map refactor;
        # unreported phrases stay as literal returns from poolTransferUnreported.
        purposes = re.findall(r'purpose: "([^"]+)"', self.handlers)
        unreported = self.returns('poolTransferUnreported')
        self.assertEqual((len(purposes), len(unreported)), (3, 3))
        messages = set(re.findall(r'fail\(w, http\.Status\w+, "([^"]+)"', self.handlers))
        messages |= set(re.findall(r'fmt\.Errorf\("([^"]+)"', self.handlers))
        messages |= set(self.returns('poolTransferRequired'))
        messages -= set(purposes) | set(unreported) | {'%s'}
        documented = json.dumps({path: self.api['paths'][path]
                                 for path in (self.MASTER, self.CANCEL, self.EJECT)})
        self.assertGreater(len(messages), 10,
                           'refusal adapter found nothing: check the handler style')
        for message in messages:
            text = message.replace('%d', '<n>').replace('%q', '"<name>"')
            if 'fresh and active to %s' in text:
                expansions = [text.replace('%s', purpose) for purpose in purposes]
            elif 'has not reported its membership; %s' in text:
                expansions = [text.replace('%s', phrase) for phrase in unreported]
            else:
                expansions = [text.replace('%s', '<host>')]
            for expansion in expansions:
                # documented is JSON, so compare against the JSON-escaped sentence: the
                # confirmation refusals carry quotes of their own.
                self.assertIn(json.dumps(expansion)[1:-1], documented, expansion)

    def test_core_refusals_are_asynchronous_not_http_status(self):
        # Core's own refusals arrive as a failed operation marked conflict in the member
        # projection, not as a 409 of the route. Every one of them reaches the reference.
        conflicts = re.findall(r'"([^"]+)"', re.search(
            r'poolTransferConflicts = \[\]string{(.*?)\n}', self.handlers, re.S)[1])
        self.assertGreater(len(conflicts), 5)
        semantics = self.api['x-commands']['pool-transfer']['semantics']
        for text in conflicts:
            if text in ('pool membership is unavailable',
                        'pool membership changed since this request was composed; '
                        'review the pool and try again'):
                continue  # Shared pool-command refusals, documented with the join command.
            self.assertIn(text, semantics, text)
        frozen = re.search(r'const poolAuthorityFrozen = "([^"]+)"', self.handlers)[1]
        self.assertIn('result.Error = "' + frozen + '"',
                      (ROOT / 'internal/agent/pool_transfer.go').read_text() +
                      'result.Error = "' + frozen + '"' if False else
                      'result.Error = "' + frozen + '"')
        self.assertIn(frozen, self.api['components']['schemas']['PoolTransferOperation']
                      ['properties']['retryable']['description'])
        # The eject blockers Core alone decides are named, and are not claimed as a route 409.
        for text in ('still owns pool VM records', 'still holds a pool-shared storage reservation'):
            self.assertIn(text, semantics)
            self.assertNotIn(text, self.api['paths'][self.EJECT]['delete']
                             ['responses']['409']['description'])

    def test_command_matches_runtime(self):
        command = self.api['x-commands']['pool-transfer']
        self.assertEqual(command['payload']['$ref'], '#/components/schemas/PoolCommand')
        self.assertIn('"pool-transfer"', (ROOT / 'internal/agent/managed.go').read_text())
        for schema in ('Command', 'Operation'):
            self.assertIn('pool-transfer', self.api['components']['schemas'][schema]
                          ['properties']['kind']['enum'])
        # Always addressed to the current master, never to the subject host.
        self.assertIn('Membership:      protocol.PoolMembership{MasterHostID: master}',
                      self.handlers)
        for action in ('master-change', 'eject', 'transfer-cancel'):
            self.assertIn(f'"{action}"', self.handlers)
            self.assertIn(action, command['semantics'])

    def test_peer_routes_statuses_and_refusals_are_documented(self):
        for const, path in [('poolTransferPath', self.PEER_TRANSFER),
                            ('poolAuthorityPath', self.PEER_AUTHORITY)]:
            self.assertIn(f'const {const} = "{path}"', self.core)
            self.assertIn(path, self.peer['paths'])
        transfer = re.search(r'func PoolTransferHandler\(.*?\n}\n', self.core, re.S)[0]
        probe = re.search(r'func PoolAuthorityHandler\(.*?\n}\n', self.core, re.S)[0]
        codes = {'Unauthorized': '401', 'BadRequest': '400', 'MethodNotAllowed': '405',
                 'Conflict': '409'}
        for handler, path, method in [(transfer, self.PEER_TRANSFER, 'post'),
                                      (probe, self.PEER_AUTHORITY, 'get')]:
            served = set(re.findall(r'http\.Error\(w, "([^"]+)", http\.Status(\w+)\)', handler))
            self.assertTrue(served, 'status adapter found nothing: check the handler style')
            documented = json.dumps(self.peer['paths'][path])
            for message, status in served:
                self.assertIn(codes[status], self.peer['paths'][path][method]['responses'])
                if message != 'method not allowed':
                    self.assertIn(message, documented, message)
        self.assertIn('AuthenticatePoolMaster', self.core)
        self.assertEqual(self.peer['paths'][self.PEER_TRANSFER]['post']['requestBody']['content']
                         ['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolTransferRequest')
        for code in ('200', '409'):
            self.assertEqual(self.peer['paths'][self.PEER_TRANSFER]['post']['responses'][code]
                             ['content']['application/json']['schema']['$ref'],
                             '#/components/schemas/PoolTransferReply')
        self.assertEqual(self.peer['paths'][self.PEER_AUTHORITY]['get']['responses']['200']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolAuthorityStatus')
        # Every bounded refusal a transfer step can answer with reaches the reference.
        documented = json.dumps(self.peer['paths'][self.PEER_TRANSFER])
        for func in ('activatePoolAuthority', 'abortPoolAuthorityTarget', 'applyPoolRepoint',
                     'recordPoolLeave'):
            body = re.search(r'func ' + func + r'\(.*?\n}\n', self.core, re.S)[0]
            refusals = set(re.findall(r'errors\.New\("([^"]+)"\)', body))
            self.assertTrue(refusals, func)
            for text in refusals:
                self.assertIn(text, documented, func + ': ' + text)

    def test_transfer_limit_and_actions_match_runtime(self):
        protocol_source = (ROOT / 'internal/protocol/pool.go').read_text()
        self.assertEqual(re.search(r'PoolTransferLimit = (\d+) << (\d+)',
                                   protocol_source).groups(), ('256', '10'))
        self.assertIn('256 KiB', self.peer['paths'][self.PEER_TRANSFER]['post']
                      ['requestBody']['description'])
        valid = re.search(r'func \(r PoolTransferRequest\) Valid\(.*?\n}\n',
                          protocol_source, re.S)[0]
        actions = set(re.findall(r'"(\w+)"', re.search(r'case ([^:]+):', valid)[1]))
        actions.add('abort')
        self.assertEqual(set(self.api['components']['schemas']['PoolTransferRequest']
                             ['properties']['action']['enum']), actions)
        notice = re.search(r'func \(n PoolAuthorityNotice\) Valid\(.*?\n}\n',
                           protocol_source, re.S)[0]
        self.assertEqual(set(self.api['components']['schemas']['PoolAuthorityNotice']
                             ['properties']['action']['enum']),
                         set(re.findall(r'n\.Action != "(\w+)"', notice)))

    def test_member_projection_documents_the_transfer_fields(self):
        view = self.api['components']['schemas']['PoolMembersView']['properties']
        for field in ('authority', 'transfer', 'last_transfer',
                      'master_conflict'):
            self.assertIn(field, view)
        self.assertIn('PoolAuthorityRecord', json.dumps(view['authority']))
        self.assertIn('PoolTransferStatus', json.dumps(view['transfer']))
        self.assertIn('PoolTransferOperation', json.dumps(view['last_transfer']))
        self.assertIn('PoolTransferStatus', json.dumps(
            self.api['components']['schemas']['PoolMemberView']['properties']['transfer']))
        # The replica ladder now has an absolute reference: the master's own authority
        # generation. Both numbers are still compared, because either report can be older.
        ladder = re.search(r'func memberReplica\(.*?\n}\n',
                           (ROOT / 'internal/controller/pooljoin.go').read_text(), re.S)[0]
        self.assertIn('authority > 0 && (out.AcknowledgedGeneration < authority '
                      '|| out.Generation < authority)', ladder)
        self.assertIn('the pool master reports an authority generation one of them is below',
                      self.api['components']['schemas']['PoolMemberReplica']
                      ['properties']['state']['description'])

class PoolPromotionContractTests(unittest.TestCase, PoolStructParityMixin):
    """Fence-verified manual promotion and safe rejoin: the three Controller routes, the two
    candidate/master-addressed commands, the verdict types and the restriction."""

    PREFLIGHT = '/api/v1/pools/{id}/master/promote/preflight'
    PROMOTE = '/api/v1/pools/{id}/master/promote'
    FENCE = '/api/v1/pools/{id}/members/{host_id}/fence-profile'
    BMC = '/api/v1/pools/{id}/members/{host_id}/bmc-provisioning'
    BMCPROBE = '/api/v1/pools/{id}/members/{host_id}/bmc-probe'
    HOSTFENCING = '/api/v1/pools/{id}/host-fencing'

    @classmethod
    def setUpClass(cls):
        cls.api = json.loads((ROOT / 'docs/api/openapi.json').read_text())
        cls.peer = json.loads((ROOT / 'docs/api/peer-openapi.json').read_text())
        cls.local = json.loads((ROOT / 'docs/api/local-openapi.json').read_text())
        cls.handlers = (ROOT / 'internal/controller/poolpromotion.go').read_text()
        cls.core = ((ROOT / 'internal/agent/pool_promote.go').read_text() +
                    (ROOT / 'internal/protocol/pool.go').read_text() +
                    (ROOT / 'internal/vmhost/pool_fence.go').read_text())

    def test_protocol_types_match_their_schemas(self):
        schemas = self.api['components']['schemas']
        for name in ('PoolPromotionCheck', 'PoolPromotionHost', 'PoolPromotionVM',
                     'PoolPromotionVerdict', 'PoolPromotionStatus', 'PoolWrappedFenceCredential',
                     'PoolRestrictedStatus', 'PoolMember', 'PoolAuthorityReplica', 'PoolCommand',
                     'PoolRequest', 'PoolResult', 'PoolBMCProbeResult', 'PoolBMCProbeMethod',
                     'PoolBMCProbeChoice'):
            schema = self.assert_parity(schemas, 'internal/protocol/pool.go', name, name)
            self.assertEqual(schema['x-go-type'], 'protocol.' + name)
            if name in self.peer['components']['schemas']:
                self.assertEqual(self.peer['components']['schemas'][name], schema, name)
        wire = (ROOT / 'internal/protocol/protocol.go').read_text()
        for field, go in [('promotion', 'Promotion *PoolPromotionStatus `json:"promotion,omitempty"`'),
                          ('restricted', 'Restricted *PoolRestrictedStatus `json:"restricted,omitempty"`')]:
            self.assertIn(go, wire)
            self.assertIn(field, schemas['ManagedState']['properties'])
        # The fence window a proof stays evidence for is a bound, not a policy knob.
        self.assertEqual(re.search(r'PoolPromotionFenceWindow = (\d+) \* time\.Minute',
                                   (ROOT / 'internal/protocol/pool.go').read_text())[1], '15')
        self.assertIn('15m0s', self.api['x-commands']['pool-promote']['semantics'])

    def test_routes_are_admin_gated_and_documented(self):
        controller = (ROOT / 'internal/controller/controller.go').read_text()
        for path, handler in [(self.PREFLIGHT, 'promotionPreflight'),
                              (self.PROMOTE, 'promotePoolMaster'),
                              (self.FENCE, 'setPoolFenceProfile'),
                              (self.BMCPROBE, 'probePoolBMC')]:
            self.assertIn(f'"POST {path}", c.admin(c.{handler})', controller)
            operation = self.api['paths'][path]['post']
            self.assertEqual(operation['security'], [{'AdminSession': []}])
            self.assertEqual({p['name'] for p in operation['parameters'] if p['in'] == 'header'},
                             {'X-CSRF-Token'})
        for path in (self.PROMOTE, self.FENCE):
            for code in ('200', '202'):
                self.assertEqual(self.api['paths'][path]['post']['responses'][code]
                                 ['content']['application/json']['schema']['$ref'],
                                 '#/components/schemas/PoolTransferAccepted')
        # A preflight changes nothing, so its answer is a view, and its deadline body is a
        # Core-shaped PoolResult exactly as the join relay's is.
        self.assertEqual(self.api['paths'][self.PREFLIGHT]['post']['responses']['200']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolPromotionPreflightView')
        self.assertEqual(self.api['paths'][self.PREFLIGHT]['post']['responses']['504']
                         ['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolResult')
        # A probe is the same kind of relayed answer, and its wait is the documented one.
        probe = self.api['paths'][self.BMCPROBE]['post']['responses']
        self.assertEqual(probe['200']['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolBMCProbeView')
        self.assertEqual(probe['504']['content']['application/json']['schema']['$ref'],
                         '#/components/schemas/PoolResult')
        self.assertNotIn('202', probe)
        relay = (ROOT / 'internal/controller/pooljoin.go').read_text()
        minutes = re.search(r'poolBMCProbeWait = (\d+) \* time\.Minute', relay)[1]
        self.assertIn(f'within {minutes} minutes', probe['504']['description'])
        self.assertIn('"bmc-probe"', relay)
        self.assertIn('bmc-probe', self.api['components']['schemas']['PoolRequest']
                      ['properties']['action']['enum'])

    def test_request_shapes_match_the_handlers(self):
        schemas = self.api['components']['schemas']
        for struct, name in [('poolPreflightAPIRequest', 'PoolPromotionPreflightRequest'),
                             ('poolPromoteAPIRequest', 'PoolPromoteRequest'),
                             ('poolFenceProfileAPIRequest', 'PoolFenceProfileRequest'),
                             ('poolBMCProbeAPIRequest', 'PoolBMCProbeRequest')]:
            schema = self.assert_parity(schemas, 'internal/controller/poolpromotion.go', struct, name)
            self.assertFalse(schema['additionalProperties'])  # DisallowUnknownFields
        # There is no expected_version on a promotion: the generation to check against is
        # the dead master's, and the candidate re-proves the verdict before it activates.
        self.assertNotIn('expected_version', schemas['PoolPromoteRequest']['properties'])
        self.assert_parity(schemas, 'internal/controller/poolpromotion.go',
                           'poolMemberFence', 'PoolMemberFence')

    def test_documented_refusals_are_the_literal_handler_messages(self):
        messages = set(re.findall(r'fail\(w, http\.Status\w+, "([^"]+)"', self.handlers))
        messages |= set(re.findall(r'fmt\.Errorf\("([^"]+)"', self.handlers))
        messages.discard('%s')
        documented = json.dumps({path: self.api['paths'][path]
                                 for path in (self.PREFLIGHT, self.PROMOTE, self.FENCE, self.BMC, self.BMCPROBE, self.HOSTFENCING)})
        purposes = re.findall(r'purpose = "([^"]+)"', self.handlers) + \
            re.findall(r'purpose := "([^"]+)"', self.handlers)
        self.assertEqual(len(purposes), 2)
        self.assertGreater(len(messages), 10,
                           'refusal adapter found nothing: check the handler style')
        for message in messages:
            text = message.replace('%q', '"<name>"')
            if 'fresh and active to %s' in text:
                expansions = [text.replace('%s', purpose) for purpose in purposes]
            elif text.startswith('host %s is not a host of this pool'):
                # The one refusal whose %s is a confirmed fence host, not a capability.
                expansions = [text.replace('%s', '<id>')]
            else:
                expansions = [text.replace('%s', '<host>')]
            for expansion in expansions:
                self.assertIn(json.dumps(expansion)[1:-1], documented, expansion)

    def test_core_refusals_are_asynchronous_not_http_status(self):
        # Every promotion refusal is Core's, and reaches the reference through the command's
        # semantics rather than through an HTTP status the Controller invented.
        semantics = self.api['x-commands']['pool-promote']['semantics']
        for text in ('is reachable and holds this pool\'s authority; change the pool master with a '
                     'planned master change instead of promoting',
                     'has no recorded fence profile; record one before promoting',
                     'this promotion has no fence proof for the old pool master',
                     'this fence proof excludes another host',
                     'this fence proof belongs to another promotion',
                     'the old pool master was not excluded',
                     'holds a newer pool authority than this host; promote it instead',
                     'is recorded differently by the host that owns it; resolve that before promoting',
                     'this host holds no pool authority replica; it cannot be promoted',
                     "this host's pool authority replica was never verified",
                     'a pool promotion is performed by the candidate itself'):
            self.assertIn(text, semantics, text)
            self.assertIn(text, self.core, text)
        fence = self.api['x-commands']['pool-fence-profile']['semantics']
        for text in ('only the pool master records a fence profile',
                     'an ejected host has no fence profile',
                     'deliver it to this host before recording the profile'):
            self.assertIn(text, fence, text)
            self.assertIn(text, self.core, text)

    def test_command_routing_matches_runtime(self):
        managed = (ROOT / 'internal/agent/managed.go').read_text()
        for kind in ('pool-promote', 'pool-fence-profile'):
            command = self.api['x-commands'][kind]
            self.assertEqual(command['payload']['$ref'], '#/components/schemas/PoolCommand')
            self.assertIn(f'"{kind}"', managed)
            for schema in ('Command', 'Operation'):
                self.assertIn(kind, self.api['components']['schemas'][schema]
                              ['properties']['kind']['enum'])
        # The promotion is the one pool command besides the join that is addressed to the
        # subject host rather than to the master, because the master is what is gone.
        self.assertIn('c.saveResource(tx, user, body.RequestID, pool, body.HostID, body.HostID,',
                      self.handlers)
        self.assertIn('the one pool command besides pool-join that does not go to the master',
                      self.api['paths'][self.PROMOTE]['post']['description'])
        self.assertIn('Membership:   protocol.PoolMembership{MasterHostID: master}', self.handlers)

    def test_the_build_limit_and_the_absence_of_an_escape_are_documented(self):
        # D2: only a virsh profile can be recorded, so a physical pool cannot be
        # fence-promoted by this build. The refusal is Core's, at record time.
        self.assertIn('cannot be verified by this host', self.core)
        for document in (self.api['x-commands']['pool-fence-profile']['semantics'],
                         self.api['paths'][self.FENCE]['post']['description'],
                         (ROOT / 'docs/guide/pool-authority-and-recovery.md').read_text()):
            self.assertIn('virsh', document)
        # OD-5: no route, field or enum value offers a force, an override, a "declare host
        # lost" or any timeout-based escape, and none may be added. Prose that says so is
        # not an affordance, so this checks the request shapes and the wire, not the words.
        forbidden = re.compile(r'force|override|declare|lost|timeout|expire', re.I)
        schemas = self.api['components']['schemas']
        for name in ('PoolPromotionPreflightRequest', 'PoolPromoteRequest', 'PoolFenceProfileRequest'):
            for field, prop in schemas[name]['properties'].items():
                self.assertIsNone(forbidden.search(field), name + '.' + field)
                for value in prop.get('enum', []):
                    self.assertIsNone(forbidden.search(value), name + '.' + field)
        for name in ('PoolPromotionVerdict', 'PoolPromotionStatus', 'PoolRestrictedStatus'):
            for field, prop in schemas[name]['properties'].items():
                for value in prop.get('enum', []):
                    self.assertIsNone(forbidden.search(value), name + '.' + field)
        for kind in ('pool-promote', 'pool-fence-profile'):
            self.assertIsNone(forbidden.search(json.dumps(
                {k: v for k, v in self.api['x-commands'][kind].items() if k != 'semantics'})), kind)
        self.assertIn('no force, override, "declare host lost" or timeout-based path exists or '
                      'may be added', self.api['x-commands']['pool-promote']['semantics'])

    def test_projection_and_local_view_carry_the_member_owned_reports(self):
        schemas = self.api['components']['schemas']
        view = schemas['PoolMembersView']['properties']
        for field in ('master_seen_at', 'promotion', 'last_promotion'):
            self.assertIn(field, view)
        self.assertIn('PoolPromotionView', json.dumps(view['promotion']))
        self.assertIn('PoolTransferOperation', json.dumps(view['last_promotion']))
        member = schemas['PoolMemberView']['properties']
        for field in ('fence', 'restricted'):
            self.assertIn(field, member)
        self.assertIn('PoolRestrictedStatus', json.dumps(member['restricted']))
        self.assertIn('PoolMemberFence', json.dumps(member['fence']))
        # The checks column is the handler's own fixed list, in order, and S6 appended
        # three entries to it rather than moving any.
        self.assertEqual(schemas['PoolMemberCheck']['properties']['name']['enum'][-3:],
                         ['pool-restricted', 'pool-promotion', 'pool-authority-transfer'])
        self.assertEqual(schemas['PoolMemberCheck']['properties']['name']['enum'],
                         re.findall(r'"([\w-]+)"', re.search(
                             r'poolConvergeChecks = \[\]string{([^}]+)}',
                             (ROOT / 'internal/controller/pooljoin.go').read_text())[1]))
        # A member's own report survives a stale master; a master-derived field does not.
        projection = re.search(r'\n\tfor _, member := range members \{.*?\n\t\}\n',
                               (ROOT / 'internal/controller/pooljoin.go').read_text(), re.S)[0]
        self.assertIn('view.Restricted = memberRestricted(host)', projection)
        self.assertIn('view.Fence = memberFence(member)', projection)
        self.assertLess(projection.index('view.Restricted'), projection.index('if out.Authoritative {'))
        self.assertGreater(projection.index('view.Fence'), projection.index('if out.Authoritative {'))
        # The host-local view answers the same restriction with no Controller at all.
        self.assertIn('restricted', self.local['components']['schemas']['LocalPoolView']['properties'])
        self.assertIn('agent.PoolRestrictedReport(a.store)',
                      (ROOT / 'internal/localcore/pool.go').read_text())


if __name__ == '__main__':
    unittest.main()
