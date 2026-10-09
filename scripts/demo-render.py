#!/usr/bin/env python3
"""Draws the recorded demo (scripts/demo-record.py) as an animation of a terminal.

  python demo-render.py <recording.json> <output without extension> [--width 1280] [--ffmpeg <ffmpeg> [--mp4-only]]

Writes <output>.gif, and <output>.mp4 as well when ffmpeg is given (only that, with --mp4-only). Needs Pillow and the fonts
that come with Windows (Cascadia Mono, Microsoft JhengHei), so it is run with the Windows
Python; scripts/demo.sh does that.

The commands, what they printed, the answers that were typed and the timing all come from
the recording; none of the text is changed here. This script decides only how it looks: the
speed at which a command is typed, the colours, how long a finished screen stays up, and that
a wait of more than a second and a half is shortened to that.
"""

import json
import os
import subprocess
import sys
import unicodedata

from PIL import Image, ImageDraw, ImageFont

args = sys.argv[1:]
recording_path, out_base = args[0], args[1]
WIDTH = int(args[args.index('--width') + 1]) if '--width' in args else 1280
FFMPEG = args[args.index('--ffmpeg') + 1] if '--ffmpeg' in args else ''
S = WIDTH / 1280
HEIGHT = int(720 * S) // 2 * 2
recording = json.load(open(recording_path, encoding='utf-8'))
LANG = recording['lang']

FONT_DIRS = [r'C:\Windows\Fonts', '/mnt/c/Windows/Fonts']


def font(names, size, index=0):
    for folder in FONT_DIRS:
        for name in names:
            path = os.path.join(folder, name)
            if os.path.exists(path):
                return ImageFont.truetype(path, int(size * S), index=index)
    raise SystemExit('none of these fonts was found: ' + ', '.join(names))


MONO = font(['CascadiaMono.ttf', 'consola.ttf'], 21)
# Chinese characters take two cells; a slightly larger size fills them the way a terminal does.
WIDE = font(['msjh.ttc'], 23.5)
CAPTION = font(['msjhbd.ttc', 'msjh.ttc'], 26)
SMALL = font(['msjh.ttc'], 17)
HUGE = font(['msjhbd.ttc', 'msjh.ttc'], 76)
LARGE = font(['msjh.ttc'], 30)

BG, BAR, TEXT, BRIGHT, DIM = '#0d1117', '#161b22', '#c9d1d9', '#f0f6fc', '#8b949e'
BLUE, GREEN, ACCENT = '#58a6ff', '#3fb950', '#79c0ff'

