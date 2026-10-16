export function renderStatistics(root, score) {
  if (!score) { root.replaceChildren(); return; }
  const block = document.createElement('div');
  block.className = 'statistics-counts';
  block.setAttribute('role', 'group');
  block.setAttribute('aria-label', 'Победы, ничьи, поражения');
  for (const [field, title] of [['wins', 'Победы'], ['draws', 'Ничьи'], ['losses', 'Поражения']]) {
    const number = document.createElement('span');
    number.className = `statistics-${field}`;
    number.textContent = score[field] ?? '—';
    number.title = title;
    number.setAttribute('aria-label', `${title}: ${number.textContent}`);
    block.append(number);
  }
  const message = document.createElement('p');
  message.className = 'statistics-message';
  // Also hide the old no-comparison message in immutable invitation snapshots.
  message.textContent = score.played > 0 && score.better_percent == null ? '' : (score.message || '');
  message.hidden = !message.textContent;
  root.replaceChildren(block, message);
}
