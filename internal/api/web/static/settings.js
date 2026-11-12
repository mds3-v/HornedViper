(function () {
  const list = document.getElementById('provider-list');
  if (!list) return;
  let dragged = null;

  list.querySelectorAll('.provider-row').forEach((row) => {
    row.addEventListener('dragstart', (e) => {
      if (e.target.closest('button, a, form')) {
        e.preventDefault();
        return;
      }
      dragged = row;
      row.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      try { e.dataTransfer.setData('text/plain', row.dataset.key); } catch (_) {}
    });
    row.addEventListener('dragend', () => {
      row.classList.remove('dragging');
      dragged = null;
    });
    row.addEventListener('dragover', (e) => {
      e.preventDefault();
      if (!dragged || dragged === row) return;
      const rect = row.getBoundingClientRect();
      const after = e.clientY - rect.top > rect.height / 2;
      list.insertBefore(dragged, after ? row.nextSibling : row);
    });
    row.addEventListener('drop', async (e) => {
      e.preventDefault();
      if (!dragged) return;
      const order = [...list.querySelectorAll('.provider-row')].map((r) => r.dataset.key);
      try {
        const res = await fetch('/settings/providers/reorder', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ order }),
        });
        if (!res.ok) {
          const data = await res.json().catch(() => ({}));
          alert(data.error || '保存排序失败');
        }
      } catch (err) {
        alert('保存排序失败: ' + err);
      }
      window.location.reload();
    });
  });
})();

// Keep wide tables inside the card on small screens instead of widening the page.
(function () {
  document.querySelectorAll('table.table').forEach(function (table) {
    if (table.closest('.auto-ticker')) return;
    if (table.parentElement && table.parentElement.classList.contains('table-wrap')) return;
    const wrap = document.createElement('div');
    wrap.className = 'table-wrap';
    table.parentNode.insertBefore(wrap, table);
    wrap.appendChild(table);
  });
})();
