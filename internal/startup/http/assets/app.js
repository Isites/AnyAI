const page = resolvePage(window.location.pathname);
const content = document.getElementById('content');
const flash = document.getElementById('flash');
const pageTitle = document.getElementById('page-title');
const pageSubtitle = document.getElementById('page-subtitle');
const pageName = document.getElementById('page-name');
const pageTime = document.getElementById('page-time');
const authToken = new URLSearchParams(window.location.search).get('token') || '';

const appState = {
  inventory: {
    agents: [],
    sharedSkills: [],
    systemSkills: [],
    notes: [],
  },
  chat: {
    agents: [],
    agentID: '',
    sessions: [],
    sessionID: '',
    history: [],
    pendingEntries: [],
    pendingHistoryBaseline: 0,
    pendingUserText: '',
    runEvents: [],
    runRecord: null,
    runStatus: 'idle',
    liveText: '',
    sending: false,
    activeSource: null,
    historySyncTimer: 0,
    historySyncToken: 0,
    runTree: [],
    runView: createRunViewState(),
    runTreeLoading: false,
    runTreeSyncTimer: 0,
    runTreeSyncToken: 0,
    runtimeDrawerOpen: false,
    runtimeDetailView: 'tree',
  },
  runs: {
    list: [],
    selectedRun: null,
    events: [],
    runTree: [],
    runView: createRunViewState(),
  },
};

pageName.textContent = page;
document.querySelectorAll('.nav a, .brand').forEach((link) => {
  if (link.dataset.page === page) {
    link.classList.add('active');
  }
  const href = link.getAttribute('href');
  if (href) {
    link.setAttribute('href', withAuthQuery(href));
  }
});

setInterval(() => {
  pageTime.textContent = new Date().toLocaleTimeString();
}, 1000);
pageTime.textContent = new Date().toLocaleTimeString();

window.addEventListener('beforeunload', () => {
  closeChatSource();
});

boot().catch((error) => {
  showFlash(error.message || String(error), true);
});

async function boot() {
  initDrawer();
  switch (page) {
    case 'chat':
      pageTitle.textContent = '会话工作台';
      pageSubtitle.textContent = '聚焦 Session 列表与会话窗口，运行态在消息区内实时展开。';
      await renderChat();
      break;
    case 'runs':
      pageTitle.textContent = '运行树';
      pageSubtitle.textContent = '查看最近运行、流式事件、工具与委派状态，并补读最终输出。';
      await renderRuns();
      break;
    case 'jobs':
      pageTitle.textContent = '计划任务';
      pageSubtitle.textContent = '通过 HTTP 接口管理 cron 作业，暂停、恢复、删除和改计划。';
      await renderJobs();
      break;
    case 'logs':
      pageTitle.textContent = '实时日志';
      pageSubtitle.textContent = '流式查看运行日志，帮助定位启动、路由和执行问题。';
      await renderLogs();
      break;
    case 'api':
      pageTitle.textContent = 'API 导航';
      pageSubtitle.textContent = '为其他 agent 工程师准备的接入手册，覆盖 HTTP、SSE 和事件契约。';
      await renderAPI();
      break;
    case 'settings':
      pageTitle.textContent = '配置编辑';
      pageSubtitle.textContent = '读取、编辑并保存当前 AnyAI 配置。';
      await renderSettings();
      break;
    default:
      pageTitle.textContent = '运行总览';
      pageSubtitle.textContent = '把 Agent、通道、运行、任务与 API 面统一放在一个控制台里。';
      await renderOverview();
      break;
  }
}

function initDrawer() {
  const drawer = document.getElementById('drawer');
  const backdrop = document.getElementById('drawer-backdrop');
  const toggle = document.getElementById('drawer-toggle');
  const close = document.getElementById('drawer-close');

  if (!drawer || !backdrop || !toggle) return;

  const open = () => {
    drawer.classList.add('active');
    drawer.setAttribute('aria-hidden', 'false');
    backdrop.classList.add('active');
    toggle.setAttribute('aria-expanded', 'true');
    document.body.style.overflow = 'hidden';
  };

  const closeFn = () => {
    drawer.classList.remove('active');
    drawer.setAttribute('aria-hidden', 'true');
    backdrop.classList.remove('active');
    toggle.setAttribute('aria-expanded', 'false');
    document.body.style.overflow = '';
  };

  toggle.addEventListener('click', open);
  if (close) close.addEventListener('click', closeFn);
  backdrop.addEventListener('click', closeFn);

  // Close drawer when clicking on nav links (for SPA feel)
  drawer.querySelectorAll('.nav a').forEach((link) => {
    link.addEventListener('click', () => {
      // Only close if not on large screen
      if (window.innerWidth < 1400) {
        closeFn();
      }
    });
  });

  // Close on escape key
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && drawer.classList.contains('active')) {
      closeFn();
    }
  });
}

function resolvePage(pathname) {
  const path = pathname.replace(/\/+$/, '') || '/';
  if (path === '/' || path === '/ui') return 'overview';
  if (path === '/ui/tasks') return 'overview';
  if (path === '/chat') return 'chat';
  if (path === '/jobs') return 'jobs';
  if (path === '/logs') return 'logs';
  if (path === '/settings') return 'settings';
  if (path.startsWith('/ui/')) return path.split('/').pop() || 'overview';
  return 'overview';
}

function withAuthQuery(url) {
  if (!authToken) return url;
  const absolute = new URL(url, window.location.origin);
  if (!absolute.searchParams.has('token')) {
    absolute.searchParams.set('token', authToken);
  }
  if (absolute.origin === window.location.origin) {
    return `${absolute.pathname}${absolute.search}${absolute.hash}`;
  }
  return absolute.toString();
}

async function fetchJSON(url, options = {}) {
  const response = await fetch(withAuthQuery(url), options);
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.error || `Request failed: ${response.status}`);
  }
  return payload;
}

async function fetchRunTree(runID) {
  if (!runID) return [];
  const payload = await fetchJSON(`/api/runs/${encodeURIComponent(runID)}/tree`).catch(() => ({ tree: [] }));
  return Array.isArray(payload.tree) ? payload.tree : [];
}

function showFlash(message, isError = false) {
  flash.textContent = message;
  flash.classList.remove('hidden');
  flash.classList.toggle('error', isError);
}

function clearFlash() {
  flash.textContent = '';
  flash.classList.add('hidden');
  flash.classList.remove('error');
}

// Toast notifications
function showToast(message, type = 'info', duration = 3000) {
  const container = document.getElementById('toast-container');
  if (!container) return;

  const toast = document.createElement('div');
  toast.className = `toast toast-${type}`;
  toast.textContent = message;
  container.appendChild(toast);

  // Trigger animation
  requestAnimationFrame(() => {
    toast.classList.add('show');
  });

  setTimeout(() => {
    toast.classList.remove('show');
    setTimeout(() => toast.remove(), 300);
  }, duration);
}

// Copy button SVG icon
const copyIconSVG = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>`;

// Add copy buttons to code blocks
function addCopyButtons(container) {
  if (!container) return;

  container.querySelectorAll('pre').forEach((pre) => {
    // Skip if already has copy button
    if (pre.querySelector('.copy-button')) return;

    const button = document.createElement('button');
    button.className = 'copy-button';
    button.innerHTML = `${copyIconSVG} 复制`;
    button.title = '复制到剪贴板';

    button.onclick = async () => {
      const code = pre.querySelector('code') || pre;
      const text = code.textContent || '';

      try {
        await navigator.clipboard.writeText(text);
        showToast('已复制到剪贴板', 'success', 2000);
      } catch (err) {
        showToast('复制失败，请手动复制', 'error', 3000);
      }
    };

    pre.appendChild(button);
  });
}

function escapeHTML(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;');
}

function renderMarkdown(text) {
  const source = String(text || '').trim();
  if (!source) return '';
  if (!window.marked?.parse) {
    return escapeHTML(source).replace(/\n/g, '<br>');
  }
  return window.marked.parse(escapeHTML(source), {
    gfm: true,
    breaks: true,
  });
}

function safeJSON(value) {
  if (value === undefined || value === null || value === '') return '';
  return JSON.stringify(value, null, 2);
}

function statusPill(status, label = '') {
  const normalized = (status || 'idle').toLowerCase();
  return `<span class="status-pill ${escapeHTML(normalized)}">${escapeHTML(label || normalized)}</span>`;
}

function methodPill(method) {
  return `<span class="method-pill ${method.toLowerCase()}">${escapeHTML(method)}</span>`;
}

function emptyState(message, actionHTML = '') {
  return `
    <div class="empty">
      <div>${escapeHTML(message)}</div>
      ${actionHTML}
    </div>
  `;
}

function sectionHeader(title, description = '', actionHTML = '') {
  return `
    <div class="section-head">
      <div>
        <h3>${escapeHTML(title)}</h3>
        ${description ? `<p class="muted">${escapeHTML(description)}</p>` : ''}
      </div>
      ${actionHTML ? `<div class="section-actions">${actionHTML}</div>` : ''}
    </div>
  `;
}

function formatDateTime(value) {
  if (!value) return '未记录';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}

function formatTime(value) {
  if (!value) return '--:--';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleTimeString();
}

function formatRelativeTime(value) {
  if (!value) return '刚刚';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  const diff = Date.now() - date.getTime();
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (Math.abs(diff) < minute) return '刚刚';
  if (Math.abs(diff) < hour) return `${Math.round(diff / minute)} 分钟前`;
  if (Math.abs(diff) < day) return `${Math.round(diff / hour)} 小时前`;
  return `${Math.round(diff / day)} 天前`;
}

function truncate(value, max = 96) {
  const text = String(value ?? '').trim();
  if (text.length <= max) return text;
  return `${text.slice(0, max)}...`;
}

function pad(value) {
  return String(value).padStart(2, '0');
}

function generateSessionName() {
  const now = new Date();
  return `session-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
}

function safeStorageGet(key) {
  try {
    return window.localStorage.getItem(key) || '';
  } catch {
    return '';
  }
}

function safeStorageSet(key, value) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Ignore browser storage failures.
  }
}

function safeStorageRemove(key) {
  try {
    window.localStorage.removeItem(key);
  } catch {
    // Ignore browser storage failures.
  }
}

function createRunViewState() {
  return {
    selectedRunID: '',
    expanded: {},
    filterStatus: 'all',
    filterAgent: 'all',
    relatedOnly: false,
    latestStackOpen: false,
  };
}

function resetRunViewState(view) {
  const target = view || createRunViewState();
  target.selectedRunID = '';
  target.expanded = {};
  target.filterStatus = 'all';
  target.filterAgent = 'all';
  target.relatedOnly = false;
  target.latestStackOpen = false;
  return target;
}

function resetChatRunTreeState() {
  if (appState.chat.runTreeSyncTimer) {
    clearTimeout(appState.chat.runTreeSyncTimer);
    appState.chat.runTreeSyncTimer = 0;
  }
  appState.chat.runTreeSyncToken += 1;
  appState.chat.runTree = [];
  appState.chat.runTreeLoading = false;
  resetRunViewState(appState.chat.runView);
}

function cancelChatHistorySync() {
  if (appState.chat.historySyncTimer) {
    clearTimeout(appState.chat.historySyncTimer);
    appState.chat.historySyncTimer = 0;
  }
  appState.chat.historySyncToken += 1;
}

function resetChatPendingState() {
  appState.chat.pendingEntries = [];
  appState.chat.pendingHistoryBaseline = appState.chat.history.length;
  appState.chat.pendingUserText = '';
}

function resetChatRuntimeState(nextStatus = 'idle') {
  cancelChatHistorySync();
  resetChatPendingState();
  appState.chat.runEvents = [];
  appState.chat.runRecord = null;
  appState.chat.runStatus = nextStatus;
  appState.chat.liveText = '';
  appState.chat.runtimeDrawerOpen = false;
  appState.chat.runtimeDetailView = 'tree';
  resetChatRunTreeState();
}

function summarizeInputBlocks(inputs) {
  if (!Array.isArray(inputs) || !inputs.length) return '无输入块';
  return inputs.map((block) => {
    if (block.type === 'text') return truncate(block.text || '空文本', 56);
    return `${block.type}${block.name ? `:${block.name}` : ''}`;
  }).join(' · ');
}

function summarizeToolPayload(payload) {
  if (!payload) return '等待工具输入';
  const input = payload.input || payload;
  if (typeof input === 'string') return truncate(input, 120);
  if (input && typeof input === 'object') {
    const priorityKeys = ['task', 'query', 'path', 'command', 'url', 'prompt', 'text', 'file', 'goal'];
    for (const key of priorityKeys) {
      if (typeof input[key] === 'string' && input[key].trim()) {
        return truncate(input[key], 120);
      }
    }
    for (const value of Object.values(input)) {
      if (typeof value === 'string' && value.trim()) {
        return truncate(value, 120);
      }
    }
    if (Array.isArray(input.items) && input.items.length) {
      return truncate(String(input.items[0] || ''), 120) || '工具已收到结构化输入。';
    }
    return '工具已收到结构化输入。';
  }
  return '工具已收到输入。';
}

function summarizeToolResultPayload(payload = {}) {
  const error = truncate(payload.error || '', 120);
  if (error) return error;
  const output = truncate(payload.output || '', 120);
  if (output) return output;
  return payload.is_error ? '工具调用失败。' : '工具已返回结果。';
}

function normalizePlanLine(line) {
  const text = String(line || '').trim();
  if (!text) return '';

  const checkboxPrefixes = ['- [ ] ', '- [x] ', '- [X] ', '* [ ] ', '* [x] ', '* [X] '];
  for (const prefix of checkboxPrefixes) {
    if (text.startsWith(prefix)) {
      return text.slice(prefix.length).trim();
    }
  }

  const bulletPrefixes = ['- ', '* ', '• '];
  for (const prefix of bulletPrefixes) {
    if (text.startsWith(prefix)) {
      return text.slice(prefix.length).trim();
    }
  }

  const numbered = text.match(/^\d+[\.\)\:]\s*(.+)$/);
  if (numbered) {
    return numbered[1].trim();
  }
  return text;
}

function summarizePlanEntry(plan) {
  const steps = String(plan || '')
    .replaceAll('\r\n', '\n')
    .split('\n')
    .map(normalizePlanLine)
    .filter(Boolean);

  if (!steps.length) return '进入规划阶段。';
  const current = truncate(steps[0], 88);
  if (steps.length === 1) return `当前阶段：${current}`;
  return `当前阶段：${current}；后续 ${steps.length - 1} 步待推进。`;
}

function todoStatusLabel(status) {
  switch (String(status || '').trim().toLowerCase()) {
    case 'completed':
    case 'done':
      return '已完成';
    case 'in_progress':
    case 'in-progress':
    case 'running':
    case 'doing':
    case 'active':
      return '进行中';
    case 'blocked':
    case 'paused':
    case 'waiting':
      return '暂时阻塞';
    case 'cancelled':
    case 'canceled':
      return '已取消';
    default:
      return '待处理';
  }
}

function todoStatusTone(status) {
  switch (String(status || '').trim().toLowerCase()) {
    case 'completed':
    case 'done':
      return 'completed';
    case 'in_progress':
    case 'in-progress':
    case 'running':
    case 'doing':
    case 'active':
      return 'running';
    case 'blocked':
    case 'paused':
    case 'waiting':
      return 'paused';
    case 'cancelled':
    case 'canceled':
      return 'cancelled';
    default:
      return 'idle';
  }
}

function summarizeTodoEntry(status, content, fallbackID = '') {
  const task = truncate(String(content || fallbackID || '事项'), 88);
  return `${todoStatusLabel(status)}：${task}`;
}

function decorateChatHistoryEntries(events) {
  const items = [];
  const index = new Map();

  const upsert = (key, factory) => {
    if (index.has(key)) return index.get(key);
    const item = factory();
    item._key = key;
    index.set(key, item);
    items.push(item);
    return item;
  };

  (Array.isArray(events) ? events : []).forEach((event) => {
    const payload = event?.payload || {};
    const name = String(event?.name || '').trim();
    const timestamp = event?.timestamp || new Date().toISOString();

    switch (name) {
      case 'session.input.stored': {
        const text = String(payload.text || '').trim();
        if (!text) break;
        items.push({
          _key: liveWindowKey('user', event.run_id, event.session_id, timestamp, text),
          type: 'message',
          role: 'user',
          text,
          timestamp,
          session_id: event.session_id || appState.chat.sessionID,
          run_id: event.run_id || '',
        });
        break;
      }
      case 'session.output.stored': {
        const text = String(payload.text || '').trim();
        if (!text) break;
        items.push({
          _key: liveWindowKey('message', event.run_id, event.agent_id, event.session_id),
          type: 'message',
          role: 'assistant',
          text,
          timestamp,
          agent_id: event.agent_id || appState.chat.agentID,
          session_id: event.session_id || appState.chat.sessionID,
          run_id: event.run_id || '',
        });
        break;
      }
      case 'agent.call.completed':
      case 'agent.call.failed': {
        const target = String(payload.target_agent || payload.agent || '').trim();
        const text = String(payload.summary || payload.error || '').trim();
        if (!target || !text) break;
        const key = liveWindowKey('agent', event.run_id, target, payload.session_id || payload.id || payload.task_id);
        const item = upsert(key, () => ({
          type: 'message',
          role: 'assistant',
          agent_id: target,
          session_id: payload.session_id || '',
          run_id: payload.run_id || event.run_id || '',
          text,
          status: payload.error || name === 'agent.call.failed' ? 'failed' : 'success',
          timestamp,
        }));
        item.text = text;
        item.status = payload.error || name === 'agent.call.failed' ? 'failed' : 'success';
        item.pending = false;
        break;
      }
      case 'tool.call.started':
      case 'tool.completed':
      case 'tool.failed':
      case 'tool.finished': {
        const toolName = String(payload.tool || '').trim();
        if (shouldHideChatWindowTool(toolName)) break;
        const key = liveWindowKey('tool', event.run_id, event.agent_id, event.session_id, payload.id, toolName);
        const item = upsert(key, () => ({
          type: 'tool_call',
          agent_id: event.agent_id || appState.chat.agentID,
          session_id: event.session_id || appState.chat.sessionID,
          run_id: event.run_id || '',
          tool_call_id: payload.id || '',
          tool: toolName || 'tool',
          status: 'running',
          summary: summarizeToolPayload(payload),
          timestamp,
        }));
        item.status = (name === 'tool.failed' || payload.error) ? 'failed' : (name === 'tool.call.started' ? 'running' : 'success');
        item.summary = item.summary || summarizeToolPayload(payload);
        break;
      }
      default:
        break;
    }
  });

  return items.sort((left, right) => new Date(left.timestamp || 0).getTime() - new Date(right.timestamp || 0).getTime());
}

function summarizeAgentCallPayload(payload) {
  if (!payload) return '等待委派详情';
  if (Array.isArray(payload.tasks) && payload.tasks.length) {
    return `${payload.tasks.length} 个并行任务`;
  }
  const agent = payload.target_agent || 'unknown-agent';
  const task = payload.task || payload.summary || payload.error || '未提供任务摘要';
  return `${agent} · ${truncate(task, 88)}`;
}

function summarizeMemoryPayload(payload) {
  if (!payload) return '等待 memory 召回信息';
  const entries = Array.isArray(payload.entries) ? payload.entries : [];
  const query = payload.query || '未记录查询';
  if (!entries.length) {
    return `query: ${truncate(query, 88)} · 0 命中`;
  }
  const labels = entries.slice(0, 2).map((entry) => entry.title || entry.id || '--').join(' · ');
  const suffix = entries.length > 2 ? ` +${entries.length - 2}` : '';
  return `query: ${truncate(query, 72)} · ${entries.length} 命中 · ${truncate(labels, 72)}${suffix}`;
}

function summarizeLLMRetryPayload(payload) {
  if (!payload) return 'LLM 调用失败，准备重试。';
  const attempt = Number(payload.attempt || 0);
  const maxAttempts = Number(payload.max_attempts || 0);
  const waitMS = Number(payload.wait_ms || 0);
  const stage = payload.stage || 'request';
  const error = truncate(payload.error || '未知错误', 96);
  const attemptLabel = attempt > 0 && maxAttempts > 0 ? `第 ${attempt}/${maxAttempts} 次尝试` : '准备重试';
  const waitLabel = waitMS > 0 ? `，${(waitMS / 1000).toFixed(waitMS >= 1000 ? 1 : 2)}s 后重试` : '';
  return `${attemptLabel}${waitLabel} · ${stage} · ${error}`;
}

function renderJSONBlock(value) {
  if (value === undefined || value === null || value === '') return '';
  return `<pre>${escapeHTML(safeJSON(value))}</pre>`;
}

function renderMemoryRecallList(entries) {
  if (!Array.isArray(entries) || !entries.length) return '';
  return `
    <div class="note-list">
      ${entries.map((entry) => {
        const id = String(entry.id || '');
        const href = withAuthQuery(`/api/memory/item?id=${encodeURIComponent(id)}`);
        const matched = Array.isArray(entry.matched_terms) && entry.matched_terms.length
          ? ` · matched: ${entry.matched_terms.join(', ')}`
          : '';
        return `
          <div class="meta-note compact">
            <strong>${escapeHTML(entry.title || id || 'memory')}</strong>
            <div class="muted tiny">${escapeHTML(entry.layer || 'memory')}${escapeHTML(matched)}</div>
            <div class="muted tiny"><code>${escapeHTML(id)}</code></div>
            <a class="inline-link" href="${escapeHTML(href)}" target="_blank" rel="noreferrer">查看条目 JSON</a>
          </div>
        `;
      }).join('')}
    </div>
  `;
}

