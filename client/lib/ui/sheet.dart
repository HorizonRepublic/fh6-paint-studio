/// The settings behind the two chips.
///
/// Every switch here is a measured trade, not a preference, so each one says
/// what it costs. Two of them — the contour and false-edge terms — are the only
/// knobs in the whole engine whose sign is not consistent across pictures: each
/// helps about half of them and hurts the rest by a few percent. No automatic
/// chooser survived replication at that effect size, which is exactly why they
/// are a toggle and not a default.
library;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../state/logfile.dart';
import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

void _copyPath() {
  final p = AppLog.path;
  if (p != null) Clipboard.setData(ClipboardData(text: p));
}

/// Every engine knob the expert block edits, for the one-tap reset. Kept in
/// one place so "reset" cannot silently miss a knob the panel gained later.
const expertKeys = <String>[
  'Polish',
  'PolishIters',
  'PolishTau1',
  'Alpha',
  'AlphaMin',
  'WeightStrength',
  'Aspect',
  'Kinds',
  'KindWeights',
  'Boundary',
  'Backfit',
  'Compact',
  'StandoutTol',
  'Grid',
  'Quality',
  'Seed',
  'Random',
  'Mutated',
  'SampleBudget',
  'MaxNoImprove',
  'Overdraw',
  'RampWeight',
];

class AdvancedSheet extends StatefulWidget {
  const AdvancedSheet({super.key, required this.studio, required this.onClose});

  final Studio studio;
  final VoidCallback onClose;

  @override
  State<AdvancedSheet> createState() => _AdvancedSheetState();
}

class _AdvancedSheetState extends State<AdvancedSheet> {
  Studio get studio => widget.studio;
  final _scroll = ScrollController();

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  @override
  void initState() {
    super.initState();
    // The concrete numbers behind "default" depend on the mode AND the open
    // image; ask the engine as the panel opens so untouched knobs show what
    // will actually run.
    studio.refreshKnobDefaults();
  }

  String get _edge => studio.choices['EdgeTerms'] as String? ?? '';

  /// The two edge terms live in one string field, so a toggle has to compose
  /// with the other rather than overwrite it.
  void _setEdge({bool? eagle, bool? fe}) {
    final noEagle = eagle ?? (_edge == 'no-eagle' || _edge == 'no-both');
    final noFe = fe ?? (_edge == 'no-fe' || _edge == 'no-both');
    final value = switch ((noEagle, noFe)) {
      (true, true) => 'no-both',
      (true, false) => 'no-eagle',
      (false, true) => 'no-fe',
      _ => '',
    };
    studio.setChoice('EdgeTerms', value);
  }

  bool _boolKnob(String key, {bool fallback = false}) =>
      studio.knobValue(key) as bool? ?? fallback;

