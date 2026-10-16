const status = document.querySelector('#status');
const button = document.querySelector('#refresh');

async function refresh() {
  button.disabled = true;
  try {
    const response = await fetch('/readyz', { cache: 'no-store', signal: AbortSignal.timeout(4000) });
    status.textContent = response.ok ? 'Сервер готов к работе.' : 'Сервер временно не готов. Повторите проверку позже.';
  } catch {
    status.textContent = 'Нет связи с сервером.';
  } finally {
    button.disabled = false;
  }
}

button.addEventListener('click', refresh);
refresh();
