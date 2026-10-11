#!/usr/bin/env python3
"""Records the tour that the README shows, as it happens.

Each step is a command line that is handed to bash exactly as it is shown, on a PC where
wslbak is really installed and a demo distro exists. What the command prints is written to a
JSON file together with the time each piece arrived; scripts/demo-render.py draws that file.
Nothing is replaced, hidden or added: the text in the pictures is the text that was printed,
and the commands in the pictures are the commands that ran. Questions are answered through
the command's input, and the answers are recorded as what was typed. The output is read
through pipes, so the progress line that wslbak redraws on a console is not part of it.

wslbak asks its questions on stderr and prints everything else on stdout, and the two streams
of a Windows program reach WSL through separate channels, in no fixed order: a question can
arrive before the lines that were printed just before it. On a console the question stands
after them, and so it does here: what comes from stderr is put into the recording once both
streams have been quiet for a moment. By then everything printed before the question has
arrived, because the program is waiting for the answer and prints nothing until it gets one.

If a step fails, the recording stops and nothing is drawn.

Usage (from scripts/demo.sh, inside WSL):
  demo-record.py <lang> <demo distro> <out.json>
The language comes from WSLBAK_LANG, which scripts/demo.sh sets; PATH must lead to wslbak,
and DEMO_SETTINGS is the path of the settings file that init writes.
"""

import codecs
import json
import os
import re
import select
import subprocess
import sys
import time

lang, distro, out_path = sys.argv[1:4]

TITLES = {
    'en': {
        'init': 'Set it up once',
        'mode': 'Optional: on most days, store only what changed',
        'run': 'Back up now. The distro keeps running.',
        'files': 'Look inside a backup',
        'oops': 'A file gets deleted',
        'gone': 'It is gone',
        'path': 'Bring back just that file',
        'cat': 'It is back, in a new folder',
        'diff': 'The next backup holds only what changed',
        'list': 'A full backup and a differential',
        'restore': 'Or restore the whole distro, as a new one',
        'status': 'From now on it runs every day',
    },
    'zh-TW': {
        'init': '設定一次就好',
        'mode': '可以選擇：平常只存變過的部分',
        'run': '立刻備份，distro 照常執行',
        'files': '看備份裡有什麼',
        'oops': '不小心刪掉一個檔案',
        'gone': '檔案不見了',
        'path': '只取回那個檔案',
        'cat': '檔案回來了，放在新的資料夾',
        'diff': '下一次備份只存變過的部分',
        'list': '一份完整備份，一份差異備份',
        'restore': '或把整個 distro 還原成新的',
        'status': '之後每天自動備份',
    },
}[lang]

# A question: the output stops, without a newline, at "… [y/N] ", "…: " or the Chinese "…：".
QUESTION = re.compile(r'(\] |: |：)\Z')
NUMBERED = re.compile(r'^\s*\[(\d+)\]\s+' + re.escape(distro) + r'\s*$', re.M)


