export function scoreText(score) {
  const rate = score.win_rate == null ? '—' : `${new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 2 }).format(score.win_rate * 100)} %`;
  return `Побед: ${score.wins}. Завершённых партий: ${score.played}. Доля побед: ${rate}.`;
}
