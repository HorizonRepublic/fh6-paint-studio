#include "flutter_window.h"

#include <commctrl.h>

#include <shobjidl_core.h>
#include <windowsx.h>

#include <atomic>
#include <optional>

#include "flutter/generated_plugin_registrant.h"

namespace {

// The taskbar button, created on first use and held for the process. COM is
// already initialised by the Flutter runner, so this only has to ask for the
// object. A null return means the shell is not available (a rare session type,
// or the shell restarted) — every caller treats progress as optional.
ITaskbarList3* ShellTaskbar() {
  static ITaskbarList3* bar = [] {
    ITaskbarList3* p = nullptr;
    if (FAILED(CoCreateInstance(CLSID_TaskbarList, nullptr, CLSCTX_ALL,
                                IID_PPV_ARGS(&p)))) {
      return static_cast<ITaskbarList3*>(nullptr);
    }
    if (p && FAILED(p->HrInit())) {
      p->Release();
      return static_cast<ITaskbarList3*>(nullptr);
    }
    return p;
  }();
  return bar;
}

// Kept in step with win32_window.cpp and lib/ui/window.dart.
constexpr int kCaptionHeightDip = 52;

// How much of the caption's right end belongs to Flutter's own controls. Dart
// measures the real cluster and pushes it here after every layout, because the
// language button is as wide as that language's name for itself. The starting
// value errs WIDE — covering the widest locale's cluster — because the two
// failure directions are not symmetric: a band too wide only costs drag area
// until the first report lands, a band too narrow sends clicks on the leftmost
// caption buttons to the window drag instead. Mirrored by kStartupControlsDip
// in test/caption_test.dart, which checks it against every locale.
std::atomic<int> g_controls_dip{1050};

int ScaledFor(int dip, HWND window) {
  UINT dpi = GetDpiForWindow(window);
  if (dpi == 0) dpi = 96;
  return MulDiv(dip, static_cast<int>(dpi), 96);
}

// Flutter's view is a CHILD window filling the client area, so it receives the
// mouse and the parent's WM_NCHITTEST — where the title bar lives — is never
// consulted. Returning HTTRANSPARENT tells Windows to keep looking underneath,
// which lands on the parent and gives back drag, double-click-to-maximise, the
// system menu and Aero Snap.
//
// Only the DRAG part of the caption is made transparent. Everything in the
// controls strip at the right, the three window buttons included, stays with
// Flutter: they are ordinary widgets that hover, depress and animate like the
// rest of the app, and they reach Win32 through the method channel below. The
// alternative — reporting HTCLOSE/HTMAXBUTTON to Windows — buys the Snap
// Layouts flyout and costs every one of those affordances, because a button
// answered by the non-client hit test never sees a Flutter pointer event.
LRESULT CALLBACK ChildProc(HWND hwnd, UINT message, WPARAM wparam,
                           LPARAM lparam, UINT_PTR id, DWORD_PTR data) {
  if (message == WM_NCHITTEST) {
    HWND parent = GetParent(hwnd);
    POINT cursor = {GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
    ScreenToClient(hwnd, &cursor);
    RECT client = {};
    GetClientRect(hwnd, &client);

    const int caption = ScaledFor(kCaptionHeightDip, parent ? parent : hwnd);
    // +4dip cushion: Dart reports the exact distance to the cluster's left
    // edge, and with zero margin the button's first border pixel rounds into
    // the drag band. Four dip of lost drag area buys the whole edge.
    const int controls =
        ScaledFor(g_controls_dip.load() + 4, parent ? parent : hwnd);
    if (cursor.y < caption && cursor.x < client.right - controls) {
      return HTTRANSPARENT;
    }
  }
  return DefSubclassProc(hwnd, message, wparam, lparam);
}

}  // namespace

FlutterWindow::FlutterWindow(const flutter::DartProject& project)
    : project_(project) {}

FlutterWindow::~FlutterWindow() {}

bool FlutterWindow::OnCreate() {
  if (!Win32Window::OnCreate()) {
    return false;
  }

  RECT frame = GetClientArea();

  // The size here must match the window dimensions to avoid unnecessary surface
  // creation / destruction in the startup path.
  flutter_controller_ = std::make_unique<flutter::FlutterViewController>(
      frame.right - frame.left, frame.bottom - frame.top, project_);
  // Ensure that basic setup of the controller was successful.
  if (!flutter_controller_->engine() || !flutter_controller_->view()) {
    return false;
  }
  RegisterPlugins(flutter_controller_->engine());
  HWND view = flutter_controller_->view()->GetNativeWindow();
  SetChildContent(view);
  SetWindowSubclass(view, ChildProc, 1, 0);

  registrar_ = std::make_unique<flutter::PluginRegistrarWindows>(
      flutter_controller_->engine()->GetRegistrarForPlugin("fh6.window"));
  window_channel_ =
      std::make_unique<flutter::MethodChannel<flutter::EncodableValue>>(
          registrar_->messenger(), "fh6/window",
          &flutter::StandardMethodCodec::GetInstance());
  window_channel_->SetMethodCallHandler(
      [this](const flutter::MethodCall<flutter::EncodableValue>& call,
             std::unique_ptr<flutter::MethodResult<flutter::EncodableValue>>
                 result) {
        HWND hwnd = GetHandle();
        const std::string& name = call.method_name();
        if (name == "minimize") {
          ShowWindow(hwnd, SW_MINIMIZE);
        } else if (name == "toggleMaximize") {
          ShowWindow(hwnd, IsZoomed(hwnd) ? SW_RESTORE : SW_MAXIMIZE);
        } else if (name == "close") {
          PostMessage(hwnd, WM_CLOSE, 0, 0);
        } else if (name == "isMaximized") {
          result->Success(flutter::EncodableValue(IsZoomed(hwnd) != 0));
          return;
        } else if (name == "chime") {
          MessageBeep(MB_OK);
        } else if (name == "flash") {
          // Only when the window is not the one being looked at. Flashing the
          // foreground window is the kind of thing that makes people turn
          // notifications off entirely.
          if (GetForegroundWindow() != hwnd) {
            FLASHWINFO fw = {};
            fw.cbSize = sizeof(fw);
            fw.hwnd = hwnd;
            fw.dwFlags = FLASHW_TRAY | FLASHW_TIMERNOFG;
            fw.uCount = 3;
            FlashWindowEx(&fw);
          }
        } else if (name == "progress") {
          // The taskbar button IS the progress bar for a job the user walks
          // away from — a fit runs for minutes and they are usually alt-tabbed
          // into the game. Explorer copies and browser downloads set the same
          // state, so it needs no explaining.
          //
          // Argument: 0..1 while running, -1 to clear, -2 for the error state.
          if (const auto* v = std::get_if<double>(call.arguments())) {
            if (auto* bar = ShellTaskbar()) {
              if (*v < -1.5) {
                bar->SetProgressState(hwnd, TBPF_ERROR);
                bar->SetProgressValue(hwnd, 1, 1);
              } else if (*v < 0) {
                bar->SetProgressState(hwnd, TBPF_NOPROGRESS);
              } else {
                bar->SetProgressState(hwnd, TBPF_NORMAL);
                bar->SetProgressValue(
                    hwnd, static_cast<ULONGLONG>(*v * 1000.0), 1000);
              }
            }
          }
        } else if (name == "setControlsWidth") {
          if (const auto* w = std::get_if<int32_t>(call.arguments())) {
            g_controls_dip.store(*w);
          }
        } else {
          result->NotImplemented();
          return;
        }
        result->Success();
      });

  flutter_controller_->engine()->SetNextFrameCallback([&]() {
    this->Show();
  });

  // Flutter can complete the first frame before the "show window" callback is
  // registered. The following call ensures a frame is pending to ensure the
  // window is shown. It is a no-op if the first frame hasn't completed yet.
  flutter_controller_->ForceRedraw();

  return true;
}

void FlutterWindow::OnDestroy() {
  if (flutter_controller_) {
    flutter_controller_ = nullptr;
  }

  Win32Window::OnDestroy();
}

LRESULT
FlutterWindow::MessageHandler(HWND hwnd, UINT const message,
                              WPARAM const wparam,
                              LPARAM const lparam) noexcept {
  // Give Flutter, including plugins, an opportunity to handle window messages.
  if (flutter_controller_) {
    std::optional<LRESULT> result =
        flutter_controller_->HandleTopLevelWindowProc(hwnd, message, wparam,
                                                      lparam);
    if (result) {
      return *result;
    }
  }

  switch (message) {
    case WM_FONTCHANGE:
      flutter_controller_->engine()->ReloadSystemFonts();
      break;
    case WM_SIZE:
      PushWindowState();
      break;
  }

  return Win32Window::MessageHandler(hwnd, message, wparam, lparam);
}

void FlutterWindow::PushWindowState() {
  if (!window_channel_) return;
  const bool maximized = IsZoomed(GetHandle()) != 0;
  if (maximized == last_maximized_) return;
  last_maximized_ = maximized;
  window_channel_->InvokeMethod(
      "maximized",
      std::make_unique<flutter::EncodableValue>(maximized));
}
