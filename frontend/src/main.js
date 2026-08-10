import './styles/base.css';
import './styles/themes/crt.css';
import './styles/themes/neu.css';
import './styles/themes/glass.css';
import './styles/themes/cyber.css';
import './styles/themes/vapor.css';

import { api, onScanProgress, onScanDone } from './api';
import { state, fmtBytes } from './state';
import * as analyze from './views/analyze';
import * as clean from './views/clean';
import * as history from './views/history';

// ---------- theme ----------

const THEMES = [
    { id: 'crt', label: 'CRT 终端' },
    { id: 'neu', label: '新拟态' },
    { id: 'glass', label: '玻璃拟态' },
    { id: 'cyber', label: '赛博朋克' },
    { id: 'vapor', label: '蒸汽波' },
];

function initTheme() {
    const saved = localStorage.getItem('ds-theme') || 'crt';
    applyTheme(saved);
    const menu = document.getElementById('themeMenu');
    menu.innerHTML = THEMES.map(t =>
        `<button class="theme-option ${t.id === saved ? 'active' : ''}" data-theme="${t.id}" type="button">${t.label}</button>`
    ).join('');
    document.getElementById('themeBtn').addEventListener('click', (e) => {
        e.stopPropagation();
        menu.classList.toggle('open');
    });
    menu.addEventListener('click', (e) => {
        const btn = e.target.closest('[data-theme]');
        if (!btn) return;
        applyTheme(btn.dataset.theme);
        localStorage.setItem('ds-theme', btn.dataset.theme);
        menu.querySelectorAll('.theme-option').forEach(o =>
            o.classList.toggle('active', o.dataset.theme === btn.dataset.theme));
        menu.classList.remove('open');
        toast(`已切换到「${btn.textContent}」主题`);
    });
    document.addEventListener('click', () => menu.classList.remove('open'));
}

function applyTheme(id) {
    document.documentElement.setAttribute('data-theme', id);
}

// ---------- toast / log ----------

function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

let toastTimer = null;
export function toast(msg, type = '') {
    const el = document.getElementById('toast');
    el.textContent = msg;
    el.className = `toast ${type}`.trim();
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, 2600);
}

// ---------- modal (shared by clean confirm & settings) ----------

export function showModal(title, bodyHTML, confirmLabel, onConfirm) {
    document.getElementById('modalTitle').textContent = title;
    document.getElementById('modalBody').innerHTML = bodyHTML;
    const confirm = document.getElementById('modalConfirm');
    confirm.textContent = confirmLabel || '确认';
    confirm.onclick = () => {
        closeModal();
        if (onConfirm) onConfirm();
    };
    document.getElementById('modalOverlay').hidden = false;
}

export function closeModal() {
    document.getElementById('modalOverlay').hidden = true;
    document.getElementById('modalConfirm').onclick = null;
}

function initModal() {
    document.getElementById('modalCancel').addEventListener('click', closeModal);
    document.getElementById('modalOverlay').addEventListener('click', (e) => {
        if (e.target === e.currentTarget) closeModal();
    });
}

// ---------- admin / elevation ----------

async function initAdmin() {
    state.isAdmin = await api.isAdmin();
    const badge = document.getElementById('adminBadge');
    badge.textContent = state.isAdmin ? '管理员' : '标准权限';
    badge.classList.toggle('is-admin', state.isAdmin);
    document.getElementById('adminBar').hidden = state.isAdmin;
}

function initElevate() {
    document.getElementById('btnElevate').addEventListener('click', async () => {
        const ok = await api.requestElevation();
        if (ok) {
            toast('正在以管理员身份重启…');
            setTimeout(() => api.quit(), 800);
        } else {
            toast('已取消提权', 'err');
        }
    });
}

// ---------- drives ----------

function renderDriveTabs() {
    const tabs = document.getElementById('driveTabs');
    const stats = document.getElementById('driveStats');
    const fill = document.getElementById('driveFill');
    tabs.innerHTML = state.drives.map(d =>
        `<button class="drive-tab ${d.drive === state.currentDrive ? 'active' : ''}" data-drive="${d.drive}" type="button">${d.drive}</button>`
    ).join('');
    tabs.querySelectorAll('.drive-tab').forEach(btn =>
        btn.addEventListener('click', () => switchDrive(btn.dataset.drive)));

    const d = state.drives.find(x => x.drive === state.currentDrive);
    if (!d) return;
    const pct = d.total ? (d.used / d.total * 100) : 0;
    stats.textContent = `已用 ${fmtBytes(d.used)} / 共 ${fmtBytes(d.total)}（${pct.toFixed(1)}%）· 可用 ${fmtBytes(d.free)}`;
    fill.style.width = pct.toFixed(1) + '%';
}

function switchDrive(drive) {
    state.currentDrive = drive;
    state.currentPath = drive + '\\';
    state.dirStack = [state.currentPath];
    state.dirCache.clear();
    renderDriveTabs();
    analyze.render();
    clean.refresh();
}

// ---------- scan ----------

