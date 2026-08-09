/// The two choices that decide a run: what kind of picture this is, and how many
/// shapes to spend on it.
///
/// They are popovers over the command bar rather than a settings page because
/// they are the only two things a user changes often, and the design puts them
/// one click from the canvas for that reason.
library;

import 'package:flutter/material.dart';

import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

/// A style, as the user sees it, and the engine mode behind it. The pairing
/// lives here and nowhere else: the mode strings are the engine's vocabulary and
/// have to match `preset.PresetMode` exactly, while the labels are ours.
class StyleOption {
  const StyleOption(
    this.label,
    this.hint,
    this.mode,
    this.swatch, {
    this.ink = false,
  });
  final String label;
  final String hint;
  final String mode;
  final Color swatch;

  /// Whether the style lays designed ink lines over the fill.
  final bool ink;
}

const styleOptions = <StyleOption>[
  StyleOption(
    'Drawing / Anime',
    'cel art, flat drawings',
    'anime',
    Color(0xFFC06A82),
  ),
  StyleOption(
    'Anime + Ink',
    'colour plus a black outline',
    'anime-ink',
    Color(0xFF4E74B0),
    ink: true,
  ),
  StyleOption(
    'Line art',
    'manga, pencil, sketch',
    'lineart',
    Color(0xFFE7E9EC),
    ink: true,
  ),
  StyleOption(
    'Logo / Flat',
    'logos, posters, decals',
    'flat',
    Color(0xFFE0A84B),
  ),
  StyleOption('Photo', 'photographs, faces', 'photo', Color(0xFF7C5BC0)),
  StyleOption(
    'Soft glow',
    'gradients, airbrush',
    'gaussian',
    Color(0xFF54CBB8),
  ),
  StyleOption('Pixel art', 'exact sprite copy', 'pixel', Color(0xFFD9694F)),
];

/// The three counts worth offering, each named by what it actually covers on a
/// car. A number alone means nothing to someone who has not injected one yet,
/// and a longer list of stops is a longer list of decisions: anything between
/// them is one keystroke away in the field beside the slider.
const detailPopoverWidth = 288.0;

const detailStops = <(int, String)>[
  (500, 'stopSticker'),
  (1000, 'stopBumper'),
  (3000, 'stopMax'),
];

/// The built-in styles, and under a divider the ones the user saved.
///
/// A saved preset is not another style — it is a whole configuration, advanced
/// settings included — so it sits below the line rather than among them.
class StylePopover extends StatefulWidget {
  const StylePopover({super.key, required this.studio, required this.onPick});

  final Studio studio;
  final void Function(String mode) onPick;

  @override
  State<StylePopover> createState() => _StylePopoverState();
}

class _StylePopoverState extends State<StylePopover> {
  final _name = TextEditingController();
  bool _naming = false;

  Studio get studio => widget.studio;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  void _save() {
    final n = _name.text.trim();
    if (n.isEmpty) return;
    studio.savePreset(n);
    _name.clear();
    setState(() => _naming = false);
  }

