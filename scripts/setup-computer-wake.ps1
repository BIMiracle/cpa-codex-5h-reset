param(
    [string]$TaskName = 'CPA Window Keeper - Wake Computer'
)

$ErrorActionPreference = 'Stop'
$times = @('05:00', '10:01', '15:02', '20:03')
$triggers = @(
    $times | ForEach-Object {
        New-ScheduledTaskTrigger -Daily -At ([datetime]::ParseExact(
            $_, 'HH:mm', [Globalization.CultureInfo]::InvariantCulture))
    }
)
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument (
    '-NoProfile -NonInteractive -WindowStyle Hidden -Command "exit 0"'
)
$settings = New-ScheduledTaskSettingsSet -WakeToRun -StartWhenAvailable
$principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $triggers `
    -Settings $settings -Principal $principal -Force | Out-Null
Write-Host "Registered $TaskName with wake-only triggers. It sends no CPA or Codex requests."
