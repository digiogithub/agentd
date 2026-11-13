/// Kill / Remove menu with confirmation dialogs naming session and host.
/// Reusable: the session view can place [SessionActionsMenu] in its header.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:material_ui/material_ui.dart';

import '../../state/session_actions.dart';

/// Asks to kill [session]; returns the chosen signal (`TERM` / `KILL`) or
/// null when cancelled.
Future<String?> showKillDialog(
  BuildContext context, {
  required String session,
  required String hostLabel,
}) => showDialog<String>(
  context: context,
  builder: (_) => _KillDialog(session: session, hostLabel: hostLabel),
);

class _KillDialog extends StatefulWidget {
  const _KillDialog({required this.session, required this.hostLabel});
  final String session;
  final String hostLabel;

  @override
  State<_KillDialog> createState() => _KillDialogState();
}

class _KillDialogState extends State<_KillDialog> {
  String _signal = 'TERM';

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: const Text('Kill session?'),
    content: Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          'Stop "${widget.session}" on host "${widget.hostLabel}". '
          'The agent is interrupted and may lose unsaved work.',
        ),
        const SizedBox(height: 12),
        SegmentedButton<String>(
          segments: const [
            ButtonSegment(value: 'TERM', label: Text('TERM (graceful)')),
            ButtonSegment(value: 'KILL', label: Text('KILL (force)')),
          ],
          selected: {_signal},
          onSelectionChanged: (s) => setState(() => _signal = s.first),
        ),
      ],
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('Cancel'),
      ),
      FilledButton(
        onPressed: () => Navigator.pop(context, _signal),
        child: const Text('Kill'),
      ),
    ],
  );
}

/// Asks to remove [session]; true when confirmed. A running session is
/// stopped first (`force`).
Future<bool> showRemoveDialog(
  BuildContext context, {
  required String session,
  required String hostLabel,
  required bool running,
}) async =>
    await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Remove session?'),
        content: Text(
          running
              ? '"$session" on host "$hostLabel" is still running. It will '
                    'be stopped (TERM, then KILL) and removed, and its '
                    'final screen is lost.'
              : 'Remove "$session" from host "$hostLabel". Its final '
                    'screen is lost.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: Text(running ? 'Stop and remove' : 'Remove'),
          ),
        ],
      ),
    ) ??
    false;

/// Overflow menu with Kill (running only) and Remove. [onKilled] /
/// [onRemoved] run after the host accepted; failures show a snackbar.
class SessionActionsMenu extends ConsumerWidget {
  const SessionActionsMenu({
    required this.hostId,
    required this.hostLabel,
    required this.session,
    required this.running,
    this.canWrite = true,
    this.onKilled,
    this.onRemoved,
    super.key,
  });

  final String hostId;
  final String hostLabel;
  final String session;
  final bool running;
  final bool canWrite;
  final VoidCallback? onKilled;
  final VoidCallback? onRemoved;

  void _snack(BuildContext c, String m) =>
      ScaffoldMessenger.maybeOf(c)?.showSnackBar(SnackBar(content: Text(m)));

  Future<void> _kill(BuildContext context, WidgetRef ref) async {
    final signal = await showKillDialog(
      context,
      session: session,
      hostLabel: hostLabel,
    );
    if (signal == null || !context.mounted) return;
    final r = await ref
        .read(sessionActionsProvider)
        .kill(hostId, session, signal: signal);
    if (!context.mounted) return;
    switch (r) {
      case ActionOk():
        _snack(context, 'Sent $signal to $session');
        onKilled?.call();
      case ActionFailed(:final message):
        _snack(context, message);
    }
  }

  Future<void> _remove(BuildContext context, WidgetRef ref) async {
    final ok = await showRemoveDialog(
      context,
      session: session,
      hostLabel: hostLabel,
      running: running,
    );
    if (!ok || !context.mounted) return;
    final r = await ref
        .read(sessionActionsProvider)
        .remove(hostId, session, force: running);
    if (!context.mounted) return;
    switch (r) {
      case ActionOk():
        _snack(context, 'Removed $session');
        onRemoved?.call();
      case ActionFailed(:final notFound, :final message):
        if (notFound) {
          onRemoved?.call();
        } else {
          _snack(context, message);
        }
    }
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) => PopupMenuButton<String>(
    tooltip: 'Session actions',
    enabled: canWrite,
    onSelected: (v) =>
        v == 'kill' ? _kill(context, ref) : _remove(context, ref),
    itemBuilder: (_) => [
      if (running) const PopupMenuItem(value: 'kill', child: Text('Kill')),
      const PopupMenuItem(value: 'remove', child: Text('Remove')),
    ],
  );
}
