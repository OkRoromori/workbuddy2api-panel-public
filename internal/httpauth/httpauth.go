// Package httpauth 网关与面板共用的 Bearer 鉴权原语。
//
// 单独成包的原因：server（/v1/*、/status）与 panel（/panel/api/*）两处鉴权
// 必须完全同口径——此前各自复制了一份"字符串直接比较"的实现，既容易漂移，
// 又都带计时侧信道。统一到这里后，口径只有一份，且天然常量时间比较。
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

const bearerScheme = "Bearer"

// tokenFrom 取出请求携带的密钥：优先 `Authorization: Bearer`，回落到 `x-api-key`。
//
// 为什么要认 x-api-key：**Anthropic 协议用的就是它**（Claude Code、ccswitch、
// 官方 SDK 都只发 x-api-key，不发 Authorization）。只认 Bearer 的话，
// 接 `/v1/messages` 的客户端会一律 401——而 401 看起来像"密钥填错了"，
// 排查时会一路去核对密钥，很难想到真正原因是"协议用的认证头不一样"。
//
// 空串表示没出示密钥：`Fingerprint("")` 也是空串，两者语义必须一致。
func tokenFrom(r *http.Request) string {
	if authz := r.Header.Get("Authorization"); len(authz) > len(bearerScheme) &&
		strings.EqualFold(authz[:len(bearerScheme)], bearerScheme) && authz[len(bearerScheme)] == ' ' {
		return authz[len(bearerScheme)+1:]
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

// VerifyBearer 校验请求头是否携带正确的 Bearer 密钥。
//
// key 为空表示"未启用鉴权"，恒返回 true（调用方据此放行）。
// 比较用 SHA-256 摘要 + subtle.ConstantTimeCompare：
//   - 常量时间，不因前缀匹配长度而泄露信息；
//   - 先摘要再比较，长度差异被吸收进摘要（不会因长度不同提前返回）；
//   - 摘要本身不可逆，即便有侧信道也拿不到密钥原文。
func VerifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return true
	}
	tok := tokenFrom(r)
	if tok == "" {
		// 没出示密钥：仍走一次摘要比较，保持耗时形状一致。
		subtle.ConstantTimeCompare(digest(""), digest(key))
		return false
	}
	return subtle.ConstantTimeCompare(digest(tok), digest(key)) == 1
}

// VerifyBearerAny 在多密钥表里校验请求头，返回匹配到的**密钥名**与**密钥指纹**。
//
// keys 为 密钥 → 名字 的映射；空表表示"未启用鉴权"，恒返回 ("", "", true)。
// 名字用于人读（审计里显示"谁用的"），指纹用于**稳定标识是哪一把密钥**——
// 名字会被改，改了之后按名字统计的历史用量就归零了；指纹不会变。
//
// 与 VerifyBearer 一样做摘要 + 常量时间比较，且**不提前返回**：即便第 1 个
// 密钥就命中，也要把所有条目比完，否则响应耗时能反推出"命中的是第几个密钥"。
func VerifyBearerAny(r *http.Request, keys map[string]string) (name, fingerprint string, ok bool) {
	if len(keys) == 0 {
		return "", "", true
	}
	tok := tokenFrom(r)
	dt := digest(tok)
	matched := ""
	for k, v := range keys {
		if subtle.ConstantTimeCompare(dt, digest(k)) == 1 {
			// 不给名字留空：审计里空名会和"老流水没这个字段"混淆。
			if v == "" {
				v = "默认"
			}
			name, matched, ok = v, k, true
		}
	}
	return name, Fingerprint(matched), ok
}

// Fingerprint 返回密钥的短指纹（12 位十六进制）。
//
// 用途：日志与审计需要"这是哪一把密钥"这个信息，但不能写入密钥本身
//（那些文件会被备份、被下载、被翻看）。取 SHA-256 前 6 字节足够区分家用规模的密钥，
// 又短到可以进表格和 URL。
//
// 空密钥返回空串——"没出示密钥"和"出示了某把密钥"必须能分开。
func Fingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// digest 返回 s 的 SHA-256（定长 32 字节，供常量时间比较）。
func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
