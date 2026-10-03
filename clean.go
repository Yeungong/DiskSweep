package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Clean levels.
const (
	LevelSafe     = "safe"     // safe to delete, no rebuild cost
	LevelModerate = "moderate" // rebuilds on next use, may be slow
	LevelCautious = "cautious" // deleting forces re-download or re-login
)

// infoOnlyRuleIDs is the authoritative set of targets DiskSweep refuses to
// delete. It is kept separate from the rule table so ExecuteClean can enforce
// the guarantee cheaply, without rebuilding every rule definition (which would
// re-run glob expansion and disk reads) just to check a flag.
//
// These directories look like "wasted space" but are load-bearing: WinSxS and
// Windows\Installer back Windows Update, uninstall and repair; WindowsApps is
// the Store apps themselves. All three are serviceable only by DISM or Windows
// itself. A cleanup tool that deletes them breaks the OS.
//
// TestInfoOnlyRulesMatchGuard asserts this set never drifts from the InfoOnly
// flags in cleanItemDefs.
var infoOnlyRuleIDs = map[string]bool{
	"winsxs":            true,
	"windows_installer": true,
	"windows_apps":      true,
}

// CleanItem is one rule-driven cleanup target.
type CleanItem struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Level         string   `json:"level"`
	Paths         []string `json:"paths"`                // candidate paths (probed for existence)
	PathGlobs     []string `json:"pathGlobs,omitempty"`  // glob patterns expanded at probe time (e.g. "*-updater")
	Match         string   `json:"match,omitempty"`      // "" | "glob:<pattern>" | "ext:<ext>" | "name:<name>"
	MaxAgeDays    int      `json:"maxAgeDays,omitempty"` // >0: only entries older than N days
	RequiresAdmin bool     `json:"requiresAdmin"`
	Exists        bool     `json:"exists"`
	Size          int64    `json:"size"`
	FileCount     int      `json:"fileCount"`
	Drive         string   `json:"drive"` // primary drive of the first existing path; "ALL" for multi-drive items

	// InfoOnly marks a target that DiskSweep will never delete: it exists purely
	// so the user can see where the space went. Components like WinSxS and
	// Windows\Installer must only be serviced by DISM, and deleting them by
	// hand breaks servicing and Store apps. ExecuteClean refuses these outright
	// (see delete.go), so the guarantee does not depend on the UI hiding them.
	InfoOnly bool `json:"infoOnly"`
	// InfoNote explains how to actually reclaim the space, shown in the UI
	// instead of a clean action.
	InfoNote string `json:"infoNote,omitempty"`

	// ProtectRecentMinutes keeps cleanup away from entries that were touched very
	// recently, and is the guard that makes cleaning a shared, live directory
	// like %TEMP% safe.
	//
	// Programs routinely save state atomically: write "X.json.2.tmp", then
	// rename it over "X.json". That temp file exists for only an instant, so any
	// tool that scans and deletes %TEMP% concurrently can remove it in the
	// window between write and rename -- the program then fails with ENOENT and
	// looks broken for no visible reason. That is exactly what happened: a
	// WorkBuddy session died because DiskSweep deleted
	// %TEMP%\workbuddy-conversation-product-*\acc-product-config-*.json.2.tmp.
	//
	// Age alone is not a safe signal either, because a file opened at process
	// start is legitimately old, so this is deliberately a "recently touched"
	// (mtime) window rather than a filesystem-lock check: the lock is released
	// between the write and the rename, which is precisely when we must not act.
	//
	// >0 also forces per-entry handling: the whole-directory fast path is
	// skipped, because a directory as a whole has no meaningful mtime.
	ProtectRecentMinutes int `json:"protectRecentMinutes,omitempty"`

	// NoAutoCheck keeps the UI from pre-selecting this rule. Rules aggressive
	// enough to affect running programs must be an explicit opt-in.
	NoAutoCheck bool `json:"noAutoCheck,omitempty"`

	// Children breaks a target down into its notable parts, so the user can see
	// *what* is taking the space (which Store app, for example) instead of only
	// a total. Only populated for rules that opt in; see windowsAppsBreakdown.
	Children []ItemChild `json:"children,omitempty"`
	// ChildCount is how many distinct parts exist, which can exceed
	// len(Children) when only the largest are listed.
	ChildCount int `json:"childCount,omitempty"`
}

// ItemChild is one notable entry inside a cleanup target.
type ItemChild struct {
	Name string `json:"name"`         // friendly label shown in the UI
	ID   string `json:"id,omitempty"` // raw identity (e.g. Store package name)
	Size int64  `json:"size"`
}

