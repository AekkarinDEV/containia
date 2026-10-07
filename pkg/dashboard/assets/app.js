const state = {
  containers: [],
  images: [],
  layers: [],
  system: {},
  pollInterval: 2000,
  pollTimer: null,
  activeTab: 'containers',
  activeLogContainerId: null,
  logTimer: null,
  mode: 'compact',
  lastTrigger: null,
  polling: false,
  loaded: new Set(),
  running: false,
  building: false,
  pulling: false,
};

const modeCopy = {
  compact: { description: 'Dense controls and live resource data for daily operations.' },
  terminal: { description: 'A familiar command-line workspace with live controls.' },
  guided: { description: 'Clear steps and explanations for every container task.' },
};

const tabCopy = {
  containers: {
    title: 'Containers at a glance',
    compact: ['LIVE OPERATIONS', 'Scan status, resources and actions without leaving the list.', 'New container'],
    terminal: ['$ containia ps -a', 'Inspect processes, read logs, and control their lifecycle.', 'New container'],
    guided: ['STEP 1 · RUN', 'Start from a local image, then watch its status and open its logs here.', 'New container'],
  },
  images: {
    title: 'Your local images',
    compact: ['LOCAL REGISTRY', 'Search images, launch one, or reclaim storage.', 'Pull image'],
    terminal: ['$ containia images', 'Pull, inspect and run image references from your local store.', 'Pull image'],
    guided: ['STEP 1 · GET AN IMAGE', 'Pull an image first. Then use Run beside it to launch a container.', 'Pull image'],
  },
  builder: {
    title: 'Build an image',
    compact: ['IMAGE BUILD', 'Edit the Dockerfile and inspect the build output.', 'Build image'],
    terminal: ['$ containia build -t <tag> <context>', 'Turn Dockerfile instructions into a reusable local image.', 'Build image'],
    guided: ['STEP 1 · DEFINE', 'Set a tag and context, review the Dockerfile, then build.', 'Build image'],
  },
  network: {
    title: 'Container network',
    compact: ['NETWORK VIEW', 'Inspect assigned container addresses and bridge settings.', 'Refresh'],
    terminal: ['$ ip addr show containia0', 'Inspect the configured bridge and connected containers.', 'Refresh'],
    guided: ['HOW IT CONNECTS', 'Running containers with an IP appear beneath the host bridge.', 'Refresh'],
  },
  docs: {
    title: 'Guides and commands',
    compact: ['REFERENCE', 'Find the exact command for the next task.', 'New container'],
    terminal: ['$ containia --help', 'Copy commands and explore how Containia uses Linux primitives.', 'New container'],
    guided: ['LEARN THE BASICS', 'Start with installation, then pull an image and run a container.', 'New container'],
  },
  oslab: {
    title: 'OS Kernel Lab & Primitives',
    compact: ['KERNEL PRIMITIVES', 'Inspect Namespaces, Cgroups v2 limits, OverlayFS CoW, and Syscalls.', 'Refresh OS State'],
    terminal: ['$ cat /proc/<pid>/status | grep NSpid', 'Inspect Linux kernel namespace inodes and cgroups v2 limits.', 'Refresh OS State'],
    guided: ['OS DEEP DIVE', 'Observe how Linux Kernel isolates processes, memory, and storage.', 'Refresh OS State'],
  },
};

document.addEventListener('DOMContentLoaded', () => {
  let savedMode = 'compact';
  try { savedMode = localStorage.getItem('containia-mode') || 'compact'; } catch (_) {}
  setMode(savedMode);
  switchTab('containers');
  setupDialogs();
  setupTableActions();
  fetchDashboardData();
  setupPolling();
});

function setMode(mode) {
  if (!['compact', 'terminal'].includes(mode)) mode = 'compact';
  state.mode = mode;
  document.body.dataset.mode = mode;
  document.querySelectorAll('[data-mode-option]').forEach(button => {
    const active = button.dataset.modeOption === mode;
    button.classList.toggle('active', active);
    button.setAttribute('aria-pressed', String(active));
  });
  document.getElementById('modeDescription').textContent = modeCopy[mode].description;
  try { localStorage.setItem('containia-mode', mode); } catch (_) {}
  updateContext();
}

function setupTableActions() {
  document.getElementById('containersTableBody')?.addEventListener('click', event => {
    const button = event.target.closest('button[data-action]');
    if (!button) return;
    const { action, id, name } = button.dataset;
    if (action === 'logs') openLogsModal(id, name);
    if (action === 'stop') stopContainer(id);
    if (action === 'remove') removeContainer(id);
  });
  document.getElementById('imagesTableBody')?.addEventListener('click', event => {
    const button = event.target.closest('button[data-action]');
    if (!button) return;
    if (button.dataset.action === 'run') launchFromImage(button.dataset.image);
    if (button.dataset.action === 'delete') removeImage(button.dataset.image);
  });
  document.getElementById('quickPicksImages')?.addEventListener('click', event => {
    const button = event.target.closest('button[data-image]');
    if (button) document.getElementById('runImageSelect').value = button.dataset.image;
  });
}

function updateContext() {
  const tab = tabCopy[state.activeTab];
  if (!tab) return;
  let [marker, description, action] = tab[state.mode];
  let target = state.activeTab;
  if (state.mode === 'guided' && state.activeTab === 'containers') {
    if (!state.images.length) {
      marker = 'STEP 1 · PULL';
      description = 'Your image store is empty. Pull an image before launching a container.';
      action = 'Pull an image';
      target = 'images';
    } else if (!state.containers.length) {
      marker = 'STEP 2 · RUN';
      description = `You have ${state.images.length} local image${state.images.length === 1 ? '' : 's'}. Choose one to launch your first container.`;
    } else {
      marker = 'STEP 3 · OBSERVE';
      description = 'Open Logs beside a container to inspect its output. Stop ends a running process.';
    }
  }
  document.querySelector('.page-subtitle').textContent = tab[state.mode][1];
  document.getElementById('contextMarker').textContent = marker;
  document.getElementById('contextTitle').textContent = tab.title;
  document.getElementById('contextText').textContent = description;
  const button = document.getElementById('contextAction');
  button.textContent = action;
  button.dataset.target = target;
}

