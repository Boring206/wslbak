#!/usr/bin/env python3
"""Records the demo that is shown in the README.

It runs real wslbak commands against a throwaway distro and writes what they printed, with
the time each piece arrived, to a JSON file. scripts/demo-render.py turns that file into
pictures. Nothing here is typed in by hand: the output in the demo is what the program said.

Two things are changed for the viewer, and only in how they are shown:
  - the commands are shown without the options that point them at the test distro and the
    sandbox folder (a user with one distro would not type them);
  - the name of the test distro and the sandbox paths are replaced by the names a user
    would see (the distro really is Debian; the folders are the default ones).

Usage (from scripts/demo.sh, inside WSL):
  demo-record.py <lang> <distro> <home dir> <dest dir> <folder for the restored distro> <out.json>
"""

import json
import os
import re
import subprocess
import sys
import time

lang, distro, home, dest, restored, out_path = sys.argv[1:7]
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
wsl_exe = '/mnt/c/Windows/System32/wsl.exe'


def win(path):
    return subprocess.run(['wslpath', '-w', path], capture_output=True, text=True).stdout.strip()


home_win, dest_win, restored_win = win(home), win(dest), win(restored)
users = home_win.split('\\AppData\\')[0].rsplit('\\', 1)[0]

TITLES = {
    'en': {
        'init': 'Set it up once',
        'run': 'Back up now. The distro keeps running.',
        'list': 'What is there?',
        'files': 'Look inside a backup',
        'oops': 'A file gets deleted',
        'path': 'Bring back just that file',
        'cat': 'It is back, in a new folder',
        'restore': 'Or restore the whole distro, as a new one',
        'status': 'From now on it runs every day',
    },
    'zh-TW': {
        'init': '設定一次就好',
        'run': '立刻備份，distro 照常執行',
        'list': '有哪些備份',
        'files': '看備份裡有什麼',
        'oops': '不小心刪掉一個檔案',
        'path': '只取回那個檔案',
        'cat': '檔案回來了，放在新的資料夾',
        'restore': '或把整個 distro 還原成新的',
        'status': '之後每天自動備份',
    },
}[lang]

# What the viewer sees instead of the test names. Longer strings first.
SHOWN = [
    # The scheduled task of a real installation has no --home option; that is the sandbox's.
    (' --home ' + home_win, ''),
    (home_win + '\\program', users + '\\you\\AppData\\Local\\Programs\\wslbak'),
    (home_win, users + '\\you\\AppData\\Local\\wslbak'),
    (dest_win, 'D:\\WSLBackup'),
    (distro, 'Debian'),
    ('wslbak-sandbox-', 'wslbak-'),
]
SID = re.compile(r'S-1-5-21-[0-9-]+')
RESTORED_NAME = re.compile(r'Debian-restored-[0-9]{8}')


def shown(text):
    for old, new in SHOWN:
        text = text.replace(old, new)
    # A restored distro goes to a folder named after it in the user's WSL folder by default;
    # the recording puts it in the sandbox instead.
    name = RESTORED_NAME.search(text)
    text = text.replace(restored_win, users + '\\you\\WSL\\' + (name.group(0) if name else 'Debian-restored'))
    return SID.sub('S-1-5-21-…', text)


def record(title, command, argv, answers=(), stdin_text=None):
    """Runs argv, feeding one answer each time the output stops at a question."""
    proc = subprocess.Popen(argv, cwd=root, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if stdin_text is not None:
        proc.stdin.write(stdin_text.encode())
        proc.stdin.close()
    began = time.monotonic()
    events, seen, pending = [], '', list(answers)
    fd = proc.stdout.fileno()
    while True:
        chunk = os.read(fd, 4096)
        if not chunk:
            break
        text = chunk.decode('utf-8', 'replace').replace('\r', '')
        events.append([round(time.monotonic() - began, 3), 'out', text])
        seen += text
        if pending and seen.rstrip('\n').endswith('] ') and seen.endswith(' '):
            time.sleep(0.9)
            answer = pending.pop(0)
            events.append([round(time.monotonic() - began, 3), 'in', answer])
            proc.stdin.write((answer + '\n').encode())
            proc.stdin.flush()
    if stdin_text is None:
        proc.stdin.close()
    rc = proc.wait()
    for e in events:
        e[2] = shown(e[2])
    step = {'title': title, 'command': command, 'events': events, 'seconds': round(time.monotonic() - began, 2), 'rc': rc}
    print('%-40s rc=%d  %.1fs' % (command[:40], rc, step['seconds']), file=sys.stderr)
    return step


def wslbak(title, command, *args, answers=()):
    step = record(title, command, ['node', 'bin/wslbak.js', '--lang', lang, '--home', home, *args], answers)
    step['shell'] = 'windows'
    return step


def inside(title, command):
    """A command typed in the distro itself, as its default user."""
    step = record(title, command, [wsl_exe, '-d', distro, '--cd', '~', '-e', 'sh', '-s'], stdin_text=command + '\n')
    step['shell'] = 'distro'
    return step


yes = 'y'
steps = [
    wslbak(TITLES['init'], 'wslbak init', 'init', '-d', distro, '--dest', dest, answers=(yes, 'n')),
    wslbak(TITLES['run'], 'wslbak run', 'run'),
    wslbak(TITLES['list'], 'wslbak list', 'list'),
    wslbak(TITLES['files'], 'wslbak files /home/me/project', 'files', '/home/me/project'),
    inside(TITLES['oops'], 'rm project/notes.md && ls project'),
    wslbak(TITLES['path'], 'wslbak restore --path /home/me/project/notes.md --into /home/me/recovered',
           'restore', '--path', '/home/me/project/notes.md', '--into', '/home/me/recovered', answers=(yes,)),
    inside(TITLES['cat'], 'cat recovered/home/me/project/notes.md'),
    wslbak(TITLES['restore'], 'wslbak restore', 'restore', '--to', restored, answers=(yes,)),
    wslbak(TITLES['status'], 'wslbak status', 'status'),
]

json.dump({'lang': lang, 'steps': steps}, open(out_path, 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
failed = [s['command'] for s in steps if s['rc'] != 0]
if failed:
    print('these steps did not exit with 0: ' + ', '.join(failed), file=sys.stderr)
    sys.exit(1)
