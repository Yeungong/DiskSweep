# DiskSweep — 磁盘空间管家 / Disk Space Sweeper

[中文 · English] — Windows 磁盘空间分析与清理工具 · A disk space analyzer & cleaner for Windows.

> ⚠️ **安全第一 / Safety first**：所有清理操作默认**移入回收站**（可恢复），绝不直接永久删除；数据/配置类项目默认不勾选。
> All cleanups move files to the **Recycle Bin** (reversible) by default; data/config entries are unchecked by default.

---

## 1. 项目解决什么问题 / What it solves

**中文**：C 盘空间不明去向、AI 工具悄悄堆了几 GB 缓存、系统垃圾清理工具又慢又扫不全、还怕误删重要数据。

- **扫不全** → 并发扫描引擎全盘分析，目录下钻 + 大文件 Top，空间去向一目了然
- **清不干净** → 63 条规则化清理项自动探测（含 35+ 条 AI 工具），占用/权限错误重试并逐项报告
- **怕误删** → 所有清理移入回收站 + 完整历史记录 + 一键恢复，删错了也能捞回来
- **扫不到** → 无权限目录（系统还原点、升级残留等）明确标记"⚠ 无权限"，不再静默显示 0，提示提权后重扫

**English**: C: drive mysteriously full, AI tools silently piling up gigabytes of cache, system cleaners that are slow and miss most junk — and the fear of deleting important data.

- **Incomplete scans** → a concurrent scan engine analyzes the whole disk; drill down directories + top-file list show exactly where space went
- **Incomplete cleaning** → 63 rule-based cleanup entries auto-detected (35+ AI tools), retries on locked/permission errors and reports each failure
- **Deletion anxiety** → every cleanup goes to the Recycle Bin, a full history with one-click restore, so nothing is truly lost
- **Invisible usage** → permission-denied dirs (system restore points, upgrade leftovers) are flagged "⚠ no access" instead of silently showing 0, with a hint to re-scan elevated

---

## 2. 主要功能 / Features

### 空间分析 / Space Analysis

| 功能 / Feature | 说明 / Description |
|---|---|
| 全盘并发扫描 / Concurrent full-disk scan | worker pool 遍历；真盘实测 726MB / 4600+ 文件 ≈ 0.3s。Real-world: 726MB / 4600+ files in ~0.3s |
| 目录树下钻 / Drill-down directory tree | 面包屑导航逐层查看目录占用，按大小排序。Breadcrumb navigation, size-sorted |
| 大文件 Top 200 / Top-200 files | 全盘最大文件列表，可定位到资源管理器 / 移入回收站。Locate in Explorer / send to Recycle Bin |
| 实时进度 + 取消 / Live progress & cancel | 目录数 / 百分比 / 字节实时推送。Dirs / percent / bytes pushed live |
| 无权限目录标记 / Inaccessible-dir flagging | 权限拒绝的目录（系统还原点、升级残留等）标"⚠ 无权限"并计入扫描统计，不再静默显示 0。Permission-denied dirs are flagged and counted |
| SQLite 快照缓存 / SQLite snapshot cache | 重启秒级恢复上次扫描结果（可换盘存放）。Instant restore on relaunch (relocatable) |

### 清理中心 / Cleanup Center

| 功能 / Feature | 说明 / Description |
|---|---|
| 63 条规则化清理项 / 63 rule-based entries | 自动探测**实际存在**的路径，不存在的不显示。Only paths that actually exist are shown |
| 三级分级 / 3 safety levels | 🟢 安全（临时文件/缓存）· 🟡 中等（重建类）· 🔴 谨慎（数据/登录态，默认不勾选） |
| **AI 工具专项 / AI-tool coverage** | Codex、Claude Code、Cline、OpenCode、Copilot、Cursor、Windsurf、Trae、Kimi、豆包、Coze、Qoder、Cherry Studio、Ollama、LM Studio 等 35+ 工具 |
| 删除执行器 / Delete executor | 全部**移入回收站**；占用/权限错误重试 3 次、自动清除只读、逐项报告失败原因 |
| UAC 提权 / Elevation | 系统目录（WER、Windows Update 等）一键以管理员身份重启 |
| 清理历史 + 恢复 / History & restore | 每次清理记录路径/大小/时间（保留 90 天），一键从回收站恢复到原位置 |

### 体验 / Experience

- **5 套独立主题**（顶栏即时切换、自动记住）：CRT 绿色终端 / 新拟态 / 玻璃拟态 / 赛博朋克 / 蒸汽波
- **首启引导教程**：6 步动画教程介绍全部功能
- **缓存位置可换盘**：扫描快照与历史可存到 D:/E: 等盘（⚙ 设置）
- **操作日志**：每次清理逐项记录成功/失败
- **右下角 GitHub 按钮**：一键打开项目仓库

