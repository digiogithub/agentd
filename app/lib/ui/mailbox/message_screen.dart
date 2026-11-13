/// Message detail: selectable body, explicit "mark read", reply.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:material_ui/material_ui.dart';

import '../../api/agentd_api.dart';
import '../../repositories/host.dart';
import '../../state/mailbox.dart';
import '../../state/providers.dart';
import 'format.dart';

class MessageScreen extends ConsumerStatefulWidget {
  const MessageScreen({required this.hostId, required this.msgId, super.key});
  final String hostId;
  final int msgId;

  @override
  ConsumerState<MessageScreen> createState() => _MessageScreenState();
}

class _MessageScreenState extends ConsumerState<MessageScreen> {
  final _reply = TextEditingController();
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _reply.dispose();
    super.dispose();
  }

  MessageKey get _key => (hostId: widget.hostId, id: widget.msgId);

  Future<void> _markRead() async {
    setState(() => _busy = true);
    final r = await ref
        .read(mailboxActionsProvider)
        .markRead(widget.hostId, widget.msgId);
    if (!mounted) return;
    setState(() {
      _busy = false;
      _error = r is MailFailed ? r.error : null;
    });
    if (r is MailOk) ref.invalidate(messageProvider(_key));
  }

  Future<void> _send() async {
    final body = _reply.text.trim();
    if (body.isEmpty) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    final r = await ref
        .read(mailboxActionsProvider)
        .reply(widget.hostId, widget.msgId, body);
    if (!mounted) return;
    switch (r) {
      case MailOk(:final message):
        _reply.clear();
        setState(() => _busy = false);
        ref.invalidate(messageProvider(_key));
        ScaffoldMessenger.maybeOf(context)?.showSnackBar(
          SnackBar(
            content: Text(
              message.warning == null
                  ? 'Reply sent'
                  : 'Reply stored: ${message.warning}',
            ),
          ),
        );
      case MailFailed(:final error, :final outcomeUnknown):
        setState(() {
          _busy = false;
          _error = outcomeUnknown
              ? '$error Sending again reuses the same request key.'
              : error;
        });
    }
  }

  @override
  Widget build(BuildContext context) {
    // Keeps the idempotency keys alive for the life of this screen only.
    ref.watch(mailboxActionsProvider);
    final msg = ref.watch(messageProvider(_key));
    final hosts = ref.watch(hostsProvider).value ?? const <Host>[];
    final label =
        hosts.where((h) => h.id == widget.hostId).firstOrNull?.label ??
        widget.hostId;
    return Scaffold(
      appBar: AppBar(title: Text('Message #${widget.msgId}')),
      body: msg.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(describeMailboxError(e)),
                const SizedBox(height: 12),
                OutlinedButton(
                  onPressed: () => ref.invalidate(messageProvider(_key)),
                  child: const Text('Retry'),
                ),
              ],
            ),
          ),
        ),
        data: (m) => _detail(context, m, label),
      ),
    );
  }

  Widget _detail(BuildContext context, Message m, String hostLabel) {
    final isRead =
        m.state == MessageState.read || m.state == MessageState.replied;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        if (m.subject.isNotEmpty)
          Text(m.subject, style: Theme.of(context).textTheme.titleLarge),
        const SizedBox(height: 4),
        Text('From ${m.sender} to ${m.recipient}'),
        Text('Host $hostLabel  -  ${m.stateRaw}  -  ${ago(m.createdAt)} ago'),
        const Divider(height: 24),
        SelectableText(
          m.body,
          key: const ValueKey('body'),
          style: const TextStyle(fontFamily: 'monospace'),
        ),
        const SizedBox(height: 16),
        Align(
          alignment: Alignment.centerLeft,
          child: OutlinedButton.icon(
            key: const ValueKey('mark-read'),
            icon: const Icon(Icons.done),
            label: Text(isRead ? 'Read' : 'Mark read'),
            onPressed: isRead || _busy ? null : _markRead,
          ),
        ),
        const Divider(height: 32),
        TextField(
          key: const ValueKey('reply'),
          controller: _reply,
          minLines: 2,
          maxLines: 8,
          decoration: const InputDecoration(
            labelText: 'Reply',
            border: OutlineInputBorder(),
          ),
        ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Text(
              _error!,
              key: const ValueKey('mail-error'),
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        const SizedBox(height: 8),
        Align(
          alignment: Alignment.centerRight,
          child: FilledButton.icon(
            key: const ValueKey('send-reply'),
            icon: const Icon(Icons.send),
            label: const Text('Send reply'),
            onPressed: _busy ? null : _send,
          ),
        ),
      ],
    );
  }
}
