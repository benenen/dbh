"""Model the ANSI controls used by the ASCII prompt layout regression tests."""

import re


class TerminalScreen:
    def __init__(self, width=100, height=24, start_row=0):
        self.width, self.height = width, height
        self.x, self.y = 0, start_row
        self.rows = [[" "] * width for _ in range(height)]

    def newline(self):
        if self.y == self.height - 1:
            self.rows.pop(0)
            self.rows.append([" "] * self.width)
        else:
            self.y += 1

    def feed(self, text):
        for token in re.findall(r"\x1b\[[0-?]*[ -/]*[@-~]|.", text, re.DOTALL):
            if token.startswith("\x1b["):
                self.control(token)
            elif token == "\r":
                self.x = 0
            elif token == "\n":
                self.newline()
            elif token == "\x07":
                continue
            else:
                if ord(token) < 32:
                    raise AssertionError(f"Unsupported terminal character: {token!r}")
                if self.x == self.width:
                    self.x = 0
                    self.newline()
                self.rows[self.y][self.x] = token
                self.x += 1

    def control(self, token):
        params, command = token[2:-1], token[-1]
        if command in "mhlq":  # Colors, modes, cursor visibility and shape.
            return
        value = int(params or "0")
        steps = value or 1
        if command == "A":
            self.y = max(0, self.y - steps)
        elif command == "B":
            self.y = min(self.height - 1, self.y + steps)
        elif command == "C":
            self.x = min(self.width - 1, self.x + steps)
        elif command == "D":
            self.x = max(0, self.x - steps)
        elif command == "K" and value == 0:
            self.rows[self.y][self.x:] = [" "] * (self.width - self.x)
        elif command == "K" and value == 1:
            self.rows[self.y][:self.x + 1] = [" "] * (self.x + 1)
        elif command == "J" and value == 0:
            self.rows[self.y][self.x:] = [" "] * (self.width - self.x)
            for row in range(self.y + 1, self.height):
                self.rows[row] = [" "] * self.width
        else:
            raise AssertionError(f"Unsupported terminal control: {token!r}")

    def line(self, row):
        return "".join(self.rows[row]).rstrip()
