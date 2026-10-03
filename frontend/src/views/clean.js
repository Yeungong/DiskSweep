import { api } from '../api';
import { state, fmtBytes } from '../state';
import { toast, refreshDrives, showModal } from '../main';
import * as analyze from './analyze';
import * as history from './history';

// ---------- log ----------

export function log(msg, type = '') {
    const area = document.getElementById('logArea');
    const entry = document.createElement('div');
    entry.className = 'log-entry ' + type;
    const time = new Date().toLocaleTimeString('zh-CN', { hour12: false });
    entry.textContent = `[${time}] ${msg}`;
    area.appendChild(entry);
    area.scrollTop = area.scrollHeight;
}

// ---------- render ----------

const LEVEL_META = {
    safe: { label: '安全清理', desc: '可放心删除，不影响系统', icon: '🟢' },
    moderate: { label: '中等清理', desc: '重建后首次使用稍慢', icon: '🟡' },
    cautious: { label: '谨慎清理', desc: '删除后需重新下载或登录', icon: '🔴' },
};

function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// Some rules resolve to dozens of paths (Blizzard event caches add one folder
// per content update), so show the first couple plus a count instead of an
// unreadable wall of paths.
function pathSummary(paths) {
    const list = paths || [];
    if (list.length <= 2) return list.map(esc).join('；');
    return `${list.slice(0, 2).map(esc).join('；')} 等 ${list.length} 个位置`;
}

// Items that permanently delete data (no recycle-bin undo).
const PERMANENT_IDS = new Set(['recycle_bin', 'vss_shadows', 'win_upgrade_residue', 'windows_old']);

function isPermanent(it) {
    return PERMANENT_IDS.has(it.id);
}

