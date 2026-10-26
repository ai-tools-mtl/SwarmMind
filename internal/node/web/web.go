// Package web 承载 Node 本地控制台前端（Loop 7，INTERACTION 客户端三旅程）：
// 无框架原生 JS 单页（index.html + app.js + style.css），go:embed 编译进 node
// 二进制——node 启动即带控制台，无外部静态文件依赖。视觉与 hub 控制台同源
// （Apple 灰阶工具 token，见 style.css :root，FE-PLAN v3）。
//
// 挂载约定（localapi Handler）：GET / 控制台入口 + /app.js /style.css 静态资源，
// 免令牌（令牌输入页必须可达）；/api/v1/* 数据接口另行鉴权（Bearer local_token，
// 前端持久化后随请求携带），两侧不冲突。
package web

import (
	"embed"
	"net/http"
	"strings"

	brandassets "github.com/mtl802/swarmmind/assets"
)

//go:embed index.html app.js style.css
var assets embed.FS

// contentType 按扩展名定 Content-Type；控制台仅三类文件，显式列举防遗漏。
var contentType = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// Handler 返回控制台静态资源处理器：/ → index.html（SPA 入口，hash 路由在前端），
// /app.js、/style.css → 同名文件。仅放行白名单内文件（embed 集合即白名单），
// 不做路径拼接，无目录列举与穿越面。
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/logo.svg" || r.URL.Path == "/favicon.svg" {
			brandassets.ServeLogo(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		b, err := assets.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if ct, ok := contentType[strings.ToLower(name[strings.LastIndexByte(name, '.'):])]; ok {
			w.Header().Set("Content-Type", ct)
		}
		// 资源随二进制发布：禁缓存，避免升级后浏览器拿到旧 JS/旧 API 组合
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
}
