#include "win32_window.h"

#include <dwmapi.h>
#include <flutter_windows.h>
#include <windowsx.h>

#include "resource.h"

namespace {

/// Window attribute that enables dark mode window decorations.
///
/// Redefined in case the developer's machine has a Windows SDK older than
/// version 10.0.22000.0.
/// See: https://docs.microsoft.com/windows/win32/api/dwmapi/ne-dwmapi-dwmwindowattribute
#ifndef DWMWA_USE_IMMERSIVE_DARK_MODE
#define DWMWA_USE_IMMERSIVE_DARK_MODE 20
#endif

// Window-appearance attributes from the Windows 11 SDK, redefined so this still
// builds against an older one. DWM ignores an attribute it does not know, so on
// Windows 10 these calls simply do nothing and the window keeps its default
// chrome — no version check needed.
#ifndef DWMWA_WINDOW_CORNER_PREFERENCE
#define DWMWA_WINDOW_CORNER_PREFERENCE 33
#endif
#ifndef DWMWA_BORDER_COLOR
#define DWMWA_BORDER_COLOR 34
#endif
#ifndef DWMWA_CAPTION_COLOR
#define DWMWA_CAPTION_COLOR 35
#endif
#ifndef DWMWA_TEXT_COLOR
#define DWMWA_TEXT_COLOR 36
#endif
#ifndef DWMWCP_ROUND
#define DWMWCP_ROUND 2
#endif

// The desk colour from the design, as COLORREF (0x00BBGGRR — byte-reversed from
// the #RRGGBB the design states). The caption is drawn by the app, so this is
// only the window BORDER: at this darkness the default border reads as a bright
// outline around the app.
constexpr COLORREF kDeskColor = 0x000A0908; // #08090A

// The app draws its own title bar: the system caption is removed and this band
// of client area takes its place.
//
// The catch that makes this look impossible at first: Flutter hosts its view in
// a CHILD window covering the whole client area, so the mouse never reaches
// these hit tests and the title bar draws correctly while doing nothing. The
// child is subclassed in flutter_window.cpp and answers HTTRANSPARENT over this
// band, which is what makes the test fall through to here.
//
// Everything below is in UNSCALED pixels and converted per monitor, because a
// window can straddle two displays at different scales and the hit test has to
// agree with what Flutter drew.
//
// kCaptionHeightDip is duplicated in lib/ui/window.dart and in
// flutter_window.cpp, which MUST agree: C++ decides what a pixel does before any
// Dart runs, and Dart decides what it looks like.
constexpr int kCaptionHeightDip = 52;
constexpr int kResizeBorderDip = 6;

int Scaled(int dip, HWND window) {
  UINT dpi = GetDpiForWindow(window);
  if (dpi == 0) dpi = 96;
  return MulDiv(dip, static_cast<int>(dpi), 96);
}

// NOT named IsMaximized: winuser.h defines that as a macro expanding to
// IsZoomed, so a function by that name silently becomes a redeclaration of a
// Win32 API and every call site stops compiling.
bool WindowIsMaximized(HWND window) {
  WINDOWPLACEMENT placement = {};
  placement.length = sizeof(placement);
  if (!GetWindowPlacement(window, &placement)) return false;
  return placement.showCmd == SW_SHOWMAXIMIZED;
}

constexpr const wchar_t kWindowClassName[] = L"FLUTTER_RUNNER_WIN32_WINDOW";

/// Registry key for app theme preference.
///
/// A value of 0 indicates apps should use dark mode. A non-zero or missing
/// value indicates apps should use light mode.
constexpr const wchar_t kGetPreferredBrightnessRegKey[] =
  L"Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize";
constexpr const wchar_t kGetPreferredBrightnessRegValue[] = L"AppsUseLightTheme";

// The number of Win32Window objects that currently exist.
static int g_active_window_count = 0;

using EnableNonClientDpiScaling = BOOL __stdcall(HWND hwnd);

// Scale helper to convert logical scaler values to physical using passed in
// scale factor
int Scale(int source, double scale_factor) {
  return static_cast<int>(source * scale_factor);
}

// Dynamically loads the |EnableNonClientDpiScaling| from the User32 module.
// This API is only needed for PerMonitor V1 awareness mode.
void EnableFullDpiSupportIfAvailable(HWND hwnd) {
  HMODULE user32_module = LoadLibraryA("User32.dll");
  if (!user32_module) {
    return;
  }
  auto enable_non_client_dpi_scaling =
      reinterpret_cast<EnableNonClientDpiScaling*>(
          GetProcAddress(user32_module, "EnableNonClientDpiScaling"));
  if (enable_non_client_dpi_scaling != nullptr) {
    enable_non_client_dpi_scaling(hwnd);
  }
  FreeLibrary(user32_module);
}

}  // namespace