function runContextAction() {
  const target = document.getElementById('contextAction').dataset.target || state.activeTab;
  if (target === 'images') openPullModal();
  else if (target === 'builder') triggerBuild();
  else if (target === 'network') fetchDashboardData();
  else openRunModal();
}

function setConnectionStatus(status) {
  const label = document.getElementById('connectionStatus');
  const dot = document.getElementById('liveDot');
  if (!label || !dot) return;
  label.textContent = status === 'online' ? 'Runtime API connected'
    : status === 'degraded' ? 'Some runtime data unavailable' : 'Runtime API unavailable';
  document.getElementById('syncStatus').textContent = status === 'online'
    ? 'Runtime connected' : status === 'degraded' ? 'Some data unavailable · showing last available values' : 'Connection lost · data may be out of date';
  document.querySelector('.sync-status').dataset.status = status;
  dot.classList.toggle('live', status === 'online');
  dot.classList.toggle('offline', status !== 'online');
}

function setupPolling() {
  if (state.pollTimer) clearInterval(state.pollTimer);
  if (state.pollInterval > 0) {
    state.pollTimer = setInterval(fetchDashboardData, state.pollInterval);
  }
}

function changePollInterval(val) {
  state.pollInterval = parseInt(val, 10);
  setupPolling();
  showToast(`Polling interval set to ${val === '0' ? 'Paused' : val / 1000 + 's'}`, 'info');
}

async function fetchDashboardData() {
  if (state.polling) return;
  state.polling = true;
  document.getElementById('btnRefreshManual').disabled = true;
  try {
    const resources = ['containers', 'images', 'status', 'layers'];
    const results = await Promise.allSettled(resources.map(async resource => {
      const response = await fetch(`/api/${resource}`, { signal: AbortSignal.timeout(10000) });
      if (!response.ok) throw new Error(`${resource} unavailable`);
      const data = await response.json();
      state[resource === 'status' ? 'system' : resource] = resource === 'status' ? (data || {}) : (data || []);
      state.loaded.add(resource);
    }));
    const successes = results.filter(result => result.status === 'fulfilled').length;
    setConnectionStatus(successes === 4 ? 'online' : successes ? 'degraded' : 'offline');
    document.getElementById('lastUpdated').textContent = `Last checked ${new Date().toLocaleTimeString()}`;
    updateKPICards();
    renderContainersTable();
    renderImagesTable();
    renderLayersGrid();
    renderNetworkTopology();
    updateQuickPicks();
    updateContext();
  } catch (err) {
    console.error('Failed to poll dashboard data:', err);
    setConnectionStatus('offline');
  } finally {
    state.polling = false;
    document.getElementById('btnRefreshManual').disabled = false;
  }
}

function updateKPICards() {
  const runningContainers = state.containers.filter(c => c.status === 'Running');
  const runningEl = document.getElementById('kpiRunningVal');
  const totalEl = document.getElementById('kpiTotalVal');
  const navRunningEl = document.getElementById('navRunningCount');
  const navImagesEl = document.getElementById('navImagesCount');

  if (runningEl) runningEl.textContent = runningContainers.length;
  if (totalEl) totalEl.textContent = `${state.containers.length} total containers`;
  if (navRunningEl) navRunningEl.textContent = runningContainers.length;
  if (navImagesEl) navImagesEl.textContent = state.images.length;

  let totalMemBytes = 0;
  for (const c of runningContainers) {
    if (c.stats && c.stats.memory_current) {
      totalMemBytes += c.stats.memory_current;
    }
  }
  const memEl = document.getElementById('kpiMemoryVal');
  if (memEl) {
    memEl.textContent = `${(totalMemBytes / (1024 * 1024)).toFixed(1)} MB`;
  }

  const imagesEl = document.getElementById('kpiImagesVal');
  const layersEl = document.getElementById('kpiLayersVal');
  if (imagesEl) imagesEl.textContent = state.images.length;
  if (layersEl) layersEl.textContent = `${state.layers.length} cached layers`;

  const bridgeEl = document.getElementById('kpiBridgeIP');
  if (bridgeEl) bridgeEl.textContent = state.system.bridge_ip || '—';
  document.getElementById('bridgeSubnetText').textContent = state.system.bridge_subnet || 'Bridge unavailable';
}

