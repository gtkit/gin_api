package bootstrap

import (
	"net/http"

	"github.com/gtkit/gin-api/api"
)

// registerDevDocs 把 API 文档挂到调试端点。
//
// **挂这里而不是业务路由**，三条理由，每条都是硬的：
//
//  1. 调试端点已有完整的安全边界——默认关闭、只绑回环地址、配置校验拒绝
//     非回环绑定、生命周期受管。文档入口需要的恰好是同一套约束，
//     再造一遍只会造出第二个略有差异的版本。
//  2. 文档页刻意**不进 OpenAPI 契约**：契约描述的是对外 API，一个 dev-only
//     的查看器出现在契约里，会让契约的消费者以为生产环境也有这个端点。
//  3. 业务路由的 body 契约机制假定 JSON 信封，而这里返回的是 HTML 页面——
//     硬塞进去要么给 HTML 编造一个 JSON schema（契约说谎），要么为一个
//     调试页面给契约机制开洞。
//
// 契约 JSON 来自编译期嵌入（见 api 包），与二进制的真实行为同源。
func registerDevDocs(mux *http.ServeMux) {
	mux.HandleFunc("/docs/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(api.Contract)
	})
	mux.HandleFunc("/docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(devDocsPage))
	})
}

// devDocsPage 是自包含的契约查看器。
//
// 不用 Swagger UI：它要从 CDN 拉资源，而调试端点只绑回环、且开发机可能离线——
// 一个"有网才能打开"的本地文档页在最需要它的环境里恰好打不开。
// 这里的原生 JS 只做一件事：把同端点的 openapi.json 渲染成可读列表。
const devDocsPage = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>API Contract</title>
<style>
  body { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; margin: 2rem; color: #1a1a1a; }
  h1 { font-size: 1.2rem; }
  .op { border: 1px solid #ddd; border-radius: 6px; margin: .5rem 0; padding: .6rem .8rem; }
  .m { display: inline-block; min-width: 4.5em; font-weight: 700; }
  .GET { color: #0969da; } .POST { color: #1a7f37; } .PUT { color: #9a6700; }
  .PATCH { color: #8250df; } .DELETE { color: #cf222e; } .HEAD { color: #57606a; }
  .path { font-weight: 600; }
  .sum { color: #57606a; margin-left: .5rem; }
  details { margin-top: .4rem; }
  pre { background: #f6f8fa; padding: .6rem; border-radius: 6px; overflow-x: auto; font-size: .85em; }
  .tag { font-size: 1rem; margin-top: 1.2rem; border-bottom: 1px solid #ddd; padding-bottom: .2rem; }
</style>
</head>
<body>
<h1 id="title">API Contract</h1>
<p><a href="/docs/openapi.json">openapi.json</a></p>
<div id="ops">loading…</div>
<script>
fetch('/docs/openapi.json').then(r => r.json()).then(doc => {
  document.getElementById('title').textContent =
    (doc.info && doc.info.title || 'API') + ' ' + (doc.info && doc.info.version || '');
  const groups = {};
  for (const [path, item] of Object.entries(doc.paths || {})) {
    for (const [method, op] of Object.entries(item)) {
      const tag = (op.tags && op.tags[0]) || 'default';
      (groups[tag] = groups[tag] || []).push({ path, method: method.toUpperCase(), op });
    }
  }
  const root = document.getElementById('ops');
  root.textContent = '';
  for (const tag of Object.keys(groups).sort()) {
    const h = document.createElement('div');
    h.className = 'tag'; h.textContent = tag;
    root.appendChild(h);
    for (const { path, method, op } of groups[tag]) {
      const d = document.createElement('div');
      d.className = 'op';
      const det = document.createElement('details');
      const sum = document.createElement('summary');
      sum.innerHTML = '<span class="m ' + method + '">' + method + '</span>' +
        '<span class="path"></span><span class="sum"></span>';
      sum.querySelector('.path').textContent = path;
      sum.querySelector('.sum').textContent = op.summary || '';
      const pre = document.createElement('pre');
      pre.textContent = JSON.stringify(op, null, 2);
      det.appendChild(sum); det.appendChild(pre); d.appendChild(det);
      root.appendChild(d);
    }
  }
}).catch(e => { document.getElementById('ops').textContent = 'failed: ' + e; });
</script>
</body>
</html>`