  double _numKnob(String key, double fallback) =>
      (studio.knobValue(key) as num?)?.toDouble() ?? fallback;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: studio,
      builder: (context, _) => _body(context),
    );
  }

  Widget _body(BuildContext context) {
    final noEagle = _edge == 'no-eagle' || _edge == 'no-both';
    final noFe = _edge == 'no-fe' || _edge == 'no-both';
    final mono = (studio.choices['MonoColor'] as String? ?? '').isNotEmpty;
    final polishOn = _boolKnob('Polish', fallback: true);
    final alphaOn = _boolKnob('Alpha', fallback: true);

    return Center(
      child: Glass(
        radius: 16,
        live: false,
        child: SizedBox(
          width: 620,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 14, 15, 12),
                child: Row(
                  children: [
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          context.s('advancedTitle'),
                          style: T.text(
                            15,
                            color: T.title,
                            weight: FontWeight.w600,
                          ),
                        ),
                        Text(
                          'overriding ${studio.mode}',
                          style: T.text(11.5, color: T.hint),
                        ),
                      ],
                    ),
                    const Spacer(),
                    Btn(
                      context.s('done'),
                      kind: BtnKind.primary,
                      onTap: widget.onClose,
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
              Flexible(
                // The expert panel is two dozen knobs deep; without a scrollbar
                // nothing says there is more below the fold.
                child: Scrollbar(
                  controller: _scroll,
                  child: SingleChildScrollView(
                  controller: _scroll,
                  padding: const EdgeInsets.fromLTRB(15, 6, 15, 8),
                  child: Column(
                    children: [
                      _Row(
                        label: context.s('srcRes'),
                        help: context.s('srcResHelp'),
                        value: studio.sourceRes,
                        onChanged: studio.setSourceRes,
                      ),
                      _Row(
                        label: context.s('keepIn'),
                        help: context.s('keepInHelp'),
                        value: studio.keepInside,
                        onChanged: studio.setKeepInside,
                      ),
                      _Row(
                        label: context.s('fastMode'),
                        help: context.s('fastModeHelp'),
                        value: studio.choices['AIFast'] as bool? ?? false,
                        onChanged: (v) => studio.setChoice('AIFast', v),
                      ),
                      _Row(
                        label: context.s('dropContour'),
                        help: context.s('dropContourHelp'),
                        value: noEagle,
                        onChanged: (v) => _setEdge(eagle: v),
                      ),
                      _Row(
                        label: context.s('dropFalseEdge'),
                        help: context.s('dropFalseEdgeHelp'),
                        value: noFe,
                        onChanged: (v) => _setEdge(fe: v),
                      ),
                      _Row(
                        label: context.s('singleColour'),
                        help: context.s('singleColourHelp'),
                        value: mono,
                        onChanged: (v) =>
                            studio.setChoice('MonoColor', v ? 'auto' : ''),
                      ),
                      _Row(
                        label: context.s('economy'),
                        help: context.s('economyHelp'),
                        value: studio.choices['Economy'] as bool? ?? false,
                        onChanged: (v) => studio.setChoice('Economy', v),
                      ),
                      _Stepper(
                        label: context.s('bestOf'),
                        help: context.s('bestOfHelp'),
                        value: (studio.choices['BestOf'] as num?)?.toInt() ?? 1,
                        min: 1,
                        // The engine clamps at nine; offering more would be a
                        // control that silently does nothing past the fourth tap.
                        max: 9,
                        onChanged: (v) => studio.setChoice('BestOf', v),
                      ),
                      const SizedBox(height: 6),
                      Container(height: 1, color: T.hairline),
                      Padding(
                        padding: const EdgeInsets.fromLTRB(8, 10, 8, 2),
                        child: Row(
                          children: [
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    context.s('expertTitle').toUpperCase(),
                                    style: T.label,
                                  ),
                                  const SizedBox(height: 2),
                                  Text(
                                    context.s('expertWarn'),
                                    style: T.text(10.5, color: T.hint),
                                  ),
                                ],
                              ),
                            ),
                            const SizedBox(width: 10),
                            Btn(
                              context.s('expertReset'),
                              onTap: () => studio.clearChoices(expertKeys),
                            ),
                          ],
                        ),
                      ),
                      _Section(context.s('expertGroupSmooth')),
                      _knobBool(context, 'Polish', 'kPolish', fallback: true),
                      _knobInt(
                        context,
                        'PolishIters',
                        'kPolishIters',
                        min: 20,
                        max: 2000,
                        enabled: polishOn,
                      ),
                      _knobSlider(
                        context,
                        'PolishTau1',
                        'kPolishTau',
                        min: 0.02,
                        max: 0.20,
                        fallback: 0.08,
                        enabled: polishOn,
                      ),
                      _knobBool(context, 'Alpha', 'kAlpha', fallback: true),
                      _knobSlider(
                        context,
                        'AlphaMin',
                        'kAlphaMin',
                        min: 0.05,
                        max: 1.0,
                        fallback: 0.30,
                        enabled: alphaOn,
                      ),
                      _Section(context.s('expertGroupShapes')),
                      _knobSlider(
                        context,
                        'WeightStrength',
                        'kWeightStrength',
                        min: 0,
                        max: 1,
                        fallback: 0.15,
                      ),
                      _knobSlider(
                        context,
                        'Aspect',
                        'kAspect',
                        min: 1,
                        max: 12,
                        fallback: 6,
                        decimals: 1,
                      ),
                      _knobText(context, 'Kinds', 'kKinds'),
                      _knobText(context, 'KindWeights', 'kKindWeights'),
                      _knobBool(context, 'Boundary', 'kBoundary'),
                      _knobBool(context, 'Backfit', 'kBackfit'),
                      _knobBool(context, 'Compact', 'kCompact', fallback: true),
                      _knobSlider(
                        context,
                        'StandoutTol',
                        'kStandout',
                        min: 0,
                        max: 0.02,
                        fallback: 0,
                        decimals: 3,
                      ),
                      _knobInt(context, 'Grid', 'kGrid', min: 16, max: 160),
                      _Section(context.s('expertGroupSearch')),
                      _KnobShell(
                        label: context.s('kQuality'),
                        help: context.s('kQualityHelp'),
                        overridden: studio.isOverridden('Quality'),
                        onReset: () => _reset('Quality'),
                        child: _Select(
                          value:
                              studio.knobValue('Quality') as String? ??
                              'quality',
                          options: const [
                            'fast',
                            'balanced',
                            'max',
                            'quality',
                            'ultra',
                          ],
                          onChanged: (v) {
                            studio.setChoice('Quality', v);
                            studio.refreshKnobDefaults();
                          },
                        ),
                      ),
                      _knobInt(
                        context,
                        'Seed',
                        'kSeed',
                        min: 0,
                        max: 1 << 30,
                      ),
                      // The counts are deliberately near-unclamped (owner's
                      // call): whoever opens this panel is trading their own
                      // time and VRAM, and a ceiling that second-guesses them
                      // is a control that silently does nothing.
                      _knobInt(
                        context,
                        'Random',
                        'kRandom',
                        min: 1,
                        max: 5000000,
                      ),
                      _knobInt(
                        context,
                        'Mutated',
                        'kMutated',
                        min: 0,
                        max: 2000000,
                      ),
                      _knobInt(
                        context,
                        'SampleBudget',
                        'kSample',
                        min: 100,
                        max: 2000000,
                      ),
                      _knobInt(
                        context,
                        'MaxNoImprove',
                        'kPatience',
                        min: 1,
                        max: 1000000,
                      ),
                      _knobSlider(
                        context,
                        'Overdraw',
                        'kOverdraw',
                        min: 1,
                        max: 3,
                        fallback: 1,
                        decimals: 1,
                      ),
                      _knobSlider(
                        context,
                        'RampWeight',
                        'kRampWeight',
                        min: 0,
                        max: 4,
                        fallback: 1.5,
                        decimals: 1,
                      ),
                    ],
                  ),
                ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  void _reset(String key) {
    studio.clearChoice(key);
    if (key == 'Quality') studio.refreshKnobDefaults();
  }

  Widget _knobBool(
    BuildContext context,
    String key,
    String strKey, {
    bool fallback = false,
  }) => _KnobShell(
    label: context.s(strKey),
    help: context.s('${strKey}Help'),
    overridden: studio.isOverridden(key),
    onReset: () => _reset(key),
    onTap: () => studio.setChoice(key, !_boolKnob(key, fallback: fallback)),
    child: _Toggle(value: _boolKnob(key, fallback: fallback)),
  );

  Widget _knobInt(
    BuildContext context,
    String key,
    String strKey, {
    required int min,
    required int max,
    bool enabled = true,
  }) => _KnobShell(
    label: context.s(strKey),
    help: context.s('${strKey}Help'),
    overridden: studio.isOverridden(key),
    onReset: () => _reset(key),
    enabled: enabled,
    child: _IntField(
      value: (studio.knobValue(key) as num?)?.toInt(),
      min: min,
      max: max,
      enabled: enabled,
      onChanged: (v) => studio.setChoice(key, v),
    ),
  );

  Widget _knobSlider(
    BuildContext context,
    String key,
    String strKey, {
    required double min,
    required double max,
    required double fallback,
    int decimals = 2,
    bool enabled = true,
  }) => _KnobShell(
    label: context.s(strKey),
    help: context.s('${strKey}Help'),
    overridden: studio.isOverridden(key),
    onReset: () => _reset(key),
    enabled: enabled,
    child: _SliderControl(
      value: _numKnob(key, fallback).clamp(min, max).toDouble(),
      min: min,
      max: max,
      decimals: decimals,
      enabled: enabled,
      onChanged: (v) => studio.setChoice(key, v),
    ),
  );

  Widget _knobText(BuildContext context, String key, String strKey) =>
      _KnobShell(
        label: context.s(strKey),
        help: context.s('${strKey}Help'),
        overridden: studio.isOverridden(key),
        onReset: () => _reset(key),
        child: _TextFieldControl(
          value: studio.knobValue(key) as String? ?? '',
          onChanged: (v) => studio.setChoice(key, v),
        ),
      );
}

class _Row extends StatelessWidget {
  const _Row({
    required this.label,
    required this.help,
    required this.value,
    required this.onChanged,
  });

  final String label;
  final String help;
  final bool value;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: () => onChanged(!value),
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      padding: const EdgeInsets.symmetric(vertical: 7, horizontal: 8),
      margin: const EdgeInsets.symmetric(vertical: 1),
      decoration: BoxDecoration(
        color: hoverOver(const Color(0x00000000), hover, down),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Row(
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  label,
                  style: T.text(12.5, color: hover ? T.title : T.body),
                ),
                Text(help, style: T.text(10.5, color: T.hint)),
              ],
            ),
          ),
          const SizedBox(width: 14),
          _Toggle(value: value),
        ],
      ),
    ),
  );
}