---

## 3. 安装方法 / Installation

### 方式一：直接运行（推荐） / Option 1: Prebuilt binary (recommended)

1. 从 GitHub Releases 下载 `DiskSweep.exe`（约 15MB，单文件，无需安装）
2. 双击运行

**系统要求 / Requirements**：Windows 10/11 64 位 + WebView2 Runtime（Win11 自带；Win10 一般由 Edge 提供，缺失时软件会提示安装）

### 方式二：从源码构建 / Option 2: Build from source

**前置依赖 / Prerequisites**：

| 依赖 | 版本 |
|---|---|
| Go | ≥ 1.25 |
| Node.js | ≥ 18（仅前端构建） |
| Wails CLI | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` |

> 国内网络建议配置 Go 镜像：`go env -w GOPROXY=https://goproxy.cn,direct`
> CN users: `go env -w GOPROXY=https://goproxy.cn,direct`

```powershell
# 进入项目目录
cd DiskSweep

# 构建前端 + 打包 exe
wails build
# 产物：build\bin\DiskSweep.exe

# 开发模式（前端热重载）
wails dev
```

---

## 4. 使用方法 / Usage

**首次启动**会播放 6 步引导教程，之后不再自动弹出（⚙ 设置 → 重看引导教程）。

### 空间分析：找空间去向 / Analyze: find where space went

1. 顶部选择要分析的盘（默认 C:）→ 点 **「开始扫描」**
2. 扫描中实时显示进度，可随时 **「取消扫描」**
3. 扫描完成后：
   - **目录占用**面板：点目录名逐层下钻，面包屑可回退；双击目录下钻
   - **大文件 Top** 面板：全盘最大的文件，`⌕` 定位到资源管理器、`🗑` 移入回收站

### 清理中心：清垃圾 / Cleanup: remove junk

1. 切到 **「清理中心」** tab（自动探测本机实际存在的垃圾项，按盘分组折叠）
2. 勾选要清理的项 → 点 **「清理选中项」** → 确认弹窗（标注每项是「移入回收站」还是「⚠ 永久清空回收站」）
3. 执行结果逐项写入操作日志（成功释放量 / 失败原因）
4. ⚠️ 移入回收站**不释放空间**——确定不要了请去**清空回收站**

### 历史记录：反悔药 / History: undo

切到 **「历史记录」** tab → 每条记录显示时间、清理项、原路径、大小 → 点 **↩** 从回收站恢复到原位置（原位置已有同名文件或回收站已清空时会提示）。

### 其他 / Others

- **管理员权限**：非管理员时，需要提权的系统目录项显示「🔒 需管理员」，点提示条一键 UAC 重启
- **主题**：右上角「主题 ▾」切换 5 套主题
- **缓存换盘**：⚙ 设置 → 输入目录或「浏览…」→ 应用并切换（自动迁移现有数据）

---

## 5. 输入输出示例 / Input & Output Examples

DiskSweep 是 GUI 应用，后端方法同时暴露给前端（Wails bindings）。以下为真实 API 的输入输出示例，供开发者参考。

*DiskSweep is a GUI app; its backend methods are exposed to the frontend via Wails bindings. Real API I/O examples for developers:*

### 扫描 / Scan

```text
// 输入 / input
StartScan("C:\\", 200)          // root, topN
// 输出 / output
null                            // err（nil）

// 事件流 / events（wails EventsOn）
scan:progress → {"phase":"scan","dirsTotal":1462,"dirsDone":1000,"files":3210,"bytes":654000000}
scan:done     → {"ok":true,"cancelled":false,"root":"C:\\","dirs":1462,"files":4653,
                 "bytes":726927863,"errors":4,"durationMs":284}
```

### 目录下钻 / Drill down

```text
// 输入 / input
GetDirChildren("C:\\Users")
// 输出 / output
[
  {"name":"<用户名>","path":"C:\\Users\\<用户名>","isDir":true,"size":32768000000,"fileCount":482913,"modTime":0},
  {"name":"Public","path":"C:\\Users\\Public","isDir":true,"size":1234000,"fileCount":89,"modTime":0}
]
```

### 大文件 Top / Top files

```text
// 输入 / input
GetTopFiles()
// 输出 / output
[{"path":"C:\\Users\\<用户名>\\AppData\\Local\\Temp\\x\\big.iso","size":10208137,"modTime":1720000000}, ...]
```

### 清理项探测 / Probe cleanup items