// Manages the Win32Window's window class registration.
class WindowClassRegistrar {
 public:
  ~WindowClassRegistrar() = default;

  // Returns the singleton registrar instance.
  static WindowClassRegistrar* GetInstance() {
    if (!instance_) {
      instance_ = new WindowClassRegistrar();
    }
    return instance_;
  }

  // Returns the name of the window class, registering the class if it hasn't
  // previously been registered.
  const wchar_t* GetWindowClass();

  // Unregisters the window class. Should only be called if there are no
  // instances of the window.
  void UnregisterWindowClass();

 private:
  WindowClassRegistrar() = default;

  static WindowClassRegistrar* instance_;

  bool class_registered_ = false;
};

WindowClassRegistrar* WindowClassRegistrar::instance_ = nullptr;

const wchar_t* WindowClassRegistrar::GetWindowClass() {
  if (!class_registered_) {
    WNDCLASS window_class{};
    window_class.hCursor = LoadCursor(nullptr, IDC_ARROW);
    window_class.lpszClassName = kWindowClassName;
    window_class.style = CS_HREDRAW | CS_VREDRAW;
    window_class.cbClsExtra = 0;
    window_class.cbWndExtra = 0;
    window_class.hInstance = GetModuleHandle(nullptr);
    window_class.hIcon =
        LoadIcon(window_class.hInstance, MAKEINTRESOURCE(IDI_APP_ICON));
    window_class.hbrBackground = 0;
    window_class.lpszMenuName = nullptr;
    window_class.lpfnWndProc = Win32Window::WndProc;
    RegisterClass(&window_class);
    class_registered_ = true;
  }
  return kWindowClassName;
}

void WindowClassRegistrar::UnregisterWindowClass() {
  UnregisterClass(kWindowClassName, nullptr);
  class_registered_ = false;
}

Win32Window::Win32Window() {
  ++g_active_window_count;
}

Win32Window::~Win32Window() {
  --g_active_window_count;
  Destroy();
}

bool Win32Window::Create(const std::wstring& title,
                         const Point& origin,
                         const Size& size) {
  Destroy();

  const wchar_t* window_class =
      WindowClassRegistrar::GetInstance()->GetWindowClass();

  const POINT target_point = {static_cast<LONG>(origin.x),
                              static_cast<LONG>(origin.y)};
  HMONITOR monitor = MonitorFromPoint(target_point, MONITOR_DEFAULTTONEAREST);
  UINT dpi = FlutterDesktopGetDpiForMonitor(monitor);
  double scale_factor = dpi / 96.0;

  HWND window = CreateWindow(
      window_class, title.c_str(), WS_OVERLAPPEDWINDOW,
      Scale(origin.x, scale_factor), Scale(origin.y, scale_factor),
      Scale(size.width, scale_factor), Scale(size.height, scale_factor),
      nullptr, nullptr, GetModuleHandle(nullptr), this);

  if (!window) {
    return false;
  }

  UpdateTheme(window);
  ApplyDesign(window);

  return OnCreate();
}

bool Win32Window::Show() {
  return ShowWindow(window_handle_, SW_SHOWNORMAL);
}

// static
LRESULT CALLBACK Win32Window::WndProc(HWND const window,
                                      UINT const message,
                                      WPARAM const wparam,
                                      LPARAM const lparam) noexcept {
  if (message == WM_NCCREATE) {
    auto window_struct = reinterpret_cast<CREATESTRUCT*>(lparam);
    SetWindowLongPtr(window, GWLP_USERDATA,
                     reinterpret_cast<LONG_PTR>(window_struct->lpCreateParams));

    auto that = static_cast<Win32Window*>(window_struct->lpCreateParams);
    EnableFullDpiSupportIfAvailable(window);
    that->window_handle_ = window;
  } else if (Win32Window* that = GetThisFromHandle(window)) {
    return that->MessageHandler(window, message, wparam, lparam);
  }

  return DefWindowProc(window, message, wparam, lparam);
}

