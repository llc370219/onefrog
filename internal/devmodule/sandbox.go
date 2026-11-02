package devmodule

import "strings"

// SandboxCSP forces every rendered custom module frame into an isolated opaque
// null origin with zero network access, zero form submission, and zero access
// to cookies or same-origin storage.
const SandboxCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; " +
	"img-src data: blob:; font-src data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

const bootstrapHead = `<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{color-scheme:dark;--bg:#141517;--s1:#18191c;--s2:#202126;--fg:#eeeef0;--fg2:#b4b4bc;--fg3:#92929c;--fg4:#73737e;--line:rgba(255,255,255,.08);--line2:rgba(255,255,255,.15);--ok:#9cceac;--warn:#dbb77a;--danger:#e69b9b}
*{box-sizing:border-box}
html,body{margin:0;padding:0;background:transparent;color:var(--fg);font-family:Inter,"PingFang SC","Microsoft YaHei",system-ui,sans-serif;font-size:13px;line-height:1.55;-webkit-font-smoothing:antialiased}
h1,h2,h3,h4,p{margin:0}
button,input,select,textarea{font:inherit;color:inherit}
input,select,textarea{background:#111214;border:1px solid var(--line2);border-radius:9px;padding:8px 11px;color:var(--fg);outline:none;width:100%;min-width:0}
input:focus,select:focus,textarea:focus{border-color:rgba(255,255,255,.38)}
button,.of-btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;height:34px;padding:0 14px;border-radius:999px;border:1px solid var(--line2);background:var(--s2);color:var(--fg);font-size:12px;font-weight:500;cursor:pointer;white-space:nowrap;transition:background .15s,border-color .15s}
button:hover:not(:disabled),.of-btn:hover:not(:disabled){background:#2b2c32;border-color:rgba(255,255,255,.26)}
button:disabled,.of-btn:disabled{opacity:.48;cursor:default}
button.primary,.of-btn.primary{background:#efeff1;color:#141416;border-color:#efeff1}
button.primary:hover:not(:disabled),.of-btn.primary:hover:not(:disabled){background:#d7d7de}
button.danger,.of-btn.danger{color:var(--danger);border-color:rgba(230,155,155,.28)}
table{width:100%;border-collapse:collapse;font-size:12px}
th,td{padding:9px 10px;border-bottom:1px solid var(--line);text-align:left}
th{color:var(--fg3);font-weight:400;font-size:11px}
.of-root{padding:14px 18px 16px;display:flex;flex-direction:column;gap:12px}
.of-card{border:1px solid var(--line);border-radius:12px;background:var(--s1);padding:14px 16px}
.of-row{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.of-between{display:flex;align-items:center;justify-content:space-between;gap:10px}
.of-col{display:flex;flex-direction:column;gap:8px}
.of-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px}
.of-stat{border:1px solid var(--line);border-radius:10px;padding:11px 13px;background:rgba(255,255,255,.02)}
.of-stat span{display:block;font-size:11px;color:var(--fg3)}
.of-stat strong{display:block;margin-top:4px;font-size:20px;font-weight:500;font-variant-numeric:tabular-nums}
.of-badge{display:inline-flex;align-items:center;padding:2px 8px;border-radius:6px;border:1px solid var(--line2);font-size:11px;color:var(--fg2)}
.of-list{list-style:none;margin:0;padding:0;display:flex;flex-direction:column}
.of-item{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:9px 0;border-bottom:1px solid var(--line);font-size:12px}
.of-item:last-child{border-bottom:0}
.of-muted{color:var(--fg3);font-size:12px}
.of-empty{padding:18px 12px;text-align:center;color:var(--fg4);font-size:12px;border:1px dashed var(--line2);border-radius:10px}
#__of_err{display:none;margin:10px 18px;padding:9px 12px;border-radius:9px;border:1px solid rgba(230,155,155,.32);background:rgba(230,155,155,.08);color:var(--danger);font-size:12px;white-space:pre-wrap}
</style>
<script>
(function(){
  var token = "";
  var seq = 0;
  var initDone = false;
  var cache = { state: {}, records: [], compact: false };
  var readyResolve;
  var readyPromise = new Promise(function(r){ readyResolve = r; });
  var pending = new Map();
  var listeners = new Set();

  function normalizeRecord(r){
    if (!r || typeof r !== "object") return { id: "", created_at: "", updated_at: "", data: {} };
    var d = (r.data && typeof r.data === "object") ? r.data : {};
    var out = {};
    var keys = Object.keys(d);
    for (var i = 0; i < keys.length; i++) out[keys[i]] = d[keys[i]];
    out.id = r.id || "";
    out.created_at = r.created_at || "";
    out.updated_at = r.updated_at || r.created_at || "";
    out.data = d;
    return out;
  }

  function normalizePayload(raw){
    var src = raw || {};
    var recs = Array.isArray(src.records) ? src.records.map(normalizeRecord) : [];
    var res = {
      state: (src.state && typeof src.state === "object") ? src.state : {},
      records: recs,
      compact: Boolean(cache.compact)
    };
    if (src.record) res.record = normalizeRecord(src.record);
    return res;
  }

  function cloneCache(){
    return JSON.parse(JSON.stringify(cache));
  }

  function post(msg){
    if (window.parent && window.parent !== window) {
      window.parent.postMessage(msg, "*");
    }
  }

  var lastHeight = 0;
  function reportHeight(){
    if (!token) return;
    var root = document.documentElement;
    var body = document.body;
    var h = Math.max(body ? body.scrollHeight : 0, root ? root.offsetHeight : 0, 72);
    if (Math.abs(h - lastHeight) > 2) {
      lastHeight = h;
      post({ __onefrog_resize: true, token: token, height: h });
    }
  }

  function showErr(msg){
    var text = String(msg || "脚本运行出错");
    var el = document.getElementById("__of_err");
    if (el) { el.style.display = "block"; el.textContent = "模块运行出错：" + text; }
    if (token) post({ __onefrog_error: true, token: token, message: text });
    reportHeight();
  }

  window.addEventListener("error", function(ev){
    showErr(ev && (ev.message || (ev.error && ev.error.message)) || "未知错误");
  });
  window.addEventListener("unhandledrejection", function(ev){
    var r = ev && ev.reason;
    showErr((r && r.message) ? r.message : String(r || "Promise 异常"));
  });

  window.addEventListener("message", function(ev){
    var d = ev && ev.data;
    if (!d || typeof d !== "object") return;
    if (d.__onefrog_init) {
      token = String(d.token || "");
      cache.compact = Boolean(d.compact);
      var norm = normalizePayload(d.data);
      cache = { state: norm.state, records: norm.records, compact: Boolean(d.compact) };
      if (!initDone) { initDone = true; readyResolve(cloneCache()); }
      listeners.forEach(function(fn){ try { fn(cloneCache()); } catch (_){} });
      setTimeout(reportHeight, 10);
      return;
    }
    if (d.__onefrog_reply && token && d.token === token) {
      var item = pending.get(d.reqId);
      if (!item) return;
      pending.delete(d.reqId);
      if (d.error) {
        item.reject(new Error(String(d.error)));
      } else {
        var next = normalizePayload(d.data);
        cache.state = next.state;
        cache.records = next.records;
        listeners.forEach(function(fn){ try { fn(cloneCache()); } catch (_){} });
        item.resolve(next);
      }
      setTimeout(reportHeight, 10);
    }
  });

  function call(action, payload){
    return readyPromise.then(function(){
      return new Promise(function(resolve, reject){
        var reqId = "r" + (++seq);
        pending.set(reqId, { resolve: resolve, reject: reject });
        post({ __onefrog_req: true, token: token, reqId: reqId, action: action, payload: payload || {} });
      });
    });
  }

  function pad(n){ return n < 10 ? "0" + n : String(n); }
  function formatTime(iso){
    var dt = iso ? new Date(iso) : new Date();
    if (isNaN(dt.getTime())) return String(iso || "");
    return dt.getFullYear() + "-" + pad(dt.getMonth() + 1) + "-" + pad(dt.getDate()) + " " +
      pad(dt.getHours()) + ":" + pad(dt.getMinutes()) + ":" + pad(dt.getSeconds());
  }

  window.onefrog = {
    load: function(){ return readyPromise.then(cloneCache); },
    refresh: function(){ return call("get", {}); },
    saveState: function(patch){ return call("save_state", { state: patch || {} }); },
    setState: function(state){ return call("set_state", { state: state || {} }); },
    addRecord: function(data){ return call("add_record", { data: data || {} }); },
    updateRecord: function(id, data){ return call("update_record", { id: String(id || ""), data: data || {} }); },
    deleteRecord: function(id){ return call("delete_record", { id: String(id || "") }); },
    clearRecords: function(){ return call("clear_records", {}); },
    systemOverview: function(){
      return readyPromise.then(function(){
        return new Promise(function(resolve, reject){
          var reqId = "r" + (++seq);
          pending.set(reqId, { resolve: resolve, reject: reject });
          post({ __onefrog_req: true, token: token, reqId: reqId, action: "system_overview", payload: {} });
        });
      });
    },
    onChange: function(fn){ if (typeof fn === "function") listeners.add(fn); return function(){ listeners.delete(fn); }; },
    formatTime: formatTime
  };

  post({ __onefrog_hello: true });
  document.addEventListener("DOMContentLoaded", function(){
    if (!token) post({ __onefrog_hello: true });
    if (typeof ResizeObserver !== "undefined" && document.body) {
      new ResizeObserver(reportHeight).observe(document.body);
    }
    if (typeof MutationObserver !== "undefined" && document.body) {
      new MutationObserver(reportHeight).observe(document.body, { childList: true, subtree: true, characterData: true });
    }
    setTimeout(reportHeight, 30);
  });
  window.addEventListener("load", function(){
    if (!token) post({ __onefrog_hello: true });
    reportHeight();
  });
})();
</script>`

