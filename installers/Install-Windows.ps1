# Install Yarr.It on Windows (sideload build).
#
# WHY THIS SCRIPT EXISTS
# The package is signed with a self-signed certificate because it has not been
# through the Microsoft Store yet, and Windows refuses to install an app package
# whose signature does not chain to a trusted root.
#
# WHY THE FIRST VERSION OF THIS SCRIPT FAILED WITH 0x800B0109
# It imported the certificate into TrustedPeople only. That is the documented
# store for sideloaded packages, but it only suffices when the signing
# certificate chains up to a root Windows already trusts. This certificate is
# self-signed -- Subject and Issuer are identical -- so it IS its own root, and
# with nothing in the root store the chain terminated untrusted:
#
#     A certificate chain processed, but terminated in a root certificate
#     which is not trusted by the trust provider.   (0x800B0109)
#
# So it has to go into BOTH stores: Root to make the chain valid, TrustedPeople
# to authorise it for app packages.
#
# READ THIS BEFORE RUNNING
# Adding a certificate to Trusted Root Certification Authorities is a real
# security decision. Until you remove it, this machine trusts ANYTHING signed by
# that private key. Only proceed if you trust whoever built the package, and use
# the removal commands printed at the end when you have finished testing.
#
# Run in an ADMIN PowerShell, from the folder holding the .msix and .cer:
#     Set-ExecutionPolicy -Scope Process Bypass -Force
#     .\Install-Windows.ps1

#Requires -RunAsAdministrator
$ErrorActionPreference = 'Stop'

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$cer  = Get-ChildItem -Path $here -Filter '*.cer'  | Select-Object -First 1
$msix = Get-ChildItem -Path $here -Filter '*.msix' | Select-Object -First 1

if (-not $cer)  { throw "No .cer found next to this script." }
if (-not $msix) { throw "No .msix found next to this script." }

$cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2 $cer.FullName

Write-Host ""
Write-Host "About to trust this signing certificate:" -ForegroundColor Yellow
Write-Host "  Subject    $($cert.Subject)"
Write-Host "  Thumbprint $($cert.Thumbprint)"
Write-Host "  Expires    $($cert.NotAfter)"
Write-Host ""
Write-Host "It will be added to Trusted Root Certification Authorities and to" -ForegroundColor Yellow
Write-Host "Trusted People for this machine. Windows will then trust anything"  -ForegroundColor Yellow
Write-Host "signed by that key until you remove it."                            -ForegroundColor Yellow
Write-Host ""
if ((Read-Host "Type YES to continue") -ne 'YES') { Write-Host "Cancelled."; exit 1 }

Write-Host "Trusting the certificate..." -ForegroundColor Cyan
Import-Certificate -FilePath $cer.FullName -CertStoreLocation 'Cert:\LocalMachine\Root'          | Out-Null
Import-Certificate -FilePath $cer.FullName -CertStoreLocation 'Cert:\LocalMachine\TrustedPeople' | Out-Null

Write-Host "Installing $($msix.Name)..." -ForegroundColor Cyan
Add-AppxPackage -Path $msix.FullName

Write-Host ""
Write-Host "Done. Launch 'Yarr.It' from the Start menu." -ForegroundColor Green
Write-Host ""
Write-Host "To remove the app AND withdraw the certificate trust:" -ForegroundColor DarkGray
Write-Host "  Get-AppxPackage *MOVEWEIGHT.Stream* | Remove-AppxPackage"        -ForegroundColor DarkGray
Write-Host "  Remove-Item Cert:\LocalMachine\Root\$($cert.Thumbprint)"          -ForegroundColor DarkGray
Write-Host "  Remove-Item Cert:\LocalMachine\TrustedPeople\$($cert.Thumbprint)" -ForegroundColor DarkGray