function renderFieldList(fields) {
  if (!fields || !fields.length) return '';
  return `
    <div class="field-grid">
      ${fields.map((field) => `
        <article class="field-card">
          <div class="toolbar">
            <strong>${escapeHTML(field.name)}</strong>
            <span class="pill">${escapeHTML(field.type)}</span>
            ${field.required ? '<span class="pill warn">required</span>' : ''}
          </div>
          <p class="muted">${escapeHTML(field.description || '')}</p>
        </article>
      `).join('')}
    </div>
  `;
}

function rememberAgentInventory(payload) {
  appState.inventory = {
    agents: Array.isArray(payload?.agents) ? payload.agents : [],
    sharedSkills: Array.isArray(payload?.shared_skills) ? payload.shared_skills : [],
    systemSkills: Array.isArray(payload?.system_skills) ? payload.system_skills : [],
    notes: Array.isArray(payload?.notes) ? payload.notes : [],
  };
  return appState.inventory;
}

function findAgent(agentID) {
  return appState.inventory.agents.find((agent) => agent.id === agentID) || null;
}

function limitItems(items, max = 6) {
  if (!Array.isArray(items)) return [];
  return items.slice(0, max);
}

function renderScopePills(items, max = 6, kind = 'skill') {
  if (!Array.isArray(items) || !items.length) {
    return '<span class="pill ghost-pill">None</span>';
  }
  const visible = limitItems(items, max);
  const more = items.length - visible.length;
  return `
    ${visible.map((item) => {
      const label = kind === 'tool'
        ? `${item.name}${item.tier ? ` · ${item.tier}` : ''}`
        : `${item.scope ? `${item.scope} · ` : ''}${item.name}`;
      return `<span class="pill scope-pill ${escapeHTML(item.scope || item.tier || 'neutral')}">${escapeHTML(label)}</span>`;
    }).join('')}
    ${more > 0 ? `<span class="pill ghost-pill">+${escapeHTML(more)} more</span>` : ''}
  `;
}

function renderCapabilityMetric(label, value) {
  return `
    <div class="mini-stat compact">
      <span class="mini-stat-label">${escapeHTML(label)}</span>
      <strong>${escapeHTML(value)}</strong>
    </div>
  `;
}

function renderAgentCapabilityCard(agent, compact = false) {
  if (!agent) return '';
  const effectiveSkills = agent.skills?.effective || [];
  const sharedSkills = agent.skills?.shared || [];
  const privateSkills = agent.skills?.private || [];
  const tools = agent.tools || [];
  const direct = agent.direct_request || {};
  const toolPolicy = agent.tool_policy || {};

  return `
    <article class="agent-card ${agent.entry ? 'entry' : ''}">
      <div class="toolbar">
        <div>
          <div class="eyebrow subtle">${escapeHTML(agent.entry ? 'ENTRY AGENT' : 'SPECIALIST AGENT')}</div>
          <strong>${escapeHTML(agent.name || agent.id)}</strong>
          <div class="muted tiny"><code>${escapeHTML(agent.id)}</code> · ${escapeHTML(agent.model || '--')}</div>
        </div>
        <div class="toolbar">
          ${agent.entry ? '<span class="pill accent-pill">entry</span>' : '<span class="pill">direct</span>'}
          <span class="pill">${escapeHTML(direct.recommended ? 'recommended' : 'specialist')}</span>
          <span class="pill">${escapeHTML(toolPolicy.mode || 'all')}</span>
        </div>
      </div>
      ${agent.description ? `<p class="muted">${escapeHTML(agent.description)}</p>` : ''}
      <div class="mini-stat-list capability-stats">
        ${renderCapabilityMetric('Tools', tools.length)}
        ${renderCapabilityMetric('Skills', effectiveSkills.length)}
        ${renderCapabilityMetric('Shared', sharedSkills.length)}
        ${renderCapabilityMetric('Private', privateSkills.length)}
      </div>
      ${agent.tags?.length ? `<div class="tag-row">${agent.tags.map((tag) => `<span class="pill">${escapeHTML(tag)}</span>`).join('')}</div>` : ''}
      ${direct.warning ? `<div class="meta-note compact warning-note">${escapeHTML(direct.warning)}</div>` : ''}
      <div class="capability-grid">
        <div class="capability-block">
          <div class="doc-subhead">Effective Skills</div>
          <div class="tag-row">${renderScopePills(effectiveSkills, compact ? 4 : 8)}</div>
        </div>
        <div class="capability-block">
          <div class="doc-subhead">Tool Surface</div>
          <div class="tag-row">${renderScopePills(tools, compact ? 6 : 10, 'tool')}</div>
        </div>
      </div>
      ${!compact && direct.notes?.length ? `<div class="note-list">${direct.notes.map((note) => `<div class="meta-note compact">${escapeHTML(note)}</div>`).join('')}</div>` : ''}
    </article>
  `;
}

function renderSharedSkillDeck(title, description, skills) {
  if (!Array.isArray(skills) || !skills.length) {
    return `
      <div class="list-item">
        <strong>${escapeHTML(title)}</strong>
        <p class="muted">${escapeHTML(description)}</p>
        <div class="muted tiny">当前没有可见技能。</div>
      </div>
    `;
  }
  return `
    <div class="list-item">
      <strong>${escapeHTML(title)}</strong>
      <p class="muted">${escapeHTML(description)}</p>
      <div class="tag-row">${renderScopePills(skills, 10)}</div>
    </div>
  `;
}

function renderOverviewLinks() {
  const cards = [
    { href: '/chat', title: '会话工作台', desc: '创建 / 切换 session，直接聊天并追踪活动流。', badge: 'Chat' },
    { href: '/ui/runs', title: '运行树', desc: '查看 run tree 和工具调用历史。', badge: 'Runs' },
    { href: '/ui/api', title: 'API 导航', desc: '按工程接入方式阅读 HTTP、SSE 与事件契约文档。', badge: 'API' },
  ];
  return cards.map((card) => `
    <a class="quick-link-card" href="${escapeHTML(withAuthQuery(card.href))}">
      <span class="pill">${escapeHTML(card.badge)}</span>
      <strong>${escapeHTML(card.title)}</strong>
      <p class="muted">${escapeHTML(card.desc)}</p>
    </a>
  `).join('');
}

async function renderOverview() {
  let overview = {};
  let inventoryPayload = {};
  try {
    [{ overview }, inventoryPayload] = await Promise.all([
      fetchJSON('/api/runtime/overview'),
      fetchJSON('/api/agents'),
    ]);
  } catch (err) {
    showFlash('加载概览数据失败: ' + (err.message || String(err)), true);
  }

  // Ensure safe defaults for all overview properties
  overview = overview || {};
  const counts = overview.counts || {};
  const gateway = overview.gateway || {};
  const channels = overview.channels || [];
  const recentRuns = overview.recent_runs || [];
  const recentTasks = overview.recent_tasks || [];
  const jobs = overview.jobs || [];

  const inventory = rememberAgentInventory(inventoryPayload);

  const stats = [
    ['Agents', counts.agents || 0],
    ['Channels', counts.channels || 0],
    ['Runs', counts.runs || 0],
    ['Tasks', counts.tasks || 0],
  ].map(([label, value]) => `
    <div class="stat">
      <div class="label">${label}</div>
      <div class="value">${escapeHTML(value)}</div>
    </div>
  `).join('');

  const channelsHtml = channels.length ? channels.map((channel) => `
    <div class="surface-card">
      <div class="toolbar">
        <strong>${escapeHTML(channel.name)}</strong>
        ${statusPill(channel.status)}
        <span class="pill">${escapeHTML(channel.category)}</span>
      </div>
      <p class="muted">${escapeHTML(channel.description)}</p>
    </div>
  `).join('') : emptyState('当前没有已注册通道。');

  const runsHtml = recentRuns.length ? `
    <table class="table">
      <thead><tr><th>ID</th><th>Agent</th><th>Status</th><th>Session</th><th>Started</th></tr></thead>
      <tbody>
        ${recentRuns.map((run) => `
          <tr>
            <td><code>${escapeHTML(run.id)}</code></td>
            <td>${escapeHTML(run.agent_id)}</td>
            <td>${statusPill(run.status)}</td>
            <td>${escapeHTML(run.session_id)}</td>
            <td>${escapeHTML(formatDateTime(run.started_at))}</td>
          </tr>
        `).join('')}
      </tbody>
    </table>
  ` : emptyState('还没有运行记录。');

  const tasksHtml = recentTasks.length ? recentTasks.map((task) => `
    <div class="list-item">
      <div class="toolbar">
        <strong>${escapeHTML(task.id)}</strong>
        ${statusPill(task.status)}
      </div>
      <p class="muted">${escapeHTML(task.input || task.summary || 'No summary')}</p>
    </div>
  `).join('') : emptyState('当前没有后台任务。');

  const jobsHtml = jobs.length ? jobs.map((job) => `
    <div class="list-item">
      <div class="toolbar">
        <strong>${escapeHTML(job.name)}</strong>
        ${statusPill(job.paused ? 'paused' : 'running')}
      </div>
      <p class="muted">${escapeHTML(job.schedule)} · ${escapeHTML(job.prompt)}</p>
    </div>
  `).join('') : emptyState('当前没有计划任务。');

  const agents = inventory.agents.length ? `
    <div class="agent-matrix">
      ${inventory.agents.map((agent) => renderAgentCapabilityCard(agent)).join('')}
    </div>
  ` : emptyState('当前没有可用 agent。');

  const skillDeck = `
    <div class="list">
      ${renderSharedSkillDeck('Shared Skills', '面向所有继承共享技能的 agent。', inventory.sharedSkills)}
      ${renderSharedSkillDeck('System Skills', '系统级技能，会在被启用后进入 agent 能力面。', inventory.systemSkills)}
      ${inventory.notes?.length ? inventory.notes.map((note) => `<div class="meta-note compact">${escapeHTML(note)}</div>`).join('') : ''}
    </div>
  `;

  content.innerHTML = `
    <section class="hero">
      <div class="hero-grid">
        <div>
          <div class="eyebrow">UNIFIED CONTROL SURFACE</div>
          <h2>${escapeHTML(overview.project_name || 'AnyAI Project')}</h2>
          <p class="muted">版本 ${escapeHTML(overview.version || 'dev')} · ${escapeHTML(gateway.host || 'localhost')}:${escapeHTML(gateway.port || '8080')} · 鉴权 ${gateway.auth_required ? '开启' : '关闭'}</p>
          <div class="stats">${stats}</div>
        </div>
        <div class="panel panel-soft">
          <h3>快速入口</h3>
          <div class="quick-link-grid">${renderOverviewLinks()}</div>
        </div>
      </div>
    </section>

    <section class="grid cards-2">
      <div class="panel">
        <h3>通道面</h3>
        <div class="grid">${channelsHtml}</div>
      </div>
      <div class="panel">
        <h3>计划任务</h3>
        <div class="list">${jobsHtml}</div>
      </div>
    </section>

    <section class="grid cards-2">
      <div class="panel">
        <h3>最近运行</h3>
        ${runsHtml}
      </div>
      <div class="panel">
        <h3>最近任务</h3>
        <div class="list">${tasksHtml}</div>
      </div>
    </section>

    <section class="grid cards-2">
      <div class="panel">
        <h3>Agent 拓扑</h3>
        ${agents}
      </div>
      <div class="panel">
        <h3>共享技能与接入提示</h3>
        ${skillDeck}
      </div>
    </section>
  `;
}

async function renderChat() {
  const inventory = rememberAgentInventory(await fetchJSON('/api/agents'));
  const agents = inventory.agents || [];
  appState.chat.agents = agents;

  if (!agents.length) {
    content.innerHTML = emptyState('当前没有可用 agent，请先检查配置或启动入口 agent。');
    return;
  }

  const storedAgent = safeStorageGet('anyai.chat.agent');
  const preferredAgent = agents.some((agent) => agent.id === storedAgent) ? storedAgent : agents[0].id;
  appState.chat.agentID = preferredAgent;
  appState.chat.sessionID = safeStorageGet(`anyai.chat.session.${preferredAgent}`);
  appState.chat.history = [];
  resetChatRuntimeState('idle');

  content.innerHTML = `
    <section class="chat-shell chat-shell-compact">
      <section class="panel transcript-panel transcript-panel-compact">
        <div class="chat-session-toolbar">
          <label class="stack-field chat-agent-field chat-toolbar-field">Agent
            <select id="chat-agent">
              ${agents.map((agent) => `<option value="${escapeHTML(agent.id)}">${escapeHTML(agent.name || agent.id)} (${escapeHTML(agent.id)})</option>`).join('')}
            </select>
          </label>
          <div id="chat-session-list" class="chat-session-switcher"></div>
          <div class="toolbar chat-session-actions">
            <button id="chat-new-session">新建会话</button>
            <button id="chat-refresh-sessions" class="ghost">刷新</button>
          </div>
        </div>
        <div id="chat-session-head"></div>
        <div id="chat-transcript-shell" class="chat-transcript-shell chat-transcript-shell-compact">
          <div id="chat-transcript" class="transcript"></div>
        </div>
        <form id="chat-form" class="composer">
          <div id="chat-runtime-note-line" class="composer-runtime-line" title="当前运行状态摘要">
            <span class="eyebrow subtle">RUNTIME NOTE</span>
            <span id="chat-runtime-note" class="composer-runtime-text">state idle   0 live   0 done   0 failed   0 events</span>
          </div>
          <label class="stack-field composer-input-field">
            <textarea id="chat-input" rows="3" aria-label="发送给当前 Session" placeholder="直接输入任务、需求或提问。发送后会继续保留在当前 session。"></textarea>
          </label>
          <div class="composer-actions">
            <div class="muted" id="chat-composer-note">准备就绪</div>
            <button type="submit" id="chat-send">发送消息</button>
          </div>
        </form>
      </section>
    </section>
  `;

  const agentSelect = document.getElementById('chat-agent');
  const promptInput = document.getElementById('chat-input');
  agentSelect.value = preferredAgent;
  promptInput.value = safeStorageGet('anyai.chat.draft');
  updateChatRuntimeNote();

  agentSelect.addEventListener('change', async () => {
    clearFlash();
    closeChatSource();
    appState.chat.agentID = agentSelect.value;
    safeStorageSet('anyai.chat.agent', appState.chat.agentID);
    appState.chat.sessionID = safeStorageGet(`anyai.chat.session.${appState.chat.agentID}`);
    appState.chat.history = [];
    resetChatRuntimeState('idle');
    renderChatTopStats();
    await loadChatSessions(appState.chat.agentID, appState.chat.sessionID);
  });

  promptInput.addEventListener('input', () => {
    safeStorageSet('anyai.chat.draft', promptInput.value);
    updateChatSnippet();
  });

  document.getElementById('chat-refresh-sessions').addEventListener('click', async () => {
    clearFlash();
    await loadChatSessions(appState.chat.agentID, appState.chat.sessionID);
  });

  document.getElementById('chat-new-session').addEventListener('click', async () => {
    clearFlash();
    await createChatSession();
  });

  document.getElementById('chat-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    await sendChatPrompt();
  });

  renderChatTopStats();
  await loadChatSessions(preferredAgent, appState.chat.sessionID);
}

async function loadChatSessions(agentID, preferredSession = '') {
  cancelChatHistorySync();
  const previousSessionID = appState.chat.sessionID;
  const payload = await fetchJSON(`/api/sessions/${encodeURIComponent(agentID)}`);
  const sessions = (payload.sessions || []).map((session) => ({
    ...session,
    id: session.id || '',
  })).sort((left, right) => {
    const leftTime = new Date(left.lastActivity || left.createdAt || 0).getTime();
    const rightTime = new Date(right.lastActivity || right.createdAt || 0).getTime();
    return rightTime - leftTime;
  });

  appState.chat.sessions = sessions;
  const requested = preferredSession && sessions.some((session) => session.id === preferredSession) ? preferredSession : '';
  const nextSessionID = requested || sessions[0]?.id || '';
  if (previousSessionID && previousSessionID !== nextSessionID) {
    resetChatPendingState();
  }
  appState.chat.sessionID = nextSessionID;
  safeStorageSet(`anyai.chat.session.${agentID}`, nextSessionID);
  renderChatSessions();
  renderChatTopStats();
  updateChatSnippet();

  if (!nextSessionID) {
    appState.chat.history = [];
    resetChatPendingState();
    renderChatSessionHead();
    renderChatTranscript(true);
    renderChatActivity();
    return;
  }
  await loadChatHistory(agentID, nextSessionID);
}

