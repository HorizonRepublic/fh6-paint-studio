/// Getting an image into the app: the OS file dialog, and drag-and-drop.
///
/// Both use the platform's own mechanism rather than anything drawn here. The
/// picker is the real Explorer dialog, owned by this window, so it centres on
/// the app and blocks it the way every other Windows program does; the drop
/// target is the OLE one, so a drag from Explorer shows the system's own cursor
/// feedback. An in-app imitation of either is instantly recognisable as fake.
library;

import 'package:desktop_drop/desktop_drop.dart';
import 'package:file_selector/file_selector.dart';
import 'package:flutter/widgets.dart';

import 'strings.dart';
import 'tokens.dart';

const _imageExtensions = <String>[
  'png',
  'jpg',
  'jpeg',
  'webp',
  'bmp',
  'tif',
  'tiff',
];

/// Opens the system picker. Returns null when the user cancels.
Future<String?> pickImage() async {
  final file = await openFile(
    acceptedTypeGroups: const [
      XTypeGroup(label: 'Images', extensions: _imageExtensions),
      XTypeGroup(label: 'All files', extensions: ['*']),
    ],
  );
  return file?.path;
}

bool _isImage(String path) {
  final dot = path.lastIndexOf('.');
  if (dot < 0) return false;
  return _imageExtensions.contains(path.substring(dot + 1).toLowerCase());
}

/// Asks where to write the geometry. Returns null when the user cancels. The
/// suggested name follows the source image, which is what makes a folder of
/// exports readable later.
Future<String?> saveGeometryTo(String suggested) async {
  final base = suggested.contains('.')
      ? suggested.substring(0, suggested.lastIndexOf('.'))
      : suggested;
  final location = await getSaveLocation(
    suggestedName: '$base.forza.json',
    acceptedTypeGroups: const [
      XTypeGroup(label: 'Forza geometry', extensions: ['json']),
    ],
  );
  return location?.path;
}

/// Wraps the whole window as a drop target. Anything droppable lands here, so a
/// user never has to find the one rectangle that accepts a file.
class DropCatcher extends StatefulWidget {
  const DropCatcher({super.key, required this.child, required this.onFile});

  final Widget child;
  final void Function(String path) onFile;

  @override
  State<DropCatcher> createState() => _DropCatcherState();
}

class _DropCatcherState extends State<DropCatcher> {
  bool _over = false;

  @override
  Widget build(BuildContext context) {
    return DropTarget(
      onDragEntered: (_) => setState(() => _over = true),
      onDragExited: (_) => setState(() => _over = false),
      onDragDone: (detail) {
        setState(() => _over = false);
        // Several files can be dropped at once; take the first that is an image
        // rather than refusing the whole drop over one stray text file.
        for (final f in detail.files) {
          if (_isImage(f.path)) {
            widget.onFile(f.path);
            return;
          }
        }
      },
      child: Stack(
        fit: StackFit.expand,
        children: [
          widget.child,
          // A teal edge and one word in the middle. The edge alone was there
          // before and went unnoticed — at the size of a maximised window a
          // 2px border is not where anyone is looking while they drag.
          IgnorePointer(
            child: AnimatedOpacity(
              duration: Motion.fast,
              opacity: _over ? 1 : 0,
              child: DecoratedBox(
                decoration: BoxDecoration(
                  color: const Color(0x2606231F),
                  border: Border.all(color: T.teal, width: 2),
                  boxShadow: const [
                    BoxShadow(
                      color: T.tealFaint,
                      blurRadius: 40,
                      spreadRadius: -10,
                    ),
                  ],
                ),
                child: Center(
                  child: AnimatedScale(
                    duration: Motion.base,
                    curve: Motion.curve,
                    scale: _over ? 1 : 0.92,
                    child: Glass(
                      radius: 14,
                      child: Padding(
                        padding: const EdgeInsets.fromLTRB(22, 14, 22, 16),
                        child: Column(
                          mainAxisSize: MainAxisSize.min,
                          children: [
                            const HalftoneMark(size: 30, tile: false),
                            const SizedBox(height: 10),
                            Text(
                              context.s('dropHere'),
                              style: T.text(
                                14,
                                color: T.tealBright,
                                weight: FontWeight.w600,
                              ),
                            ),
                          ],
                        ),
                      ),
                    ),
                  ),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }
}
