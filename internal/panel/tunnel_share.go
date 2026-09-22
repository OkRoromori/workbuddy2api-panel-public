package panel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TunnelShareConfig 是生成连接脚本所需的服务器侧信息（给另一台电脑用）。
// AuthorizedKeysPath 指向专供 sshd AuthorizedKeysCommand 读取的文件。
type TunnelShareConfig struct {
	SSHHost            string
	SSHPort            int
	SSHUser            string
	GatewayPort        int
	AuthorizedKeysPath string
	HostPublicKey      string
}

func (c TunnelShareConfig) enabled() bool {
	return strings.TrimSpace(c.SSHHost) != "" && c.SSHPort > 0 &&
		strings.TrimSpace(c.SSHUser) != "" && c.GatewayPort > 0 &&
		strings.TrimSpace(c.AuthorizedKeysPath) != "" && strings.TrimSpace(c.HostPublicKey) != ""
}

func tunnelKeyMarker(id string) string { return "wb2api-share-" + id }

func (p *Panel) keyShare(w http.ResponseWriter, r *http.Request) {
	if !p.cfg.TunnelShare.enabled() {
		writeErr(w, http.StatusNotImplemented, "连接脚本尚未在服务器启用")
		return
	}
	v, err := p.loadKeys(w)
	if err != nil {
		return
	}
	id := r.PathValue("id")
	if v.Owner != "" && keyID(v.Owner) == id {
		writeErr(w, http.StatusBadRequest, "默认密钥不能生成连接脚本：请先新建一把独立密钥")
		return
	}
	apiKey := ""
	for key := range v.Extra {
		if keyID(key) == id {
			apiKey = key
			break
		}
	}
	if apiKey == "" {
		writeErr(w, http.StatusNotFound, "找不到这把密钥（可能已被删除）")
		return
	}

	privatePEM, publicLine, err := generateOpenSSHKey(tunnelKeyMarker(id))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成隧道密钥失败："+err.Error())
		return
	}
	if err := p.storeTunnelKey(id, publicLine); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存隧道授权失败："+err.Error())
		return
	}

	body := makeShareBatch(p.cfg.TunnelShare, id, apiKey, privatePEM)
	w.Header().Set("Content-Type", "application/x-msdos-program")
	stamp := time.Now().Format("20060102-150405")
	w.Header().Set("Content-Disposition", `attachment; filename="workbuddy-share-`+id+`-`+stamp+`.bat"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (p *Panel) storeTunnelKey(id, publicLine string) error {
	p.tunnelMu.Lock()
	defer p.tunnelMu.Unlock()
	cfg := p.cfg.TunnelShare
	line := fmt.Sprintf(`restrict,port-forwarding,permitopen="127.0.0.1:%d" %s`,
		cfg.GatewayPort, strings.TrimSpace(publicLine))
	return rewriteTunnelKeys(cfg.AuthorizedKeysPath, tunnelKeyMarker(id), line)
}

func (p *Panel) revokeTunnelKey(id string) error {
	if !p.cfg.TunnelShare.enabled() {
		return nil
	}
	p.tunnelMu.Lock()
	defer p.tunnelMu.Unlock()
	return rewriteTunnelKeys(p.cfg.TunnelShare.AuthorizedKeysPath, tunnelKeyMarker(id), "")
}

func rewriteTunnelKeys(path, marker, replacement string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.Contains(line, marker) {
			continue
		}
		out = append(out, line)
	}
	if replacement != "" {
		out = append(out, replacement)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data := []byte(strings.Join(out, "\n"))
	if len(data) > 0 {
		data = append(data, '\n')
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func sshString(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out
}

func generateOpenSSHKey(comment string) ([]byte, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	pubBlob := append(sshString([]byte("ssh-ed25519")), sshString(pub)...)
	check := make([]byte, 4)
	if _, err := rand.Read(check); err != nil {
		return nil, "", err
	}
	privateBlock := append([]byte{}, check...)
	privateBlock = append(privateBlock, check...)
	privateBlock = append(privateBlock, sshString([]byte("ssh-ed25519"))...)
	privateBlock = append(privateBlock, sshString(pub)...)
	privateBlock = append(privateBlock, sshString(priv)...)
	privateBlock = append(privateBlock, sshString([]byte(comment))...)
	for pad := byte(1); len(privateBlock)%8 != 0; pad++ {
		privateBlock = append(privateBlock, pad)
	}
	blob := []byte("openssh-key-v1\x00")
	blob = append(blob, sshString([]byte("none"))...)
	blob = append(blob, sshString([]byte("none"))...)
	blob = append(blob, sshString(nil)...)
	n := make([]byte, 4)
	binary.BigEndian.PutUint32(n, 1)
	blob = append(blob, n...)
	blob = append(blob, sshString(pubBlob)...)
	blob = append(blob, sshString(privateBlock)...)
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: blob})
	publicLine := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(pubBlob) + " " + comment
	return privatePEM, publicLine, nil
}

func makeShareBatch(cfg TunnelShareConfig, id, apiKey string, privatePEM []byte) []byte {
	hostKey := strings.Join(strings.Fields(cfg.HostPublicKey)[:2], " ")
	lines := []string{
		"@echo off", "setlocal", "title WorkBuddy2API",
		`set "WB2_API_KEY=` + apiKey + `"`,
		`set "WB2_BASE=http://127.0.0.1:` + strconv.Itoa(cfg.GatewayPort) + `/v1"`,
		`set "WB2_SSH_HOST=` + cfg.SSHHost + `"`,
		`set "WB2_SSH_PORT=` + strconv.Itoa(cfg.SSHPort) + `"`,
		`set "WB2_SSH_USER=` + cfg.SSHUser + `"`,
		`set "WB2_HOST_KEY=` + hostKey + `"`,
		`set "WB2_KEY_B64=` + base64.StdEncoding.EncodeToString(privatePEM) + `"`,
		`set "WB2_SHARE_ID=` + id + `"`,
		`powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -Command "$raw=[IO.File]::ReadAllText('%~f0'); Invoke-Expression $raw.Substring($raw.IndexOf(('#PS'+'START'))+8)"`,
		"exit /b %errorlevel%", "#PSSTART", sharePowerShell(),
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func sharePowerShell() string {
	return `$ErrorActionPreference = 'Stop'
$host.UI.RawUI.WindowTitle = 'WorkBuddy2API - 安全隧道'
$base = $env:WB2_BASE
$apiKey = $env:WB2_API_KEY
$baseUri = [Uri]$base
$port = $baseUri.Port
$healthURL = $baseUri.GetLeftPart([UriPartial]::Authority) + '/healthz'
$stem = Join-Path $env:TEMP ('wb2api-share-' + $env:WB2_SHARE_ID)
$keyPath = $stem + '.key'
$knownPath = $stem + '.known_hosts'
$sshLogPath = $stem + '.ssh.log'
$sshProcess = $null
function Get-SSHFailure([string]$path) {
  $detail = if (Test-Path -LiteralPath $path) { (Get-Content -LiteralPath $path -Raw).Trim() } else { '' }
  if ($detail -match 'Permission denied \(publickey\)') { return '连接密钥已失效。请删除旧脚本，并从面板重新下载最新连接脚本。' }
  if ($detail -match 'UNPROTECTED PRIVATE KEY FILE|bad permissions|Load key.*Permission denied') { return '临时 SSH 密钥权限不符合要求，请尝试以管理员身份运行。' }
  if ($detail -match 'REMOTE HOST IDENTIFICATION HAS CHANGED|Host key verification failed') { return '服务器身份校验失败，请回到面板重新生成连接脚本。' }
  if ($detail -match 'Connection timed out|Connection refused|Connection reset|Could not resolve hostname|No route to host') { return '无法连接服务器，请检查网络、防火墙或加速器设置。' }
  if ($detail -match 'Address already in use|cannot listen to port') { return "本机端口 $port 已被占用，请先关闭已有的 WorkBuddy2API 连接后重试。" }
  if ($detail) { return 'SSH 隧道启动失败：' + $detail }
  return 'SSH 隧道进程提前退出，请检查网络或联系管理员。'
}
try {
  $portProbe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $port)
  try { $portProbe.Start() } catch { throw "本机端口 $port 已被占用，请先关闭已有的 WorkBuddy2API 连接后重试。" } finally { $portProbe.Stop() }
  [IO.File]::WriteAllBytes($keyPath, [Convert]::FromBase64String($env:WB2_KEY_B64))
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
  & icacls.exe $keyPath /inheritance:r /grant:r "${identity}:(F)" | Out-Null
  if ($LASTEXITCODE -ne 0) { throw '无法保护临时 SSH 密钥文件，请检查当前用户权限。' }
  $knownHost = if ([int]$env:WB2_SSH_PORT -eq 22) { $env:WB2_SSH_HOST } else { '[' + $env:WB2_SSH_HOST + ']:' + $env:WB2_SSH_PORT }
  [IO.File]::WriteAllText($knownPath, $knownHost + ' ' + $env:WB2_HOST_KEY + [Environment]::NewLine, [Text.Encoding]::ASCII)
  Remove-Item -LiteralPath $sshLogPath -Force -ErrorAction SilentlyContinue
  $sshArgs = @('-i', ('"' + $keyPath + '"'), '-p', $env:WB2_SSH_PORT, '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes', '-o', ('UserKnownHostsFile="' + $knownPath + '"'), '-o', 'ExitOnForwardFailure=yes', '-o', 'LogLevel=ERROR', '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=3', '-N', '-L', ($port.ToString() + ':127.0.0.1:' + $port), ($env:WB2_SSH_USER + '@' + $env:WB2_SSH_HOST))
  Write-Host ''
  Write-Host '============================================================' -ForegroundColor DarkCyan
  Write-Host '                 WorkBuddy2API 安全隧道' -ForegroundColor Cyan
  Write-Host '============================================================' -ForegroundColor DarkCyan
  Write-Host ''
  Write-Host '[1/3] 正在建立安全隧道...' -ForegroundColor Yellow
  $sshProcess = Start-Process -FilePath 'ssh.exe' -ArgumentList $sshArgs -PassThru -NoNewWindow -RedirectStandardError $sshLogPath
  $ready = $false
  for ($i = 0; $i -lt 30; $i++) {
    if ($sshProcess.HasExited) { $sshProcess.WaitForExit(); throw (Get-SSHFailure $sshLogPath) }
    try { Invoke-RestMethod -Uri $healthURL -TimeoutSec 2 | Out-Null; $ready = $true; break } catch { Start-Sleep -Seconds 1 }
  }
  if (-not $ready) { throw '连接超时：隧道未能在 30 秒内就绪。' }
  Write-Host ''
  Write-Host '[2/3] 安全隧道连接成功' -ForegroundColor Green
  Write-Host ''
  Write-Host '-------------------- 连接信息 --------------------' -ForegroundColor DarkCyan
  Write-Host ('接口地址 : ' + $base)
  Write-Host ('API 密钥 : ' + $apiKey)
  Write-Host ''
  Write-Host '[3/3] 可用模型及当前倍率' -ForegroundColor Green
  try {
    $result = Invoke-RestMethod -Uri ($base + '/models') -Headers @{ Authorization = 'Bearer ' + $apiKey } -TimeoutSec 30
    foreach ($model in $result.data) {
      $rate = if ($model.credits) { $model.credits } else { '倍率未知' }
      Write-Host ('  {0,-38} {1}' -f $model.id, $rate)
    }
  } catch { Write-Host ('  模型列表获取失败：' + $_.Exception.Message) -ForegroundColor Yellow }
  Write-Host ''
  Write-Host '--------------------------------------------------' -ForegroundColor DarkCyan
  Write-Host '请保持此窗口打开，按 Enter 键断开连接。' -ForegroundColor Cyan
  [void](Read-Host)
} catch {
  Write-Host ''
  Write-Host '============================================================' -ForegroundColor DarkRed
  Write-Host ('连接失败：' + $_.Exception.Message) -ForegroundColor Red
  Write-Host '============================================================' -ForegroundColor DarkRed
  [void](Read-Host '按 Enter 键关闭')
} finally {
  if ($sshProcess -and -not $sshProcess.HasExited) { Stop-Process -Id $sshProcess.Id -Force -ErrorAction SilentlyContinue }
  Remove-Item -LiteralPath $keyPath, $knownPath, $sshLogPath -Force -ErrorAction SilentlyContinue
}`
}
