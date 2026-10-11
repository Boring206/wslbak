#!/usr/bin/env python3
"""Reads what scripts/e2e-tarfacts-in.sh left behind and says whether GNU tar behaved the
way differential backups need it to. Started by scripts/e2e-tarfacts.sh.

  e2e-tarfacts.py analyze <folder>            compare the listings, dumps and snapshots
  e2e-tarfacts.py import-cases <rootfs> <folder>
                                              write the archives for the import checks
"""
import io
import os
import sys
import tarfile

passed = failed = 0


def ok(label, detail=""):
    global passed
    passed += 1
    print(f"  \033[32mpass\033[0m {label}" + (f"  ({detail})" if detail else ""))


def bad(label, detail=""):
    global failed
    failed += 1
    print(f"  \033[31mFAIL\033[0m {label}" + (f"\n        | {detail}" if detail else ""))


def note(text):
    print(f"  \033[36mnote\033[0m {text}")


def check(condition, label, detail=""):
    """detail says what was found instead; it is only shown when the check fails."""
    if condition:
        ok(label)
    else:
        bad(label, detail)


def show(names, limit=8):
    names = sorted(names)
    text = ", ".join(repr(n.decode("utf-8", "backslashreplace")) for n in names[:limit])
    return text + (f" … and {len(names) - limit} more" if len(names) > limit else "")


# ---- GNU tar's snapshot file, format 2 --------------------------------------------------

class Snapshot:
    def __init__(self, data):
        self.problems = []
        self.dirs = {}  # name -> (dev, ino, [(mark, child name)])
        self.order = []
        newline = data.find(b"\n")
        self.header = data[:newline] if newline >= 0 else data[:40]
        if newline < 0 or not self.header.startswith(b"GNU tar-") or not self.header.endswith(b"-2"):
            self.problems.append(f"unexpected first line {self.header!r}")
            return
        fields = data[newline + 1:].split(b"\0")
        # The time of the run: seconds, nanoseconds.
        at = 2
        while at + 6 <= len(fields):
            _nfs, _sec, _nsec, dev, ino, name = fields[at:at + 6]
            at += 6
            children = []
            while at < len(fields) and fields[at] != b"":
                children.append((chr(fields[at][0]), fields[at][1:]))
                at += 1
            # Two NULs close the list: the empty string just met, and one more.
            at += 2
            if name in self.dirs:
                self.problems.append(f"directory listed twice: {name!r}")
            self.dirs[name] = (int(dev), int(ino), children)
            self.order.append(name)
        if at < len(fields) - 1:
            self.problems.append(f"{len(fields) - at} fields left over at the end")

    def validate_tree(self):
        """The checks wslbak will make before it trusts a listing."""
        problems = list(self.problems)
        if b"." not in self.dirs:
            problems.append("no record for the top directory")
        seen = {}
        for name, (dev, ino, _children) in self.dirs.items():
            if (dev, ino) in seen:
                problems.append(f"{name!r} and {seen[(dev, ino)]!r} are the same directory (inode {ino})")
            seen[(dev, ino)] = name
        expected = {b"."}
        for name, (_dev, _ino, children) in self.dirs.items():
            names = [c for _m, c in children]
            if len(names) != len(set(names)):
                problems.append(f"a name appears twice in {name!r}")
            for mark, child in children:
                if mark == "D":
                    path = name + b"/" + child
                    expected.add(path)
                    if path not in self.dirs:
                        problems.append(f"{path!r} is marked as a directory but has no record")
                elif mark not in "YN":
                    problems.append(f"unknown mark {mark!r} in {name!r}")
        for name in self.dirs:
            if name not in expected:
                problems.append(f"{name!r} has a record but no parent lists it as a directory")
        return problems

    def paths(self):
        """Returns (directories, entries marked Y, entries marked N)."""
        dirs, yes, no = set(self.dirs), set(), set()
        for name, (_dev, _ino, children) in self.dirs.items():
            for mark, child in children:
                path = name + b"/" + child
                if mark == "Y":
                    yes.add(path)
                elif mark == "N":
                    no.add(path)
                else:
                    dirs.add(path)
        return dirs, yes, no


def members(path):
    """Every member of a tar file: name (bytes, no trailing slash) -> TarInfo."""
    out = {}
    with tarfile.open(path, "r:", encoding="utf-8", errors="surrogateescape") as tar:
        for m in tar:
            name = os.fsencode(m.name).rstrip(b"/") or b"."
            if name in out:
                out[name + b"\0second"] = m
            out[name] = m
    return out


def lines(path):
    with open(path, "rb") as f:
        return [line for line in f.read().decode("utf-8", "backslashreplace").splitlines() if line]


