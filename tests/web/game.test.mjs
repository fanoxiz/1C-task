import assert from 'node:assert/strict';
import { test } from 'node:test';
import { GamePanel } from '../../web/common/game.js';

class Element {
  children = [];
  textContent = '';
  hidden = false;
  disabled = false;
  dataset = {};
  listeners = {};
  addEventListener(name, listener) { this.listeners[name] = listener; }
  replaceChildren(...children) { this.children = children; }
  setAttribute() {}
  click() { if (!this.disabled) this.listeners.click?.(); }
}

function setup(storage = new Map()) {
  const elements = new Map();
  const root = new Element();
  root.querySelector = selector => {
    if (!elements.has(selector)) elements.set(selector, new Element());
    return elements.get(selector);
  };
  globalThis.document = { createElement: () => new Element() };
  globalThis.sessionStorage = {
    getItem: key => storage.get(key),
    setItem: (key, value) => storage.set(key, value),
  };
  const requests = [];
  const panel = new GamePanel(root, 'mobius', draft => new Promise((resolve, reject) => {
    requests.push({ draft, resolve, reject });
  }), async () => {});
  return { panel, root, requests, storage };
}

function board(id, moves = []) {
  const cells = Array.from({ length: 3 }, () => ['', '', '']);
  for (const move of moves) cells[move.row][move.column] = move.side;
  return { id, mobius_side: 'X', loki_side: 'O', status: 'active', next_actor: moves.length % 2 ? 'loki' : 'mobius', board: cells, moves };
}

function applied(request) {
  return board(request.draft.gameId, [{ ...request.draft, number: 1, actor: 'mobius', side: 'X', delivered_at: null }]);
}

const settled = () => new Promise(resolve => setImmediate(resolve));

for (const outcome of ['success', 'uncertain_failure', 'rejection']) {
  test(`switching games while the first move waits: ${outcome}`, async () => {
    const { panel, root, requests, storage } = setup();
    panel.render(board('A'), true);
    panel.cells[0].click();
    assert.equal(requests.length, 1);
    const first = requests[0];
    assert.ok(panel.cells.every(cell => cell.disabled));
    // A's pending request must still block another move when returning to A.
    panel.render(board('B'), true);
    panel.render(board('A'), true);
    panel.cells[1].click();
    assert.equal(requests.length, 1);
    panel.render(board('B'), true);
    assert.equal(panel.cells[4].disabled, false, 'A must not block B');
    panel.cells[4].click();
    assert.equal(requests.length, 2);
    const second = requests[1];
    assert.equal(second.draft.gameId, 'B');
    second.resolve(applied(second));
    await settled();
    if (outcome === 'success') first.resolve(applied(first));
    else first.reject(Object.assign(new Error('A failed'), { status: outcome === 'rejection' ? 409 : 503 }));
    await settled();
    assert.equal(panel.game.id, 'B');
    assert.equal(panel.cells[4].textContent, 'X');
    assert.equal(panel.cells[0].textContent.trim(), '');
    assert.equal(root.querySelector('[data-moves]').children.length, 1);
    assert.equal(root.querySelector('[data-error]').textContent, '');
    assert.equal(root.querySelector('[data-retry]').hidden, true);
    const drafts = JSON.parse(storage.get('onec.mobius.moves'));
    assert.equal(drafts.B, undefined);
    if (outcome === 'uncertain_failure') {
      assert.equal(drafts.A.request_id, first.draft.request_id);
      panel.render(board('A'), true);
      assert.equal(root.querySelector('[data-retry]').hidden, false);
      root.querySelector('[data-retry]').click();
      assert.equal(requests.length, 3);
      assert.equal(requests[2].draft.request_id, first.draft.request_id);
      requests[2].resolve(applied(requests[2]));
      await settled();
    } else assert.equal(drafts.A, undefined);
  });
}

test('completion of A does not unlock the still pending B', async () => {
  const { panel, requests } = setup();
  panel.render(board('A'), true);
  panel.cells[0].click();
  panel.render(board('B'), true);
  panel.cells[4].click();
  assert.equal(requests.length, 2);
  requests[0].resolve(applied(requests[0]));
  await settled();
  assert.equal(panel.game.id, 'B');
  assert.ok(panel.cells.every(cell => cell.disabled));
  panel.cells[5].click();
  assert.equal(requests.length, 2);
  requests[1].resolve(applied(requests[1]));
  await settled();
  assert.equal(panel.cells[4].textContent, 'X');
});

test('reload after a lost move response retries the persisted request once', async () => {
  const first = setup();
  first.panel.render(board('A'), true);
  first.panel.cells[0].click();
  const original = first.requests[0];
  original.reject(new Error('connection closed after commit'));
  await settled();
  const reloaded = setup(first.storage);
  reloaded.panel.render(board('A'), true);
  reloaded.root.querySelector('[data-retry]').click();
  assert.equal(reloaded.requests.length, 1);
  assert.deepEqual(reloaded.requests[0].draft, original.draft);
  reloaded.requests[0].resolve(applied(original));
  await settled();
  assert.equal(reloaded.panel.game.moves.length, 1);
  assert.deepEqual(JSON.parse(first.storage.get('onec.mobius.moves')), {});
});

test('fresh snapshot after a lost response clears the draft without resending', async () => {
  const first = setup();
  first.panel.render(board('A'), true);
  first.panel.cells[0].click();
  const original = first.requests[0];
  first.panel.render(applied(original), true);
  original.reject(new Error('response lost'));
  await settled();
  const reloaded = setup(first.storage);
  reloaded.panel.render(applied(original), true);
  assert.equal(reloaded.root.querySelector('[data-retry]').hidden, true);
  assert.equal(reloaded.root.querySelector('[data-error]').textContent, '');
  assert.equal(reloaded.requests.length, 0);
});
