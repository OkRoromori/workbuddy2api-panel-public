// alias.go 账号别名：面板本地的「显示名」覆盖层。
//
// 为什么不直接改 auths/ 里凭据 JSON 的 nickname：
//   - 凭据文件由 auth.Auth.SaveAtomic 在每次 token 刷新时**整份重写**，
//     往里塞自定义字段会被下一次刷新抹掉；
//   - 那文件含 accessToken/refreshToken，越少改动越好。
//
// 所以别名独立存 data/aliases.json（与 state.json 同目录），纯展示层：
// 只影响面板怎么显示账号，不参与选号、不影响上游账号本身。
package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// aliasMaxLen 别名长度上限（按 rune 计，避免把中文从中间截断）。
const aliasMaxLen = 32

// aliasStore 别名表：uid → 显示名。空表等价于「全部用原始昵称」。
type aliasStore struct {
	mu   sync.RWMutex
	path string
	m    map[string]string
}

func newAliasStore(path string) *aliasStore {
	s := &aliasStore{path: path, m: map[string]string{}}
	s.load()
	return s
}

// load 读取别名文件。文件不存在 / 不可读 / 内容非法一律当作空表处理：
// 别名是纯展示功能，绝不能因为它让服务起不来。
func (s *aliasStore) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return
	}
	s.m = m
}

func (s *aliasStore) get(uid string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[uid]
}

// set 写入别名；alias 为空表示删除该条。返回落盘错误。
func (s *aliasStore) set(uid, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias == "" {
		delete(s.m, uid)
	} else {
		s.m[uid] = alias
	}
	return s.saveLocked()
}

// saveLocked 原子落盘：先写 .tmp 再 rename，避免中途崩溃留下半个 JSON。
// 不写 BOM（与 config.json 同口径，保证任何 JSON 读取方都能解析）。
func (s *aliasStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// normalizeAlias 清洗别名：去首尾空白、拒绝换行/制表符与控制字符。
// 返回清洗后的值与错误信息（错误信息为空表示通过）。
func normalizeAlias(raw string) (string, string) {
	a := strings.TrimSpace(raw)
	for _, r := range a {
		if r < 0x20 || r == 0x7f {
			return "", "别名不能包含换行、制表符或控制字符"
		}
	}
	if n := len([]rune(a)); n > aliasMaxLen {
		return "", "别名过长（" + strconv.Itoa(n) + " 字符，上限 " + strconv.Itoa(aliasMaxLen) + "）"
	}
	return a, ""
}