CELL = MONO.getlength('M')
LINE = int(30 * S)
LEFT, TOP = int(34 * S), int(92 * S)
COLS = int((WIDTH - 2 * LEFT) // CELL)
ROWS = (HEIGHT - TOP - int(20 * S)) // LINE

CARDS = {
    'en': {
        'tagline': 'Backups of a WSL distro that do not stop it',
        'tour': 'a one-minute tour',
        'points': ['every backup is test-restored', 'restore a whole distro, or a single file',
                   'a standard .tar.gz that wsl --import accepts'],
        'small': ['Recorded as it happened, on a PC with wslbak installed from its npm package and a demo distro.',
                  'No text is changed. Typing speed is drawn; waits over a second and a half are shortened.'],
    },
    'zh-TW': {
        'tagline': '不用停機的 WSL 備份',
        'tour': '一分鐘看完怎麼用',
        'points': ['每份備份都試還原過', '可以還原整個 distro，也可以只取回一個檔案', '標準的 .tar.gz，wsl --import 直接能用'],
        'small': ['全程實錄：電腦上真的用 npm 套件裝了 wslbak，並有一個示範用的 distro。',
                  '文字沒有任何修改。打字速度是畫出來的；超過一秒半的等待有縮短。'],
    },
}[LANG]


def wide(ch):
    return unicodedata.east_asian_width(ch) in ('W', 'F')


def width_of(text):
    return sum(2 if wide(c) else 1 for c in text)


def wrap(segments):
    """Splits a line of (text, colour) pieces into rows no wider than the terminal, at a space
    where there is one in the second half of the row."""
    cells = [(ch, colour) for text, colour in segments for ch in text]
    rows = []
    while cells:
        used, cut, space = 0, len(cells), -1
        for i, (ch, _) in enumerate(cells):
            w = 2 if wide(ch) else 1
            if used + w > COLS:
                cut = i
                break
            used += w
            if ch == ' ':
                space = i
        if cut < len(cells) and space >= COLS // 2:
            row, cells = cells[:space], cells[space + 1:]
        else:
            row, cells = cells[:cut], cells[cut:]
        merged = []
        for ch, colour in row:
            if merged and merged[-1][1] == colour:
                merged[-1] = (merged[-1][0] + ch, colour)
            else:
                merged.append((ch, colour))
        rows.append(merged)
    return rows or [[]]


strips = {}


def strip(row):
    """One row of the terminal as a picture; rows repeat from frame to frame, so they are kept."""
    key = tuple(row)
    if key not in strips:
        im = Image.new('RGB', (WIDTH, LINE), BG)
        draw = ImageDraw.Draw(im)
        col = 0
        for text, colour in row:
            for ch in text:
                if ch == '\u2588':
                    draw.rectangle([LEFT + col * CELL, int(4 * S), LEFT + (col + 1) * CELL - 1, LINE - int(4 * S)], fill=colour)
                else:
                    if wide(ch):
                        draw.text((LEFT + col * CELL, int(-1 * S)), ch, font=WIDE, fill=colour)
                    else:
                        draw.text((LEFT + col * CELL, int(3 * S)), ch, font=MONO, fill=colour)
                col += 2 if wide(ch) else 1
        strips[key] = im
    return strips[key]


frames = []  # (picture, milliseconds)


def show(picture, ms):
    if frames and frames[-1][0] is picture:
        frames[-1] = (picture, frames[-1][1] + ms)
    else:
        frames.append((picture, ms))


class Screen:
    def __init__(self, title, number, total):
        self.lines = [[]]  # each line: list of (text, colour)
        self.first = 0  # the first row that is on screen; it moves on a page at a time
        self.header = Image.new('RGB', (WIDTH, TOP - int(12 * S)), BAR)
        draw = ImageDraw.Draw(self.header)
        draw.text((LEFT, int(22 * S)), title, font=CAPTION, fill=BRIGHT)
        counter = '%d / %d' % (number, total)
        draw.text((WIDTH - LEFT - SMALL.getlength(counter), int(30 * S)), counter, font=SMALL, fill=DIM)
        self.cache = {}

    def put(self, text, colour):
        """Appends text to the last line; a newline starts the next one."""
        parts = text.split('\n')
        for i, part in enumerate(parts):
            if i:
                self.lines.append([])
            if part:
                self.lines[-1].append((part, colour))

    def frame(self, ms, cursor=True):
        rows = []
        for line in self.lines:
            rows.extend(wrap(line))
        # More than fits: leave the full page up long enough to read, then go on from its
        # last two rows, the way a pager does. Nothing scrolls past unread.
        while len(rows) - self.first > ROWS:
            show(self.picture(rows[self.first:self.first + ROWS]), 3400)
            self.first += ROWS - 2
        page = rows[self.first:]
        if cursor:
            page[-1] = page[-1] + [('\u2588', DIM)]
        show(self.picture(page), ms)

    def picture(self, rows):
        key = tuple(tuple(r) for r in rows)
        if key not in self.cache:
            im = Image.new('RGB', (WIDTH, HEIGHT), BG)
            im.paste(self.header, (0, 0))
            for i, row in enumerate(rows):
                im.paste(strip(row), (0, TOP + i * LINE))
            self.cache[key] = im
        return self.cache[key]


def card(lines, ms):
    """A full-screen card: a list of (text, font, colour, gap above)."""
    im = Image.new('RGB', (WIDTH, HEIGHT), BG)
    draw = ImageDraw.Draw(im)
    total = sum(gap + f.getbbox('Ag')[3] for _, f, _, gap in lines)
    y = (HEIGHT - total) // 2
    for text, f, colour, gap in lines:
        y += gap
        draw.text(((WIDTH - f.getlength(text)) / 2, y), text, font=f, fill=colour)
        y += f.getbbox('Ag')[3]
    show(im, ms)


g = lambda n: int(n * S)
card([('wslbak', HUGE, BRIGHT, 0), (CARDS['tagline'], LARGE, TEXT, g(26)), (CARDS['tour'], SMALL, DIM, g(30))], 2600)

steps = recording['steps']
for number, step in enumerate(steps, 1):
    screen = Screen(step['title'], number, len(steps))
    # Every command of the tour is typed in a shell inside WSL.
    screen.put('$ ', BLUE)
    screen.frame(700)
    command = step['command']
    for i in range(0, len(command), 2):
        screen.put(command[i:i + 2], BRIGHT)
        screen.frame(70)
    screen.frame(450)
    screen.put('\n', TEXT)

    clock = 0.0
    for at, kind, text in step['events']:
        wait = min(max(at - clock, 0.0), 1.5)
        clock = at
        if wait > 0.04:
            screen.frame(int(wait * 1000))
        if kind == 'in':
            for ch in text:
                screen.put(ch, BRIGHT)
                screen.frame(160)
            screen.frame(300)
            screen.put('\n', TEXT)
        else:
            # A program's last line ends with a newline; do not leave an empty line under it.
            screen.put(text, TEXT)
    if screen.lines[-1] == [] and len(screen.lines) > 1:
        screen.lines.pop()
    rows = sum(len(wrap(line)) for line in screen.lines) - screen.first
    screen.frame(min(1900 + 130 * rows, 5200), cursor=False)

card([('wslbak', HUGE, BRIGHT, 0), (CARDS['tagline'], LARGE, TEXT, g(22))]
     + [('·  ' + p, LARGE, ACCENT, g(18) if i else g(34)) for i, p in enumerate(CARDS['points'])]
     + [('npm install -g wslbak', ImageFont.truetype(MONO.path, g(26)), GREEN, g(40))]
     + [(line, SMALL, DIM, g(34) if i == 0 else g(6)) for i, line in enumerate(CARDS['small'])], 6000)

total_ms = sum(ms for _, ms in frames)
print('%d frames, %.1f s' % (len(frames), total_ms / 1000))

# ---- GIF: one palette for the whole animation, so that only what changes is stored.
if '--mp4-only' in args:
    frames_for_gif = []
else:
    frames_for_gif = frames
sample = Image.new('RGB', (WIDTH, HEIGHT * 3))
for i, index in enumerate((0, len(frames) // 2, len(frames) - 1)):
    sample.paste(frames[index][0], (0, i * HEIGHT))
palette = sample.quantize(colors=64, method=Image.Quantize.MEDIANCUT)
paletted = [im.quantize(palette=palette, dither=Image.Dither.NONE) for im, _ in frames_for_gif]
if paletted:
    paletted[0].save(out_base + '.gif', save_all=True, append_images=paletted[1:],
                     duration=[ms for _, ms in frames_for_gif], loop=0, optimize=False)
    print('%s.gif  %.1f MB' % (out_base, os.path.getsize(out_base + '.gif') / 1e6))

# ---- MP4, when ffmpeg is at hand: every frame is repeated for as long as it is on screen.
if FFMPEG:
    fps = 25
    proc = subprocess.Popen([FFMPEG, '-y', '-loglevel', 'error', '-f', 'rawvideo', '-pix_fmt', 'rgb24',
                             '-s', '%dx%d' % (WIDTH, HEIGHT), '-r', str(fps), '-i', '-',
                             '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-crf', '20', '-movflags', '+faststart',
                             out_base + '.mp4'], stdin=subprocess.PIPE)
    owed = 0.0
    for im, ms in frames:
        owed += ms * fps / 1000
        count = int(owed)
        owed -= count
        data = im.tobytes()
        for _ in range(count):
            proc.stdin.write(data)
    proc.stdin.close()
    if proc.wait() != 0:
        raise SystemExit('ffmpeg failed')
    print('%s.mp4  %.1f MB' % (out_base, os.path.getsize(out_base + '.mp4') / 1e6))
