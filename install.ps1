# Joblox: установка и запуск одной командой на Windows. В PowerShell:
#
#   irm https://raw.githubusercontent.com/Dmitry-Fedotov-Dev/joblox/main/install.ps1 | iex
#
# Скачивает программу последней версии в %USERPROFILE%\joblox, сверяет контрольную сумму, кладёт ярлык
# Joblox на рабочий стол и запускает её: сайт получает публичную ссылку через Cloudflare, а в браузере
# открывается кабинет владельца. Запустить снова: ярлык Joblox.
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'Dmitry-Fedotov-Dev/joblox'
$dir = Join-Path $env:USERPROFILE 'joblox'
$asset = 'joblox-windows-amd64.exe' # на Windows для ARM тоже: она запускает программы x64
$base = "https://github.com/$repo/releases/latest/download"
New-Item -ItemType Directory -Force $dir | Out-Null

Write-Host "Скачиваем $asset..."
$new = Join-Path $dir 'joblox.new'
Invoke-WebRequest "$base/$asset" -OutFile $new -UseBasicParsing
$sums = (Invoke-WebRequest "$base/SHA256SUMS" -UseBasicParsing).Content
if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
$line = ($sums -split "`n") | Where-Object { $_.TrimEnd() -like "* $asset" } | Select-Object -First 1
$want = if ($line) { ($line -split ' ')[0] } else { '' }
$got = (Get-FileHash $new -Algorithm SHA256).Hash.ToLower()
if ($want -ne $got) {
    Remove-Item $new
    throw 'Контрольная сумма не совпала, файл не используется. Попробуйте ещё раз позже.'
}
$exe = Join-Path $dir 'joblox.exe'
Move-Item -Force $new $exe

$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut((Join-Path ([Environment]::GetFolderPath('Desktop')) 'Joblox.lnk'))
$link.TargetPath = $exe
$link.WorkingDirectory = $dir
$link.Save()

Write-Host "Готово: $dir, ярлык Joblox на рабочем столе. Запускаем..."
Start-Process $exe -WorkingDirectory $dir