  @override
  Widget build(BuildContext context) {
    final current = studio.mode;
    final saved = studio.userPresets;
    return Glass(
      child: SizedBox(
        width: 268,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(14, 11, 14, 6),
              child: Text(context.s('style').toUpperCase(), style: T.label),
            ),
            for (final s in styleOptions)
              _StyleRow(
                option: s,
                selected: s.mode == current,
                onTap: () => widget.onPick(s.mode),
              ),
            const SizedBox(height: 6),
            Container(height: 1, color: T.hairline),
            if (saved.isNotEmpty)
              Padding(
                padding: const EdgeInsets.fromLTRB(14, 9, 14, 4),
                child: Text(context.s('myPresets'), style: T.label),
              ),
            for (final p in saved)
              _PresetRow(
                name: p['name'] as String? ?? '',
                onTap: () => studio.applyPreset(p),
                onDelete: () => studio.deletePreset(p['id'] as String),
              ),
            Padding(
              padding: const EdgeInsets.fromLTRB(8, 8, 8, 9),
              child: _naming
                  ? Row(
                      children: [
                        Expanded(
                          child: SizedBox(
                            height: 29,
                            child: TextField(
                              controller: _name,
                              autofocus: true,
                              onSubmitted: (_) => _save(),
                              style: T.text(12.5, color: T.body),
                              cursorColor: T.teal,
                              decoration: InputDecoration(
                                isDense: true,
                                filled: true,
                                fillColor: T.fillSoft,
                                hintText: context.s('presetName'),
                                hintStyle: T.text(12.5, color: T.faint),
                                contentPadding: const EdgeInsets.symmetric(
                                  horizontal: 10,
                                  vertical: 7,
                                ),
                                border: OutlineInputBorder(
                                  borderRadius: BorderRadius.circular(8),
                                  borderSide: BorderSide.none,
                                ),
                              ),
                            ),
                          ),
                        ),
                        const SizedBox(width: 7),
                        Btn(
                          context.s('save'),
                          kind: BtnKind.primary,
                          onTap: _save,
                        ),
                      ],
                    )
                  : SizedBox(
                      width: double.infinity,
                      child: Btn(
                        context.s('savePreset'),
                        onTap: () => setState(() => _naming = true),
                      ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

class _PresetRow extends StatelessWidget {
  const _PresetRow({
    required this.name,
    required this.onTap,
    required this.onDelete,
  });

  final String name;
  final VoidCallback onTap;
  final VoidCallback onDelete;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: 32,
      margin: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
      padding: const EdgeInsets.only(left: 10, right: 4),
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(8),
        color: hoverOver(const Color(0x00000000), hover, down),
      ),
      child: Row(
        children: [
          Expanded(
            child: Text(
              name,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: T.text(12.5, color: T.body),
            ),
          ),
          // The delete only appears under the pointer: a row of little crosses
          // beside every preset is an invitation to hit one by mistake.
          AnimatedOpacity(
            duration: Motion.fast,
            opacity: hover ? 1 : 0,
            child: Pressable(
              onTap: hover ? onDelete : null,
              builder: (context, h2, d2) => Container(
                width: 26,
                height: 26,
                alignment: Alignment.center,
                decoration: BoxDecoration(
                  borderRadius: BorderRadius.circular(7),
                  color: h2 ? const Color(0x26F0685F) : null,
                ),
                child: Text(
                  '✕',
                  style: T.text(11, color: h2 ? T.danger : T.soft),
                ),
              ),
            ),
          ),
        ],
      ),
    ),
  );
}

class _StyleRow extends StatefulWidget {
  const _StyleRow({
    required this.option,
    required this.selected,
    required this.onTap,
  });

  final StyleOption option;
  final bool selected;
  final VoidCallback onTap;

  @override
  State<_StyleRow> createState() => _StyleRowState();
}

class _StyleRowState extends State<_StyleRow> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) {
    final s = widget.option;
    return MouseRegion(
      cursor: SystemMouseCursors.click,
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: GestureDetector(
        onTap: widget.onTap,
        child: AnimatedContainer(
          duration: Motion.fast,
          height: 38,
          margin: const EdgeInsets.symmetric(horizontal: 6),
          padding: const EdgeInsets.symmetric(horizontal: 8),
          decoration: BoxDecoration(
            borderRadius: BorderRadius.circular(8),
            color: widget.selected
                ? T.tealWash
                : (_hover ? T.fillSoft : const Color(0x00000000)),
          ),
          child: Row(
            children: [
              Container(
                width: 22,
                height: 22,
                decoration: BoxDecoration(
                  gradient: LinearGradient(
                    begin: Alignment.topLeft,
                    end: Alignment.bottomRight,
                    colors: [s.swatch, s.swatch.withValues(alpha: 0.53)],
                  ),
                  borderRadius: BorderRadius.circular(6),
                  border: Border.all(color: T.border),
                ),
              ),
              const SizedBox(width: 9),
              Expanded(
                child: Column(
                  mainAxisAlignment: MainAxisAlignment.center,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      s.label,
                      style: T.text(
                        12.5,
                        color: widget.selected ? T.tealBright : T.body,
                        weight: FontWeight.w500,
                      ),
                    ),
                    Text(
                      s.hint,
                      style: T.text(10.5, color: T.hint),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ],
                ),
              ),
              if (s.ink)
                Container(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 5,
                    vertical: 2,
                  ),
                  decoration: BoxDecoration(
                    color: T.tealFaint,
                    borderRadius: BorderRadius.circular(4),
                  ),
                  child: Text(
                    'ink',
                    style: T.text(
                      9,
                      color: T.teal,
                      weight: FontWeight.w600,
                      spacing: 0.4,
                    ),
                  ),
                ),
              if (widget.selected)
                const Padding(
                  padding: EdgeInsets.only(left: 7),
                  child: Text(
                    '✓',
                    style: TextStyle(fontSize: 11, color: T.teal),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

/// How many shapes to spend, as three named counts and a number you can type.
///
/// The slider alone was not enough: the counts that matter are decided by what
/// the decal has to cover, and everything between them is a judgement the user
/// makes with a number in their head. So the field is authoritative and the
/// slider follows it.
class DetailPopover extends StatefulWidget {
  const DetailPopover({super.key, required this.studio, required this.onPick});

  final Studio studio;
  final void Function(int budget) onPick;

  /// 3000 reads as "3 000": the design spaces thousands so a four-digit budget
  /// is legible at a glance in a monospace face.
  static String grouped(int n) {
    final s = n.toString();
    if (s.length <= 3) return s;
    return '${s.substring(0, s.length - 3)} ${s.substring(s.length - 3)}';
  }

  /// The panel budget the game enforces. A run past it cannot be injected, so
  /// nothing here offers one.
  static const maxShapes = 3000;
  static const minShapes = 50;

  @override
  State<DetailPopover> createState() => _DetailPopoverState();
}

class _DetailPopoverState extends State<DetailPopover> {
  late final _field = TextEditingController(
    text: widget.studio.budget.toString(),
  );
  final _focus = FocusNode();

  @override
  void initState() {
    super.initState();
    // Committing on focus loss as well as on Enter: a user who types a number
    // and clicks Generate meant that number.
    _focus.addListener(() {
      if (!_focus.hasFocus) _commit();
      setState(() {}); // the field's border shows focus
    });
  }

  @override
  void dispose() {
    _field.dispose();
    _focus.dispose();
    super.dispose();
  }

  void _commit() {
    final n = int.tryParse(_field.text.replaceAll(RegExp(r'[^0-9]'), ''));
    if (n == null) {
      _field.text = widget.studio.budget.toString();
      return;
    }
    final clamped = n.clamp(DetailPopover.minShapes, DetailPopover.maxShapes);
    _field.text = clamped.toString();
    widget.onPick(clamped);
  }

  void _pick(int n) {
    _field.text = n.toString();
    widget.onPick(n);
  }

  @override
  Widget build(BuildContext context) {
    final budget = widget.studio.budget;
    // Only an exact match names itself. "1000 shapes, fits a bumper" is useful;
    // "1400 shapes, fits a bumper" is a claim the number does not support.
    final named = detailStops.where((s) => s.$1 == budget).firstOrNull?.$2;

    return Glass(
      child: SizedBox(
        width: detailPopoverWidth,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(13, 11, 13, 2),
              child: Text(context.s('detail').toUpperCase(), style: T.label),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(13, 0, 13, 13),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    crossAxisAlignment: CrossAxisAlignment.center,
                    children: [
                      // Dressed as an input on purpose: the same number drawn
                      // bare read as a label, and nobody clicks a label.
                      SizedBox(
                        width: 108,
                        child: AnimatedContainer(
                          duration: const Duration(milliseconds: 120),
                          padding: const EdgeInsets.symmetric(horizontal: 8),
                          decoration: BoxDecoration(
                            color: T.fillSoft,
                            borderRadius: BorderRadius.circular(8),
                            border: Border.all(
                              color: _focus.hasFocus ? T.teal : T.border,
                            ),
                          ),
                          child: TextField(
                            controller: _field,
                            focusNode: _focus,
                            onSubmitted: (_) => _commit(),
                            keyboardType: TextInputType.number,
                            style: T.monoText(
                              26,
                              color: T.title,
                              weight: FontWeight.w500,
                            ),
                            cursorColor: T.teal,
                            decoration: const InputDecoration(
                              isDense: true,
                              contentPadding: EdgeInsets.symmetric(vertical: 4),
                              border: InputBorder.none,
                            ),
                          ),
                        ),
                      ),
                      const SizedBox(width: 8),
                      Expanded(
                        child: Text(
                          named == null
                              ? context.s('shapesWord')
                              : '${context.s('shapesWord')} · '
                                    '${context.s(named)}',
                          maxLines: 2,
                          style: T.text(11.5, color: T.soft),
                        ),
                      ),
                    ],
                  ),
                  Text(context.s('customCount'), style: T.label),
                  const SizedBox(height: 10),
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
                      value: budget
                          .clamp(
                            DetailPopover.minShapes,
                            DetailPopover.maxShapes,
                          )
                          .toDouble(),
                      min: DetailPopover.minShapes.toDouble(),
                      max: DetailPopover.maxShapes.toDouble(),
                      onChanged: (v) => _pick((v / 50).round() * 50),
                    ),
                  ),
                  Row(
                    children: [
                      for (final (n, key) in detailStops) ...[
                        Expanded(
                          child: _Stop(
                            n: n,
                            label: context.s(key),
                            selected: budget == n,
                            onTap: () => _pick(n),
                          ),
                        ),
                        if (n != detailStops.last.$1) const SizedBox(width: 5),
                      ],
                    ],
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

class _Stop extends StatelessWidget {
  const _Stop({
    required this.n,
    required this.label,
    required this.selected,
    required this.onTap,
  });

  final int n;
  final String label;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      padding: const EdgeInsets.symmetric(vertical: 6),
      decoration: BoxDecoration(
        color: selected
            ? T.tealFaint
            : hoverOver(const Color(0x0DFFFFFF), hover, down),
        borderRadius: BorderRadius.circular(7),
        border: Border.all(color: selected ? T.tealFaint : T.hairline),
      ),
      child: Column(
        children: [
          Text(
            DetailPopover.grouped(n),
            style: T.monoText(
              11.5,
              color: selected ? T.tealBright : (hover ? T.body : T.soft),
            ),
          ),
          Text(
            label,
            style: T.text(9.5, color: selected ? T.tealBright : T.soft),
          ),
        ],
      ),
    ),
  );
}
