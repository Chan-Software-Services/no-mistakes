#!/usr/bin/env python3
"""Drive the no-mistakes TUI through a real pty and press the yolo key.

argv: <no-mistakes binary> <workdir> <keys> <ready-text> <ready-deadline-seconds> <seconds-after-keys>

The keys are sent once the screen shows <ready-text> (the run's branch), which
the TUI draws only after it has started and is reading its input: a key sent
before then is dropped and yolo never engages. A screen that never shows it by
the deadline exits 2 with what was drawn. The seconds after the keys are the
window in which a wrongly sent response would land, so they stay fixed.

The driver answers the terminal queries the TUI sends at startup, as an
operator's terminal does: the background-colour query (OSC 11, answered with a
dark background) and the cursor-position report (CSI 6n). Left unanswered, each
waits out termenv's 5s timeout before the TUI draws anything.

The pty is given a non-zero window size before the TUI reads its grid: a 0x0
grid makes bubbletea exit immediately, which would turn this live check into a
fake. The master side is drained continuously and the final screen is written
to stdout.
"""
import fcntl, os, pty, select, signal, struct, sys, termios, time

binary, workdir, keys, ready, ready_deadline, post = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4].encode(), float(sys.argv[5]), float(sys.argv[6])

pid, fd = pty.fork()
if pid == 0:
    os.chdir(workdir)
    os.environ["TERM"] = "xterm-256color"
    os.execv(binary, [binary, "attach"])

fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 45, 160, 0, 0))

buf = bytearray()

# Each terminal query the TUI may send and the answer a dark terminal gives.
ANSWERS = (
    (b"\x1b]11;?\x1b\\", b"\x1b]11;rgb:0000/0000/0000\x1b\\"),
    (b"\x1b[6n", b"\x1b[1;1R"),
)
answered = [0] * len(ANSWERS)

def answer_queries():
    for i, (query, reply) in enumerate(ANSWERS):
        while buf.count(query) > answered[i]:
            os.write(fd, reply)
            answered[i] += 1

def drain(seconds, until=None):
    end = time.time() + seconds
    while time.time() < end:
        if until is not None and until in buf:
            return True
        r, _, _ = select.select([fd], [], [], 0.2)
        if fd in r:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                return
            if not chunk:
                return
            buf.extend(chunk)
            answer_queries()
    return until is not None and until in buf

def stop():
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    os.waitpid(pid, 0)

if not drain(ready_deadline, until=ready):
    stop()
    sys.stdout.write(buf.decode("utf-8", "replace"))
    sys.stderr.write("the screen never showed %r within %ss\n" % (ready.decode(), ready_deadline))
    sys.exit(2)
for key in keys:
    os.write(fd, key.encode())
    drain(0.4)
drain(post)
os.write(fd, b"q")
drain(1.0)
stop()
sys.stdout.write(buf.decode("utf-8", "replace"))