/// A small integer, set by two buttons. A slider for a range of nine would be
/// harder to land on than the number is worth.
class _Stepper extends StatelessWidget {
  const _Stepper({
    required this.label,
    required this.help,
    required this.value,
    required this.min,
    required this.max,
    required this.onChanged,
  });

  final String label;
  final String help;
  final int value;
  final int min;
  final int max;
  final ValueChanged<int> onChanged;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 8),
    child: Row(
      children: [
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(label, style: T.text(12.5, color: T.body)),
              Text(help, style: T.text(10.5, color: T.hint)),
            ],
          ),
        ),
        const SizedBox(width: 14),
        _Step('−', onTap: value > min ? () => onChanged(value - 1) : null),
        SizedBox(
          width: 30,
          child: Text(
            '$value',
            textAlign: TextAlign.center,
            style: T.monoText(13, color: T.title),
          ),
        ),
        _Step('+', onTap: value < max ? () => onChanged(value + 1) : null),
      ],
    ),
  );
}

class _Step extends StatelessWidget {
  const _Step(this.glyph, {required this.onTap});
  final String glyph;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      width: 26,
      height: 26,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        glyph,
        style: T.text(
          13,
          color: onTap == null ? T.faint : (hover ? T.title : T.dim),
        ),
      ),
    ),
  );
}

