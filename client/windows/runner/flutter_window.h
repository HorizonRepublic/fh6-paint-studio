#ifndef RUNNER_FLUTTER_WINDOW_H_
#define RUNNER_FLUTTER_WINDOW_H_

#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <flutter/method_channel.h>
#include <flutter/plugin_registrar_windows.h>
#include <flutter/standard_method_codec.h>

#include <memory>

#include "win32_window.h"

// A window that does nothing but host a Flutter view.
class FlutterWindow : public Win32Window {
 public:
  // Creates a new FlutterWindow hosting a Flutter view running |project|.
  explicit FlutterWindow(const flutter::DartProject& project);
  virtual ~FlutterWindow();

 protected:
  // Win32Window:
  bool OnCreate() override;
  void OnDestroy() override;
  LRESULT MessageHandler(HWND window, UINT const message, WPARAM const wparam,
                         LPARAM const lparam) noexcept override;

 private:
  // Tells Dart whether the window is maximized, so the middle caption button
  // can draw restore instead of maximize however the state changed — our own
  // button, a double-click on the bar, or a drag to the top of the screen.
  void PushWindowState();

  // The project to run.
  flutter::DartProject project_;

  // The Flutter instance hosted by this window.
  std::unique_ptr<flutter::FlutterViewController> flutter_controller_;

  // The caption buttons are drawn AND driven by Dart; this is how the taps get
  // back to Win32. Kept alive for the window's lifetime.
  std::unique_ptr<flutter::PluginRegistrarWindows> registrar_;
  std::unique_ptr<flutter::MethodChannel<flutter::EncodableValue>> window_channel_;
  bool last_maximized_ = false;
};

#endif  // RUNNER_FLUTTER_WINDOW_H_
