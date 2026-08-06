/// Every saved run, as a wall of thumbnails.
///
/// The rail beside the canvas holds the last few; this is for finding one from
/// last week. Opening a run puts its geometry and preview back on the canvas, so
/// injecting an old design is the same act as injecting the one that just
/// finished — which is the point of keeping them at all.
library;

import 'dart:ui' show ImageFilter;

import 'package:flutter/material.dart';

import '../state/studio.dart';
import 'confirm.dart';
import 'popovers.dart';
import 'strings.dart';
import 'tokens.dart';

class Gallery extends StatefulWidget {
  const Gallery({
    super.key,
    required this.studio,
    required this.onClose,
    required this.onConfirm,
  });

  final Studio studio;
  final VoidCallback onClose;

  /// Raises a question with the shell rather than answering it here: a dialog
  /// inside the gallery would be a dialog inside an overlay, and closing the
  /// gallery would take the question with it.
  final void Function(Confirm) onConfirm;

  @override
  State<Gallery> createState() => _GalleryState();
}

/// What the wall is ordered by.
enum _Sort { date, name }

class _GalleryState extends State<Gallery> {
  final _search = TextEditingController();
  String _preset = '';
  _Sort _sort = _Sort.date;

  /// Newest and A-Z are the two orders anyone actually starts from, so each
  /// key gets the direction that is useful first.
  bool _desc = true;

  /// The runs picked for a bulk action, by id. Empty means not selecting: a
  /// separate mode flag would let the header say "selecting" with nothing
  /// selected, which is a state with no purpose.
  final _picked = <String>{};

  /// Whether clicks pick instead of open.
  ///
  /// Derived from the selection at first, which meant the FIRST run could only
  /// be picked through a small tick on the card — so clicking one opened it
  /// instead, which is exactly what the owner ran into. A mode you turn on says
  /// what the next click will do before you make it.
  bool _selecting = false;

  Studio get studio => widget.studio;
  VoidCallback get onClose => widget.onClose;

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  /// Filtering by preset and by name, because a library of a hundred runs is
  /// mostly other pictures: the one being looked for is known by its name or by
  /// the style it was made with.
  List<Map<String, dynamic>> get _visible {
    final q = _search.text.trim().toLowerCase();
    return studio.entries.where((e) {
      if (_preset.isNotEmpty && (e['preset'] as String? ?? '') != _preset) {
        return false;
      }
      if (q.isEmpty) return true;
      return (e['name'] as String? ?? '').toLowerCase().contains(q);
    }).toList();
  }

  void _askOpen(Map<String, dynamic> entry) {
    final id = entry['id'] as String;
    widget.onConfirm(
      Confirm(
        title: context.s('loadRunTitle'),
        body: entry['name'] as String? ?? context.s('loadRunBody'),
        action: context.s('load'),
        thumb: studio.thumbProvider(id),
        onConfirm: () {
          studio.openRun(entry);
          onClose();
        },
      ),
    );
  }

  /// Deletion is final and the owner has already lost a run to a stray click,
  /// so it asks — and the confirming button is the destructive-looking one, at
  /// the far side of the dialog from where the pointer came in.
  void _askDelete(Map<String, dynamic> entry) {
    final id = entry['id'] as String;
    widget.onConfirm(
      Confirm(
        title: context.s('deleteRunTitle'),
        body: '${entry['name'] ?? ''}\n${context.s('deleteRunBody')}',
        action: context.s('delete'),
        destructive: true,
        thumb: studio.thumbProvider(id),
        onConfirm: () async {
          await studio.deleteRun(id);
          if (mounted) setState(() {});
        },
      ),
    );
  }

  DateTime _dateOf(Map<String, dynamic> e) =>
      DateTime.tryParse(e['created'] as String? ?? '')?.toLocal() ??
      DateTime.fromMillisecondsSinceEpoch(0);

  List<Map<String, dynamic>> get _ordered {
    final list = _visible;
    list.sort((a, b) {
      final c = _sort == _Sort.name
          ? (a['name'] as String? ?? '').toLowerCase().compareTo(
              (b['name'] as String? ?? '').toLowerCase(),
            )
          : _dateOf(a).compareTo(_dateOf(b));
      return _desc ? -c : c;
    });
    return list;
  }