def analyze(folder):
    p = lambda name: os.path.join(folder, name)

    print("\n\033[1m1. The listing pass against a full dump\033[0m")
    snap = Snapshot(open(p("list.snar"), "rb").read())
    note(f"snapshot header {snap.header.decode()!r}, {len(snap.dirs)} directories")
    problems = snap.validate_tree()
    check(not problems, "the snapshot is one tree: unique names and inodes, every directory has its record", "; ".join(problems[:5]))
    dirs, yes, no = snap.paths()
    dump = members(p("dump.tar"))
    dumped_dirs = {n for n, m in dump.items() if m.isdir()}
    dumped_other = {n for n, m in dump.items() if not m.isdir()}
    check(dirs == dumped_dirs, "the listing and the dump hold the same directories",
          f"only in the listing: {show(dirs - dumped_dirs)}; only in the dump: {show(dumped_dirs - dirs)}")
    only_listing = yes - dumped_other
    only_dump = dumped_other - yes
    sockets = {n for n in only_listing if n.endswith(b"/sock")}
    check(only_listing == sockets, "entries the listing marks Y that the dump lacks are sockets, and nothing else",
          show(only_listing - sockets))
    note(f"marked Y but not dumped: {show(only_listing) or 'none'}")
    mounts = {n for n in only_dump if n.endswith(b"/bindfile")}
    check(only_dump == mounts, "entries the dump has that the listing does not mark Y are files mounted from another device, and nothing else",
          show(only_dump - mounts))
    note(f"dumped but not marked Y: {show(only_dump) or 'none'}")
    check(only_dump <= no, "those are marked N in the listing, so the listing still knows the name exists", show(only_dump - no))
    excluded = {b"./excl/drop", b"./a/sub/f2"}
    check(excluded <= no and not (excluded & set(dump)), "excluded names are marked N in the listing and are absent from the dump",
          f"marked N: {show(no)}")
    odd = [n for n in dumped_other if b"\n" in n or b"\\" in n or b"\xff" in n or n.endswith(b" ")]
    check(len(odd) >= 4 and set(odd) <= yes, "names with a newline, a backslash, bytes that are not UTF-8 and a trailing space come through the listing unchanged", show(odd))
    for mount in (b"./tmpfsmnt", b"./vol2"):
        if mount in snap.dirs:
            check(snap.dirs[mount][2] == [] and mount in dumped_dirs and not any(n.startswith(mount + b"/") for n in dump),
                  f"{mount.decode()} (another file system) is listed and dumped as a directory, without its contents")

    print("\n\033[1m4. What the listing pass writes to standard error\033[0m")
    err = lines(p("list.err"))
    unexpected = [line for line in err if not line.endswith(": socket ignored")]
    check(not unexpected, "with --warning=no-xdev: nothing but one line per socket", " | ".join(unexpected[:5]))
    note(f"listing stderr: {err or 'empty'}")
    note(f"without --warning=no-xdev: {lines(p('list-xdev.err')) or 'empty'}")
    note(f"full dump stderr: {lines(p('dump.err')) or 'empty'}")

    print("\n\033[1m2. One directory visible at two paths\033[0m")
    if os.path.exists(p("alias.snar")):
        alias = Snapshot(open(p("alias.snar"), "rb").read())
        problems = alias.validate_tree()
        check(bool(problems), "the tree check rejects the snapshot", "the snapshot passed the check")
        for problem in problems[:6]:
            note(problem)
        adirs, ayes, _ano = alias.paths()
        adump = members(p("alias-dump.tar"))
        twice = {n for n in adump if n.startswith(b"./alias/")}
        note(f"a full dump stores the tree twice: {len(twice)} entries under ./alias")
        missing = {n for n, m in adump.items() if not m.isdir() and n not in ayes and not n.endswith(b"/bindfile")}
        note(f"entries of the dump that the listing does not mark Y: {show(missing) or 'none'}")
        note(f"listing stderr: {lines(p('alias.err')) or 'empty'}")
        if os.path.exists(p("alias-warn.err")):
            warned = lines(p("alias-warn.err"))
            check(any("renamed" in line for line in warned), "with --warning=rename-directory tar says so on standard error", " | ".join(warned[:5]))
            note(f"with --warning=rename-directory: {warned or 'empty'}")
        root_dev, hits = None, []
        rows = [line.split() for line in lines(p("alias.mountinfo"))]
        for row in rows:
            if row[4] == "/":
                root_dev = row[2]
        for row in rows:
            if row[2] == root_dev and row[4].endswith("/tree/alias"):
                hits.append(row)
        check(len(hits) == 1 and hits[0][3] != hits[0][4], "mountinfo shows it: same device as /, and the mounted folder is not the mount point",
              str(hits))
    else:
        note("not run")

    print("\n\033[1m6. A dump of what changed since a time\033[0m")
    since = members(p("since.tar"))
    T = int(open(p("since.T")).read())
    want_dirs = {line.encode() for line in lines(p("since.dirs"))}
    got_dirs = {n for n, m in since.items() if m.isdir()}
    check(got_dirs == want_dirs, "every directory is in it, changed or not",
          f"missing: {show(want_dirs - got_dirs)}; extra: {show(got_dirs - want_dirs)}")
    work = {n[len(b"./since/work/"):]: m for n, m in since.items() if n.startswith(b"./since/work/")}
    expected = {b"edited", b"chmodded", b"backdated", b"renamed2", b"linked", b"linked2", b"created"}
    check(set(work) == expected, "exactly the changed files are in it: edited, mode changed, time set back, renamed, newly linked, created",
          f"got {show(set(work))}")
    others = {n for n, m in since.items() if not m.isdir() and not n.startswith(b"./since/work/")}
    check(not others, "nothing older than the time is in it", show(others))
    if b"backdated" in work:
        check(work[b"backdated"].mtime < T, "the file whose time was set back was chosen by its change time, not its modification time")
    kinds = sorted(m.type for m in (work.get(b"linked"), work.get(b"linked2")) if m)
    check(kinds == [tarfile.REGTYPE, tarfile.LNKTYPE], "a hard-linked pair comes out as one file and one link", str(kinds))
    without = [n for n, m in since.items() if "ctime" not in m.pax_headers]
    check(not without, "every entry carries its change time", show(without))
    full_without = [n for n, m in dump.items() if "ctime" not in m.pax_headers]
    check(not full_without, "so does every entry of a full dump (files, directories, links, FIFOs, devices)", show(full_without))
    note(f"standard error: {lines(p('since.err')) or 'empty'}")

    print("\n\033[1m8. A change stamped earlier than the time asked about\033[0m")
    clock = members(p("clock.tar"))
    ahead = int(open(p("clock.T")).read())
    check(b"./clock/file" not in clock, "the rewritten file is missed, as expected: its times are earlier than the time asked about")
    before = lines(p("clock.before"))[0].split("|")[1]
    after_text, after_seconds = lines(p("clock.after"))[0].split("|")[1:3]
    folder_entry = clock.get(b"./clock")
    check(folder_entry is not None and before != after_text and int(after_seconds) < ahead,
          "and it can be noticed: the folder's change time differs from what was recorded, yet is earlier than that time",
          f"before {before}, after {after_text}")
    if folder_entry is not None:
        check(abs(float(folder_entry.pax_headers.get("ctime", 0)) - int(after_seconds)) < 2, "the folder's change time is in the archive header to compare with")


