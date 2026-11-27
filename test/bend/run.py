#!/usr/bin/env python3
"""Container-only law gate; host Python uses only its standard library."""
import concurrent.futures
import fnmatch
import itertools
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
CHECKS = {"inventory", "proof", "verdict", "model-mutations", "harness-tests",
          "website-tests", "website-build", "installer-tests", "build"}


def scope_matches(path, pattern):
    if pattern.endswith("/**"):
        return path.startswith(pattern[:-2])
    return path.count("/") == pattern.count("/") and fnmatch.fnmatchcase(path, pattern)


def inventory(root=ROOT, files=None):
    manifest = json.loads((root / "test/bend/coverage.json").read_text())
    if manifest.get("schema") != 1 or not manifest.get("areas"):
        raise ValueError("invalid or empty coverage inventory")
    laws = re.findall(r"^law (\w+):", (root / "LAWS.bend").read_text(), re.M)
    proofs = re.findall(r"^def Laws\.(\w+)\(", (root / "PROOF.bend").read_text(), re.M)
    if not laws or len(set(laws)) != len(laws) or sorted(laws) != sorted(proofs):
        raise ValueError("laws and proof definitions must match exactly")
    if files is None:
        files = subprocess.check_output(
            ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
            cwd=root).decode().split("\0")
        files = sorted({f for f in files if f and (root / f).is_file()})
    # Every maintained Bend module must participate in the proof import graph.
    reached, pending = set(), [root / "PROOF.bend"]
    while pending:
        path = pending.pop().resolve()
        relative = path.relative_to(root.resolve()).as_posix()
        if relative in reached:
            continue
        reached.add(relative)
        source = "\n".join(line.split("#", 1)[0] for line in path.read_text().splitlines())
        if re.search(r"@unsafe|\?\w*|\bimport\s+[\"']", source):
            raise ValueError(f"unsafe, unfinished or foreign proof code: {path}")
        for target in re.findall(r"^import (\S+)", source, re.M):
            if target == "Base":
                continue
            if not target.startswith(".") or not target.endswith(".bend"):
                raise ValueError(f"unreviewed nonlocal proof import: {target}")
            pending.append(path.parent / target)
    orphaned = {f for f in files if f.endswith(".bend")} - reached
    if orphaned:
        raise ValueError(f"Bend modules outside the proof graph: {sorted(orphaned)}")
    actual_packages = {str(Path(f).parent) for f in files if f.endswith(".go")}
    declared_packages = [p for a in manifest["areas"] for p in a.get("go_packages", [])]
    if len(declared_packages) != len(set(declared_packages)) or set(declared_packages) != actual_packages:
        raise ValueError(f"Go package inventory mismatch: new={actual_packages - set(declared_packages)}, stale={set(declared_packages) - actual_packages}")
    linked, evidence = set(), []
    for area in manifest["areas"]:
        if not area.get("contract") or not area.get("paths") or not area.get("laws"):
            raise ValueError(f"incomplete area: {area.get('area')}")
        if not area.get("evidence") and not area.get("checks"):
            raise ValueError(f"no executable evidence: {area['area']}")
        if set(area.get("checks", [])) - CHECKS:
            raise ValueError(f"unknown check in {area['area']}")
        for pattern in area["paths"]:
            if not any(scope_matches(f, pattern) for f in files):
                raise ValueError(f"stale scope: {pattern}")
        for law in area["laws"]:
            if law not in laws:
                raise ValueError(f"unknown law {law} in {area['area']}")
            linked.add(law)
        for item in area.get("evidence", []):
            source = (root / item["file"]).read_text()
            if not re.search(r"^func " + re.escape(item["test"]) + r"\(t \*testing\.T\)", source, re.M):
                raise ValueError(f"missing named test: {item}")
            if item["module"] not in {"main", "scheduleproof"}:
                raise ValueError(f"unknown evidence module: {item}")
            evidence.append(item)
    if linked != set(laws):
        raise ValueError(f"unlinked laws: {set(laws) - linked}")
    for file in files:
        owners = [a["area"] for a in manifest["areas"] if any(scope_matches(file, p) for p in a["paths"])]
        if len(owners) != 1:
            raise ValueError(f"{file}: expected one contract area, found {owners}")
    return manifest, evidence, len(files), len(laws)


