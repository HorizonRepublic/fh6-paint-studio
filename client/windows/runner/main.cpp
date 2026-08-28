#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <windows.h>

#include <string>

#include "flutter_window.h"
#include "utils.h"

int APIENTRY wWinMain(_In_ HINSTANCE instance, _In_opt_ HINSTANCE prev,
                      _In_ wchar_t *command_line, _In_ int show_command) {
  // Attach to console when present (e.g., 'flutter run') or create a
  // new console when running with a debugger.
  if (!::AttachConsole(ATTACH_PARENT_PROCESS) && ::IsDebuggerPresent()) {
    CreateAndAttachConsole();
  }

  // Initialize COM, so that it is available for use in the library and/or
  // plugins.
  ::CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

  // The release keeps everything but this exe inside bin\. The Flutter runtime
  // is delay-loaded (see CMakeLists.txt), so redirecting the loader here — one
  // line before the first flutter:: call — is early enough. A dev build has no
  // bin\ beside the exe, and both probes fall back to the stock layout.
  wchar_t exe_path[MAX_PATH];
  ::GetModuleFileNameW(nullptr, exe_path, MAX_PATH);
  std::wstring exe_dir(exe_path);
  exe_dir = exe_dir.substr(0, exe_dir.find_last_of(L"\\/"));
  const std::wstring bin = exe_dir + L"\\bin";
  if (::GetFileAttributesW(bin.c_str()) != INVALID_FILE_ATTRIBUTES) {
    ::SetDllDirectoryW(bin.c_str());
  }
  std::wstring assets = bin + L"\\data";
  if (::GetFileAttributesW(assets.c_str()) == INVALID_FILE_ATTRIBUTES) {
    assets = exe_dir + L"\\data";
  }

  // A half-unpacked folder would otherwise die before any code that could say
  // so — the delay-loaded runtime just never resolves and the process ends in
  // silence. This is the one message the user gets instead.
  const bool runtime_present =
      ::GetFileAttributesW((bin + L"\\flutter_windows.dll").c_str()) !=
          INVALID_FILE_ATTRIBUTES ||
      ::GetFileAttributesW((exe_dir + L"\\flutter_windows.dll").c_str()) !=
          INVALID_FILE_ATTRIBUTES;
  if (!runtime_present ||
      ::GetFileAttributesW(assets.c_str()) == INVALID_FILE_ATTRIBUTES) {
    // ASCII only: MSVC reads this file without a BOM as ANSI, and anything
    // fancier arrives on screen as mojibake.
    ::MessageBoxW(nullptr,
                  L"Part of the app is missing next to FH6 Paint Studio.exe.\n"
                  L"Extract the WHOLE archive into one folder (the exe needs "
                  L"its bin\\ folder beside it), then run it again.",
                  L"FH6 Paint Studio", MB_ICONERROR | MB_OK);
    return EXIT_FAILURE;
  }

  flutter::DartProject project(assets);

  // Skia, explicitly. Flutter 3.47 turned Impeller-on-Vulkan on by default on
  // Windows, and this app is its worst case: the engine service saturates the
  // same GPU with Vulkan compute while the UI renders, and users on older
  // cards reported extreme lag the moment 3.0.0 shipped on it (v2.2.0, built
  // on 3.44/Skia, was fine). Revisit only with a deliberate A/B on weak
  // hardware, not as a side effect of an SDK bump.
  project.set_impeller_switch(flutter::ImpellerSwitch::Disabled);

  std::vector<std::string> command_line_arguments =
      GetCommandLineArguments();

  project.set_dart_entrypoint_arguments(std::move(command_line_arguments));

  FlutterWindow window(project);
  Win32Window::Point origin(10, 10);
  Win32Window::Size size(1440, 900);
  if (!window.Create(L"FH6 Paint Studio", origin, size)) {
    return EXIT_FAILURE;
  }
  window.SetQuitOnClose(true);

  ::MSG msg;
  while (::GetMessage(&msg, nullptr, 0, 0)) {
    ::TranslateMessage(&msg);
    ::DispatchMessage(&msg);
  }

  ::CoUninitialize();
  return EXIT_SUCCESS;
}
