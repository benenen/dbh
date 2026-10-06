"""Exercise the built CLI and real terminal input using only the standard library."""

import errno
import json
import os
from pathlib import Path
import select
import sqlite3
import subprocess
import tempfile
import time
import unittest

from terminal_screen import TerminalScreen

if os.name == "posix":
    import fcntl
    import pty
    import struct
    import termios


BINARY = Path(__file__).resolve().parents[1] / "bin" / "dbh"


class Terminal:
    def __init__(self, args, env):
        self.master, slave = pty.openpty()
        try:
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 100, 0, 0))
            self.process = subprocess.Popen(
                args, stdin=slave, stdout=slave, stderr=slave,
                env=env, start_new_session=True,
            )
        except BaseException:
            os.close(self.master)
            raise
        finally:
            os.close(slave)
        self.transcript = ""

    def exchange(self, text="", expected=None):
        if text:
            os.write(self.master, text.encode())
        output = b""
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            ready, _, _ = select.select([self.master], [], [], 0.15)
            if not ready:
                if output and (expected is None or expected in output.decode(errors="replace")):
                    break
                continue
            try:
                chunk = os.read(self.master, 65536)
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
                break
            if not chunk:
                break
            output += chunk
        result = output.decode(errors="replace")
        self.transcript += result
        if expected is not None and expected not in result:
            raise AssertionError(
                f"Terminal did not show {expected!r} after {text!r}:\n{self.transcript!r}"
            )
        return result

    def exit(self, text="\\q\r"):
        self.exchange(text)
        code = self.process.wait(timeout=5)
        if code != 0:
            raise AssertionError(f"Terminal exited with {code}: {self.transcript!r}")

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=2)
        os.close(self.master)