def run(command, log, cwd=ROOT, env=None, timeout=900):
    with log.open("w") as output:
        result = subprocess.run(command, cwd=cwd, env=env, stdout=output,
                                stderr=subprocess.STDOUT, timeout=timeout)
    text = log.read_text()
    if result.returncode:
        raise RuntimeError(f"{log.name} failed (exit {result.returncode}):\n{text[-12000:]}")
    return text


def container(command, *, directory="/work", root=False, env=()):
    args = ["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges", "--pids-limit", "512", "--memory", "4g",
            "--tmpfs", "/tmp:rw,exec,nosuid,size=1g",
            "--mount", f"type=bind,source={ROOT},target=/work,readonly",
            "--mount", "type=volume,source=nomctl-laws-go-cache,target=/cache",
            "-w", directory, "-e", "GOMAXPROCS=4", "-e", "GOCACHE=/cache/build",
            "-e", "NOMCTL_REQUIRE_RSYNC=1"]
    if root:
        args += ["--user", "0:0"]
    for value in env:
        args += ["-e", value]
    return args + [os.environ.get("LAW_CHECK_IMAGE", "nomctl-law-checks:go1.26")] + command


def vector_inputs():
    cases = []
    def add(family, args, expression, boolean=False):
        cases.append(({"family": family, "args": args}, f"bit({expression})" if boolean else expression))
    for count in range(33):
        for keep in range(-2, 36):
            add("retention", [count, keep], f"M.prune_count({count}n, {max(0, keep)}n)")
    for i, state in enumerate(["Active", "Inactive", "Failed", "NotFound", "Activating",
                               "Deactivating", "Reloading", "Maintenance", "Refreshing", "Unknown"]):
        add("service", [i], f"M.stopped(M.{state}{{}})", True)
    for i, path in enumerate(["Nom", "Network", "Consensus", "Cache", "Log", "Wallet", "Config", "Other"]):
        for j, method in enumerate(["backup_path", "resync_path"]):
            add("paths", [i, j], f"M.{method}(M.{path}{{}})", True)
    for i, outcome in enumerate(["Healthy", "Syncing", "Stalled", "Uncertain", "Unhealthy", "Uncertain", "Uncertain"]):
        add("health", [i], f"M.passed(M.{outcome}{{}})", True)
    for i, kind in enumerate(["Rule", "Event", "Event", "Unrecognized"]):
        for j, state in enumerate(["Firing", "Recovered", "Info", "InvalidState"]):
            for k, severity in enumerate(["Critical", "Warning", "Informational", "InvalidSeverity"]):
                for n, bounded in enumerate([True, True, False, False, False, True]):
                    add("alerts", [i, j, k, n], f"M.valid_alert(M.{kind}{{}}, M.{state}{{}}, M.{severity}{{}}, {bounded}{{}})", True)
    for present, value, fallback in itertools.product([0, 1], [1, 10, 80, 255], [1, 80, 255]):
        configured = f"Some{{{value}n}}" if present else "None{}"
        add("config", [present, value, fallback], f"M.effective({configured}, {fallback}n)")
    for i, path in enumerate(["OrchEvents", "OrchQueues", "OrchConfig", "OrchTss", "OrchProducer", "OrchVotes", "OrchOther"]):
        add("orchestrator_sync", [i], f"M.orchestrator_sync_path(M.{path}{{}})", True)
    return cases


def balanced(parts):
    if len(parts) == 1:
        return parts[0]
    mid = len(parts) // 2
    return f"({balanced(parts[:mid])} ++ {balanced(parts[mid:])})"


def generate_vectors(work):
    # All expected values are evaluated by Bend, never by a substitute Python/Go model.
    cases, result = vector_inputs(), []
    for offset in range(0, len(cases), 128):
        chunk = cases[offset:offset + 128]
        pieces = [json.dumps("[")]
        for i, (case, expression) in enumerate(chunk):
            prefix = ("," if i else "") + json.dumps(case)[:-1] + ',"want":'
            pieces += [json.dumps(prefix), f"Nat.show({expression})", json.dumps("}")]
        pieces += [json.dumps("]")]
        source = 'import Base\nimport ../../test/bend/model.bend as M\n\ndef bit(b: Bool) -> Nat:\n  match b:\n    case False{}: 0n\n    case True{}: 1n\n\ndef main() -> String:\n  ' + balanced(pieces) + '\n'
        oracle = work / f"oracle-{offset}.bend"
        oracle.write_text(source)
        output = run([str(ROOT / "scripts/bend"), str(oracle.relative_to(ROOT))], work / f"oracle-{offset}.log")
        values = json.loads(json.loads(output.strip()))
        if len(values) != len(chunk):
            raise ValueError("Bend oracle returned the wrong number of results")
        for actual, (case, _) in zip(values, chunk):
            if {k: actual[k] for k in case} != case or type(actual.get("want")) is not int:
                raise ValueError("malformed Bend oracle output")
        result.extend(values)
    path = work / "vectors.json"
    path.write_text(json.dumps(result) + "\n")
    return path, len(result)