function renderContainersTable() {
  const tbody = document.getElementById('containersTableBody');
  const filterInput = document.getElementById('filterContainersInput');
  const statusFilter = document.getElementById('containerStatusFilter').value;
  const countBadge = document.getElementById('containersCountBadge');

  if (!tbody) return;

  if (!state.loaded.has('containers')) {
    tbody.innerHTML = '<tr><td colspan="8"><div class="empty-state"><h3>Container data unavailable</h3><p>Check the runtime connection, then refresh to try again.</p><button class="btn btn-secondary" onclick="fetchDashboardData()">Retry connection</button></div></td></tr>';
    return;
  }
  const query = (filterInput?.value || '').toLowerCase().trim();
  let list = state.containers || [];

  if (statusFilter !== 'all') {
    list = list.filter(c => c.status === statusFilter);
  }

  if (query) {
    list = list.filter(c => 
      c.name.toLowerCase().includes(query) ||
      c.id.toLowerCase().includes(query) ||
      c.image.toLowerCase().includes(query) ||
      (c.ip_address && c.ip_address.includes(query))
    );
  }

  if (countBadge) countBadge.textContent = `${list.length} containers`;

  if (list.length === 0) {
    tbody.innerHTML = `
      <tr>
        <td colspan="8" class="text-center py-8 text-muted">
          <div class="empty-state"><span class="empty-symbol" aria-hidden="true">◇</span>
          <h3>${query || statusFilter !== 'all' ? 'No matching containers' : 'No containers'}</h3>
          <p>${query || statusFilter !== 'all' ? 'Try another search or reset your filters.' : 'Run a local image to create a container.'}</p>
          <button class="btn btn-secondary" onclick="${query || statusFilter !== 'all' ? 'resetContainerFilters()' : 'openRunModal()'}">${query || statusFilter !== 'all' ? 'Reset filters' : 'New container'}</button></div>
        </td>
      </tr>
    `;
    return;
  }

  const focusedAction = captureTableFocus(tbody);
  tbody.innerHTML = list.map(c => {
    const isRunning = c.status === 'Running';
    const statusClass = isRunning ? 'running' : (c.status === 'Stopped' ? 'stopped' : 'exited');
    const shortId = c.id.substring(0, 12);

    let memDisplay = 'Unlimited';
    let memPercent = 0;
    if (c.memory_limit > 0) {
      const limitMB = (c.memory_limit / (1024 * 1024)).toFixed(0);
      const curMB = c.stats?.memory_current ? (c.stats.memory_current / (1024 * 1024)).toFixed(1) : '0';
      memPercent = Math.min(100, Math.round(((c.stats?.memory_current || 0) / c.memory_limit) * 100));
      memDisplay = `${curMB} / ${limitMB} MB (${memPercent}%)`;
    } else if (c.stats?.memory_current) {
      memDisplay = `${(c.stats.memory_current / (1024 * 1024)).toFixed(1)} MB`;
    }

    const cpuDisplay = c.cpu_shares ? `${c.cpu_shares.split(' ')[0]} us` : 'Uncapped';
    const createdStr = formatTimeAgo(c.created_at);

    return `
      <tr id="row-${shortId}">
        <td data-label="Status">
          <span class="status-badge ${statusClass}">
            <span class="status-dot ${isRunning ? 'live' : ''}"></span>
            ${c.status}
          </span>
        </td>
        <td data-label="Name / ID">
          <div class="container-name-col">
            <span class="container-name">${escapeHtml(c.name)}</span>
            <span class="container-id">${shortId} (PID: ${c.pid || '-'})</span>
          </div>
        </td>
        <td data-label="Image">
          <span class="image-tag-badge">${escapeHtml(c.image)}</span>
        </td>
        <td data-label="IP address">
          <span class="font-mono text-xs">${c.ip_address ? escapeHtml(c.ip_address) : '<span class="text-dim">none</span>'}</span>
        </td>
        <td data-label="Memory">
          <div class="resource-bar-wrapper">
            <div class="resource-bar-labels">
              <span>${memDisplay}</span>
            </div>
            ${c.memory_limit > 0 ? `
              <div class="progress-bar-bg">
                <div class="progress-bar-fill ${memPercent > 80 ? 'high' : ''}" style="width: ${memPercent}%"></div>
              </div>
            ` : ''}
          </div>
        </td>
        <td data-label="CPU">
          <span class="font-mono text-xs">${cpuDisplay}</span>
        </td>
        <td data-label="Created">
          <span class="text-dim text-xs">${createdStr}</span>
        </td>
        <td data-label="Actions" class="text-right">
          <div class="table-actions">
            <button class="btn-xs" data-action="logs" data-id="${escapeHtml(c.id)}" data-name="${escapeHtml(c.name)}" aria-label="View logs for ${escapeHtml(c.name)}">Logs</button>
            ${isRunning ? `
              <button class="btn-xs btn-action-stop" data-action="stop" data-id="${escapeHtml(c.id)}">Stop</button>
            ` : `
              <button class="btn-xs btn-action-rm" data-action="remove" data-id="${escapeHtml(c.id)}">Remove</button>
            `}
          </div>
        </td>
      </tr>
    `;
  }).join('');
  restoreTableFocus(tbody, focusedAction);
}

function renderImagesTable() {
  const tbody = document.getElementById('imagesTableBody');
  const countBadge = document.getElementById('imagesCountBadge');
  if (!tbody) return;

  if (!state.loaded.has('images')) {
    tbody.innerHTML = '<tr><td colspan="7"><div class="empty-state"><h3>Image data unavailable</h3><p>Check the runtime connection, then refresh to try again.</p><button class="btn btn-secondary" onclick="fetchDashboardData()">Retry connection</button></div></td></tr>';
    return;
  }
  const query = document.getElementById('filterImagesInput')?.value.toLowerCase().trim() || '';
  const images = (state.images || []).filter(img => !query ||
    [img.repository, img.tag, img.id].some(value => String(value || '').toLowerCase().includes(query)));
  if (countBadge) countBadge.textContent = `${images.length} images`;

  if (images.length === 0) {
    tbody.innerHTML = `
      <tr>
        <td colspan="7" class="text-center py-8 text-muted">
          <div class="empty-state"><span class="empty-symbol" aria-hidden="true">▱</span>
          <h3>${query ? 'No matching images' : 'No local images'}</h3>
          <p>${query ? 'Try a different repository, tag or image ID.' : 'Pull an image to get started.'}</p>
          ${query ? '' : '<button class="btn btn-secondary" onclick="openPullModal()">Pull image</button>'}</div>
        </td>
      </tr>
    `;
    return;
  }

  const focusedAction = captureTableFocus(tbody);
  tbody.innerHTML = images.map(img => {
    const sizeMB = (img.size / (1024 * 1024)).toFixed(2);
    const layersCount = img.layers ? img.layers.length : 1;
    const createdStr = formatTimeAgo(img.created_at);

    return `
      <tr>
        <td data-label="Repository"><strong>${escapeHtml(img.repository)}</strong></td>
        <td data-label="Tag"><span class="image-tag-badge">${escapeHtml(img.tag || 'latest')}</span></td>
        <td data-label="Image ID"><span class="font-mono text-xs text-dim">${escapeHtml(img.id || '-')}</span></td>
        <td data-label="Size"><span class="font-mono text-xs">${sizeMB} MB</span></td>
        <td data-label="Layers"><span class="font-mono text-xs">${layersCount} layers</span></td>
        <td data-label="Created"><span class="text-dim text-xs">${createdStr}</span></td>
        <td data-label="Actions" class="text-right">
          <div class="table-actions">
            <button class="btn-xs" data-action="run" data-image="${escapeHtml(img.repository)}">Run</button>
            <button class="btn-xs btn-action-rm" data-action="delete" data-image="${escapeHtml(img.repository)}">Delete</button>
          </div>
        </td>
      </tr>
    `;
  }).join('');
  restoreTableFocus(tbody, focusedAction);
}

