; PicDup Windows installer (Inno Setup 6)
; Build with:
;   "C:\Program Files\Inno Setup 6\ISCC.exe" installer\PicDup.iss
; The installer is per-user so the portable PicDupData directory stays writable.

#define MyAppName "PicDup"
#define MyAppVersion "1.0.0"
#define MyAppPublisher "孤独雪 (guduxue)"
#define MyAppURL "https://github.com/QQ510447097/PicDup"
#define MyAppExeName "PicDup.exe"

[Setup]
AppId={{850985ed-b7bb-4e3b-b8ab-c162d1f9cd68}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}/releases
AppCopyright=Copyright (c) 2026 孤独雪 (guduxue)
DefaultDirName={localappdata}\Programs\PicDup
DefaultGroupName=PicDup
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=..\dist
OutputBaseFilename=PicDup-v{#MyAppVersion}-win64-setup
SetupIconFile=..\build\windows\icon.ico
UninstallDisplayIcon={app}\{#MyAppExeName}
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
LicenseFile=..\LICENSE
CloseApplications=yes
RestartApplications=no
SetupLogging=yes
VersionInfoVersion=1.0.0.0
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription={#MyAppName} 图片去重与清理
VersionInfoProductName={#MyAppName}
VersionInfoProductVersion={#MyAppVersion}

[Languages]
Name: "chinesesimplified"; MessagesFile: "ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加图标："; Flags: unchecked

[Files]
Source: "..\bin\PicDup.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\THIRD_PARTY_NOTICES.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\PicDup"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\PicDup"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "启动 PicDup"; Flags: nowait postinstall skipifsilent

[Code]
const
  WebView2Client = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';

function WebView2Installed: Boolean;
var
  Version: String;
begin
  Result :=
    RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\' + WebView2Client, 'pv', Version) or
    RegQueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\' + WebView2Client, 'pv', Version) or
    RegQueryStringValue(HKCU, 'Software\Microsoft\EdgeUpdate\Clients\' + WebView2Client, 'pv', Version) or
    DirExists(ExpandConstant('{commonpf32}\Microsoft\EdgeWebView\Application')) or
    DirExists(ExpandConstant('{commonpf64}\Microsoft\EdgeWebView\Application'));
end;

function InitializeSetup(): Boolean;
var
  ErrorCode: Integer;
begin
  Result := True;
  if not WebView2Installed then
  begin
    if MsgBox('未检测到 Microsoft WebView2 Runtime。' + #13#10 + #13#10 +
      'PicDup 需要 WebView2 才能启动。是否打开微软下载页面？安装完成后可以重新运行本安装包。',
      mbConfirmation, MB_YESNO) = IDYES then
      ShellExec('open', 'https://go.microsoft.com/fwlink/p/?LinkId=2124703', '', '', SW_SHOWNORMAL, ewNoWait, ErrorCode);
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if (CurUninstallStep = usPostUninstall) and DirExists(ExpandConstant('{app}\PicDupData')) then
    MsgBox('设置、缓存和隔离区已保留在：' + #13#10 + ExpandConstant('{app}\PicDupData'), mbInformation, MB_OK);
end;