/// A group header inside the expert block.
class _Section extends StatelessWidget {
  const _Section(this.title);
  final String title;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(8, 12, 8, 3),
    child: Align(
      alignment: Alignment.centerLeft,
      child: Text(title.toUpperCase(), style: T.label),
    ),
  );
}

/// One expert knob: label + hint on the left, the control on the right, and a
/// reset affordance that appears only once the knob differs from the default —
/// an untouched knob follows the preset, and the dot says which ones no longer
/// do.
class _KnobShell extends StatelessWidget {
  const _KnobShell({
    required this.label,
    required this.help,
    required this.overridden,
    required this.onReset,
    required this.child,
    this.onTap,
    this.enabled = true,
  });

  final String label;
  final String help;
  final bool overridden;
  final VoidCallback onReset;
  final Widget child;
  final VoidCallback? onTap;
  final bool enabled;

  @override
  Widget build(BuildContext context) {
    final row = Row(
      children: [
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Flexible(
                    child: Text(label, style: T.text(12.5, color: T.body)),
                  ),
                  if (overridden) ...[
                    const SizedBox(width: 6),
                    Container(
                      width: 5,
                      height: 5,
                      decoration: const BoxDecoration(
                        color: T.teal,
                        shape: BoxShape.circle,
                      ),
                    ),
                  ],
                ],
              ),
              Text(help, style: T.text(10.5, color: T.hint)),
            ],
          ),
        ),
        if (overridden)
          Pressable(
            onTap: onReset,
            builder: (context, hover, down) => Padding(
              padding: const EdgeInsets.symmetric(horizontal: 5),
              child: Text(
                '↺',
                style: T.text(13, color: hover ? T.title : T.dim),
              ),
            ),
          ),
        const SizedBox(width: 8),
        child,
      ],
    );
    final body = Padding(
      padding: const EdgeInsets.symmetric(vertical: 6, horizontal: 8),
      child: row,
    );
    final dimmed = enabled
        ? body
        : Opacity(opacity: 0.4, child: IgnorePointer(child: body));
    if (onTap == null || !enabled) return dimmed;
    return Pressable(
      onTap: onTap,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        decoration: BoxDecoration(
          color: hoverOver(const Color(0x00000000), hover, down),
          borderRadius: BorderRadius.circular(8),
        ),
        child: body,
      ),
    );
  }
}

