param(
    [Parameter(Mandatory = $true)][string]$Auth,
    [Parameter(Mandatory = $true)][string]$Reason
)

$message = "Codex 额度唤醒最终失败。`n凭据 ID：$Auth`n原因：$Reason`n请在 CPA 插件状态中检查并手动重试。"
Add-Type -AssemblyName PresentationFramework
[System.Windows.MessageBox]::Show(
    $message,
    'CPA Codex 额度唤醒失败',
    [System.Windows.MessageBoxButton]::OK,
    [System.Windows.MessageBoxImage]::Error
) | Out-Null