// RenderFrameHTML wraps a custom module's HTML snippet or document with the
// OneFrog dark theme stylesheet and window.onefrog persistence bridge.
func RenderFrameHTML(item CustomModule) string {
	raw := strings.TrimSpace(item.HTML)
	lower := strings.ToLower(raw)
	errBox := `<div id="__of_err"></div>`
	if idx := strings.Index(lower, "<head>"); idx >= 0 {
		pos := idx + len("<head>")
		return insertErrBox(raw[:pos] + bootstrapHead + raw[pos:])
	}
	if idx := strings.Index(lower, "<body"); idx >= 0 {
		return insertErrBox("<head>" + bootstrapHead + "</head>" + raw[idx:])
	}
	return "<!doctype html><html lang=\"zh-CN\"><head>" + bootstrapHead +
		"</head><body>" + errBox + "<div class=\"of-root\">" + raw + "</div></body></html>"
}

func insertErrBox(doc string) string {
	lower := strings.ToLower(doc)
	idx := strings.Index(lower, "<body")
	if idx < 0 {
		return doc
	}
	end := strings.Index(doc[idx:], ">")
	if end < 0 {
		return doc
	}
	pos := idx + end + 1
	return doc[:pos] + `<div id="__of_err"></div>` + doc[pos:]
}