function renderLayersGrid() {
  const container = document.getElementById('layersGrid');
  if (!container) return;

  const layers = state.layers || [];
  if (layers.length === 0) {
    container.innerHTML = `<div class="text-muted p-4">No layers currently cached in /var/lib/containia/layers.</div>`;
    return;
  }

  container.innerHTML = layers.map(l => {
    const sizeMB = (l.size / (1024 * 1024)).toFixed(2);
    const cleanDigest = l.digest.replace('sha256_', 'sha256:');
    const shortDigest = cleanDigest.length > 20 ? cleanDigest.substring(0, 19) + '...' : cleanDigest;

    return `
      <div class="layer-card">
        <div class="layer-card-header">
          <span class="layer-hash">${escapeHtml(shortDigest)}</span>
          <span class="badge-version">${sizeMB} MB</span>
        </div>
        <div class="text-dim text-xs">Filesystem: Stacked OverlayFS Lowerdir</div>
      </div>
    `;
  }).join('');
}

function renderNetworkTopology() {
  const container = document.getElementById('networkContainersList');
  if (!container) return;

  document.getElementById('networkGateway').textContent = state.system.bridge_ip || 'Unknown';
  document.getElementById('networkSubnet').textContent = state.system.bridge_subnet || 'Unknown';
  document.getElementById('networkState').textContent = 'Configured by Containia · live NAT state not available';

  const runningWithIP = state.containers.filter(c => c.status === 'Running' && c.ip_address);
  if (runningWithIP.length === 0) {
    container.innerHTML = `
      <div class="text-muted text-center p-4 w-full">
        No active containers connected to bridge. Start a container to attach network namespace.
      </div>
    `;
    return;
  }

  container.innerHTML = runningWithIP.map(c => `
    <div class="net-cont-card">
      <div class="flex justify-between items-center mb-2">
        <span class="status-badge running">
          <span class="status-dot live"></span>
          ${escapeHtml(c.name)}
        </span>
        <span class="image-tag-badge">${escapeHtml(c.image)}</span>
      </div>
      <div class="net-node-body">
        <div class="net-row"><span class="k">Container IP:</span><span class="v status-good">${escapeHtml(c.ip_address)}</span></div>
        <div class="net-row"><span class="k">Gateway:</span><span class="v">${escapeHtml(state.system.bridge_ip || 'Unknown')}</span></div>
        <div class="net-row"><span class="k">Netns PID:</span><span class="v font-mono">${escapeHtml(c.pid)}</span></div>
      </div>
    </div>
  `).join('');
}

function updateQuickPicks() {
  const qp = document.getElementById('quickPicksImages');
  if (!qp) return;
  if (!state.images || state.images.length === 0) {
    qp.innerHTML = `<span class="qp-label">No local images yet</span>`;
    return;
  }
  qp.innerHTML = state.images.map(img => {
    const fullRef = img.repository;
    return `<button type="button" class="qp-btn" data-image="${escapeHtml(fullRef)}">${escapeHtml(fullRef)}</button>`;
  }).join('');
}

async function stopContainer(id) {
  try {
    showToast(`Stopping container ${id.substring(0, 12)}...`, 'info');
    const res = await fetch('/api/containers/stop', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id })
    });
    if (res.ok) {
      showToast(`Container stopped`, 'success');
      fetchDashboardData();
    } else {
      const err = await res.text();
      showToast(`Failed to stop: ${err}`, 'error');
    }
  } catch (err) {
    showToast(`Network error stopping container`, 'error');
  }
}

async function removeContainer(id) {
  if (!confirm(`Are you sure you want to remove container ${id.substring(0, 12)}?`)) return;
  try {
    const res = await fetch('/api/containers/rm', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, force: true })
    });
    if (res.ok) {
      showToast(`Container removed`, 'success');
      fetchDashboardData();
    } else {
      const err = await res.text();
      showToast(`Failed to remove: ${err}`, 'error');
    }
  } catch (err) {
    showToast(`Network error removing container`, 'error');
  }
}

async function handleRunSubmit(e) {
  e.preventDefault();
  if (state.running) return;
  const submit = document.getElementById('btnSubmitRun');
  const error = document.getElementById('runError');
  error.hidden = true;
  state.running = true;
  submit.disabled = true;
  submit.textContent = 'Launching…';
  const image = document.getElementById('runImageSelect').value.trim();
  const name = document.getElementById('runNameInput').value.trim();
  const cmd = document.getElementById('runCommandInput').value.trim();
  const memory = document.getElementById('runMemoryInput').value.trim();
  const cpus = document.getElementById('runCPUsInput').value.trim();
  const envText = document.getElementById('runEnvInput').value.trim();
  const volText = document.getElementById('runVolumeInput').value.trim();

  const env = envText ? envText.split('\n').map(s => s.trim()).filter(Boolean) : [];
  const volumes = volText ? volText.split('\n').map(s => s.trim()).filter(Boolean) : [];
  const command = cmd ? cmd.split(' ') : [];

  showToast(`Launching container from ${image}...`, 'info');

  try {
    const res = await fetch('/api/containers/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        image,
        name,
        command,
        memory,
        cpus,
        env,
        volumes,
        detach: true
      })
    });

    if (res.ok) {
      const data = await res.json();
      closeRunModal();
      switchTab('containers');
      showToast(`Launch requested: ${data.id?.substring(0, 12) || 'check the container list'}`, 'success');
      fetchDashboardData();
    } else {
      const err = await res.text();
      error.textContent = `Launch failed: ${err}`;
      error.hidden = false;
    }
  } catch (err) {
    error.textContent = 'Could not reach the runtime. Your settings are saved here; check the connection before retrying.';
    error.hidden = false;
  } finally {
    state.running = false;
    submit.disabled = false;
    submit.textContent = 'Launch container';
  }
}

