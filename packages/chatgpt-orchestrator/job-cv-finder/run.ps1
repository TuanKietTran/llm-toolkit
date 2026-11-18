# Windows twin of run.sh. Same config: env vars or job-cv-finder\.env.local (KEY=value lines).
# For a local model via llama-swap (~/.pi/agent/models.json): -Provider llama-swap -Model gemma4-12b
# (the local-model-harness only applies prompt-cache/keep-warm to providers in PI_LOCAL_PROVIDERS).
param(
  [string]$LeadsPath = $(if ($env:LEADS_PATH) { $env:LEADS_PATH } else { "$PSScriptRoot\leads\leads.json" }),
  [string]$PromptPath = $(if ($env:PROMPT_PATH) { $env:PROMPT_PATH } else { "$PSScriptRoot\prompts\orchestrate.md" }),
  [string]$Provider = $(if ($env:PI_PROVIDER) { $env:PI_PROVIDER } else { "openai-codex" }),
  [string]$Model = $(if ($env:PI_MODEL) { $env:PI_MODEL } else { "gpt-5.6-sol" }),
  [string]$Thinking = $(if ($env:PI_THINKING) { $env:PI_THINKING } else { "medium" })
)
$ErrorActionPreference = "Stop"

$localEnv = Join-Path $PSScriptRoot ".env.local"
if (Test-Path $localEnv) {
  Get-Content $localEnv | Where-Object { $_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$' } | ForEach-Object {
    if (-not (Test-Path "env:$($Matches[1])")) { Set-Item "env:$($Matches[1])" $Matches[2].Trim('"') }
  }
}
if (-not $env:CANDIDATE_NAME) { throw "Set CANDIDATE_NAME (env or job-cv-finder\.env.local)" }
if (-not (Test-Path $LeadsPath)) { throw "No leads file at $LeadsPath -- follow INGEST_VIA_CHROME.md first." }

$prompt = (Get-Content -Raw $PromptPath) -replace '\{\{LEADS_PATH\}\}', $LeadsPath -replace '\{\{CANDIDATE_NAME\}\}', $env:CANDIDATE_NAME
if ($Provider -eq "llama-swap" -and -not $env:PI_LOCAL_PROVIDERS) { $env:PI_LOCAL_PROVIDERS = "llama-swap" }
if (-not $env:ORCH_BROWSER__HEADLESS) { $env:ORCH_BROWSER__HEADLESS = "true" }

Push-Location (Split-Path $PSScriptRoot -Parent)
try {
  pi -e ./integrations/pi/index.ts --provider $Provider --model $Model --thinking $Thinking -p $prompt
} finally {
  Pop-Location
}
