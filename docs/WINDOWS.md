# نصب آماده روی ویندوز

برای نصب، Go، Git، Visual Studio یا منابع کامپایل لازم نیست. بسته‌های Windows x64 و ARM64 در [انتشار آمادهٔ ۰٫۱٫۲](https://github.com/AlirezaDadrass/codex-token-monitor/releases/tag/v0.1.2) هستند. اسکریپت معماری را تشخیص می‌دهد، ZIP همان نسخه را می‌گیرد و SHA-256 را پیش از استخراج بررسی می‌کند. کلید ورود OpenAI لازم نیست؛ فقط توکن مرکز خودتان را وارد کنید.

## کامپیوتر شخصی / نصب MCP

فایل مستقل `install-windows.ps1` را از همان انتشار دریافت کنید. در PowerShell:

```powershell
Invoke-WebRequest 'https://github.com/AlirezaDadrass/codex-token-monitor/releases/download/v0.1.2/install-windows.ps1' -OutFile '.\install-windows.ps1'
Unblock-File '.\install-windows.ps1'
powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File '.\install-windows.ps1'
```

این اجرای RemoteSigned فقط به فرایند فوق مربوط است؛ تنظیمات دائمی ExecutionPolicy تغییر نمی‌کند و Group Policy سازمان همچنان اعمال می‌شود. پس از بررسی فایل، آن را اجرا کنید.

نصب‌کننده نام کامپیوتر، آدرس سرور و توکن را می‌پرسد. ورود توکن مخفی است. قبل از ثبت MCP در Codex، اتصال بررسی می‌شود. Codex CLI باید از قبل در PATH باشد؛ خود مانیتور به Go یا Node برای اجرا نیاز ندارد. نام دستگاه باید یکتا باشد؛ تعداد دستگاه‌ها محدود نیست.

اجرای غیرتعاملی برای ایجنت، با فایل خصوصی توکن:

```powershell
.\install-windows.ps1 -Mode Client -NonInteractive `
  -Name 'کامپیوتر دفتر' -HubUrl 'https://monitor.example.com' `
  -TokenFile 'C:\private\hub-token'
```

یا `-Token (Read-Host 'Hub token' -AsSecureString)` بدهید؛ مقدار واقعی توکن را در متن دستور ننویسید. فایل ورودی توکن داخل پوشهٔ خصوصی نصب کپی می‌شود. نصب مجدد بدون ورودیِ کلید، نام و شناسه و اتصال قبلی را حفظ می‌کند. باینری در پوشهٔ نسخه‌دار قرار می‌گیرد تا نسخهٔ در حال اجرای Codex بازنویسی نشود؛ اتصال MCP را برای استفاده از نسخهٔ جدید تازه‌سازی کنید.

مسیر پیش‌فرض کلاینت: `%LOCALAPPDATA%\CodexTokenMonitor`. باینری زیر `bin\v0.1.2` و تنظیمات زیر `config\config.json` است. اسکریپت فقط همین پوشه را برای کاربر، SYSTEM و Administrators خصوصی می‌کند؛ PATH سیستم و فایروال را تغییر نمی‌دهد.

## سرور ویندوز، بدون کامپایل

PowerShell را **با دسترسی Administrator روی سرور انتخاب‌شده** باز و همان اسکریپت را اجرا کنید:

```powershell
.\install-windows.ps1 -Mode Server
```

نام مرکز، آدرس گوش‌دادن، پورت و آدرس بیرونی را وارد کنید. توکن خالی یعنی ساخت کلید تصادفی جدید. TLS مستقیم اختیاری است؛ برای reverse proxy یا تونل SSH لازم نیست گواهی را به خود باینری بدهید.

فرمان غیرتعاملی با ساخت توکن جدید:

```powershell
.\install-windows.ps1 -Mode Server -NonInteractive `
  -Name 'مرکز مصرف' -ListenAddress '127.0.0.1' -Port 8787 `
  -PublicUrl 'https://monitor.example.com'
```

سرور با یک Scheduled Task بومی ویندوز به نام `CodexTokenMonitorHub`، هنگام روشن‌شدن سیستم و بدون نیاز به ورود کاربر، تحت حساب محدود **LOCAL SERVICE** اجرا می‌شود. اجرای Go به‌صورت Windows Service ثبت نمی‌شود، چون این باینری پروتکل Service Control Manager را پیاده نمی‌کند. [حساب سرویس در Task Scheduler](https://learn.microsoft.com/en-us/powershell/module/scheduledtasks/new-scheduledtaskprincipal)

مسیر پیش‌فرض سرور `%ProgramData%\CodexTokenMonitor` است. LOCAL SERVICE فقط به باینری/تنظیمات دسترسی خواندن و به پوشهٔ `data` دسترسی نوشتن دارد. توکن در آرگومان task یا خروجی نمایش داده نمی‌شود؛ فایل کلید در پوشهٔ خصوصی `config` می‌ماند. فایل‌های گواهی انتخاب‌شده نیز به همین پوشه کپی می‌شوند. برای TLS مستقیم، `-TlsCertificate C:\certs\server.crt -TlsKey C:\certs\server.key` بدهید.

```powershell
Get-ScheduledTask -TaskName CodexTokenMonitorHub
Get-ScheduledTaskInfo -TaskName CodexTokenMonitorHub
```

نصب‌کننده پس از شروع task، در حال اجرا ماندن آن را بررسی می‌کند. دسترسی بیرونی/DNS/TLS را از یک کلاینت واقعی هم بررسی کنید. فایروال و reverse proxy خودکار تغییر نمی‌کنند. برای اینترنت از HTTPS استفاده کنید. تنظیمات یا task موجود بازنویسی نمی‌شوند؛ برای ارتقای سرور، task را متوقف، باینری نسخهٔ جدید را آماده و action همان task را با حفظ نام، توکن و تنظیمات به مسیر جدید وصل کنید.

## نصب آفلاین و پیش‌نمایش

ZIP و `checksums.txt` همان انتشار را روی کامپیوتر دیگری دانلود و منتقل کنید:

```powershell
.\install-windows.ps1 -Mode Client `
  -ArchivePath '.\token-monitor_0.1.2_windows_amd64.zip' `
  -ChecksumsPath '.\checksums.txt'
```

بررسی checksum در حالت آفلاین هم انجام می‌شود؛ ساخت از سورس یا دانلود Go هیچ‌وقت fallback این اسکریپت نیست. انتخاب `-Architecture amd64` یا `arm64` برای پیش‌نمایش ممکن است؛ برای نصب واقعی باید با معماری سیستم یکسان باشد.

```powershell
.\install-windows.ps1 -Mode Client -Name 'تست' `
  -HubUrl 'https://monitor.example.com' -DryRun
```

`-DryRun` دانلود، فایل، توکن، task یا تنظیمات سیستم را تغییر نمی‌دهد. پارامتر `-InstallRoot` مسیر نصب مستقل را مشخص می‌کند. برای کلاینت و سرور پوشهٔ جدا بگذارید.

## بیلدهای GitHub

با ثبت تگ نسخه، workflow انتشار تست‌ها را اجرا می‌کند، هر شش باینری Linux/macOS/Windows را روی منابع GitHub می‌سازد و ZIP/TAR، اسکریپت مستقل و `checksums.txt` را در Release می‌گذارد. CI ویندوز نیز `.exe` را بومی می‌سازد و PowerShell 5.1 و 7، نصب کلاینت، ثبت با `codex.cmd`، ACL توکن، اجرای سرور با LOCAL SERVICE، احراز هویت و گزارش HTML را در محیط جدا آزمایش می‌کند.