function setScanning(on) {
    state.scanning = on;
    document.getElementById('btnScan').disabled = on;
    document.getElementById('btnCancel').hidden = !on;
    const status = document.getElementById('scanStatus');
    if (!on) return;
    status.textContent = '准备扫描…';
}

function initScan() {
    const btnScan = document.getElementById('btnScan');
    const btnCancel = document.getElementById('btnCancel');
    btnScan.addEventListener('click', async () => {
        if (!state.currentDrive) {
            toast('未检测到磁盘', 'err');
            return;
        }
        setScanning(true);
        try {
            await api.startScan(state.currentDrive + '\\', 200);
        } catch (e) {
            setScanning(false);
            toast('扫描启动失败: ' + e, 'err');
        }
    });
    btnCancel.addEventListener('click', () => {
        api.cancelScan();
        document.getElementById('scanStatus').textContent = '正在取消…';
    });

    onScanProgress((p) => {
        const status = document.getElementById('scanStatus');
        if (p.phase === 'enumerate') {
            status.textContent = `枚举目录… 共 ${p.dirsTotal} 个目录`;
        } else {
            const pct = p.dirsTotal ? Math.round(p.dirsDone / p.dirsTotal * 100) : 0;
            status.textContent = `扫描中… ${pct}%（${p.dirsDone}/${p.dirsTotal} 目录 · ${fmtBytes(p.bytes)}）`;
        }
    });

    onScanDone(async (summary) => {
        setScanning(false);
        state.scanSummary = summary;
        if (summary.cancelled) {
            document.getElementById('scanStatus').textContent = '扫描已取消';
            return;
        }
        const base = `扫描完成：${summary.dirs} 目录 · ${summary.files} 文件 · ${fmtBytes(summary.bytes)}（${(summary.durationMs / 1000).toFixed(1)}s）`;
        const inaccessible = summary.inaccessibleDirs > 0
            ? ` · ⚠ ${summary.inaccessibleDirs} 个目录无权限读取（可能隐藏占用，建议提权后重扫）`
            : '';
        document.getElementById('scanStatus').textContent = base + inaccessible;
        state.dirStack = [state.currentPath];
        state.dirCache.clear();
        state.topFiles = null; // force re-fetch, drop stale snapshot data

        // Each view refresh is isolated so one failure can't block the others.
        try { await analyze.refresh(); } catch (e) { console.error('analyze refresh failed:', e); }
        try { await clean.refresh(); } catch (e) { console.error('clean refresh failed:', e); }

        // Drive usage changed after a fresh scan.
        try {
            state.drives = await api.getDrives();
            renderDriveTabs();
        } catch (_) { /* non-fatal */ }
    });
}

// ---------- view switching ----------

function initViews() {
    document.querySelectorAll('.view-tab').forEach(btn =>
        btn.addEventListener('click', () => {
            document.querySelectorAll('.view-tab').forEach(b => b.classList.toggle('active', b === btn));
            document.getElementById('view-analyze').hidden = btn.dataset.view !== 'analyze';
            document.getElementById('view-clean').hidden = btn.dataset.view !== 'clean';
            document.getElementById('view-history').hidden = btn.dataset.view !== 'history';
            if (btn.dataset.view === 'history') history.refresh();
        }));
}

// ---------- settings (cache location) ----------

async function renderSettingsModal() {
    let loc;
    try {
        loc = await api.getCacheLocation();
    } catch (e) {
        toast('读取设置失败: ' + e, 'err');
        return;
    }
    const body = `
        <p>扫描快照与清理历史的存储位置：</p>
        <div class="setting-row">
            <span class="setting-label">当前</span>
            <span class="setting-path" id="curCachePath">${esc(loc.path)}（${fmtBytes(loc.size)}${loc.custom ? ' · 自定义' : ' · 默认' }）</span>
        </div>
        <div class="setting-row">
            <span class="setting-label">新位置</span>
            <div class="setting-input-row">
                <input id="cacheDirInput" type="text" placeholder="输入已存在的目录，如 D:\\Cache">
                <button class="btn btn-small" id="btnBrowse" type="button">浏览…</button>
            </div>
        </div>
        <p class="modal-warn">切换会自动迁移现有数据；建议先取消正在进行的扫描。</p>
        <div class="setting-row">
            <button class="btn btn-small" id="btnReplayOnboarding" type="button">重看引导教程</button>
        </div>`;

    showModal('设置 · 缓存位置', body, '应用并切换', async () => {
        const dir = document.getElementById('cacheDirInput').value.trim();
        if (!dir) {
            toast('请输入目录路径', 'err');
            return;
        }
        try {
            const res = await api.setCacheLocation(dir);
            toast('缓存已切换到: ' + res.path, 'ok');
        } catch (e) {
            toast('切换失败: ' + e, 'err');
        }
    });

    document.getElementById('btnBrowse').addEventListener('click', async () => {
        const dir = await api.pickDirectory();
        if (dir) document.getElementById('cacheDirInput').value = dir;
    });
    document.getElementById('btnReplayOnboarding').addEventListener('click', () => {
        localStorage.removeItem('ds-onboarded');
        closeModal();
        initOnboarding();
    });
}

function initSettings() {
    document.getElementById('btnSettings').addEventListener('click', renderSettingsModal);
}

