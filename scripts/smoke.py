#!/usr/bin/env python3
"""Drive numbered shell menus through a real PTY, using only isolated test data."""
import errno
import json
import os
from pathlib import Path
import pty
import select
import signal
import sys
import tempfile
import termios
import time


class Session:
    def __init__(self, binary, env):
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.execve(binary, [binary], env)
        self.pending = b""
        self.history = b""
        self.reaped = False
        self.original = termios.tcgetattr(self.fd)

    def read(self, timeout=0.1):
        if not select.select([self.fd], [], [], timeout)[0]:
            return b""
        try:
            data = os.read(self.fd, 65536)
        except OSError as error:
            if error.errno == errno.EIO:
                return b""
            raise
        self.pending += data
        self.history += data
        return data

    def prompt(self, expected=None):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if self.pending.rstrip().endswith(b">"):
                text = self.pending.decode("utf-8", "replace")
                self.pending = b""
                if expected is not None:
                    assert expected in text, "Unexpected prompt: " + text
                return text
            self.read()
        raise AssertionError("Prompt timeout: " + self.pending.decode("utf-8", "replace"))

    def answer(self, text, expected=None):
        os.write(self.fd, text.encode() + b"\r")
        return self.prompt(expected)

    def secret(self, text, expected=None):
        deadline = time.monotonic() + 3
        while termios.tcgetattr(self.fd)[3] & termios.ECHO:
            assert time.monotonic() < deadline, "Secret prompt did not disable echo"
            time.sleep(0.01)
        return self.answer(text, expected)

    def finish(self, expected_code=0):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            self.read(0.05)
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.reaped = True
                self.read(0)
                assert os.waitstatus_to_exitcode(status) == expected_code, "Unexpected process exit"
                restored = termios.tcgetattr(self.fd)
                assert restored == self.original, "Terminal mode not restored"
                assert b"\x1b[?1049" not in self.history and b"\x1b[?1047" not in self.history
                assert b"\x1b[2J" not in self.history, "Shell interaction cleared the screen"
                return
        raise AssertionError("Process did not exit")

    def close(self):
        if not self.reaped:
            try:
                os.kill(self.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            os.waitpid(self.pid, 0)
        os.close(self.fd)


def open_form(s, tool, name):
    s.answer("2", "名称")
    s.answer(name, "Base URL")
    s.answer("https://gateway.example.invalid", "模型")
    s.answer("gateway-model", "凭据")
    s.answer("1", "Secret")


def aliases(s, tool):
    if tool == "claude":
        for _ in range(5):
            s.answer("")


def main():
    binary = str(Path(sys.argv[1]).resolve())
    with tempfile.TemporaryDirectory(prefix="aipm-shell-smoke-") as directory:
        base = Path(directory).resolve()
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(base),
               "AIPM_HOME": str(base / "aipm"), "CODEX_HOME": str(base / "codex"),
               "CLAUDE_CONFIG_DIR": str(base / "claude"), "TERM": "xterm-256color", "LANG": "en_US.UTF-8"}
        for name in ("codex", "claude"):
            (base / name).mkdir(mode=0o700)
        codex = base / "codex" / "config.toml"
        claude = base / "claude" / "settings.json"
        codex.write_text('# preserved smoke comment\nmodel = "official-model"\n')
        claude.write_text(json.dumps({"model": "official-model", "env": {"KEEP": "value"}}))
        login = base / "codex" / "auth.json"
        login.write_text("authentication-must-not-change")
        secret = "synthetic-secret-never-echo-this"
        s = Session(binary, env)
        try:
            s.prompt("客户端")
            for number, tool in (("1", "codex"), ("2", "claude")):
                s.answer(number, "操作")
                open_form(s, tool, "smoke-" + tool)
                s.secret(secret)
                aliases(s, tool)
                s.answer("y", "操作")
                result = s.answer("1", "操作")
                assert "smoke-" + tool in result and secret not in result
                s.answer("5", "Provider")
                s.answer("1", "确认执行")
                s.answer("y", "操作")
                s.answer("3", "Provider")
                s.answer("1", "名称")
                s.answer("edited-" + tool, "Base URL")
                s.answer("", "模型")
                s.answer("", "凭据")
                s.answer("", "Secret")
                s.secret("")
                aliases(s, tool)
                s.answer("y", "操作")
                s.answer("6", "确认执行")
                s.answer("y", "操作")
                s.answer("4", "Provider")
                s.answer("1", "确认执行")
                s.answer("y", "操作")
                s.answer("0", "客户端")
            os.write(s.fd, b"0\r")
            s.finish()
            assert secret.encode() not in s.history, "Secret leaked to terminal history"
        finally:
            s.close()
        registry = base / "aipm" / "registry.json"
        assert not json.loads(registry.read_text())["providers"]
        assert '# preserved smoke comment' in codex.read_text() and 'official-model' in codex.read_text()
        settings = json.loads(claude.read_text())
        assert settings["model"] == "official-model" and settings["env"]["KEEP"] == "value"
        assert "ANTHROPIC_BASE_URL" not in settings["env"]
        assert login.read_text() == "authentication-must-not-change"
        for p in [registry, codex, claude, *base.glob("*/.aipm-backup-*")]:
            assert p.stat().st_mode & 0o777 == 0o600
        before = registry.read_bytes()
        # Both terminal Ctrl+C and an external SIGINT must restore mode during a secret read.
        for interrupt in ("key", "signal"):
            s = Session(binary, env)
            try:
                s.prompt("客户端")
                s.answer("1", "操作")
                open_form(s, "codex", "cancelled")
                deadline = time.monotonic() + 3
                while termios.tcgetattr(s.fd)[3] & termios.ECHO:
                    assert time.monotonic() < deadline
                    time.sleep(0.01)
                if interrupt == "key":
                    os.write(s.fd, b"\x03")
                else:
                    os.kill(s.pid, signal.SIGINT)
                s.finish(0 if interrupt == "key" else 130)
            finally:
                s.close()
            assert registry.read_bytes() == before, "Interrupted form wrote configuration"
        print("PASS: shell menus; Codex/Claude CRUD, switch, official restore; no secret echo or alternate screen; Ctrl+C/SIGINT restore terminal")


if __name__ == "__main__":
    main()