// expandPathGlobs resolves patterns such as
// %LOCALAPPDATA%\*-updater into the concrete directories that exist right now.
// Rules use this for folders whose names change between releases (every app
// ships its own "*-updater" cache dir), so a single rule covers them all
// instead of one hard-coded rule per product.
//
// Only the last path element may be a glob: dir/*/sub has no unambiguous
// meaning here, and a recursive wildcard is exactly what we do not want in a
// deletion rule.
func expandPathGlobs(patterns []string) []string {
	out := []string{}
	for _, pat := range patterns {
		dir := filepath.Dir(pat)
		base := filepath.Base(pat)
		if !strings.ContainsAny(base, "*?") {
			if _, err := os.Stat(pat); err == nil {
				out = append(out, pat)
			}
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if ok, _ := filepath.Match(base, e.Name()); ok {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func localAppData() string {
	if p := os.Getenv("LOCALAPPDATA"); p != "" {
		return p
	}
	return filepath.Join(homeDir(), "AppData", "Local")
}

func systemRoot() string {
	if p := os.Getenv("SystemRoot"); p != "" {
		return p
	}
	return `C:\Windows`
}

// cleanItemDefs returns the full rule set. Rules whose paths don't exist are
// filtered out by CleanupItems.
//
// PathGlobs are resolved here rather than only at probe time: every rule must
// leave this function with a concrete, non-empty Paths list representing what
// will actually be deleted. Probe-time globbing alone let UI (probe) and
// cleanup (execute) disagree about the target set.
func (a *App) cleanItemDefs() []CleanItem {
	home := homeDir()
	local := localAppData()
	la := func(rel ...string) string { return filepath.Join(append([]string{local}, rel...)...) }
	hp := func(rel ...string) string { return filepath.Join(append([]string{home}, rel...)...) }
	win := func(rel ...string) string { return filepath.Join(append([]string{systemRoot()}, rel...)...) }

	return expandDefs([]CleanItem{
		{
			ID: "recycle_bin", Name: "回收站",
			Description: "清空所有磁盘的回收站",
			Level:       LevelSafe, Paths: a.recycleBinPaths(), Drive: "ALL",
		},
		{
			ID: "temp_user", Name: "用户临时文件",
			Description: "超过 7 天的 %TEMP% 临时文件",
			Level:       LevelSafe, Paths: []string{la("Temp")}, MaxAgeDays: 7,
		},
		{
			// The most aggressive temp rule, and the one that must be safest.
			//
			// It used to have no Match and no MaxAgeDays, so it took the
			// whole-directory path and tried to recycle all of %TEMP% in one
			// move -- which deletes the working files of whatever is running.
			// It was also LevelSafe, so the UI pre-selected it. That combination
			// killed a live WorkBuddy session (see ProtectRecentMinutes).
			//
			// Now: per-entry only, nothing touched in the last 30 minutes, and
			// never pre-selected.
			ID: "temp_user_all", Name: "用户临时文件（全量）",
			Description: "清理 %TEMP% 中 30 分钟内未被改动的文件。" +
				"正在写入的临时文件会被保留：很多程序用「先写临时文件、再改名」的方式保存状态，删掉会导致对方报错甚至数据丢失",
			Level:                LevelSafe,
			Paths:                []string{la("Temp")},
			ProtectRecentMinutes: 30,
			NoAutoCheck:          true,
		},
		{
			ID: "temp_windows", Name: "Windows 临时文件",
			Description: "超过 7 天的 Windows\\Temp",
			Level:       LevelSafe, Paths: []string{win("Temp")}, MaxAgeDays: 7, RequiresAdmin: true,
		},
		{
			ID: "crash_dumps", Name: "崩溃转储",
			Description: "应用崩溃产生的 .dmp 转储文件",
			Level:       LevelSafe, Paths: []string{la("CrashDumps")}, Match: "ext:.dmp",
		},
		{
			ID: "wer", Name: "Windows 错误报告",
			Description: "ProgramData\\Microsoft\\Windows\\WER",
			Level:       LevelSafe, Paths: []string{filepath.Join(programData(), "Microsoft", "Windows", "WER")}, RequiresAdmin: true,
		},
		{
			ID: "thumb_cache", Name: "缩略图缓存",
			Description: "Explorer 缩略图缓存（重启资源管理器后生效）",
			Level:       LevelSafe, Paths: []string{la("Microsoft", "Windows", "Explorer")}, Match: "glob:thumbcache_*",
		},
		{
			ID: "browser_cache", Name: "浏览器缓存",
			Description: "Chrome / Edge 的页面缓存",
			Level:       LevelSafe,
			Paths: []string{
				la("Google", "Chrome", "User Data", "Default", "Cache"),
				la("Google", "Chrome", "User Data", "Default", "Code Cache"),
				la("Microsoft", "Edge", "User Data", "Default", "Cache"),
				la("Microsoft", "Edge", "User Data", "Default", "Code Cache"),
			},
		},
		{
			ID: "dx_shader", Name: "DirectX 着色器缓存",
			Description: "游戏与应用着色器缓存（重建后首次运行稍慢）",
			Level:       LevelModerate, Paths: []string{la("D3DSCache")},
		},
		{
			// NVIDIA's own shader cache is separate from D3DSCache and is
			// routinely one of the largest caches on a gaming machine
			// (5.2 GB measured on this box). Rebuilt automatically.
			ID: "nvidia_cache", Name: "NVIDIA 着色器缓存（DXCache/GL Cache）",
			Description: "NVIDIA 驱动的 DX 着色器缓存与 OpenGL 缓存（删除后游戏首次启动会重新编译着色器，稍慢；不影响驱动与设置）",
			Level:       LevelModerate,
			Paths: []string{
				la("NVIDIA", "DXCache"),
				la("NVIDIA", "GLCache"),
				la("NVIDIA", "ComputeCache"),
				la("NVIDIA", "NV_Cache"),
				la("NVIDIA Corporation", "NV_Cache"),
			},
		},
		{
			// NVIDIA keeps downloaded driver installers here. They are only
			// needed for uninstall/rollback; Windows keeps its own copy.
			ID: "nvidia_installer", Name: "NVIDIA 驱动安装包残留",
			Description: "NVIDIA 已下载的驱动安装包（驱动本身已安装，删除不影响使用）",
			Level:       LevelModerate,
			Paths: []string{
				la("NVIDIA Corporation", "Downloader"),
				la("NVIDIA", "NvBackend"),
				filepath.Join(programData(), "NVIDIA Corporation", "Downloader"),
				filepath.Join(programData(), "NVIDIA", "NvTelemetry"),
			},
			RequiresAdmin: true,
		},
		{
			ID: "windows_logs", Name: "Windows 日志",
			Description: "超过 30 天的 Windows\\Logs",
			Level:       LevelModerate, Paths: []string{win("Logs")}, MaxAgeDays: 30, RequiresAdmin: true,
		},
		{
			ID: "wu_downloads", Name: "Windows Update 下载缓存",
			Description: "已下载的更新安装包（系统会重新下载）",
			Level:       LevelModerate, Paths: []string{win("SoftwareDistribution", "Download")}, RequiresAdmin: true,
		},
		{
			ID: "delivery_opt", Name: "传递优化缓存",
			Description: "Windows 更新点对点传输缓存",
			Level:       LevelModerate, Paths: []string{win("SoftwareDistribution", "DeliveryOptimization")}, RequiresAdmin: true,
		},
		{
			ID: "prefetch", Name: "预读取缓存",
			Description: "Windows\\Prefetch 启动预读",
			Level:       LevelModerate, Paths: []string{win("Prefetch")}, RequiresAdmin: true,
		},
		{
			ID: "npm_cache", Name: "npm 缓存",
			Description: "npm 包缓存（需要时重新下载）",
			Level:       LevelModerate,
			Paths:       []string{la("npm-cache"), hp(".npm"), filepath.Join(roamingAppData(), "npm-cache")},
		},
		{
			ID: "pip_cache", Name: "pip 缓存",
			Description: "pip 下载缓存（需要时重新下载）",
			Level:       LevelModerate, Paths: []string{la("pip", "Cache")},
		},
		{
			ID: "go_cache", Name: "Go 模块缓存",
			Description: "GOPATH 模块缓存（重新编译时重新下载）",
			Level:       LevelModerate, Paths: []string{hp("go", "pkg", "mod")},
		},
		{
			// Separate from the module cache: build artifacts for packages you
			// compiled. Clearing only costs one slower rebuild.
			ID: "go_build_cache", Name: "Go 编译缓存",
			Description: "Go 编译中间产物缓存（go build cache；清空后首次编译稍慢，等价于 go clean -cache）",
			Level:       LevelModerate, Paths: []string{la("go-build")},
		},
		{
			ID: "pnpm_cache", Name: "pnpm 缓存",
			Description: "pnpm 全局 store 与缓存（需要时重新下载）",
			Level:       LevelModerate,
			Paths:       []string{la("pnpm"), la("pnpm-cache"), la("pnpm-state")},
		},
		{
			ID: "nuget_cache", Name: "NuGet 缓存",
			Description: "NuGet 包缓存（重新还原时重新下载）",
			Level:       LevelModerate, Paths: []string{hp(".nuget", "packages")},
		},
		{
			ID: "codex_data", Name: ".codex 数据",
			Description: "Codex CLI 的会话与数据（删除后需重新登录）",
			Level:       LevelCautious, Paths: []string{hp(".codex"), hp(".codex-ppt-skill")},
		},
		{
			ID: "qoder_data", Name: "Qoder 全家桶数据",
			Description: "Qoder CLI / 工作台 / 桌面的本地数据（删除后需重新登录）",
			Level:       LevelCautious,
			Paths: []string{
				hp(".qoderworkcn"), hp(".qoderwork"), hp(".qoder-cn"), hp(".qoder-cli"),
				filepath.Join(roamingAppData(), "QoderCN"), filepath.Join(roamingAppData(), "QoderWork CN"),
			},
		},
		{
			ID: "claude_data", Name: ".claude 数据",
			Description: "Claude Code 的会话、配置与插件（删除后需重新登录）",
			Level:       LevelCautious, Paths: []string{hp(".claude")},
		},
		{
			ID: "claude_cli_node", Name: "Claude CLI Node 运行时",
			Description: "claude-cli-nodejs 运行时缓存（删除后自动重新下载）",
			Level:       LevelModerate, Paths: []string{la("claude-cli-nodejs")},
		},
		{
			ID: "mimocode_data", Name: ".mimocode 数据",
			Description: "MimoCode 的本地数据",
			Level:       LevelCautious, Paths: []string{hp(".mimocode")},
		},
		{
			ID: "cherry_studio", Name: "Cherry Studio 数据",
			Description: "Cherry Studio 桌面端缓存与数据（删除后需重新配置）",
			Level:       LevelCautious,
			Paths:       []string{hp(".cherrystudio"), filepath.Join(roamingAppData(), "CherryStudio")},
		},
		{
			ID: "kimi_data", Name: "Kimi 数据",
			Description: "Kimi 桌面端 / kimi-code / kimi-work 的本地数据",
			Level:       LevelCautious,
			Paths: []string{
				filepath.Join(roamingAppData(), "kimi-desktop"), la("kimi-code"),
				hp(".kimi-code"), hp(".kimi-webbridge"), hp(".kimi-work"),
			},
		},
		{
			ID: "cline_data", Name: "Cline 数据",
			Description: "Cline 编程助手的本地数据（含 VS Code 扩展缓存）",
			Level:       LevelCautious,
			Paths: []string{
				hp(".cline"), hp(".agents"),
				filepath.Join(roamingAppData(), "Code", "User", "globalStorage", "saoudrizwan.claude-dev"),
			},
		},
		{
			ID: "copilot_data", Name: "GitHub Copilot 数据",
			Description: "Copilot CLI 与 VS Code Copilot 扩展数据",
			Level:       LevelCautious,
			Paths: []string{
				hp(".copilot"),
				filepath.Join(roamingAppData(), "Code", "User", "globalStorage", "github.copilot-chat"),
			},
		},
		{
			ID: "coze_data", Name: "Coze / 扣子数据",
			Description: "Coze 桌面端与 CLI 的本地数据（删除后需重新登录）",
			Level:       LevelCautious,
			Paths:       []string{hp(".coze"), filepath.Join(roamingAppData(), "Coze")},
		},
		{
			ID: "doubao_data", Name: "豆包数据",
			Description: "豆包桌面端与 CLI 的本地数据",
			Level:       LevelCautious,
			Paths:       []string{hp(".doubao"), filepath.Join(roamingAppData(), "Doubao")},
		},
		{
			ID: "opencode_data", Name: "OpenCode 数据",
			Description: "OpenCode 编程助手的本地数据",
			Level:       LevelCautious, Paths: []string{hp(".opencode")},
		},
		{
			ID: "cc_switch", Name: "CC Switch 数据",
			Description: "Claude Code 配置切换器数据",
			Level:       LevelCautious,
			Paths: []string{
				hp(".cc-switch"),
				filepath.Join(roamingAppData(), "com.ccswitch.desktop"),
				filepath.Join(localAppData(), "com.ccswitch.desktop"),
			},
		},
		{
			ID: "arkcli_data", Name: "arkcli 数据",
			Description: "火山方舟 arkcli 工具数据",
			Level:       LevelCautious, Paths: []string{hp(".arkcli")},
		},
		{
			ID: "dreamina_cli", Name: "即梦 AI (Dreamina)",
			Description: "即梦 CLI 的本地数据",
			Level:       LevelCautious, Paths: []string{hp(".dreamina_cli")},
		},
		{
			ID: "openchatcut", Name: "OpenChatCut 数据",
			Description: "聊天截图 / 数据处理工具的本地数据",
			Level:       LevelCautious,
			Paths:       []string{hp(".openchatcut"), filepath.Join(roamingAppData(), "openchatcut")},
		},
		{
			ID: "openai_desktop", Name: "OpenAI 桌面端数据",
			Description: "OpenAI 桌面应用的本地数据",
			Level:       LevelCautious, Paths: []string{la("OpenAI")},
		},
		{
			ID: "ai_canvas", Name: "AI CanvasPro 数据",
			Description: "AI 画布工具的本地数据",
			Level:       LevelCautious,
			Paths: []string{
				filepath.Join(roamingAppData(), "AI CanvasPro"),
				filepath.Join(localAppData(), "AI-CanvasPro"),
			},
		},
		{
			ID: "streamlit_cache", Name: "Streamlit 缓存",
			Description: "Streamlit 应用缓存（重新运行自动重建）",
			Level:       LevelModerate, Paths: []string{hp(".streamlit")},
		},
		{
			ID: "hf_cache", Name: "HuggingFace 模型缓存",
			Description: "~/.cache/huggingface 下载的模型与数据集缓存（重新下载）",
			Level:       LevelModerate, Paths: []string{hp(".cache", "huggingface")},
		},
		{
			ID: "cursor_data", Name: "Cursor 数据",
			Description: "Cursor 编辑器的本地数据与索引",
			Level:       LevelCautious,
			Paths:       []string{hp(".cursor"), filepath.Join(roamingAppData(), "Cursor")},
		},
		{
			ID: "windsurf_data", Name: "Windsurf / Codeium 数据",
			Description: "Windsurf 编辑器与 Codeium 扩展数据",
			Level:       LevelCautious,
			Paths: []string{
				hp(".codeium"),
				filepath.Join(roamingAppData(), "Windsurf"), filepath.Join(localAppData(), "Windsurf"),
			},
		},
		{
			ID: "trae_data", Name: "Trae 数据",
			Description: "Trae 编辑器的本地数据",
			Level:       LevelCautious,
			Paths:       []string{filepath.Join(roamingAppData(), "Trae"), filepath.Join(localAppData(), "Trae")},
		},
		{
			ID: "jan_data", Name: "Jan 数据",
			Description: "Jan 本地 AI 客户端的模型与数据",
			Level:       LevelCautious,
			Paths:       []string{hp("jan"), filepath.Join(roamingAppData(), "jan")},
		},
		{
			ID: "lmstudio", Name: "LM Studio 数据",
			Description: "LM Studio 的模型与缓存（模型文件很大）",
			Level:       LevelCautious, Paths: []string{hp(".lmstudio")},
		},
		{
			ID: "ollama", Name: "Ollama 模型",
			Description: "Ollama 下载的本地模型（模型文件很大，删除需重新拉取）",
			Level:       LevelCautious, Paths: []string{hp(".ollama")},
		},
		{
			ID: "chatbox", Name: "Chatbox 数据",
			Description: "Chatbox AI 客户端的本地数据",
			Level:       LevelCautious, Paths: []string{filepath.Join(roamingAppData(), "chatbox")},
		},
		{
			ID: "anythingllm", Name: "AnythingLLM 数据",
			Description: "AnythingLLM 的本地知识库与数据",
			Level:       LevelCautious, Paths: []string{hp(".anythingllm")},
		},
		{
			ID: "lobechat", Name: "LobeChat 数据",
			Description: "LobeChat 桌面端本地数据",
			Level:       LevelCautious, Paths: []string{filepath.Join(roamingAppData(), "LobeChat")},
		},
		{
			ID: "openwebui", Name: "Open WebUI 数据",
			Description: "Open WebUI 本地数据",
			Level:       LevelCautious, Paths: []string{hp(".open-webui")},
		},
		{
			ID: "nextchat", Name: "NextChat 数据",
			Description: "NextChat 桌面端本地数据",
			Level:       LevelCautious, Paths: []string{filepath.Join(roamingAppData(), "NextChat")},
		},
		{
			ID: "zed_data", Name: "Zed 数据",
			Description: "Zed 编辑器的本地数据",
			Level:       LevelCautious, Paths: []string{filepath.Join(roamingAppData(), "Zed")},
		},
		{
			ID: "n8n_data", Name: "n8n 数据",
			Description: "n8n 自动化平台的本地数据",
			Level:       LevelCautious, Paths: []string{hp(".n8n")},
		},
		{
			ID: "ultralytics", Name: "Ultralytics (YOLO)",
			Description: "Ultralytics 的配置与设置缓存",
			Level:       LevelModerate, Paths: []string{filepath.Join(roamingAppData(), "Ultralytics")},
		},
		{
			ID: "continue_ext", Name: "Continue 扩展数据",
			Description: "VS Code Continue 扩展的本地数据",
			Level:       LevelCautious, Paths: []string{filepath.Join(roamingAppData(), "Continue")},
		},
		{
			// Every Electron/auto-updating app ships its own "*-updater"
			// cache folder under %LOCALAPPDATA% and none of them ever clean up
			// after themselves. Measured ~2.9 GB across 20+ products on this
			// box. Deleting them only costs a re-download on the next update.
			ID: "updater_cache", Name: "应用更新包缓存（*-updater）",
			Description: "各桌面应用下载更新时留下的安装包缓存（WorkBuddy / MiMo / Coze / ZCode / Fiddler / Qoder 等）。删除后下次更新重新下载，不影响已安装的软件",
			Level:       LevelModerate,
			PathGlobs: []string{
				la("*-updater"),
				filepath.Join(roamingAppData(), "*-updater"),
				hp("*-updater"),
			},
		},
		{
			ID: "windows_old", Name: "Windows.old",
			Description: "旧系统备份目录（删除不可恢复）",
			Level:       LevelCautious, Paths: []string{`C:\Windows.old`}, RequiresAdmin: true,
		},
		{
			ID: "uv_cache", Name: "uv 包缓存",
			Description: "Python uv 下载的包缓存（重新安装时重新下载）",
			Level:       LevelModerate, Paths: []string{la("uv")},
		},
		{
			ID: "playwright_cache", Name: "Playwright 浏览器",
			Description: "Playwright 下载的测试用浏览器（需要时重新下载）",
			Level:       LevelModerate, Paths: []string{la("ms-playwright")},
		},
		{
			ID: "netease_cache", Name: "网易云音乐缓存",
			Description: "NetEase 客户端缓存（重新播放时重新缓存）",
			Level:       LevelModerate, Paths: []string{la("NetEase")},
		},
		{
			ID: "thunder_cache", Name: "迅雷下载缓存",
			Description: "Thunder Network 客户端缓存（重新下载时重新缓存）",
			Level:       LevelModerate, Paths: []string{la("Thunder Network")},
		},
		{
			ID: "douyin_cache", Name: "抖音/DouyinAR 缓存",
			Description: "抖音相关应用缓存（重新使用时重建）",
			Level:       LevelModerate,
			Paths: []string{
				la("DouyinAR"),
				filepath.Join(roamingAppData(), "douyin"),
			},
		},
		{
			// Blizzard game caches. Only the Cache/Logs subdirs are cleaned;
			// game data dirs (e.g. Overwatch with seasonal/event data, settings)
			// are left untouched so users never lose progress or config.
			ID: "blizzard_cache", Name: "暴雪/战网缓存",
			Description: "战网与暴雪游戏缓存（浏览器缓存/日志/错误报告，重新打开时重建；不删游戏本体与设置）",
			Level:       LevelModerate,
			Paths: []string{
				la("Battle.net", "BrowserCaches"),
				la("Battle.net", "Cache"),
				la("Battle.net", "Errors"),
				la("Battle.net", "Logs"),
				la("Battle.net", "CachedData.db"),
				la("Blizzard Entertainment", "Telemetry"),
			},
		},
		{
			// Overwatch (and other Blizzard games) store seasonal/event data
			// under %LOCALAPPDATA%\Blizzard Entertainment\<Game>\ as numbered
			// folders (each patch/event adds one). We clean the numbered
			// event-cache folders plus disposable subdirs, never game settings
			// or the game root. Cleaned data re-downloads on next launch.
			ID: "blizzard_game_cache", Name: "暴雪游戏活动数据（含守望先锋过往活动）",
			Description: "守望先锋等暴雪游戏的过往活动/赛季缓存（数字文件夹，每次活动更新都会累积；删除后重新下载，游戏设置不受影响）",
			Level:       LevelCautious,
			Paths: []string{
				la("Blizzard Entertainment", "Overwatch", "Cache"),
				la("Blizzard Entertainment", "Overwatch", "Logs"),
				la("Blizzard Entertainment", "Overwatch", "Errors"),
				la("Blizzard Entertainment", "Overwatch", "Saved"),
				la("Blizzard Entertainment", "Diablo IV", "Cache"),
				la("Blizzard Entertainment", "Diablo IV", "Logs"),
				la("Blizzard Entertainment", "World of Warcraft", "Cache"),
				la("Blizzard Entertainment", "World of Warcraft", "Logs"),
				la("Blizzard Entertainment", "Hearthstone", "Cache"),
				la("Blizzard Entertainment", "Hearthstone", "Logs"),
				la("Blizzard Entertainment", "Call of Duty", "Cache"),
				la("Blizzard Entertainment", "Call of Duty", "Logs"),
			},
		},
		{
			ID: "game_crash_dumps", Name: "游戏崩溃转储",
			Description: "游戏与应用崩溃时的内存转储文件（无价值，可安全删除）",
			Level:       LevelSafe,
			Paths: []string{
				la("CrashDumps"),
			},
		},
		{
			ID: "steam_cache", Name: "Steam 缓存与日志",
			Description: "Steam 客户端缓存与日志（游戏本体不受影响）",
			Level:       LevelModerate,
			Paths: []string{
				filepath.Join(roamingAppData(), "Steam", "logs"),
				filepath.Join(roamingAppData(), "Steam", "htmlcache"),
				filepath.Join(roamingAppData(), "Steam", "config", "htmlcache"),
				filepath.Join(roamingAppData(), "Steam", "SteamAppData", "shadercache"),
				// %LOCALAPPDATA%\Steam\htmlcache is the main cache dir on modern
				// installs (415MB on a typical gamer machine).
				la("Steam", "htmlcache"),
				la("Steam", "logs"),
				la("Steam", "config", "htmlcache"),
			},
		},
		{
			ID: "riot_cache", Name: "Riot 游戏缓存（LOL/瓦罗兰特）",
			Description: "拳头游戏客户端的日志/崩溃报告/HTTP 缓存（游戏本体与配置不受影响）",
			Level:       LevelModerate,
			Paths: []string{
				la("Riot Games", "Riot Client", "Logs"),
				la("Riot Games", "Riot Client", "Crashes"),
				la("Riot Games", "Riot Client", "HttpCache"),
				la("Riot Games", "League of Legends", "Logs"),
				la("Riot Games", "League of Legends", "Crashes"),
				la("Riot Games", "VALORANT", "Logs"),
				la("Riot Games", "VALORANT", "Crashes"),
				la("Riot Games", "VALORANT", "HttpCache"),
			},
		},
		{
			ID: "rockstar_cache", Name: "Rockstar 启动器缓存（GTA5）",
			Description: "Rockstar 启动器的崩溃日志与缓存（游戏本体与存档不受影响）",
			Level:       LevelModerate,
			Paths: []string{
				la("Rockstar Games", "Launcher", "CrashLogs"),
				la("Rockstar Games", "Launcher", "Logs"),
				la("Rockstar Games", "Launcher", "Cache"),
				la("Rockstar Games", "Launcher", "HttpCache"),
				la("Rockstar Games", "GTAV Enhanced", "CrashLogs"),
			},
		},
		{
			ID: "epic_cache", Name: "Epic 游戏缓存",
			Description: "Epic 启动器缓存与日志（游戏本体不受影响）",
			Level:       LevelModerate,
			Paths: []string{
				filepath.Join(roamingAppData(), "Epic", "EpicGamesLauncher", "Logs"),
				la("Epic GamesLauncher", "Saved", "Logs"),
			},
		},
		{
			ID: "tencent_cache", Name: "腾讯系应用缓存",
			Description: "Roaming\\Tencent 下的 QQ/微信等客户端缓存",
			Level:       LevelCautious, Paths: []string{filepath.Join(roamingAppData(), "Tencent")},
		},
		{
			ID: "code_cache", Name: "VS Code 缓存",
			Description: "VS Code 的缓存与日志（重新打开时重建）",
			Level:       LevelModerate,
			Paths: []string{
				filepath.Join(roamingAppData(), "Code", "Cache"),
				filepath.Join(roamingAppData(), "Code", "CachedData"),
				filepath.Join(roamingAppData(), "Code", "logs"),
				filepath.Join(roamingAppData(), "Code", "Code Cache"),
			},
		},
		{
			ID: "vss_shadows", Name: "系统还原点（卷影副本）",
			Description: "删除所有盘的系统还原点（不可恢复，无法移入回收站）",
			Level:         LevelCautious,
			Paths:         []string{`C:\System Volume Information`},
			RequiresAdmin: true,
		},
		{
			ID: "win_upgrade_residue", Name: "Windows 升级残留",
			Description: "$WINDOWS.~BT / $Windows.~WS / ESD 升级残留文件（删除不可恢复）",
			Level:       LevelCautious,
			Paths: []string{
				`C:\$WINDOWS.~BT`,
				`C:\$Windows.~WS`,
				`C:\ESD`,
			},
			RequiresAdmin: true,
		},
		{
			// ---- Probe-only targets -------------------------------------
			// These three hold a lot of the C: drive, but none of them may be
			// deleted by a cleanup tool: they are serviced by DISM / Windows
			// itself, and removing them by hand breaks Windows Update,
			// uninstall/repair, and Store apps. They exist so the user can see
			// where the space went; ExecuteClean refuses them outright.
			ID: "winsxs", Name: "Windows 组件存储（WinSxS）",
			Description: "系统组件的旧版本备份，供更新回滚和功能安装使用。只能用 DISM 清理，手动删除会导致系统无法更新或修复",
			Level:       LevelCautious,
			Paths:       []string{win("WinSxS")},
			InfoOnly:    true,
			InfoNote: "回收方式：以管理员运行 DISM /Online /Cleanup-Image /StartComponentCleanup（加 /ResetBase 可再压缩，但会失去回滚旧更新的能力）。" +
				"注意该目录与 System32 共享硬链接，显示的体积会大于实际新增占用",
		},
		{
			ID: "windows_installer", Name: "Windows 安装缓存（MSI）",
			Description: "已安装程序的 MSI/MSP 安装包备份，用于修复和卸载。删除后对应的软件将无法卸载或修复",
			Level:       LevelCautious,
			Paths:       []string{win("Installer")},
			InfoOnly:    true,
			InfoNote: "回收方式：不要手动删除。先卸载不用的软件，再用 DISM /Online /Cleanup-Image /StartComponentCleanup 或专门的孤立补丁清理工具处理残留",
		},
		{
			ID: "windows_apps", Name: "应用商店应用（WindowsApps）",
			Description: "从 Microsoft Store 安装的应用本体，体积随已安装应用数量增长",
			Level:       LevelCautious,
			Paths:       []string{filepath.Join(programFiles(), "WindowsApps")},
			InfoOnly:    true,
			InfoNote:    "回收方式：在「设置 → 应用 → 已安装的应用」里卸载不用的商店应用；部分应用可在设置中迁移到其他磁盘",
		},
	})
}

// expandDefs folds each rule's PathGlobs into its Paths so that every rule
// leaves cleanItemDefs with a concrete, non-empty target list. Rules whose
// globs matched nothing are dropped rather than returned with an empty Paths
// list, which is what CleanupItems would do anyway.
func expandDefs(defs []CleanItem) []CleanItem {
	out := make([]CleanItem, 0, len(defs))
	for _, d := range defs {
		if len(d.PathGlobs) > 0 {
			d.Paths = append(d.Paths, expandPathGlobs(d.PathGlobs)...)
			if len(d.Paths) == 0 {
				continue
			}
		}
		out = append(out, d)
	}
	return out
}

// roamingAppData returns the %APPDATA% directory.
func roamingAppData() string {
	if p := os.Getenv("APPDATA"); p != "" {
		return p
	}
	return filepath.Join(homeDir(), "AppData", "Roaming")
}

// recycleBinPaths returns the $Recycle.Bin path of every present fixed drive.
func (a *App) recycleBinPaths() []string {
	paths := []string{}
	for _, d := range a.GetDrives() {
		paths = append(paths, d.Drive+`\$Recycle.Bin`)
	}
	return paths
}

// CleanupItems probes every cleanup rule and returns the ones that exist on
// this machine, with current sizes. Probing runs in parallel.
func (a *App) CleanupItems() []CleanItem {
	defs := a.cleanItemDefs()
	var wg sync.WaitGroup
	for i := range defs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			a.probeItem(&defs[idx])
		}(i)
	}
	wg.Wait()

	out := defs[:0]
	for _, d := range defs {
		if d.Exists {
			out = append(out, d)
		}
	}
	return out
}

// probeItem accumulates size/count for every existing path of a rule.
// Sizes are always computed live (never from the scan snapshot) so the
// cleanup center always reflects the current disk state, even if the
// snapshot is stale or the scan happened long ago.
func (a *App) probeItem(item *CleanItem) {
	// Glob-based rules (names that change between releases) expand to the
	// concrete directories that exist right now, then share the normal path
	// probing below.
	a.probeGlobPaths(item)

	// System-level items whose primary path is unreadable by design
	// (permission-denied), so os.Stat would hide them. Probe them specially.
	switch item.ID {
	case "vss_shadows":
		a.probeVSS(item)
		return
	case "win_upgrade_residue":
		item.Exists = false
		for _, p := range item.Paths {
			if _, err := os.Stat(p); err != nil {
				continue // directory not present (or hidden); nothing to clean
			}
			item.Exists = true
			if item.Drive == "" && len(p) > 1 {
				item.Drive = strings.ToUpper(p[:1])
			}
			// Sizes come from a best-effort walk; permission errors are ignored.
			s, fc := walkDirSize(p)
			item.Size += s
			item.FileCount += fc
		}
		return
	case "blizzard_game_cache":
		// The real targets are discovered at runtime (numbered event/patch
		// folders), so store the resolved list on the item: the cleanup then
		// operates on exactly the set the UI measured.
		item.Paths = blizzardGameCachePaths()
		item.Exists = len(item.Paths) > 0
		for _, p := range item.Paths {
			s, fc := walkDirSize(p)
			item.Size += s
			item.FileCount += fc
			if item.Drive == "" && len(p) > 1 {
				item.Drive = strings.ToUpper(p[:1])
			}
		}
		return
	}

	// Probe-only targets (WinSxS / Windows\Installer / WindowsApps): measured
	// so the user can see the space, but never cleanable.
	if item.InfoOnly {
		a.probeInfoOnly(item)
		return
	}

	for _, p := range item.Paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		item.Exists = true
		if item.Drive == "" && len(p) > 1 {
			item.Drive = strings.ToUpper(p[:1])
		}
		if info.IsDir() {
			s, fc := walkDirSize(p)
			item.Size += s
			item.FileCount += fc
		} else {
			item.Size += info.Size()
			item.FileCount++
		}
	}
}

// probeInfoOnly measures a rule that DiskSweep will never delete (WinSxS,
// Windows\Installer, WindowsApps).
//
// Its whole purpose is to make the space visible and explain how to reclaim it,
// so a permission failure still reports the item instead of hiding it --
// otherwise the largest consumers on C: would silently vanish from the list,
// which is exactly the complaint that started this: "don't know what ate the
// disk". ExecuteClean refuses these items, so showing them is safe.
func (a *App) probeInfoOnly(item *CleanItem) {
	item.Exists = false
	for _, p := range item.Paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		item.Exists = true
		if item.Drive == "" && len(p) > 1 {
			item.Drive = strings.ToUpper(p[:1])
		}
		if !info.IsDir() {
			item.Size += info.Size()
			item.FileCount++
			continue
		}

		// WindowsApps gets a per-app breakdown in the same pass, so the walk
		// over ~11 GB / 58k files is not paid twice.
		if item.ID == "windows_apps" {
			children, appCount, size, files, err := windowsAppsBreakdown(p)
			if err == nil {
				item.Children = children
				item.ChildCount = appCount
				item.Size += size
				item.FileCount += files
				continue
			}
			// fall through to the plain walk if the directory cannot be listed
		}

		s, fc := walkDirSize(p)
		item.Size += s
		item.FileCount += fc
	}
	if item.Exists && item.FileCount == 0 {
		// Present but not enumerable (standard user on a locked-down box).
		// Report the item with an unmeasured size instead of a fake zero.
		item.InfoNote = "当前权限无法读取体积，以管理员运行可测量。" + item.InfoNote
	}
}