# ---- Archives for the import checks ----------------------------------------------------

def import_cases(rootfs, folder):
    base = tarfile.open(rootfs, "r:*")
    base_members = [(m, base.extractfile(m).read() if m.isreg() else None) for m in base]

    def entry(name, kind=tarfile.REGTYPE, data=b"", mode=0o644, uid=0, gid=0, mtime=1_000_000_000, link=""):
        info = tarfile.TarInfo(name)
        info.type, info.mode, info.uid, info.gid, info.mtime, info.linkname = kind, mode, uid, gid, mtime, link
        info.size = len(data) if kind == tarfile.REGTYPE else 0
        return info, data

    d = lambda name, **kw: entry(name, kind=tarfile.DIRTYPE, **kw)
    cases = {
        # A directory whose entry comes after the files in it.
        "dir-after-files": [entry("./case/late/file", data=b"inside\n"), d("./case/late", mode=0o750, uid=1234, gid=5678, mtime=978307200)],
        # A directory entry for a directory that an earlier entry already made.
        "dir-twice": [d("./case/twice", mode=0o755), entry("./case/twice/file", data=b"inside\n"),
                      d("./case/twice", mode=0o700, uid=1234, gid=5678, mtime=978307200)],
        "file-twice": [d("./case"), entry("./case/f", data=b"first\n"), entry("./case/f", data=b"second\n")],
        "link-to-nothing": [d("./case"), entry("./case/link", kind=tarfile.LNKTYPE, link="./case/absent")],
        "file-over-full-dir": [d("./case"), d("./case/x"), entry("./case/x/inner", data=b"inner\n"), entry("./case/x", data=b"file now\n")],
    }
    os.makedirs(folder, exist_ok=True)
    for name, extra in cases.items():
        with tarfile.open(os.path.join(folder, name + ".tar"), "w", format=tarfile.PAX_FORMAT) as out:
            for m, data in base_members:
                out.addfile(m, io.BytesIO(data) if data is not None else None)
            for info, data in extra:
                out.addfile(info, io.BytesIO(data) if info.isreg() else None)
        print(name)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "analyze":
        analyze(sys.argv[2])
        print(f"\n{passed} passed, {failed} failed")
        sys.exit(1 if failed else 0)
    if len(sys.argv) == 4 and sys.argv[1] == "import-cases":
        import_cases(sys.argv[2], sys.argv[3])
        sys.exit(0)
    print(__doc__)
    sys.exit(2)
