import { GamePanel, moveBody, moveResponse } from '/game.js';
import { scoreText } from '/statistics.js';

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
let currentState = null;
let invitationDeadline = 0;
let localAvailable = false;
const gamePanel = new GamePanel(document.querySelector('#game'), 'loki', async (draft) => {
  pending = true;
  version += 1;
  connectButton.disabled = disconnectButton.disabled = true;
  try {
    const response = await fetch(`/api/v1/games/${draft.gameId}/moves`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(moveBody(draft)), signal: AbortSignal.timeout(9000) });
    const state = await moveResponse(response);
    render(state);
    return state.game;
  } finally {
    pending = false;
    version += 1;
    connectButton.disabled = disconnectButton.disabled = false;
  }
}, refresh);

function countdown() {
  const seconds = Math.max(0, Math.ceil((invitationDeadline - Date.now()) / 1000));
  document.querySelector('#invitation-timer').textContent = `${seconds} сек.`;
  const disabled = pending || !localAvailable || currentState?.status !== 'connected' || seconds === 0;
  document.querySelector('#accept').disabled = document.querySelector('#decline').disabled = disabled;
}

function render(state) {
  localAvailable = true;
  if (state.invitation && state.invitation.id !== currentState?.invitation?.id) actionError = '';
  currentState = state;
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
  document.querySelector('#own-statistics').textContent = `${scoreText(state.statistics)} ${state.statistics.message}`;
  const invitation = state.invitation;
  document.querySelector('#notice').textContent = state.notice || '';
  document.querySelector('#invitation').hidden = !invitation;
  if (invitation) {
    invitationDeadline = Date.now() + Date.parse(invitation.expires_at) - Date.parse(state.server_time)
      - Math.max(0, Date.now() - Date.parse(state.observed_at));
    document.querySelector('#invitation-side').textContent = `Мобиус играет ${invitation.mobius_side}. Вы играете ${invitation.mobius_side === 'X' ? 'O' : 'X'}.`;
    document.querySelector('#statistics').textContent = `На момент приглашения: ${scoreText(invitation.statistics_snapshot)} ${invitation.statistics_snapshot.message}`;
  }
  gamePanel.render(state.game, state.status === 'connected');
  countdown();
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
    if (requestVersion === version && !pending) {
      localAvailable = false;
      statusLine.textContent = 'Нет связи с приложением Локи';
      gamePanel.render(gamePanel.game, false);
      countdown();
    }
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
  statusLine.textContent = method === 'POST' ? 'Подключаемся…' : 'Отключаемся…';
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
    statusLine.textContent = 'Не удалось изменить подключение.';
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
async function respond(decision) {
  if (pending || !currentState?.invitation || document.querySelector('#accept').disabled) return;
  const id = currentState.invitation.id;
  pending = true;
  connectButton.disabled = disconnectButton.disabled = true;
  version += 1;
  actionError = '';
  countdown();
  try {
    const response = await fetch(`/api/v1/invitations/${id}/response`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ decision }), signal: AbortSignal.timeout(9000),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error?.message || 'Не удалось ответить на приглашение.');
    render(result);
  } catch (error) {
    actionError = error.message || 'Не удалось ответить на приглашение.';
    errorLine.textContent = actionError;
  } finally {
    pending = false;
    connectButton.disabled = disconnectButton.disabled = false;
    countdown();
    refresh();
  }
}
document.querySelector('#accept').addEventListener('click', () => respond('accept'));
document.querySelector('#decline').addEventListener('click', () => respond('decline'));
refresh();
setInterval(refresh, 1000);
setInterval(countdown, 200);