async function handlePullSubmit(e) {
  e.preventDefault();
  if (state.pulling) return;
  const imgRef = document.getElementById('pullImageInput').value.trim();
  if (!imgRef) return;
  state.pulling = true;
  document.getElementById('pullSpinner').hidden = false;

  const progressPanel = document.getElementById('pullProgressPanel');
  const progressText = document.getElementById('pullProgressText');
  const progressOutput = document.getElementById('pullProgressOutput');
  const submitBtn = document.getElementById('btnSubmitPull');

  if (progressPanel) progressPanel.style.display = 'block';
  if (submitBtn) submitBtn.disabled = true;
  if (progressText) progressText.textContent = `Pulling ${imgRef} from Docker Registry v2...`;
  if (progressOutput) progressOutput.textContent = `Connecting to registry-1.docker.io for ${imgRef}...\n`;

  try {
    const res = await fetch('/api/images/pull', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ image: imgRef })
    });

    if (res.ok) {
      const data = await res.json();
      if (progressOutput) progressOutput.textContent += `\nSuccess! Pulled image: ${imgRef}`;
      showToast(`Image ${imgRef} pulled successfully!`, 'success');
      fetchDashboardData();
      progressText.textContent = 'Image ready to use';
    } else {
      const err = await res.text();
      if (progressOutput) progressOutput.textContent += `\nError: ${err}`;
      progressText.textContent = 'Pull failed · review the error below';
      showToast(`Pull failed: ${err}`, 'error');
    }
  } catch (err) {
    if (progressOutput) progressOutput.textContent += `\nNetwork error during pull`;
    progressText.textContent = 'Connection lost · try again';
    showToast(`Pull failed: network error`, 'error');
  } finally {
    state.pulling = false;
    document.getElementById('pullSpinner').hidden = true;
    if (submitBtn) submitBtn.disabled = false;
  }
}

async function removeImage(imageName) {
  if (!confirm(`Are you sure you want to remove image ${imageName}? Unused layers will be cleaned up.`)) return;
  try {
    const res = await fetch('/api/images', {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ image: imageName })
    });
    if (res.ok) {
      showToast(`Image removed and layers garbage-collected`, 'success');
      fetchDashboardData();
    } else {
      const err = await res.text();
      showToast(`Failed to remove image: ${err}`, 'error');
    }
  } catch (err) {
    showToast(`Network error removing image`, 'error');
  }
}

async function triggerBuild() {
  if (state.building) return;
  for (const id of ['buildTargetTag', 'buildContextDir']) {
    if (!document.getElementById(id).reportValidity()) return;
  }
  state.building = true;
  const buildButton = document.getElementById('btnTriggerBuild');
  buildButton.disabled = true;
  buildButton.textContent = 'Building…';
  const tag = document.getElementById('buildTargetTag').value.trim();
  const contextDir = document.getElementById('buildContextDir').value.trim();
  const instructions = document.getElementById('buildDockerfileEditor').value;
  const consoleEl = document.getElementById('buildConsoleOutput');

  if (consoleEl) {
    consoleEl.textContent = `[containia build] Starting build for ${tag} from context ${contextDir}...\n`;
  }

  showToast(`Building image ${tag}...`, 'info');

  try {
    const res = await fetch('/api/build', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        tag,
        context_dir: contextDir,
        dockerfile_content: instructions
      })
    });

    const data = await res.json();
    if (res.ok) {
      if (consoleEl) {
        consoleEl.textContent += (data.logs || 'Build completed successfully.') + `\nSuccessfully built and tagged ${tag}!`;
      }
      showToast(`Image ${tag} built successfully!`, 'success');
      fetchDashboardData();
    } else {
      if (consoleEl) {
        consoleEl.textContent += `\nBuild Error: ${data.error || 'Unknown error'}`;
      }
      showToast(`Build failed: ${data.error}`, 'error');
    }
  } catch (err) {
    if (consoleEl) consoleEl.textContent += `\nNetwork error triggering build.`;
    showToast(`Build error`, 'error');
  } finally {
    state.building = false;
    buildButton.disabled = false;
    buildButton.textContent = 'Build image';
  }
}

function clearBuildLogs() {
  const consoleEl = document.getElementById('buildConsoleOutput');
  if (consoleEl) consoleEl.textContent = 'Logs cleared. Ready for next build.';
}

async function openLogsModal(containerId, name) {
  state.activeLogContainerId = containerId;
  document.getElementById('logsContent').textContent = 'Loading logs…';
  const modal = document.getElementById('logsModal');
  const title = document.getElementById('logsModalTitle');
  const subtitle = document.getElementById('logsModalSubtitle');

  if (title) title.textContent = `Logs: ${name}`;
  if (subtitle) subtitle.textContent = `Container ID: ${containerId}`;
  if (modal) showDialog(modal, '.modal-close');

  fetchActiveLogs();
  if (state.logTimer) clearInterval(state.logTimer);
  state.logTimer = setInterval(fetchActiveLogs, 2000);
}

async function fetchActiveLogs() {
  if (!state.activeLogContainerId) return;
  const terminal = document.getElementById('logsContent');
  const requestedId = state.activeLogContainerId;
  try {
    const res = await fetch(`/api/containers/logs?id=${state.activeLogContainerId}`);
    if (res.ok) {
      const text = await res.text();
      if (state.activeLogContainerId !== requestedId) return;
      if (terminal) {
        terminal.textContent = text || '(Log is currently empty - container may not have produced output)';
      }
    } else {
      if (terminal) terminal.textContent = 'No logs available for this container.';
    }
  } catch (err) {
    if (terminal) terminal.textContent = 'Error fetching container logs.';
  }
}

function closeLogsModal() {
  const modal = document.getElementById('logsModal');
  if (modal) hideDialog(modal);
  state.activeLogContainerId = null;
  if (state.logTimer) clearInterval(state.logTimer);
}

function openRunModal() {
  showDialog(document.getElementById('runModal'), '#runImageSelect');
}
function closeRunModal() {
  hideDialog(document.getElementById('runModal'));
}
function openPullModal() {
  showDialog(document.getElementById('pullModal'), '#pullImageInput');
  const panel = document.getElementById('pullProgressPanel');
  if (panel && !state.pulling) panel.style.display = 'none';
}
function closePullModal() {
  hideDialog(document.getElementById('pullModal'));
}

function showDialog(modal, focusSelector) {
  if (!modal) return;
  state.lastTrigger = document.activeElement;
  document.querySelector('.app-layout').inert = true;
  modal.inert = false;
  modal.classList.add('open');
  modal.setAttribute('aria-hidden', 'false');
  document.body.classList.add('dialog-open');
  requestAnimationFrame(() => (modal.querySelector(focusSelector) || modal.querySelector('.modal-dialog')).focus());
}

