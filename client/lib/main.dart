/// FH6 Paint Studio — the Flutter client.
///
/// The engine is a separate process this app talks to over a loopback socket,
/// so nothing here touches the GPU, decodes an image, or writes to the game's
/// memory. That is what keeps the client free of FFI: every capability lives
/// behind `internal/ipc` on the Go side.
library;

import 'dart:io';

import 'package:flutter/material.dart';

import 'state/logfile.dart';
import 'state/prefs.dart';
import 'state/studio.dart';
import 'ui/shell.dart';
import 'ui/strings.dart';
import 'ui/tokens.dart';

/// Where the engine service lives.
///
/// Beside this executable in a release, because that is where the installer puts
/// it. In development the build output is buried under `build/windows/...`, so
/// fall back to the repository's `bin/` — resolved from the working directory,
/// which is the repo when the app is launched by `flutter run`. FH6_ENGINED
/// overrides both.
String engineExecutable() {
  final override = Platform.environment['FH6_ENGINED'];
  if (override != null && override.isNotEmpty) return override;

  final dir = File(Platform.resolvedExecutable).parent.path;
  final sep = Platform.pathSeparator;
  // The release tucks the service into bin\engine so the folder a user opens
  // holds one .exe — the one they should run.
  final tucked = File('$dir${sep}bin${sep}engine${sep}engined.exe');
  if (tucked.existsSync()) return tucked.path;
  final beside = File('$dir${sep}engined.exe');
  if (beside.existsSync()) return beside.path;
  return 'bin${sep}engined.exe';
}

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  // Before anything else, so a crash during startup is in the file too.
  await AppLog.init();
  runApp(const StudioApp());
}

class StudioApp extends StatefulWidget {
  const StudioApp({super.key});

  @override
  State<StudioApp> createState() => _StudioAppState();
}

class _StudioAppState extends State<StudioApp> {
  final studio = Studio();
  String? connectError;
  Lang lang = languages.first;
  Prefs? _prefs;

  @override
  void initState() {
    super.initState();
    // The language is restored before the engine is reached, so the failure
    // screen is already in the user's language if the engine never starts.
    Prefs.load().then((p) {
      if (!mounted) return;
      setState(() {
        _prefs = p;
        // No saved choice means first run: follow the OS rather than assuming
        // English, which is the one guess that is wrong for most people here.
        final saved = p.get<String>('lang');
        lang = saved != null
            ? langFor(saved)
            : langForSystem(Platform.localeName);
      });
    });
    // Connecting is the app's first act, and its failure is the one the user
    // most needs explained: without the engine there is nothing this window can
    // do, so the reason has to reach the screen rather than a log file.
    studio.connect(engineExecutable()).catchError((Object e) {
      AppLog.write('error', 'engine did not start: $e');
      if (mounted) setState(() => connectError = '$e');
    });
  }

  void _setLanguage(Lang next) {
    setState(() => lang = next);
    _prefs?.set('lang', next.code);
  }

  @override
  void dispose() {
    studio.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'FH6 Paint Studio',
      debugShowCheckedModeBanner: false,
      theme: ThemeData.dark(
        useMaterial3: true,
      ).copyWith(scaffoldBackgroundColor: T.desk),
      home: Strings(
        lang: lang,
        child: Scaffold(
          backgroundColor: T.desk,
          body: connectError == null
              ? Shell(studio: studio, lang: lang, onLanguage: _setLanguage)
              : _NoEngine(message: connectError!),
        ),
      ),
    );
  }
}

class _NoEngine extends StatelessWidget {
  const _NoEngine({required this.message});
  final String message;

  @override
  Widget build(BuildContext context) => Center(
    child: SizedBox(
      width: 460,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const HalftoneMark(size: 46, tile: false),
          const SizedBox(height: 22),
          Text(
            context.s('engineFailed'),
            style: T.text(17, color: T.title, weight: FontWeight.w600),
          ),
          const SizedBox(height: 8),
          Text(
            message,
            textAlign: TextAlign.center,
            style: T.monoText(11.5, color: T.hint),
          ),
        ],
      ),
    ),
  );
}