LRESULT
Win32Window::MessageHandler(HWND hwnd,
                            UINT const message,
                            WPARAM const wparam,
                            LPARAM const lparam) noexcept {
  switch (message) {
    case WM_DESTROY:
      window_handle_ = nullptr;
      Destroy();
      if (quit_on_close_) {
        PostQuitMessage(0);
      }
      return 0;

    // Remove the system caption while keeping the frame: the client area is
    // extended over the title bar and the app draws it. The resize borders are
    // left alone, so dragging an edge still works exactly as it does elsewhere.
    case WM_NCCALCSIZE: {
      // BOTH forms of this message are answered. With wParam FALSE lParam is a
      // bare RECT rather than NCCALCSIZE_PARAMS, and skipping that form leaves
      // Windows to compute the frame its own way — which puts the system
      // caption back and the window ends up wearing two title bars.
      RECT* rect = wparam
                       ? &reinterpret_cast<NCCALCSIZE_PARAMS*>(lparam)->rgrc[0]
                       : reinterpret_cast<RECT*>(lparam);
      const RECT before = *rect;
      DefWindowProc(hwnd, WM_NCCALCSIZE, wparam, lparam);
      rect->top = before.top;
      // A maximized window is deliberately grown past the monitor by the frame
      // thickness so its borders fall off-screen. Without this inset the top of
      // the app is cut off — the classic custom-caption bug.
      if (WindowIsMaximized(hwnd)) {
        rect->top += GetSystemMetrics(SM_CYSIZEFRAME) +
                     GetSystemMetrics(SM_CXPADDEDBORDER);
      }
      return 0;
    }

    // With no system caption, Windows no longer knows which parts of the window
    // drag it, maximise it or close it. Answering here rather than in Dart is
    // what keeps every native behaviour: double-click to maximise, the system
    // menu on right-click, Aero Snap, and the Windows 11 Snap Layouts flyout,
    // which appears only for a window that reports HTMAXBUTTON.
    case WM_NCHITTEST: {
      POINT cursor = {GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
      ScreenToClient(hwnd, &cursor);
      RECT client = {};
      GetClientRect(hwnd, &client);

      const int caption = Scaled(kCaptionHeightDip, hwnd);
      const int border = Scaled(kResizeBorderDip, hwnd);

      // The resize edges are measured INSIDE the client area rather than left
      // to DefWindowProc. Its own borders sit in the frame, which on Windows 11
      // is mostly the invisible margin out in the drop shadow: the window can be
      // resized there, but only if you find it, and the cursor never changes
      // over the edge you can actually see. Answering here puts the grab band
      // where the border is drawn.
      if (!WindowIsMaximized(hwnd)) {
        const bool left = cursor.x < border;
        const bool right = cursor.x >= client.right - border;
        const bool top = cursor.y < border;
        const bool bottom = cursor.y >= client.bottom - border;
        if (top && left) return HTTOPLEFT;
        if (top && right) return HTTOPRIGHT;
        if (bottom && left) return HTBOTTOMLEFT;
        if (bottom && right) return HTBOTTOMRIGHT;
        if (left) return HTLEFT;
        if (right) return HTRIGHT;
        if (top) return HTTOP;
        if (bottom) return HTBOTTOM;
      }

      // Anything else up here that got past the child is drag area: the window
      // buttons never reach this point, they are Flutter widgets talking to
      // Win32 over the fh6/window channel.
      if (cursor.y < caption) return HTCAPTION;
      return HTCLIENT;
    }

    // Windows picks the sizing cursor from the hit-test code, but only if this
    // message reaches DefWindowProc with that code intact. Answering it here
    // keeps the double arrows on the edges and corners.
    case WM_SETCURSOR: {
      const WORD hit = LOWORD(lparam);
      if (hit != HTCLIENT) {
        return DefWindowProc(hwnd, WM_SETCURSOR, wparam, lparam);
      }
      break;
    }

    // The theme manager sends two undocumented messages to draw a classic
    // caption and frame over a window it still believes has one — which is what
    // put a second, white close button over ours. Swallowing exactly these two
    // leaves only the glyphs Flutter drew.
    //
    // WM_NCPAINT is deliberately NOT swallowed with them. It looks like it
    // belongs to the same problem and it does not: it is also what fills the
    // frame around the client area, so returning 0 leaves that band unpainted
    // and the window ends up with a white border and a strip of whatever was
    // behind it along the top.
    case 0x00AE:  // WM_NCUAHDRAWCAPTION
    case 0x00AF:  // WM_NCUAHDRAWFRAME
      return 0;

    // Activation repaints the caption too. The -1 region says "nothing to
    // repaint", which keeps the phantom buttons from flashing back every time
    // the window gains or loses focus.
    case WM_NCACTIVATE:
      return DefWindowProc(hwnd, WM_NCACTIVATE, wparam, -1);

    // A floor under the window. The UI carries fixed-width furniture — the
    // expert sheet is 620 logical px, the log drawer 720, the rail 92 — so a
    // window dragged below that does not reflow, it overflows: clipped controls
    // and Flutter's overflow stripes. Scaled by the window's own DPI so the
    // limit means the same thing at 100% and 150%.
    case WM_GETMINMAXINFO: {
      const UINT dpi = FlutterDesktopGetDpiForHWND(hwnd);
      const double scale = dpi / 96.0;
      auto* info = reinterpret_cast<MINMAXINFO*>(lparam);
      // 1100x680, not a guess: client/test/command_bar_test.dart pumps the whole
      // shell at exactly this size in all twelve locales and fails on any
      // RenderFlex overflow. 900 was picked from panel widths alone and the
      // header overran it by up to 348px in German.
      //
      // Clamped to the work area so a 1366x768 laptop at 150% scaling can still
      // size the window down to its own screen.
      RECT work = {};
      SystemParametersInfo(SPI_GETWORKAREA, 0, &work, 0);
      const LONG wantX = static_cast<LONG>(1100 * scale);
      const LONG wantY = static_cast<LONG>(680 * scale);
      const LONG maxX = work.right - work.left;
      const LONG maxY = work.bottom - work.top;
      info->ptMinTrackSize.x = (maxX > 0 && wantX > maxX) ? maxX : wantX;
      info->ptMinTrackSize.y = (maxY > 0 && wantY > maxY) ? maxY : wantY;
      return 0;
    }

    case WM_DPICHANGED: {
      auto newRectSize = reinterpret_cast<RECT*>(lparam);
      LONG newWidth = newRectSize->right - newRectSize->left;
      LONG newHeight = newRectSize->bottom - newRectSize->top;

      SetWindowPos(hwnd, nullptr, newRectSize->left, newRectSize->top, newWidth,
                   newHeight, SWP_NOZORDER | SWP_NOACTIVATE);

      return 0;
    }
    case WM_SIZE: {
      RECT rect = GetClientArea();
      if (child_content_ != nullptr) {
        // Size and position the child window.
        MoveWindow(child_content_, rect.left, rect.top, rect.right - rect.left,
                   rect.bottom - rect.top, TRUE);
      }
      return 0;
    }

    case WM_ACTIVATE:
      if (child_content_ != nullptr) {
        SetFocus(child_content_);
      }
      return 0;

    case WM_DWMCOLORIZATIONCOLORCHANGED:
      UpdateTheme(hwnd);
      return 0;
  }

  return DefWindowProc(window_handle_, message, wparam, lparam);
}

void Win32Window::Destroy() {
  OnDestroy();

  if (window_handle_) {
    DestroyWindow(window_handle_);
    window_handle_ = nullptr;
  }
  if (g_active_window_count == 0) {
    WindowClassRegistrar::GetInstance()->UnregisterWindowClass();
  }
}

Win32Window* Win32Window::GetThisFromHandle(HWND const window) noexcept {
  return reinterpret_cast<Win32Window*>(
      GetWindowLongPtr(window, GWLP_USERDATA));
}

void Win32Window::SetChildContent(HWND content) {
  child_content_ = content;
  SetParent(content, window_handle_);
  RECT frame = GetClientArea();

  MoveWindow(content, frame.left, frame.top, frame.right - frame.left,
             frame.bottom - frame.top, true);

  SetFocus(child_content_);
}

RECT Win32Window::GetClientArea() {
  RECT frame;
  GetClientRect(window_handle_, &frame);
  return frame;
}

HWND Win32Window::GetHandle() {
  return window_handle_;
}

void Win32Window::SetQuitOnClose(bool quit_on_close) {
  quit_on_close_ = quit_on_close;
}

bool Win32Window::OnCreate() {
  // No-op; provided for subclasses.
  return true;
}

void Win32Window::OnDestroy() {
  // No-op; provided for subclasses.
}

void Win32Window::ApplyDesign(HWND const window) {
  // Rounded corners are the Windows 11 default for a top-level window, but ask
  // explicitly so a window created before the preference is read still gets them.
  DWORD corner = DWMWCP_ROUND;
  DwmSetWindowAttribute(window, DWMWA_WINDOW_CORNER_PREFERENCE, &corner,
                        sizeof(corner));

  COLORREF border = kDeskColor;
  DwmSetWindowAttribute(window, DWMWA_BORDER_COLOR, &border, sizeof(border));

  // Force one frame recalculation now that the handler is in place. The window
  // was created before it could run, so without this the first layout can keep
  // whatever frame CreateWindow decided on.
  SetWindowPos(window, nullptr, 0, 0, 0, 0,
               SWP_FRAMECHANGED | SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER |
                   SWP_NOACTIVATE);
}

void Win32Window::UpdateTheme(HWND const window) {
  DWORD light_mode;
  DWORD light_mode_size = sizeof(light_mode);
  LSTATUS result = RegGetValue(HKEY_CURRENT_USER, kGetPreferredBrightnessRegKey,
                               kGetPreferredBrightnessRegValue,
                               RRF_RT_REG_DWORD, nullptr, &light_mode,
                               &light_mode_size);

  if (result == ERROR_SUCCESS) {
    BOOL enable_dark_mode = light_mode == 0;
    DwmSetWindowAttribute(window, DWMWA_USE_IMMERSIVE_DARK_MODE,
                          &enable_dark_mode, sizeof(enable_dark_mode));
  }
}