// windowsAppsDetailLimit is how many Store apps are listed individually. The
// rest are folded into one "other" row so the numbers still add up.
const windowsAppsDetailLimit = 15

// appxDisplayNameRe matches the package's own DisplayName, which is the first
// <DisplayName> element in AppxManifest.xml (under <Properties>).
var appxDisplayNameRe = regexp.MustCompile(`<DisplayName>([^<]*)</DisplayName>`)

// windowsAppsBreakdown groups the Store app directories by package identity and
// returns the largest ones, the total app count, and the aggregate size.
//
// A single app owns several directories: the payload plus one per resource
// variant ("_neutral_split.scale-400_", "_neutral_~_"). Summing per directory
// would scatter one app over many rows and understate each, so everything
// before the first "_" is treated as the identity -- that is the manifest's
// <Identity Name> by construction.
//
// Friendly names come from each package's AppxManifest.xml. Localised packages
// store an "ms-resource:..." token that resolves through resources.pri, which we
// cannot decode, so those fall back to the package identity.
func windowsAppsBreakdown(root string) (children []ItemChild, appCount int, total int64, files int, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	type acc struct {
		size    int64
		mainDir string // best directory to read AppxManifest.xml from
	}
	byID := map[string]*acc{}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		id := name
		if i := strings.IndexByte(name, '_'); i > 0 {
			id = name[:i]
		}
		s, fc := walkDirSize(filepath.Join(root, name))
		total += s
		files += fc

		acc1 := byID[id]
		if acc1 == nil {
			acc1 = &acc{}
			byID[id] = acc1
		}
		acc1.size += s
		if acc1.mainDir == "" || preferManifestDir(name, acc1.mainDir) {
			acc1.mainDir = name
		}
	}

	all := make([]ItemChild, 0, len(byID))
	for id, acc1 := range byID {
		all = append(all, ItemChild{
			Name: appDisplayName(filepath.Join(root, acc1.mainDir), id),
			ID:   id,
			Size: acc1.size,
		})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Size != all[j].Size {
			return all[i].Size > all[j].Size
		}
		return all[i].Name < all[j].Name // stable output for tests
	})

	appCount = len(all)
	if len(all) > windowsAppsDetailLimit {
		rest := all[windowsAppsDetailLimit:]
		var restSize int64
		for _, c := range rest {
			restSize += c.Size
		}
		all = append(all[:windowsAppsDetailLimit:windowsAppsDetailLimit], ItemChild{
			Name: fmt.Sprintf("其他 %d 个应用", len(rest)),
			Size: restSize,
		})
	}
	return all, appCount, total, files, nil
}