export async function refresh() {
    const groups = document.getElementById('cleanGroups');
    groups.innerHTML = '<div class="empty-state">正在探测清理项…</div>';
    let items;
    try {
        items = await api.cleanupItems();
    } catch (e) {
        groups.innerHTML = `<div class="empty-state">探测失败: ${esc(e)}</div>`;
        return;
    }
    state.cleanItems = items;

    // Multi-drive items (e.g. recycle bin) shown at top; the rest grouped by drive.
    const multi = items.filter(it => it.drive === 'ALL');
    const byDrive = new Map();
    items.forEach(it => {
        if (it.drive === 'ALL') return;
        const d = it.drive || '?';
        if (!byDrive.has(d)) byDrive.set(d, []);
        byDrive.get(d).push(it);
    });
    const driveOrder = [...byDrive.keys()].sort();

    // Collapse all drives except the currently analyzed one.
    const collapsed = new Set(driveOrder.filter(d => d !== state.currentDrive));

    // View-only targets (WinSxS / Windows\Installer / WindowsApps) render as
    // plain rows with no checkbox, so they can never be selected or cleaned.
    // The backend refuses them independently -- see infoOnlyRuleIDs.
    //
    // Anything carrying a per-part breakdown (WindowsApps lists the biggest
    // Store apps) gets a collapsible list, so the user can see what is actually
    // installed instead of only the directory total.
    const childrenHTML = (it) => {
        const kids = it.children || [];
        if (!kids.length) return '';
        const count = it.childCount || kids.length;
        return `
            <details class="item-children">
                <summary>查看装了哪些应用（共 ${count} 个）</summary>
                <div class="child-list">
                    ${kids.map(c => `
                        <div class="child-row" title="${esc(c.id || c.name)}">
                            <span class="child-name">${esc(c.name)}</span>
                            <span class="child-size">${fmtBytes(c.size)}</span>
                        </div>`).join('')}
                </div>
            </details>`;
    };

    const itemHTML = (it) => it.infoOnly ? `
    <div class="clean-item clean-item-infoonly">
        <span class="item-check-spacer"></span>
        <div class="item-info">
            <div class="item-name">${esc(it.name)} <span class="badge badge-infoonly">仅供查看</span></div>
            <div class="item-path">${pathSummary(it.paths)}</div>
            ${it.infoNote ? `<div class="item-note">${esc(it.infoNote)}</div>` : ''}
            ${childrenHTML(it)}
        </div>
        <span class="item-size">${fmtBytes(it.size)}</span>
    </div>` : `
    <label class="clean-item">
        <input type="checkbox" data-id="${it.id}" ${it.level === 'safe' && it.id !== 'recycle_bin' && !it.noAutoCheck ? 'checked' : ''}>
        <div class="item-info">
            <div class="item-name">${esc(it.name)}${isPermanent(it) ? ' <span class="badge badge-danger">永久删除</span>' : ''}${it.noAutoCheck ? ' <span class="badge badge-admin">需手动勾选</span>' : ''}</div>
            <div class="item-path">${it.requiresAdmin && !state.isAdmin ? '🔒 ' : ''}${pathSummary(it.paths)}</div>
        </div>
        <span class="item-size">${fmtBytes(it.size)}</span>
        ${it.requiresAdmin && !state.isAdmin ? '<span class="badge badge-admin">需管理员</span>' : ''}
    </label>`;

    // Totals only ever count cleanable items: adding WinSxS' ~12 GB (which is
    // mostly hardlinks and cannot be freed by this tool anyway) would make the
    // "can free" figure meaningless.
    const cleanable = (its) => its.filter(it => !it.infoOnly);
    const viewOnly = (its) => its.filter(it => it.infoOnly);

    const levelBlockHTML = (its) => byLevelKeys.map(level => {
        const list = cleanable(its).filter(it => it.level === level);
        if (!list.length) return '';
        const meta = LEVEL_META[level];
        return `
        <div class="level-block" data-level="${level}">
            <div class="level-head">
                <span class="group-icon">${meta.icon}</span>
                <span class="group-name-sm">${meta.label}</span>
                <span class="group-desc-sm">${meta.desc}</span>
                <span class="group-total">${fmtBytes(list.reduce((s, it) => s + it.size, 0))}</span>
            </div>
            ${list.map(itemHTML).join('')}
        </div>`;
    }).join('');

    const infoOnlyBlockHTML = (its) => {
        const list = viewOnly(its);
        if (!list.length) return '';
        return `
        <div class="infoonly-block">
            <div class="infoonly-head">
                <span>👁 仅供查看 · 本工具不会删除（需 DISM / 系统自带工具处理）</span>
                <span class="infoonly-total">${fmtBytes(list.reduce((s, it) => s + it.size, 0))}</span>
            </div>
            ${list.map(itemHTML).join('')}
        </div>`;
    };

    const driveGroupHTML = (its, drive) => {
        const total = cleanable(its).reduce((s, it) => s + it.size, 0);
        const isCollapsed = collapsed.has(drive);
        return `
        <div class="clean-group drive-group" data-drive="${drive}">
            <div class="drive-head" data-drive="${drive}">
                <span class="drive-fold">${isCollapsed ? '▸' : '▾'}</span>
                <span class="drive-name">${esc(drive)} 盘</span>
                <span class="drive-count">${its.length} 项</span>
                <span class="group-total">${fmtBytes(total)}</span>
            </div>
            <div class="drive-body ${isCollapsed ? 'collapsed' : ''}">
                ${levelBlockHTML(its)}
                ${infoOnlyBlockHTML(its)}
            </div>
        </div>`;
    };

    try {
        groups.innerHTML = `
        ${multi.length ? `
        <div class="clean-group drive-group" data-drive="ALL">
            <div class="drive-head">
                <span class="drive-name">全部磁盘</span>
                <span class="drive-count">${multi.length} 项</span>
                <span class="group-total">${fmtBytes(cleanable(multi).reduce((s, it) => s + it.size, 0))}</span>
            </div>
            <div class="drive-body">${levelBlockHTML(multi)}${infoOnlyBlockHTML(multi)}</div>
        </div>` : ''}
        ${driveOrder.map(d => driveGroupHTML(byDrive.get(d), d)).join('')}
    `;

        groups.querySelectorAll('.drive-head[data-drive]').forEach(h =>
            h.addEventListener('click', () => {
                const body = h.nextElementSibling;
                body.classList.toggle('collapsed');
                const fold = h.querySelector('.drive-fold');
                if (fold) fold.textContent = body.classList.contains('collapsed') ? '▸' : '▾';
            }));

        groups.querySelectorAll('input[type=checkbox]').forEach(cb =>
            cb.addEventListener('change', updateSummary));

        updateSummary();
    } catch (e) {
        console.error('clean render failed:', e);
        groups.innerHTML = `<div class="empty-state">清理项渲染失败: ${esc(String((e && e.message) || e))}</div>`;
    }
}

