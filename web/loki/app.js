const form = document.querySelector('#connection');
const serverInput = document.querySelector('#server');
const nameInput = document.querySelector('#name');
const statusLine = document.querySelector('#status');
const errorLine = document.querySelector('#error');
const connectButton = document.querySelector('#connect');
const disconnectButton = document.querySelector('#disconnect');
let pending = false;
let loading = false;
let initialized = false;
let actionError = '';
let version = 0;

function render(state) {
  const labels = { disconnected: 'Отключён', connecting: 'Подключаемся…', connected: 'Подключён', error: 'Ошибка подключения' };
  statusLine.textContent = labels[state.status] || 'Состояние неизвестно';
  errorLine.textContent = state.error || actionError;
  if (!initialized) {
    serverInput.value = state.server_address || 'http://127.0.0.1:8081';
    initialized = true;
  }
  document.querySelector('#identity').hidden = !state.identity;
  document.querySelector('#display-name').textContent = state.identity?.display_name || '';
  document.querySelector('#loki-id').textContent = state.identity?.id || '';
}

async function refresh() {
  if (loading || pending) return;
  loading = true;
  const requestVersion = version;
  try {
    const response = await fetch('/api/v1/state', { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    if (!response.ok) throw new Error('Не удалось получить состояние клиента.');
    const state = await response.json();
    if (requestVersion === version && !pending) render(state);
  } catch {
    statusLine.textContent = 'Нет связи с приложением Локи';
  } finally {
    loading = false;
  }
}

async function change(method, body) {
  if (pending) return;
  pending = true;
  version += 1;
  actionError = '';
  connectButton.disabled = disconnectButton.disabled = true;
  errorLine.textContent = '';
  try {
    const response = await fetch('/api/v1/connection', {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(9000),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error?.message || 'Не удалось изменить подключение.');
    render(result);
  } catch (error) {
    actionError = error.message || 'Нет связи с приложением Локи.';
    errorLine.textContent = actionError;
  } finally {
    pending = false;
    connectButton.disabled = disconnectButton.disabled = false;
  }
}

form.addEventListener('submit', (event) => {
  event.preventDefault();
  change('POST', { server_address: serverInput.value, display_name: nameInput.value });
});
disconnectButton.addEventListener('click', () => change('DELETE'));
refresh();
setInterval(refresh, 1000);