// isResourceVariantDir reports whether a WindowsApps directory holds only
// resources (scale/theme splits) rather than a real payload. Those have no
// usable AppxManifest.xml.
func isResourceVariantDir(name string) bool {
	return strings.Contains(name, "split.scale") || strings.Contains(name, "_~_")
}

// preferManifestDir picks which of an app's directories to read the manifest
// from, favouring a real payload directory over a resource-only variant.
func preferManifestDir(candidate, current string) bool {
	cur, can := isResourceVariantDir(current), isResourceVariantDir(candidate)
	if cur == can {
		return candidate < current // deterministic pick
	}
	return !can
}

// appDisplayName reads the package's display name, falling back to its identity.
func appDisplayName(dir, fallback string) string {
	b, err := os.ReadFile(filepath.Join(dir, "AppxManifest.xml"))
	if err != nil {
		return fallback
	}
	m := appxDisplayNameRe.FindSubmatch(b)
	if m == nil {
		return fallback
	}
	name := strings.TrimSpace(string(m[1]))
	if name == "" || strings.HasPrefix(name, "ms-resource:") {
		return fallback
	}
	return name
}

// probeGlobPaths resolves a rule's PathGlobs and folds the results into
// item.Paths, so that the paths the UI measured are exactly the paths the
// cleanup will act on (see resolveItemPaths).
//
// Already-expanded paths are skipped, which makes this idempotent: cleanItemDefs
// expands globs up front (so a rule always carries a concrete Paths list), and
// probing then only has to measure them.
func (a *App) probeGlobPaths(item *CleanItem) {
	if len(item.PathGlobs) == 0 {
		return
	}
	have := make(map[string]bool, len(item.Paths))
	for _, p := range item.Paths {
		have[p] = true
	}
	found := []string{}
	for _, p := range expandPathGlobs(item.PathGlobs) {
		if !have[p] {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		// Nothing new to add, but the paths we already carry still count as
		// existing so the item is not filtered out by CleanupItems.
		if len(item.Paths) > 0 {
			item.Exists = true
		}
		return
	}
	item.Exists = true
	item.Paths = append(item.Paths, found...)
	for _, p := range found {
		s, fc := walkDirSize(p)
		item.Size += s
		item.FileCount += fc
		if item.Drive == "" && len(p) > 1 {
			item.Drive = strings.ToUpper(p[:1])
		}
	}
}

// probeVSS fills a vss_shadows item. The System Volume Information directory
// is unreadable without SYSTEM privileges, so we rely on vssadmin (admin) to
// report the storage usage; without admin we still show the item with 0 size
// and let the RequiresAdmin badge explain it.
func (a *App) probeVSS(item *CleanItem) {
	item.Exists = false
	if _, err := os.Stat(`C:\System Volume Information`); err != nil {
		return // no system restore volume at all
	}
	item.Exists = true
	item.Drive = "C"
	item.FileCount = 1
	// Try vssadmin for a real size (needs elevation). Non-fatal.
	if out, err := exec.Command("vssadmin", "list", "shadowstorage").CombinedOutput(); err == nil {
		item.Size = parseVSSUsedBytes(string(out))
	}
}

// parseVSSUsedBytes extracts the used space from `vssadmin list shadowstorage`
// output. The output is localized, so we match the "used" line and read the
// byte count in its parentheses, e.g.
//   zh: "已使用的空间: 5.10 GB (5476081664 字节)"
//   en: "Used Space: 1.50 GB (1610612736 bytes)"
// Only the used line is counted (max-space lines carry their own numbers).
func parseVSSUsedBytes(out string) int64 {
	lines := strings.Split(out, "\n")
	var total int64
	for _, ln := range lines {
		if !strings.Contains(ln, "(") {
			continue
		}
		if strings.Contains(ln, "已使用的空间") || strings.Contains(ln, "Used Space") {
			re := regexp.MustCompile(`\((\d+)\s*(?:字节|bytes)\)`)
			if m := re.FindStringSubmatch(ln); len(m) == 2 {
				n, _ := strconv.ParseInt(m[1], 10, 64)
				total += n
			}
		}
	}
	return total
}

// dirSizeCached returns a directory's size from the scan cache, falling back
// to a live walk when the cache has no entry.
func (a *App) dirSizeCached(p string) (int64, int) {
	if v, ok := a.scans.sizeMap.Load(p); ok {
		di := v.(dirInfo)
		return di.size, di.fileCount
	}
	return walkDirSize(p)
}

// walkDirSize recursively computes the size and file count of a directory
// using bounded concurrency. The semaphore is acquired inside the spawned
// goroutine so the dispatch path never blocks (avoids semaphore deadlock).
// walkDirSize returns the total size and file count of p, recursively.
//
// Concurrency follows the same rule as walkAndScan: a semaphore bounds how many
// workers are inside the traversal, and the slot is taken BEFORE a goroutine is
// spawned. The previous version spawned a goroutine for every subdirectory and
// only then blocked on the semaphore, so a wide directory (WinSxS has ~19k
// entries) created thousands of goroutines that did nothing but wait — with
// dozens of rules probing in parallel that is a large, pointless stack and
// scheduler cost. When the pool is saturated a child is now walked inline on the
// current goroutine, which keeps progress without piling up.
func walkDirSize(p string) (int64, int) {
	workers := runtime.NumCPU() * 2
	if workers > 32 {
		workers = 32
	}
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var size int64
	var files int

	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		subdirs := make([]string, 0, 8)
		var localSize int64
		var localFiles int
		for _, e := range entries {
			if e.IsDir() {
				subdirs = append(subdirs, filepath.Join(dir, e.Name()))
				continue
			}
			if info, err := e.Info(); err == nil {
				localSize += info.Size()
				localFiles++
			}
		}
		if localFiles > 0 {
			mu.Lock()
			size += localSize
			files += localFiles
			mu.Unlock()
		}

		for _, sd := range subdirs {
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(child string) {
					defer wg.Done()
					defer func() { <-sem }()
					walk(child)
				}(sd)
			default:
				walk(sd) // inline: pool is full
			}
		}
	}
	walk(p)
	wg.Wait()
	return size, files
}

