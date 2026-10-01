/**
 * Containia Monitor - Frontend Application Logic
 * Modular reactive client for Containia Mini Container Runtime API.
 */

// Global State
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
};

// Initialize Application
document.addEventListener('DOMContentLoaded', () => {
  fetchDashboardData();
  setupPolling();
});

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

/**
 * Fetch all runtime state from Containia Backend API
 */
async function fetchDashboardData() {
  try {
    const [containersRes, imagesRes, statusRes, layersRes] = await Promise.all([
      fetch('/api/containers'),
      fetch('/api/images'),
      fetch('/api/status'),
      fetch('/api/layers')
    ]);

    if (containersRes.ok) state.containers = await containersRes.json();
    if (imagesRes.ok) state.images = await imagesRes.json();
    if (statusRes.ok) state.system = await statusRes.json();
    if (layersRes.ok) state.layers = await layersRes.json();

    updateKPICards();
    renderContainersTable();
    renderImagesTable();
    renderLayersGrid();
    renderNetworkTopology();
    updateQuickPicks();
  } catch (err) {
    console.error('Failed to poll dashboard data:', err);
  }
}

/**
 * Update Top KPI Summary Cards
 */
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

  // Calculate live memory
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

  // Images count & cached layers
  const imagesEl = document.getElementById('kpiImagesVal');
  const layersEl = document.getElementById('kpiLayersVal');
  if (imagesEl) imagesEl.textContent = state.images.length;
  if (layersEl) layersEl.textContent = `${state.layers.length} cached layers`;

  // Bridge IP
  if (state.system.bridge_ip) {
    const bridgeEl = document.getElementById('kpiBridgeIP');
    if (bridgeEl) bridgeEl.textContent = state.system.bridge_ip;
  }
}

/**
 * Render Containers Table
 */
function renderContainersTable() {
  const tbody = document.getElementById('containersTableBody');
  const filterInput = document.getElementById('filterContainersInput');
  const showAll = document.getElementById('chkShowAll')?.checked ?? true;
  const countBadge = document.getElementById('containersCountBadge');

  if (!tbody) return;

  const query = (filterInput?.value || '').toLowerCase().trim();
  let list = state.containers || [];

  if (!showAll) {
    list = list.filter(c => c.status === 'Running');
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
          No containers found. Click "New Container" to launch one!
        </td>
      </tr>
    `;
    return;
  }

  tbody.innerHTML = list.map(c => {
    const isRunning = c.status === 'Running';
    const statusClass = isRunning ? 'running' : (c.status === 'Stopped' ? 'stopped' : 'exited');
    const shortId = c.id.substring(0, 12);

    // Memory bar calculation
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
        <td>
          <span class="status-badge ${statusClass}">
            <span class="status-dot ${isRunning ? 'live' : ''}"></span>
            ${c.status}
          </span>
        </td>
        <td>
          <div class="container-name-col">
            <span class="container-name">${escapeHtml(c.name)}</span>
            <span class="container-id">${shortId} (PID: ${c.pid || '-'})</span>
          </div>
        </td>
        <td>
          <span class="image-tag-badge">${escapeHtml(c.image)}</span>
        </td>
        <td>
          <span class="font-mono text-xs">${c.ip_address || '<span class="text-dim">none</span>'}</span>
        </td>
        <td>
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
        <td>
          <span class="font-mono text-xs">${cpuDisplay}</span>
        </td>
        <td>
          <span class="text-dim text-xs">${createdStr}</span>
        </td>
        <td class="text-right">
          <div class="table-actions">
            <button class="btn-xs" onclick="openLogsModal('${c.id}', '${escapeHtml(c.name)}')">Logs</button>
            ${isRunning ? `
              <button class="btn-xs btn-action-stop" onclick="stopContainer('${c.id}')">Stop</button>
            ` : `
              <button class="btn-xs btn-action-rm" onclick="removeContainer('${c.id}')">Remove</button>
            `}
          </div>
        </td>
      </tr>
    `;
  }).join('');
}

/**
 * Render Images Table
 */
function renderImagesTable() {
  const tbody = document.getElementById('imagesTableBody');
  const countBadge = document.getElementById('imagesCountBadge');
  if (!tbody) return;

  const images = state.images || [];
  if (countBadge) countBadge.textContent = `${images.length} images`;

  if (images.length === 0) {
    tbody.innerHTML = `
      <tr>
        <td colspan="7" class="text-center py-8 text-muted">
          No local images available. Click "Pull Image" above to download.
        </td>
      </tr>
    `;
    return;
  }

  tbody.innerHTML = images.map(img => {
    const sizeMB = (img.size / (1024 * 1024)).toFixed(2);
    const layersCount = img.layers ? img.layers.length : 1;
    const createdStr = formatTimeAgo(img.created_at);

    return `
      <tr>
        <td><strong>${escapeHtml(img.repository)}</strong></td>
        <td><span class="image-tag-badge">${escapeHtml(img.tag || 'latest')}</span></td>
        <td><span class="font-mono text-xs text-dim">${img.id || '-'}</span></td>
        <td><span class="font-mono text-xs">${sizeMB} MB</span></td>
        <td><span class="font-mono text-xs">${layersCount} layers</span></td>
        <td><span class="text-dim text-xs">${createdStr}</span></td>
        <td class="text-right">
          <div class="table-actions">
            <button class="btn-xs" onclick="launchFromImage('${escapeHtml(img.repository)}:${escapeHtml(img.tag || 'latest')}')">Run</button>
            <button class="btn-xs btn-action-rm" onclick="removeImage('${escapeHtml(img.repository)}')">Delete</button>
          </div>
        </td>
      </tr>
    `;
  }).join('');
}

