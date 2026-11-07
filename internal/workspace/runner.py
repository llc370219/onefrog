"""Fixed operations sent to the selected checkout over SSH; input is JSON on stdin."""
import errno
import fcntl
import hashlib
import json
import os
import secrets
import selectors
import signal
import stat
import subprocess
import sys
import time

MAX_FILE = 256 * 1024
OUTPUT = 96 * 1024
DIR = os.O_DIRECTORY | os.O_NOFOLLOW | os.O_RDONLY


class BadRequest(Exception):
    pass


def allowed(part):
    p = part.lower()
    return not (
        p in {".git", ".ssh", ".gnupg", ".npmrc", ".netrc", "credentials.json", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"}
        or (p.startswith(".env") and p not in {".env.example", ".env.sample", ".env.template"})
        or p.endswith((".pem", ".key", ".p12", ".pfx", ".enc"))
        or any(ord(ch) < 32 or ord(ch) == 127 for ch in p)
    )


def parts(p):
    out = p.split("/")
    if any(x in {"", ".", ".."} or not allowed(x) for x in out):
        raise BadRequest("路径超出仓库或属于受保护文件")
    return out


def read_regular(fd, name):
    file = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    with os.fdopen(file, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise BadRequest("只能打开仓库内的普通文件")
        data = source.read(MAX_FILE + 1)
    if len(data) > MAX_FILE or b"\0" in data:
        raise BadRequest("编辑器仅支持 256 KiB 以内的 UTF-8 文本")
    return data, data.decode("utf-8"), info


def file_operation(rootfd, req):
    names = parts(req["path"])
    action = req["action"]
    parent = os.dup(rootfd)
    tmp = None
    try:
        for name in names[:-1]:
            child = os.open(name, DIR, dir_fd=parent)
            os.close(parent)
            parent = child
        name = names[-1]
        if action != "read":
            # Serialize writes from this workbench; outside editors are checked by revision.
            fcntl.flock(rootfd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            data, text, info = read_regular(parent, name)
            rev = hashlib.sha256(data).hexdigest()
        except FileNotFoundError:
            if action != "write":
                raise
            text, info, rev = "", None, ""
        if action == "read":
            count = req.get("lines", 0)
            first = max(1, req.get("from_line", 1))
            truncated = False
            if count:
                lines = text.splitlines(keepends=True)
                text = "".join(lines[first - 1:first + count - 1])
                truncated = first + count - 1 < len(lines) or len(text) > 12000
                text = text[:12000]
            return {"text": text, "revision": rev, "truncated": truncated}
        if req.get("revision", "") != rev:
            raise BadRequest("文件已变化，请重新读取后再保存")
        if action == "edit":
            old = req["old"]
            if not old or text.count(old) != 1:
                raise BadRequest("要替换的原文必须恰好出现一次")
            text = text.replace(old, req.get("new", ""), 1)
        else:
            text = req["content"]
        data = text.encode("utf-8")
        if len(data) > MAX_FILE or b"\0" in data:
            raise BadRequest("编辑结果必须是 256 KiB 以内的文本")
        staged = ".onefrog-write-" + secrets.token_hex(12)
        file = os.open(staged, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600, dir_fd=parent)
        tmp = staged
        with os.fdopen(file, "wb") as dest:
            dest.write(data)
            dest.flush()
            if info:
                os.fchmod(dest.fileno(), stat.S_IMODE(info.st_mode) & 0o777)
            os.fsync(dest.fileno())
        if info:
            os.replace(tmp, name, src_dir_fd=parent, dst_dir_fd=parent)
            tmp = None
        else:
            # link fails instead of overwriting a concurrently created file.
            os.link(tmp, name, src_dir_fd=parent, dst_dir_fd=parent, follow_symlinks=False)
        return {"revision": hashlib.sha256(data).hexdigest()}
    finally:
        if tmp:
            os.unlink(tmp, dir_fd=parent)
        os.close(parent)


def execute(argv, cwd, env, timeout=20, limit=OUTPUT):
    with subprocess.Popen(argv, cwd=cwd, env=env, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                          start_new_session=True) as proc:
        data = bytearray()
        deadline = time.monotonic() + timeout
        try:
            with selectors.DefaultSelector() as sel:
                sel.register(proc.stdout, selectors.EVENT_READ)
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0 or not sel.select(remaining):
                        raise subprocess.TimeoutExpired(argv, timeout)
                    block = os.read(proc.stdout.fileno(), 65536)
                    if not block:
                        break
                    data.extend(block[:max(0, limit + 1 - len(data))])
            code = proc.wait(timeout=max(0, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            proc.wait()
            return bytes(data[:limit]), 124, len(data) > limit
    return bytes(data[:limit]), code, len(data) > limit


def main(req):
    if os.geteuid() == 0:
        raise BadRequest("代码工作区需要非 root 的 SSH 开发账号")
    root = req["root"]
    # Open the root through directory descriptors so a swapped symlink cannot redirect it.
    fd = os.open("/", DIR)
    try:
        for name in root.removeprefix("/").split("/"):
            child = os.open(name, DIR, dir_fd=fd)
            os.close(fd)
            fd = child
        os.fchdir(fd)
        if os.getcwd() != root:
            raise BadRequest("仓库目录已变化，请重新连接")
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        git_env = dict(env, GIT_OPTIONAL_LOCKS="0", GIT_TERMINAL_PROMPT="0")
        git = ["git", "--no-pager", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "color.ui=false", "-c", "color.status=false"]
        output, code, _ = execute(git + ["rev-parse", "--show-toplevel"], None, git_env)
        if code or output.decode("utf-8", errors="replace").rstrip("\n") != root:
            raise BadRequest("该路径不是 Git 工作树的根目录，或 SSH 账号没有访问权限")
        action = req["action"]
        if action in {"read", "write", "edit"}:
            return file_operation(fd, req)
        if action == "run":
            data, code, truncated = execute(["/bin/sh", "-c", req["command"]], None, env, timeout=145)
            return {"text": data.decode("utf-8", errors="replace"), "exit_code": code, "truncated": truncated}
        # Git inspection must not execute a repo-configured clean/process filter.
        keys, code, too_many = execute(git + ["config", "--name-only", "--get-regexp", "^filter\\."], None, git_env, limit=8192)
        if too_many or code not in {0, 1}:
            raise BadRequest("无法安全读取仓库的 Git 过滤器配置")
        for key in set(keys.decode("utf-8", errors="replace").splitlines()):
            name, _, setting = key.rpartition(".")
            if setting in {"clean", "process", "required"}:
                git.extend(["-c", name + "." + setting + ("=false" if setting == "required" else "=")])
        p = req.get("path")
        if p:
            parts(p)
        paths = [":(literal)" + p] if p else ["."]
        for pattern in (".env*", ".ssh/**", ".gnupg/**", ".npmrc", ".netrc", "credentials.json",
                        "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "*.pem", "*.key", "*.p12", "*.pfx", "*.enc"):
            paths.append(":(exclude,glob,icase)**/" + pattern)
        if action == "list":
            data, code, truncated = execute(git + ["ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"] + paths, None, git_env)
            if code:
                raise BadRequest("无法列出仓库文件")
            files = {name for raw in data.split(b"\0")[:-1] if (name := raw.decode("utf-8", errors="replace"))
                     and all(allowed(part) for part in name.split("/"))}
            return {"files": sorted(files)[:500], "truncated": truncated or len(files) > 500}
        if action == "status":
            args = ["status", "--short", "--branch", "--untracked-files=normal", "--ignore-submodules=all"]
        else:
            args = ["diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--no-color"]
            _, head_code, _ = execute(git + ["rev-parse", "--verify", "HEAD"], None, git_env)
            args += ["HEAD"] if head_code == 0 else ["--cached"]
        data, code, truncated = execute(git + args + ["--"] + paths, None, git_env)
        if code:
            raise BadRequest("无法读取 Git 状态或差异")
        return {"text": data.decode("utf-8", errors="replace"), "truncated": truncated}
    finally:
        os.close(fd)


try:
    result = main(json.load(sys.stdin))
except BadRequest as exc:
    result = {"error": str(exc)}
except UnicodeError:
    result = {"error": "文件或路径不是有效的 UTF-8 文本"}
except OSError as exc:
    messages = {errno.ENOENT: "目录或文件不存在，请确认路径（父目录需已存在）",
                errno.EACCES: "SSH 账号没有操作此目录或文件的权限",
                errno.EPERM: "SSH 账号没有操作此目录或文件的权限",
                errno.ENOTDIR: "目录不存在或包含符号链接",
                errno.ELOOP: "工作区不能跟随符号链接",
                errno.EAGAIN: "文件正被修改，请稍后重试",
                errno.EEXIST: "文件已存在，请重新读取后再保存"}
    result = {"error": messages.get(exc.errno, "宿主机工作区操作失败")}
print(json.dumps(result, ensure_ascii=False))