function hideDialog(modal) {
  if (!modal) return;
  modal.classList.remove('open');
  modal.setAttribute('aria-hidden', 'true');
  modal.inert = true;
  if (!document.querySelector('.modal-backdrop.open')) {
    document.body.classList.remove('dialog-open');
    document.querySelector('.app-layout').inert = false;
  }
  if (state.lastTrigger?.isConnected) state.lastTrigger.focus();
}

function setupDialogs() {
  document.querySelectorAll('.modal-backdrop').forEach(modal => {
    modal.addEventListener('click', event => {
      if (event.target !== modal) return;
      if (modal.id === 'runModal') closeRunModal();
      if (modal.id === 'pullModal') closePullModal();
      if (modal.id === 'logsModal') closeLogsModal();
    });
  });
  document.addEventListener('keydown', event => {
    const modal = document.querySelector('.modal-backdrop.open');
    if (!modal) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      if (modal.id === 'runModal') closeRunModal();
      if (modal.id === 'pullModal') closePullModal();
      if (modal.id === 'logsModal') closeLogsModal();
    }
    if (event.key !== 'Tab') return;
    const focusable = [...modal.querySelectorAll('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], summary')].filter(element => element.getClientRects().length > 0);
    if (!focusable.length) return;
    if (event.shiftKey && document.activeElement === focusable[0]) {
      event.preventDefault(); focusable[focusable.length - 1].focus();
    } else if (!event.shiftKey && document.activeElement === focusable[focusable.length - 1]) {
      event.preventDefault(); focusable[0].focus();
    }
  });
}
function setPullInput(val) {
  const input = document.getElementById('pullImageInput');
  if (input) input.value = val;
}
function launchFromImage(imageRef) {
  openRunModal();
  const input = document.getElementById('runImageSelect');
  if (input) input.value = imageRef;
}

function resetContainerFilters() {
  document.getElementById('filterContainersInput').value = '';
  document.getElementById('containerStatusFilter').value = 'all';
  renderContainersTable();
  document.getElementById('filterContainersInput').focus();
}

function switchTab(tabName) {
  state.activeTab = tabName;
  document.querySelectorAll('.nav-item').forEach(el => el.classList.remove('active'));
  document.querySelectorAll('.tab-view').forEach(el => el.classList.remove('active'));

  if (tabName === 'containers') {
    document.getElementById('tabBtnContainers')?.classList.add('active');
    document.getElementById('viewContainers')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Containers';
  } else if (tabName === 'images') {
    document.getElementById('tabBtnImages')?.classList.add('active');
    document.getElementById('viewImages')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Images';
  } else if (tabName === 'builder') {
    document.getElementById('tabBtnBuilder')?.classList.add('active');
    document.getElementById('viewBuilder')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Image builder';
  } else if (tabName === 'network') {
    document.getElementById('tabBtnNetwork')?.classList.add('active');
    document.getElementById('viewNetwork')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Network';
  } else if (tabName === 'docs') {
    document.getElementById('tabBtnDocs')?.classList.add('active');
    document.getElementById('viewDocs')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Documentation';
  } else if (tabName === 'oslab') {
    document.getElementById('tabBtnOSLab')?.classList.add('active');
    document.getElementById('viewOSLab')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'OS Kernel Lab';
    fetchOSLabData();
  }
  document.querySelectorAll('.nav-item').forEach(button => {
    if (button.classList.contains('active')) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  });
  document.querySelector('.kpi-grid').hidden = !['containers', 'images', 'network'].includes(tabName);
  document.querySelector('.page-subtitle').textContent = tabCopy[tabName][state.mode][1];
  updateContext();
}

function copySnippet(btn, text) {
  if (!navigator.clipboard) {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand('copy');
    document.body.removeChild(textarea);
    btn.textContent = 'Copied!';
    setTimeout(() => { btn.textContent = 'Copy'; }, 2000);
    showToast('Command copied to clipboard', 'info');
    return;
  }
  navigator.clipboard.writeText(text).then(() => {
    btn.textContent = 'Copied!';
    setTimeout(() => { btn.textContent = 'Copy'; }, 2000);
    showToast('Command copied to clipboard', 'info');
  });
}

function showToast(message, type = 'info') {
  const container = document.getElementById('toastContainer');
  if (!container) return;

  const toast = document.createElement('div');
  toast.className = `toast ${type}`;
  toast.textContent = message;

  container.appendChild(toast);
  setTimeout(() => {
    toast.style.opacity = '0';
    toast.style.transform = 'translateY(10px)';
    toast.style.transition = 'all 0.3s ease';
    setTimeout(() => toast.remove(), 300);
  }, 3500);
}

