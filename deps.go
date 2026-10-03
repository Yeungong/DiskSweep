package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DepCleanable describes whether a dependency may be cleaned.
const (
	DepKeep    = "keep"    // never delete - system/software depends on it
	DepPartial = "partial" // some parts (cache) are cleanable, core is not
	DepModel   = "model"   // local AI model weights - delete = re-download GBs
)

// DepInfo describes one recognized dependency on this machine.
type DepInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`       // Chinese display name
	Category   string `json:"category"`   // 系统运行时 / 开发工具链 / 本地大模型 / 开源软件
	Desc       string `json:"desc"`       // Chinese comment shown to the user
	Cleanable  string `json:"cleanable"`  // DepKeep | DepPartial | DepModel
	CleanNote  string `json:"cleanNote"`  // Chinese cleanup advice
	Paths      []string `json:"paths"`    // candidate paths (probed for existence)
	Exists     bool   `json:"exists"`
	Size       int64  `json:"size"`
	FileCount  int    `json:"fileCount"`
	Drive      string `json:"drive"`
	// DirMatch matches a directory NAME (filepath.Base) regardless of location,
	// e.g. "llama.cpp" catches D:\llama.cpp and E:\anything\llama.cpp.
	DirMatch string `json:"-"`
	// FileMatch matches a file name for top-level detection (e.g. "*.gguf").
	FileMatch string `json:"-"`
}

func (d *DepInfo) markExists(drive string) {
	d.Exists = true
	if d.Drive == "" {
		d.Drive = drive
	}
}

// depDefs returns the built-in dependency knowledge base.
// Paths use environment-relative helpers so rules work across machines;
// DirMatch/FileMatch allow locating dependencies that live outside the
// standard install locations (e.g. a llama.cpp copy on D:).
func (a *App) depDefs() []DepInfo {
	home := homeDir()
	la := func(rel ...string) string {
		return filepath.Join(append([]string{localAppData()}, rel...)...)
	}
	prog := func(rel ...string) string {
		p := filepath.Join(append([]string{`C:\Program Files`}, rel...)...)
		return p
	}
	progX86 := func(rel ...string) string {
		p := filepath.Join(append([]string{`C:\Program Files (x86)`}, rel...)...)
		return p
	}
	winSys := func(rel ...string) string {
		p := filepath.Join(append([]string{systemRoot(), "System32"}, rel...)...)
		return p
	}
	hp := func(rel ...string) string { return filepath.Join(append([]string{home}, rel...)...) }

	return []DepInfo{
		// ---------- 系统运行时 ----------
		{
			ID: "dotnet_runtime", Name: ".NET 运行时", Category: "系统运行时",
			Desc:      "运行 .NET 应用（C#/WPF/WinForms 软件）必需的运行时，包含 6.0/8.0 等版本。删除后所有 .NET 软件无法启动",
			Cleanable: DepKeep, CleanNote: "请勿删除；旧版本可通过「磁盘清理」的 .NET 清理功能移除",
			Paths:     []string{prog("dotnet", "shared"), prog("dotnet", "host")},
			DirMatch:  "dotnet",
		},
		{
			ID: "dotnet_sdk", Name: ".NET SDK", Category: "系统运行时",
			Desc:      "用于编译 .NET 项目的开发工具包（dotnet build 等）。如果只运行软件不需要它",
			Cleanable: DepPartial, CleanNote: "只保留 shared 运行时即可，SDK 可卸载（需用官方卸载工具）",
			Paths:     []string{prog("dotnet", "sdk")},
			DirMatch:  "sdk",
		},
		{
			ID: "jdk", Name: "Java JDK / JRE", Category: "系统运行时",
			Desc:      "运行 Java 程序（Minecraft、IDEA、Tomcat 等）必需。版本 8/11/17/21 各对应不同软件",
			Cleanable: DepKeep, CleanNote: "请勿删除；不需要的版本可用官方卸载器移除",
			Paths:     []string{prog("Java")},
			DirMatch:  "jdk",
		},
		{
			ID: "vcpp_runtime", Name: "VC++ 运行库", Category: "系统运行时",
			Desc:      "绝大多数 Windows 软件（游戏、办公、开发工具）依赖的 C++ 运行库。System32 里的 vcruntime/msvcp dll",
			Cleanable: DepKeep, CleanNote: "请勿删除；缺少会导致软件报错 0xc000007b",
			Paths:     []string{winSys("vcruntime140.dll")},
			FileMatch: "vcruntime*.dll",
		},
		{
			ID: "directx", Name: "DirectX 运行库", Category: "系统运行时",
			Desc:      "游戏与图形软件依赖的 DirectX 组件（d3dcompiler 等）",
			Cleanable: DepKeep, CleanNote: "请勿删除",
			Paths:     []string{winSys("d3dcompiler_47.dll")},
			FileMatch: "d3dcompiler*.dll",
		},
		{
			ID: "webview2", Name: "WebView2 运行时", Category: "系统运行时",
			Desc:      "很多桌面应用（微信、Office、Wails 应用等）内嵌网页界面依赖的运行时",
			Cleanable: DepKeep, CleanNote: "请勿删除",
			Paths:     []string{progX86("Microsoft", "EdgeWebView"), prog("Microsoft", "EdgeWebView")},
		},
		{
			ID: "nodejs", Name: "Node.js", Category: "系统运行时",
			Desc:      "运行 JavaScript 开发工具链（npm 包、Vite、前端构建）的运行时",
			Cleanable: DepKeep, CleanNote: "请勿删除；npm 全局缓存可用清理中心清",
			Paths:     []string{prog("nodejs")},
			DirMatch:  "node",
		},
		{
			ID: "python", Name: "Python", Category: "系统运行时",
			Desc:      "Python 解释器（脚本、AI 工具、自动化依赖）。版本多可能装多个",
			Cleanable: DepKeep, CleanNote: "请勿删除；pip 缓存可用清理中心清",
			Paths:     []string{prog("Python313"), prog("Python311"), la("Programs", "Python")},
			DirMatch:  "python",
		},
		{
			ID: "go", Name: "Go 工具链", Category: "系统运行时",
			Desc:      "Go 语言编译工具（构建 Go 项目必需）",
			Cleanable: DepKeep, CleanNote: "请勿删除；GOPATH 模块缓存可清理",
			Paths:     []string{prog("Go")},
		},
		{
			ID: "git", Name: "Git", Category: "系统运行时",
			Desc:      "版本控制工具，很多开发/开源软件依赖它拉取代码",
			Cleanable: DepKeep, CleanNote: "请勿删除",
			Paths:     []string{prog("Git")},
		},

		// ---------- 开发工具链 / 包缓存 ----------
		{
			ID: "go_mod", Name: "Go 模块缓存", Category: "开发工具链",
			Desc:      "go build 下载的依赖包缓存，重新编译时重新下载",
			Cleanable: DepPartial, CleanNote: "可用清理中心「Go 模块缓存」清理",
			Paths:     []string{hp("go", "pkg", "mod")},
		},
		{
			ID: "npm_global", Name: "npm 全局包", Category: "开发工具链",
			Desc:      "npm install -g 安装的全局命令行工具",
			Cleanable: DepPartial, CleanNote: "删了需要重新 npm install -g",
			Paths:     []string{hp("AppData", "Roaming", "npm")},
		},
		{
			ID: "uv_cache", Name: "uv/Python 包缓存", Category: "开发工具链",
			Desc:      "Python uv/pip 下载的包缓存",
			Cleanable: DepPartial, CleanNote: "可用清理中心「uv 包缓存」清理",
			Paths:     []string{la("uv"), la("pip", "Cache")},
		},
		{
			ID: "rust", Name: "Rust 工具链", Category: "开发工具链",
			Desc:      "Rust 编译工具与 cargo 包缓存",
			Cleanable: DepPartial, CleanNote: "cargo 缓存可清，工具链请保留",
			Paths:     []string{hp(".cargo")},
			DirMatch:  ".cargo",
		},

		// ---------- 本地大模型 ----------
		{
			ID: "ollama", Name: "Ollama 模型", Category: "本地大模型",
			Desc:      "Ollama 下载的本地大模型权重（每个几 GB~几十 GB）",
			Cleanable: DepModel, CleanNote: "删除后需重新下载模型（数十 GB）；不用的模型在 Ollama 里 ollama rm 更安全",
			Paths:     []string{hp(".ollama"), hp(".local", "share", "ollama")},
			DirMatch:  ".ollama",
		},
		{
			ID: "lmstudio", Name: "LM Studio 模型", Category: "本地大模型",
			Desc:      "LM Studio 图形界面下载的本地模型",
			Cleanable: DepModel, CleanNote: "删除后需重新下载；可在 LM Studio 内管理模型",
			Paths:     []string{hp(".lmstudio")},
			DirMatch:  ".lmstudio",
		},
		{
			ID: "llamacpp", Name: "llama.cpp 部署", Category: "本地大模型",
			Desc:      "llama.cpp 本地推理部署（可含 model 子目录的 GGUF 模型，每个 1~30GB）",
			Cleanable: DepModel, CleanNote: "model 目录是模型权重，删除后需重新下载；程序本体保留",
			DirMatch:  "llama.cpp",
		},
		{
			ID: "gguf_model", Name: "GGUF 模型文件", Category: "本地大模型",
			Desc:      "GGUF 格式本地大模型权重文件（Qwen/DeepSeek/Llama 等，单个可达数十 GB）",
			Cleanable: DepModel, CleanNote: "删除 = 重新下载；确定不用再删",
			FileMatch: "*.gguf",
		},
		{
			ID: "hf_cache", Name: "HuggingFace 模型缓存", Category: "本地大模型",
			Desc:      "HuggingFace 下载的模型/数据集缓存",
			Cleanable: DepPartial, CleanNote: "可用清理中心「HuggingFace 模型缓存」清理",
			Paths:     []string{hp(".cache", "huggingface")},
		},

		// ---------- 开源软件 / Python 项目依赖 ----------
		{
			ID: "python_venv", Name: "Python 虚拟环境", Category: "开源软件",
			Desc:      "Python 项目独立依赖环境（venv/.venv），通常由开源脚本或 AI 工具创建",
			Cleanable: DepPartial, CleanNote: "删除后该项目需重新 pip install 依赖",
			DirMatch:  "venv",
		},
		{
			ID: "comfyui", Name: "ComfyUI / Stable Diffusion", Category: "开源软件",
			Desc:      "AI 绘画工作流工具（含 models 目录的模型权重）",
			Cleanable: DepModel, CleanNote: "models 是模型权重，删除后需重新下载",
			DirMatch:  "ComfyUI",
			FileMatch: "*.safetensors",
		},
		{
			ID: "node_modules", Name: "node_modules 依赖", Category: "开源软件",
			Desc:      "npm 项目依赖目录（开源软件/前端项目），每个项目一份",
			Cleanable: DepPartial, CleanNote: "删除后需 npm install 重新安装",
			DirMatch:  "node_modules",
		},
	}
}

// Dependencies probes the knowledge base and returns deps present on this
// machine, with real sizes. Missing paths are filtered out.
//
// The path probe runs in parallel, like CleanupItems does, because it is not
// cheap: model directories (.ollama, .lmstudio, ComfyUI) are routinely tens of
// gigabytes, and this call is synchronous from the UI. Walking them one after
// another left the window frozen for as long as the whole set took.
func (a *App) Dependencies() []DepInfo {
	defs := a.depDefs()
	// First pass: probe explicit paths (one goroutine per rule, each writing
	// only its own index, so no locking is needed).
	var wg sync.WaitGroup
	for i := range defs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			d := &defs[idx]
			for _, p := range d.Paths {
				info, err := os.Stat(p)
				if err != nil {
					continue
				}
				drive := "?"
				if len(p) > 1 {
					drive = strings.ToUpper(p[:1])
				}
				d.markExists(drive)
				if info.IsDir() {
					s, fc := walkDirSize(p)
					d.Size += s
					d.FileCount += fc
				} else {
					d.Size += info.Size()
					d.FileCount++
				}
			}
		}(i)
	}
	wg.Wait()
	// Second pass: locate by directory name across all fixed drives (for
	// deps installed in non-standard locations, e.g. D:\llama.cpp).
	drives := a.GetDrives()
	for i := range defs {
		if defs[i].Exists {
			continue
		}
		if defs[i].DirMatch == "" {
			continue
		}
		for _, d := range drives {
			root := d.Drive + `\`
			if found, size, fc := findDirByName(root, defs[i].DirMatch); found {
				defs[i].markExists(d.Drive[:1])
				defs[i].Size = size
				defs[i].FileCount = fc
				break
			}
		}
	}
	out := defs[:0]
	for _, d := range defs {
		if d.Exists {
			out = append(out, d)
		}
	}
	return out
}

// findDirByName searches top-level dirs of root for a directory whose name
// equals name (case-insensitive). Returns first match with its recursive size.
func findDirByName(root, name string) (bool, int64, int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), name) {
			p := filepath.Join(root, e.Name())
			s, fc := walkDirSize(p)
			return true, s, fc
		}
	}
	return false, 0, 0
}

// matchDepByPath finds the first dependency whose rule matches path.
// Used by the scan/analyze views to annotate directories with Chinese notes.
// Priority: explicit path prefix > FileMatch (file-level, most specific) >
// DirMatch (any segment).
func (a *App) matchDepByPath(path string) *DepInfo {
	lp := strings.ToLower(path)
	var dirMatch *DepInfo
	for i := range a.depCache {
		d := &a.depCache[i]
		// Pass 1: exact path prefix (strongest, e.g. dotnet\sdk beats dotnet).
		for _, p := range d.Paths {
			pp := strings.ToLower(p)
			if lp == pp || strings.HasPrefix(lp, pp+`\`) {
				return d
			}
		}
		// Pass 2: file-level match (e.g. *.gguf) beats directory match.
		if d.FileMatch != "" {
			base := strings.ToLower(filepath.Base(lp))
			if ok, _ := filepath.Match(strings.ToLower(d.FileMatch), base); ok {
				return d
			}
		}
		// Pass 3: any path segment equals the dir name (case-insensitive).
		// Leading dots are stripped so ".venv" matches "venv".
		if d.DirMatch != "" {
			want := strings.ToLower(strings.TrimPrefix(d.DirMatch, "."))
			for _, seg := range strings.Split(lp, `\`) {
				segNorm := strings.ToLower(strings.TrimPrefix(seg, "."))
				if segNorm == want {
					dirMatch = d
					break
				}
			}
		}
	}
	return dirMatch
}