async function createChatSession() {
  const agentID = appState.chat.agentID;
  const response = await fetchJSON(`/api/sessions/${encodeURIComponent(agentID)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: generateSessionName() }),
  });
  const sessionID = response.session.id || '';
  showFlash(`已创建会话 ${sessionID}`);
  await loadChatSessions(agentID, sessionID);
}

async function loadChatHistory(agentID, sessionID) {
  const payload = await fetchJSON(`/api/sessions/${encodeURIComponent(agentID)}/${encodeURIComponent(sessionID)}`);
  safeStorageSet(`anyai.chat.session.${agentID}`, sessionID);
  applyChatHistory(payload.session.events || [], true);
}

function reconcileChatPendingEntriesWithHistory() {
  if (!appState.chat.pendingEntries.length) return;

  const baseline = Math.max(0, appState.chat.pendingHistoryBaseline || 0);
  const historySincePending = appState.chat.history.slice(baseline);
  const pendingUserText = String(appState.chat.pendingUserText || '').trim();
  const hasPendingUserEcho = pendingUserText && historySincePending.some((entry) =>
    entry?.type === 'message'
      && entry?.role === 'user'
      && String(entry?.text || '').trim() === pendingUserText
  );
  const hasAssistantHistory = historySincePending.some((entry) =>
    entry?.type === 'message' && entry?.role === 'assistant'
  );

  if (!hasPendingUserEcho && !hasAssistantHistory) return;

  let removedEmptyAssistant = false;
  appState.chat.pendingEntries = appState.chat.pendingEntries.filter((entry) => {
    if (entry?.type !== 'message') return true;
    if (entry.role === 'user' && hasPendingUserEcho) {
      return false;
    }
    if (entry.role === 'assistant' && hasAssistantHistory && !String(entry.text || '').trim()) {
      removedEmptyAssistant = true;
      return false;
    }
    return true;
  });

  if (!appState.chat.pendingEntries.some((entry) => entry?.type === 'message' && entry?.role === 'user')) {
    appState.chat.pendingUserText = '';
  }
  if (removedEmptyAssistant || !appState.chat.pendingEntries.length) {
    appState.chat.pendingHistoryBaseline = appState.chat.history.length;
  }
}

function applyChatHistory(history, forceBottom = false) {
  appState.chat.history = decorateChatHistoryEntries(history);
  reconcileChatPendingEntriesWithHistory();
  renderChatSessionHead();
  renderChatTranscript(forceBottom);
  renderChatActivity();
  renderChatTopStats();
  updateChatSnippet();
}

function renderChatTopStats() {
  const host = document.getElementById('chat-top-stats');
  if (!host) return;
  const displayTree = getChatDisplayRunTree();
  const runtime = getChatRuntimeStatusMeta(displayTree);
  const currentSession = appState.chat.sessionID || '--';
  host.innerHTML = `
    <div class="mini-stat compact">
      <span class="mini-stat-label">Session</span>
      <strong>${escapeHTML(truncate(currentSession, 24))}</strong>
    </div>
    <div class="mini-stat compact">
      <span class="mini-stat-label">Runtime</span>
      ${statusPill(runtime.key, runtime.label)}
    </div>
    <div class="mini-stat compact">
      <span class="mini-stat-label">Agent</span>
      <strong>${escapeHTML(appState.chat.agentID || '--')}</strong>
    </div>
  `;
}

function renderChatSessions() {
  const list = document.getElementById('chat-session-list');
  if (!list) return;

  if (!appState.chat.sessions.length) {
    list.innerHTML = `
      <label class="stack-field chat-session-field chat-toolbar-field">Session
        <select id="chat-session-select" disabled>
          <option value="">暂无会话</option>
        </select>
      </label>
      <div class="chat-session-summary empty">
        <strong>等待首条消息</strong>
        <span>发送后会自动创建新的 session。</span>
      </div>
    `;
    return;
  }

  const activeSession = appState.chat.sessions.find((session) => session.id === appState.chat.sessionID) || appState.chat.sessions[0];
  list.innerHTML = `
    <label class="stack-field chat-session-field chat-toolbar-field">Session
      <select id="chat-session-select">
        ${appState.chat.sessions.map((session) => `
          <option value="${escapeHTML(session.id)}" ${session.id === appState.chat.sessionID ? 'selected' : ''}>
            ${escapeHTML(session.id)}
          </option>
        `).join('')}
      </select>
    </label>
    <div class="chat-session-summary" title="${escapeHTML(activeSession?.id || '')}">
      <strong>${escapeHTML(truncate(activeSession?.id || '--', 30))}</strong>
      <span>${escapeHTML(activeSession ? `${activeSession.entryCount || 0} 条 · 最近活动 ${formatRelativeTime(activeSession.lastActivity || activeSession.createdAt)}` : '等待历史加载')}</span>
    </div>
  `;

  document.getElementById('chat-session-select')?.addEventListener('change', async (event) => {
    const nextSessionID = String(event.target?.value || '').trim();
    if (!nextSessionID || nextSessionID === appState.chat.sessionID) return;
    clearFlash();
    closeChatSource();
    resetChatRuntimeState('idle');
    appState.chat.sessionID = nextSessionID;
    await loadChatHistory(appState.chat.agentID, appState.chat.sessionID);
    renderChatSessions();
  });
}

function renderChatSessionHead() {
  const host = document.getElementById('chat-session-head');
  if (!host) return;
  const activeSession = appState.chat.sessions.find((session) => session.id === appState.chat.sessionID);
  const runtime = getChatRuntimeStatusMeta();
  const note = appState.chat.sessionID
    ? `Agent ${appState.chat.agentID || '--'} · ${activeSession ? `${activeSession.entryCount || 0} 条记录` : '等待历史加载'}${activeSession?.lastActivity ? ` · 最后活动 ${formatRelativeTime(activeSession.lastActivity || activeSession.createdAt)}` : ''}`
    : '还没有选中的 Session';

  host.innerHTML = `
    <div class="chat-session-head">
      <div class="chat-session-title">
        <div class="eyebrow">SESSION WINDOW</div>
        <h3>${escapeHTML(appState.chat.sessionID || '未选择 Session')}</h3>
        <p class="muted">${escapeHTML(note)}</p>
      </div>
      <div class="chat-session-badges">
        ${statusPill(runtime.key, runtime.label)}
        ${activeSession ? `<span class="pill">${escapeHTML(activeSession.entryCount || 0)} 条</span>` : ''}
        ${activeSession?.lastActivity ? `<span class="pill ghost-pill">${escapeHTML(formatDateTime(activeSession.lastActivity || activeSession.createdAt))}</span>` : ''}
      </div>
    </div>
  `;
}

function renderTranscriptEntry(entry) {
  const agentLabel = displayChatAgentLabel(entry);
  switch (entry.type) {
    case 'message':
      return `
        <article class="message-bubble ${escapeHTML(entry.role || 'assistant')} ${entry.pending ? 'pending' : ''} ${entry.agent_id ? 'agent-message' : ''}">
          <div class="message-meta">
            <span>${escapeHTML(agentLabel)}</span>
            ${entry.status ? statusPill(entry.status === 'success' ? 'completed' : entry.status, entry.status === 'success' ? 'done' : entry.status) : ''}
            ${entry.pending ? '<span class="pill">streaming</span>' : ''}
          </div>
          <div class="message-text message-markdown markdown-body">${renderMarkdown(entry.text || (entry.pending ? '正在生成回复…' : ''))}</div>
          ${entry.images && entry.images.length ? `<div class="muted tiny">附带 ${escapeHTML(entry.images.length)} 张图片</div>` : ''}
        </article>
      `;
    case 'tool_call':
      return `
        <article class="transcript-card tool-runtime-card">
          <div class="toolbar">
            <div>
              <span class="eyebrow subtle">${escapeHTML(agentLabel)}</span>
              <strong>${escapeHTML(entry.tool || 'unknown-tool')}</strong>
            </div>
            ${statusPill(
              entry.status === 'success' ? 'completed' : (entry.status || (entry.pending ? 'running' : 'idle')),
              entry.status === 'success' ? 'done' : (entry.status || (entry.pending ? 'running' : 'idle')),
            )}
          </div>
          ${entry.summary ? `<div class="tool-runtime-summary">${escapeHTML(truncate(entry.summary, 88))}</div>` : ''}
        </article>
      `;
    case 'meta':
    default:
      return '';
  }
}

function displayChatAgentLabel(entry) {
  if (entry?.role === 'user') return 'user';
  const agentID = String(entry?.agent_id || '').trim();
  if (!agentID) return String(entry?.role || 'assistant').trim() || 'assistant';
  const agent = findAgent(agentID);
  return agent?.name || agentID;
}

function buildChatRuntimeStatusEntry() {
  const counts = { live: 0, done: 0, failed: 0 };
  const stateByKey = new Map();
  appState.chat.runEvents.forEach((event) => {
    const key = `${event.run_id || ''}:${event.agent_id || ''}:${event.session_id || ''}`;
    if (!key.trim()) return;
    const name = String(event.name || '').trim();
    if (name === 'run.failed' || name === 'run.aborted' || name === 'agent.call.failed') {
      stateByKey.set(key, 'failed');
      return;
    }
    if (name === 'run.completed' || name === 'agent.call.completed') {
      stateByKey.set(key, 'done');
      return;
    }
    stateByKey.set(key, 'live');
  });

  stateByKey.forEach((state) => {
    if (state === 'failed') {
      counts.failed += 1;
    } else if (state === 'done') {
      counts.done += 1;
    } else {
      counts.live += 1;
    }
  });

  return {
    type: 'meta',
    text: `state ${appState.chat.runStatus || 'idle'}   ${counts.live} live   ${counts.done} done   ${counts.failed} failed   ${appState.chat.runEvents.length} events`,
  };
}

function chatRuntimeNoteText() {
  return buildChatRuntimeStatusEntry().text || 'state idle   0 live   0 done   0 failed   0 events';
}

function updateChatRuntimeNote() {
  const line = document.getElementById('chat-runtime-note-line');
  const note = document.getElementById('chat-runtime-note');
  if (!note) return;
  const text = chatRuntimeNoteText();
  const runtime = getChatRuntimeStatusMeta();
  if (line) {
    line.dataset.state = String(runtime.key || appState.chat.runStatus || 'idle').trim().toLowerCase() || 'idle';
  }
  note.textContent = text;
  note.title = text;
}

function shouldHideChatWindowTool(toolName) {
  const tool = String(toolName || '').trim();
  return !tool || tool === 'goal_complete' || tool === 'await_user_input';
}

function liveWindowKey(...parts) {
  return parts.map((part) => String(part || '').trim()).filter(Boolean).join(':');
}

function chatWindowEntryKey(entry) {
  if (!entry || typeof entry !== 'object') return '';
  if (entry._key) return String(entry._key).trim();
  if (entry.type === 'tool_call') {
    return liveWindowKey('tool', entry.run_id, entry.agent_id, entry.session_id, entry.tool_call_id, entry.tool);
  }
  if (entry.type === 'message' && entry.role !== 'user' && entry.agent_id) {
    return liveWindowKey('message', entry.run_id, entry.agent_id, entry.session_id);
  }
  return '';
}

function buildChatLiveWindowEntries() {
  const items = [];
  const index = new Map();
  const persisted = new Set((appState.chat.history || []).map(chatWindowEntryKey).filter(Boolean));

  const upsert = (key, factory) => {
    if (index.has(key)) return index.get(key);
    const item = factory();
    item._key = key;
    items.push(item);
    index.set(key, item);
    return item;
  };

  appState.chat.runEvents.forEach((event) => {
    const payload = event.payload || {};
    const name = String(event.name || '').trim();

    switch (name) {
      case 'text.delta': {
        const key = liveWindowKey('message', event.run_id, event.agent_id, event.session_id);
        const item = upsert(key, () => ({
          type: 'message',
          role: 'assistant',
          agent_id: event.agent_id || appState.chat.agentID,
          session_id: event.session_id || appState.chat.sessionID,
          run_id: event.run_id || '',
          text: '',
          pending: true,
          timestamp: event.timestamp || new Date().toISOString(),
        }));
        item.text += payload.text || '';
        item.pending = true;
        break;
      }
      case 'tool.call.started':
      case 'tool.called': {
        if (shouldHideChatWindowTool(payload.tool)) break;
        const key = liveWindowKey('tool', event.run_id, event.agent_id, event.session_id, payload.id, payload.tool);
        const item = upsert(key, () => ({
          type: 'tool_call',
          agent_id: event.agent_id || appState.chat.agentID,
          session_id: event.session_id || appState.chat.sessionID,
          run_id: event.run_id || '',
          tool_call_id: payload.id || '',
          tool: payload.tool || 'tool',
          status: 'running',
          summary: summarizeToolPayload(payload),
          timestamp: event.timestamp || new Date().toISOString(),
        }));
        item.status = 'running';
        item.summary = item.summary || summarizeToolPayload(payload);
        break;
      }
      case 'tool.completed':
      case 'tool.failed':
      case 'tool.finished': {
        if (shouldHideChatWindowTool(payload.tool)) break;
        const key = liveWindowKey('tool', event.run_id, event.agent_id, event.session_id, payload.id, payload.tool);
        const item = upsert(key, () => ({
          type: 'tool_call',
          agent_id: event.agent_id || appState.chat.agentID,
          session_id: event.session_id || appState.chat.sessionID,
          run_id: event.run_id || '',
          tool_call_id: payload.id || '',
          tool: payload.tool || 'tool',
          status: payload.error ? 'failed' : 'success',
          summary: summarizeToolPayload(payload),
          timestamp: event.timestamp || new Date().toISOString(),
        }));
        item.status = payload.error ? 'failed' : 'success';
        item.summary = item.summary || summarizeToolPayload(payload);
        break;
      }
      case 'agent.call.completed':
      case 'agent.call.failed': {
        const target = String(payload.target_agent || payload.agent || '').trim();
        const text = String(payload.summary || payload.error || '').trim();
        if (!target || !text) break;
        const key = liveWindowKey('agent', event.run_id, target, payload.session_id || payload.id || payload.task_id);
        const item = upsert(key, () => ({
          type: 'message',
          role: 'assistant',
          agent_id: target,
          session_id: payload.session_id || '',
          run_id: payload.run_id || event.run_id || '',
          text,
          status: payload.error || name === 'agent.call.failed' ? 'failed' : 'success',
          timestamp: event.timestamp || new Date().toISOString(),
        }));
        item.text = item.text || text;
        item.status = payload.error || name === 'agent.call.failed' ? 'failed' : 'success';
        item.pending = false;
        break;
      }
      case 'run.completed':
      case 'run.failed':
      case 'run.aborted': {
        items.forEach((item) => {
          if (item.type === 'message' && item.pending && item.run_id === event.run_id) {
            item.pending = false;
          }
        });
        break;
      }
      default:
        break;
    }
  });

  return items
    .filter((item) => !persisted.has(chatWindowEntryKey(item)))
    .sort((left, right) => new Date(left.timestamp || 0).getTime() - new Date(right.timestamp || 0).getTime());
}

function renderChatTranscript(forceBottom = false) {
  const host = document.getElementById('chat-transcript');
  if (!host) return;
  const previousScrollTop = host.scrollTop;
  const previousScrollHeight = host.scrollHeight;
  const previousClientHeight = host.clientHeight;
  const wasNearBottom = (previousScrollHeight - (previousScrollTop + previousClientHeight)) < 56;
  const entries = [...appState.chat.history, ...appState.chat.pendingEntries];
  const liveEntries = buildChatLiveWindowEntries();
  updateChatRuntimeNote();
  if (!entries.length && !liveEntries.length) {
    host.innerHTML = emptyState(
      appState.chat.sessionID
        ? '这个 Session 还没有消息。现在就发送第一条消息，会话窗口会同步展开运行状态。'
        : '先创建一个 Session，或者直接输入消息并发送，系统会自动为当前 agent 创建会话。'
    );
    return;
  }

  host.innerHTML = [
    ...entries.map(renderTranscriptEntry),
    ...liveEntries.map(renderTranscriptEntry),
  ].filter(Boolean).join('');
  addCopyButtons(host);
  requestAnimationFrame(() => {
    if (forceBottom || wasNearBottom) {
      host.scrollTop = host.scrollHeight;
      return;
    }
    const maxScrollTop = Math.max(0, host.scrollHeight - host.clientHeight);
    host.scrollTop = Math.min(previousScrollTop, maxScrollTop);
  });
}

function closeChatSource() {
  if (appState.chat.activeSource) {
    appState.chat.activeSource.close();
    appState.chat.activeSource = null;
  }
}

function scheduleChatHistorySync(agentID, sessionID, immediate = false) {
  if (!agentID || !sessionID) return;
  if (appState.chat.historySyncTimer) {
    clearTimeout(appState.chat.historySyncTimer);
  }
  const token = ++appState.chat.historySyncToken;
  const delay = immediate ? 0 : 160;
  appState.chat.historySyncTimer = window.setTimeout(async () => {
    try {
      const payload = await fetchJSON(`/api/sessions/${encodeURIComponent(agentID)}/${encodeURIComponent(sessionID)}`);
      if (token !== appState.chat.historySyncToken) return;
      if (appState.chat.agentID !== agentID || appState.chat.sessionID !== sessionID) return;
      applyChatHistory(payload.session.events || []);
    } catch (error) {
      if (token !== appState.chat.historySyncToken) return;
      console.warn('chat history sync failed', error);
    } finally {
      if (token === appState.chat.historySyncToken) {
        appState.chat.historySyncTimer = 0;
      }
    }
  }, delay);
}

function scheduleChatRunTreeSync(runID, immediate = false) {
  if (!runID) return;
  if (appState.chat.runTreeSyncTimer) {
    clearTimeout(appState.chat.runTreeSyncTimer);
  }
  const token = ++appState.chat.runTreeSyncToken;
  appState.chat.runTreeLoading = true;
  const delay = immediate ? 0 : 160;
  appState.chat.runTreeSyncTimer = window.setTimeout(async () => {
    try {
      const tree = await fetchRunTree(runID);
      if (token !== appState.chat.runTreeSyncToken) return;
      appState.chat.runTree = tree;
    } finally {
      if (token === appState.chat.runTreeSyncToken) {
        appState.chat.runTreeLoading = false;
        appState.chat.runTreeSyncTimer = 0;
        renderChatActivity();
      }
    }
  }, delay);
}

async function fetchEventStream(url, options, onEvent) {
  const response = await fetch(withAuthQuery(url), options);
  if (!response.ok) {
    const raw = await response.text();
    let message = raw || `Request failed: ${response.status}`;
    try {
      const payload = JSON.parse(raw);
      message = payload.error || message;
    } catch {
      // keep raw text
    }
    throw new Error(message);
  }

  if (!response.body) return;

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let currentEvent = 'message';
  let dataLines = [];

  const flushEvent = async () => {
    if (!dataLines.length) return;
    const raw = dataLines.join('\n');
    let payload = raw;
    try {
      payload = JSON.parse(raw);
    } catch {
      // keep raw string payload
    }
    await onEvent(currentEvent, payload);
    currentEvent = 'message';
    dataLines = [];
  };

  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value || new Uint8Array(), { stream: !done });

    let lineBreak = buffer.search(/\r?\n/);
    while (lineBreak >= 0) {
      const line = buffer.slice(0, lineBreak);
      const separatorLength = buffer[lineBreak] === '\r' && buffer[lineBreak + 1] === '\n' ? 2 : 1;
      buffer = buffer.slice(lineBreak + separatorLength);

      if (!line) {
        await flushEvent();
      } else if (line.startsWith('event:')) {
        currentEvent = line.slice(6).trim() || 'message';
      } else if (line.startsWith('data:')) {
        dataLines.push(line.slice(5).trimStart());
      }
      lineBreak = buffer.search(/\r?\n/);
    }

    if (done) {
      if (buffer.length) {
        if (buffer.startsWith('event:')) {
          currentEvent = buffer.slice(6).trim() || currentEvent;
        } else if (buffer.startsWith('data:')) {
          dataLines.push(buffer.slice(5).trimStart());
        }
      }
      await flushEvent();
      return;
    }
  }
}

async function ensureChatSession() {
  if (appState.chat.sessionID) return appState.chat.sessionID;
  const response = await fetchJSON(`/api/sessions/${encodeURIComponent(appState.chat.agentID)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: generateSessionName() }),
  });
  appState.chat.sessionID = response.session.id || '';
  safeStorageSet(`anyai.chat.session.${appState.chat.agentID}`, appState.chat.sessionID);
  await loadChatSessions(appState.chat.agentID, appState.chat.sessionID);
  return appState.chat.sessionID;
}

async function sendChatPrompt() {
  const promptInput = document.getElementById('chat-input');
  const sendButton = document.getElementById('chat-send');
  const note = document.getElementById('chat-composer-note');
  const text = promptInput.value.trim();
  if (!text) {
    showFlash('请输入消息内容后再发送。', true);
    return;
  }

  clearFlash();
  closeChatSource();

  const sessionID = await ensureChatSession();
  resetChatRuntimeState('queued');
  appState.chat.pendingHistoryBaseline = appState.chat.history.length;
  appState.chat.pendingUserText = text;
  appState.chat.pendingEntries = [
    { type: 'message', role: 'user', text },
  ];
  appState.chat.sending = true;

  renderChatSessionHead();
  renderChatTranscript(true);
  renderChatActivity();
  renderChatTopStats();
  updateChatSnippet();

  sendButton.disabled = true;
  note.textContent = '正在提交并建立单请求流式连接…';

  const requestBody = {
    agent_id: appState.chat.agentID,
    session_id: sessionID,
    text,
  };

  promptInput.value = '';
  safeStorageRemove('anyai.chat.draft');
  note.textContent = `已发送到 ${sessionID}，等待 run.accepted…`;
  sendButton.disabled = false;
  appState.chat.sending = false;
  renderChatSessionHead();
  renderChatActivity();
  renderChatTopStats();
  updateChatSnippet();

  void streamChatRun(requestBody).catch((error) => {
    if (error?.name === 'AbortError') return;
    appState.chat.runStatus = 'failed';
    resetChatPendingState();
    renderChatActivity();
    renderChatTranscript();
    renderChatSessionHead();
    renderChatTopStats();
    showFlash(error.message || '运行失败', true);
  });
}

async function streamChatRun(requestBody) {
  closeChatSource();
  const controller = new AbortController();
  appState.chat.activeSource = {
    close() {
      controller.abort();
    },
  };

  let terminalEvent = null;
  try {
    await fetchEventStream('/api/chat?stream=1', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'text/event-stream',
      },
      body: JSON.stringify(requestBody),
      signal: controller.signal,
    }, async (name, payload) => {
      handleChatEvent(name, payload);
      if (name === 'run.completed' || name === 'run.failed' || name === 'run.aborted') {
        terminalEvent = { name, payload };
      }
    });
  } finally {
    if (appState.chat.activeSource?.close) {
      appState.chat.activeSource = null;
    }
  }

  if (!terminalEvent) return;

  const sessionID = appState.chat.sessionID;
  cancelChatHistorySync();
  resetChatPendingState();
  appState.chat.sending = false;
  renderChatActivity();
  renderChatTranscript();
  renderChatSessionHead();
  renderChatTopStats();
  await loadChatSessions(appState.chat.agentID, sessionID);
  resetChatRuntimeState('idle');
  renderChatTranscript();
  renderChatSessionHead();
  renderChatTopStats();
  if (terminalEvent.name === 'run.completed') {
    showFlash(`Session ${sessionID} 已收到新回复。`);
  } else if (terminalEvent.name === 'run.aborted') {
    showFlash(terminalEvent.payload?.payload?.message || terminalEvent.payload?.message || '运行已中止', true);
  } else {
    showFlash(terminalEvent.payload?.payload?.message || terminalEvent.payload?.message || '运行失败', true);
  }
}

function normalizeRuntimeEventName(name) {
  return name;
}

function normalizeRuntimeEventPayload(name, payload) {
  const next = { ...(payload || {}) };
  switch (name) {
    case 'agent.call.started':
      if (!next.status) next.status = 'running';
      return next;
    case 'agent.call.completed':
      if (!next.status) next.status = 'completed';
      return next;
    case 'agent.call.failed':
      if (!next.status) next.status = 'failed';
      return next;
    default:
      return next;
  }
}

function isRuntimeDisplayNoiseEvent(name) {
  switch (name) {
    case 'run.activity':
    case 'task.queued':
    case 'task.started':
    case 'task.running':
      return true;
    default:
      return false;
  }
}

function shouldHideRuntimeActivityItem(name) {
  switch (name) {
    case 'run.started':
      return true;
    default:
      return isRuntimeDisplayNoiseEvent(name);
  }
}

function handleChatEvent(name, record) {
  if (name === 'run.accepted') {
    const acceptedRun = record?.run || record;
    appState.chat.runRecord = acceptedRun || null;
    appState.chat.runStatus = acceptedRun?.status || 'queued';
    appState.chat.runTreeLoading = Boolean(acceptedRun?.id);
    appState.chat.runtimeDrawerOpen = false;
    appState.chat.runtimeDetailView = 'events';
    appState.chat.runEvents.push({
      name,
      timestamp: new Date().toISOString(),
      payload: { run: acceptedRun },
      agent_id: acceptedRun?.agent_id,
      session_id: acceptedRun?.session_id,
      run_id: acceptedRun?.id,
    });
    if (acceptedRun?.id) {
      appState.chat.runView.selectedRunID = acceptedRun.id;
    }
    if (acceptedRun?.id) {
      scheduleChatRunTreeSync(acceptedRun.id, true);
    }
    scheduleChatHistorySync(appState.chat.agentID, acceptedRun?.session_id || appState.chat.sessionID, true);
    renderChatActivity();
    renderChatSessionHead();
    renderChatTopStats();
    updateChatSnippet();
    return;
  }

  const semanticName = normalizeRuntimeEventName(name);
  const semanticPayload = normalizeRuntimeEventPayload(name, record.payload || {});
  const normalized = {
    name: semanticName,
    raw_name: name,
    timestamp: record.timestamp || new Date().toISOString(),
    payload: semanticPayload,
    agent_id: record.agent_id,
    session_id: record.session_id,
    run_id: record.run_id,
    parent_agent_id: record.parent_agent_id,
  };

  appState.chat.runEvents.push(normalized);

  if (semanticName === 'run.started') {
    appState.chat.runStatus = 'thinking';
  }
  if (semanticName === 'text.delta') {
    appState.chat.runStatus = 'generating';
    appState.chat.liveText += normalized.payload.text || '';
    renderChatTranscript();
  }
  if ((semanticName === 'tool.call.started' || semanticName === 'tool.called') && normalized.payload.tool !== 'callagent') {
    appState.chat.runStatus = 'tooling';
  }
  if ((semanticName === 'tool.completed' || semanticName === 'tool.failed' || semanticName === 'tool.finished') && normalized.payload.tool !== 'callagent') {
    appState.chat.runStatus = normalized.payload.error ? 'failed' : 'thinking';
  }
  if (semanticName === 'agent.call.started') {
    appState.chat.runStatus = 'calling';
  }
  if (semanticName === 'agent.call.completed' || semanticName === 'agent.call.failed') {
    appState.chat.runStatus = normalized.payload.error || normalized.payload.status === 'failed' ? 'failed' : 'integrating';
  }
  if (name === 'agent.fanout.completed') {
    appState.chat.runStatus = normalized.payload.status === 'failed' ? 'failed' : 'integrating';
  }
  if (semanticName === 'memory.recalled') {
    appState.chat.runStatus = 'thinking';
  }
  if (semanticName === 'llm.retrying') {
    appState.chat.runStatus = 'retrying';
  }
  if (semanticName === 'tool.retrying') {
    appState.chat.runStatus = 'retrying';
  }
  if (semanticName === 'tool.warning') {
    appState.chat.runStatus = normalized.payload.blocked ? 'failed' : 'thinking';
  }
  if (semanticName === 'run.completed') {
    appState.chat.runStatus = 'completed';
  }
  if (semanticName === 'run.failed') {
    appState.chat.runStatus = 'failed';
  }
  if (semanticName === 'run.aborted') {
    appState.chat.runStatus = 'aborted';
  }
  if (appState.chat.runRecord && normalized.run_id === appState.chat.runRecord.id) {
    if (semanticName === 'run.started') {
      appState.chat.runRecord.status = 'running';
      appState.chat.runRecord.started_at = normalized.timestamp;
    }
    if (semanticName === 'run.completed') {
      appState.chat.runRecord.status = 'completed';
      appState.chat.runRecord.completed_at = normalized.timestamp;
      if (normalized.payload.output) {
        appState.chat.runRecord.output = normalized.payload.output;
      }
    }
    if (semanticName === 'run.failed') {
      appState.chat.runRecord.status = 'failed';
      appState.chat.runRecord.completed_at = normalized.timestamp;
      appState.chat.runRecord.error = normalized.payload.message || normalized.payload.error || '';
    }
    if (semanticName === 'run.aborted') {
      appState.chat.runRecord.status = 'aborted';
      appState.chat.runRecord.completed_at = normalized.timestamp;
      appState.chat.runRecord.error = normalized.payload.message || normalized.payload.error || '';
    }
  }

  const activeRunID = appState.chat.runRecord?.id || '';
  if (activeRunID && semanticName !== 'text.delta') {
    scheduleChatRunTreeSync(activeRunID, semanticName === 'run.started' || semanticName === 'run.completed' || semanticName === 'run.failed' || semanticName === 'run.aborted');
  }

  const currentSessionID = appState.chat.runRecord?.session_id || appState.chat.sessionID;
  if (currentSessionID && semanticName !== 'text.delta') {
    scheduleChatHistorySync(
      appState.chat.agentID,
      currentSessionID,
      semanticName === 'run.started' || semanticName === 'run.completed' || semanticName === 'run.failed' || semanticName === 'run.aborted',
    );
  }

  renderChatActivity();
  renderChatSessionHead();
  renderChatTopStats();
  updateChatSnippet();
}