```text
// 输入 / input
CleanupItems()
// 输出 / output（节选 / excerpt）
[
  {"id":"temp_user","name":"用户临时文件","level":"safe","paths":["C:\\Users\\<用户名>\\AppData\\Local\\Temp"],
   "exists":true,"size":726927863,"fileCount":4653,"requiresAdmin":false},
  {"id":"qoder_data","name":"Qoder 全家桶数据","level":"cautious",
   "paths":["C:\\Users\\<用户名>\\.qoderwork","..."],"exists":true,"size":874053720,"requiresAdmin":false}
]
```

### 执行清理 / Execute cleanup

```text
// 输入 / input
ExecuteClean(["temp_user","qoder_data"])
// 输出 / output
[
  {"id":"temp_user","name":"用户临时文件","ok":true,"freed":726927863,"errors":[]},
  {"id":"qoder_data","name":"Qoder 全家桶数据","ok":true,"freed":874053720,"errors":[]}
]
```

### 历史记录与恢复 / History & restore

```text
// 输入 / input
GetCleanHistory()
// 输出 / output
[{"id":42,"time":1760000000,"itemId":"qoder_data","itemName":"Qoder 全家桶数据",
  "path":"C:\\Users\\<用户名>\\.qoderwork","size":874053720,"ok":true,"restored":false}, ...]

// 输入 / input
RestoreHistory(42)
// 输出 / output
{"ok":true,"message":"已恢复到原位置"}
```

### 其他 / Others

```text
GetDrives()        → [{"drive":"C:","total":512000000000,"used":385000000000,"free":127000000000}]
IsAdmin()          → false
GetCacheLocation() → {"path":"C:\\Users\\<用户名>\\AppData\\Roaming\\DiskSweep\\cache.db","size":35636000,"custom":false}
SetCacheLocation("D:\\Cache") → {"path":"D:\\Cache\\cache.db","size":35636000,"custom":true}
```

---

## 6. 技术栈 / Tech Stack

| 层 / Layer | 技术 / Tech |
|---|---|
| 后端 / Backend | Go + [Wails v2](https://wails.io)（v2.13.0）+ `golang.org/x/sys` |
| 存储 / Storage | `modernc.org/sqlite`（纯 Go 免 cgo） |
| 前端 / Frontend | Vite + 原生 HTML/JS/CSS（无框架，免 Node 构建链需求） |
| 渲染 / Renderer | 系统 WebView2（Windows 10/11 自带） |
| 打包 / Packaging | `wails build` 单 exe（约 15MB） |

## 7. 构建与测试 / Build & Test

```powershell
go test ./...                    # 单元测试 / unit tests
go test -tags integration ./...  # 集成测试（真实盘扫描 + 清理闭环）/ integration tests
wails build                      # 打包 / build exe
```

测试覆盖：扫描聚合正确性、符号链接防循环、权限拒绝容错、批量回收站、历史记录往返、清理→重扫闭环、真实盘冒烟。

## 8. AI 工具覆盖清单 / AI Tool Coverage

编程助手 / Coding: Codex · Claude Code · Cline · OpenCode · Copilot · Cursor · Windsurf/Codeium · Trae · Continue · Zed
聊天客户端 / Chat: Kimi · 豆包 Doubao · Coze · Chatbox · Cherry Studio · LobeChat · NextChat · Open WebUI · Jan · AnythingLLM
Agent 与平台 / Agents & Platforms: Qoder（全家桶）· n8n · arkcli · OpenChatCut · CC Switch · AI CanvasPro · Dreamina · MimoCode
本地模型 / Local models: Ollama · LM Studio · HuggingFace cache

> 规则采用「存在性探测」：路径不存在时自动隐藏，因此**欢迎提交你所用工具的规则**（见贡献）。

## 9. 贡献 / Contributing

MIT 许可，欢迎 PR。**新增一个 AI 工具的清理规则**只需 5 分钟：

```go
// clean.go → cleanItemDefs() 里追加一条 / append an entry:
{
    ID: "my_ai_tool", Name: "MyAI 数据",
    Description: "MyAI 的本地数据（删除后需重新登录）",
    Level:  LevelCautious, // 数据类 cautious；纯缓存 moderate
    Paths: []string{
        hp("myai"),                              // ~\myai
        filepath.Join(roamingAppData(), "MyAI"), // %APPDATA%\MyAI
    },
},
```

路径不存在自动隐藏，可放心多写候选。跑 `go test -run TestCleanItemDefsSanity` 校验。

**安全约定 / Safety contract**：所有清理默认移入回收站；数据/配置类一律 `LevelCautious`（默认不勾选）；纯缓存用 `LevelModerate`。

## 10. 许可 / License

[MIT](LICENSE)

---

> 注意：本目录（`DiskSweep/`）与旧版 Python/PowerShell 工具完全隔离；所有本地数据（扫描快照/历史）只存于用户目录，可通过 ⚙ 设置换盘。
