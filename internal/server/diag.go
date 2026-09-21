package server

import (
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// diag.go **临时诊断**（环境变量控制，用完即失效）。
//
// 用途一：内容拦截（11128）的请求体落盘，供离线二分定位触发点。
// 用途二：记录客户端请求了哪些路径 —— 排查"某客户端拉不到模型列表"这类问题时，
// 与其猜它是拼错路径还是压根没发请求，不如直接看它发了什么。
//
// 开关是**环境变量**而不是配置项，也刻意不写进 config.json：
// 它会把请求路径/对话内容写进磁盘，不该是常驻能力；诊断完去掉环境变量即失效。
//
// 目录也在 env 里指定：服务有 ProtectSystem=strict + PrivateTmp，
// 只能写 ReadWritePaths 列出的路径，/tmp 是私有的、外面看不见。
const (
	dumpBlockedEnv = "WB2A_DUMP_BLOCKED"
	logReqEnv      = "WB2A_LOG_REQ"
	// 只保留最近几份：一次排查一两份就够，留多了既占盘又扩大内容暴露面。
	dumpKeep = 3
)

var dumpMu sync.Mutex

func stamp() string { return time.Now().Format("20060102-150405.000") }

// dumpBlockedBody 在开启诊断时把出站请求体写盘。未开启则立即返回（零开销）。
func dumpBlockedBody(body []byte) {
	dir := os.Getenv(dumpBlockedEnv)
	if dir == "" || len(body) == 0 {
		return
	}
	dumpMu.Lock()
	defer dumpMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(dir+"/blocked-"+stamp()+".json", body, 0o600); err != nil {
		return
	}
	pruneDumps(dir)
}

// pruneDumps 只留最近 dumpKeep 份（文件名带时间戳，字典序即时间序）。
func pruneDumps(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) <= dumpKeep {
		return
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-dumpKeep] {
		_ = os.Remove(dir + "/" + n)
	}
}

// statusRecorder 记下状态码，供请求日志使用。
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// Flush 保留底层 writer 的流式刷新能力，避免开启请求日志后把 SSE 变成缓冲响应。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 让 http.ResponseController 等标准库能力探测继续找到底层 writer。
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests 请求日志中间件（仅 WB2A_LOG_REQ 打开时挂上）。
//
// **不记录密钥原文**：只记"有没有带 Authorization / x-api-key"，够判断认证方式。
// 记原文等于把密钥写进日志文件——那是不能碰的红线。
func logRequests(next http.Handler, dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sr := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(sr, r)

		auth := "-"
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			auth = "Bearer"
		} else if r.Header.Get("x-api-key") != "" {
			auth = "x-api-key"
		}
		ua := r.Header.Get("User-Agent")
		if len(ua) > 60 {
			ua = ua[:60]
		}
		line := strings.Join([]string{
			stamp(), r.Method, r.URL.RequestURI(),
			"auth=" + auth, "ua=" + ua, "->" + strconv.Itoa(sr.code),
		}, " | ")

		dumpMu.Lock()
		defer dumpMu.Unlock()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		f, err := os.OpenFile(dir+"/requests.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	})
}
