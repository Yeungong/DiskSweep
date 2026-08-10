import { api } from '../api';
import { state, fmtBytes, fmtTime, fmtAge } from '../state';
import { toast } from '../main';

// ---------- directory browsing ----------

function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

export async function render() {
    const list = document.getElementById('dirList');
    const bc = document.getElementById('breadcrumb');

    // Breadcrumb
    const parts = [];
    const segments = state.currentPath.replace(/\\$/, '').split('\\');
    let acc = '';
    for (const seg of segments) {
        acc += seg + '\\';
        const label = seg === '' ? state.currentDrive : seg;
        const p = acc;
        parts.push(`<button class="crumb" data-path="${esc(p)}" type="button">${esc(label)}</button>`);
    }
    bc.innerHTML = parts.join('<span class="crumb-sep">›</span>');

    bc.querySelectorAll('.crumb').forEach(btn =>
        btn.addEventListener('click', () => {
            state.dirStack = [btn.dataset.path];
            state.currentPath = btn.dataset.path;
            render();
        }));

    // Children
    const key = state.currentPath;
    let children = state.dirCache.get(key);
    if (!children) {
        list.innerHTML = '<div class="empty-state">加载中…</div>';
        try {
            children = await api.getDirChildren(key);
            state.dirCache.set(key, children);
        } catch (e) {
            list.innerHTML = `<div class="empty-state">读取失败: ${esc(e)}</div>`;
            return;
        }
    }

    const dirs = children.filter(c => c.isDir).sort((a, b) => b.size - a.size);
    const files = children.filter(c => !c.isDir).sort((a, b) => b.size - a.size);

    if (dirs.length === 0 && files.length === 0) {
        list.innerHTML = '<div class="empty-state">空目录</div>';
        return;
    }

    const row = (c, isDir) => {
        const inaccessible = c.inaccessible
            ? '<span class="row-badge" title="该目录存在但无权限读取，可能隐藏大量占用">⚠ 无权限</span>'
            : '';
        return `
        <div class="dir-row" data-path="${esc(c.path)}" ${c.inaccessible ? 'data-locked="1"' : ''}>
            <span class="row-icon">${isDir ? (c.inaccessible ? '🔒' : '📁') : '📄'}</span>
            <span class="row-name" title="${esc(c.path)}">${esc(c.name)}${inaccessible}</span>
            <span class="row-size">${c.inaccessible ? '—' : fmtBytes(c.size)}</span>
            <span class="row-count">${c.isDir && !c.inaccessible ? c.fileCount + ' 文件' : ''}</span>
            <span class="row-actions">
                <button class="mini-btn" data-act="open" title="在资源管理器中打开">⌕</button>
                ${isDir ? '' : '<button class="mini-btn" data-act="recycle" title="移入回收站">🗑</button>'}
            </span>
        </div>`;
    };

    list.innerHTML = [
        ...dirs.map(d => row(d, true)),
        ...files.map(f => row(f, false)),
    ].join('');

    list.querySelectorAll('.dir-row').forEach(el => {
        const p = el.dataset.path;
        // double-click / name click drills into directories
        el.addEventListener('dblclick', () => {
            if (el.querySelector('.row-icon').textContent !== '📁') return;
            if (el.dataset.locked) {
                toast('该目录无权限读取，请以管理员身份重启后重扫', 'err');
                return;
            }
            state.dirStack.push(p);
            state.currentPath = p;
            render();
        });
        el.querySelector('[data-act="open"]').addEventListener('click', (e) => {
            e.stopPropagation();
            api.openInExplorer(p);
        });
        const recycle = el.querySelector('[data-act="recycle"]');
        if (recycle) {
            recycle.addEventListener('click', async (e) => {
                e.stopPropagation();
                try {
                    await api.recyclePath(p);
                    toast('已移入回收站');
                    state.dirCache.delete(state.currentPath);
                    render();
                } catch (err) {
                    toast('移入回收站失败: ' + err, 'err');
                }
            });
        }
    });
}

// ---------- top files ----------

export async function renderTopFiles() {
    const list = document.getElementById('fileList');
    const hint = document.getElementById('topHint');

    let top = state.topFiles;
    if (!top || top.length === 0) {
        try {
            top = await api.getTopFiles();
            state.topFiles = top;
        } catch (_) { /* ignore */ }
    }

    hint.textContent = top && top.length ? `共 ${top.length} 个` : '';

    if (!top || top.length === 0) {
        list.innerHTML = '<div class="empty-state">扫描后显示全盘最大的文件</div>';
        return;
    }

    list.innerHTML = top.map((f, i) => `
        <div class="file-row" title="${esc(f.path)}">
            <span class="file-rank">${i + 1}</span>
            <div class="file-info">
                <div class="file-name">${esc(f.path.split('\\').pop())}</div>
                <div class="file-path">${esc(f.path)}</div>
            </div>
            <div class="file-meta">
                <span class="file-size">${fmtBytes(f.size)}</span>
                <span class="file-age">${fmtAge(f.modTime)}</span>
            </div>
            <span class="row-actions">
                <button class="mini-btn" data-act="open" title="在资源管理器中定位">⌕</button>
                <button class="mini-btn" data-act="recycle" title="移入回收站">🗑</button>
            </span>
        </div>`).join('');

    list.querySelectorAll('.file-row').forEach((el, i) => {
        const p = top[i].path;
        el.querySelector('[data-act="open"]').addEventListener('click', () => api.openInExplorer(p));
        el.querySelector('[data-act="recycle"]').addEventListener('click', async () => {
            try {
                await api.recyclePath(p);
                toast('已移入回收站');
                state.topFiles = state.topFiles.filter(f => f.path !== p);
                renderTopFiles();
            } catch (err) {
                toast('移入回收站失败: ' + err, 'err');
            }
        });
    });
}

export async function refresh() {
    await Promise.all([render(), renderTopFiles()]);
}
