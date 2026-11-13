/// Selectable monospace transcript that follows the tail.
library;

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart' hide View;

import '../../api/agentd_api.dart';

const transcriptFontSize = 13.0;

class Transcript extends StatefulWidget {
  const Transcript({required this.view, super.key});
  final View view;

  @override
  State<Transcript> createState() => _TranscriptState();
}

class _TranscriptState extends State<Transcript> {
  final _ctl = ScrollController();
  bool _atBottom = true;
  bool _unseen = false;

  @override
  void initState() {
    super.initState();
    _ctl.addListener(_onScroll);
    WidgetsBinding.instance.addPostFrameCallback((_) => _jump());
  }

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  void _onScroll() {
    if (!_ctl.hasClients) return;
    final at = _ctl.position.maxScrollExtent - _ctl.position.pixels < 48;
    if (at != _atBottom || (at && _unseen)) {
      setState(() {
        _atBottom = at;
        if (at) _unseen = false;
      });
    }
  }

  void _jump() {
    if (_ctl.hasClients) _ctl.jumpTo(_ctl.position.maxScrollExtent);
  }

  @override
  void didUpdateWidget(Transcript old) {
    super.didUpdateWidget(old);
    if (old.view.endLine == widget.view.endLine &&
        old.view.lines.length == widget.view.lines.length &&
        old.view.revision == widget.view.revision) {
      return;
    }
    if (_atBottom) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _jump());
    } else if (old.view.revision != widget.view.revision) {
      _unseen = true;
    }
  }

  @override
  Widget build(BuildContext context) {
    final lines = widget.view.lines;
    final scheme = Theme.of(context).colorScheme;
    final base = TextStyle(
      fontFamily: 'monospace',
      fontFamilyFallback: const ['Menlo', 'Consolas', 'Courier New'],
      fontSize: transcriptFontSize,
      height: 1.3,
      color: scheme.onSurface,
    );
    return Stack(
      children: [
        SelectionArea(
          child: ListView.builder(
            key: const Key('transcript-list'),
            controller: _ctl,
            padding: const EdgeInsets.all(8),
            itemCount: lines.length,
            itemBuilder: (c, i) => _LineText(lines[i], base, scheme),
          ),
        ),
        if (!_atBottom && _unseen)
          Positioned(
            right: 12,
            bottom: 12,
            child: ActionChip(
              key: const Key('new-output-chip'),
              avatar: const Icon(Icons.arrow_downward, size: 16),
              label: const Text('New output'),
              onPressed: () {
                _ctl.animateTo(
                  _ctl.position.maxScrollExtent,
                  duration: const Duration(milliseconds: 200),
                  curve: Curves.easeOut,
                );
              },
            ),
          ),
      ],
    );
  }
}

class _LineText extends StatelessWidget {
  const _LineText(this.line, this.base, this.scheme);
  final Line line;
  final TextStyle base;
  final ColorScheme scheme;

  @override
  Widget build(BuildContext context) {
    // Blank lines keep their height; text wraps at any width.
    final text = line.text.isEmpty ? ' ' : line.text;
    switch (line.role) {
      case LineRole.status:
        return Text(
          text,
          style: base.copyWith(
            color: scheme.onSurfaceVariant,
            fontStyle: FontStyle.italic,
          ),
        );
      case LineRole.dialog:
        return Container(
          color: scheme.secondaryContainer.withValues(alpha: 0.5),
          child: Text(
            text,
            style: base.copyWith(
              color: scheme.onSecondaryContainer,
              fontWeight: FontWeight.w600,
            ),
          ),
        );
      case LineRole.gap:
        return SizedBox(height: base.fontSize! * 0.6, child: const Text(''));
      case LineRole.transcript:
      case LineRole.unknown:
        return Text(text, style: base);
    }
  }
}

/// Copy helper for tests and menus.
Future<void> copyText(String s) => Clipboard.setData(ClipboardData(text: s));
