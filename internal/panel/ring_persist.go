package panel

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ringFlushInterval = 5 * time.Second

// ringFile 旧格式（纯 JSON 数组包装）。仅为向后兼容读取；当前落盘用逐行文本。
type ringFile struct {
	Lines []string `json:"lines"`
}

// Open 绑定落盘路径：有文件则读回最近若干行，并启动后台落盘。空路径 = 不落盘。
func (r *Ring) Open(path string) {
	if path == "" {
		return
	}
	r.mu.Lock()
	r.path = path
	r.mu.Unlock()
	r.load()
	r.writePlain() // 旧格式转成一行一条，后续才能安全追加
	go r.flusher()
}

func (r *Ring) markDirty() { r.dirty.Store(true) }

func (r *Ring) flusher() {
	t := time.NewTicker(ringFlushInterval)
	defer t.Stop()
	for range t.C {
		r.Flush()
	}
}

// Flush 把当前缓冲写到磁盘。无变更不写。
func (r *Ring) Flush() {
	if !r.dirty.Swap(false) {
		return
	}
	r.mu.Lock()
	path := r.path
	r.mu.Unlock()
	if path == "" {
		return
	}
	r.writePlain()
}

// writePlain 全量重写：文本一行一条（带频道标记前缀，读回时还原）。
//
// 为什么带频道前缀：频道的判定规则可能随版本演进（比如新加 keepalive 频道），
// 落盘时把判定结果固化下来，读回后历史行的频道不会因为规则变化而漂移。
func (r *Ring) writePlain() {
	r.mu.Lock()
	path := r.path
	entries := make([]LogEntry, len(r.entries))
	copy(entries, r.entries)
	r.mu.Unlock()
	if path == "" {
		return
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Ch)
		b.WriteByte('\t')
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		log.Printf("panel logs: 落盘失败: %v", err)
		r.dirty.Store(true)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("panel logs: 落盘改名失败: %v", err)
		r.dirty.Store(true)
	}
}

// parseLogFile 兼容三种形态：带频道前缀的文本行 / 纯文本行 / 旧 JSON 包装。
func parseLogFile(raw []byte) []LogEntry {
	trim := strings.TrimSpace(string(raw))
	if trim == "" {
		return nil
	}
	// 旧 JSON 包装（{"lines":[...]}）。
	if trim[0] == '{' {
		var f ringFile
		if json.Unmarshal([]byte(trim), &f) == nil && len(f.Lines) > 0 {
			out := make([]LogEntry, 0, len(f.Lines))
			for _, line := range f.Lines {
				out = append(out, LogEntry{Ch: classifyLine(line), Text: line})
			}
			return out
		}
	}
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	var out []LogEntry
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		// 带频道前缀：<ch>\t<text>；无前缀的旧行按当前规则现判。
		if i := strings.IndexByte(line, '\t'); i > 0 {
			ch := line[:i]
			if ch == ChChat || ch == ChTask || ch == ChKeepalive || ch == ChSys {
				out = append(out, LogEntry{Ch: ch, Text: line[i+1:]})
				continue
			}
		}
		out = append(out, LogEntry{Ch: classifyLine(line), Text: line})
	}
	return out
}

func (r *Ring) load() {
	r.mu.Lock()
	path := r.path
	r.mu.Unlock()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	entries := parseLogFile(raw)
	if len(entries) > r.cap {
		entries = entries[len(entries)-r.cap:]
	}
	r.mu.Lock()
	r.entries = entries
	r.mu.Unlock()
}