  /// Runs under day headings, the way a phone gallery reads. Only when sorted
  /// by date: under a name sort the headings would cut the alphabet into
  /// arbitrary pieces.
  List<(String, List<Map<String, dynamic>>)> _grouped(
    BuildContext context,
    List<Map<String, dynamic>> list,
  ) {
    if (_sort != _Sort.date) return [('', list)];
    final now = DateTime.now();
    final today = DateTime(now.year, now.month, now.day);
    final out = <(String, List<Map<String, dynamic>>)>[];
    for (final e in list) {
      final d = _dateOf(e);
      final day = DateTime(d.year, d.month, d.day);
      final gap = today.difference(day).inDays;
      final label = switch (gap) {
        0 => context.s('today'),
        1 => context.s('yesterday'),
        _ when d.millisecondsSinceEpoch == 0 => context.s('earlier'),
        _ =>
          '${day.day.toString().padLeft(2, '0')}.'
              '${day.month.toString().padLeft(2, '0')}.${day.year}',
      };
      if (out.isEmpty || out.last.$1 != label) {
        out.add((label, <Map<String, dynamic>>[]));
      }
      out.last.$2.add(e);
    }
    return out;
  }

  void _togglePick(String id) => setState(() {
    if (!_picked.remove(id)) _picked.add(id);
  });