/// A bounded integer, typed rather than stepped: the counts here span four
/// orders of magnitude and no stepper survives that.
class _IntField extends StatefulWidget {
  const _IntField({
    required this.value,
    required this.min,
    required this.max,
    required this.onChanged,
    this.enabled = true,
  });

  final int? value;
  final int min;
  final int max;
  final ValueChanged<int> onChanged;
  final bool enabled;

  @override
  State<_IntField> createState() => _IntFieldState();
}

class _IntFieldState extends State<_IntField> {
  late final _ctl = TextEditingController(text: widget.value?.toString() ?? '');
  final _focus = FocusNode();
  late final VoidCallback _focusListener;

  /// The text as the MODEL last put it there. _commit compares against this so a focus pass with
  /// no edit commits nothing: the field commits on blur, and several knobs legitimately display a
  /// value outside [min,max] — a sentinel 0 meaning "the engine decides". Clicking into such a
  /// field and clicking away used to clamp the sentinel and store the clamp as a real override,
  /// silently and permanently: `0.clamp(1, 5000000)` writes Random = 1, and every later run in
  /// every mode then searches ONE candidate per shape. Nothing on screen says so and it survives a
  /// restart.
  late String _asShown = widget.value?.toString() ?? '';

  @override
  void initState() {
    super.initState();
    _focusListener = () {
      if (!_focus.hasFocus) _commit();
    };
    _focus.addListener(_focusListener);
  }

  @override
  void didUpdateWidget(_IntField old) {
    super.didUpdateWidget(old);
    // Follow the model while the user is not typing — a reset or a mode change
    // must reach the field, a keystroke must not be fought over.
    if (!_focus.hasFocus) {
      final text = widget.value?.toString() ?? '';
      if (_ctl.text != text) _ctl.text = text;
      _asShown = text;
    }
  }

  @override
  void dispose() {
    // Remove the listener before disposing, then commit while the controller is
    // still alive: a bare listener could not be removed, so disposing the focus
    // node fired _commit against an ALREADY-disposed controller (a crash on any
    // close that happened while the field held focus). Committing here also
    // preserves a typed-but-uncommitted value when the sheet closes on Esc.
    _focus.removeListener(_focusListener);
    if (_focus.hasFocus) _commit();
    _focus.dispose();
    _ctl.dispose();
    super.dispose();
  }

