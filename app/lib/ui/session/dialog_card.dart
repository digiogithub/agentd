/// Question and option buttons of a menu / permission dialog.
library;

import 'package:material_ui/material_ui.dart' hide View;

import '../../api/agentd_api.dart';
import '../../state/session_view.dart';

class DialogCard extends StatelessWidget {
  const DialogCard({
    required this.view,
    required this.enabled,
    required this.reviewAgain,
    required this.onChoose,
    required this.onDismiss,
    super.key,
  });

  final View view;
  final bool enabled;
  final bool reviewAgain;

  /// [shown] is the view displayed when the user tapped (before any confirm).
  final void Function(Option, View shown) onChoose;
  final void Function(View shown) onDismiss;

  Future<void> _tap(BuildContext context, Option o) async {
    // What the user saw when tapping; the notifier refuses to send when the
    // view moved on while the confirm dialog was open.
    final shown = view;
    if (o.confidence < lowConfidenceThreshold) {
      final ok = await showDialog<bool>(
        context: context,
        builder: (c) => AlertDialog(
          title: const Text('Low confidence option'),
          content: Text(
            'The app is not sure it read this menu right. Select '
            '"${o.label}"?',
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(c, false),
              child: const Text('Cancel'),
            ),
            FilledButton(
              key: const Key('confirm-low-confidence'),
              onPressed: () => Navigator.pop(c, true),
              child: const Text('Select'),
            ),
          ],
        ),
      );
      if (ok != true) return;
    }
    onChoose(o, shown);
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final question = view.dialog?.question ?? '';
    return Card(
      key: const Key('dialog-card'),
      margin: const EdgeInsets.fromLTRB(8, 4, 8, 4),
      color: view.status == Status.waiting
          ? scheme.tertiaryContainer
          : scheme.surfaceContainerHighest,
      child: Padding(
        padding: const EdgeInsets.all(10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            if (question.isNotEmpty)
              Text(question, style: Theme.of(context).textTheme.titleSmall),
            if (reviewAgain)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  'The menu changed: review again',
                  key: const Key('review-again'),
                  style: TextStyle(color: scheme.error),
                ),
              ),
            const SizedBox(height: 6),
            Wrap(
              spacing: 8,
              runSpacing: 6,
              children: [for (final o in view.options) _button(context, o)],
            ),
            if (view.options.isNotEmpty)
              Align(
                alignment: Alignment.centerRight,
                child: TextButton(
                  key: const Key('dismiss-menu'),
                  onPressed: enabled ? () => onDismiss(view) : null,
                  child: const Text('Dismiss menu'),
                ),
              ),
          ],
        ),
      ),
    );
  }

  Widget _button(BuildContext context, Option o) {
    final low = o.confidence < lowConfidenceThreshold;
    final prefix = o.kind == OptionKind.checkbox
        ? (o.checked == true ? '[x] ' : '[ ] ')
        : '';
    final label = Text('$prefix${o.label}${low ? '  (suggested)' : ''}');
    final onPressed = enabled ? () => _tap(context, o) : null;
    final key = Key('option-${o.id}');
    return o.selected
        ? FilledButton(key: key, onPressed: onPressed, child: label)
        : OutlinedButton(key: key, onPressed: onPressed, child: label);
  }
}
