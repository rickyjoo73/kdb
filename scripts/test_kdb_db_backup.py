"""Backup failure tests. Uses a fake Docker executable; never connects to a DB."""

import gzip
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest


SCRIPT = Path(__file__).with_name("kdb-db-backup.sh")


class BackupTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.project = self.root / "moved project's checkout"
        scripts = self.project / "scripts"
        scripts.mkdir(parents=True)
        self.script = scripts / SCRIPT.name
        shutil.copy2(SCRIPT, self.script)
        self.backups = self.project / "backups"
        self.backups.mkdir()
        self.old = self.backups / "kdb-20000101-000000.sql.gz"
        self.old.write_bytes(b"preserve existing recovery point on failure")
        old_time = time.time() - 30 * 86400
        os.utime(self.old, (old_time, old_time))
        self.payload = self.root / "payload.sql"
        self.payload.write_bytes(os.urandom(16384))
        self.docker = self.root / "docker"
        self.docker.write_text(
            '#!/bin/bash\n'
            'if [[ " $* " == *" pg_dump "* ]]; then\n'
            '  cat "$FAKE_PAYLOAD"\n'
            '  if [[ "$FAKE_MODE" == dump_failure ]]; then\n'
            '    echo "pg_dump: simulated failure after writing data" >&2\n'
            '    exit 7\n'
            '  fi\n'
            'elif [[ " $* " == *" psql "* ]]; then\n'
            '  printf "%s\\n" "$@" > "$FAKE_ARGS"\n'
            '  cat > "$FAKE_SQL"\n'
            '  if [[ "$FAKE_MODE" == log_failure ]]; then\n'
            '    echo "ERROR: simulated DB log failure" >&2\n'
            '    exit 8\n'
            '  fi\n'
            'else\n'
            '  exit 9\n'
            'fi\n'
        )
        self.docker.chmod(0o700)

    def run_backup(self, mode="ok", minimum=1000):
        env = os.environ.copy()
        for key in list(env):
            if key.startswith("KDB_BACKUP_"):
                del env[key]
        env.update(
            KDB_BACKUP_DOCKER_BIN=str(self.docker),
            KDB_BACKUP_MIN_BYTES=str(minimum),
            FAKE_MODE=mode,
            FAKE_PAYLOAD=str(self.payload),
            FAKE_ARGS=str(self.root / "psql-args"),
            FAKE_SQL=str(self.root / "psql.sql"),
        )
        return subprocess.run(
            ["bash", str(self.script)],
            cwd=self.root, env=env, text=True, capture_output=True, timeout=20,
        )

    def new_backups(self):
        return [p for p in self.backups.glob("kdb-*.sql.gz") if p != self.old]

    def test_success_uses_checkout_path_and_records_before_retention(self):
        result = self.run_backup()
        self.assertEqual(result.returncode, 0, result.stderr)
        [backup] = self.new_backups()
        self.assertEqual(gzip.decompress(backup.read_bytes()), self.payload.read_bytes())
        self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
        self.assertFalse(self.old.exists())
        args = (self.root / "psql-args").read_text().splitlines()
        self.assertIn("ON_ERROR_STOP=1", args)
        self.assertIn(f"backup_file={backup}", args)
        sql = (self.root / "psql.sql").read_text()
        self.assertIn(":'backup_file'", sql)
        self.assertIn("OK ", (self.backups / "backup.log").read_text())

    def test_partial_dump_failure_is_not_hidden_by_successful_gzip(self):
        result = self.run_backup("dump_failure")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.new_backups(), [])
        self.assertEqual(list(self.backups.glob("*.partial")), [])
        self.assertTrue(self.old.exists())
        self.assertFalse((self.root / "psql-args").exists())
        self.assertIn("FAIL pg_dump/gzip failed", result.stderr)

    def test_small_dump_is_rejected_without_removing_previous_backup(self):
        result = self.run_backup(minimum=1000000)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.new_backups(), [])
        self.assertTrue(self.old.exists())
        self.assertFalse((self.root / "psql-args").exists())
        self.assertIn("dump too small", result.stderr)

    def test_db_log_failure_is_reported_and_both_backups_are_preserved(self):
        result = self.run_backup("log_failure")
        self.assertNotEqual(result.returncode, 0)
        [backup] = self.new_backups()
        self.assertEqual(gzip.decompress(backup.read_bytes()), self.payload.read_bytes())
        self.assertTrue(self.old.exists())
        self.assertIn("backup saved but DB log failed", result.stderr)
        self.assertNotIn("OK ", (self.backups / "backup.log").read_text())


if __name__ == "__main__":
    unittest.main()
