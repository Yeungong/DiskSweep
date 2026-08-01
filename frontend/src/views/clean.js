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

    const itemHTML = (it) => `
        <label class="clean-item">
            <input type="checkbox" data-id="${it.id}" ${it.level === 'safe' && it.id !== 'recycle_bin' ? 'checked' : ''}>
            <div class="item-info">
                <div class="item-name">${esc(it.name)}</div>
                <div class="item-path">${it.requiresAdmin && !state.isAdmin ? '🔒 ' : ''}${esc(it.paths.join('；'))}</div>
            </div>
            <span class="item-size">${fmtBytes(it.size)}</span>
            ${it.requiresAdmin && !state.isAdmin ? '<span class="badge badge-admin">需管理员</span>' : ''}
        </label>`;

    const levelBlockHTML = (its) => byLevelKeys.map(level => {
        const list = its.filter(it => it.level === level);
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

    const driveGroupHTML = (its, drive) => {
        const total = its.reduce((s, it) => s + it.size, 0);
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
                <span class="group-total">${fmtBytes(multi.reduce((s, it) => s + it.size, 0))}</span>
            </div>
            <div class="drive-body">${levelBlockHTML(multi)}</div>
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
            <p>将清理以下 ${items.length} 项，预计移入回收站 <b>${fmtBytes(items.reduce((s, it) => s + it.size, 0))}</b>：</p>
            <div class="modal-items">
                ${items.map(it => {
                    const nature = it.id === 'recycle_bin'
                        ? '<span class="modal-nature modal-warn">⚠ 将永久清空回收站</span>'
                        : '<span class="modal-nature">移入回收站（可恢复）</span>';
                    return `<div class="modal-item"><span>${esc(it.name)}</span>${nature}<span>${fmtBytes(it.size)}</span></div>`;
                }).join('')}
            </div>
            <p class="modal-warn">清理不会直接删除文件，而是移入回收站；如需真正释放空间，请之后清空回收站。</p>
            ${adminNeeded ? '<p class="modal-warn">⚠ 部分项目需要管理员权限，将以普通权限尝试，无法移动的会列出。</p>' : ''}
        `;
        showModal('确认清理', body, '确认清理', () => runClean(items.map(i => i.id)));
    });
}

async function runClean(ids) {
    const btn = document.getElementById('btnClean');
    btn.disabled = true;
    log(`开始清理 ${ids.length} 项…`, 'warn');

    let results;
    try {
        results = await api.executeClean(ids);
    } catch (e) {
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
    log(`清理完成，共移入回收站 ${fmtBytes(freed)}`, 'ok');
    toast(`已移入回收站 ${fmtBytes(freed)}，可在历史记录中恢复`, 'ok');
    btn.disabled = false;

    // Refresh items, analyze view, history and drive info.
    refresh();
    state.dirCache.clear();   // drop cached listings that may reference deleted files
    state.topFiles = null;    // force top-files re-fetch (some may be gone now)
    analyze.refresh();        // refresh dir list + top files after deletion
    history.refresh();        // new entries were recorded
    refreshDrives();
}

initClean();
