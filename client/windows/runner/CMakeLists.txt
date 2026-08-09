cmake_minimum_required(VERSION 3.14)
project(runner LANGUAGES CXX)

# Define the application target. To change its name, change BINARY_NAME in the
# top-level CMakeLists.txt, not the value here, or `flutter run` will no longer
# work.
#
# Any new source files that you add to the application should be added here.
add_executable(${BINARY_NAME} WIN32
  "flutter_window.cpp"
  "main.cpp"
  "utils.cpp"
  "win32_window.cpp"
  "${FLUTTER_MANAGED_DIR}/generated_plugin_registrant.cc"
  "Runner.rc"
  "runner.exe.manifest"
)

# Apply the standard set of build settings. This can be removed for applications
# that need different build settings.
apply_standard_settings(${BINARY_NAME})

# Add preprocessor definitions for the build version.
target_compile_definitions(${BINARY_NAME} PRIVATE "FLUTTER_VERSION=\"${FLUTTER_VERSION}\"")
target_compile_definitions(${BINARY_NAME} PRIVATE "FLUTTER_VERSION_MAJOR=${FLUTTER_VERSION_MAJOR}")
target_compile_definitions(${BINARY_NAME} PRIVATE "FLUTTER_VERSION_MINOR=${FLUTTER_VERSION_MINOR}")
target_compile_definitions(${BINARY_NAME} PRIVATE "FLUTTER_VERSION_PATCH=${FLUTTER_VERSION_PATCH}")
target_compile_definitions(${BINARY_NAME} PRIVATE "FLUTTER_VERSION_BUILD=${FLUTTER_VERSION_BUILD}")

# Disable Windows macros that collide with C++ standard library functions.
target_compile_definitions(${BINARY_NAME} PRIVATE "NOMINMAX")

# Add dependency libraries and include directories. Add any application-specific
# dependencies here.
# flutter_wrapper_plugin as well as the app wrapper: the runner owns a method
# channel of its own — the caption buttons are Flutter widgets that have to reach
# Win32 — and MethodChannel lives in the plugin half of the C++ wrapper.
target_link_libraries(${BINARY_NAME} PRIVATE flutter flutter_wrapper_app flutter_wrapper_plugin)
# The release ships every DLL inside bin\, not beside the exe, so the user sees
# one .exe and nothing else (scripts/build-client-release.ps1). A normal import
# would be resolved by the loader before wWinMain runs and fail; delay-loading
# defers each DLL to its first call, which happens after main.cpp has pointed
# the loader at bin\. A NEW PLUGIN'S DLL MUST BE ADDED HERE — left off the
# list it stays an ordinary import and the release exe will not start.
target_link_libraries(${BINARY_NAME} PRIVATE "delayimp.lib")
target_link_options(${BINARY_NAME} PRIVATE
  "/DELAYLOAD:flutter_windows.dll"
  "/DELAYLOAD:desktop_drop_plugin.dll"
  "/DELAYLOAD:file_selector_windows_plugin.dll"
)
target_link_libraries(${BINARY_NAME} PRIVATE "dwmapi.lib")
# SetWindowSubclass, for making the caption hit test fall through to the parent.
target_link_libraries(${BINARY_NAME} PRIVATE "comctl32.lib")
target_include_directories(${BINARY_NAME} PRIVATE "${CMAKE_SOURCE_DIR}")

# Run the Flutter tool portions of the build. This must not be removed.
add_dependencies(${BINARY_NAME} flutter_assemble)