class CLIEndToEnd(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="dbh-e2e-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.config = self.root / "config"
        self.database = self.root / "demo.db"
        self.env = {
            **os.environ,
            "DBH_CONFIG_DIR": str(self.config),
            "HOME": str(self.root),
            "XDG_CONFIG_HOME": str(self.root / "xdg"),
            "INPUTRC": str(self.root / "empty-inputrc"),
            "TERM": "xterm-256color",
        }
        self.env.pop("NO_COLOR", None)
        (self.root / "empty-inputrc").touch()
        self.run_cli("n", "demo", "--driver", "sqlite", "--dsn", str(self.database))

    def run_cli(self, *args, stdin="", success=True, env=None):
        result = subprocess.run(
            [str(BINARY), *args], input=stdin, capture_output=True,
            text=True, timeout=10, env=self.env if env is None else env,
        )
        if success:
            self.assertEqual(result.returncode, 0, (args, result.stdout, result.stderr))
            self.assertEqual(result.stderr, "", (args, result.stderr))
        else:
            self.assertNotEqual(result.returncode, 0, (args, result.stdout))
            self.assertEqual(result.stdout, "", (args, result.stdout))
            self.assertIn("Error:", result.stderr)
        return result

    def sql(self, query, *args, success=True):
        return self.run_cli("c", "demo", "--sql", query, *args, success=success)

    def seed(self):
        self.sql(
            "CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); "
            "INSERT INTO users(name) VALUES ('Alice');", "--no-history",
        )

    def history(self):
        path = self.config / "history" / "demo.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()]

    def terminal(self, *args):
        terminal = Terminal([str(BINARY), "c", "demo", "--format", "json", *args], self.env)
        self.addCleanup(terminal.close)
        terminal.exchange(expected="demo> ")
        return terminal

    def test_connection_lifecycle_and_aliases(self):
        listing = self.run_cli("list").stdout
        self.assertEqual(listing, self.run_cli("ls").stdout)
        self.assertIn("demo", listing)
        self.assertNotIn(str(self.database), listing)
        self.run_cli("new", "demo", "--driver", "sqlite", "--dsn", ":memory:", success=False)
        self.run_cli("new", "../escape", "--driver", "sqlite", "--dsn", ":memory:", success=False)
        self.assertFalse((self.root / "escape").exists())
        self.seed()
        self.run_cli("edit", "demo", "--dsn", ":memory:")
        self.sql("SELECT * FROM users", success=False)
        self.run_cli("e", "demo", "--dsn", str(self.database))
        self.assertEqual(self.sql("SELECT name FROM users", "--format", "json").stdout,
                         '{"name":"Alice"}\n')
        for command in ("remove", "rm", "r"):
            with self.subTest(command=command):
                self.run_cli(command, "demo")
                self.sql("SELECT 1", success=False)
                self.assertTrue(self.database.is_file())
                self.run_cli("new", "demo", "--driver", "sqlite", "--dsn", str(self.database))
        if os.name == "posix":
            self.assertEqual(self.config.stat().st_mode & 0o777, 0o700)
            self.assertEqual((self.config / "connections.json").stat().st_mode & 0o777, 0o600)

    def test_environment_dsn_and_config_override(self):
        other = self.root / "other-config"
        env = {**self.env, "DBH_E2E_DSN": str(self.database)}
        self.run_cli("--config-dir", str(other), "new", "env-test", "--driver", "sqlite",
                     "--dsn-env", "DBH_E2E_DSN", env=env)
        self.assertNotIn("env-test", self.run_cli("ls").stdout)
        result = self.run_cli("--config-dir", str(other), "connect", "env-test",
                              "--sql", "SELECT 7 AS value", "--format", "json")
        self.assertEqual(result.stdout, '{"value":7}\n')
        self.run_cli("new", "missing", "--driver", "sqlite", "--dsn-env",
                     "DBH_E2E_UNSET", env={k: v for k, v in self.env.items() if k != "DBH_E2E_UNSET"},
                     success=False)

    def test_sql_inputs_formats_and_transactions(self):
        self.assertEqual(self.sql(
            "CREATE TABLE values_test(value TEXT); INSERT INTO values_test VALUES ('a;b'); "
            "BEGIN; INSERT INTO values_test VALUES ('rolled back'); ROLLBACK; "
            "SELECT value, NULL AS empty FROM values_test;", "--format", "json",
        ).stdout, '{"empty":null,"value":"a;b"}\n')
        query = "SELECT value FROM values_test;"
        file = self.root / "query.sql"
        file.write_text(query)
        for args, stdin in (
            (("--file", str(file)), ""),
            (("--file", "-"), query),
            ((), query),
        ):
            with self.subTest(args=args):
                self.assertEqual(self.run_cli("connect", "demo", *args, "--format", "csv",
                                              stdin=stdin).stdout, "value\na;b\n")
        table = self.sql(query).stdout
        self.assertIn("a;b", table)
        self.assertIn("(1 rows)", table)

    def test_batch_error_stops_remaining_statements(self):
        self.seed()
        result = self.sql("SELECT * FROM missing; INSERT INTO users VALUES (2, 'unexpected');",
                          success=False)
        self.assertIn("missing", result.stderr)
        with sqlite3.connect(self.database) as database:
            self.assertEqual(database.execute("SELECT name FROM users").fetchall(), [("Alice",)])
        self.sql("SELECT 'unfinished", success=False)
        self.sql("SELECT 1", "--format", "invalid", success=False)
        self.run_cli("connect", "demo", "--sql", "SELECT 1", "--file", "-", success=False)

    def test_history_multiline_and_no_history(self):
        query = "SELECT 'first\nsecond;third' AS value;"
        self.sql(query, "--format", "json")
        self.assertEqual(self.history(), [query])
        path = self.config / "history" / "demo.jsonl"
        before = path.read_bytes()
        self.sql("SELECT 99", "--no-history")
        self.assertEqual(path.read_bytes(), before)
        self.assertIn(query, self.run_cli("history", "demo").stdout)
        if os.name == "posix":
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_bracketed_paste_is_editable_before_execution(self):
        self.seed()
        terminal = self.terminal()
        self.assertIn("\x1b[?2004h", terminal.transcript)
        for newline in ("\n", "\r\n", "\r"):
            with self.subTest(newline=repr(newline)):
                query = "SELECT\n    '你好;粘贴' AS value;\n"
                output = terminal.exchange("\x1b[200~" + query.replace("\n", newline) + "\x1b[201~")
                self.assertNotIn('{"value":', output)
                self.assertFalse((self.config / "history" / "demo.jsonl").exists())
                terminal.exchange("\x03", "demo> ")
        # Paste a write and a query: neither may run until the user presses Enter.
        query = "INSERT INTO users(name)\n    VALUES ('粘贴');\nSELECT name\n    FROM users WHERE id = 2;\n"
        terminal.exchange("\x1b[200~" + query + "\x1b[201~")
        with sqlite3.connect(self.database) as database:
            self.assertEqual(database.execute("SELECT COUNT(*) FROM users").fetchone(), (1,))
        # Up edits the previous line of the same buffer; it must not recall history.
        terminal.exchange("\x1b[A\x05")
        terminal.exchange("\x7f")
        terminal.exchange("\x7f")
        terminal.exchange("3;\x1b[B")
        terminal.exchange("\r", "demo> ")
        with sqlite3.connect(self.database) as database:
            self.assertEqual(database.execute("SELECT name FROM users ORDER BY id").fetchall(),
                             [("Alice",), ("粘贴",)])
        self.assertEqual(self.history(), ["INSERT INTO users(name)\n    VALUES ('粘贴');",
                                         "SELECT name\n    FROM users WHERE id = 3;"])
        terminal.exit()

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_paste_line_endings_and_literal_commands(self):
        terminal = self.terminal()
        query = "SELECT\n    '你好;\n\\q\n缩进' AS value;\n"
        expected = json.dumps({"value": "你好;\n\\q\n缩进"}, ensure_ascii=False, separators=(",", ":"))
        for newline in ("\n", "\r\n", "\r"):
            with self.subTest(newline=repr(newline)):
                terminal.exchange("\x1b[200~" + query.replace("\n", newline) + "\x1b[201~")
                terminal.exchange("\r", expected)
        terminal.exit()
        self.assertEqual(self.history(), [query.rstrip()] * 3)

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_cursor_report_does_not_block_input(self):
        self.env["TERM"] = "xterm-ghostty"
        self.env["TERM_PROGRAM"] = "ghostty"
        terminal = self.terminal()
        # A terminal-only report must not stop the reader before the next read.
        os.write(terminal.master, b"\x1b[1;7R")
        time.sleep(0.1)
        # Another reply fills the same channel; preserve coalesced user input.
        terminal.exchange("\x1b[1;7RSELECT 42 AS value;\r", '{"value":42}')
        terminal.exchange("\x03", "demo> ")
        terminal.exit()

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_prompt_at_bottom_keeps_cursor_and_input_aligned(self):
        terminal = self.terminal("--no-history")

        def check_screen(suffix, prefix="demo>"):
            for start_row in (0, 22, 23):
                with self.subTest(start_row=start_row, suffix=suffix):
                    screen = TerminalScreen(start_row=start_row)
                    screen.feed(terminal.transcript)
                    self.assertEqual(screen.line(screen.y), prefix + suffix)
                    self.assertEqual(screen.x, len("demo> ") + len(suffix.strip()))
                    self.assertTrue(any("Connected to demo" in screen.line(row)
                                        for row in range(screen.height)))

        check_screen("")
        terminal.exchange("se", "SELECT")
        check_screen(" se")
        terminal.exchange("\x03", "demo> ")
        check_screen("")
        terminal.exchange("SELECT 42 AS value;\r", '{"value":42}')
        check_screen("")
        terminal.exchange("SELECT\r", "...> ")
        check_screen("", prefix="...>")
        terminal.exchange("43 AS value;\r", '{"value":43}')
        check_screen("")
        terminal.exit()

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_prompt_colors_and_commands_during_input(self):
        self.seed()
        terminal = self.terminal()
        self.assertIn("\x1b[36m", terminal.transcript)
        terminal.exchange("SELECT\r", "...> ")
        terminal.exchange("\\tables\r", "users")
        terminal.exchange("name FROM users;\r", '{"name":"Alice"}')
        terminal.exchange("SELECT\r", "...> ")
        terminal.exchange("\\clear\r", "demo> ")
        terminal.exchange("SELECT 42 AS value;\r", '{"value":42}')
        terminal.exit()
        self.assertEqual(self.history(), ["SELECT\nname FROM users;", "SELECT 42 AS value;"])
        self.env["NO_COLOR"] = "1"
        terminal = self.terminal()
        self.assertNotIn("\x1b[36m", terminal.transcript)
        terminal.exit()

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_manual_newline_keeps_whole_buffer(self):
        terminal = self.terminal()
        terminal.exchange("SELECT 6 AS value;\x1b\r", "...> ")
        self.assertFalse((self.config / "history" / "demo.jsonl").exists())
        terminal.exchange("SELECT 8 AS value;\r", '{"value":8}')
        terminal.exit()
        self.assertEqual(self.history(), ["SELECT 6 AS value;", "SELECT 8 AS value;"])

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_live_completion_and_interrupt(self):
        self.seed()
        terminal = self.terminal()
        terminal.exchange("sel", "SELECT")  # No Tab: suggestions must appear while typing.
        terminal.exchange("\t ")
        terminal.exchange("na", "name")
        terminal.exchange("\t FROM ")
        terminal.exchange("us", "users")
        terminal.exchange("\t;\r", '{"name":"Alice"}')
        terminal.exchange("se\t")
        terminal.exchange("\x03", "demo> ")
        terminal.exchange("SELECT 41 AS value;\r", '{"value":41}')
        terminal.exit()
        self.assertEqual(self.history(), ["SELECT name FROM users;", "SELECT 41 AS value;"])

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_persisted_history_and_search(self):
        self.seed()
        terminal = self.terminal()
        terminal.exchange("SELECT name FROM users;\r", '{"name":"Alice"}')
        terminal.exit()
        terminal = self.terminal()
        ghost = terminal.exchange("SELECT na", "FROM users;")
        self.assertIn("\x1b[2m", ghost)
        terminal.exchange("\x1b[C\r", '{"name":"Alice"}')
        terminal.exchange("\x12name", "SQL history")
        terminal.exchange("\x03", "demo> ")
        terminal.exchange("SELECT 41 AS value;\r", '{"value":41}')
        terminal.exchange("\x1b[A\r", '{"value":41}')
        terminal.exit()
        path = self.config / "history" / "demo.jsonl"
        before = path.read_bytes()
        terminal = self.terminal("--no-history")
        self.assertNotIn("FROM users;", terminal.exchange("SELECT na", "SELECT na"))
        terminal.exchange("\x03", "demo> ")
        terminal.exchange("SELECT 99 AS value;\r", '{"value":99}')
        terminal.exit()
        self.assertEqual(path.read_bytes(), before)

    @unittest.skipUnless(os.name == "posix", "PTY tests require Linux or macOS")
    def test_terminal_multiline_error_recovery_and_eof(self):
        self.seed()
        terminal = self.terminal()
        terminal.exchange("SELECT\r", "...> ")
        terminal.exchange("name FROM users;\r", '{"name":"Alice"}')
        terminal.exchange("SELECT * FROM missing;\r", "SQL error:")
        terminal.exchange("SELECT\r", "...> ")
        terminal.exchange("\x03", "demo> ")
        terminal.exchange("SELECT 9 AS value;\r", '{"value":9}')
        terminal.exchange("\\tables\r", "users")
        terminal.exit("\x04")
        self.assertEqual(self.history(), ["SELECT\nname FROM users;",
                                          "SELECT * FROM missing;", "SELECT 9 AS value;"])


if __name__ == "__main__":
    if not BINARY.is_file():
        raise SystemExit("Build the CLI first with 'make build', or run 'make e2e'.")
    unittest.main(verbosity=2)
