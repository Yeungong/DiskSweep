import { api } from '../api';
import { fmtBytes } from '../state';
import { toast } from '../main';

function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

let trendCache = null;

// invalidate drops the cached payload (called after a new scan completes).
export function invalidate() {
    trendCache = null;
}

export async function refresh(force = false) {
    const hint = document.getElementById('trendHint');
    const deltaHint = document.getElementById('deltaHint');
    if (!force && trendCache) {
        renderTrend(trendCache);
        return;
    }
    let data;
    try {
        data = await api.diskTrend('C:');
    } catch (e) {
        hint.textContent = '空间趋势加载失败: ' + e;
        return;
    }
    trendCache = data;
    renderTrend(data);
    renderDups();
}

function renderTrend(data) {
    const canvas = document.getElementById('trendChart');
    const hint = document.getElementById('trendHint');
    const deltaHint = document.getElementById('deltaHint');
    const deltas = data.deltas || [];

    // ---------- line chart ----------
    const snaps = data.snapshots || [];
    if (snaps.length < 2) {
        canvas.outerHTML = `<div class="empty-state" id="trendChart">至少需要两次不同日期的扫描才会生成趋势图（每次点「开始扫描」自动记录）</div>`;
    } else {
        renderLineChart(canvas, snaps);
        hint.textContent = `${data.drive} 盘剩余空间变化：${snaps[0].day} ${fmtBytes(snaps[0].free)} → ${snaps[snaps.length - 1].day} ${fmtBytes(snaps[snaps.length - 1].free)}`;
    }

    // ---------- delta list ----------
    const list = document.getElementById('deltaList');
    if (deltas.length === 0) {
        list.innerHTML = '<div class="empty-state">最近两次扫描之间没有超过 50MB 的目录变化</div>';
        deltaHint.textContent = data.fromDay && data.toDay ? `${data.fromDay} → ${data.toDay} 无显著变化` : '需要两次不同日期的扫描';
        return;
    }
    deltaHint.textContent = data.fromDay ? `${data.fromDay} → ${data.toDay} 目录空间变化（正=空间减少/占用增加）` : '';
    list.innerHTML = deltas.map(d => {
        const grew = d.delta > 0;
        const sign = grew ? '+' : '';
        const cls = grew ? 'delta-grew' : 'delta-shrunk';
        return `
        <div class="dir-row delta-row ${cls}">
            <span class="row-icon">${grew ? '📈' : '📉'}</span>
            <span class="row-name" title="${esc(d.path)}">${esc(d.path)}</span>
            <span class="row-size">${sign}${fmtBytes(d.delta)}</span>
            <span class="row-count">${fmtBytes(d.sizeNow)}</span>
        </div>`;
    }).join('');
}

// renderLineChart draws an SVG line chart of free space per day.
function renderLineChart(el, snaps) {
    const W = 660, H = 240, PL = 64, PR = 16, PT = 24, PB = 40;
    const iw = W - PL - PR, ih = H - PT - PB;
    const free = snaps.map(s => s.free);
    const max = Math.max(...free) * 1.1 || 1;
    const min = Math.min(...free) * 0.9;
    const range = (max - min) || 1;
    const px = (i) => PL + (iw * i) / (snaps.length - 1);
    const py = (v) => PT + ih - (ih * (v - min)) / range;
    const gridLines = 4;

    const isDark = getComputedStyle(document.documentElement).getPropertyValue('--bg').trim().startsWith('#0') ||
        getComputedStyle(document.documentElement).getPropertyValue('--bg').trim().length < 4;
    const lineColor = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#e74c3c';
    const textColor = getComputedStyle(document.documentElement).getPropertyValue('--muted').trim() || '#888';
    const gridColor = getComputedStyle(document.documentElement).getPropertyValue('--border').trim() || '#333';

    let g = '';
    for (let i = 0; i <= gridLines; i++) {
        const v = min + (range * i) / gridLines;
        const y = py(v);
        g += `<line x1="${PL}" y1="${y}" x2="${W - PR}" y2="${y}" stroke="${gridColor}" stroke-width="1" stroke-dasharray="3 3"/>`;
        g += `<text x="${PL - 8}" y="${y + 4}" text-anchor="end" font-size="10" fill="${textColor}">${fmtBytes(v).replace(' ', '')}</text>`;
    }
    let pts = '';
    free.forEach((v, i) => { pts += `${px(i)},${py(v)} `; });
    g += `<polyline points="${pts.trim()}" fill="none" stroke="${lineColor}" stroke-width="2.5" stroke-linejoin="round"/>`;
    free.forEach((v, i) => {
        g += `<circle cx="${px(i)}" cy="${py(v)}" r="4" fill="${lineColor}"/>`;
        g += `<text x="${px(i)}" y="${H - PB + 16}" text-anchor="middle" font-size="10" fill="${textColor}">${snaps[i].day.slice(5)}</text>`;
        g += `<title>${snaps[i].day}: ${fmtBytes(v)} 剩余</title>`;
    });
    el.outerHTML = `
        <svg id="trendChart" viewBox="0 0 ${W} ${H}" style="width:100%;height:auto;display:block">
            <rect x="0" y="0" width="${W}" height="${H}" fill="transparent"/>
            ${g}
        </svg>`;
}

// ---------- duplicate apps ----------

async function renderDups() {
    const list = document.getElementById('dupAppList');
    let groups;
    try {
        groups = await api.duplicateApps();
    } catch (e) {
        list.innerHTML = `<div class="empty-state">重复软件检测失败: ${esc(e)}</div>`;
        return;
    }
    if (!groups || groups.length === 0) {
        list.innerHTML = '<div class="empty-state">未检测到明显的重复安装软件</div>';
        return;
    }
    const total = groups.reduce((s, g) => s + g.totalSizeMB, 0);
    list.innerHTML = `
        <div class="dep-summary">共 ${groups.length} 组疑似重复（合计约 ${total} MB，多为多版本并存，请自行判断）</div>
        ${groups.map(g => `
        <div class="dep-block">
            <div class="dep-block-head">
                <span class="group-icon">📦</span>
                <span class="group-name-sm">${esc(g.baseName)}</span>
                <span class="group-count">${g.count} 个</span>
                <span class="group-total">${fmtBytes(g.totalSizeMB * 1024 * 1024)}</span>
            </div>
            ${g.apps.map(e => `
            <div class="dep-row">
                <div class="dep-info">
                    <div class="dep-name">${esc(e.name)}</div>
                    <div class="dep-desc">${e.publisher ? esc(e.publisher) : ''}${e.version ? ' · v' + esc(e.version) : ''}</div>
                </div>
                <div class="dep-meta">
                    <span class="dep-size">${e.sizeMB ? fmtBytes(e.sizeMB * 1024 * 1024) : '—'}</span>
                </div>
            </div>`).join('')}
        </div>`).join('')}`;
}
