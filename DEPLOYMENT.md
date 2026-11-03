# Развёртывание Reposter на VPS

Инструкция рассчитана на Ubuntu 24.04 LTS и одного владельца сервиса. Команды с пометкой «локально» выполняются на твоём компьютере; остальные — на сервере по SSH. Замени `<USER>`, `<IP>`, `reposter.example.com` и `<REPOSITORY_URL>` своими значениями. Подписки и многопользовательский вход пока не реализованы. Telegram и X включены, Threads в конфигурации запуска отключён.

Для HTTPS нужен домен или поддомен с DNS-записью `A`, указывающей на IPv4 сервера. Если настроена запись `AAAA`, она тоже должна указывать на этот сервер. **Покупать домен необязательно:** укажи в `APP_DOMAIN` имя вида `<IP>.sslip.io`, например `203.0.113.10.sslip.io`. [sslip.io](https://sslip.io/) возвращает IP, встроенный в имя, поэтому сайт будет доступен по адресу, привязанному к твоему IP. Это сторонний DNS-сервис; его можно использовать и постоянно, пока IP сервера не меняется. При прямом открытии `https://<IP>` текущая конфигурация Caddy может выдать сертификат, которому браузер не доверяет, поэтому для обычного входа используй имя `.sslip.io`. Открой входящие TCP-порты 80 и 443 и SSH-порт. PostgreSQL и приложение наружу не публикуются: доступ к ним есть только внутри сети Docker Compose. Caddy получает и продлевает сертификат автоматически, если DNS и порты настроены правильно ([документация Caddy](https://caddyserver.com/docs/automatic-https)).

## 1. Установить Docker

Подключись к серверу:

```sh
ssh <USER>@<IP>
```

Установи Docker Engine и Compose из [официального репозитория Docker](https://docs.docker.com/engine/install/ubuntu/):

```sh
sudo apt update
sudo apt install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo docker compose version
```

Если на VPS только 2 ГБ RAM и сборка образа завершается из-за нехватки памяти, добавь swap. Этот блок выполняется один раз:

```sh
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

## 2. Передать код

Если репозиторий доступен по Git, на сервере выполни:

```sh
git clone <REPOSITORY_URL> ~/reposter-backend
cd ~/reposter-backend
```

Для ещё не опубликованного репозитория передай файлы с локального компьютера. Сначала создай каталог на сервере: `mkdir -p ~/reposter-backend`. Затем **локально**, из корня репозитория:

```sh
rsync -az --exclude '.git/' --exclude '.env' --exclude '.env.*' --exclude 'data/' \
  ./ <USER>@<IP>:~/reposter-backend/
```

Файл `.env` создаётся на сервере отдельно. Он и другие файлы `.env.*` игнорируются Git; публичный шаблон `.env.example` можно коммитить. При использовании `rsync` скопируй шаблон отдельно или убери из команды исключение `.env.*` и убедись, что личные файлы `.env.*` не попали на сервер:

```sh
scp .env.example <USER>@<IP>:~/reposter-backend/.env.example
```

## 3. Настроить `.env`

На сервере:

```sh
cd ~/reposter-backend
umask 077
cp .env.example .env
chmod 600 .env
```

Для новых секретов можно использовать `openssl rand -hex 32` (пароль базы, админ-токен, ключ сессии) и `openssl rand -base64 32` (ключ шифрования X). Выполни команды и запиши результаты в `.env` через `nano .env` или другой редактор. Заполни как минимум:

```dotenv
APP_DOMAIN=reposter.example.com
POSTGRES_PASSWORD=<случайные-hex-символы>
ADMIN_TOKEN=<другие-случайные-hex-символы>
PANEL_PASSWORD=<твой-уникальный-пароль-от-16-символов>
PANEL_SESSION_KEY=<третьи-случайные-hex-символы>
CODEX_API_KEY=<ключ-OpenAI-Platform>
TELEGRAM_BOT_TOKEN=<токен-бота>
```

`PANEL_PASSWORD` — обычный пароль входа, который ты задаёшь прямо в `.env`. Он проверяется сервером; в публичной `.env.example` значения нет. Используй длинный пароль без пробелов, `#` и `$`, чтобы его однозначно прочитал Docker Compose. `ADMIN_TOKEN` нужен для Postman и других административных запросов, но не для кода будущей веб-панели. Не публикуй `.env` и не отправляй эти значения в чат.

Если нужен X, дополнительно укажи `X_BEARER_TOKEN`, `X_CONSUMER_KEY`, `X_SECRET_KEY` и `X_TOKEN_ENCRYPTION_KEY`. Последний генерируется через `openssl rand -base64 32` и должен сохраняться при всех обновлениях: им зашифрованы пользовательские токены X в базе. Для Telegram бот должен быть администратором канала назначения с правом публикации. Продакшн Compose игнорирует `BOOTSTRAP_TG_*`, `BOOTSTRAP_X_*` и `BOOTSTRAP_THREADS_*` из `.env`: тестовые маршруты на старте не создаются. `ACTIVE_PLATFORMS` принудительно установлен в `telegram,x`.

## 4. Запустить сервис

Собери React-панель в соседнем каталоге `reposter-frontend` (рядом с `reposter-backend`). Локально:

```sh
cd ../reposter-frontend
npm install
npm run build
```

При передаче файлов на VPS скопируй `reposter-frontend/dist` в `~/reposter-frontend/dist`:

```sh
rsync -az dist/ <USER>@<IP>:~/reposter-frontend/dist/
```

Продакшн Compose монтирует этот каталог в Caddy как веб-панель. API доступен на том же домене, поэтому токен администратора в браузере не нужен.

Используй **только** `compose.prod.yaml` и постоянное имя проекта `reposter`: от него зависят имена Docker-томов. Файл `compose.yaml` предназначен для локальной разработки.

```sh
cd ~/reposter-backend
sudo docker compose -p reposter -f compose.prod.yaml config --quiet
sudo docker compose -p reposter -f compose.prod.yaml up -d --build
sudo docker compose -p reposter -f compose.prod.yaml ps
```

Проверь HTTPS и защиту API, заменив домен:

```sh
curl -I https://reposter.example.com/
curl -i https://reposter.example.com/v1/auth/me
```

Первый запрос должен вернуть страницу, второй без входа — `401`. Открой `https://reposter.example.com/` в браузере и войди с `PANEL_PASSWORD`. Панель позволяет добавлять каналы и источники, связывать их, менять правила публикации, смотреть статистику и черновики, а также подключать X аккаунт через PIN. API доступен по `/v1` на том же домене через защищённую cookie-сессию; админ-токен в JavaScript не нужен. Для Postman укажи `https://reposter.example.com` как `base_url` и передавай `X-Admin-Token` в заголовке.

На новой базе автоматическая обработка выключена. Все новые источники тоже создаются выключенными. Добавь свои источники, назначения и маршруты через API, затем включи нужные источники и общий переключатель в панели. `PATCH /v1/sources/{id}` принимает `{"enabled":true}`, а `PATCH /v1/settings` принимает `{"automation_enabled":true}`. При выключенном общем переключателе сервис автоматически не читает источники и не публикует готовые черновики. Уже начатая операция может завершиться после выключения. Если переносишь старую базу, миграция также выключит все ранее созданные источники — они не начнут обрабатываться сами.

Если что-то не запустилось:

```sh
sudo docker compose -p reposter -f compose.prod.yaml logs --tail=100 app caddy postgres
```

Если HTTPS не поднялся, проверь DNS, доступность портов 80/443 и отсутствие другого сервиса на этих портах. Caddy уже входит в Compose; устанавливать его на хост отдельно не нужно.

## 5. Перенести текущие данные с компьютера

Новый сервер без переноса томов создаст пустую базу. Если нужно сохранить маршруты, черновики, медиа и подключённый X-аккаунт, перенеси базу и папку медиа. **Локально**, из корня текущего проекта:

```sh
docker compose exec -T postgres pg_dump -Fc -U reposter reposter > reposter.dump
docker run --rm -v reposter-backend_media_data:/data:ro -v "$PWD:/backup" \
  alpine tar -czf /backup/reposter-media.tar.gz -C /data media
scp reposter.dump reposter-media.tar.gz <USER>@<IP>:~/
```

На сервере останови приложение, если оно уже запущено после раздела 4. Затем запусти только PostgreSQL, восстанови базу и медиа, после чего подними весь проект:

```sh
cd ~/reposter-backend
sudo docker compose -p reposter -f compose.prod.yaml stop app caddy
sudo docker compose -p reposter -f compose.prod.yaml up -d postgres
sudo docker compose -p reposter -f compose.prod.yaml exec -T postgres \
  pg_restore -U reposter -d reposter --clean --if-exists < ~/reposter.dump
sudo docker run --rm -v reposter_media_data:/data -v "$HOME:/backup:ro" \
  alpine tar -xzf /backup/reposter-media.tar.gz -C /data
sudo docker compose -p reposter -f compose.prod.yaml up -d --build
```

Перед восстановлением убедись, что в серверной `.env` указан **тот же** `X_TOKEN_ENCRYPTION_KEY`, что был локально. Иначе сохранённое подключение X нельзя будет расшифровать. Если локальное имя Docker-проекта отличается от `reposter-backend`, узнай точное имя тома через `docker volume ls` и подставь его в локальную команду. После запуска миграция выключит перенесённые источники и общую автоматическую обработку. Проверь их список в панели перед включением. После проверки переноса удали временные файлы `.dump` и `.tar.gz` с обеих машин или перемести их в защищённое хранилище.

## 6. Резервные копии и обновления

На сервере перед обновлением сохрани базу и медиа:

```sh
cd ~/reposter-backend
umask 077
mkdir -p ~/reposter-backups
chmod 700 ~/reposter-backups
sudo docker compose -p reposter -f compose.prod.yaml exec -T postgres \
  pg_dump -Fc -U reposter reposter > ~/reposter-backups/reposter-$(date +%F).dump
sudo docker run --rm -v reposter_media_data:/data:ro -v "$HOME/reposter-backups:/backup" \
  alpine tar -czf /backup/media-$(date +%F).tar.gz -C /data media
```

Храни копии базы, медиа и `.env` вне сервера. При обычном обновлении не меняй `POSTGRES_PASSWORD`, `X_TOKEN_ENCRYPTION_KEY` и `PANEL_SESSION_KEY`. Изменение `PANEL_PASSWORD` вступает в силу после пересоздания контейнера приложения и завершает все старые браузерные сессии. Изменение `PANEL_SESSION_KEY` тоже завершит их.

Для обновления кода выполни `git pull` либо повтори `rsync` из раздела 2. Затем на сервере:

```sh
cd ~/reposter-backend
sudo docker compose -p reposter -f compose.prod.yaml up -d --build
sudo docker compose -p reposter -f compose.prod.yaml ps
sudo docker compose -p reposter -f compose.prod.yaml logs --tail=100 app
```

Не запускай `docker compose down -v`: это удалит тома с базой и медиа.
