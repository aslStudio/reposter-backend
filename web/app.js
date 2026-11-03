const login = document.querySelector('#login');
const signedIn = document.querySelector('#signed-in');
const message = document.querySelector('#message');
const automation = document.querySelector('#automation');
const sources = document.querySelector('#sources');

function showAuthenticated(authenticated) {
  login.hidden = authenticated;
  signedIn.hidden = !authenticated;
  message.textContent = '';
}

async function api(path, options) {
  const response = await fetch(path, { credentials: 'same-origin', cache: 'no-store', ...options });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json();
}

async function loadControls() {
  try {
    const [settings, items] = await Promise.all([api('/v1/settings'), api('/v1/sources')]);
    automation.checked = settings.automation_enabled;
    sources.replaceChildren();
    if (items.length === 0) {
      const empty = document.createElement('li');
      empty.textContent = 'Источников пока нет. Добавь их через API.';
      sources.append(empty);
    }
    for (const source of items) {
      const row = document.createElement('li');
      const label = document.createElement('label');
      label.className = 'switch';
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.checked = source.enabled;
      checkbox.addEventListener('change', async () => {
        checkbox.disabled = true;
        try {
          await api(`/v1/sources/${source.id}`, {
            method: 'PATCH', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ enabled: checkbox.checked }),
          });
          message.textContent = '';
        } catch {
          checkbox.checked = !checkbox.checked;
          message.textContent = 'Не удалось изменить источник.';
        } finally {
          checkbox.disabled = false;
        }
      });
      label.append(checkbox, document.createTextNode(`${source.platform}: ${source.handle}`));
      row.append(label);
      sources.append(row);
    }
  } catch {
    message.textContent = 'Не удалось загрузить настройки.';
  }
}

automation.addEventListener('change', async () => {
  automation.disabled = true;
  try {
    await api('/v1/settings', {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ automation_enabled: automation.checked }),
    });
    message.textContent = '';
  } catch {
    automation.checked = !automation.checked;
    message.textContent = 'Не удалось изменить обработку.';
  } finally {
    automation.disabled = false;
  }
});

async function refresh() {
  try {
    const response = await fetch('/v1/auth/me', { credentials: 'same-origin', cache: 'no-store' });
    showAuthenticated(response.ok);
    if (response.ok) await loadControls();
  } catch {
    login.hidden = false;
    message.textContent = 'Сервер недоступен. Попробуйте позже.';
  }
}

login.addEventListener('submit', async (event) => {
  event.preventDefault();
  message.textContent = '';
  const password = document.querySelector('#password');
  try {
    const response = await fetch('/v1/auth/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: password.value }),
    });
    password.value = '';
    if (response.ok) {
      showAuthenticated(true);
      await loadControls();
    }
    else message.textContent = 'Неверный пароль.';
  } catch {
    message.textContent = 'Сервер недоступен. Попробуйте позже.';
  }
});

document.querySelector('#logout').addEventListener('click', async () => {
  let failed = false;
  try {
    await fetch('/v1/auth/logout', { method: 'POST', credentials: 'same-origin' });
  } catch {
    failed = true;
  }
  showAuthenticated(false);
  if (failed) message.textContent = 'Не удалось связаться с сервером.';
});

refresh();
