import { api } from '../api';
import { fmtBytes } from '../state';
import { toast } from '../main';

function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

const CAT_ICON = {
    '系统运行时': '⚙️',
    '开发工具链': '🛠',
    '本地大模型': '🧠',
    '开源软件': '📦',
};

const CLEAN_META = {
    keep: { label: '不可清理', cls: 'dep-tag dep-tag-keep' },
    partial: { label: '部分可清', cls: 'dep-tag dep-tag-partial' },
    model: { label: '模型·慎重', cls: 'dep-tag dep-tag-model' },
};

let depsCache = null;

export async function refresh(force = false) {
    const list = document.getElementById('depList');
    const summary = document.getElementById('depSummary');
    if (!force && depsCache) {
        renderDeps(depsCache, list, summary);
        return;
    }
    list.innerHTML = '<div class="empty-state">正在探测本机依赖…</div>';
    let deps;
    try {
        deps = await api.dependencies();
    } catch (e) {
        list.innerHTML = `<div class="empty-state">依赖探测失败: ${esc(e)}</div>`;
        return;
    }
    depsCache = deps;
    renderDeps(deps, list, summary);
}

function renderDeps(deps, list, summary) {
    if (!deps.length) {
        list.innerHTML = '<div class="empty-state">未识别到已知依赖</div>';
        summary.textContent = '';
        return;
    }
    // Summary: total count + biggest deps.
    const total = deps.reduce((s, d) => s + d.size, 0);
    const biggest = [...deps].sort((a, b) => b.size - a.size)[0];
    summary.textContent = `共识别 ${deps.length} 个依赖 · 合计约 ${fmtBytes(total)} · 最大: ${biggest.name} (${fmtBytes(biggest.size)})`;

    // Group by category, sorted by size desc.
    const byCat = new Map();
    deps.forEach(d => {
        if (!byCat.has(d.category)) byCat.set(d.category, []);
        byCat.get(d.category).push(d);
    });

    const block = (cat, its) => {
        const totalCat = its.reduce((s, d) => s + d.size, 0);
        return `
        <div class="dep-block">
            <div class="dep-block-head">
                <span class="group-icon">${CAT_ICON[cat] || '📁'}</span>
                <span class="group-name-sm">${esc(cat)}</span>
                <span class="group-count">${its.length} 项</span>
                <span class="group-total">${fmtBytes(totalCat)}</span>
            </div>
            ${its.map(d => {
                const cm = CLEAN_META[d.cleanable] || CLEAN_META.keep;
                const drives = d.drive ? `${d.drive}盘` : '';
                return `
                <div class="dep-row">
                    <div class="dep-info">
                        <div class="dep-name">${esc(d.name)} <span class="${cm.cls}">${cm.label}</span></div>
                        <div class="dep-desc">${esc(d.desc)}</div>
                        <div class="dep-note">💡 ${esc(d.cleanNote)}</div>
                    </div>
                    <div class="dep-meta">
                        <span class="dep-size">${fmtBytes(d.size)}</span>
                        <span class="dep-drive">${drives}</span>
                    </div>
                </div>`;
            }).join('')}
        </div>`;
    };

    list.innerHTML = [...byCat.entries()]
        .map(([cat, its]) => block(cat, its.sort((a, b) => b.size - a.size)))
        .join('');
}