function formatTimeAgo(dateString) {
  if (!dateString) return '-';
  const date = new Date(dateString);
  const sec = Math.floor((new Date() - date) / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}

function escapeHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

function captureTableFocus(table) {
  const active = document.activeElement;
  return table.contains(active) && active.matches('button[data-action]') ? { ...active.dataset } : null;
}
function restoreTableFocus(table, previous) {
  if (!previous) return;
  const button = [...table.querySelectorAll('button[data-action]')].find(button =>
    Object.keys(previous).every(key => button.dataset[key] === previous[key]));
  (button || table.closest('.table-responsive')).focus({ preventScroll: true });
}

let activeOSContainerId = '';
let activeOSSubView = 'namespaces';

function switchOSSubView(subId) {
  activeOSSubView = subId;
  const subviews = {
    namespaces: { btn: 'btnSubNamespaces', view: 'subViewNamespaces' },
    chaos: { btn: 'btnSubChaos', view: 'subViewChaos' },
    storage: { btn: 'btnSubStorage', view: 'subViewStorage' },
    network: { btn: 'btnSubNetwork', view: 'subViewNetwork' },
    lifecycle: { btn: 'btnSubLifecycle', view: 'subViewLifecycle' },
  };

  Object.keys(subviews).forEach(k => {
    const isTarget = k === subId;
    document.getElementById(subviews[k].btn)?.classList.toggle('active', isTarget);
    document.getElementById(subviews[k].view)?.classList.toggle('active', isTarget);
  });
}

function onOSContainerChange(cid) {
  activeOSContainerId = cid;
  loadSelectedOSContainer();
}

async function fetchOSLabData() {
  try {
    const res = await fetch('/api/containers');
    if (!res.ok) return;
    const containers = await res.json();
    const select = document.getElementById('osContainerSelect');
    if (!select) return;

    const previousCid = activeOSContainerId || select.value;
    select.innerHTML = '';

    if (!containers || containers.length === 0) {
      select.innerHTML = '<option value="">(No containers found - launch one first)</option>';
      activeOSContainerId = '';
      return;
    }

    containers.forEach(c => {
      const opt = document.createElement('option');
      opt.value = c.id;
      const statusIcon = c.status === 'running' ? '🟢' : '⚪';
      opt.textContent = `${statusIcon} ${c.name || c.id.slice(0, 12)} (${c.image}) [${c.status}]`;
      select.appendChild(opt);
    });

    if (previousCid && containers.some(c => c.id === previousCid)) {
      select.value = previousCid;
      activeOSContainerId = previousCid;
    } else {
      select.value = containers[0].id;
      activeOSContainerId = containers[0].id;
    }

    loadSelectedOSContainer();
    fetchOSLifecycle();
  } catch (err) {
    console.error('Failed to load OS Lab container list:', err);
  }
}

async function loadSelectedOSContainer() {
  if (!activeOSContainerId) return;

  fetchOSNamespaces(activeOSContainerId);

  fetchOSCgroups(activeOSContainerId);

  fetchOSStorage(activeOSContainerId);

  fetchOSNetwork(activeOSContainerId);
}

async function fetchOSNamespaces(cid) {
  try {
    const res = await fetch(`/api/os/namespaces?cid=${encodeURIComponent(cid)}`);
    if (!res.ok) return;
    const data = await res.json();

    document.getElementById('osHostPIDVal').textContent = data.host_pid > 0 ? data.host_pid : 'N/A (Exited)';
    document.getElementById('osNSpidText').textContent = data.nspid ? `NSpid: ${data.nspid}` : 'NSpid: isolated';
    document.getElementById('osTargetStatus').textContent = `Status: ${data.status || 'unknown'}`;
    document.getElementById('osTargetHostPID').textContent = `Host PID: ${data.host_pid || '-'}`;

    const tbody = document.getElementById('osNamespaceTableBody');
    if (!tbody) return;

    if (!data.namespaces || data.namespaces.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty-state">No namespace data found.</td></tr>';
      return;
    }

    tbody.innerHTML = data.namespaces.map(ns => `
      <tr>
        <td><strong>${escapeHtml(ns.type.toUpperCase())}</strong></td>
        <td><span class="badge-inode" style="color: var(--accent); font-weight: 600;">${escapeHtml(ns.container_inode)}</span></td>
        <td><span class="badge-inode">${escapeHtml(ns.host_inode)}</span></td>
        <td>
          <span class="${ns.is_isolated ? 'badge-iso-yes' : 'badge-iso-no'}">
            ${ns.is_isolated ? 'ISOLATED' : 'SHARED/HOST'}
          </span>
        </td>
        <td style="font-size: 12px; color: var(--text-muted);">${escapeHtml(ns.description)}</td>
      </tr>
    `).join('');
  } catch (err) {
    console.error('Failed to fetch OS namespaces:', err);
  }
}

async function fetchOSCgroups(cid) {
  try {
    const res = await fetch(`/api/os/cgroups?cid=${encodeURIComponent(cid)}`);
    if (!res.ok) return;
    const data = await res.json();

    const memCurrentMB = (data.memory_current / 1024 / 1024).toFixed(1);
    const memMaxMB = data.memory_max > 0 ? (data.memory_max / 1024 / 1024).toFixed(1) + ' MB' : 'Unlimited';
    document.getElementById('osCgroupMemVal').textContent = `${memCurrentMB} MB`;
    document.getElementById('osCgroupMemLimit').textContent = `Limit: ${memMaxMB}`;

    const pidsMax = data.pids_max > 0 ? data.pids_max : 'Unlimited';
    document.getElementById('osCgroupPidsVal').textContent = data.pids_current || 0;
    document.getElementById('osCgroupPidsLimit').textContent = `Limit: ${pidsMax} tasks`;

    const cpuQuota = data.cpu_quota > 0 ? `${data.cpu_quota} / ${data.cpu_period} µs` : '100% (No limit)';
    const throttledUsec = data.cpu_stat?.throttled_usec || 0;
    document.getElementById('osCgroupCpuVal').textContent = cpuQuota;
    document.getElementById('osCgroupCpuThrottled').textContent = `CFS Throttled: ${(throttledUsec / 1000).toFixed(1)} ms`;
  } catch (err) {
    console.error('Failed to fetch OS cgroups:', err);
  }
}

async function runChaosExperiment(expType) {
  if (!activeOSContainerId) {
    showToast('Please select a running container first.', 'warning');
    return;
  }

  const consoleEl = document.getElementById('chaosConsole');
  const statusEl = document.getElementById('chaosStatusIndicator');
  if (statusEl) statusEl.textContent = 'Kernel Executing...';

  consoleEl.textContent = `[CONTAINIA KERNEL LAB]\n> Triggering experiment: ${expType} on container ${activeOSContainerId.slice(0, 12)}...\n> Interacting with Linux kernel subsystems...\n`;

  try {
    const res = await fetch('/api/os/chaos', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cid: activeOSContainerId, experiment: expType }),
    });

    const result = await res.json();
    if (statusEl) statusEl.textContent = result.success ? 'Success / Enforced' : 'Finished with notice';

    if (result.error) {
      consoleEl.textContent += `\n❌ Error: ${result.error}\n`;
      showToast(result.error, 'error');
      return;
    }

    consoleEl.textContent += `\n✅ ${result.message}\n\n`;
    if (result.console_out) {
      consoleEl.textContent += `--- KERNEL CONSOLE OUTPUT ---\n${result.console_out}\n`;
    }

    setTimeout(() => {
      fetchOSCgroups(activeOSContainerId);
      fetchOSStorage(activeOSContainerId);
    }, 500);

    showToast(`Experiment '${expType}' executed!`, 'success');
  } catch (err) {
    console.error('Failed to run chaos experiment:', err);
    if (statusEl) statusEl.textContent = 'Error';
    consoleEl.textContent += `\n❌ Execution failed: ${err.message}\n`;
    showToast('Execution failed: ' + err.message, 'error');
  }
}