function compactObservabilityLabel(value) {
  switch (String(value || '').trim()) {
    case 'native_compact_available':
      return 'native available';
    case 'chat_fallback_only':
      return 'chat fallback only';
    case 'native_compact':
      return 'native compact';
    case 'chat_fallback_compact':
      return 'chat fallback compact';
    case 'hook_supplied':
      return 'hook supplied';
    default:
      return String(value || '').trim();
  }
}

function summarizeCompactionPayload(payload, eventName) {
  const capability = compactObservabilityLabel(payload.compact_capability);
  const strategy = compactObservabilityLabel(payload.compact_strategy);
  const provider = String(payload.provider || '--').trim() || '--';
  const model = String(payload.model || '--').trim() || '--';
  const keepEntries = Number(payload.keep_entries || 0);
  const historyBefore = Number(payload.history_len_before || payload.history_len || 0);
  const historyAfter = Number(payload.history_len_after || payload.history_len || 0);

  if (eventName === 'session.compact.requested') {
    return `session compaction requested: ${provider}/${model}, keep ${keepEntries || '--'}, compact ${historyBefore || '--'} entries${capability ? `, capability ${capability}` : ''}`;
  }
  return `session compacted: ${provider}/${model}, ${historyBefore || '--'} -> ${historyAfter || '--'}${strategy ? ` via ${strategy}` : ''}${capability ? `, capability ${capability}` : ''}`;
}

function renderCompactionNotes(payload) {
  if (!payload || typeof payload !== 'object') return '';
  const rows = [];
  const capability = compactObservabilityLabel(payload.compact_capability);
  const strategy = compactObservabilityLabel(payload.compact_strategy);
  if (capability) rows.push(`capability: ${capability}`);
  if (strategy) rows.push(`strategy: ${strategy}`);
  if (payload.summary_source) rows.push(`summary source: ${payload.summary_source}`);
  if (payload.provider || payload.model) rows.push(`provider: ${String(payload.provider || '--').trim() || '--'} / model: ${String(payload.model || '--').trim() || '--'}`);
  if (payload.keep_entries || payload.history_len_before || payload.history_len_after) {
    rows.push(`window: keep ${payload.keep_entries || '--'} • compact ${payload.history_len_before || payload.history_len || '--'} -> ${payload.history_len_after || payload.history_len || '--'}`);
  }
  if (payload.protected_prefix_len) rows.push(`protected system prefix: ${payload.protected_prefix_len}`);
  if (payload.summary) rows.push(`summary: ${truncate(payload.summary, 220)}`);
  if (!rows.length) return '';
  return `<div class="note-list">${rows.map((row) => `<div class="meta-note compact">${escapeHTML(row)}</div>`).join('')}</div>`;
}