def model_mutations(work):
    faults = [
        ("lock", "locked && (validated && (stopped && recoverable))", "True{} && (validated && (stopped && recoverable))", "change_requires_locked"),
        ("verification", "complete && (verified && (fresh && owned))", "complete && (True{} && (fresh && owned))", "publication_requires_verified"),
        ("stale-resume", "Resume{}: Schedule{True{}, False{}}", "Resume{}: Schedule{True{}, True{}}", "stale_schedule_never_recovers"),
        ("dual-active", "ActivateDestination{}: Handoff{True{}, destination}", "ActivateDestination{}: Handoff{True{}, True{}}", "handoff_preserves_exclusion"),
        ("retention", "case 1n+n 0n: 1n+n", "case 1n+n 0n: n", "pruning_zero_keeps_nothing"),
        ("redaction", "case Secret{value}: Redacted{}", "case Secret{value}: Secret{value}", "secrets_are_redacted"),
        ("alert-budget", "case Rule{} _ _: bounded", "case Rule{} _ _: True{}", "oversized_alert_is_rejected"),
    ]
    source = (ROOT / "test/bend/model.bend").read_text()
    for name, old, new, law in faults:
        if source.count(old) != 1:
            raise ValueError(f"stale model mutation: {name}")
        dest = work / ("fault-" + name)
        (dest / "test/bend").mkdir(parents=True)
        for file in ["LAWS.bend", "PROOF.bend"]:
            shutil.copyfile(ROOT / file, dest / file)
        (dest / "test/bend/model.bend").write_text(source.replace(old, new))
        result = subprocess.run([str(ROOT / "scripts/bend"), str((dest / "PROOF.bend").relative_to(ROOT))],
                                cwd=ROOT, capture_output=True, text=True, timeout=60)
        text = result.stdout + result.stderr
        (work / f"mutation-{name}.log").write_text(text)
        if result.returncode == 0 or "SOME PROOFS FAIL" not in text or f"Location: Laws.{law}" not in text or "- observed :" not in text:
            raise RuntimeError(f"mutation {name} was not rejected as a proof mismatch:\n{text}")
    return len(faults)


def check_evidence(evidence, logs):
    passed = set()
    for module, path in logs.items():
        for line in path.read_text().splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get("Action") == "pass" and event.get("Test"):
                passed.add((module, event["Package"], event["Test"]))
    for item in evidence:
        module = item["module"]
        module_dir = ROOT if module == "main" else ROOT / "experiments/scheduleproof"
        module_name = re.search(r"^module (\S+)", (module_dir / "go.mod").read_text(), re.M)[1]
        package_dir = (ROOT / item["file"]).parent.relative_to(module_dir).as_posix()
        package = module_name + ("/" + package_dir if package_dir != "." else "")
        if (module, package, item["test"]) not in passed:
            raise ValueError(f"required evidence did not run and pass: {item}")


def implementation_mutations(work, vector_env):
    faults = [
        ("service", "internal/service/service.go", "return s == Inactive || s == Failed || s == NotFound", "return true", "TestBendServiceStates"),
        ("retention", "internal/backup/backup.go", "return sorted[keep:]", "return sorted", "TestBendRetention"),
        ("alert", "internal/alertproto/proto.go", 'if _, ok := Lookup(a.Alert); !ok && !Events[a.Alert] {\n\t\treturn false', 'if _, ok := Lookup(a.Alert); !ok && !Events[a.Alert] {\n\t\treturn true', "TestBendAlertValidation"),
        ("wallet", "internal/resync/resync.go", 'var Dirs = []string{"network", "nom", "consensus", "log"}', 'var Dirs = []string{"network", "nom", "consensus", "log", "wallet"}', "TestBendDataScope"),
        ("orchestrator_sync", "internal/orchestrator/orchestrator.go", 'var SyncDirs = []string{"events", "queues"}', 'var SyncDirs = []string{"events", "queues", "tss"}', "TestBendOrchestratorSyncScope"),
    ]
    for name, source_path, old, new, test in faults:
        source = (ROOT / source_path).read_text()
        if source.count(old) != 1:
            raise ValueError(f"stale Go mutation: {name}")
        dest = work / f"fault-{name}.go"
        dest.write_text(source.replace(old, new))
        overlay = work / f"overlay-{name}.json"
        overlay.write_text(json.dumps({"Replace": {"/work/" + source_path: "/work/" + str(dest.relative_to(ROOT))}}))
        cmd = container(["go", "test", "-mod=readonly", "-p=2", "-race", "-count=1", "-tags=bendlaws",
                         "-overlay=/work/" + str(overlay.relative_to(ROOT)), "-run=^" + test + "$", "./test/bend"], env=vector_env)
        result = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, timeout=300)
        output = result.stdout + result.stderr
        (work / f"go-mutation-{name}.log").write_text(output)
        if result.returncode == 0 or "BEND_MISMATCH" not in output or f"--- FAIL: {test}" not in output:
            raise RuntimeError(f"Go mutation {name} was not detected by its comparison:\n{output}")
    return len(faults)