// StarterHTML returns a clean default click-to-record card template.
func StarterHTML(title string) string {
	if strings.TrimSpace(title) == "" {
		title = "打卡记录"
	}
	return `<div class="of-card of-col">
  <div class="of-between">
    <div>
      <strong id="title">` + escapeHTML(title) + `</strong>
      <p class="of-muted" id="summary">正在读取记录…</p>
    </div>
    <button class="primary" id="tapBtn" type="button">点击打卡</button>
  </div>
  <div class="of-row">
    <input id="noteInput" placeholder="输入简短记述或备注（可选，回车直接记录）" maxlength="160" />
  </div>
  <ul class="of-list" id="list"></ul>
</div>
<script>
(async function () {
  const summary = document.getElementById("summary");
  const list = document.getElementById("list");
  const noteInput = document.getElementById("noteInput");
  const tapBtn = document.getElementById("tapBtn");

  function render(data) {
    const rows = data.records || [];
    summary.textContent = rows.length
      ? "累计 " + rows.length + " 次 · 最近 " + onefrog.formatTime(rows[0].created_at)
      : "还没有记录，点右侧按钮记下第一条";
    list.innerHTML = "";
    rows.slice(0, data.compact ? 4 : 20).forEach(function (r) {
      const li = document.createElement("li");
      li.className = "of-item";
      const left = document.createElement("div");
      const time = document.createElement("strong");
      time.textContent = onefrog.formatTime(r.created_at);
      const note = document.createElement("div");
      note.className = "of-muted";
      note.textContent = r.note || "已打卡";
      left.appendChild(time);
      left.appendChild(note);
      const del = document.createElement("button");
      del.textContent = "删除";
      del.onclick = async function () {
        render(await onefrog.deleteRecord(r.id));
      };
      li.appendChild(left);
      if (!data.compact) li.appendChild(del);
      list.appendChild(li);
    });
  }

  async function punch() {
    tapBtn.disabled = true;
    try {
      const note = noteInput.value.trim();
      const next = await onefrog.addRecord({ note: note || "打卡" });
      noteInput.value = "";
      render(next);
    } finally {
      tapBtn.disabled = false;
    }
  }

  tapBtn.onclick = punch;
  noteInput.onkeydown = function (e) {
    if (e.key === "Enter") { e.preventDefault(); punch(); }
  };
  render(await onefrog.load());
})();
</script>`
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}
