/// Read-only dump of the real terminal screen of a session
/// (`GET .../terminal`): a diagnostic view, never typed into.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:material_ui/material_ui.dart';

import '../../state/session_view.dart';

Future<void> showRawScreen(
  BuildContext context, {
  required SessionKey session,
  required String hostLabel,
}) => showDialog<void>(
  context: context,
  builder: (_) => RawScreenDialog(session: session, hostLabel: hostLabel),
);

class RawScreenDialog extends ConsumerStatefulWidget {
  const RawScreenDialog({
    required this.session,
    required this.hostLabel,
    super.key,
  });
  final SessionKey session;
  final String hostLabel;

  @override
  ConsumerState<RawScreenDialog> createState() => _RawScreenDialogState();
}

class _RawScreenDialogState extends ConsumerState<RawScreenDialog> {
  late Future<String> _text = _load();

  Future<String> _load() async {
    final gw = await ref.read(sessionGatewayProvider(widget.session).future);
    return (await gw.terminal()).text;
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    key: const Key('raw-screen'),
    title: Text('Raw screen: ${widget.session.name} @ ${widget.hostLabel}'),
    content: SizedBox(
      width: double.maxFinite,
      child: FutureBuilder<String>(
        future: _text,
        builder: (c, snap) {
          if (snap.hasError) {
            return Text('Cannot read the screen: ${snap.error}');
          }
          if (!snap.hasData) {
            return const Center(child: CircularProgressIndicator());
          }
          return SingleChildScrollView(
            child: SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: SelectableText(
                snap.data!,
                key: const Key('raw-screen-text'),
                style: const TextStyle(fontFamily: 'monospace', fontSize: 12),
              ),
            ),
          );
        },
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => setState(() => _text = _load()),
        child: const Text('Refresh'),
      ),
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('Close'),
      ),
    ],
  );
}