def main():
    os.chdir(ROOT)
    manifest, evidence, files, laws = inventory()
    print(f"Inventory: {files} files, {len(manifest['areas'])} contract areas, {laws} laws", flush=True)
    if sys.argv[1:] == ["--inventory-only"]:
        return
    if sys.argv[1:]:
        raise ValueError("usage: run.py [--inventory-only]")
    if not shutil.which("nodebox"):
        raise RuntimeError("nodebox is unavailable; install the approved helper before running the gate")
    scratch = ROOT / ".bend-work"
    scratch.mkdir(exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="run-", dir=scratch))
    work.chmod(0o755)
    print(f"Logs: {work.relative_to(ROOT)}", flush=True)
    run([str(ROOT / "scripts/bend"), "PROOF.bend"], work / "proof.log")
    env = dict(os.environ, BEND_IMAGE=os.environ.get("BEND_VERDICT_IMAGE", "nomctl-bend:2.0.40-verdict"))
    run([str(ROOT / "scripts/bend"), "PROOF.bend", "--verdict"], work / "verdict.log", env=env)
    vectors, count = generate_vectors(work)
    vector_env = ["BEND_VECTORS=/work/" + str(vectors.relative_to(ROOT))]
    jobs = {
        "main-tests": (container(["go", "test", "-mod=readonly", "-p=2", "-race", "-json", "-count=1", "-tags=bendlaws", "./..."], env=vector_env), ROOT),
        "schedule-tests": (container(["go", "test", "-mod=readonly", "-p=2", "-race", "-json", "-count=1", "./..."], directory="/work/experiments/scheduleproof"), ROOT),
        "website": (["nodebox", "--offline", "sh", "-c", "npm test && npm run build"], ROOT / "website"),
        "harness-tests": (container(["python3", "-B", "-m", "unittest", "discover", "-s", "test/bend", "-p", "test_*.py"], root=True), ROOT),
        "build": (container(["sh", "-c", "go vet -mod=readonly ./... && go build -mod=readonly -buildvcs=false ./... && cd experiments/scheduleproof && go vet -mod=readonly ./... && go build -mod=readonly -buildvcs=false -o /tmp/scheduleproof ./cmd/scheduleproof"]), ROOT),
    }
    # Independent checks run concurrently; mutable node operations retain their existing ordering.
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
        futures = {pool.submit(run, cmd, work / (name + ".log"), cwd): name for name, (cmd, cwd) in jobs.items()}
        faults = model_mutations(work)
        print(f"Proofs and independent verdict passed; {count} model comparisons generated; {faults} model faults rejected", flush=True)
        failures = []
        for future in concurrent.futures.as_completed(futures):
            name = futures[future]
            try:
                future.result()
                print(f"PASS {name}", flush=True)
            except Exception as error:
                failures.append(str(error))
        if failures:
            raise RuntimeError("\n".join(failures))
    check_evidence(evidence, {"main": work / "main-tests.log", "scheduleproof": work / "schedule-tests.log"})
    go_faults = implementation_mutations(work, vector_env)
    print(f"PASS {go_faults} real Go source faults rejected by model comparisons", flush=True)
    print(f"PASS: {laws} Bend laws; {len(manifest['areas'])} areas; {count} Go comparisons; all named evidence ran and passed", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError) as error:
        print(f"LAW GATE FAILED: {error}", file=sys.stderr)
        sys.exit(1)
