<#
While there is a go module for excel output, this is a great usage for PowerShell and not reinventing the wheelGet-SSMParametersByPath -Path "/aws/service/ami-windows-latest/" -ProfileName '{0}' | Where-Object Name -match 'Windows_Server-2016' | Format-Table
#>

if (-not (Get-InstalledModule ImportExcel -ErrorAction SilentlyContinue))
{
    Install-Module ImportExcel -Force -Confirm:$false -Scope CurrentUser
}

$files = Get-ChildItem -Path ../ -Filter *.json | Where-Object Name -Match 'report-*.json'
$results = $Files | ForEach-Object {
    Get-Content $_ -Raw | ConvertFrom-Json -Depth 10
}

$results | Export-Excel ami-report.xlsx -TableName report -TableStyle Light8 -WorksheetName 'results'
Invoke-Item ami-report.xlsx
