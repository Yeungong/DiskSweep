import { api } from '../api';
import { fmtBytes, fmtTime } from '../state';
import { toast } from '../main';

function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// format a date group header from a unix timestamp
function dayKey(unix) {
    const d = new Date(unix * 1000);
    const p = (x) => String(x).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export async function refresh() {
    const list = document.getElementById('historyList');
    let entries;
    try {
        entries = await api.getCleanHistory();
    } catch (e) {
        list.innerHTML = `<div class="empty-state">读取历史失败: ${esc(e)}</div>`;
        return;
    }
    if (!entries || entries.length === 0) {
        list.innerHTML = '<div class="empty-state">暂无清理记录。清理中心执行清理后，这里会记录每个移入回收站的项目。</div>';
        return;
    }

    // Group by day, newest first.
    let lastDay = '';
    const groups = [];
    entries.forEach(h => {
        const k = dayKey(h.time);
        if (k !== lastDay) {
            lastDay = k;
            groups.push({ day: k, items: [] });
        }
        groups[groups.length - 1].items.push(h);
    });

    list.innerHTML = groups.map(g => `
        <div class="history-day">${esc(g.day)}</div>
        ${g.items.map(h => `
            <div class="history-row ${h.ok ? '' : 'history-fail'}" data-id="${h.id}">
                <span class="history-time">${esc(fmtTime(h.time).slice(11))}</span>
                <div class="history-info">
                    <div class="history-name">${esc(h.itemName)}${h.ok ? '' : ' <span class="history-bad">失败</span>'}</div>
                    <div class="history-path" title="${esc(h.path)}">${esc(h.path)}${h.error ? '（' + esc(h.error) + '）' : ''}</div>
                </div>
                <span class="history-size">${fmtBytes(h.size)}</span>
                <span class="row-actions">
                    ${h.ok && !h.restored
                        ? '<button class="mini-btn btn-restore" data-act="restore" title="从回收站恢复到原位置">↩</button>'
                        : h.restored
                            ? '<span class="history-restored">已恢复</span>'
                            : ''}
                </span>
            </div>`).join('')}
    </div>`).join('');

    list.querySelectorAll('.btn-restore').forEach(btn => {
        btn.addEventListener('click', async () => {
            const row = btn.closest('.history-row');
            const id = Number(row.dataset.id);
            btn.disabled = true;
            try {
                const res = await api.restoreHistory(id);
                if (res.ok) {
                    toast('已恢复到原位置', 'ok');
                } else {
                    toast(res.message || '恢复失败', 'err');
                }
                refresh();
            } catch (e) {
                toast('恢复请求失败: ' + e, 'err');
                btn.disabled = false;
            }
        });
    });
}