/**
 * Render OverlayFS Layers Grid
 */
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
          <span class="layer-hash">${shortDigest}</span>
          <span class="badge-version">${sizeMB} MB</span>
        </div>
        <div class="text-dim text-xs">Filesystem: Stacked OverlayFS Lowerdir</div>
      </div>
    `;
  }).join('');
}

/**
 * Render Network Topology
 */
function renderNetworkTopology() {
  const container = document.getElementById('networkContainersList');
  if (!container) return;

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
        <div class="net-row"><span class="k">Container IP:</span><span class="v status-good">${c.ip_address}</span></div>
        <div class="net-row"><span class="k">Gateway:</span><span class="v">172.19.0.1</span></div>
        <div class="net-row"><span class="k">Netns PID:</span><span class="v font-mono">${c.pid}</span></div>
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
    const fullRef = img.tag ? `${img.repository}:${img.tag}` : img.repository;
    return `<button type="button" class="qp-btn" onclick="document.getElementById('runImageSelect').value='${escapeHtml(fullRef)}'">${escapeHtml(fullRef)}</button>`;
  }).join('');
}

/**
 * Container Lifecycle Actions
 */
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
  closeRunModal();

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
      showToast(`Container launched: ${data.id?.substring(0, 12) || 'OK'}`, 'success');
      fetchDashboardData();
    } else {
      const err = await res.text();
      showToast(`Launch failed: ${err}`, 'error');
    }
  } catch (err) {
    showToast(`Network error launching container`, 'error');
  }
}

/**
 * Image Actions: Pull & Remove
 */
async function handlePullSubmit(e) {
  e.preventDefault();
  const imgRef = document.getElementById('pullImageInput').value.trim();
  if (!imgRef) return;

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
      setTimeout(closePullModal, 1500);
    } else {
      const err = await res.text();
      if (progressOutput) progressOutput.textContent += `\nError: ${err}`;
      showToast(`Pull failed: ${err}`, 'error');
    }
  } catch (err) {
    if (progressOutput) progressOutput.textContent += `\nNetwork error during pull`;
    showToast(`Pull failed: network error`, 'error');
  } finally {
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

/**
 * Dockerfile Builder Trigger
 */
async function triggerBuild() {
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
  }
}

function clearBuildLogs() {
  const consoleEl = document.getElementById('buildConsoleOutput');
  if (consoleEl) consoleEl.textContent = 'Logs cleared. Ready for next build.';
}

/**
 * Logs Modal
 */
async function openLogsModal(containerId, name) {
  state.activeLogContainerId = containerId;
  const modal = document.getElementById('logsModal');
  const title = document.getElementById('logsModalTitle');
  const subtitle = document.getElementById('logsModalSubtitle');

  if (title) title.textContent = `Logs: ${name}`;
  if (subtitle) subtitle.textContent = `Container ID: ${containerId}`;
  if (modal) modal.classList.add('open');

  fetchActiveLogs();
  if (state.logTimer) clearInterval(state.logTimer);
  state.logTimer = setInterval(fetchActiveLogs, 2000);
}

async function fetchActiveLogs() {
  if (!state.activeLogContainerId) return;
  const terminal = document.getElementById('logsContent');
  try {
    const res = await fetch(`/api/containers/logs?id=${state.activeLogContainerId}`);
    if (res.ok) {
      const text = await res.text();
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
  if (modal) modal.classList.remove('open');
  state.activeLogContainerId = null;
  if (state.logTimer) clearInterval(state.logTimer);
}

/**
 * Modals & Navigation
 */
function openRunModal() {
  document.getElementById('runModal')?.classList.add('open');
}
function closeRunModal() {
  document.getElementById('runModal')?.classList.remove('open');
}
function openPullModal() {
  document.getElementById('pullModal')?.classList.add('open');
  const panel = document.getElementById('pullProgressPanel');
  if (panel) panel.style.display = 'none';
}
function closePullModal() {
  document.getElementById('pullModal')?.classList.remove('open');
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

function switchTab(tabName) {
  state.activeTab = tabName;
  document.querySelectorAll('.nav-item').forEach(el => el.classList.remove('active'));
  document.querySelectorAll('.tab-view').forEach(el => el.classList.remove('active'));

  if (tabName === 'containers') {
    document.getElementById('tabBtnContainers')?.classList.add('active');
    document.getElementById('viewContainers')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Container Dashboard';
  } else if (tabName === 'images') {
    document.getElementById('tabBtnImages')?.classList.add('active');
    document.getElementById('viewImages')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'OCI Images & Layer Store';
  } else if (tabName === 'builder') {
    document.getElementById('tabBtnBuilder')?.classList.add('active');
    document.getElementById('viewBuilder')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Dockerfile Builder';
  } else if (tabName === 'network') {
    document.getElementById('tabBtnNetwork')?.classList.add('active');
    document.getElementById('viewNetwork')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Kernel & Virtual Network';
  } else if (tabName === 'docs') {
    document.getElementById('tabBtnDocs')?.classList.add('active');
    document.getElementById('viewDocs')?.classList.add('active');
    document.getElementById('pageTitle').textContent = 'Documentation & Installation Guide';
  }
}

function copySnippet(btn, text) {
  if (!navigator.clipboard) {
    // fallback
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

/**
 * Toast Notification Utility
 */
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