func programData() string {
	if p := os.Getenv("ProgramData"); p != "" {
		return p
	}
	return `C:\ProgramData`
}

// programFiles returns %ProgramFiles%. Kept separate from programData because
// the Store app directory lives under Program Files, not ProgramData.
func programFiles() string {
	if p := os.Getenv("ProgramFiles"); p != "" {
		return p
	}
	if p := os.Getenv("ProgramW6432"); p != "" {
		return p
	}
	return `C:\Program Files`
}

// blizzardGames are the Blizzard titles whose %LOCALAPPDATA% folder is probed
// for disposable event/patch caches.
var blizzardGames = []string{
	"Overwatch", "Diablo IV", "World of Warcraft", "Hearthstone",
	"Call of Duty", "StarCraft II",
}

// blizzardGameCachePaths returns the per-game directories that are safe to
// remove: the numbered event/patch folders (Blizzard adds one per content
// update, and they accumulate forever) plus the classic Cache/Logs/Errors/
// Saved subdirs. The game root and its settings are never included.
//
// Probing and cleaning share this function on purpose: the size shown in the
// UI is the size of exactly this set, so cleaning it can never leave a large
// number behind with nothing to show for it.
func blizzardGameCachePaths() []string {
	out := []string{}
	for _, g := range blizzardGames {
		base := filepath.Join(localAppData(), "Blizzard Entertainment", g)
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Numbered folder = event/patch cache; also known cache dirs.
			if isNumericDirName(e.Name()) || isBlizzardDisposableDir(e.Name()) {
				out = append(out, filepath.Join(base, e.Name()))
			}
		}
	}
	return out
}

// isNumericDirName reports whether name consists only of digits — Blizzard
// games use numbered folders for per-patch/per-event data (e.g. Overwatch's
// "592095225" event cache).
func isNumericDirName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isBlizzardDisposableDir reports whether a Blizzard game subdirectory is a
// disposable cache/log folder (safe to clean; re-downloads/rebuilds).
func isBlizzardDisposableDir(name string) bool {
	switch strings.ToLower(name) {
	case "cache", "logs", "errors", "saved", "telemetry", "crash":
		return true
	}
	return false
}
