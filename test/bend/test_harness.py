import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import run


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "test/bend").mkdir(parents=True)
        self.files = ["LAWS.bend", "PROOF.bend", "test/bend/model.bend", "go.mod", "main_test.go", "test/bend/coverage.json"]
        (self.root / "LAWS.bend").write_text("law safe:\n  {0n == 0n : Nat}\n")
        (self.root / "PROOF.bend").write_text("import ./LAWS.bend as Laws\nimport ./test/bend/model.bend as M\ndef Laws.safe():\n  {==}\n")
        (self.root / "test/bend/model.bend").write_text("import Base\n")
        (self.root / "main_test.go").write_text("func TestSafe(t *testing.T) {}\n")
        (self.root / "go.mod").write_text("module example.test/project\n")
        self.manifest = {"schema": 1, "areas": [{"area": "fixture", "contract": "Preserve safety",
            "paths": self.files[:], "laws": ["safe"], "go_packages": ["."], "evidence": [{"file": "main_test.go", "test": "TestSafe", "module": "main"}]}]}
        self.save()

    def save(self):
        (self.root / "test/bend/coverage.json").write_text(json.dumps(self.manifest))

    def test_valid_inventory(self):
        self.assertEqual(run.inventory(self.root, self.files)[2:], (6, 1))

    def test_new_area_fails(self):
        with self.assertRaisesRegex(ValueError, "expected one contract area"):
            run.inventory(self.root, self.files + ["new/script.sh"])

    def test_new_nested_package_fails(self):
        with self.assertRaisesRegex(ValueError, "Go package inventory mismatch"):
            run.inventory(self.root, self.files + ["internal/new/code.go"])

    def test_orphaned_bend_module_fails(self):
        with self.assertRaisesRegex(ValueError, "outside the proof graph"):
            run.inventory(self.root, self.files + ["unproved.bend"])

    def test_missing_proof_fails(self):
        (self.root / "PROOF.bend").write_text("")
        with self.assertRaisesRegex(ValueError, "match exactly"):
            run.inventory(self.root, self.files)

    def test_stale_evidence_fails(self):
        (self.root / "main_test.go").write_text("func TestRenamed(t *testing.T) {}\n")
        with self.assertRaisesRegex(ValueError, "missing named test"):
            run.inventory(self.root, self.files)

    def test_unsafe_model_fails(self):
        (self.root / "test/bend/model.bend").write_text("@unsafe def loop(): loop()\n")
        with self.assertRaisesRegex(ValueError, "unsafe"):
            run.inventory(self.root, self.files)

    def test_skipped_evidence_fails(self):
        log = self.root / "test.log"
        evidence = self.manifest["areas"][0]["evidence"]
        with patch.object(run, "ROOT", self.root):
            log.write_text(json.dumps({"Action": "skip", "Package": "example.test/project", "Test": "TestSafe"}) + "\n")
            with self.assertRaisesRegex(ValueError, "did not run and pass"):
                run.check_evidence(evidence, {"main": log})
            log.write_text(json.dumps({"Action": "pass", "Package": "example.test/project", "Test": "TestSafe"}) + "\n")
            run.check_evidence(evidence, {"main": log})

    def test_scope_does_not_swallow_nested_packages(self):
        self.assertFalse(run.scope_matches("cmd/nomctl-relay/main.go", "cmd/*.go"))
        self.assertTrue(run.scope_matches("internal/node/client.go", "internal/node/**"))


class InstallerTests(unittest.TestCase):
    def setUp(self):
        # Only the isolated test container runs this suite as root. The root
        # filesystem and project are read-only; mock downloads stay in /tmp.
        if os.geteuid() != 0:
            self.fail("installer tests must run in the isolated root test container")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.bin = self.base / "bin"
        self.bin.mkdir()
        self.destination = self.base / "installed"
        self.destination.mkdir()
        self.archive = "nomctl_1.2.3_linux_arm64.tar.gz"
        payload = b"#!/bin/sh\necho fixture-version\n"
        with tarfile.open(self.base / self.archive, "w:gz") as tar:
            info = tarfile.TarInfo("nomctl")
            info.size, info.mode = len(payload), 0o755
            tar.addfile(info, io.BytesIO(payload))
        digest = hashlib.sha256((self.base / self.archive).read_bytes()).hexdigest()
        (self.base / "checksums.txt").write_text(f"{digest}  {self.archive}\n")
        self.mock("uname", '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo aarch64;; esac\n')
        self.mock("curl", '''#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in -o) out=$2; shift 2;; -*) shift;; *) url=$1; shift;; esac
done
cp "$FIXTURE_DIR/${url##*/}" "$out"
''')
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"],
                        FIXTURE_DIR=str(self.base), NOMCTL_VERSION="v1.2.3",
                        NOMCTL_INSTALL_DIR=str(self.destination))

    def mock(self, name, source):
        path = self.bin / name
        path.write_text(source)
        path.chmod(0o755)

    def install(self):
        return subprocess.run(["bash", str(run.ROOT / "install.sh")], env=self.env,
                              capture_output=True, text=True, timeout=10)

    def test_verified_archive_installs(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.destination / "nomctl").is_file())

    def test_checksum_mismatch_cannot_install(self):
        (self.base / "checksums.txt").write_text("0" * 64 + "  " + self.archive + "\n")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", (result.stdout + result.stderr).lower())
        self.assertFalse((self.destination / "nomctl").exists())

    def test_missing_checksum_cannot_install(self):
        (self.base / "checksums.txt").write_text("")
        self.assertNotEqual(self.install().returncode, 0)
        self.assertFalse((self.destination / "nomctl").exists())


if __name__ == "__main__":
    unittest.main()