const byLevelKeys = ['safe', 'moderate', 'cautious'];

function selected() {
    return Array.from(document.querySelectorAll('#cleanGroups input:checked')).map(cb => cb.dataset.id);
}

function updateSummary() {
    const ids = selected();
    const items = state.cleanItems.filter(it => ids.includes(it.id));
    const total = items.reduce((s, it) => s + it.size, 0);
    document.getElementById('cleanSummary').textContent =
        `已选 ${items.length} 项，预计释放 ${fmtBytes(total)}`;
    document.getElementById('btnClean').disabled = items.length === 0;
    return items;
}

// ---------- clean flow ----------

function initClean() {
    document.getElementById('btnClean').addEventListener('click', () => {
        const items = updateSummary();
        if (items.length === 0) return;

        const adminNeeded = items.some(it => it.requiresAdmin && !state.isAdmin);
        const body = `
            <p>将清理以下 ${items.length} 项，预计释放 <b>${fmtBytes(items.reduce((s, it) => s + it.size, 0))}</b>：</p>
            <div class="modal-items">
                ${items.map(it => {
                    let nature;
                    if (it.id === 'vss_shadows') {
                        nature = '<span class="modal-nature modal-danger">⚠ 将永久删除系统还原点，不可恢复</span>';
                    } else if (isPermanent(it)) {
                        nature = '<span class="modal-nature modal-danger">⚠ 将永久删除，不可恢复</span>';
                    } else {
                        nature = '<span class="modal-nature">移入回收站（可恢复）</span>';
                    }
                    return `<div class="modal-item"><span>${esc(it.name)}</span>${nature}<span>${fmtBytes(it.size)}</span></div>`;
                }).join('')}
            </div>
            <p class="modal-warn">普通清理不会直接删除文件，而是移入回收站；如需真正释放空间，请之后清空回收站。</p>
            ${adminNeeded ? '<p class="modal-warn">⚠ 部分项目需要管理员权限，将以普通权限尝试，无法移动的会列出。</p>' : ''}
        `;
        showModal('确认清理', body, '确认清理', () => runClean(items.map(i => i.id)));
    });
}

async function runClean(ids) {
    const btn = document.getElementById('btnClean');
    btn.disabled = true;
    log(`开始清理 ${ids.length} 项…`, 'warn');

    // Show progress immediately (indeterminate until the first event lands)
    // so fast cleanups still give visible feedback.
    const startedAt = Date.now();
    window.progressApi?.showProgress(0, `正在清理 ${ids.length} 项…`, 'indeterminate');

    let results;
    try {
        results = await api.executeClean(ids);
    } catch (e) {
        window.progressApi?.hideProgress();
        log('清理请求失败: ' + e, 'err');
        btn.disabled = false;
        return;
    }

    let freed = 0;
    results.forEach(r => {
        if (r.ok) {
            freed += r.freed;
            log(`✓ ${r.name}: 移入回收站 ${fmtBytes(r.freed)}${r.warning ? '（' + r.warning + '）' : ''}`, 'ok');
        } else {
            log(`✗ ${r.name}: 清理失败`, 'err');
            (r.errors || []).forEach(err =>
                log(`    · ${err.path}: ${err.error}（${err.kind}）`, 'err'));
        }
    });
    log(`清理完成，共移入回收站 ${fmtBytes(freed)}（用时 ${((Date.now() - startedAt) / 1000).toFixed(1)}s）`, 'ok');
    toast(`已移入回收站 ${fmtBytes(freed)}，可在历史记录中恢复`, 'ok');
    btn.disabled = false;

    // Hold the "100% done" state for at least ~900ms so the completion is
    // visible even for near-instant cleanups.
    window.progressApi?.showProgress(100, `清理完成 ${ids.length} 项 · 共释放 ${fmtBytes(freed)}`);
    const elapsed = Date.now() - startedAt;
    const hold = Math.max(0, 900 - elapsed);
    setTimeout(() => window.progressApi?.hideProgress(), hold);

    // Refresh items, analyze view, history and drive info.
    refresh();
    state.dirCache.clear();   // drop cached listings that may reference deleted files
    state.topFiles = null;    // force top-files re-fetch (some may be gone now)
    analyze.refresh();        // refresh dir list + top files after deletion
    history.refresh();        // new entries were recorded
    refreshDrives();
}

initClean();
