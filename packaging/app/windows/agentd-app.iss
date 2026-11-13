; Inno Setup script for the agentd client app (Flutter Windows release).
; Compile: iscc /DAppVersion=1.2.3 /DSourceDir=<abs path to build\windows\x64\runner\Release>
;               /DOutputDir=<abs dist dir> packaging\app\windows\agentd-app.iss
; The exe inside SourceDir is Authenticode-signed BEFORE this runs; the resulting
; installer is signed afterwards by the workflow.
#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #error SourceDir is required
#endif
#ifndef OutputDir
  #define OutputDir "."
#endif

[Setup]
AppId={{6C0D8B52-3B1E-4C57-9D1E-8F4A2A7E5B10}
AppName=agentd
AppVersion={#AppVersion}
AppPublisher=Digio Soluciones Digitales SL
AppPublisherURL=https://github.com/digiogithub/agentd
DefaultDirName={autopf}\agentd
DefaultGroupName=agentd
UninstallDisplayIcon={app}\agentd_app.exe
OutputDir={#OutputDir}
OutputBaseFilename=agentd-app_{#AppVersion}_windows_x64_setup
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequiredOverridesAllowed=dialog
PrivilegesRequired=lowest
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ChangesAssociations=yes

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: recursesubdirs ignoreversion createallsubdirs

[Icons]
Name: "{group}\agentd"; Filename: "{app}\agentd_app.exe"
Name: "{autodesktop}\agentd"; Filename: "{app}\agentd_app.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked

; agentd:// URL scheme (per-user when installed unprivileged, machine-wide otherwise).
[Registry]
Root: HKA; Subkey: "Software\Classes\agentd"; ValueType: string; ValueName: ""; ValueData: "URL:agentd Protocol"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\Classes\agentd"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""
Root: HKA; Subkey: "Software\Classes\agentd\DefaultIcon"; ValueType: string; ValueName: ""; ValueData: "{app}\agentd_app.exe,0"
Root: HKA; Subkey: "Software\Classes\agentd\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\agentd_app.exe"" ""%1"""

[Run]
Filename: "{app}\agentd_app.exe"; Description: "Launch agentd"; Flags: nowait postinstall skipifsilent
