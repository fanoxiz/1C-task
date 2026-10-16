export class GamePanel {
  constructor(root, role, send, refresh) {
    this.root = root;
    this.role = role;
    this.send = send;
    this.refresh = refresh;
    this.game = null;
    this.available = false;
    this.pendingGames = new Set();
    this.error = '';
    this.storageKey = `onec.${role}.moves`;
    try {
      const stored = JSON.parse(sessionStorage.getItem(this.storageKey) || '{}');
      this.drafts = stored && typeof stored === 'object' && !Array.isArray(stored) ? stored : {};
    } catch { this.drafts = {}; }
    this.cells = Array.from({ length: 9 }, (_, index) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.dataset.row = String(Math.floor(index / 3));
      button.dataset.column = String(index % 3);
      button.addEventListener('click', () => this.play(Math.floor(index / 3), index % 3));
      return button;
    });
    root.querySelector('[data-board]').replaceChildren(...this.cells);
    root.querySelector('[data-retry]').addEventListener('click', () => {
      const draft = this.drafts[this.game?.id];
      if (draft && !this.pending && this.available) this.submit(draft);
    });
  }

  get pending() {
    return this.pendingGames.has(this.game?.id);
  }

  save() {
    try { sessionStorage.setItem(this.storageKey, JSON.stringify(this.drafts)); } catch { /* In-memory retries still retain their key. */ }
  }

  applied(draft) {
    return this.game?.id === draft.gameId && this.game.moves.some(move => move.request_id === draft.request_id && move.actor === this.role);
  }

  render(game, available = this.available) {
    if (this.game?.id !== game?.id) this.error = '';
    this.game = game;
    this.available = available;
    this.root.hidden = !game;
    if (!game) return;
    const draft = this.drafts[game.id];
    if (draft && this.applied(draft)) {
      delete this.drafts[game.id];
      this.save();
      this.error = '';
    }
    this.root.querySelector('[data-game-id]').textContent = game.id;
    this.root.querySelector('[data-sides]').textContent = `Мобиус: ${game.mobius_side}. Локи: ${game.loki_side}.`;
    this.root.querySelector('[data-turn]').textContent = game.status === 'active'
      ? `Ходит ${game.next_actor === 'mobius' ? 'Мобиус' : 'Локи'}.` : 'Партия завершена.';
    const outcomes = { mobius_win: 'Победил Мобиус.', loki_win: 'Победил Локи.', draw: 'Ничья.' };
    this.root.querySelector('[data-result]').textContent = outcomes[game.result] || '';
    const blocked = !available || this.pending || !!this.drafts[game.id] || game.status !== 'active' || game.next_actor !== this.role;
    this.cells.forEach((cell, index) => {
      const value = game.board[Math.floor(index / 3)][index % 3];
      cell.textContent = value || ' ';
      cell.setAttribute('aria-label', `Строка ${Math.floor(index / 3) + 1}, столбец ${index % 3 + 1}: ${value || 'пусто'}`);
      cell.disabled = blocked || !!value;
    });
    const history = game.moves.map(move => {
      const item = document.createElement('li');
      let delivery = '';
      if (this.role === 'mobius' && move.actor === 'mobius') delivery = move.delivered_at ? ' — доставлено' : ' — ожидает подтверждения';
      item.textContent = `${move.number}. ${move.actor === 'mobius' ? 'Мобиус' : 'Локи'} (${move.side}): ${move.row + 1}, ${move.column + 1}${delivery}`;
      return item;
    });
    this.root.querySelector('[data-moves]').replaceChildren(...history);
    const retry = this.root.querySelector('[data-retry]');
    retry.hidden = !this.drafts[game.id];
    retry.disabled = !available || this.pending;
    this.root.querySelector('[data-error]').textContent = this.error;
  }

  play(row, column) {
    if (!this.game || !this.available || this.pending || this.drafts[this.game.id] || this.game.status !== 'active' || this.game.next_actor !== this.role || this.game.board[row][column]) return;
    const draft = { gameId: this.game.id, request_id: crypto.randomUUID(), expected_move_number: this.game.moves.length + 1, row, column };
    this.drafts[draft.gameId] = draft;
    this.save();
    this.submit(draft);
  }

  async submit(draft) {
    this.pendingGames.add(draft.gameId);
    this.error = '';
    this.render(this.game);
    try {
      const result = await this.send(draft);
      delete this.drafts[draft.gameId];
      this.save();
      if (this.game?.id === draft.gameId) this.render(result);
    } catch (error) {
      const definite = error.status >= 400 && error.status < 500;
      if (definite || this.applied(draft)) delete this.drafts[draft.gameId];
      if (this.game?.id === draft.gameId && !this.applied(draft)) this.error = error.message || 'Не удалось отправить ход. Повторите отправку с тем же ключом.';
      this.save();
      try { await this.refresh(); } catch { /* The regular polling loop reports connection status. */ }
    } finally {
      this.pendingGames.delete(draft.gameId);
      this.render(this.game);
    }
  }
}

export function moveBody(draft) {
  return { request_id: draft.request_id, expected_move_number: draft.expected_move_number, row: draft.row, column: draft.column };
}

export async function moveResponse(response) {
  const data = await response.json();
  if (!response.ok) {
    const messages = { wrong_turn: 'Сейчас ход соперника.', cell_occupied: 'Клетка уже занята.', game_finished: 'Партия завершена.', stale_move: 'Состояние партии изменилось.', conflict: 'Ключ запроса уже использован для другого хода.', not_found: 'Партия удалена или недоступна.', client_offline: 'Локи отключён.' };
    const error = new Error(messages[data.error?.code] || data.error?.message || 'Не удалось отправить ход.');
    error.status = response.status;
    throw error;
  }
  return data;
}