  void _commit() {
    if (_ctl.text.trim() == _asShown.trim()) return; // see _asShown: no edit, no override
    final v = int.tryParse(_ctl.text.trim());
    if (v == null) {
      _ctl.text = widget.value?.toString() ?? '';
      _asShown = _ctl.text;
      return;
    }
    final clamped = v.clamp(widget.min, widget.max);
    _ctl.text = clamped.toString();
    _asShown = _ctl.text;
    if (clamped != widget.value) widget.onChanged(clamped);
  }

  @override
  Widget build(BuildContext context) => SizedBox(
    width: 86,
    height: 27,
    child: TextField(
      controller: _ctl,
      focusNode: _focus,
      enabled: widget.enabled,
      onSubmitted: (_) => _commit(),
      keyboardType: TextInputType.number,
      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
      textAlign: TextAlign.right,
      style: T.monoText(12, color: T.title),
      cursorColor: T.teal,
      decoration: InputDecoration(
        isDense: true,
        filled: true,
        fillColor: T.fillSoft,
        contentPadding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(7),
          borderSide: BorderSide.none,
        ),
      ),
    ),
  );
}

/// A bounded float with its value beside it.
class _SliderControl extends StatelessWidget {
  const _SliderControl({
    required this.value,
    required this.min,
    required this.max,
    required this.decimals,
    required this.onChanged,
    this.enabled = true,
  });

  final double value;
  final double min;
  final double max;
  final int decimals;
  final ValueChanged<double> onChanged;
  final bool enabled;

  @override
  Widget build(BuildContext context) => SizedBox(
    width: 214,
    child: Row(
      children: [
        Expanded(
          child: SliderTheme(
            data: SliderThemeData(
              trackHeight: 3,
              activeTrackColor: T.teal,
              inactiveTrackColor: T.fill,
              thumbColor: const Color(0xFFFFFFFF),
              overlayColor: const Color(0x2254CBB8),
              thumbShape: const RoundSliderThumbShape(enabledThumbRadius: 7),
              overlayShape: const RoundSliderOverlayShape(overlayRadius: 13),
            ),
            child: Slider(
              value: value,
              min: min,
              max: max,
              onChanged: enabled ? onChanged : null,
            ),
          ),
        ),
        SizedBox(
          width: 44,
          child: Text(
            value.toStringAsFixed(decimals),
            textAlign: TextAlign.right,
            style: T.monoText(11.5, color: T.title),
          ),
        ),
      ],
    ),
  );
}

/// Free text for the CSV knobs (shape kinds and their mix).
class _TextFieldControl extends StatefulWidget {
  const _TextFieldControl({required this.value, required this.onChanged});

  final String value;
  final ValueChanged<String> onChanged;

  @override
  State<_TextFieldControl> createState() => _TextFieldControlState();
}

class _TextFieldControlState extends State<_TextFieldControl> {
  late final _ctl = TextEditingController(text: widget.value);
  final _focus = FocusNode();
  late final VoidCallback _focusListener;

  @override
  void initState() {
    super.initState();
    _focusListener = () {
      if (!_focus.hasFocus) _commit();
    };
    _focus.addListener(_focusListener);
  }

  @override
  void didUpdateWidget(_TextFieldControl old) {
    super.didUpdateWidget(old);
    if (!_focus.hasFocus && _ctl.text != widget.value) {
      _ctl.text = widget.value;
    }
  }

  @override
  void dispose() {
    // See _IntField: remove first, commit while the controller lives, dispose
    // the focus node before it. Avoids the disposed-controller crash and keeps a
    // typed-but-uncommitted value when the sheet closes on Esc.
    _focus.removeListener(_focusListener);
    if (_focus.hasFocus) _commit();
    _focus.dispose();
    _ctl.dispose();
    super.dispose();
  }

  void _commit() {
    final v = _ctl.text.trim();
    if (v != widget.value) widget.onChanged(v);
  }

  @override
  Widget build(BuildContext context) => SizedBox(
    width: 214,
    height: 27,
    child: TextField(
      controller: _ctl,
      focusNode: _focus,
      onSubmitted: (_) => _commit(),
      style: T.monoText(11.5, color: T.title),
      cursorColor: T.teal,
      decoration: InputDecoration(
        isDense: true,
        filled: true,
        fillColor: T.fillSoft,
        contentPadding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(7),
          borderSide: BorderSide.none,
        ),
      ),
    ),
  );
}

