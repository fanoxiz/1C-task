import { GamePanel, moveBody, moveResponse } from '/game.js';
import { scoreText } from '/statistics.js';

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
let gameCursor = '';
let nextGameCursor = '';
let selectedGameId = '';
let gameVersion = 0;
let historical = false;
let historyCursor = '';
let nextHistoryCursor = '';
let historyVersion = 0;
let available = false;
const gamePanel = new GamePanel(document.querySelector('#game-panel'), 'mobius', async (move) => {
  gameVersion += 1;
  try {
    return await moveResponse(await fetch(`/api/v1/mobius/games/${move.gameId}/moves`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(moveBody(move)), signal: AbortSignal.timeout(9000) }));
  } finally { gameVersion += 1; }
}, refreshGame);

async function refreshGame() {
  if (!selectedGameId) { gamePanel.render(null); return; }
  const id = selectedGameId;
  const version = gameVersion;
  try {
    const response = await fetch(`/api/v1/mobius/games/${id}`, { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    if (id !== selectedGameId || version !== gameVersion) return;
    if (response.status === 404) { selectedGameId = ''; gamePanel.render(null); return; }
    if (!response.ok) throw new Error('game unavailable');
    const game = await response.json();
    if (id === selectedGameId && version === gameVersion) gamePanel.render(game, true);
  } catch {
    if (id === selectedGameId && version === gameVersion) gamePanel.render(gamePanel.game, false);
  }
}

async function refreshHistory() {
  if (!selected) return;
  const owner = selected.id;
  const version = ++historyVersion;
  try {
    const params = new URLSearchParams({ cursor: historyCursor });
    const response = await fetch(`/api/v1/mobius/loki/${owner}/games?${params}`, { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    if (!response.ok) throw new Error('history unavailable');
    const history = await response.json();
    if (selected?.id !== owner || version !== historyVersion) return;
    nextHistoryCursor = history.next_cursor || '';
    document.querySelector('#history-next').disabled = !nextHistoryCursor;
    document.querySelector('#history-status').textContent = history.items.length ? '' : 'Завершённых партий пока нет.';
    const results = { loki_win: 'Победил Локи', mobius_win: 'Победил Мобиус', draw: 'Ничья' };
    document.querySelector('#history').replaceChildren(...history.items.map((game) => {
      const item = document.createElement('li');
      const open = document.createElement('button');
      open.type = 'button';
      open.textContent = `${new Date(game.finished_at).toLocaleString()} · ${results[game.result]} · Мобиус: ${game.mobius_side} · ${game.id}`;
      open.addEventListener('click', () => {
        historical = true;
        selectedGameId = game.id;
        gameVersion += 1;
        gamePanel.render(null);
        refreshGame();
      });
      item.append(open);
      return item;
    }));
  } catch {
    if (selected?.id === owner && version === historyVersion) document.querySelector('#history-status').textContent = 'Не удалось обновить историю.';
  }
}

function selectLoki(loki) {
  selected = loki;
  historical = false;
  historyCursor = nextHistoryCursor = '';
  document.querySelector('#history').replaceChildren();
  document.querySelector('#history-status').textContent = 'Загружаем историю…';
  document.querySelector('#history-next').disabled = true;
  selectedGameId = loki.game?.id || loki.invitation?.game_id || '';
  gameVersion += 1;
  gamePanel.render(null);
  document.querySelector('#invite-error').textContent = '';
  renderSelected();
  refreshGame();
  refreshHistory();
}
const inviteButton = document.querySelector('#invite');
const sideInput = document.querySelector('#mobius-side');

function renderSelected() {
  document.querySelector('#selected').hidden = !selected;
  if (!selected) return;
  document.querySelector('#selected-name').textContent = `${selected.display_name} (${selected.id})`;
  document.querySelector('#selected-statistics').textContent = scoreText(selected.statistics);
  const invitation = selected.invitation;
  const labels = { pending: 'Ожидает ответа', accepted: 'Принято', declined: 'Отклонено', expired: 'Время ответа истекло', cancelled: 'Отменено' };
  let text = invitation ? labels[invitation.status] : 'Приглашений ещё нет.';
  if (invitation?.status === 'pending') {
    const seconds = Math.max(0, Math.ceil((Date.parse(invitation.expires_at) - Date.now() - serverOffset) / 1000));
    text += ` — осталось ${seconds} сек.`;
  }
  document.querySelector('#invitation-status').textContent = text;
  document.querySelector('#invitation-statistics').textContent = invitation
    ? `На момент приглашения: ${scoreText(invitation.statistics_snapshot)} ${invitation.statistics_snapshot.message}` : '';
  document.querySelector('#active-game').textContent = selected.game
    ? `Партия ${selected.game.id}. Мобиус: ${selected.game.mobius_side}, Локи: ${selected.game.loki_side}. Ходит ${selected.game.next_actor === 'mobius' ? 'Мобиус' : 'Локи'}.`
    : (invitation?.status === 'accepted' ? (invitation.game_id ? 'Партия завершена.' : 'Незаконченная партия удалена.') : '');
  const retry = draft?.owner === selected.id && draft.side === sideInput.value;
  inviteButton.textContent = invitePending ? 'Отправляем…' : (retry ? 'Повторить запрос' : 'Пригласить');
  inviteButton.disabled = invitePending || !available || !selected.connected || (!retry && (!!selected.game || invitation?.status === 'pending'));
}

async function refresh() {
  if (pending) return;
  pending = true;
  button.disabled = true;
  try {
    const response = await fetch('/readyz', { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    available = response.ok;
    status.textContent = response.ok ? 'Сервер готов к работе.' : 'Сервер временно не готов. Повторите проверку позже.';
    if (!response.ok) { gamePanel.render(gamePanel.game, false); renderSelected(); return; }
    const params = new URLSearchParams({ cursor, connected: String(onlineOnly.checked) });
    const listResponse = await fetch(`/api/v1/mobius/loki?${params}`, { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    if (!listResponse.ok) throw new Error('list unavailable');
    const list = await listResponse.json();
    serverOffset = Date.parse(list.server_time) - Date.now();
    const rows = list.items.map((loki) => {
      const row = document.createElement('tr');
      for (const text of [loki.display_name, loki.id, loki.connected ? 'Подключён' : 'Отключён', loki.last_seen_at ? new Date(loki.last_seen_at).toLocaleString() : '—', scoreText(loki.statistics)]) {
        const cell = document.createElement('td');
        cell.textContent = text;
        row.append(cell);
      }
      const controls = document.createElement('td');
      const select = document.createElement('button');
      select.type = 'button';
      select.textContent = 'Выбрать';
      select.addEventListener('click', () => selectLoki(loki));
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
    if (!historical && selected?.game && selectedGameId !== selected.game.id) {
      selectedGameId = selected.game.id;
      gameVersion += 1;
      gamePanel.render(null);
    }
    const gamesResponse = await fetch(`/api/v1/mobius/games?status=active&cursor=${gameCursor}`, { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    if (!gamesResponse.ok) throw new Error('games unavailable');
    const games = await gamesResponse.json();
    nextGameCursor = games.next_cursor || '';
    document.querySelector('#games-next').disabled = !nextGameCursor;
    document.querySelector('#active-games').replaceChildren(...games.items.map((game) => {
      const item = document.createElement('li');
      const open = document.createElement('button');
      open.type = 'button';
      open.textContent = `Локи ${game.loki_id} · партия ${game.id}`;
      open.addEventListener('click', () => { selected = null; historical = false; selectedGameId = game.id; gameVersion += 1; gamePanel.render(null); renderSelected(); refreshGame(); });
      item.append(open);
      return item;
    }));
    await Promise.all([refreshGame(), refreshHistory()]);
  } catch {
    available = false;
    status.textContent = 'Нет связи с сервером.';
    gamePanel.render(gamePanel.game, false);
    renderSelected();
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
document.querySelector('#games-first').addEventListener('click', () => { gameCursor = ''; refresh(); });
document.querySelector('#games-next').addEventListener('click', () => { gameCursor = nextGameCursor; refresh(); });
document.querySelector('#history-first').addEventListener('click', () => { historyCursor = ''; refreshHistory(); });
document.querySelector('#history-next').addEventListener('click', () => { historyCursor = nextHistoryCursor; refreshHistory(); });
refresh();
setInterval(refresh, 1000);
setInterval(renderSelected, 200);