  void _deletePicked(List<Map<String, dynamic>> visible) {
    final ids = List<String>.of(_picked);
    if (ids.isEmpty) return;
    widget.onConfirm(
      Confirm(
        title: context
            .s('deleteManyTitle')
            .replaceFirst('{n}', '${ids.length}'),
        body: context.s('deleteManyBody'),
        action: context.s('delete'),
        destructive: true,
        onConfirm: () async {
          // One refresh at the end, not one per run: a hundred deletions would
          // otherwise re-list the whole library a hundred times.
          for (final id in ids) {
            await studio.deleteRun(id, refresh: false);
          }
          await studio.refreshLibrary();
          if (mounted) {
            setState(() {
              _picked.clear();
              _selecting = false;
            });
          }
        },
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final visible = _ordered;
    final groups = _grouped(context, visible);
    final allPicked =
        visible.isNotEmpty &&
        visible.every((e) => _picked.contains(e['id'] as String));
    // A filter for every preset the library ACTUALLY holds, named through
    // styleOptions when the value is one of ours.
    //
    // Offering only known modes was wrong in the other direction: a library
    // built by the previous studio stores whatever that build called the
    // preset — including localised names like "гібрид" — so filtering by the
    // engine's vocabulary alone left a library of a hundred runs with no
    // filters at all. What is on disk decides; the label is cosmetic.
    final present = <String>{
      for (final e in studio.entries) (e['preset'] as String? ?? ''),
    }..remove('');
    final presets = present.toList()..sort();
    String labelFor(String p) {
      for (final o in styleOptions) {
        if (o.mode == p) return o.label;
      }
      return p;
    }

    // A near-opaque surface over a blur, not a tint. At 72% the shell read
    // straight through it: the command bar's labels landed on top of the
    // gallery's own, and its controls were invisible against the canvas.
    return BackdropFilter(
      filter: ImageFilter.blur(sigmaX: 18, sigmaY: 18),
      child: Container(
        decoration: const BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.topCenter,
            end: Alignment.bottomCenter,
            colors: [Color(0xFC0C0E10), Color(0xFC08090A)],
          ),
        ),
        child: Column(
          children: [
            SizedBox(
              height: 56,
              child: Row(
                children: [
                  const SizedBox(width: 18),
                  Text(
                    context.s('runs'),
                    style: T.text(17, color: T.title, weight: FontWeight.w600),
                  ),
                  const SizedBox(width: 10),
                  Text(
                    '${visible.length}',
                    style: T.monoText(11.5, color: T.hint),
                  ),
                  const SizedBox(width: 12),
                  // The filters take whatever room is left and scroll sideways
                  // when there is not enough. A library with a dozen presets
                  // used to push the sort and search controls off the window.
                  Expanded(
                    child: SingleChildScrollView(
                      scrollDirection: Axis.horizontal,
                      child: Row(
                        children: [
                          _Filter(
                            label: context.s('all'),
                            on: _preset.isEmpty,
                            onTap: () => setState(() => _preset = ''),
                          ),
                          for (final p in presets)
                            Padding(
                              padding: const EdgeInsets.only(left: 4),
                              child: _Filter(
                                label: labelFor(p),
                                on: _preset == p,
                                onTap: () => setState(() => _preset = p),
                              ),
                            ),
                        ],
                      ),
                    ),
                  ),
                  const SizedBox(width: 10),
                  // Order: which key, then which way. Two controls rather than
                  // four combined chips, because the direction applies to both.
                  _Filter(
                    label: context.s('sortDate'),
                    on: _sort == _Sort.date,
                    onTap: () => setState(() => _sort = _Sort.date),
                  ),
                  const SizedBox(width: 4),
                  _Filter(
                    label: context.s('sortName'),
                    on: _sort == _Sort.name,
                    onTap: () => setState(() => _sort = _Sort.name),
                  ),
                  const SizedBox(width: 4),
                  _Filter(
                    label: _desc ? '↓' : '↑',
                    on: false,
                    onTap: () => setState(() => _desc = !_desc),
                  ),
                  const SizedBox(width: 10),
                  _Filter(
                    label: context.s('select'),
                    on: _selecting,
                    onTap: () => setState(() {
                      _selecting = !_selecting;
                      if (!_selecting) _picked.clear();
                    }),
                  ),
                  if (_selecting) ...[
                    const SizedBox(width: 4),
                    _Filter(
                      label: allPicked
                          ? context.s('selectNone')
                          : context.s('selectAll'),
                      on: _picked.isNotEmpty,
                      onTap: () => setState(() {
                        _picked.clear();
                        if (!allPicked) {
                          _picked.addAll(visible.map((e) => e['id'] as String));
                        }
                      }),
                    ),
                  ],
                  if (_picked.isNotEmpty) ...[
                    const SizedBox(width: 6),
                    Btn(
                      '${context.s('delete')}  ${_picked.length}',
                      kind: BtnKind.danger,
                      onTap: () => _deletePicked(visible),
                    ),
                  ],
                  const SizedBox(width: 10),
                  SizedBox(
                    width: 150,
                    height: 29,
                    child: TextField(
                      controller: _search,
                      onChanged: (_) => setState(() {}),
                      style: T.text(12.5, color: T.body),
                      cursorColor: T.teal,
                      decoration: InputDecoration(
                        isDense: true,
                        filled: true,
                        fillColor: T.fillSoft,
                        hintText: context.s('search'),
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
                  const SizedBox(width: 8),
                  Btn(context.s('close'), onTap: onClose),
                  const SizedBox(width: 18),
                ],
              ),
            ),
            Expanded(
              child: visible.isEmpty
                  ? Center(
                      child: Text(
                        context.s(
                          studio.entries.isEmpty ? 'nothingSaved' : 'noMatch',
                        ),
                        style: T.text(12.5, color: T.soft),
                      ),
                    )
                  : ListView.builder(
                      padding: const EdgeInsets.fromLTRB(18, 0, 18, 18),
                      itemCount: groups.length,
                      itemBuilder: (context, gi) {
                        final (label, items) = groups[gi];
                        return Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            if (label.isNotEmpty)
                              Padding(
                                padding: const EdgeInsets.fromLTRB(2, 10, 0, 8),
                                child: Text(
                                  label.toUpperCase(),
                                  style: T.label,
                                ),
                              ),
                            GridView.builder(
                              shrinkWrap: true,
                              physics: const NeverScrollableScrollPhysics(),
                              gridDelegate:
                                  const SliverGridDelegateWithMaxCrossAxisExtent(
                                    maxCrossAxisExtent: 190,
                                    crossAxisSpacing: 12,
                                    mainAxisSpacing: 12,
                                    childAspectRatio: 0.78,
                                  ),
                              itemCount: items.length,
                              itemBuilder: (context, i) {
                                final id = items[i]['id'] as String;
                                return _Card(
                                  studio: studio,
                                  entry: items[i],
                                  picked: _picked.contains(id),
                                  picking: _selecting,
                                  onPick: () => _togglePick(id),
                                  onOpen: () => _askOpen(items[i]),
                                  onDelete: () => _askDelete(items[i]),
                                );
                              },
                            ),
                          ],
                        );
                      },
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Filter extends StatelessWidget {
  const _Filter({required this.label, required this.on, required this.onTap});
  final String label;
  final bool on;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: 24,
      padding: const EdgeInsets.symmetric(horizontal: 10),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: on ? T.tealWash : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        label,
        style: T.text(
          11.5,
          color: on ? T.tealBright : (hover ? T.body : T.soft),
        ),
      ),
    ),
  );
}

class _Card extends StatefulWidget {
  const _Card({
    required this.studio,
    required this.entry,
    required this.picked,
    required this.picking,
    required this.onPick,
    required this.onOpen,
    required this.onDelete,
  });

  final Studio studio;
  final Map<String, dynamic> entry;
  final bool picked;

  /// True once anything is selected. From then on a plain click picks rather
  /// than opens — the same bargain a phone gallery makes, and the reason it
  /// never needs a long press on a mouse.
  final bool picking;
  final VoidCallback onPick;
  final VoidCallback onOpen;
  final VoidCallback onDelete;

  @override
  State<_Card> createState() => _CardState();
}

class _CardState extends State<_Card> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) {
    final e = widget.entry;
    final name = e['name'] as String? ?? '';
    final shapes = (e['shapes'] as num?)?.toInt() ?? 0;
    final w = (e['width'] as num?)?.toInt() ?? 0;
    final h = (e['height'] as num?)?.toInt() ?? 0;

    return MouseRegion(
      cursor: SystemMouseCursors.click,
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: GestureDetector(
        onTap: widget.picking ? widget.onPick : widget.onOpen,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: AnimatedContainer(
                duration: Motion.fast,
                clipBehavior: Clip.antiAlias,
                transform: Matrix4.translationValues(0, _hover ? -2 : 0, 0),
                decoration: BoxDecoration(
                  color: const Color(0xFF1A1C1F),
                  borderRadius: BorderRadius.circular(10),
                  border: Border.all(
                    color: widget.picked
                        ? T.teal
                        : (_hover ? T.teal : T.border),
                    width: widget.picked ? 2 : 1,
                  ),
                  boxShadow: _hover
                      ? const [
                          BoxShadow(
                            color: Color(0x66000000),
                            blurRadius: 18,
                            offset: Offset(0, 6),
                          ),
                        ]
                      : null,
                ),
                child: Stack(
                  fit: StackFit.expand,
                  children: [
                    FutureBuilder(
                      future: widget.studio.thumb(e['id'] as String),
                      // Empty bytes are a thumbnail that could not be
                      // fetched, not an image: decoding them throws.
                      builder: (context, snap) =>
                          snap.hasData && snap.data!.isNotEmpty
                          ? Image.memory(snap.data!, fit: BoxFit.cover)
                          : const SizedBox.shrink(),
                    ),
                    // The tick sits top-left and appears as soon as the
                    // pointer is over the card, so selecting a second run is
                    // one click and never a hunt for a hidden control.
                    if (_hover || widget.picking)
                      Positioned(
                        left: 6,
                        top: 6,
                        child: Pressable(
                          onTap: widget.onPick,
                          builder: (context, h, d) => AnimatedContainer(
                            duration: Motion.fast,
                            width: 22,
                            height: 22,
                            alignment: Alignment.center,
                            decoration: BoxDecoration(
                              color: widget.picked
                                  ? T.teal
                                  : const Color(0xCC101214),
                              borderRadius: BorderRadius.circular(6),
                              border: Border.all(
                                color: widget.picked ? T.teal : T.border,
                              ),
                            ),
                            child: Text(
                              widget.picked ? '✓' : '',
                              style: T.text(11, color: T.ink),
                            ),
                          ),
                        ),
                      ),
                    AnimatedOpacity(
                      duration: Motion.fast,
                      opacity: _hover && !widget.picking ? 1 : 0,
                      child: IgnorePointer(
                        ignoring: !_hover || widget.picking,
                        child: Container(
                          color: const Color(0x99000000),
                          alignment: Alignment.center,
                          child: Column(
                            mainAxisSize: MainAxisSize.min,
                            children: [
                              Btn(
                                context.s('open'),
                                kind: BtnKind.primary,
                                onTap: widget.onOpen,
                              ),
                              const SizedBox(height: 6),
                              Btn(
                                context.s('delete'),
                                kind: BtnKind.danger,
                                onTap: widget.onDelete,
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
            const SizedBox(height: 7),
            Text(
              name,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: T.text(12, color: T.body, weight: FontWeight.w500),
            ),
            Text(
              '$shapes shapes · ${w}x$h',
              style: T.monoText(10, color: T.hint),
            ),
          ],
        ),
      ),
    );
  }
}
