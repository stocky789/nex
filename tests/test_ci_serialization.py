# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the heavy Actions shells with real flock and a disposable mocked Docker host.

Docker's volume mount is mapped into a temporary directory; no daemon, images,
real runner or infrastructure is used. Workflow shell templates are executed as
an Actions runner executes them, including steps with a different working directory.
"""
import ctypes
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ("image", "release", "phase1", "demo")
PROVIDERS = (".gitea", ".github")


def workflow_shell(provider, workflow, job=None):
    text = (ROOT / provider / "workflows" / f"{workflow}.yml").read_text()
    if job:
        block = text.split(f"\n  {job}:\n", 1)[1]
        step_shell = re.search(r"(?m)^        shell: (.+)$", block)
        if step_shell:
            shell = step_shell.group(1)
            if shell == "bash":
                return ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "{0}"]
            return shlex.split(shell)
    defaults = re.search(r"(?ms)^defaults:\n(.*?)(?=^[^ \n]|\Z)", text)
    if defaults:
        shell = re.search(r"(?m)^    shell: (.+)$", defaults.group(1))
        if shell:
            return shlex.split(shell.group(1))
    return ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "{0}"]


class CiSerializationTests(unittest.TestCase):
    def setUp(self):
        if not shutil.which("flock"):
            self.skipTest("util-linux flock is required")
        if sys.platform != "linux":
            self.skipTest("Linux process ownership and subreaping are required")
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        self.volume = self.work / "volume"
        self.volume.mkdir()
        self.processes = []
        self.owned_processes = {}
        self.owned_sessions = set()
        # Adopt orphaned supervisors/helpers so failures can clean up and reap
        # their own children, not leave them to an unrelated system PID 1.
        libc = ctypes.CDLL(None, use_errno=True)
        previous = ctypes.c_int()
        if libc.prctl(37, ctypes.byref(previous), 0, 0, 0) or libc.prctl(36, 1, 0, 0, 0):
            raise OSError(ctypes.get_errno(), "cannot enable test child subreaping")
        self.addCleanup(lambda: libc.prctl(36, previous.value, 0, 0, 0))
        self.addCleanup(self.stop_processes)
        # Signal each attempt before the real flock blocks. This gives the tests
        # a deterministic barrier without guessing when a contender has started.
        flock = self.bin / "flock"
        flock.write_text(
            '#!/bin/sh\n'
            'touch "$NEX_CI_TEST_WORK/attempt-$NEX_CI_TEST_ID"\n'
            f'exec {shlex.quote(shutil.which("flock"))} "$@"\n')
        flock.chmod(0o755)
        docker = self.bin / "docker"
        docker.write_text(f'#!{sys.executable}\n' + '''import os, signal, sys
from pathlib import Path
args = sys.argv[1:]
if args[0] == "inspect":
    print("sha256:disposable-runner-image")
    raise SystemExit(0)
if args[0] == "rm":
    name = args[-1]
    work = Path(os.environ["NEX_CI_TEST_WORK"])
    with (work / "removed-containers").open("a") as log:
        log.write(name + "\\n")
    try:
        os.killpg(int((work / name).read_text()), signal.SIGKILL)
    except (FileNotFoundError, ProcessLookupError):
        pass
    raise SystemExit(0)
if args[0] != "run":
    raise SystemExit(2)
if os.environ.get("FAKE_DOCKER_FAIL"):
    raise SystemExit(17)
os.setsid()
name = args[args.index("--name") + 1]
work = Path(os.environ["NEX_CI_TEST_WORK"])
(work / name).write_text(str(os.getpid()))
(work / ("container-" + os.environ["NEX_CI_TEST_ID"])).write_text(name)
helper = args[-1].replace("/nex-ci/", os.environ["FAKE_DOCKER_VOLUME"] + "/")
if os.environ.get("FAKE_DOCKER_LOCK_LOSS"):
    helper = helper.replace("cat >/dev/null", 'while [ ! -f "$NEX_CI_TEST_WORK/lose-lock" ]; do sleep 0.01; done')
helper += '\\nprintf released > "$NEX_CI_TEST_WORK/released-$NEX_CI_TEST_ID"\\n'
os.execv("/bin/sh", ["sh", "-c", helper])
''')
        docker.chmod(0o755)

    @staticmethod
    def process_identity(pid):
        try:
            fields = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            return None
        return fields[19], fields[0], int(fields[3])

    def remember_workers(self):
        marker = os.fsencode(f"NEX_CI_TEST_WORK={self.work}")
        for path in Path("/proc").glob("[0-9]*/environ"):
            pid = int(path.parent.name)
            identity = self.process_identity(pid)
            if identity is None:
                continue
            start, state, session = identity
            try:
                owned = marker in path.read_bytes().split(b"\0")
            except (FileNotFoundError, ProcessLookupError, PermissionError):
                owned = False
            # Zombie environments are empty, but their previously verified
            # private session still identifies an orphan this test must reap.
            if owned or (state == "Z" and session in self.owned_sessions):
                self.owned_processes[pid] = start
                self.owned_sessions.add(session)

    def stop_processes(self):
        # Emergency cleanup must also work when the supervisor is broken. Verify
        # fixture environment + kernel start time before killing any individual
        # PID; never signal an unverified system process or remove a mutex inode.
        roots = {process.pid for process in self.processes}
        deadline = time.monotonic() + 5
        while True:
            self.remember_workers()
            pending = False
            for pid, start in self.owned_processes.copy().items():
                identity = self.process_identity(pid)
                if identity is None or identity[0] != start:
                    continue
                if identity[1] != "Z":
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    pending = True
                if pid not in roots:
                    try:
                        reaped, _ = os.waitpid(pid, os.WNOHANG)
                        pending = pending or reaped == 0
                    except ChildProcessError:
                        pass
            for process in self.processes:
                try:
                    process.communicate(timeout=0.05)
                except subprocess.TimeoutExpired:
                    pending = True
            if not pending:
                return
            if time.monotonic() >= deadline:
                self.fail("owned CI fixture processes did not stop and reap")
            time.sleep(0.01)

    def wait_file(self, path, process=None):
        deadline = time.monotonic() + 60  # an upper bound for a loaded host; a passing wait returns as soon as the file appears
        while not path.exists():
            if process is not None and process.poll() is not None:
                try:
                    output, error = process.communicate(timeout=1)
                except subprocess.TimeoutExpired as timeout:
                    output, error = timeout.output, timeout.stderr
                    error = f"{error or ''}\ndescendants still hold the exited shell's stdio"
                self.fail(f"shell exited ({process.returncode}) before {path.name}: {output}\n{error}")
            if time.monotonic() >= deadline:
                self.fail(f"timed out waiting for {path.name}")
            time.sleep(0.01)
        self.remember_workers()

    def start(self, identifier, script, provider=".gitea", workflow="image", docker=False,
              job=None, workspace=None, **extra):
        path = self.work / f"{identifier}.sh"
        path.write_text('printf "%s\\n" "$$" > "$NEX_CI_TEST_WORK/child-$NEX_CI_TEST_ID"\n' + script)
        cwd = self.work / f"cwd-{identifier}"
        cwd.mkdir()
        env = {
            # The workflows export NEX_CI_LOCK=shared; a test gets the lock class it asks for, never the job's.
            **{key: value for key, value in os.environ.items() if key != "NEX_CI_LOCK"},
            "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            "GITHUB_WORKSPACE": str(workspace or ROOT),
            "NEX_CI_TEST_WORK": str(self.work),
            "NEX_CI_TEST_ID": identifier,
            "FAKE_DOCKER_VOLUME": str(self.volume),
            **extra,
        }
        env.pop("NEX_CI_LOCK_DIR", None)
        if not docker:
            env["NEX_CI_LOCK_DIR"] = str(self.volume)
        command = [arg.replace("{0}", str(path)) for arg in workflow_shell(provider, workflow, job)]
        process = subprocess.Popen(command, cwd=cwd, env=env, text=True,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        self.processes.append(process)
        identity = self.process_identity(process.pid)
        if identity:
            self.owned_processes[process.pid] = identity[0]
            self.owned_sessions.add(identity[2])
        return process

    def finish(self, process, expected=0):
        self.remember_workers()
        output, error = process.communicate(timeout=60)
        self.assertEqual(process.returncode, expected, f"{output}\n{error}")
        return output, error

    def test_all_heavy_workflow_shells_serialize_without_discarding_a_waiter(self):
        for provider in PROVIDERS:
            for docker in (False, True):
                with self.subTest(provider=provider, docker=docker):
                    prefix = f"{provider[1:]}-{int(docker)}"
                    leader = self.start(prefix + "-leader", '''
mkdir "$NEX_CI_TEST_WORK/active"
touch "$NEX_CI_TEST_WORK/leader-ready"
while [ ! -f "$NEX_CI_TEST_WORK/release-leader" ]; do sleep 0.01; done
rmdir "$NEX_CI_TEST_WORK/active"
''', provider, "image", docker)
                    self.wait_file(self.work / "leader-ready", leader)
                    contenders = []
                    # Include both release channels and another release queued behind
                    # them: default concurrency's single-pending queue lost this work.
                    for index, workflow in enumerate((*WORKFLOWS, "release", "release")):
                        identifier = f"{prefix}-{index}"
                        process = self.start(identifier, '''
mkdir "$NEX_CI_TEST_WORK/active"
printf '%s\\n' "$NEX_CI_TEST_ID" >> "$NEX_CI_TEST_WORK/completed"
rmdir "$NEX_CI_TEST_WORK/active"
''', provider, workflow, docker)
                        self.wait_file(self.work / f"attempt-{identifier}", process)
                        contenders.append(process)
                    (self.work / "release-leader").touch()
                    self.finish(leader)
                    for process in contenders:
                        self.finish(process)
                    completed = (self.work / "completed").read_text().splitlines()
                    self.assertCountEqual(completed, [f"{prefix}-{index}" for index in range(len(contenders))])
                    for name in ("leader-ready", "release-leader", "completed"):
                        (self.work / name).unlink()

    def test_shared_holders_overlap_but_an_exclusive_waiter_blocks_later_arrivals(self):
        for docker in (False, True):
            with self.subTest(docker=docker):
                prefix = f"shared-{int(docker)}"
                holding = '''
touch "$NEX_CI_TEST_WORK/up-$NEX_CI_TEST_ID"
while [ ! -f "$NEX_CI_TEST_WORK/release-$NEX_CI_TEST_ID" ]; do sleep 0.01; done
'''
                first = self.start(prefix + "-a", holding, docker=docker, NEX_CI_LOCK="shared")
                second = self.start(prefix + "-b", holding, docker=docker, NEX_CI_LOCK="shared")
                self.wait_file(self.work / f"up-{prefix}-a", first)
                self.wait_file(self.work / f"up-{prefix}-b", second)
                exclusive = self.start(prefix + "-x", 'touch "$NEX_CI_TEST_WORK/up-' + prefix + '-x"\n', docker=docker)
                self.wait_file(self.work / f"attempt-{prefix}-x", exclusive)
                time.sleep(0.3)  # the exclusive waiter now holds the gate and waits on the shared holders
                late = self.start(prefix + "-c", 'touch "$NEX_CI_TEST_WORK/up-' + prefix + '-c"\n',
                                  docker=docker, NEX_CI_LOCK="shared")
                self.wait_file(self.work / f"attempt-{prefix}-c", late)
                time.sleep(0.3)
                self.assertFalse((self.work / f"up-{prefix}-x").exists(), "exclusive ran beside shared holders")
                self.assertFalse((self.work / f"up-{prefix}-c").exists(), "late shared arrival jumped the exclusive waiter")
                (self.work / f"release-{prefix}-a").touch()
                (self.work / f"release-{prefix}-b").touch()
                for process in (first, second, exclusive, late):
                    self.finish(process)
                self.assertTrue((self.work / f"up-{prefix}-x").exists())
                self.assertTrue((self.work / f"up-{prefix}-c").exists())

    def test_native_script_failure_keeps_errexit_and_pipefail_and_releases_the_lock(self):
        failed = self.start("native-failure", 'false | true\nprintf "unexpected\\n"\n')
        output, _ = self.finish(failed, 1)
        self.assertEqual(output, "")
        self.finish(self.start("native-after-failure", 'printf "resumed\\n"\n'))

    def test_docker_script_failure_keeps_errexit_and_pipefail_and_releases_the_lock(self):
        failed = self.start("docker-failure", 'false | true\nprintf "unexpected\\n"\n', docker=True)
        output, _ = self.finish(failed, 1)
        self.assertEqual(output, "")
        self.wait_file(self.work / "released-docker-failure")
        self.finish(self.start("docker-after-failure", 'printf "resumed\\n"\n', docker=True))

    def test_job_control_profile_preserves_workload_execution_and_failure_status(self):
        profile = self.work / "job-control-profile"
        profile.write_text("set -m\n")
        for docker in (False, True):
            with self.subTest(docker=docker):
                process = self.start(f"profile-status-{int(docker)}", '''
printf "workload-ran\\n"
exit 7
''', docker=docker, BASH_ENV=str(profile))
                output, _ = self.finish(process, 7)
                self.assertEqual(output, "workload-ran\n")

    def test_job_control_profile_sigkill_reaps_work_before_unlocking_and_closes_stdio(self):
        profile = self.work / "job-control-profile"
        profile.write_text("set -m\n")
        for docker in (False, True):
            with self.subTest(docker=docker):
                identifier = f"profile-sigkill-{int(docker)}"
                leader = self.start(identifier, '''
trap '' TERM
sleep 600 &
printf '%s\\n' "$!" > "$NEX_CI_TEST_WORK/descendant-$NEX_CI_TEST_ID"
touch "$NEX_CI_TEST_WORK/profile-ready-$NEX_CI_TEST_ID"
wait
''', docker=docker, BASH_ENV=str(profile))
                self.wait_file(self.work / f"profile-ready-{identifier}", leader)
                waiter = self.start(identifier + "-waiter", f'''
pid=$(<"$NEX_CI_TEST_WORK/child-{identifier}")
if kill -0 "$pid" 2>/dev/null; then
    echo "profile-enabled workload was not stopped and reaped before the waiter" >&2
    exit 1
fi
pid=$(<"$NEX_CI_TEST_WORK/descendant-{identifier}")
if [[ -r /proc/$pid/stat ]]; then
    IFS= read -r stat < "/proc/$pid/stat"
    read -r state rest <<< "${{stat##*) }}"
    [[ $state == Z || $state == X ]]
fi
printf "resumed\\n"
''', workflow="release", docker=docker, BASH_ENV=str(profile))
                self.wait_file(self.work / f"attempt-{identifier}-waiter", waiter)
                leader.kill()
                output, _ = self.finish(waiter)
                self.assertEqual(output, "resumed\n")
                self.finish(leader, -signal.SIGKILL)

    def test_native_job_termination_releases_the_lock_for_the_waiter(self):
        leader = self.start("native-killed", 'touch "$NEX_CI_TEST_WORK/native-ready"\nsleep 600\n')
        self.wait_file(self.work / "native-ready", leader)
        waiter = self.start("native-waiter", 'printf "resumed\\n"\n', workflow="release")
        self.wait_file(self.work / "attempt-native-waiter", waiter)
        os.killpg(leader.pid, signal.SIGTERM)
        leader.communicate(timeout=60)
        self.finish(waiter)

    def test_native_wrapper_sigkill_reaps_ignored_term_work_before_the_waiter_runs(self):
        leader = self.start("native-sigkill", '''
trap '' TERM
sleep 600 &
touch "$NEX_CI_TEST_WORK/native-sigkill-ready"
wait
''')
        self.wait_file(self.work / "native-sigkill-ready", leader)
        waiter = self.start("native-sigkill-waiter", '''
pid=$(<"$NEX_CI_TEST_WORK/child-native-sigkill")
if kill -0 "$pid" 2>/dev/null; then
    echo "native workload was not stopped and reaped before the waiter" >&2
    exit 1
fi
printf "resumed\\n"
''', workflow="release")
        self.wait_file(self.work / "attempt-native-sigkill-waiter", waiter)
        leader.kill()
        self.finish(waiter)
        pid = (self.work / "child-native-sigkill").read_text().strip()
        self.assertFalse(Path(f"/proc/{pid}").exists(), "native supervisor must reap the workload")

    def test_wrapper_sigkill_stops_and_reaps_ignored_term_work_before_releasing_the_mutex(self):
        leader = self.start("docker-killed", '''
trap '' TERM
sleep 600 &
printf '%s\\n' "$!" > "$NEX_CI_TEST_WORK/descendant-docker-killed"
touch "$NEX_CI_TEST_WORK/docker-ready"
wait
''', docker=True)
        self.wait_file(self.work / "docker-ready", leader)
        waiter = self.start("docker-waiter", '''
pid=$(<"$NEX_CI_TEST_WORK/child-docker-killed")
if kill -0 "$pid" 2>/dev/null; then
    echo "orphan workload started overlapping its waiter" >&2
    exit 1
fi
pid=$(<"$NEX_CI_TEST_WORK/descendant-docker-killed")
if [[ -r /proc/$pid/stat ]]; then
    IFS= read -r stat < "/proc/$pid/stat"
    read -r state rest <<< "${stat##*) }"
    [[ $state == Z || $state == X ]]
fi
printf "resumed\\n"
''', workflow="release", docker=True)
        self.wait_file(self.work / "attempt-docker-waiter", waiter)
        # Kill only the wrapper. The independent supervisor must stop and reap
        # the workload, then close helper stdin; test teardown cannot make it pass.
        leader.kill()
        self.wait_file(self.work / "released-docker-killed")
        self.finish(waiter)
        pid = (self.work / "child-docker-killed").read_text().strip()
        self.assertFalse(Path(f"/proc/{pid}").exists(), "supervisor must reap the workload")

    def test_docker_lock_acquisition_failure_never_executes_the_script(self):
        failed = self.start("docker-unavailable", 'touch "$NEX_CI_TEST_WORK/unsafe"\n',
                            docker=True, FAKE_DOCKER_FAIL="1")
        _, error = self.finish(failed, 1)
        self.assertIn("lock could not be acquired", error)
        self.assertFalse((self.work / "unsafe").exists())

    def test_docker_lock_loss_stops_the_original_script_and_reports_failure(self):
        failed = self.start("docker-lock-lost", '''
touch "$NEX_CI_TEST_WORK/lost-ready"
sleep 600
touch "$NEX_CI_TEST_WORK/unsafe"
''', docker=True, FAKE_DOCKER_LOCK_LOSS="1")
        self.wait_file(self.work / "lost-ready", failed)
        (self.work / "lose-lock").touch()
        _, error = self.finish(failed, 1)
        self.assertIn("lock was lost", error)
        self.assertFalse((self.work / "unsafe").exists())

    def test_helper_loss_bounds_term_grace_and_kills_ignored_term_descendants(self):
        failed = self.start("docker-stubborn", '''
trap '' TERM
sleep 600 &
printf '%s\\n' "$!" > "$NEX_CI_TEST_WORK/descendant-docker-stubborn"
touch "$NEX_CI_TEST_WORK/stubborn-ready"
wait
touch "$NEX_CI_TEST_WORK/unsafe"
''', docker=True, FAKE_DOCKER_LOCK_LOSS="1")
        self.wait_file(self.work / "stubborn-ready", failed)
        (self.work / "lose-lock").touch()
        _, error = self.finish(failed, 1)
        self.assertIn("lock was lost", error)
        self.assertFalse((self.work / "unsafe").exists())
        pid = (self.work / "child-docker-stubborn").read_text().strip()
        self.assertFalse(Path(f"/proc/{pid}").exists(), "supervisor must reap ignored-TERM work")
        descendant = (self.work / "descendant-docker-stubborn").read_text().strip()
        stat = Path(f"/proc/{descendant}/stat")
        if stat.exists():
            state = stat.read_text().rsplit(") ", 1)[1].split()[0]
            self.assertIn(state, ("Z", "X"), "no live descendant may outlast cleanup")

    def check_queued_cancellation(self, cancellation_signal):
        leader = self.start("queue-owner", '''
touch "$NEX_CI_TEST_WORK/queue-ready"
while [ ! -f "$NEX_CI_TEST_WORK/release-queue" ]; do sleep 0.01; done
''', docker=True)
        self.wait_file(self.work / "queue-ready", leader)
        queued = self.start("queue-cancelled", 'touch "$NEX_CI_TEST_WORK/unsafe"\n',
                            workflow="release", docker=True)
        self.wait_file(self.work / "attempt-queue-cancelled", queued)
        os.kill(queued.pid, cancellation_signal)
        queued.communicate(timeout=60)
        self.assertEqual(queued.returncode, -signal.SIGKILL if cancellation_signal == signal.SIGKILL else 143)
        own_name = (self.work / "container-queue-cancelled").read_text()
        removed = (self.work / "removed-containers").read_text().splitlines()
        self.assertEqual(removed, [own_name])
        self.assertIsNone(leader.poll(), "cancelling a waiter must not stop the lock owner")
        self.assertFalse((self.work / "unsafe").exists())
        (self.work / "release-queue").touch()
        self.finish(leader)
        self.finish(self.start("queue-after-cancel", 'printf "resumed\\n"\n', docker=True))

    def test_cancelling_a_queued_helper_removes_only_its_own_container(self):
        self.check_queued_cancellation(signal.SIGKILL)

    def test_terminating_a_queued_helper_bounds_handshake_cancellation(self):
        self.check_queued_cancellation(signal.SIGTERM)

    def test_phase1_aggregate_runs_without_a_checkout_and_preserves_lane_failures(self):
        workspace = self.work / "no-checkout"
        workspace.mkdir()
        lanes = ("go", "web", "checks")
        for provider in PROVIDERS:
            text = (ROOT / provider / "workflows/phase1.yml").read_text()
            block = text.split("\n  phase1:\n", 1)[1]
            original = textwrap.dedent(block.split("run: |\n", 1)[1])
            for failed_lane in (None, *lanes):
                script = original
                for lane in lanes:
                    script = script.replace("${{ needs." + lane + ".result }}",
                                            "failure" if lane == failed_lane else "success")
                identifier = f"gate-{provider[1:]}-{failed_lane or 'success'}"
                with self.subTest(provider=provider, failed_lane=failed_lane):
                    process = self.start(identifier, script, provider, "phase1", job="phase1",
                                         workspace=workspace)
                    self.finish(process, 1 if failed_lane else 0)

    def test_emergency_teardown_reaps_its_orphaned_supervisors_helpers_and_workloads(self):
        for docker in (False, True):
            identifier = f"emergency-{int(docker)}"
            leader = self.start(identifier, '''
trap '' TERM
sleep 600 &
touch "$NEX_CI_TEST_WORK/emergency-ready"
wait
''', docker=docker)
            self.wait_file(self.work / "emergency-ready", leader)
            supervisors = []
            for pid, start in self.owned_processes.items():
                identity = self.process_identity(pid)
                if identity is None or identity[0] != start or identity[2] != pid:
                    continue
                try:
                    args = Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")
                except (FileNotFoundError, ProcessLookupError):
                    continue
                if b"--supervise" in args:
                    supervisors.append(pid)
            self.assertEqual(len(supervisors), 1, "only this fixture's active supervisor is targeted")
            # Model a broken supervisor, not ordinary cancellation. Emergency
            # teardown must independently terminate and reap the remaining tree.
            os.kill(supervisors[0], signal.SIGKILL)
            leader.kill()
            self.stop_processes()
            for pid, start in self.owned_processes.items():
                identity = self.process_identity(pid)
                self.assertTrue(identity is None or identity[0] != start,
                                f"owned fixture process {pid} was not reaped")
            (self.work / "emergency-ready").unlink()


if __name__ == "__main__":
    unittest.main()
