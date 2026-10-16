import assert from 'node:assert/strict';
import { test } from 'node:test';
import { renderStatistics } from '../../web/common/statistics.js';

class Element {
  children = [];
  attributes = {};
  set textContent(value) { this.text = String(value); }
  get textContent() { return this.text || ''; }
  setAttribute(key, value) { this.attributes[key] = value; }
  append(child) { this.children.push(child); }
  replaceChildren(...children) { this.children = children; }
}
globalThis.document = { createElement: () => new Element() };

test('one statistics rectangle contains wins, draws and losses with the message below', () => {
  const root = new Element();
  renderStatistics(root, { wins: 3, draws: 2, losses: 1, played: 6, better_percent: 50, message: 'Вы бейзлайн' });
  assert.equal(root.children.length, 2);
  const [counts, message] = root.children;
  assert.equal(counts.className, 'statistics-counts');
  assert.deepEqual(counts.children.map(child => child.textContent), ['3', '2', '1']);
  assert.deepEqual(counts.children.map(child => child.className), ['statistics-wins', 'statistics-draws', 'statistics-losses']);
  assert.deepEqual(counts.children.map(child => child.title), ['Победы', 'Ничьи', 'Поражения']);
  assert.equal(message.textContent, 'Вы бейзлайн');
});

test('old invitation snapshots keep unknown counts and hide the obsolete comparison message', () => {
  const root = new Element();
  renderStatistics(root, { wins: 1, played: 2, better_percent: null, message: 'Пока недостаточно данных для сравнения' });
  assert.deepEqual(root.children[0].children.map(child => child.textContent), ['1', '—', '—']);
  assert.equal(root.children[1].hidden, true);
});