/// A one-of-N picker drawn as segments — five short words need no dropdown.
class _Select extends StatelessWidget {
  const _Select({
    required this.value,
    required this.options,
    required this.onChanged,
  });

  final String value;
  final List<String> options;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) => Wrap(
    spacing: 3,
    children: [
      for (final o in options)
        Pressable(
          onTap: () => onChanged(o),
          builder: (context, hover, down) => AnimatedContainer(
            duration: Motion.fast,
            padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 4),
            decoration: BoxDecoration(
              color: o == value
                  ? const Color(0x3354CBB8)
                  : hoverOver(T.fillSoft, hover, down),
              borderRadius: BorderRadius.circular(6),
            ),
            child: Text(
              o,
              style: T.text(
                10.5,
                color: o == value ? T.teal : (hover ? T.title : T.dim),
              ),
            ),
          ),
        ),
    ],
  );
}

class _Toggle extends StatelessWidget {
  const _Toggle({required this.value});
  final bool value;

  @override
  Widget build(BuildContext context) => AnimatedContainer(
    duration: const Duration(milliseconds: 130),
    width: 34,
    height: 20,
    padding: const EdgeInsets.all(2),
    alignment: value ? Alignment.centerRight : Alignment.centerLeft,
    decoration: BoxDecoration(
      color: value ? T.teal : T.fill,
      borderRadius: BorderRadius.circular(10),
    ),
    child: Container(
      width: 16,
      height: 16,
      decoration: const BoxDecoration(
        color: Color(0xFFFFFFFF),
        shape: BoxShape.circle,
        boxShadow: [
          BoxShadow(
            color: Color(0x66000000),
            blurRadius: 3,
            offset: Offset(0, 1),
          ),
        ],
      ),
    ),
  );
}

/// The app's own settings, as opposed to the engine's.
///
/// The distinction matters: everything in [AdvancedSheet] changes what the fit
/// produces, and everything here changes how the app behaves around it. They
/// were one button for a while and it was the wrong button — someone looking
/// for a sound toggle should not be reading about contour terms.
class SettingsSheet extends StatelessWidget {
  const SettingsSheet({super.key, required this.studio, required this.onClose});

  final Studio studio;
  final VoidCallback onClose;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Glass(
        radius: 16,
        live: false,
        child: SizedBox(
          width: 520,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 14, 15, 12),
                child: Row(
                  children: [
                    Text(
                      context.s('settings'),
                      style: T.text(
                        15,
                        color: T.title,
                        weight: FontWeight.w600,
                      ),
                    ),
                    const Spacer(),
                    Btn(
                      context.s('done'),
                      kind: BtnKind.primary,
                      onTap: onClose,
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 6, 15, 8),
                child: Column(
                  children: [
                    _Row(
                      label: context.s('soundOnFinish'),
                      help: context.s('soundOnFinishHelp'),
                      value: studio.soundOnFinish,
                      onChanged: studio.setSoundOnFinish,
                    ),
                    _Row(
                      label: context.s('flashOnFinish'),
                      help: context.s('flashOnFinishHelp'),
                      value: studio.flashOnFinish,
                      onChanged: studio.setFlashOnFinish,
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
              // A fit can take minutes, so the answer to "did it work" is
              // usually somewhere else on the machine by the time it lands.
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 11, 15, 14),
                child: Row(
                  children: [
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(context.s('diagnostics'), style: T.label),
                          const SizedBox(height: 3),
                          Text(
                            AppLog.path ?? context.s('logHint'),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: T.monoText(10, color: T.hint),
                          ),
                        ],
                      ),
                    ),
                    const SizedBox(width: 10),
                    Btn(context.s('copyPath'), onTap: _copyPath),
                    const SizedBox(width: 7),
                    Btn(context.s('openLogFolder'), onTap: AppLog.reveal),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