function buildActivityModel(events) {
  const items = [];
  const index = new Map();
  let streamedChars = 0;

  const upsert = (key, factory) => {
    if (index.has(key)) return index.get(key);
    const item = factory();
    index.set(key, item);
    items.push(item);
    return item;
  };

  events.forEach((event) => {
    const payload = event.payload || {};
    if (shouldHideRuntimeActivityItem(event.name)) {
      return;
    }
    switch (event.name) {
      case 'run.accepted':
        items.push({
          kind: 'run',
          title: '运行已受理',
          status: 'queued',
          timestamp: event.timestamp,
          summary: `已建立 run ${payload.run?.id || event.run_id || '--'}，等待进入执行态。`,
          runID: payload.run?.id || event.run_id || '',
          raw: event,
        });
        break;
      case 'run.started':
        items.push({
          kind: 'run',
          title: '运行开始',
          status: 'running',
          timestamp: event.timestamp,
          summary: `Agent ${event.agent_id || appState.chat.agentID} 已开始执行。`,
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'text.delta':
        streamedChars += (payload.text || '').length;
        break;
      case 'tool.call.started':
      case 'tool.called':
        if (payload.tool === 'callagent') break;
        upsert(`tool:${payload.id || items.length}`, () => ({
          kind: 'tool',
          title: payload.tool || 'unknown-tool',
          status: 'running',
          timestamp: event.timestamp,
          summary: summarizeToolPayload(payload),
          runID: event.run_id || '',
          input: payload.input,
          raw: event,
        }));
        break;
      case 'tool.completed':
      case 'tool.failed':
      case 'tool.finished': {
        if (payload.tool === 'callagent') break;
        const item = upsert(`tool:${payload.id || items.length}`, () => ({
          kind: 'tool',
          title: payload.tool || 'unknown-tool',
          status: 'completed',
          timestamp: event.timestamp,
          summary: summarizeToolPayload(payload),
          runID: event.run_id || '',
          raw: event,
        }));
        item.status = payload.error ? 'failed' : 'completed';
        item.summary = payload.error ? truncate(payload.error, 120) : truncate(payload.output || item.summary, 120);
        item.runID = event.run_id || item.runID;
        item.output = payload.output;
        item.error = payload.error;
        item.timestamp = event.timestamp;
        item.raw = event;
        break;
      }
      case 'agent.call.started': {
        const item = upsert(`agent_call:${payload.id || items.length}`, () => ({
          kind: 'agent_call',
          title: payload.target_agent || 'agent call',
          status: 'running',
          timestamp: event.timestamp,
          summary: summarizeAgentCallPayload(payload),
          runID: event.run_id || '',
          raw: event,
        }));
        item.summary = summarizeAgentCallPayload(payload);
        item.runID = event.run_id || item.runID;
        item.request = payload;
        break;
      }
      case 'agent.call.completed':
      case 'agent.call.failed': {
        const item = upsert(`agent_call:${payload.id || items.length}`, () => ({
          kind: 'agent_call',
          title: payload.target_agent || 'agent call',
          status: 'completed',
          timestamp: event.timestamp,
          summary: summarizeAgentCallPayload(payload),
          runID: event.run_id || '',
          raw: event,
        }));
        item.status = payload.error || payload.status === 'failed' ? 'failed' : 'completed';
        item.summary = truncate(payload.summary || payload.error || payload.status || item.summary, 120);
        item.runID = event.run_id || item.runID;
        item.result = payload;
        item.timestamp = event.timestamp;
        item.raw = event;
        break;
      }
      case 'agent.fanout.completed':
        items.push({
          kind: 'agent_call',
          title: 'agent call fan-in',
          status: payload.status || 'completed',
          timestamp: event.timestamp,
          summary: `completed ${payload.completed_count || 0}/${payload.total_count || 0}, failed ${payload.failed_count || 0}`,
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'memory.recalled':
        items.push({
          kind: 'memory',
          title: 'Memory Recall',
          status: 'completed',
          timestamp: event.timestamp,
          summary: summarizeMemoryPayload(payload),
          runID: event.run_id || '',
          query: payload.query,
          entries: Array.isArray(payload.entries) ? payload.entries : [],
          raw: event,
        });
        break;
      case 'llm.retrying':
        items.push({
          kind: 'run',
          title: 'LLM 重试中',
          status: 'retrying',
          timestamp: event.timestamp,
          summary: summarizeLLMRetryPayload(payload),
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'session.compact.requested':
        items.push({
          kind: 'run',
          title: '会话压缩已触发',
          status: 'warning',
          timestamp: event.timestamp,
          summary: summarizeCompactionPayload(payload, event.name),
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'session.compact.completed':
        items.push({
          kind: 'run',
          title: '会话压缩已完成',
          status: 'completed',
          timestamp: event.timestamp,
          summary: summarizeCompactionPayload(payload, event.name),
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'tool.retrying': {
        const item = upsert(`tool:${payload.id || items.length}`, () => ({
          kind: 'tool',
          title: payload.tool || 'unknown-tool',
          status: 'retrying',
          timestamp: event.timestamp,
          summary: summarizeTraceEventLine(event),
          runID: event.run_id || '',
          raw: event,
        }));
        item.status = 'retrying';
        item.summary = summarizeTraceEventLine(event);
        item.timestamp = event.timestamp;
        item.raw = event;
        break;
      }
      case 'tool.warning': {
        const item = upsert(`tool:${payload.id || items.length}`, () => ({
          kind: 'tool',
          title: payload.tool || 'unknown-tool',
          status: payload.blocked ? 'failed' : 'warning',
          timestamp: event.timestamp,
          summary: summarizeTraceEventLine(event),
          runID: event.run_id || '',
          raw: event,
        }));
        item.status = payload.blocked ? 'failed' : 'warning';
        item.summary = summarizeTraceEventLine(event);
        item.timestamp = event.timestamp;
        item.raw = event;
        break;
      }
      case 'run.completed':
        items.push({
          kind: 'run',
          title: '运行完成',
          status: 'completed',
          timestamp: event.timestamp,
          summary: '主运行已结束，建议回读 session history 作为最终事实来源。',
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'run.failed':
        items.push({
          kind: 'run',
          title: '运行失败',
          status: 'failed',
          timestamp: event.timestamp,
          summary: payload.message || '运行失败',
          runID: event.run_id || '',
          raw: event,
        });
        break;
      case 'run.aborted':
        items.push({
          kind: 'run',
          title: '运行已中止',
          status: 'aborted',
          timestamp: event.timestamp,
          summary: payload.message || payload.reason || '运行已中止',
          runID: event.run_id || '',
          raw: event,
        });
        break;
      default:
        break;
    }
  });

  return {
    streamedChars,
    items: items.slice().reverse(),
  };
}

function activityMatchesRun(item, runID) {
  if (!runID) return false;
  return item?.runID === runID;
}

function filterActivityItems(items, selectedRunID, relatedOnly) {
  if (!relatedOnly || !selectedRunID) return Array.isArray(items) ? items : [];
  return (Array.isArray(items) ? items : []).filter((item) => activityMatchesRun(item, selectedRunID));
}

function countRelatedActivityItems(items, runID) {
  return filterActivityItems(items, runID, true).length;
}

function renderActivityGroup(title, description, items, open = false, selectedRunID = '') {
  return `
    <details class="activity-group" ${open ? 'open' : ''}>
      <summary>
        <div>
          <strong>${escapeHTML(title)}</strong>
          ${description ? `<p class="muted">${escapeHTML(description)}</p>` : ''}
        </div>
        <span class="pill">${escapeHTML(items.length)}</span>
      </summary>
      <div class="stack">
        ${items.length ? items.map((item) => renderActivityCard(item, selectedRunID)).join('') : emptyState('当前分组还没有事件。')}
      </div>
    </details>
  `;
}

function renderActivityCard(item, selectedRunID = '') {
  const title = item.kind === 'agent_call'
    ? `子 Agent 调用 · ${item.title}`
    : item.kind === 'tool'
      ? `工具 · ${item.title}`
      : item.kind === 'memory'
        ? `记忆 · ${item.title}`
        : item.title;
  const linked = activityMatchesRun(item, selectedRunID);

  return `
    <details class="activity-card ${escapeHTML(item.status || 'idle')} ${linked ? 'linked' : ''}" ${item.status === 'running' ? 'open' : ''}>
      <summary>
        <div>
          <span class="eyebrow subtle">${escapeHTML(item.kind.toUpperCase())}</span>
          <strong>${escapeHTML(title)}</strong>
          <p class="muted">${escapeHTML(item.summary || '')}</p>
        </div>
        <div class="activity-side">
          ${linked ? '<span class="pill accent-pill">关联当前节点</span>' : ''}
          ${statusPill(item.status || 'idle')}
          <span class="tiny muted">${escapeHTML(formatTime(item.timestamp))}</span>
        </div>
      </summary>
      ${item.kind === 'memory' ? renderMemoryRecallList(item.entries) : ''}
      ${item.raw?.name === 'session.compact.requested' || item.raw?.name === 'session.compact.completed' ? renderCompactionNotes(item.raw?.payload || {}) : ''}
    </details>
  `;
}

function traceTreeStats(tree) {
  const stats = {
    nodes: 0,
    running: 0,
    completed: 0,
    failed: 0,
    leaves: 0,
    maxDepth: 0,
  };

  const visit = (node, depth) => {
    if (!node || !node.run) return;
    stats.nodes += 1;
    stats.maxDepth = Math.max(stats.maxDepth, depth);
    const status = String(node.run.status || 'idle').toLowerCase();
    if (status === 'running' || status === 'queued') stats.running += 1;
    else if (status === 'completed') stats.completed += 1;
    else if (status === 'failed' || status === 'aborted') stats.failed += 1;

    const children = Array.isArray(node.children) ? node.children : [];
    if (!children.length) {
      stats.leaves += 1;
    }
    children.forEach((child) => visit(child, depth + 1));
  };

  (tree || []).forEach((node) => visit(node, 0));
  return stats;
}

function compareTraceRuntime(left, right) {
  const leftTime = new Date(left?.run?.started_at || left?.run?.created_at || left?.events?.[0]?.timestamp || 0).getTime();
  const rightTime = new Date(right?.run?.started_at || right?.run?.created_at || right?.events?.[0]?.timestamp || 0).getTime();
  if (leftTime !== rightTime) return leftTime - rightTime;
  return String(left?.run?.id || '').localeCompare(String(right?.run?.id || ''));
}

function mergeTraceRun(target, patch) {
  if (!target || !patch) return target;
  Object.entries(patch).forEach(([key, value]) => {
    if (value === undefined || value === null || value === '') return;
    target[key] = value;
  });
  return target;
}

function buildTraceTreeFromEvents(events, acceptedRun = null) {
  const nodes = new Map();

  const ensureNode = (runID) => {
    if (!runID) return null;
    if (!nodes.has(runID)) {
      nodes.set(runID, {
        run: { id: runID, status: 'queued' },
        events: [],
        children: [],
      });
    }
    return nodes.get(runID);
  };

  if (acceptedRun?.id) {
    const entryNode = ensureNode(acceptedRun.id);
    mergeTraceRun(entryNode.run, acceptedRun);
  }

  (Array.isArray(events) ? events : []).forEach((event) => {
    const payload = event.payload || {};
    if (event.name === 'run.accepted') {
      const accepted = payload.run || {};
      const node = ensureNode(accepted.id || event.run_id);
      if (!node) return;
      mergeTraceRun(node.run, accepted);
      node.events.push(event);
      return;
    }

    const node = ensureNode(event.run_id);
    if (!node) return;
    node.events.push(event);
      mergeTraceRun(node.run, {
        id: event.run_id,
        agent_id: event.agent_id,
        session_id: event.session_id,
        parent_agent_id: event.parent_agent_id,
      });

    switch (event.name) {
      case 'run.started':
        mergeTraceRun(node.run, {
          status: 'running',
          started_at: event.timestamp,
        });
        break;
      case 'text.delta':
        mergeTraceRun(node.run, { status: node.run.status || 'running' });
        node.run.output = `${node.run.output || ''}${payload.text || ''}`;
        break;
      case 'tool.call.started':
      case 'tool.called':
      case 'agent.call.started':
      case 'agent.call.completed':
      case 'agent.call.failed':
      case 'llm.retrying':
      case 'tool.retrying':
        mergeTraceRun(node.run, { status: node.run.status || 'running' });
        break;
      case 'run.completed':
        mergeTraceRun(node.run, {
          status: 'completed',
          completed_at: event.timestamp,
          output: payload.output || node.run.output,
        });
        break;
      case 'run.failed':
        mergeTraceRun(node.run, {
          status: 'failed',
          completed_at: event.timestamp,
          error: payload.message || payload.error || node.run.error,
        });
        break;
      case 'run.aborted':
        mergeTraceRun(node.run, {
          status: 'aborted',
          completed_at: event.timestamp,
          error: payload.message || payload.error || node.run.error,
        });
        break;
      default:
        break;
    }
  });

  const roots = Array.from(nodes.values());

  const sortNode = (node) => {
    node.children.sort(compareTraceRuntime);
    node.children.forEach(sortNode);
  };

  roots.sort(compareTraceRuntime);
  roots.forEach(sortNode);
  return roots;
}

function flattenTraceTree(tree) {
  const list = [];
  const visit = (node, depth = 0, parent = null) => {
    if (!node) return;
    list.push({ node, depth, parent });
    (node.children || []).forEach((child) => visit(child, depth + 1, node));
  };
  (tree || []).forEach((node) => visit(node, 0, null));
  return list;
}

function pickTraceFocus(tree) {
  const flat = flattenTraceTree(tree);
  if (!flat.length) return null;
  const sorted = flat.slice().sort((left, right) => {
    const phaseRank = (node) => {
      switch (traceNodePhaseKind(node)) {
        case 'calling':
          return 0;
        case 'tooling':
          return 1;
        case 'generating':
          return 2;
        case 'integrating':
          return 3;
        case 'thinking':
          return 4;
        case 'running':
          return 5;
        case 'queued':
          return 6;
        case 'failed':
          return 7;
        case 'completed':
          return 8;
        default:
          return 9;
      }
    };
    const leftRank = phaseRank(left.node);
    const rightRank = phaseRank(right.node);
    if (leftRank !== rightRank) return leftRank - rightRank;
    const leftTime = new Date(left.node?.events?.[left.node.events.length - 1]?.timestamp || left.node?.run?.started_at || 0).getTime();
    const rightTime = new Date(right.node?.events?.[right.node.events.length - 1]?.timestamp || right.node?.run?.started_at || 0).getTime();
    return rightTime - leftTime;
  });
  return sorted[0] || null;
}

function createTraceNodeMap(tree) {
  const map = new Map();
  flattenTraceTree(tree).forEach((item) => {
    const runID = item?.node?.run?.id || '';
    if (runID) {
      map.set(runID, item);
    }
  });
  return map;
}

function traceAncestorSet(tree, runID) {
  const selectedRunID = String(runID || '');
  if (!selectedRunID) return new Set();
  const nodeMap = createTraceNodeMap(tree);
  const set = new Set([selectedRunID]);
  let current = nodeMap.get(selectedRunID);
  while (current?.parent?.run?.id) {
    const parentRunID = current.parent.run.id;
    set.add(parentRunID);
    current = nodeMap.get(parentRunID);
  }
  return set;
}

function tracePathNodes(tree, runID) {
  const targetRunID = String(runID || '').trim();
  if (!targetRunID) return [];
  const nodeMap = createTraceNodeMap(tree);
  const path = [];
  let current = nodeMap.get(targetRunID);
  while (current?.node) {
    path.unshift(current.node);
    current = current.parent?.run?.id ? nodeMap.get(current.parent.run.id) : null;
  }
  return path;
}

function collectTraceCallStackFrames(path) {
  const frames = [];
  (Array.isArray(path) ? path : []).forEach((node, depth) => {
    const agentID = String(node?.run?.agent_id || 'agent').trim() || 'agent';
    (Array.isArray(node?.events) ? node.events : []).forEach((event) => {
      const payload = event?.payload || {};
      switch (event?.name) {
        case 'agent.call.started': {
          const target = String(payload.target_agent || 'agent').trim() || 'agent';
          frames.push({
            depth,
            at: event.timestamp,
            agentID,
            kind: 'agent_call',
            label: `agent call -> ${target}`,
            detail: truncate(payloadString(payload, 'task'), 160),
          });
          break;
        }
        case 'tool.call.started':
        case 'tool.called':
          if (payload.tool === 'callagent') break;
          frames.push({
            depth,
            at: event.timestamp,
            agentID,
            kind: 'tool',
            label: String(payload.tool || 'tool').trim() || 'tool',
            detail: truncate(payloadString(payload, 'input'), 160),
          });
          break;
        default:
          break;
      }
    });
  });
  frames.sort((left, right) => {
    const leftTime = new Date(left.at || 0).getTime();
    const rightTime = new Date(right.at || 0).getTime();
    if (leftTime === rightTime) {
      if (left.depth === right.depth) return left.label.localeCompare(right.label);
      return left.depth - right.depth;
    }
    return leftTime - rightTime;
  });
  return frames.slice(-10);
}

function traceCallStackSummary(tree, runID) {
  const path = tracePathNodes(tree, runID);
  if (!path.length) {
    return '等待第一条运行事件后展示最新消息的 agent / tool 调用栈。';
  }
  const agents = path
    .map((node) => String(node?.run?.agent_id || '').trim())
    .filter(Boolean);
  const frames = collectTraceCallStackFrames(path);
  const agentSummary = agents.join(' -> ') || 'agent stack';
  if (!frames.length) return agentSummary;
  return `${agentSummary} • ${frames.map((frame) => frame.label).join(' -> ')}`;
}

function findTraceCallStackRow(rows, kind, id, label) {
  const normalizedID = String(id || '').trim();
  const normalizedLabel = String(label || '').trim();
  for (let i = rows.length - 1; i >= 0; i -= 1) {
    const row = rows[i];
    if (!row || row.kind !== kind) continue;
    if (normalizedID && String(row.callID || '').trim() === normalizedID) return row;
    if (normalizedLabel && String(row.label || '').trim() === normalizedLabel) return row;
  }
  return null;
}

function buildTraceCallStackRows(path) {
  const rows = [];
  (Array.isArray(path) ? path : []).forEach((node, depth, list) => {
    const agentID = String(node?.run?.agent_id || 'agent').trim() || 'agent';
    const runID = truncate(node?.run?.id || '--', 14);
    const fromAgent = depth === 0 ? 'entry' : `from ${list[depth - 1]?.run?.agent_id || '--'}`;
    rows.push({
      depth,
      kind: 'agent',
      prefix: depth === 0 ? '> ' : `${'| '.repeat(Math.max(0, depth - 1))}|- `,
      label: `L${depth + 1} · ${agentID}`,
      detail: `${node?.run?.status || 'idle'} • ${traceNodePhaseLabel(node)} • run ${runID} • ${fromAgent}`,
      failed: Boolean(node?.run?.error) || String(node?.run?.status || '').toLowerCase() === 'failed',
      error: node?.run?.error ? truncate(node.run.error, 220) : '',
    });

    (Array.isArray(node?.events) ? node.events : []).forEach((event) => {
      const payload = event?.payload || {};
      switch (event?.name) {
        case 'agent.call.started':
          rows.push({
            depth: depth + 1,
            kind: 'agent_call',
            prefix: `${'| '.repeat(depth)}|- `,
            label: `agent call -> ${payload.target_agent || 'agent'}`,
            detail: [formatTime(event.timestamp), 'agent call', truncate(payloadString(payload, 'task'), 160)].filter(Boolean).join(' • '),
            callID: String(payload.id || '').trim(),
            failed: false,
            error: '',
          });
          break;
        case 'tool.call.started':
        case 'tool.called':
          if (payload.tool === 'callagent') break;
          rows.push({
            depth: depth + 1,
            kind: 'tool',
            prefix: `${'| '.repeat(depth)}|- `,
            label: String(payload.tool || 'tool').trim() || 'tool',
            detail: [formatTime(event.timestamp), 'tool', truncate(payloadString(payload, 'input'), 160)].filter(Boolean).join(' • '),
            callID: String(payload.id || '').trim(),
            failed: false,
            error: '',
          });
          break;
        case 'agent.call.completed':
        case 'agent.call.failed': {
          const target = payload.target_agent || 'agent';
          const row = findTraceCallStackRow(rows, 'agent_call', payload.id, `agent call -> ${target}`);
          const err = truncate(payloadString(payload, 'error'), 220);
          const failed = Boolean(err) || String(payload.status || '').toLowerCase() === 'failed';
          if (row) {
            row.failed = failed;
            row.error = err;
          } else if (failed) {
            rows.push({
              depth: depth + 1,
              kind: 'agent_call',
              prefix: `${'| '.repeat(depth)}|- `,
              label: `agent call -> ${target}`,
              detail: [formatTime(event.timestamp), 'agent call', String(payload.status || 'failed').trim() || 'failed'].filter(Boolean).join(' • '),
              callID: String(payload.id || '').trim(),
              failed: true,
              error: err || 'agent call failed',
            });
          }
          break;
        }
        case 'tool.completed':
        case 'tool.failed':
        case 'tool.finished': {
          if (payload.tool === 'callagent') break;
          const toolLabel = String(payload.tool || 'tool').trim() || 'tool';
          const row = findTraceCallStackRow(rows, 'tool', payload.id, toolLabel);
          const err = truncate(payloadString(payload, 'error'), 220);
          if (row) {
            row.failed = Boolean(err);
            row.error = err;
          } else if (err) {
            rows.push({
              depth: depth + 1,
              kind: 'tool',
              prefix: `${'| '.repeat(depth)}|- `,
              label: toolLabel,
              detail: [formatTime(event.timestamp), 'tool', 'failed'].filter(Boolean).join(' • '),
              callID: String(payload.id || '').trim(),
              failed: true,
              error: err,
            });
          }
          break;
        }
        default:
          break;
      }
    });
  });
  return rows;
}

function collectTraceAgents(tree) {
  const agents = new Set();
  flattenTraceTree(tree).forEach(({ node }) => {
    const agentID = String(node?.run?.agent_id || '').trim();
    if (agentID) agents.add(agentID);
  });
  return Array.from(agents).sort((left, right) => left.localeCompare(right));
}

function collectTraceStatuses(tree) {
  const statuses = new Map();
  flattenTraceTree(tree).forEach(({ node }) => {
    const key = traceNodePhaseKind(node);
    if (key) statuses.set(key, traceNodePhaseLabel(node));
  });
  return Array.from(statuses.entries())
    .sort((left, right) => left[0].localeCompare(right[0]))
    .map(([value, label]) => ({ value, label }));
}

function traceNodeMatchesFilters(node, view) {
  const status = traceNodePhaseKind(node);
  const agentID = String(node?.run?.agent_id || '');
  if (view?.filterStatus && view.filterStatus !== 'all' && status !== view.filterStatus) {
    return false;
  }
  if (view?.filterAgent && view.filterAgent !== 'all' && agentID !== view.filterAgent) {
    return false;
  }
  return true;
}

function filterTraceTree(tree, view) {
  const filterNode = (node) => {
    if (!node) return null;
    const children = (Array.isArray(node.children) ? node.children : [])
      .map((child) => filterNode(child))
      .filter(Boolean);
    if (traceNodeMatchesFilters(node, view) || children.length) {
      return { ...node, children };
    }
    return null;
  };
  return (Array.isArray(tree) ? tree : []).map((node) => filterNode(node)).filter(Boolean);
}

function ensureTraceViewSelection(tree, view, fallbackRunID = '') {
  if (!view) return String(fallbackRunID || '');
  const nodeMap = createTraceNodeMap(tree);
  if (view.selectedRunID && nodeMap.has(view.selectedRunID)) {
    return view.selectedRunID;
  }
  if (fallbackRunID && nodeMap.has(fallbackRunID)) {
    view.selectedRunID = fallbackRunID;
    return view.selectedRunID;
  }
  const focusRunID = pickTraceFocus(tree)?.node?.run?.id || '';
  if (focusRunID) {
    view.selectedRunID = focusRunID;
    return focusRunID;
  }
  view.selectedRunID = tree?.[0]?.run?.id || '';
  return view.selectedRunID;
}

function collectTraceMetrics(node) {
  const metrics = { tools: 0, agentCalls: 0, memory: 0, streamed: 0 };
  const events = Array.isArray(node?.events) ? node.events : [];
  events.forEach((event) => {
    switch (event?.name) {
      case 'tool.call.started':
      case 'tool.called':
      case 'tool.completed':
      case 'tool.failed':
      case 'tool.finished':
        if (event?.payload?.tool === 'callagent') break;
        metrics.tools += 1;
        break;
      case 'agent.call.started':
      case 'agent.call.completed':
      case 'agent.call.failed':
        metrics.agentCalls += 1;
        break;
      case 'memory.recalled':
        metrics.memory += 1;
        break;
      case 'text.delta':
        metrics.streamed += String(event?.payload?.text || '').length;
        break;
      default:
        break;
    }
  });
  return metrics;
}

function traceRuntimeListNames(items, limit = 3) {
  const values = Array.from(items || []).filter(Boolean).map((value) => String(value).trim()).filter(Boolean).sort((left, right) => left.localeCompare(right));
  if (!values.length) return '';
  if (limit > 0 && values.length > limit) {
    return `${values.slice(0, limit).join(', ')}, +${values.length - limit} more`;
  }
  return values.join(', ');
}

function collectTraceRuntimeFacts(node) {
  const facts = {
    activeTools: new Map(),
    activeAgentCalls: new Map(),
    agentCallsDone: 0,
    agentCallsFailed: 0,
    textDeltaCount: 0,
    latestEvent: latestTraceNodeEvent(node?.events),
  };
  const events = Array.isArray(node?.events) ? node.events : [];
  events.forEach((event) => {
    const payload = event?.payload || {};
    switch (event?.name) {
      case 'text.delta':
        facts.textDeltaCount += 1;
        break;
      case 'tool.call.started':
      case 'tool.called':
        if (payload.tool === 'callagent') break;
        facts.activeTools.set(payload.id || payload.tool || `tool-${facts.activeTools.size}`, payload.tool || 'tool');
        break;
      case 'tool.completed':
      case 'tool.failed':
      case 'tool.finished':
        if (payload.tool === 'callagent') break;
        if (payload.id && facts.activeTools.has(payload.id)) {
          facts.activeTools.delete(payload.id);
          break;
        }
        for (const [key, value] of facts.activeTools.entries()) {
          if (value === (payload.tool || value)) {
            facts.activeTools.delete(key);
            break;
          }
        }
        break;
      case 'agent.call.started':
        facts.activeAgentCalls.set(payload.id || payload.target_agent || `agentcall-${facts.activeAgentCalls.size}`, payload.target_agent || 'agent');
        break;
      case 'agent.call.completed':
      case 'agent.call.failed':
        if (payload.id && facts.activeAgentCalls.has(payload.id)) {
          facts.activeAgentCalls.delete(payload.id);
        } else {
          for (const [key, value] of facts.activeAgentCalls.entries()) {
            if (value === (payload.target_agent || value)) {
              facts.activeAgentCalls.delete(key);
              break;
            }
          }
        }
        if (payload.error || payload.status === 'failed') facts.agentCallsFailed += 1;
        else facts.agentCallsDone += 1;
        break;
      default:
        break;
    }
  });
  return facts;
}

function traceNodePhaseKind(node) {
  const runStatus = String(node?.run?.status || 'idle').toLowerCase();
  const facts = collectTraceRuntimeFacts(node);
  if (runStatus === 'completed') return 'completed';
  if (runStatus === 'failed' || runStatus === 'aborted') return 'failed';
  if (runStatus === 'queued') return 'queued';
  if (facts.activeAgentCalls.size) return 'calling';
  if (facts.activeTools.size) return 'tooling';
  switch (facts.latestEvent?.name) {
    case 'llm.retrying':
    case 'tool.retrying':
      return 'retrying';
    case 'text.delta':
      return 'generating';
    case 'agent.call.completed':
    case 'agent.call.failed':
      return 'integrating';
    case 'tool.completed':
    case 'tool.failed':
    case 'tool.finished':
    case 'memory.recalled':
    case 'run.started':
      return 'thinking';
    default:
      return runStatus === 'running' ? 'running' : runStatus || 'idle';
  }
}

function traceNodePhaseLabel(node) {
  const facts = collectTraceRuntimeFacts(node);
  const phase = traceNodePhaseKind(node);
  const retryPayload = facts.latestEvent?.payload || {};
  switch (phase) {
    case 'completed':
      return 'completed';
    case 'failed':
      return 'failed';
    case 'queued':
      return 'queued';
    case 'retrying': {
      const attempt = Number(retryPayload.attempt || 0);
      const maxAttempts = Number(retryPayload.max_attempts || 0);
      if (attempt > 0 && maxAttempts > 0) {
        return `retry ${attempt}/${maxAttempts}`;
      }
      return 'retrying';
    }
    case 'calling':
      return facts.activeAgentCalls.size > 1 ? `subagents x${facts.activeAgentCalls.size}` : 'subagent active';
    case 'tooling':
      return facts.activeTools.size > 1 ? `tools x${facts.activeTools.size}` : 'tool active';
    case 'generating':
      return 'generating';
    case 'integrating':
      return 'integrating';
    case 'thinking':
      return 'thinking';
    default:
      return phase || 'idle';
  }
}

function traceNodeAgentCallFootprint(node) {
  const facts = collectTraceRuntimeFacts(node);
  const parts = [];
  if (facts.activeAgentCalls.size) parts.push(`${facts.activeAgentCalls.size} active`);
  if (facts.agentCallsDone) parts.push(`${facts.agentCallsDone} done`);
  if (facts.agentCallsFailed) parts.push(`${facts.agentCallsFailed} failed`);
  return parts.join(' • ');
}

function traceNodeToolFootprint(node) {
  const facts = collectTraceRuntimeFacts(node);
  return traceRuntimeListNames(facts.activeTools.values(), 3);
}

function traceNodePhaseDetail(node) {
  const facts = collectTraceRuntimeFacts(node);
  switch (traceNodePhaseKind(node)) {
    case 'completed':
      return traceNodeAgentCallFootprint(node) ? `run completed; ${traceNodeAgentCallFootprint(node)}` : 'run completed';
    case 'failed':
      return node?.run?.error || summarizeTraceEventLine(facts.latestEvent) || 'run failed';
    case 'queued':
      return 'queued for execution';
    case 'retrying':
      return summarizeTraceEventLine(facts.latestEvent) || 'runtime call failed, retrying';
    case 'calling':
      return `waiting on subagent ${traceRuntimeListNames(facts.activeAgentCalls.values(), 3)}`;
    case 'tooling':
      return `running tool ${traceRuntimeListNames(facts.activeTools.values(), 3)}`;
    case 'generating':
      return 'model is streaming a reply';
    case 'integrating':
      return 'agent call finished, integrating result';
    case 'thinking':
      return 'model is thinking';
    default:
      return summarizeTraceEventLine(facts.latestEvent) || 'waiting for runtime events';
  }
}

function traceNodePhasePill(node) {
  return statusPill(traceNodePhaseKind(node), traceNodePhaseLabel(node));
}

function traceTreeOperationalStats(tree) {
  const stats = {
    activeAgentCalls: 0,
    agentCallsDone: 0,
    agentCallsFailed: 0,
    activeTools: 0,
    retrying: 0,
    generating: 0,
    running: 0,
    queued: 0,
  };
  flattenTraceTree(tree).forEach(({ node }) => {
    const facts = collectTraceRuntimeFacts(node);
    const phase = traceNodePhaseKind(node);
    stats.activeAgentCalls += facts.activeAgentCalls.size;
    stats.agentCallsDone += facts.agentCallsDone;
    stats.agentCallsFailed += facts.agentCallsFailed;
    stats.activeTools += facts.activeTools.size;
    if (phase === 'retrying') stats.retrying += 1;
    if (phase === 'generating') stats.generating += 1;
    if (phase === 'queued') stats.queued += 1;
    if (['retrying', 'calling', 'tooling', 'generating', 'integrating', 'thinking', 'running'].includes(phase)) stats.running += 1;
  });
  return stats;
}

function traceTreeRuntimeStatus(tree, fallbackStatus = 'idle') {
  const stats = traceTreeOperationalStats(tree);
  if (stats.activeAgentCalls > 0) {
    return {
      key: 'calling',
      label: stats.activeAgentCalls > 1 ? `subagents ${stats.activeAgentCalls}` : 'subagent active',
    };
  }
  if (stats.retrying > 0) {
    return {
      key: 'retrying',
      label: stats.retrying > 1 ? `retrying ${stats.retrying}` : 'retrying',
    };
  }
  if (stats.generating > 0) {
    return { key: 'generating', label: 'generating' };
  }
  if (stats.activeTools > 0) {
    return {
      key: 'tooling',
      label: stats.activeTools > 1 ? `tools ${stats.activeTools}` : 'tool active',
    };
  }
  if (stats.running > 0) {
    return { key: 'thinking', label: 'thinking' };
  }
  if (stats.queued > 0) {
    return { key: 'queued', label: 'queued' };
  }
  const normalized = String(fallbackStatus || 'idle').toLowerCase();
  if (normalized === 'completed') return { key: 'completed', label: 'completed' };
  if (normalized === 'failed' || normalized === 'aborted') return { key: 'failed', label: 'failed' };
  return { key: normalized || 'idle', label: normalized || 'idle' };
}

function getChatDisplayRunTree() {
  const liveTree = buildTraceTreeFromEvents(appState.chat.runEvents, appState.chat.runRecord);
  if (liveTree.length) return liveTree;
  return Array.isArray(appState.chat.runTree) ? appState.chat.runTree : [];
}

function getChatRuntimeStatusMeta(tree = getChatDisplayRunTree()) {
  return traceTreeRuntimeStatus(tree, appState.chat.runStatus || 'idle');
}

function chatRuntimeFallbackDetail(status = 'idle') {
  switch (String(status || 'idle')) {
    case 'queued':
      return '消息已送达，等待入口 Agent 开始执行。';
    case 'retrying':
      return 'LLM 调用失败，运行时正在等待下一次自动重试。';
    case 'thinking':
      return '模型正在思考，还没有开始输出文本。';
    case 'generating':
      return '模型正在持续输出回复。';
    case 'tooling':
      return '当前节点正在执行工具调用。';
    case 'calling':
      return '当前节点正在等待子 Agent 返回结果。';
    case 'integrating':
      return '子 Agent 已返回，主 Agent 正在整合结果。';
    case 'completed':
      return '本次运行已经完成，可以回看下方 RunTree 和活动记录。';
    case 'failed':
      return '本次运行失败，请查看下方最近事件或错误信息。';
    case 'aborted':
      return '本次运行已中止，请查看下方最近事件或错误信息。';
    default:
      return appState.chat.sessionID
        ? '发送消息后，会话窗口会实时显示当前 phase、子 Agent 与 RunTree。'
        : '先选择或创建一个 Session，再开始对话。';
  }
}

function chatRuntimeFocusContext(tree, runtime, liveStats, model, selectedRunID = '') {
  const nodeMap = createTraceNodeMap(tree);
  const focusNode = nodeMap.get(selectedRunID)?.node || pickTraceFocus(tree)?.node || tree[0] || null;
  const focusFacts = focusNode ? collectTraceRuntimeFacts(focusNode) : null;
  const latestEvent = focusNode
    ? latestTraceNodeEvent(focusNode.events)
    : appState.chat.runEvents[appState.chat.runEvents.length - 1] || null;
  const runID = focusNode?.run?.id || appState.chat.runRecord?.id || '--';
  const phaseLabel = focusNode ? traceNodePhaseLabel(focusNode) : runtime.label;
  const detail = focusNode ? traceNodePhaseDetail(focusNode) : chatRuntimeFallbackDetail(runtime.key);
  const outputChars = model.streamedChars || String(appState.chat.liveText || '').length || String(appState.chat.runRecord?.output || '').length;
  const subagentLine = focusNode
    ? traceNodeAgentCallFootprint(focusNode) || '暂无子 Agent 调用'
    : liveStats.activeAgentCalls
      ? `${liveStats.activeAgentCalls} active`
      : liveStats.agentCallsDone
        ? `${liveStats.agentCallsDone} done`
        : '暂无子 Agent 调用';
  const toolLine = focusNode
    ? traceNodeToolFootprint(focusNode) || (liveStats.activeTools ? `${liveStats.activeTools} tool active` : '暂无工具调用')
    : liveStats.activeTools
      ? `${liveStats.activeTools} tool active`
      : '暂无工具调用';

  return {
    focusNode,
    focusFacts,
    latestEvent,
    runID,
    phaseLabel,
    detail,
    outputChars,
    subagentLine,
    toolLine,
    runtime,
    liveStats,
    model,
  };
}

function buildChatRuntimePresentation() {
  const model = buildActivityModel(appState.chat.runEvents);
  const displayTree = getChatDisplayRunTree();
  const runtime = getChatRuntimeStatusMeta(displayTree);
  const liveStats = traceTreeOperationalStats(displayTree);
  const selectedRunID = ensureTraceViewSelection(displayTree, appState.chat.runView, appState.chat.runRecord?.id || '');
  const relatedOnly = Boolean(appState.chat.runView.relatedOnly && selectedRunID);
  const context = chatRuntimeFocusContext(displayTree, runtime, liveStats, model, selectedRunID);
  return {
    model,
    displayTree,
    runtime,
    liveStats,
    selectedRunID,
    relatedOnly,
    context,
    visible: shouldShowChatRuntime(context),
  };
}

function shouldShowChatRuntime(context) {
  if (!context) return false;
  const visibleKeys = ['queued', 'thinking', 'generating', 'tooling', 'calling', 'integrating', 'completed', 'failed'];
  return Boolean(
    (context.runID && context.runID !== '--')
      && (
        appState.chat.runTreeLoading
        || visibleKeys.includes(context.runtime.key)
        || visibleKeys.includes(appState.chat.runStatus)
      )
  );
}

function renderChatRuntimeStrip(context) {
  if (!context) return '';
  const {
    focusNode,
    latestEvent,
    runID,
    phaseLabel,
    detail,
    subagentLine,
    toolLine,
    runtime,
  } = context;
  const compactDetail = truncate(detail || chatRuntimeFallbackDetail(runtime.key), 120);
  return `
    <div class="chat-runtime-inline ${escapeHTML(runtime.key || 'idle')}">
      <div class="chat-runtime-inline-main">
        ${statusPill(runtime.key, phaseLabel)}
        <div class="chat-runtime-inline-copy">
          <strong>${escapeHTML(focusNode?.run?.agent_id || appState.chat.agentID || '--')}</strong>
          <span class="muted">${escapeHTML(compactDetail)}</span>
        </div>
      </div>
      <div class="chat-runtime-inline-facts">
        ${runID !== '--' ? `<span class="pill ghost-pill">run ${escapeHTML(truncate(runID, 14))}</span>` : ''}
        ${subagentLine && subagentLine !== '暂无子 Agent 调用' ? `<span class="pill ghost-pill">${escapeHTML(subagentLine)}</span>` : ''}
        ${toolLine && toolLine !== '暂无工具调用' ? `<span class="pill ghost-pill">${escapeHTML(toolLine)}</span>` : ''}
        ${latestEvent ? `<span class="pill ghost-pill">${escapeHTML(latestEvent.name)}</span>` : ''}
        ${appState.chat.runTreeLoading ? '<span class="pill">syncing</span>' : ''}
      </div>
    </div>
  `;
}

function renderChatRuntimeSummaryMessage(context) {
  if (!context) return '';
  return `
    <article class="message-bubble system status-message runtime-summary-message ${escapeHTML(context.runtime.key || 'idle')}">
      <button
        type="button"
        class="chat-runtime-summary-button"
        data-chat-runtime-open
        aria-controls="chat-runtime-sidebar"
        aria-expanded="${appState.chat.runtimeDrawerOpen ? 'true' : 'false'}"
      >
        <div class="chat-runtime-summary-head">
          <div class="message-meta">
            <span>runtime</span>
            ${context.latestEvent?.timestamp ? `<span>${escapeHTML(formatTime(context.latestEvent.timestamp))}</span>` : ''}
          </div>
          <span class="pill accent-pill">${appState.chat.runtimeDrawerOpen ? '侧栏已展开' : '查看详情'}</span>
        </div>
        ${renderChatRuntimeStrip(context)}
        <div class="chat-runtime-summary-foot">
          <span class="muted">${escapeHTML(appState.chat.runtimeDrawerOpen ? '右侧侧栏正在显示完整 RunTree 和 Events。' : '缩略信息保留在消息流中；点击这里查看完整 RunTree 和 Events。')}</span>
          <div class="chat-runtime-summary-tags">
            <span class="pill ghost-pill">RunTree</span>
            <span class="pill ghost-pill">Events</span>
          </div>
        </div>
      </button>
    </article>
  `;
}

function renderChatRuntimeOverview(context, tree, model, selectedRunID = '') {
  if (!context) return '';
  const focusRun = context.focusNode?.run || {};
  const stats = traceTreeStats(tree);
  const relatedActivityCount = countRelatedActivityItems(model.items || [], selectedRunID);
  const latestEventName = context.latestEvent?.name || 'waiting';
  const latestEventTime = context.latestEvent?.timestamp ? formatTime(context.latestEvent.timestamp) : '--:--';
  const tone = context.runtime.key === 'failed'
    ? 'danger'
    : ['queued', 'thinking', 'calling', 'integrating', 'tooling'].includes(context.runtime.key)
      ? 'warning'
      : 'success';
  const liveFootprint = [
    context.liveStats.activeAgentCalls ? `${context.liveStats.activeAgentCalls} 子 Agent 进行中` : '',
    context.liveStats.activeTools ? `${context.liveStats.activeTools} 工具进行中` : '',
    context.liveStats.agentCallsFailed ? `${context.liveStats.agentCallsFailed} 委派失败` : '',
    context.liveStats.agentCallsDone ? `${context.liveStats.agentCallsDone} 委派完成` : '',
  ].filter(Boolean).join(' · ') || '当前没有活跃的委派或工具调用。';

  return `
    <div class="chat-runtime-overview-grid">
      <div class="chat-runtime-overview-card primary ${escapeHTML(tone)}">
        <span class="eyebrow subtle">CURRENT NODE</span>
        <strong>${escapeHTML(focusRun.agent_id || appState.chat.agentID || '--')} · ${escapeHTML(context.phaseLabel || context.runtime.label || '--')}</strong>
        <p class="muted">${escapeHTML(truncate(context.detail || chatRuntimeFallbackDetail(context.runtime.key), 140))}</p>
      </div>
      <div class="chat-runtime-overview-card">
        <span class="eyebrow subtle">LATEST EVENT</span>
        <strong>${escapeHTML(latestEventName)}</strong>
        <p class="muted">${escapeHTML(`发生在 ${latestEventTime}，当前焦点 run ${truncate(context.runID || '--', 18)}。`)}</p>
      </div>
      <div class="chat-runtime-overview-card">
        <span class="eyebrow subtle">RUN TREE</span>
        <strong>${escapeHTML(stats.nodes || 0)} 节点 / ${escapeHTML((stats.maxDepth || 0) + 1)} 层</strong>
        <p class="muted">${escapeHTML(selectedRunID ? `当前焦点关联 ${relatedActivityCount} 条活动。` : '展开后可继续聚焦具体节点。')}</p>
      </div>
      <div class="chat-runtime-overview-card">
        <span class="eyebrow subtle">LIVE FOOTPRINT</span>
        <strong>${escapeHTML(context.outputChars || 0)} chars</strong>
        <p class="muted">${escapeHTML(liveFootprint)}</p>
      </div>
    </div>
  `;
}

function renderChatRuntimeSection(title, description, body, actionHTML = '') {
  if (!body) return '';
  return `
    <section class="chat-runtime-section">
      <div class="chat-runtime-section-head">
        <div>
          <h4>${escapeHTML(title)}</h4>
          ${description ? `<p class="muted">${escapeHTML(description)}</p>` : ''}
        </div>
        ${actionHTML ? `<div class="section-actions">${actionHTML}</div>` : ''}
      </div>
      ${body}
    </section>
  `;
}

function renderChatRuntimeViewSwitcher(activeView, runTreeCount, eventCount) {
  return `
    <div class="chat-runtime-view-switch" role="tablist" aria-label="运行详情视图切换">
      <button type="button" class="chat-runtime-view-button ${activeView === 'tree' ? 'active' : ''}" data-chat-runtime-view="tree" role="tab" aria-selected="${activeView === 'tree' ? 'true' : 'false'}">
        <span>RunTree</span>
        <span class="pill ghost-pill">${escapeHTML(runTreeCount)}</span>
      </button>
      <button type="button" class="chat-runtime-view-button ${activeView === 'events' ? 'active' : ''}" data-chat-runtime-view="events" role="tab" aria-selected="${activeView === 'events' ? 'true' : 'false'}">
        <span>Events</span>
        <span class="pill ghost-pill">${escapeHTML(eventCount)}</span>
      </button>
    </div>
  `;
}

function renderLatestCallStack(tree, focusNode, view, emptyText = '等待第一条运行事件后展示最新消息的完整调用栈。') {
  const runID = focusNode?.run?.id || '';
  const path = tracePathNodes(tree, runID);
  if (!path.length) {
    return `
      <details class="trace-callstack" data-chat-call-stack ${view?.latestStackOpen ? 'open' : ''}>
        <summary>
          <div>
            <strong>Latest Call Stack</strong>
            <p class="muted">${escapeHTML(emptyText)}</p>
          </div>
        </summary>
      </details>
    `;
  }

  const frames = collectTraceCallStackFrames(path);
  const rows = buildTraceCallStackRows(path);
  const summary = traceCallStackSummary(tree, runID);

  return `
    <details class="trace-callstack" data-chat-call-stack ${view?.latestStackOpen ? 'open' : ''}>
      <summary>
        <div>
          <strong>Latest Call Stack</strong>
          <p class="muted">${escapeHTML(truncate(summary, 180))}</p>
        </div>
        <div class="trace-callstack-badges">
          <span class="pill ghost-pill">${escapeHTML(path.length)} agent</span>
          <span class="pill ghost-pill">${escapeHTML(frames.length)} step</span>
        </div>
      </summary>
      <div class="trace-callstack-body">
        <div class="eyebrow subtle">TREE</div>
        <div class="trace-callstack-tree">
          ${rows.map((row) => `
            <div class="trace-callstack-row ${escapeHTML(row.kind)} ${row.failed ? 'failed' : ''}" style="--stack-depth:${escapeHTML(row.depth)}">
              <strong><code class="trace-callstack-prefix">${escapeHTML(row.prefix)}</code>${escapeHTML(row.label)}</strong>
              <div class="muted tiny">${escapeHTML(row.detail)}</div>
              ${row.error ? `<div class="trace-callstack-error">${escapeHTML(row.error)}</div>` : ''}
            </div>
          `).join('')}
          ${!frames.length ? '<div class="muted tiny">当前最新消息还没有记录到工具或委派调用。</div>' : ''}
        </div>
      </div>
    </details>
  `;
}

function buildChatRuntimeGroups(model, selectedRunID, relatedOnly, liveStats) {
  const runItems = filterActivityItems(model.items.filter((item) => item.kind === 'run'), selectedRunID, relatedOnly);
  const toolItems = filterActivityItems(model.items.filter((item) => item.kind === 'tool'), selectedRunID, relatedOnly);
  const agentCallItems = filterActivityItems(model.items.filter((item) => item.kind === 'agent_call'), selectedRunID, relatedOnly);
  const memoryItems = filterActivityItems(model.items.filter((item) => item.kind === 'memory'), selectedRunID, relatedOnly);

  const groups = [];
  if (relatedOnly && selectedRunID) {
    groups.push(`<div class="meta-note compact">当前只展示节点 <code>${escapeHTML(selectedRunID)}</code> 的关联活动。可在下方切回“显示全部活动”。</div>`);
  }
  groups.push(renderActivityGroup('运行里程碑', 'run.accepted / started / session.compact.* / completed / failed', runItems, true, selectedRunID));
  if (agentCallItems.length || liveStats.activeAgentCalls || liveStats.agentCallsDone || liveStats.agentCallsFailed) {
    groups.push(renderActivityGroup('子 Agent', 'agent.call.started / agent.call.completed / agent.call.failed', agentCallItems, agentCallItems.length > 0, selectedRunID));
  }
  if (toolItems.length || liveStats.activeTools) {
    groups.push(renderActivityGroup('工具', 'tool.call.started / tool.completed / tool.failed', toolItems, toolItems.length > 0, selectedRunID));
  }
  if (memoryItems.length) {
    groups.push(renderActivityGroup('记忆', 'memory.recalled', memoryItems, false, selectedRunID));
  }
  return groups;
}

function renderChatRuntimeSidebar(context, model, tree, selectedRunID, relatedOnly) {
  if (!context) return '';
  const groups = buildChatRuntimeGroups(model, selectedRunID, relatedOnly, context.liveStats);
  const latestCallStack = renderLatestCallStack(tree, context.focusNode, appState.chat.runView);
  const overview = renderChatRuntimeOverview(context, tree, model, selectedRunID);
  const runTreeBody = tree.length
    ? renderTraceTree(tree, {
      selectedRunID: appState.chat.runRecord?.id || '',
      view: appState.chat.runView,
      activityItems: model.items,
      compact: true,
      surfaceClass: 'chat-runtime-trace-tree',
    })
    : appState.chat.runTreeLoading
      ? `
        <div class="micro-panel chat-runtime-empty">
          <div class="eyebrow">RUN TREE</div>
          <p class="muted">正在同步当前 RunTree…</p>
        </div>
      `
      : '';
  const runTreeCount = traceTreeStats(tree).nodes || 0;
  const eventCount = model.items.length || 0;
  const hasTreeView = Boolean(runTreeBody || latestCallStack);
  const hasEventView = groups.length > 0;
  let activeView = appState.chat.runtimeDetailView || 'tree';
  if (activeView === 'tree' && !hasTreeView && hasEventView) activeView = 'events';
  if (activeView === 'events' && !hasEventView && hasTreeView) activeView = 'tree';
  appState.chat.runtimeDetailView = activeView;

  const treePanel = hasTreeView
    ? `
      <div class="chat-runtime-pane ${activeView === 'tree' ? 'active' : ''}" data-chat-runtime-pane="tree">
        <section class="chat-runtime-section chat-runtime-section-tight">
          ${latestCallStack}
        </section>
        ${runTreeBody ? renderChatRuntimeSection('RunTree', '查看当前回复对应的 agent 运行链和焦点路径。', runTreeBody, appState.chat.runTreeLoading ? '<span class="pill">syncing</span>' : '') : ''}
      </div>
    `
    : '';
  const eventPanel = hasEventView
    ? `
      <div class="chat-runtime-pane ${activeView === 'events' ? 'active' : ''}" data-chat-runtime-pane="events">
        ${renderChatRuntimeSection('关键事件', '把运行、子 Agent 调用、工具和记忆线索按类别收拢，方便快速定位变化。', `<div class="chat-runtime-events stack">${groups.join('')}</div>`)}
      </div>
    `
    : '';
  const switcher = (hasTreeView && hasEventView)
    ? renderChatRuntimeViewSwitcher(activeView, runTreeCount, eventCount)
    : '';

  return `
    <div class="chat-runtime-sidebar-shell ${escapeHTML(context.runtime.key || 'idle')} ${appState.chat.runtimeDrawerOpen ? 'open' : ''}" data-chat-runtime-sidebar-shell>
      <div class="chat-runtime-sidebar-head">
        <div class="chat-runtime-sidebar-copy">
          <strong>运行详情</strong>
          <p class="muted">消息流里只保留缩略摘要；完整 RunTree 和 Events 在这里查看。</p>
        </div>
        <div class="chat-runtime-sidebar-badges">
          ${statusPill(context.runtime.key, context.phaseLabel)}
          <span class="pill ghost-pill">${escapeHTML(context.outputChars)} chars</span>
          <button type="button" class="ghost chat-runtime-sidebar-close" data-chat-runtime-close>收起</button>
        </div>
      </div>
      <div class="chat-runtime-sidebar-body">
        ${overview}
        ${switcher}
        <div class="chat-runtime-workspace" data-chat-runtime-workspace>
          ${treePanel}
          ${eventPanel}
        </div>
      </div>
    </div>
  `;
}

function traceDescendantCount(node) {
  let total = 0;
  (Array.isArray(node?.children) ? node.children : []).forEach((child) => {
    total += 1 + traceDescendantCount(child);
  });
  return total;
}

function traceNodeOpenState(runID, depth, status, view, selectedRunID, ancestors) {
  if (Object.prototype.hasOwnProperty.call(view?.expanded || {}, runID)) {
    return Boolean(view.expanded[runID]);
  }
  return depth < 1
    || runID === selectedRunID
    || ancestors?.has(runID)
    || status === 'running'
    || status === 'failed'
    || status === 'aborted';
}

function latestTraceNodeEvent(events) {
  const list = Array.isArray(events) ? events : [];
  if (!list.length) return null;
  for (let index = list.length - 1; index >= 0; index -= 1) {
    if (!isRuntimeDisplayNoiseEvent(list[index]?.name)) {
      return list[index];
    }
  }
  return list[list.length - 1];
}

function summarizeTraceNode(node) {
  const run = node?.run || {};
  const event = latestTraceNodeEvent(node?.events);
  const payload = event?.payload || {};
  const phaseDetail = traceNodePhaseDetail(node);
  if (phaseDetail) {
    return phaseDetail;
  }

  switch (event?.name) {
    case 'run.started':
      return `Agent ${run.agent_id || '--'} 已进入执行态。`;
    case 'memory.recalled':
      return summarizeMemoryPayload(payload);
    case 'tool.call.started':
    case 'tool.called':
      return `正在调用工具 ${payload.tool || '--'}。`;
    case 'tool.completed':
    case 'tool.failed':
    case 'tool.finished':
      return payload.error
        ? `工具 ${payload.tool || '--'} 失败：${truncate(payload.error, 92)}`
        : `工具 ${payload.tool || '--'} 已完成。`;
    case 'agent.call.started':
      return `已发起子 Agent 调用给 ${payload.target_agent || '--'}：${truncate(payload.task || payload.summary || '等待任务摘要', 92)}`;
    case 'agent.call.completed':
    case 'agent.call.failed':
      return payload.error
        ? `子 Agent 调用失败：${truncate(payload.error, 92)}`
        : `子 Agent 调用完成：${truncate(payload.summary || payload.status || '已返回结果', 92)}`;
    case 'session.compact.requested':
    case 'session.compact.completed':
      return summarizeCompactionPayload(payload, event?.name);
    case 'run.completed':
      return '该节点已经完成，可继续展开查看输出摘要与最近事件。';
    case 'run.failed':
      return payload.message || run.error || '该节点执行失败。';
    case 'run.aborted':
      return payload.message || run.error || '该节点执行已中止。';
    case 'text.delta':
      return `正在输出内容：${truncate(payload.text || run.output || 'streaming', 92)}`;
    default:
      break;
  }

  if (run.error) return truncate(run.error, 110);
  if (run.output) return `最终输出：${truncate(run.output, 110)}`;
  if (run.input) return `输入摘要：${truncate(run.input, 110)}`;
  return '等待更多运行事件。';
}

function summarizeTraceEventLine(event) {
  if (!event) return '等待更多事件';
  const payload = event.payload || {};
  switch (event.name) {
    case 'run.activity':
      return '仍在运行';
    case 'run.started':
      return 'model is thinking';
    case 'memory.recalled':
      return summarizeMemoryPayload(payload);
    case 'llm.retrying':
      return summarizeLLMRetryPayload(payload);
    case 'tool.retrying':
      return `retrying ${payload.tool || '--'}: ${truncate(payload.error || payload.error_class || 'tool call failed', 72)}`;
    case 'tool.warning':
      return payload.message || `tool warning: ${payload.tool || '--'}`;
    case 'session.compact.requested':
    case 'session.compact.completed':
      return summarizeCompactionPayload(payload, event.name);
    case 'run.completed':
      return 'run completed';
    case 'run.failed':
      return payload.message || '执行失败';
    case 'run.aborted':
      return payload.message || payload.reason || '执行已中止';
    case 'agent.call.started':
      return `waiting on subagent ${payload.target_agent || '--'}`;
    case 'agent.call.completed':
    case 'agent.call.failed':
      return payload.error ? `agent call failed: ${truncate(payload.error, 72)}` : `agent call completed: ${truncate(payload.summary || payload.status || 'returned', 72)}`;
    case 'tool.call.started':
    case 'tool.called':
      return `running ${payload.tool || '--'}`;
    case 'tool.completed':
    case 'tool.failed':
    case 'tool.finished':
      return payload.error ? `tool failed: ${truncate(payload.error, 72)}` : `tool finished: ${payload.tool || '--'}`;
    default:
      return truncate(payload.text || safeJSON(payload) || event.name || 'event', 72);
  }
}

function latestNamedTraceEvent(events, name) {
  if (!Array.isArray(events) || !events.length) return null;
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (events[index]?.name === name) return events[index];
  }
  return null;
}

function renderTraceNodeTimeline(events) {
  const notable = (Array.isArray(events) ? events : [])
    .filter((event) => event.name !== 'text.delta' && !isRuntimeDisplayNoiseEvent(event.name))
    .slice(-3)
    .reverse();
  if (!notable.length) return '';

  return `
    <div class="trace-node-timeline">
      <div class="eyebrow subtle">RECENT EVENTS</div>
      ${notable.map((event) => `
        <div class="trace-event-row">
          <div>
            <strong>${escapeHTML(event.name)}</strong>
            <div class="muted tiny">${escapeHTML(summarizeTraceEventLine(event))}</div>
          </div>
          <span class="tiny muted">${escapeHTML(formatTime(event.timestamp))}</span>
        </div>
      `).join('')}
    </div>
  `;
}

function renderTraceNode(node, depth = 0, context = {}, parentNode = null) {
  const run = node?.run || {};
  const events = Array.isArray(node?.events) ? node.events : [];
  const children = Array.isArray(node?.children) ? node.children : [];
  const status = String(run.status || 'idle').toLowerCase();
  const phase = traceNodePhaseKind(node);
  const selected = run.id === context.selectedRunID;
  const open = traceNodeOpenState(run.id, depth, status, context.view, context.selectedRunID, context.ancestors);
  const summary = summarizeTraceNode(node);
  const latestEvent = latestTraceNodeEvent(events);
  const routeLabel = parentNode?.run?.agent_id
    ? `${parentNode.run.agent_id} -> ${run.agent_id || '--'}`
    : `entry · ${run.id || 'no-run'}`;
  const metrics = collectTraceMetrics(node);
  const agentCallFootprint = traceNodeAgentCallFootprint(node);
  const toolFootprint = traceNodeToolFootprint(node);

  const previewBlocks = [];
  if (run.input) {
    previewBlocks.push(`
      <div class="trace-node-preview">
        <span class="eyebrow subtle">输入摘要</span>
        <div>${escapeHTML(truncate(run.input, 220))}</div>
      </div>
    `);
  }
  if (run.output && (selected || depth === 0)) {
    previewBlocks.push(`
      <div class="trace-node-preview output">
        <span class="eyebrow subtle">输出摘要</span>
        <div>${escapeHTML(truncate(run.output, 220))}</div>
      </div>
    `);
  }
  if (run.error) {
    previewBlocks.push(`
      <div class="trace-node-preview danger">
        <span class="eyebrow subtle">ERROR</span>
        <div>${escapeHTML(truncate(run.error, 220))}</div>
      </div>
    `);
  }
  const memoryRecall = latestNamedTraceEvent(events, 'memory.recalled');
  const latestCompaction = latestNamedTraceEvent(events, 'session.compact.completed') || latestNamedTraceEvent(events, 'session.compact.requested');
  if (memoryRecall?.payload?.entries?.length) {
    previewBlocks.push(`
      <div class="trace-node-preview">
        <span class="eyebrow subtle">MEMORY RECALL</span>
        <div class="muted">${escapeHTML(summarizeMemoryPayload(memoryRecall.payload))}</div>
        ${renderMemoryRecallList(memoryRecall.payload.entries)}
      </div>
    `);
  }
  if (latestCompaction?.payload) {
    previewBlocks.push(`
      <div class="trace-node-preview">
        <span class="eyebrow subtle">COMPACTION</span>
        <div class="muted">${escapeHTML(summarizeCompactionPayload(latestCompaction.payload, latestCompaction.name))}</div>
        ${renderCompactionNotes(latestCompaction.payload)}
      </div>
    `);
  }

  return `
    <details class="trace-node ${escapeHTML(status)} ${escapeHTML(phase)} ${selected ? 'selected' : ''}" data-trace-node="${escapeHTML(run.id || '')}" ${open ? 'open' : ''}>
      <summary data-trace-select="${escapeHTML(run.id || '')}">
        <div class="trace-node-head">
          <div class="trace-node-route">
            <span class="eyebrow subtle">${escapeHTML(depth === 0 ? 'ENTRY RUN' : `STEP · L${depth + 1}`)}</span>
            <span class="trace-node-link">${escapeHTML(routeLabel)}</span>
          </div>
          <strong>${escapeHTML(run.agent_id || 'unknown-agent')}</strong>
          <p class="muted">${escapeHTML(summary)}</p>
        </div>
        <div class="trace-node-badges">
          ${statusPill(run.status || 'idle')}
          ${traceNodePhasePill(node)}
          ${latestEvent ? `<span class="pill">${escapeHTML(latestEvent.name)}</span>` : ''}
          <span class="pill">${escapeHTML(children.length)} 子节点</span>
          ${metrics.tools ? `<span class="pill">${escapeHTML(metrics.tools)} tool</span>` : ''}
          ${metrics.agentCalls ? `<span class="pill">${escapeHTML(metrics.agentCalls)} agent call</span>` : ''}
          ${metrics.memory ? `<span class="pill">${escapeHTML(metrics.memory)} memory</span>` : ''}
          ${selected ? '<span class="pill accent-pill">当前节点</span>' : '<span class="pill ghost-pill">点击聚焦</span>'}
        </div>
      </summary>
      <div class="trace-node-body">
        <div class="mini-stat-list trace-node-stats">
          <div class="mini-stat compact"><span class="mini-stat-label">Run</span><code>${escapeHTML(run.id || '--')}</code></div>
          <div class="mini-stat compact"><span class="mini-stat-label">Agent</span><code>${escapeHTML(run.agent_id || '--')}</code></div>
          <div class="mini-stat compact"><span class="mini-stat-label">Session</span><code>${escapeHTML(run.session_id || '--')}</code></div>
          <div class="mini-stat compact"><span class="mini-stat-label">事件数</span><strong>${escapeHTML(events.length)}</strong></div>
          <div class="mini-stat compact"><span class="mini-stat-label">Phase</span><strong>${escapeHTML(traceNodePhaseLabel(node))}</strong></div>
          <div class="mini-stat compact"><span class="mini-stat-label">开始时间</span><strong>${escapeHTML(formatTime(run.started_at || run.created_at))}</strong></div>
          <div class="mini-stat compact"><span class="mini-stat-label">结束时间</span><strong>${escapeHTML(formatTime(run.completed_at || ''))}</strong></div>
        </div>
        ${agentCallFootprint ? `
          <div class="trace-node-preview">
            <span class="eyebrow subtle">SUBAGENTS</span>
            <div>${escapeHTML(agentCallFootprint)}</div>
          </div>
        ` : ''}
        ${toolFootprint ? `
          <div class="trace-node-preview">
            <span class="eyebrow subtle">TOOLS</span>
            <div>${escapeHTML(toolFootprint)}</div>
          </div>
        ` : ''}
        ${previewBlocks.join('')}
        ${renderTraceNodeTimeline(events)}
        ${children.length ? `<div class="trace-node-children">${children.map((child) => renderTraceNode(child, depth + 1, context, node)).join('')}</div>` : ''}
      </div>
    </details>
  `;
}

function renderTraceSelectionPanel(node, relatedActivityCount = 0) {
  if (!node?.run) {
    return `
      <aside class="trace-selection-panel">
        <div class="eyebrow">NODE DETAIL</div>
        <strong>等待可用节点</strong>
        <p class="muted">运行开始后，这里会解释当前焦点节点的状态、最近事件以及 memory / tool / agent call 线索。</p>
      </aside>
    `;
  }

  const run = node.run;
  const events = Array.isArray(node.events) ? node.events : [];
  const children = Array.isArray(node.children) ? node.children : [];
  const latestEvent = latestTraceNodeEvent(events);
  const metrics = collectTraceMetrics(node);
  const latestMemoryRecall = latestNamedTraceEvent(events, 'memory.recalled');
  const latestCompaction = latestNamedTraceEvent(events, 'session.compact.completed') || latestNamedTraceEvent(events, 'session.compact.requested');
  const agentCallFootprint = traceNodeAgentCallFootprint(node);
  const toolFootprint = traceNodeToolFootprint(node);

  return `
    <aside class="trace-selection-panel">
      <div class="eyebrow">NODE DETAIL</div>
      <strong>${escapeHTML(run.agent_id || '--')} · ${escapeHTML(run.status || 'idle')} · ${escapeHTML(traceNodePhaseLabel(node))}</strong>
      <div class="trace-overview-meta">
        ${statusPill(run.status || 'idle')}
        ${traceNodePhasePill(node)}
      </div>
      <p class="muted">${escapeHTML(summarizeTraceNode(node))}</p>
      <div class="mini-stat-list trace-selection-stats">
        <div class="mini-stat compact"><span class="mini-stat-label">Run</span><code>${escapeHTML(run.id || '--')}</code></div>
        <div class="mini-stat compact"><span class="mini-stat-label">Agent</span><strong>${escapeHTML(run.agent_id || '--')}</strong></div>
        <div class="mini-stat compact"><span class="mini-stat-label">Children</span><strong>${escapeHTML(children.length)}</strong></div>
        <div class="mini-stat compact"><span class="mini-stat-label">Descendants</span><strong>${escapeHTML(traceDescendantCount(node))}</strong></div>
        <div class="mini-stat compact"><span class="mini-stat-label">Related Activity</span><strong>${escapeHTML(relatedActivityCount)}</strong></div>
        <div class="mini-stat compact"><span class="mini-stat-label">Latest Event</span><strong>${escapeHTML(latestEvent?.name || 'waiting')}</strong></div>
      </div>
      <div class="trace-node-preview">
        <span class="eyebrow subtle">EXPLAINABILITY</span>
        <div class="muted">
          ${escapeHTML(metrics.memory ? `本节点共触发 ${metrics.memory} 次 memory recall。` : '本节点暂未触发 memory recall。')}
          ${escapeHTML(metrics.agentCalls ? ` 已发生 ${metrics.agentCalls} 次子 Agent 调用。` : '')}
          ${escapeHTML(metrics.tools ? ` 已发生 ${metrics.tools} 次工具调用。` : '')}
        </div>
      </div>
      ${agentCallFootprint ? `
        <div class="trace-node-preview">
          <span class="eyebrow subtle">SUBAGENTS</span>
          <div>${escapeHTML(agentCallFootprint)}</div>
        </div>
      ` : ''}
      ${toolFootprint ? `
        <div class="trace-node-preview">
          <span class="eyebrow subtle">TOOLS</span>
          <div>${escapeHTML(toolFootprint)}</div>
        </div>
      ` : ''}
      ${run.input ? `
        <div class="trace-node-preview">
          <span class="eyebrow subtle">INPUT</span>
          <div>${escapeHTML(truncate(run.input, 260))}</div>
        </div>
      ` : ''}
      ${run.output ? `
        <div class="trace-node-preview output">
          <span class="eyebrow subtle">OUTPUT</span>
          <div>${escapeHTML(truncate(run.output, 260))}</div>
        </div>
      ` : ''}
      ${run.error ? `
        <div class="trace-node-preview danger">
          <span class="eyebrow subtle">ERROR</span>
          <div>${escapeHTML(truncate(run.error, 260))}</div>
        </div>
      ` : ''}
      ${latestMemoryRecall?.payload?.entries?.length ? `
        <div class="trace-node-preview">
          <span class="eyebrow subtle">LATEST MEMORY RECALL</span>
          <div class="muted">${escapeHTML(summarizeMemoryPayload(latestMemoryRecall.payload))}</div>
          ${renderMemoryRecallList(latestMemoryRecall.payload.entries)}
        </div>
      ` : ''}
      ${latestCompaction?.payload ? `
        <div class="trace-node-preview">
          <span class="eyebrow subtle">LATEST COMPACTION</span>
          <div class="muted">${escapeHTML(summarizeCompactionPayload(latestCompaction.payload, latestCompaction.name))}</div>
          ${renderCompactionNotes(latestCompaction.payload)}
        </div>
      ` : ''}
      ${renderTraceNodeTimeline(events)}
    </aside>
  `;
}

function bindTraceTreeInteractions(host, tree, view, rerender) {
  if (!host) return;

  host.querySelectorAll('details.trace-node[data-trace-node]').forEach((details) => {
    details.addEventListener('toggle', () => {
      const runID = details.dataset.traceNode || '';
      if (!runID) return;
      view.selectedRunID = runID;
      view.expanded[runID] = details.open;
      rerender();
    });
  });

  host.querySelectorAll('[data-trace-filter="status"]').forEach((select) => {
    select.addEventListener('change', () => {
      view.filterStatus = select.value || 'all';
      rerender();
    });
  });

  host.querySelectorAll('[data-trace-filter="agent"]').forEach((select) => {
    select.addEventListener('change', () => {
      view.filterAgent = select.value || 'all';
      rerender();
    });
  });

  host.querySelectorAll('[data-trace-action]').forEach((button) => {
    button.addEventListener('click', (event) => {
      event.preventDefault();
      const action = button.dataset.traceAction || '';
      switch (action) {
        case 'focus-current': {
          const focusRunID = pickTraceFocus(tree)?.node?.run?.id || '';
          if (focusRunID) {
            view.selectedRunID = focusRunID;
          }
          break;
        }
        case 'expand-all':
          flattenTraceTree(tree).forEach(({ node }) => {
            if (node?.run?.id) view.expanded[node.run.id] = true;
          });
          break;
        case 'collapse-all':
          flattenTraceTree(tree).forEach(({ node }) => {
            if (node?.run?.id) view.expanded[node.run.id] = false;
          });
          break;
        case 'reset-filters':
          view.filterStatus = 'all';
          view.filterAgent = 'all';
          break;
        case 'toggle-related':
          view.relatedOnly = !view.relatedOnly;
          break;
        default:
          break;
      }
      rerender();
    });
  });
}

function renderTraceTree(tree, options = {}) {
  if (!Array.isArray(tree) || !tree.length) {
    return emptyState('当前还没有可展示的 RunTree。通常说明这条 run 尚未产生可嵌套的子运行。');
  }

  const view = options.view || createRunViewState();
  const compact = Boolean(options.compact);
  const surfaceClass = options.surfaceClass ? ` ${escapeHTML(options.surfaceClass)}` : '';
  const selectedRunID = ensureTraceViewSelection(tree, view, options.selectedRunID || '');
  const nodeMap = createTraceNodeMap(tree);
  const selectedNode = nodeMap.get(selectedRunID)?.node || pickTraceFocus(tree)?.node || tree[0];
  const ancestors = traceAncestorSet(tree, selectedRunID);
  const filteredTree = filterTraceTree(tree, view);
  const visibleTree = filteredTree.length ? filteredTree : [];
  const stats = traceTreeStats(tree);
  const visibleStats = traceTreeStats(visibleTree);
  const focus = pickTraceFocus(tree);
  const focusRun = focus?.node?.run || {};
  const focusEvent = latestTraceNodeEvent(focus?.node?.events || []);
  const relatedActivityCount = countRelatedActivityItems(options.activityItems || [], selectedRunID);
  const healthTone = stats.failed
    ? 'danger'
    : stats.running
      ? 'warning'
      : 'success';
  const agents = collectTraceAgents(tree);
  const statuses = collectTraceStatuses(tree);
  const context = { view, selectedRunID, ancestors };
  const runtime = traceTreeRuntimeStatus(tree, focusRun.status || 'idle');
  const liveStats = traceTreeOperationalStats(tree);
  const toolbar = `
    <div class="trace-toolbar ${compact ? 'compact' : ''}">
      <div class="trace-toolbar-group">
        <label class="trace-filter-field">状态
          <select data-trace-filter="status">
            <option value="all">全部状态</option>
            ${statuses.map((status) => `<option value="${escapeHTML(status.value)}" ${view.filterStatus === status.value ? 'selected' : ''}>${escapeHTML(status.label)}</option>`).join('')}
          </select>
        </label>
        <label class="trace-filter-field">Agent
          <select data-trace-filter="agent">
            <option value="all">全部 Agent</option>
            ${agents.map((agentID) => `<option value="${escapeHTML(agentID)}" ${view.filterAgent === agentID ? 'selected' : ''}>${escapeHTML(agentID)}</option>`).join('')}
          </select>
        </label>
      </div>
      <div class="trace-toolbar-group">
        <button class="ghost" data-trace-action="focus-current">${compact ? '定位焦点' : '定位当前焦点'}</button>
        <button class="ghost" data-trace-action="expand-all">${compact ? '展开' : '展开全部'}</button>
        <button class="ghost" data-trace-action="collapse-all">${compact ? '收起' : '收起全部'}</button>
        <button class="ghost" data-trace-action="toggle-related">${view.relatedOnly ? (compact ? '全部活动' : '显示全部活动') : (compact ? '节点活动' : '只看当前节点活动')}</button>
        <button class="ghost" data-trace-action="reset-filters">${compact ? '重置' : '重置过滤'}</button>
      </div>
    </div>
  `;

  if (compact) {
    return `
      <div class="trace-tree-shell compact${surfaceClass}">
        ${toolbar}
        <div class="trace-tree-stack compact">
          ${visibleTree.length ? visibleTree.map((node) => renderTraceNode(node, 0, context, null)).join('') : emptyState('当前过滤条件下没有可见的 RunTree 节点。', '<span class="muted tiny">可点击“重置过滤”恢复。</span>')}
        </div>
      </div>
    `;
  }

  return `
    <div class="trace-tree-shell${surfaceClass}">
      <div class="trace-overview-grid">
        <div class="trace-overview primary">
          <div class="eyebrow">CURRENT FOCUS</div>
          <strong>${escapeHTML(focusRun.agent_id || '--')} · ${escapeHTML(focusRun.status || 'idle')} · ${escapeHTML(traceNodePhaseLabel(focus?.node || null))}</strong>
          <p class="muted">${escapeHTML(focus ? summarizeTraceNode(focus.node) : '等待第一条运行事件')}</p>
          <div class="trace-overview-meta">
            ${statusPill(focusRun.status || 'idle')}
            ${traceNodePhasePill(focus?.node || null)}
            ${focusEvent ? `<span class="pill">${escapeHTML(focusEvent.name)}</span>` : '<span class="pill">waiting</span>'}
          </div>
        </div>
        <div class="trace-overview">
          <div class="eyebrow">TREE SHAPE</div>
          <strong>${escapeHTML(stats.nodes)} 节点 / ${escapeHTML(stats.maxDepth + 1)} 层</strong>
          <p class="muted">最上层是入口 run，向下每一层都是继续拆分出来的 agent 工作链。选中任一节点后，右侧会解释它最近在做什么，以及它与活动卡片的对应关系。</p>
          <div class="trace-overview-meta">
            <span class="pill">${escapeHTML(stats.leaves)} 叶子节点</span>
            <span class="pill">${escapeHTML(stats.completed)} 已完成</span>
            <span class="pill">${escapeHTML(visibleStats.nodes)} 当前可见</span>
          </div>
        </div>
        <div class="trace-overview ${escapeHTML(healthTone)}">
          <div class="eyebrow">RUNTIME HEALTH</div>
          <strong>${stats.failed ? `${stats.failed} 个失败节点` : stats.running ? `${stats.running} 个节点仍在执行` : '所有节点已收敛'}</strong>
          <p class="muted">${stats.failed ? '优先展开红色节点查看最近错误、上游来源和关联活动卡片。' : '优先聚焦 running/queued 节点，可以快速定位当前卡在哪个 agent、工具或 memory recall 上。'}</p>
          <div class="trace-overview-meta">
            ${statusPill(runtime.key, runtime.label)}
            <span class="pill">${escapeHTML(stats.running)} running</span>
            <span class="pill">${escapeHTML(stats.failed)} failed</span>
            ${liveStats.activeAgentCalls ? `<span class="pill">${escapeHTML(liveStats.activeAgentCalls)} sub active</span>` : ''}
            ${liveStats.agentCallsDone ? `<span class="pill">${escapeHTML(liveStats.agentCallsDone)} sub done</span>` : ''}
            <span class="pill">${escapeHTML(relatedActivityCount)} 关联活动</span>
          </div>
        </div>
      </div>
      ${toolbar}
      <div class="trace-tree-layout">
        <div class="trace-tree-stack">
          ${visibleTree.length ? visibleTree.map((node) => renderTraceNode(node, 0, context, null)).join('') : emptyState('当前过滤条件下没有可见的 RunTree 节点。', '<span class="muted tiny">可点击“重置过滤”恢复。</span>')}
        </div>
        ${renderTraceSelectionPanel(selectedNode, relatedActivityCount)}
      </div>
    </div>
  `;
}

function renderChatActivity() {
  updateChatSnippet();
}

function bindChatRuntimeSummary(host) {
  host?.querySelectorAll?.('[data-chat-runtime-open]').forEach((button) => {
    button.addEventListener('click', () => {
      if (appState.chat.runtimeDrawerOpen) return;
      appState.chat.runtimeDrawerOpen = true;
      renderChatActivity();
      renderChatTranscript();
    });
  });
}

function bindChatRuntimeSidebar(host) {
  host?.querySelectorAll?.('[data-chat-runtime-close]').forEach((button) => {
    button.addEventListener('click', () => {
      appState.chat.runtimeDrawerOpen = false;
      renderChatActivity();
      renderChatTranscript();
    });
  });
  const callStack = host?.querySelector?.('[data-chat-call-stack]');
  if (callStack) {
    callStack.addEventListener('toggle', () => {
      appState.chat.runView.latestStackOpen = callStack.open;
    });
  }
  host?.querySelectorAll?.('[data-chat-runtime-view]').forEach((button) => {
    button.addEventListener('click', () => {
      const nextView = button.dataset.chatRuntimeView || 'tree';
      if (appState.chat.runtimeDetailView === nextView) return;
      appState.chat.runtimeDetailView = nextView;
      renderChatActivity();
    });
  });
}

function updateChatSnippet() {
  updateChatRuntimeNote();
  const note = document.getElementById('chat-composer-note');
  if (note) {
    note.textContent = appState.chat.sessionID
      ? `写入 ${appState.chat.sessionID}`
      : '发送首条消息时会自动创建 session。';
  }
}

function renderRunDetail(run, events, runTree = [], runView = createRunViewState()) {
  const activity = buildActivityModel(events || []);
  const selectedRunID = ensureTraceViewSelection(runTree, runView, run.id);
  const nodeMap = createTraceNodeMap(runTree);
  const rootNode = nodeMap.get(run.id)?.node || runTree[0] || null;
  const relatedOnly = runView.relatedOnly && selectedRunID;
  const runItems = filterActivityItems(activity.items.filter((item) => item.kind === 'run'), selectedRunID, relatedOnly);
  const toolItems = filterActivityItems(activity.items.filter((item) => item.kind === 'tool'), selectedRunID, relatedOnly);
  const agentCallItems = filterActivityItems(activity.items.filter((item) => item.kind === 'agent_call'), selectedRunID, relatedOnly);
  const memoryItems = filterActivityItems(activity.items.filter((item) => item.kind === 'memory'), selectedRunID, relatedOnly);
  return `
    <div class="stack">
      <div class="detail-hero">
        <div>
          <div class="eyebrow">RUN DETAIL</div>
          <h3>${escapeHTML(run.agent_id)}</h3>
          <p class="muted">${escapeHTML(run.input || '无输入摘要')}</p>
        </div>
        <div class="toolbar">
          ${statusPill(run.status)}
          ${rootNode ? traceNodePhasePill(rootNode) : ''}
          <span class="pill">${escapeHTML(run.session_id)}</span>
          <span class="pill">${escapeHTML(run.id || 'no-run')}</span>
        </div>
      </div>
      <div class="mini-stat-list">
        <div class="mini-stat"><span class="mini-stat-label">Run ID</span><strong>${escapeHTML(run.id)}</strong></div>
        <div class="mini-stat"><span class="mini-stat-label">Started</span><strong>${escapeHTML(formatDateTime(run.started_at))}</strong></div>
        <div class="mini-stat"><span class="mini-stat-label">Completed</span><strong>${escapeHTML(formatDateTime(run.completed_at || run.started_at))}</strong></div>
        <div class="mini-stat"><span class="mini-stat-label">Output</span><strong>${escapeHTML((run.output || '').length)} chars</strong></div>
      </div>
      <div class="micro-panel trace-tree-panel">
        ${sectionHeader('RunTree', '把入口 run、子 agent 和最近状态按照层级可视化出来，便于普通人员快速理解整条子 Agent 调用链。')}
        ${renderTraceTree(runTree, {
          selectedRunID: run.id,
          view: runView,
          activityItems: activity.items,
        })}
      </div>
      ${run.output ? `<div class="micro-panel"><div class="eyebrow">FINAL OUTPUT</div><pre>${escapeHTML(run.output)}</pre></div>` : ''}
      ${run.error ? `<div class="micro-panel danger-panel"><div class="eyebrow">ERROR</div><pre>${escapeHTML(run.error)}</pre></div>` : ''}
      <div class="stack">
        ${sectionHeader('事件卡片', '工具调用、子 Agent 调用和运行完成状态会继续按卡片方式聚合，适合从树回到单节点细节。')}
        ${relatedOnly && selectedRunID ? `<div class="meta-note compact">当前只展示节点 <code>${escapeHTML(selectedRunID)}</code> 的关联活动。</div>` : ''}
        ${[
          renderActivityGroup('主运行节拍', '受理、开始、会话压缩、完成或失败等关键里程碑。', runItems, true, selectedRunID),
          renderActivityGroup('记忆召回', '帮助理解为什么这些上下文会被注入。', memoryItems, memoryItems.length > 0, selectedRunID),
          renderActivityGroup('工具调用', '工具输入、输出和失败信息会收敛在一起。', toolItems, toolItems.length > 0, selectedRunID),
          renderActivityGroup('子 Agent 调用', '展示主子 Agent 的拆分与回传。', agentCallItems, agentCallItems.length > 0, selectedRunID),
        ].join('')}
      </div>
    </div>
  `;
}

async function renderRuns() {
  const { runs } = await fetchJSON('/api/runs');
  appState.runs.list = runs;
  appState.runs.selectedRun = null;
  appState.runs.events = [];
  appState.runs.runTree = [];
  appState.runs.runView = createRunViewState();
  const selected = runs[0];

  content.innerHTML = `
    <section class="grid cards-2">
      <div class="panel">
        ${sectionHeader('运行列表', '选中一条运行后，右侧会同时显示最终输出、RunTree 和结构化活动卡片。')}
        <div id="run-list" class="list"></div>
      </div>
      <div class="panel">
        ${sectionHeader('运行详情', '可以把这里当作聊天侧边栏之外的详细诊断页，尤其适合看委派树。')}
        <div id="run-detail">${selected ? '<div class="muted">正在加载事件…</div>' : emptyState('暂无运行记录。')}</div>
      </div>
    </section>
  `;

  const runList = document.getElementById('run-list');
  const runDetail = document.getElementById('run-detail');

  runList.innerHTML = runs.length ? runs.map((run) => `
    <button class="list-selector" data-run-id="${escapeHTML(run.id)}">
      <div class="toolbar">
        <strong>${escapeHTML(run.agent_id)}</strong>
        ${statusPill(run.status)}
      </div>
      <div class="muted">${escapeHTML(truncate(run.input || run.session_id || run.id, 72))}</div>
      <div class="muted tiny">${escapeHTML(formatDateTime(run.started_at))}</div>
    </button>
  `).join('') : emptyState('暂无运行记录。');

  function paintRunDetail() {
    if (!appState.runs.selectedRun) {
      runDetail.innerHTML = emptyState('暂无运行记录。');
      return;
    }
    runDetail.innerHTML = renderRunDetail(
      appState.runs.selectedRun,
      appState.runs.events,
      appState.runs.runTree,
      appState.runs.runView,
    );
    if (appState.runs.runTree.length) {
      bindTraceTreeInteractions(runDetail, appState.runs.runTree, appState.runs.runView, paintRunDetail);
    }
  }

  async function loadRun(runID) {
    const [runPayload, eventPayload] = await Promise.all([
      fetchJSON(`/api/runs/${encodeURIComponent(runID)}`),
      fetchJSON(`/api/runs/${encodeURIComponent(runID)}/events`),
    ]);
    const runTree = runPayload.run?.id ? await fetchRunTree(runPayload.run.id) : [];
    appState.runs.selectedRun = runPayload.run;
    appState.runs.events = eventPayload.events || [];
    appState.runs.runTree = runTree;
    appState.runs.runView = createRunViewState();
    paintRunDetail();
  }

  runList.querySelectorAll('[data-run-id]').forEach((button) => {
    button.addEventListener('click', async () => {
      clearFlash();
      runList.querySelectorAll('.list-selector').forEach((node) => node.classList.remove('active'));
      button.classList.add('active');
      await loadRun(button.dataset.runId);
    });
  });

  if (selected) {
    const button = runList.querySelector('[data-run-id]');
    if (button) button.classList.add('active');
    await loadRun(selected.id);
  }
}

function renderTaskEventList(events) {
  const visible = (Array.isArray(events) ? events : []).filter((event) => String(event?.name || '').trim() !== 'task.running');
  if (!visible.length) return emptyState('这个任务当前只剩运行中状态，没有额外关键事件。');
  return visible.slice().reverse().map((event) => `
    <details class="activity-card ${escapeHTML((event.name || '').includes('failed') ? 'failed' : (event.name || '').includes('completed') ? 'completed' : 'running')}" ${event.name === 'task.failed' ? 'open' : ''}>
      <summary>
        <div>
          <span class="eyebrow subtle">TASK EVENT</span>
          <strong>${escapeHTML(event.name)}</strong>
          <p class="muted">${escapeHTML(summarizeTaskEvent(event))}</p>
        </div>
        <span class="tiny muted">${escapeHTML(formatDateTime(event.timestamp))}</span>
      </summary>
      ${renderJSONBlock(event.payload || {})}
    </details>
  `).join('');
}

function summarizeTaskEvent(event) {
  const payload = event?.payload || {};
  switch (event?.name) {
    case 'task.queued':
      return `任务已排队：${payload.target_agent || 'agent'}`;
    case 'task.started':
      return `任务开始执行：${payload.target_agent || 'agent'}`;
    case 'task.completed':
      return payload.summary || '任务执行完成';
    case 'task.failed':
      return payload.error || '任务执行失败';
    case 'task.cancelled':
      return payload.error || '任务已取消';
    default:
      return truncate((payload && safeJSON(payload)) || 'no payload', 96);
  }
}

async function renderTasks() {
  const { tasks } = await fetchJSON('/api/tasks');
  content.innerHTML = `
    <section class="grid cards-2">
      <div class="panel">
        ${sectionHeader('任务列表', '通过 run_id 和 session_id 追踪同一次运行中的任务、委派和工具执行。')}
        <div id="task-list" class="list"></div>
      </div>
      <div class="panel">
        ${sectionHeader('任务详情', '支持查看事件、状态和取消入口。')}
        <div id="task-detail">${tasks[0] ? '<div class="muted">正在加载任务事件…</div>' : emptyState('当前没有任务。')}</div>
      </div>
    </section>
  `;

  const taskList = document.getElementById('task-list');
  const taskDetail = document.getElementById('task-detail');

  taskList.innerHTML = tasks.length ? tasks.map((item) => `
    <button class="list-selector" data-task-id="${escapeHTML(item.id)}">
      <div class="toolbar">
        <strong>${escapeHTML(item.agent_id)}</strong>
        ${statusPill(item.status)}
      </div>
      <div class="muted">${escapeHTML(truncate(item.input || item.summary || item.id, 72))}</div>
      <div class="muted tiny">${escapeHTML(formatDateTime(item.created_at))}</div>
    </button>
  `).join('') : emptyState('当前没有任务。');

  async function loadTask(taskID) {
    const [taskPayload, eventPayload] = await Promise.all([
      fetchJSON(`/api/tasks/${encodeURIComponent(taskID)}`),
      fetchJSON(`/api/tasks/${encodeURIComponent(taskID)}/events`),
    ]);
    const task = taskPayload.task;
    taskDetail.innerHTML = `
      <div class="stack">
        <div class="detail-hero">
          <div>
            <div class="eyebrow">TASK DETAIL</div>
            <h3>${escapeHTML(task.agent_id)}</h3>
            <p class="muted">${escapeHTML(task.input || task.summary || '无任务摘要')}</p>
          </div>
          <div class="toolbar">
            ${statusPill(task.status)}
          </div>
        </div>
        <div class="mini-stat-list">
          <div class="mini-stat"><span class="mini-stat-label">Task ID</span><strong>${escapeHTML(task.id)}</strong></div>
          <div class="mini-stat"><span class="mini-stat-label">Run</span><strong>${escapeHTML(task.run_id || '--')}</strong></div>
          <div class="mini-stat"><span class="mini-stat-label">Session</span><strong>${escapeHTML(task.session_id || '--')}</strong></div>
          <div class="mini-stat"><span class="mini-stat-label">Created</span><strong>${escapeHTML(formatDateTime(task.created_at))}</strong></div>
        </div>
        ${task.status === 'running' || task.status === 'queued' ? `<div class="toolbar"><button id="task-cancel">取消任务</button></div>` : ''}
        <div class="stack">${renderTaskEventList(eventPayload.events || [])}</div>
      </div>
    `;

    const cancelButton = document.getElementById('task-cancel');
    if (cancelButton) {
      cancelButton.addEventListener('click', async () => {
        await fetchJSON(`/api/tasks/${encodeURIComponent(task.id)}/cancel`, { method: 'POST' });
        showFlash(`任务 ${task.id} 已取消`);
        await renderTasks();
      });
    }
  }

  taskList.querySelectorAll('[data-task-id]').forEach((button) => {
    button.addEventListener('click', async () => {
      clearFlash();
      taskList.querySelectorAll('.list-selector').forEach((node) => node.classList.remove('active'));
      button.classList.add('active');
      await loadTask(button.dataset.taskId);
    });
  });

  if (tasks[0]) {
    const button = taskList.querySelector('[data-task-id]');
    if (button) button.classList.add('active');
    await loadTask(tasks[0].id);
  }
}

async function renderJobs() {
  const { jobs } = await fetchJSON('/api/jobs');
  content.innerHTML = `
    <section class="panel">
      ${sectionHeader('计划任务', '支持暂停、恢复、删除；适合对接运维或低频自动任务。')}
      <div class="list">
        ${jobs.length ? jobs.map((job) => `
          <article class="list-item">
            <div class="toolbar">
              <strong>${escapeHTML(job.name)}</strong>
              ${statusPill(job.paused ? 'paused' : 'running')}
              <span class="pill">${escapeHTML(job.schedule)}</span>
            </div>
            <p class="muted">${escapeHTML(job.prompt)}</p>
            <div class="toolbar">
              <button data-job-action="${job.paused ? 'resume' : 'pause'}" data-job-name="${escapeHTML(job.name)}">${job.paused ? '恢复' : '暂停'}</button>
              <button class="ghost" data-job-action="remove" data-job-name="${escapeHTML(job.name)}">删除</button>
            </div>
          </article>
        `).join('') : emptyState('当前没有计划任务。')}
      </div>
    </section>
  `;

  document.querySelectorAll('[data-job-action]').forEach((button) => {
    button.addEventListener('click', async () => {
      clearFlash();
      const action = button.dataset.jobAction;
      const name = button.dataset.jobName;
      await fetchJSON(`/api/jobs/${encodeURIComponent(name)}/${action}`, { method: 'POST' });
      showFlash(`任务 ${name} 已执行 ${action}`);
      await renderJobs();
    });
  });
}

async function renderLogs() {
  const { entries } = await fetchJSON('/api/logs');
  content.innerHTML = `
    <section class="panel">
      ${sectionHeader('日志流', '页面先回放最近日志，再订阅增量日志。')}
      <div class="toolbar">
        <label class="stack-field">过滤
          <input id="log-filter" placeholder="输入关键词过滤日志">
        </label>
      </div>
      <div id="log-stream" class="activity-stack"></div>
    </section>
  `;

  const logStream = document.getElementById('log-stream');
  const filterInput = document.getElementById('log-filter');
  const lines = [...entries];

  const draw = () => {
    const filter = filterInput.value.trim().toLowerCase();
    const filtered = lines.filter((line) => !filter || safeJSON(line).toLowerCase().includes(filter));
    logStream.innerHTML = filtered.length ? filtered.slice().reverse().map((entry) => `
      <details class="activity-card ${escapeHTML(String(entry.level || '').toLowerCase() === 'error' ? 'failed' : 'completed')}">
        <summary>
          <div>
            <span class="eyebrow subtle">LOG</span>
            <strong>${escapeHTML(entry.level)}</strong>
            <p class="muted">${escapeHTML(entry.message)}</p>
          </div>
          <span class="tiny muted">${escapeHTML(formatDateTime(entry.time))}</span>
        </summary>
        ${renderJSONBlock(entry)}
      </details>
    `).join('') : emptyState('还没有日志。');
  };

  draw();
  filterInput.addEventListener('input', draw);

  const source = new EventSource(withAuthQuery('/api/logs/stream'));
  source.addEventListener('log', (event) => {
    lines.push(JSON.parse(event.data));
    if (lines.length > 300) lines.shift();
    draw();
  });
  source.onerror = () => source.close();
}

function renderEndpointCard(endpoint) {
  return `
    <article class="endpoint-card">
      <div class="toolbar">
        ${methodPill(endpoint.method)}
        <strong>${escapeHTML(endpoint.path)}</strong>
        <span class="pill">${escapeHTML(endpoint.transport)}</span>
        <span class="pill">${escapeHTML(endpoint.group)}</span>
      </div>
      <p>${escapeHTML(endpoint.summary)}</p>
      <p class="muted">${escapeHTML(endpoint.description)}</p>
      ${endpoint.query_fields?.length ? `<div class="doc-subhead">Query</div>${renderFieldList(endpoint.query_fields)}` : ''}
      ${endpoint.request_fields?.length ? `<div class="doc-subhead">Request</div>${renderFieldList(endpoint.request_fields)}` : ''}
      ${endpoint.response_fields?.length ? `<div class="doc-subhead">Response</div>${renderFieldList(endpoint.response_fields)}` : ''}
      ${endpoint.events?.length ? `<div class="tag-row">${endpoint.events.map((event) => `<span class="pill">${escapeHTML(event)}</span>`).join('')}</div>` : ''}
      ${endpoint.example ? `<div class="doc-subhead">Request Example</div>${renderJSONBlock(endpoint.example)}` : ''}
      ${endpoint.response_example ? `<div class="doc-subhead">Response Example</div>${renderJSONBlock(endpoint.response_example)}` : ''}
      ${endpoint.notes?.length ? `<div class="note-list">${endpoint.notes.map((note) => `<div class="meta-note compact">${escapeHTML(note)}</div>`).join('')}</div>` : ''}
      ${endpoint.curl ? `<div class="doc-subhead">cURL</div><pre>${escapeHTML(endpoint.curl)}</pre>` : ''}
    </article>
  `;
}

async function renderAPI() {
  const [payload, inventoryPayload] = await Promise.all([
    fetchJSON('/api/catalog'),
    fetchJSON('/api/agents'),
  ]);
  const catalog = payload.catalog || { endpoints: payload.endpoints || [] };
  const inventory = rememberAgentInventory(inventoryPayload);
  const groupedEndpoints = (catalog.endpoints || []).reduce((accumulator, endpoint) => {
    const group = endpoint.group || 'Other';
    accumulator[group] = accumulator[group] || [];
    accumulator[group].push(endpoint);
    return accumulator;
  }, {});

  content.innerHTML = `
    <section class="hero">
      <div class="hero-grid">
        <div>
          <div class="eyebrow">ENGINEER INTEGRATION GUIDE</div>
          <h2>${escapeHTML(catalog.title || 'AnyAI API')}</h2>
          <p class="muted">${escapeHTML(catalog.overview || '')}</p>
        </div>
        <div class="panel panel-soft">
          <h3>接入基线</h3>
          <div class="list">
            <div class="list-item">Base URL：<code>${escapeHTML(catalog.base_url || window.location.origin)}</code></div>
            <div class="list-item">鉴权：${catalog.auth?.required ? '需要 Bearer token' : '默认关闭'}</div>
            <div class="list-item">Header：<code>${escapeHTML(catalog.auth?.header || 'Authorization: Bearer <token>')}</code></div>
            <div class="list-item">SSE 降级：EventSource 可使用查询参数 <code>${escapeHTML(catalog.auth?.query_param || 'token')}</code></div>
          </div>
        </div>
      </div>
    </section>

    <section class="panel">
      <div class="section-nav">
        <a href="#workflow-chat">会话工作流</a>
        <a href="#agent-docs">Agent 能力</a>
        <a href="#endpoint-docs">HTTP Endpoints</a>
        <a href="#event-docs">SSE 事件</a>
        <a href="#schema-docs">Schema</a>
      </div>
    </section>

    <section class="panel" id="workflow-chat">
      ${sectionHeader('典型接入流程', '建议其他 agent 工程师先按这里的最小闭环打通，再继续扩展 run tree 和 task 观测。')}
      <div class="workflow-grid">
        ${(catalog.workflows || []).map((workflow) => `
          <article class="workflow-card">
            <div class="toolbar">
              <strong>${escapeHTML(workflow.name)}</strong>
              ${workflow.outcome ? `<span class="pill">Outcome</span>` : ''}
            </div>
            <p class="muted">${escapeHTML(workflow.summary || '')}</p>
            ${workflow.outcome ? `<div class="meta-note compact">${escapeHTML(workflow.outcome)}</div>` : ''}
            <ol class="step-list">
              ${(workflow.steps || []).map((step) => `
                <li>
                  <strong>${escapeHTML(step.title)}</strong>
                  <div class="muted">${escapeHTML([step.method, step.path].filter(Boolean).join(' '))}</div>
                  <p>${escapeHTML(step.description || '')}</p>
                </li>
              `).join('')}
            </ol>
          </article>
        `).join('')}
      </div>
    </section>

    <section class="panel" id="agent-docs">
      ${sectionHeader('Agent / Skill / Tool 能力画像', '接入前先确认谁是入口 agent，哪些 agent 适合直连，以及每个 agent 拥有哪些工具和技能。')}
      <div class="agent-matrix">
        ${(inventory.agents || []).map((agent) => renderAgentCapabilityCard(agent)).join('')}
      </div>
      ${inventory.notes?.length ? `<div class="note-list">${inventory.notes.map((note) => `<div class="meta-note compact">${escapeHTML(note)}</div>`).join('')}</div>` : ''}
    </section>

    <section class="panel" id="transport-docs">
      ${sectionHeader('Transport 选择', '默认推荐 HTTP + SSE：请求语义简单，回放与实时流也都统一。')}
      <div class="grid cards-3">
        ${(catalog.transports || []).map((transport) => `
          <article class="surface-card">
            <div class="toolbar">
              <strong>${escapeHTML(transport.name)}</strong>
            </div>
            <p class="muted">${escapeHTML(transport.summary || '')}</p>
            ${transport.best_for?.length ? `<div class="tag-row">${transport.best_for.map((item) => `<span class="pill">${escapeHTML(item)}</span>`).join('')}</div>` : ''}
            ${transport.entrypoints?.length ? `<pre>${escapeHTML(transport.entrypoints.join('\n'))}</pre>` : ''}
          </article>
        `).join('')}
      </div>
    </section>

    <section class="panel" id="endpoint-docs">
      ${sectionHeader('HTTP / SSE Endpoints', '字段级说明、事件类型和请求样例都在这里，适合直接给 SDK 或调用方看。')}
      ${Object.entries(groupedEndpoints).map(([group, endpoints]) => `
        <div class="doc-group">
          <div class="doc-subhead">${escapeHTML(group)}</div>
          <div class="endpoint-list">
            ${endpoints.map(renderEndpointCard).join('')}
          </div>
        </div>
      `).join('')}
    </section>

    <section class="panel" id="event-docs">
      ${sectionHeader('SSE 事件契约', '运行、任务和日志会通过统一事件结构流出，前端可以直接映射为状态卡片。')}
      <div class="endpoint-list">
        ${(catalog.event_types || []).map((event) => `
          <article class="endpoint-card">
            <div class="toolbar">
              <strong>${escapeHTML(event.name)}</strong>
              <span class="pill">${escapeHTML(event.channel)}</span>
              ${event.terminal ? '<span class="pill warn">terminal</span>' : ''}
            </div>
            <p>${escapeHTML(event.summary || '')}</p>
            ${event.triggered_by ? `<p class="muted">触发时机：${escapeHTML(event.triggered_by)}</p>` : ''}
            ${renderFieldList(event.fields || [])}
          </article>
        `).join('')}
      </div>
    </section>

    <section class="panel" id="schema-docs">
      ${sectionHeader('Payload Schemas', '适合生成类型定义，或者给其他语言 SDK 做结构映射。')}
      <div class="endpoint-list">
        ${(catalog.schemas || []).map((schema) => `
          <article class="endpoint-card">
            <div class="toolbar">
              <strong>${escapeHTML(schema.name)}</strong>
              <span class="pill">Schema</span>
            </div>
            <p>${escapeHTML(schema.summary || '')}</p>
            ${schema.description ? `<p class="muted">${escapeHTML(schema.description)}</p>` : ''}
            ${renderFieldList(schema.fields || [])}
            ${schema.example ? `<div class="doc-subhead">Example</div>${renderJSONBlock(schema.example)}` : ''}
          </article>
        `).join('')}
      </div>
      ${catalog.notes?.length ? `<div class="note-list">${catalog.notes.map((note) => `<div class="meta-note compact">${escapeHTML(note)}</div>`).join('')}</div>` : ''}
    </section>
  `;
}

async function renderSettings() {
  const response = await fetch(withAuthQuery('/api/config'));
  const text = await response.text();
  if (!response.ok) {
    throw new Error(text || '配置读取失败');
  }

  content.innerHTML = `
    <section class="panel">
      ${sectionHeader('配置编辑', '这里直接编辑运行时配置 JSON，保存后会通知后台刷新。', '<button id="config-save">保存配置</button>')}
      <textarea id="config-editor">${escapeHTML(text)}</textarea>
    </section>
  `;

  document.getElementById('config-save').addEventListener('click', async () => {
    clearFlash();
    const editor = document.getElementById('config-editor');
    try {
      await fetchJSON('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: editor.value,
      });
      showFlash('配置已保存，并已通知运行时刷新。');
    } catch (error) {
      showFlash(error.message || String(error), true);
    }
  });
}
