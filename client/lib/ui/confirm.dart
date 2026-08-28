/// The one question the app ever asks in the middle of something.
///
/// It exists because two actions in the run list are one click from destroying
/// work: opening a run replaces what is on the canvas, and deleting one is
/// final. The owner deleted a run by accident before this was here.
library;

import 'package:flutter/widgets.dart';

import 'strings.dart';
import 'tokens.dart';

/// A pending question. Held by the shell rather than pushed as a route: the app
/// is one screen, and a dialog route would put a second Navigator in the way of
/// the shortcuts.
class Confirm {
  const Confirm({
    required this.title,
    required this.body,
    required this.action,
    required this.onConfirm,
    this.destructive = false,
    this.thumb,
  });

  final String title;

  /// What is at stake, in the user's words. Usually the run's own name, because
  /// "delete this run?" and "delete THAT run?" are different questions.
  final String body;

  final String action;
  final VoidCallback onConfirm;
  final bool destructive;

  /// The picture the question is about. Naming a run is not enough to recognise
  /// it — the whole point of the rail is that runs are recognised by sight.
  final ImageProvider? thumb;
}

class ConfirmDialog extends StatelessWidget {
  const ConfirmDialog({
    super.key,
    required this.confirm,
    required this.onDismiss,
  });

  final Confirm confirm;
  final VoidCallback onDismiss;

  @override
  Widget build(BuildContext context) {
    return Stack(
      children: [
        // The scrim dismisses, which is the safe answer to every question this
        // dialog asks.
        //
        // It fades WITH the dialog. Snapping two thirds of the window to black
        // in one frame while the card eases in over 150ms is the most abrupt
        // thing the app does, and it does it just as the user is being asked
        // about something irreversible — the moment attention is highest.
        Positioned.fill(
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: onDismiss,
            child: const PopIn(
              from: Offset.zero,
              child: ColoredBox(color: Color(0xAA050607)),
            ),
          ),
        ),
        Center(
          child: PopIn(
            from: const Offset(0, 10),
            child: Glass(
              radius: 16,
              // The scrim under it is already 67% opaque.
              live: false,
              child: SizedBox(
                width: 400,
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(22, 20, 22, 16),
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        confirm.title,
                        style: T.text(
                          15.5,
                          color: T.title,
                          weight: FontWeight.w600,
                        ),
                      ),
                      const SizedBox(height: 14),
                      Row(
                        children: [
                          if (confirm.thumb != null) ...[
                            Container(
                              width: 54,
                              height: 54,
                              clipBehavior: Clip.antiAlias,
                              decoration: BoxDecoration(
                                borderRadius: BorderRadius.circular(9),
                                border: Border.all(color: T.border),
                              ),
                              child: Image(
                                image: confirm.thumb!,
                                fit: BoxFit.cover,
                              ),
                            ),
                            const SizedBox(width: 12),
                          ],
                          Expanded(
                            child: Text(
                              confirm.body,
                              style: T.text(12.5, color: T.soft),
                            ),
                          ),
                        ],
                      ),
                      const SizedBox(height: 20),
                      Row(
                        mainAxisAlignment: MainAxisAlignment.end,
                        children: [
                          Btn(context.s('cancel'), onTap: onDismiss),
                          const SizedBox(width: 9),
                          Btn(
                            confirm.action,
                            kind: confirm.destructive
                                ? BtnKind.danger
                                : BtnKind.primary,
                            onTap: () {
                              onDismiss();
                              confirm.onConfirm();
                            },
                          ),
                        ],
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ],
    );
  }
}
