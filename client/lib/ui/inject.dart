/// Getting a finished run into the game, and out to a file.
///
/// Injection is the only thing this app does that reaches outside itself: it
/// writes into the layer table of a running copy of FH6. So the panel states
/// its two preconditions plainly — the template's layer count, which cannot be
/// guessed, and what has to happen afterwards, because the game only re-derives
/// the vinyl from the written word when it is saved and reloaded. A user who
/// judges the result before reloading is looking at the previous mesh.
library;

import 'package:flutter/material.dart';

import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

class InjectPopover extends StatefulWidget {
  const InjectPopover({super.key, required this.studio, required this.onClose});

  final Studio studio;
  final VoidCallback onClose;

  @override
  State<InjectPopover> createState() => _InjectPopoverState();
}

class _InjectPopoverState extends State<InjectPopover> {
  late final _layers = TextEditingController(
    text: widget.studio.injectLayers > 0 ? '${widget.studio.injectLayers}' : '',
  );

  @override
  void dispose() {
    _layers.dispose();
    super.dispose();
  }

  Studio get studio => widget.studio;

  @override
  Widget build(BuildContext context) {
    final shapes = (studio.geometry?['shapes'] as List?)?.length ?? 0;
    final layers = int.tryParse(_layers.text.trim()) ?? 0;
    final short = layers > 0 && layers < shapes;

    return Glass(
      child: SizedBox(
        width: 300,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(13, 11, 13, 6),
              child: Text('INJECT INTO FH6', style: T.label),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(13, 0, 13, 4),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    context.s('templateLayers'),
                    style: T.text(12.5, color: T.body),
                  ),
                  const SizedBox(height: 2),
                  Text(
                    'the exact layer count of the group open in the editor',
                    style: T.text(10.5, color: T.hint),
                  ),
                  const SizedBox(height: 7),
                  _Field(
                    controller: _layers,
                    hint: 'e.g. 3000',
                    onChanged: (v) {
                      studio.setInjectLayers(int.tryParse(v.trim()) ?? 0);
                      setState(() {});
                    },
                  ),
                  if (short) ...[
                    const SizedBox(height: 7),
                    _Warning(
                      'Only $layers of $shapes shapes fit — the rest will be '
                      'dropped in-game. Use a larger template.',
                    ),
                  ],
                  const SizedBox(height: 13),
                  Row(
                    children: [
                      Text(
                        context.s('scale'),
                        style: T.text(12.5, color: T.body),
                      ),
                      const Spacer(),
                      Text(
                        studio.injectScale.toStringAsFixed(2),
                        style: T.monoText(12, color: T.dim),
                      ),
                    ],
                  ),
                  SliderTheme(
                    data: SliderThemeData(
                      trackHeight: 5,
                      activeTrackColor: T.teal,
                      inactiveTrackColor: T.fill,
                      thumbColor: const Color(0xFFFFFFFF),
                      overlayShape: SliderComponentShape.noOverlay,
                      thumbShape: const RoundSliderThumbShape(
                        enabledThumbRadius: 8,
                      ),
                    ),
                    child: Slider(
                      value: studio.injectScale.clamp(0.25, 2.0),
                      min: 0.25,
                      max: 2.0,
                      onChanged: studio.setInjectScale,
                    ),
                  ),
                  if (!studio.injectElevated)
                    _Warning(
                      'The engine is not running as administrator. Writing to '
                      "the game's memory needs it — restart elevated if the "
                      'injection is refused.',
                    ),
                ],
              ),
            ),
            const _Hairline(),
            Padding(
              padding: const EdgeInsets.fromLTRB(10, 9, 10, 10),
              child: Row(
                children: [
                  Expanded(
                    child: studio.injectSucceeded
                        // The write landed. Saying so, and saying what to do
                        // next, is the whole point: the injector takes seconds
                        // and used to close this panel the instant it started,
                        // so nothing ever reported success and the button came
                        // back ready to be pressed again.
                        ? Row(
                            children: [
                              Text('✓', style: T.text(13, color: T.green)),
                              const SizedBox(width: 7),
                              Expanded(
                                child: Text(
                                  context.s('saveReload'),
                                  style: T.text(11, color: T.green),
                                ),
                              ),
                            ],
                          )
                        : studio.injecting
                        ? Row(
                            children: [
                              const SizedBox(
                                width: 13,
                                height: 13,
                                child: CircularProgressIndicator(
                                  strokeWidth: 2,
                                  valueColor: AlwaysStoppedAnimation(T.teal),
                                ),
                              ),
                              const SizedBox(width: 9),
                              Expanded(
                                child: Text(
                                  studio.log.isEmpty
                                      ? context.s('writing')
                                      : studio.log.last.text,
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                  style: T.monoText(10, color: T.hint),
                                ),
                              ),
                            ],
                          )
                        : Text(
                            context.s('saveReload'),
                            style: T.text(10.5, color: T.hint),
                          ),
                  ),
                  const SizedBox(width: 8),
                  Btn(
                    studio.injectSucceeded
                        ? context.s('close')
                        : (studio.injecting
                              ? context.s('writing')
                              : context.s('inject')),
                    kind: BtnKind.primary,
                    onTap: studio.injecting
                        ? null
                        : (studio.injectSucceeded
                              ? widget.onClose
                              : (layers <= 0 ? null : studio.inject)),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Field extends StatelessWidget {
  const _Field({
    required this.controller,
    required this.hint,
    required this.onChanged,
  });

  final TextEditingController controller;
  final String hint;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) => Container(
    height: 30,
    padding: const EdgeInsets.symmetric(horizontal: 9),
    alignment: Alignment.centerLeft,
    decoration: BoxDecoration(
      color: T.fillSoft,
      borderRadius: BorderRadius.circular(8),
      border: Border.all(color: T.border),
    ),
    child: TextField(
      controller: controller,
      onChanged: onChanged,
      keyboardType: TextInputType.number,
      style: T.monoText(12.5, color: T.body),
      cursorColor: T.teal,
      decoration: InputDecoration(
        isDense: true,
        border: InputBorder.none,
        hintText: hint,
        hintStyle: T.monoText(12.5, color: T.faint),
      ),
    ),
  );
}

class _Warning extends StatelessWidget {
  const _Warning(this.text);
  final String text;

  @override
  Widget build(BuildContext context) => Container(
    margin: const EdgeInsets.only(top: 8),
    padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
    decoration: BoxDecoration(
      color: const Color(0x1AE0A84B),
      borderRadius: BorderRadius.circular(8),
    ),
    child: Text(text, style: T.text(10.5, color: T.amber)),
  );
}

class _Hairline extends StatelessWidget {
  const _Hairline();
  @override
  Widget build(BuildContext context) => Container(height: 1, color: T.hairline);
}
