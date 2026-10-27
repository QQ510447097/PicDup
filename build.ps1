param(
    [ValidateSet('gui', 'dev', 'server')]
    [string]$Mode = 'gui'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Push-Location $root
try {
    New-Item -ItemType Directory -Force -Path 'bin' | Out-Null

    switch ($Mode) {
        'dev' {
            Write-Host 'Building development GUI binary...'
            go build -o 'bin/PicDup-dev.exe' .
            if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
            Write-Host 'Built bin/PicDup-dev.exe'
        }
        'server' {
            Write-Host 'Building server binary...'
            go build -o 'bin/PicDup-server.exe' .
            if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
            Write-Host 'Built bin/PicDup-server.exe'
        }
        default {
            $syso = 'wails_windows_amd64.syso'
            Remove-Item $syso -ErrorAction SilentlyContinue
            $wails = Get-Command wails3 -ErrorAction SilentlyContinue
            if ($wails) {
                Write-Host 'Generating Windows icon/manifest resources...'
                wails3 generate syso `
                    -arch amd64 `
                    -icon build/windows/icon.ico `
                    -manifest build/windows/wails.exe.manifest `
                    -info build/windows/info.json `
                    -out $syso
                if ($LASTEXITCODE -ne 0) { throw 'wails3 generate syso failed' }
            } else {
                Write-Warning 'wails3 not found; building without embedded icon/version resources.'
            }

            try {
                Write-Host 'Building release GUI binary...'
                go build -tags production -trimpath -buildvcs=false `
                    -ldflags='-w -s -H windowsgui' `
                    -o 'bin/PicDup.exe' .
                if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
            }
            finally {
                Remove-Item $syso -ErrorAction SilentlyContinue
            }
            Write-Host 'Built bin/PicDup.exe'
        }
    }
}
finally {
    Pop-Location
}
