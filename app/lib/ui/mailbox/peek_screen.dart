/// Admin-only peek at another mailbox (no state change on the host).
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:material_ui/material_ui.dart';

import '../../repositories/host.dart';
import '../../state/mailbox.dart';
import 'format.dart';

Future<({String hostId, String mailbox})?> showPeekDialog(
  BuildContext context,
  List<Host> adminHosts,
) {
  final mailbox = TextEditingController();
  var hostId = adminHosts.first.id;
  return showDialog<({String hostId, String mailbox})>(
    context: context,
    builder: (ctx) => StatefulBuilder(
      builder: (ctx, setState) => AlertDialog(
        title: const Text('Peek at a mailbox'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (adminHosts.length > 1)
              DropdownButton<String>(
                value: hostId,
                isExpanded: true,
                items: [
                  for (final h in adminHosts)
                    DropdownMenuItem(value: h.id, child: Text(h.label)),
                ],
                onChanged: (v) => setState(() => hostId = v ?? hostId),
              ),
            TextField(
              controller: mailbox,
              decoration: const InputDecoration(
                labelText: 'Session or human:<name>',
              ),
              autocorrect: false,
            ),
            const SizedBox(height: 8),
            const Text('Read-only: messages are not marked delivered or read.'),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () {
              final m = mailbox.text.trim();
              if (m.isEmpty) return;
              Navigator.pop(ctx, (hostId: hostId, mailbox: m));
            },
            child: const Text('Peek'),
          ),
        ],
      ),
    ),
  );
}

class PeekScreen extends ConsumerWidget {
  const PeekScreen({required this.hostId, required this.mailbox, super.key});
  final String hostId;
  final String mailbox;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final page = ref.watch(peekProvider((hostId: hostId, mailbox: mailbox)));
    return Scaffold(
      appBar: AppBar(title: Text('Peek: $mailbox')),
      body: page.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: Text(describeMailboxError(e))),
        data: (p) => p.messages.isEmpty
            ? const Center(child: Text('No messages'))
            : ListView(
                children: [
                  for (final m in p.messages)
                    ListTile(
                      title: Text(messageTitle(m)),
                      subtitle: Text(
                        'from ${m.sender}  -  ${m.stateRaw}\n${m.body}',
                        maxLines: 4,
                        overflow: TextOverflow.ellipsis,
                      ),
                      isThreeLine: true,
                    ),
                ],
              ),
      ),
    );
  }
}
