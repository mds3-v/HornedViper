(function () {
  const panel = document.getElementById('jobs-panel');
  if (!panel) return;
  const owner = panel.dataset.owner;
  const id = panel.dataset.ownerId;
  if (!owner || !id) return;
  const list = document.getElementById('jobs-list');
  const count = document.getElementById('jobs-count');
  let timer = null;
  let stopped = false;

  function ensureDialog() {
    let dlg = document.getElementById('job-output-dialog');
    if (dlg) return dlg;
    dlg = document.createElement('dialog');
    dlg.id = 'job-output-dialog';
    dlg.className = 'modal job-output-dialog';
    dlg.innerHTML = '<div class="modal-head"><h2>后台输出</h2><button class="modal-close" type="button" aria-label="关闭">×</button></div><pre class="job-output"></pre>';
    dlg.querySelector('.modal-close').onclick = function () { dlg.close(); };
    dlg.addEventListener('click', function (e) { if (e.target === dlg) dlg.close(); });
    document.body.appendChild(dlg);
    return dlg;
  }

  async function showOutput(job) {
    const dlg = ensureDialog();
    const pre = dlg.querySelector('.job-output');
    pre.textContent = '读取中…';
    dlg.showModal();
    try {
      const res = await fetch('/api/jobs/output?owner=' + encodeURIComponent(owner) + '&id=' + encodeURIComponent(id) + '&job=' + encodeURIComponent(job));
      const data = await res.json();
      pre.textContent = data.output || data.error || '(无输出)';
    } catch (e) {
      pre.textContent = '读取失败：' + e;
    }
  }

  async function stopJob(job) {
    if (!confirm('停止后台任务 ' + job + '？')) return;
    await fetch('/api/jobs/stop?owner=' + encodeURIComponent(owner) + '&id=' + encodeURIComponent(id) + '&job=' + encodeURIComponent(job), {method: 'POST'});
    refresh();
  }

  function render(jobs) {
    if (count) count.textContent = String(jobs.length);
    list.replaceChildren();
    if (!jobs.length) {
      const empty = document.createElement('div');
      empty.className = 'empty small';
      empty.textContent = '暂无后台任务';
      list.appendChild(empty);
      return;
    }
    jobs.forEach(function (j) {
      const row = document.createElement('div');
      row.className = 'job-row';
      const meta = document.createElement('div');
      meta.className = 'job-meta';
      const name = document.createElement('span');
      name.className = 'job-id mono';
      name.textContent = j.id;
      const status = document.createElement('span');
      status.className = 'badge ' + (j.status === 'running' ? 'run' : (j.status === 'completed' ? 'ok' : 'bad'));
      status.textContent = j.active ? '运行中' : j.status;
      const cmd = document.createElement('span');
      cmd.className = 'job-command mono';
      cmd.textContent = j.command || j.description || '';
      meta.appendChild(name);
      meta.appendChild(status);
      meta.appendChild(cmd);
      row.appendChild(meta);
      const actions = document.createElement('span');
      actions.className = 'job-actions';
      const out = document.createElement('button');
      out.type = 'button'; out.className = 'btn small'; out.textContent = '输出';
      out.onclick = function () { showOutput(j.id); };
      actions.appendChild(out);
      if (j.status === 'running' && !j.active) {
        const kill = document.createElement('button');
        kill.type = 'button'; kill.className = 'btn small danger'; kill.textContent = '停止';
        kill.onclick = function () { stopJob(j.id); };
        actions.appendChild(kill);
      }
      row.appendChild(actions);
      list.appendChild(row);
    });
  }

  async function refresh() {
    if (stopped) return;
    try {
      const res = await fetch('/api/jobs?owner=' + encodeURIComponent(owner) + '&id=' + encodeURIComponent(id), {cache: 'no-store'});
      if (res.ok) render(await res.json());
    } catch (_) {}
    timer = setTimeout(refresh, 2000);
  }
  refresh();
  window.addEventListener('pagehide', function () { stopped = true; clearTimeout(timer); });
})();
