const status = document.querySelector('#status');
const button = document.querySelector('#refresh');
const onlineOnly = document.querySelector('#online-only');
const nextButton = document.querySelector('#next');
let cursor = '';
let nextCursor = '';
let pending = false;

async function refresh() {
  if (pending) return;
  pending = true;
  button.disabled = true;
  try {
    const response = await fetch('/readyz', { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    status.textContent = response.ok ? 'Сервер готов к работе.' : 'Сервер временно не готов. Повторите проверку позже.';
    if (!response.ok) return;
    const params = new URLSearchParams({ cursor, connected: String(onlineOnly.checked) });
    const listResponse = await fetch(`/api/v1/mobius/loki?${params}`, { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    if (!listResponse.ok) throw new Error('list unavailable');
    const list = await listResponse.json();
    const rows = list.items.map((loki) => {
      const row = document.createElement('tr');
      for (const text of [loki.display_name, loki.id, loki.connected ? 'Подключён' : 'Отключён', loki.last_seen_at ? new Date(loki.last_seen_at).toLocaleString() : '—']) {
        const cell = document.createElement('td');
        cell.textContent = text;
        row.append(cell);
      }
      return row;
    });
    document.querySelector('#loki-list').replaceChildren(...rows);
    document.querySelector('#empty').hidden = list.items.length !== 0;
    nextCursor = list.next_cursor || '';
    nextButton.disabled = !nextCursor;
  } catch {
    status.textContent = 'Нет связи с сервером.';
  } finally {
    button.disabled = false;
    pending = false;
  }
}

button.addEventListener('click', refresh);
onlineOnly.addEventListener('change', () => { cursor = ''; refresh(); });
document.querySelector('#first').addEventListener('click', () => { cursor = ''; refresh(); });
nextButton.addEventListener('click', () => { cursor = nextCursor; refresh(); });
refresh();
setInterval(refresh, 1000);