def windows_programs_start():
    """All WSL2 distros share one kernel, and on some systems that kernel forgets how to start
    Windows programs whenever a distro with systemd stops (scripts/e2e-distro.sh says more).
    The tour starts and stops distros, so the handler is put back before every step. That is
    preparation, like installing the demo distro, and no part of what is recorded."""
    subprocess.run(['bash', '-c', '. scripts/e2e-distro.sh && ensure_interop'],
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def record(title, command, yes_no=()):
    """Runs the command line with bash and answers its questions the way a user would:
    a yes-or-no question with the next of the given answers; "which distro?" (asked when more
    than one can be backed up) with the number printed in front of the demo distro; and a
    question that offers a default (where the backups go) by pressing Enter."""
    windows_programs_start()
    proc = subprocess.Popen(['bash', '-c', command], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    began = time.monotonic()
    events, pending = [], list(yes_no)
    streams = {proc.stdout.fileno(): 'stdout', proc.stderr.fileno(): 'stderr'}
    decode = {fd: codecs.getincrementaldecoder('utf-8')('replace').decode for fd in streams}
    held = ''  # from stderr, not yet in the recording
    since_answer = ''  # everything printed since the last answer

    def put(kind, text):
        events.append([round(time.monotonic() - began, 3), kind, text])

    while streams:
        ready = select.select(list(streams), [], [], 0.7)[0]
        for fd in ready:
            chunk = os.read(fd, 4096)
            if not chunk:
                del streams[fd]
                continue
            text = decode[fd](chunk).replace('\r', '')
            since_answer += text
            if streams[fd] == 'stdout':
                put('out', text)
            else:
                held += text
        if (ready and streams) or not held:
            continue
        # Both streams are quiet, or the command has ended: what stderr said goes in now.
        asked, held = held, ''
        put('out', asked)
        if not streams or not QUESTION.search(asked):
            continue
        question = asked.rsplit('\n', 1)[-1]
        if '[y/N]' in question:
            if not pending:
                raise SystemExit('a question that was not expected: ' + question)
            answer = pending.pop(0)
        elif NUMBERED.search(since_answer):
            answer = NUMBERED.findall(since_answer)[-1]
        elif 'Enter' in question:
            answer = ''
        else:
            raise SystemExit('a question the recorder does not know: ' + question)
        time.sleep(0.4)
        put('in', answer)
        proc.stdin.write((answer + '\n').encode())
        proc.stdin.flush()
        since_answer = ''
    proc.stdin.close()
    rc = proc.wait()
    step = {'title': title, 'command': command, 'events': events, 'seconds': round(time.monotonic() - began, 2), 'rc': rc}
    print('%-46s rc=%d  %.1fs' % (command[:46], rc, step['seconds']), file=sys.stderr)
    if pending:
        raise SystemExit('a question that was expected never came: ' + command)
    if rc != 0:
        raise SystemExit('this step failed, so the tour stops here:\n' + ''.join(t for _, k, t in events if k == 'out'))
    return step


def only_the_demo_is_set_up():
    """Before anything is backed up: the settings must name the demo distro and no other."""
    settings = json.load(open(os.environ['DEMO_SETTINGS'], encoding='utf-8'))
    names = sorted(settings['distros'])
    if names != [distro]:
        raise SystemExit('init set up %s, not just %s; stopping before any backup' % (names, distro))


def a_differential(step):
    """The second backup of the tour: it has to be a differential, or the tour would not show
    what the title of this step says."""
    printed = ''.join(text for _, kind, text in step['events'] if kind == 'out')
    if '.diff.tar.gz' not in printed:
        raise SystemExit('the second backup is not a differential, so the tour stops here:\n' + printed)
    return step


steps = []
steps.append(record(TITLES['init'], 'wslbak init', yes_no=['y', 'n']))
only_the_demo_is_set_up()
steps.append(record(TITLES['mode'], 'wslbak config --mode diff'))
steps.append(record(TITLES['run'], 'wslbak run'))
steps.append(record(TITLES['files'], 'wslbak files /home/me/project'))
steps.append(record(TITLES['oops'], 'wsl.exe -d %s rm /home/me/project/notes.md' % distro))
steps.append(record(TITLES['gone'], 'wsl.exe -d %s ls /home/me/project' % distro))
steps.append(record(TITLES['path'], 'wslbak restore --path /home/me/project/notes.md --into /home/me/recovered', yes_no=['y']))
steps.append(record(TITLES['cat'], 'wsl.exe -d %s cat /home/me/recovered/home/me/project/notes.md' % distro))
steps.append(a_differential(record(TITLES['diff'], 'wslbak run')))
steps.append(record(TITLES['list'], 'wslbak list'))
steps.append(record(TITLES['restore'], 'wslbak restore', yes_no=['y']))
steps.append(record(TITLES['status'], 'wslbak status'))

json.dump({'lang': lang, 'steps': steps}, open(out_path, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