// ---------- onboarding (first-launch tutorial) ----------

const ONBOARD_STEPS = [
    {
        icon: '👋',
        title: '欢迎使用 DiskSweep',
        body: '一个帮你「找出谁在占空间 + 一键清理垃圾」的磁盘管家。<br>先花 30 秒了解它怎么用，之后就能放心清。',
    },
    {
        icon: '🔍',
        title: '空间分析：先看清楚再动手',
        body: '点「开始扫描」分析全盘（几秒~几十秒，可随时取消）。<br>扫完按目录一层层下钻看大小，还能看全盘最大的文件，直接定位或移入回收站。',
    },
    {
        icon: '🧹',
        title: '清理中心：勾选 → 确认',
        body: '自动探测本机的缓存垃圾（临时文件、浏览器缓存、<b>AI 工具数据</b>等），按「安全 / 中等 / 谨慎」分组。<br>勾选要清的项，确认后逐项处理，失败的会列出原因。',
    },
    {
        icon: '♻️',
        title: '重要：删除 ≠ 清除',
        body: '清理是<b>移入回收站</b>，不是真正删除——随时可以撤回，也都会记在「历史记录」里一键恢复。<br><b>要手动清空回收站，才真正释放空间。</b>',
    },
    {
        icon: '🛡️',
        title: '安全设计',
        body: '· 谨慎项（.codex、Qoder、Claude 等 AI 数据）默认不勾选<br>· 系统目录一键 UAC 提权<br>· 缓存/历史可换盘存放（⚙ 设置）<br>· 5 套主题随意切换',
    },
    {
        icon: '🚀',
        title: '准备好了',
        body: '先去「空间分析」扫一次看看空间去向，再到「清理中心」试试清理。<br>有任何问题随时能找回：回收站 + 历史记录。',
    },
];

function initOnboarding() {
    if (localStorage.getItem('ds-onboarded')) return;
    const overlay = document.getElementById('onboarding');
    const steps = document.getElementById('onbSteps');
    const dots = document.getElementById('onbDots');
    const prev = document.getElementById('onbPrev');
    const next = document.getElementById('onbNext');

    let idx = 0;
    steps.innerHTML = ONBOARD_STEPS.map(s =>
        `<div class="onb-step"><div class="onb-icon">${s.icon}</div><h2>${s.title}</h2><p>${s.body}</p></div>`
    ).join('');
    dots.innerHTML = ONBOARD_STEPS.map((_, i) =>
        `<span class="onb-dot" data-i="${i}"></span>`).join('');

    function render() {
        [...steps.children].forEach((el, i) => el.classList.toggle('active', i === idx));
        [...dots.children].forEach((el, i) => el.classList.toggle('active', i === idx));
        prev.hidden = idx === 0;
        next.textContent = idx === ONBOARD_STEPS.length - 1 ? '开始使用' : '下一步';
    }
    prev.addEventListener('click', () => { if (idx > 0) { idx--; render(); } });
    next.addEventListener('click', () => {
        if (idx < ONBOARD_STEPS.length - 1) { idx++; render(); } else { finishOnboarding(); }
    });
    document.getElementById('onbSkip').addEventListener('click', finishOnboarding);
    overlay.addEventListener('click', (e) => { if (e.target === overlay) finishOnboarding(); });

    function finishOnboarding() {
        localStorage.setItem('ds-onboarded', '1');
        overlay.hidden = true;
    }

    overlay.hidden = false;
    render();
}

// ---------- github link ----------

const GITHUB_URL = 'https://github.com/Yeungong/DiskSweep';

function initGithub() {
    document.getElementById('githubBtn').addEventListener('click', () => {
        api.openURL(GITHUB_URL);
    });
}

// ---------- boot ----------

// refreshDrives re-reads drive usage and re-renders the overview card.
// Exported so the clean view can refresh it after a cleanup.
export async function refreshDrives() {
    try {
        state.drives = await api.getDrives();
        renderDriveTabs();
    } catch (_) { /* non-fatal */ }
}

async function boot() {
    initTheme();
    await initAdmin();
    initElevate();
    initScan();
    initViews();
    initModal();
    initSettings();
    initOnboarding();
    initGithub();

    state.drives = await api.getDrives();
    if (state.drives.length === 0) {
        toast('未检测到磁盘', 'err');
        return;
    }
    const c = state.drives.find(d => d.drive === 'C:') || state.drives[0];
    state.currentDrive = c.drive;
    state.currentPath = c.drive + '\\';
    renderDriveTabs();

    // Restore the last scan snapshot for instant browsing.
    try {
        const snap = await api.loadSnapshot(state.currentPath);
        if (snap.exists) {
            state.topFiles = snap.topFiles || [];
            const status = document.getElementById('scanStatus');
            status.textContent = `已恢复上次扫描快照（${snap.dirs} 目录 · ${fmtBytes(snap.topFiles.reduce((s, f) => s + f.size, 0) || 0)} 大文件）· 可重新扫描刷新`;
        }
    } catch (_) { /* snapshot is optional */ }

    analyze.render();
    clean.refresh();
}

boot();
