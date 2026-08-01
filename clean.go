package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Clean levels.
const (
	LevelSafe     = "safe"     // safe to delete, no rebuild cost
	LevelModerate = "moderate" // rebuilds on next use, may be slow
	LevelCautious = "cautious" // deleting forces re-download or re-login
)

// CleanItem is one rule-driven cleanup target.
type CleanItem struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Level         string   `json:"level"`
	Paths         []string `json:"paths"`                // candidate paths (probed for existence)
	Match         string   `json:"match,omitempty"`      // "" | "glob:<pattern>" | "ext:<ext>" | "name:<name>"
	MaxAgeDays    int      `json:"maxAgeDays,omitempty"` // >0: only entries older than N days
	RequiresAdmin bool     `json:"requiresAdmin"`
	Exists        bool     `json:"exists"`
	Size          int64    `json:"size"`
	FileCount     int      `json:"fileCount"`
	Drive         string   `json:"drive"` // primary drive of the first existing path; "ALL" for multi-drive items
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
func (a *App) cleanItemDefs() []CleanItem {
	home := homeDir()
	local := localAppData()
	la := func(rel ...string) string { return filepath.Join(append([]string{local}, rel...)...) }
	hp := func(rel ...string) string { return filepath.Join(append([]string{home}, rel...)...) }
	win := func(rel ...string) string { return filepath.Join(append([]string{systemRoot()}, rel...)...) }

	return []CleanItem{
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
			Level:       LevelModerate, Paths: []string{la("npm-cache"), hp(".npm")},
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
			ID: "windows_old", Name: "Windows.old",
			Description: "旧系统备份目录（删除不可恢复）",
			Level:       LevelCautious, Paths: []string{`C:\Windows.old`}, RequiresAdmin: true,
		},
	}
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
func walkDirSize(p string) (int64, int) {
	sem := make(chan struct{}, runtime.NumCPU()*2)
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
		for _, e := range entries {
			if e.IsDir() {
				sub := filepath.Join(dir, e.Name())
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					walk(sub)
				}()
				continue
			}
			if info, err := e.Info(); err == nil {
				mu.Lock()
				size += info.Size()
				files++
				mu.Unlock()
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
