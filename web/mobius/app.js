const status = document.querySelector('#status');
const button = document.querySelector('#refresh');
const onlineOnly = document.querySelector('#online-only');
const nextButton = document.querySelector('#next');
let cursor = '';
let nextCursor = '';
let pending = false;
let selected = null;
let serverOffset = 0;
let invitePending = false;
let draft = null;
const inviteButton = document.querySelector('#invite');
const sideInput = document.querySelector('#mobius-side');

function renderSelected() {
  document.querySelector('#selected').hidden = !selected;
  if (!selected) return;
  document.querySelector('#selected-name').textContent = `${selected.display_name} (${selected.id})`;
  const invitation = selected.invitation;
  const labels = { pending: 'Ожидает ответа', accepted: 'Принято', declined: 'Отклонено', expired: 'Время ответа истекло', cancelled: 'Отменено' };
  let text = invitation ? labels[invitation.status] : 'Приглашений ещё нет.';
  if (invitation?.status === 'pending') {
    const seconds = Math.max(0, Math.ceil((Date.parse(invitation.expires_at) - Date.now() - serverOffset) / 1000));
    text += ` — осталось ${seconds} сек.`;
  }
  document.querySelector('#invitation-status').textContent = text;
  document.querySelector('#invitation-statistics').textContent = invitation?.statistics_snapshot.message || '';
  document.querySelector('#active-game').textContent = selected.game
    ? `Партия ${selected.game.id}. Мобиус: ${selected.game.mobius_side}, Локи: ${selected.game.loki_side}. Первым ходит ${selected.game.next_actor === 'mobius' ? 'Мобиус' : 'Локи'}.`
    : (invitation?.status === 'accepted' ? 'Незаконченная партия удалена.' : '');
  const retry = draft?.owner === selected.id && draft.side === sideInput.value;
  inviteButton.textContent = retry ? 'Повторить запрос' : 'Пригласить';
  inviteButton.disabled = invitePending || !selected.connected || (!retry && (!!selected.game || invitation?.status === 'pending'));
}

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
    serverOffset = Date.parse(list.server_time) - Date.now();
    const rows = list.items.map((loki) => {
      const row = document.createElement('tr');
      for (const text of [loki.display_name, loki.id, loki.connected ? 'Подключён' : 'Отключён', loki.last_seen_at ? new Date(loki.last_seen_at).toLocaleString() : '—']) {
        const cell = document.createElement('td');
        cell.textContent = text;
        row.append(cell);
      }
      const controls = document.createElement('td');
      const select = document.createElement('button');
      select.type = 'button';
      select.textContent = 'Выбрать';
      select.addEventListener('click', () => { selected = loki; document.querySelector('#invite-error').textContent = ''; renderSelected(); });
      controls.append(select);
      row.append(controls);
      return row;
    });
    document.querySelector('#loki-list').replaceChildren(...rows);
    document.querySelector('#empty').hidden = list.items.length !== 0;
    nextCursor = list.next_cursor || '';
    nextButton.disabled = !nextCursor;
    if (selected) selected = list.items.find((loki) => loki.id === selected.id) || null;
    renderSelected();
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
sideInput.addEventListener('change', renderSelected);
document.querySelector('#invite-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!selected || inviteButton.disabled) return;
  const owner = selected.id;
  const side = sideInput.value;
  if (draft?.owner !== owner || draft.side !== side) draft = { owner, side, id: crypto.randomUUID() };
  invitePending = true;
  renderSelected();
  document.querySelector('#invite-error').textContent = '';
  try {
    const response = await fetch(`/api/v1/mobius/loki/${owner}/invitations`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ request_id: draft.id, mobius_side: side }), signal: AbortSignal.timeout(9000),
    });
    const result = await response.json();
    if (!response.ok) {
      if (response.status < 500) draft = null;
      const messages = { client_busy: 'Локи уже занят приглашением или партией.', client_offline: 'Локи отключён.', conflict: 'Этот запрос уже использован с другими параметрами.' };
      throw new Error(messages[result.error?.code] || 'Не удалось отправить приглашение.');
    }
    if (selected?.id === owner) selected.invitation = result;
    draft = null;
  } catch (error) {
    document.querySelector('#invite-error').textContent = error.message || 'Не удалось отправить приглашение. Повторите запрос.';
  } finally {
    invitePending = false;
    renderSelected();
    refresh();
  }
});
refresh();
setInterval(refresh, 1000);
setInterval(renderSelected, 200);