async function fetchOSStorage(cid) {
  try {
    const res = await fetch(`/api/os/storage?cid=${encodeURIComponent(cid)}`);
    if (!res.ok) return;
    const data = await res.json();

    document.getElementById('osMergedPath').textContent = data.mergeddir || '-';
    document.getElementById('osUpperPath').textContent = data.upperdir || '-';
    document.getElementById('osUpperStats').textContent =
      `Total CoW modifications: ${data.upper_files ? data.upper_files.length : 0} files (${(data.total_cow_bytes || 0).toLocaleString()} bytes)`;

    const lowerStack = document.getElementById('osLowerLayersStack');
    if (lowerStack) {
      if (data.lower_layers && data.lower_layers.length > 0) {
        lowerStack.innerHTML = data.lower_layers.map(l => `
          <div class="overlay-layer-card lower-layer">
            <div class="layer-info-left">
              <span class="layer-role" style="color: var(--accent-emerald);">Lower Layer #${l.index} (Image Layer: ${escapeHtml(l.digest.slice(0, 16))}...)</span>
              <span class="layer-path">${escapeHtml(l.path)}</span>
              <span class="layer-subtext">Content-Addressable Read-Only Layer (${(l.size / 1024 / 1024).toFixed(2)} MB)</span>
            </div>
            <span class="badge-version" style="color: var(--accent-emerald);">READ-ONLY</span>
          </div>
        `).join('');
      } else {
        lowerStack.innerHTML = `
          <div class="overlay-layer-card lower-layer">
            <div class="layer-info-left">
              <span class="layer-role" style="color: var(--accent-emerald);">Base Image Rootfs</span>
              <span class="layer-path">/var/lib/containia/images/&lt;image&gt;/rootfs</span>
              <span class="layer-subtext">Original Read-Only Base Image Layer</span>
            </div>
            <span class="badge-version" style="color: var(--accent-emerald);">READ-ONLY</span>
          </div>
        `;
      }
    }

    const tbody = document.getElementById('osUpperFilesTableBody');
    if (tbody) {
      if (!data.upper_files || data.upper_files.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" class="empty-state">No files modified in upperdir yet (Clean read-only baseline).</td></tr>';
      } else {
        tbody.innerHTML = data.upper_files.map(f => `
          <tr>
            <td><code style="color: var(--accent);">${escapeHtml(f.path)}</code></td>
            <td>${f.size.toLocaleString()} B</td>
            <td style="font-size: 11px; color: var(--text-dim);">${escapeHtml(f.mod_time)}</td>
            <td>
              ${f.is_char_dev
                ? '<span class="badge-version" style="color: var(--accent-rose);">WHITEOUT (c 0 0)</span>'
                : '<span class="badge-version" style="color: var(--accent-amber);">COW INODE</span>'}
            </td>
          </tr>
        `).join('');
      }
    }
  } catch (err) {
    console.error('Failed to fetch OS storage:', err);
  }
}

async function fetchOSNetwork(cid) {
  try {
    const res = await fetch(`/api/os/network?cid=${encodeURIComponent(cid)}`);
    if (!res.ok) return;
    const data = await res.json();

    document.getElementById('osTargetIP').textContent = `IP: ${data.container_ip || '-'}`;
    document.getElementById('osNetContDev').textContent = data.container_veth || 'eth0';
    document.getElementById('osNetContIP').textContent = data.container_ip || 'No IP';
    document.getElementById('osNetHostDev').textContent = data.host_veth || 'veth-host';
    document.getElementById('osNetBridgeName').textContent = data.bridge_name || 'containia0';
    document.getElementById('osNetBridgeIP').textContent = data.bridge_ip ? `${data.bridge_ip}/24` : '172.18.0.1/24';

    const tbody = document.getElementById('osNatRulesTableBody');
    if (tbody) {
      if (!data.nat_rules || data.nat_rules.length === 0) {
        tbody.innerHTML = `
          <tr>
            <td><code>-A POSTROUTING -s 172.18.0.0/24 ! -o containia0 -j MASQUERADE</code></td>
            <td><span class="badge-version">MASQUERADE</span></td>
            <td>Allows containers on 172.18.0.0/24 subnet to reach external LAN/Internet.</td>
          </tr>
        `;
      } else {
        tbody.innerHTML = data.nat_rules.map(r => `
          <tr>
            <td><code style="word-break: break-all;">${escapeHtml(r)}</code></td>
            <td><span class="badge-version">${r.includes('DNAT') ? 'DNAT (Port Fwd)' : 'MASQUERADE'}</span></td>
            <td>${r.includes('DNAT') ? 'Translates host incoming port to container private IP.' : 'Outbound NAT translation.'}</td>
          </tr>
        `).join('');
      }
    }
  } catch (err) {
    console.error('Failed to fetch OS network:', err);
  }
}

async function fetchOSLifecycle() {
  try {
    const res = await fetch('/api/os/lifecycle');
    if (!res.ok) return;
    const steps = await res.json();

    const timeline = document.getElementById('osLifecycleTimeline');
    if (!timeline) return;

    timeline.innerHTML = steps.map(s => `
      <div class="lifecycle-step">
        <div class="lifecycle-num">${s.step_number}</div>
        <div class="lifecycle-content">
          <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px;">
            <h4 class="lifecycle-title">${escapeHtml(s.title)}</h4>
            <span class="badge-version" style="color: var(--accent);">${escapeHtml(s.actor)}</span>
          </div>
          <code class="lifecycle-code">${escapeHtml(s.syscall)}</code>
          <p class="lifecycle-rationale"><strong>Kernel Action:</strong> ${escapeHtml(s.kernel_action)}</p>
          <p class="lifecycle-rationale" style="margin-top: 6px; color: var(--text-dim);">
            <strong>OS Rationale:</strong> ${escapeHtml(s.os_rationale)}
          </p>
        </div>
      </div>
    `).join('');
  } catch (err) {
    console.error('Failed to fetch OS lifecycle steps:', err);
  }
}
