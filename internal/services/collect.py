import json
import os
import re
import shutil
import subprocess

LIMIT = 500
result = {"items": [], "sources": {}}

def collect(source, command, parse):
    status = {"status": "ok", "truncated": False, "detail": ""}
    result["sources"][source] = status
    if not shutil.which(command[0]):
        status.update(status="unavailable", detail="未安装采集命令")
        return
    try:
        run = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                             text=True, timeout=6, env=dict(os.environ, LC_ALL="C", SYSTEMD_COLORS="0"))
        if run.returncode:
            status.update(status="error", detail="采集失败，请检查服务管理器或当前 SSH 用户权限")
            return
        rows = run.stdout.splitlines()
        status["truncated"] = len(rows) > LIMIT
        for line in rows[:LIMIT]:
            item = parse(line)
            if item:
                item["kind"] = source
                result["items"].append(item)
    except subprocess.TimeoutExpired:
        status.update(status="error", detail="采集命令超时")
    except Exception:
        status.update(status="error", detail="无法解析采集结果")

def systemd(line):
    cols = line.split()
    if len(cols) < 4 or not cols[0].endswith(".service"):
        return None
    return {"name": cols[0][:200], "state": cols[2][:40], "detail": cols[3][:40]}

def docker(line):
    row = json.loads(line)
    return {"name": row["Names"][:200], "state": row["State"][:40],
            "detail": row["Status"][:200], "image": row["Image"][:300],
            "ports": row["Ports"][:1000]}

def listener(line):
    cols = line.split()
    if len(cols) < 6:
        return None
    names = sorted(set(re.findall(r'\("([^"\n]+)"', " ".join(cols[6:]))))
    return {"name": ", ".join(names)[:200] or "进程不可见", "state": "listening",
            "ports": cols[4][:300], "detail": cols[0][:20]}

collect("systemd", ["systemctl", "list-units", "--type=service", "--all", "--no-pager", "--plain", "--no-legend"], systemd)
collect("docker", ["docker", "ps", "--all", "--format", '{"Names":{{json .Names}},"State":{{json .State}},"Status":{{json .Status}},"Image":{{json .Image}},"Ports":{{json .Ports}}}' ], docker)
collect("ports", ["ss", "-H", "-lntup"], listener)
print(json.dumps(result, ensure_ascii=True))
