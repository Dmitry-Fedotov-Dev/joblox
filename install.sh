#!/bin/sh
# Joblox: установка и запуск одной командой на телефоне с Android (Termux), в Linux и на Mac.
#
#   curl -fsSL https://raw.githubusercontent.com/Dmitry-Fedotov-Dev/joblox/main/install.sh | sh
#
# Скачивает программу последней версии в ~/joblox, сверяет контрольную сумму и запускает `joblox up`:
# сайт получает публичную ссылку через Cloudflare, а в браузере открывается кабинет владельца.
# Запустить снова: команда joblox up.
set -eu

REPO=Dmitry-Fedotov-Dev/joblox
DIR=${JOBLOX_DIR:-$HOME/joblox}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "Процессор $(uname -m) не поддерживается." >&2; exit 1 ;;
esac
case $os in
  linux | darwin) ;;
  *) echo "Для Windows есть install.ps1, см. https://github.com/$REPO" >&2; exit 1 ;;
esac

termux=0
if [ -n "${TERMUX_VERSION:-}" ] || [ -d /data/data/com.termux/files ]; then termux=1; fi
if [ $termux = 1 ]; then
  echo "Termux: ставим cloudflared…"
  pkg install -y cloudflared termux-tools >/dev/null
  termux-wake-lock 2>/dev/null || true # телефон не усыпит программу
fi

mkdir -p "$DIR"
cd "$DIR"
asset=joblox-$os-$arch
base=https://github.com/$REPO/releases/latest/download
echo "Скачиваем $asset…"
curl -fsSL "$base/$asset" -o joblox.new
curl -fsSL "$base/SHA256SUMS" -o SHA256SUMS
want=$(grep " $asset\$" SHA256SUMS | cut -d ' ' -f 1)
if command -v sha256sum >/dev/null; then got=$(sha256sum joblox.new); else got=$(shasum -a 256 joblox.new); fi
got=${got%% *}
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  rm -f joblox.new
  echo "Контрольная сумма не совпала, файл не используется. Попробуйте ещё раз позже." >&2
  exit 1
fi
chmod +x joblox.new
mv joblox.new joblox

# команда joblox из любой папки
if [ $termux = 1 ]; then
  ln -sf "$DIR/joblox" "$PREFIX/bin/joblox"
else
  mkdir -p "$HOME/.local/bin"
  ln -sf "$DIR/joblox" "$HOME/.local/bin/joblox"
fi

echo "Готово: $DIR. Запускаем…"
exec ./joblox up
