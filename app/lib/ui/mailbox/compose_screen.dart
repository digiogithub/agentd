/// Compose: recipient picker (that host's sessions + `human:` names),
/// subject, body.
library;

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../../api/agentd_api.dart';
import '../../repositories/host.dart';
import '../../state/launch.dart';
import '../../state/mailbox.dart';
import '../../state/providers.dart';

class ComposeScreen extends ConsumerStatefulWidget {
  const ComposeScreen({this.hostId, this.to, super.key});
  final String? hostId;
  final String? to;

  @override
  ConsumerState<ComposeScreen> createState() => _ComposeScreenState();
}

class _ComposeScreenState extends ConsumerState<ComposeScreen> {
  final _form = GlobalKey<FormState>();
  late final _to = TextEditingController(text: widget.to ?? '');
  final _subject = TextEditingController();
  final _body = TextEditingController();
  String? _hostId;
  bool _busy = false;
  bool _unknown = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _hostId = widget.hostId;
  }

  @override
  void dispose() {
    _to.dispose();
    _subject.dispose();
    _body.dispose();
    super.dispose();
  }

  Future<void> _send({bool fresh = false}) async {
    if (!(_form.currentState?.validate() ?? false) || _hostId == null) return;
    setState(() {
      _busy = true;
      _error = null;
      _unknown = false;
    });
    final subject = _subject.text.trim();
    final r = await ref
        .read(mailboxActionsProvider)
        .send(
          _hostId!,
          SendRequest(
            to: _to.text.trim(),
            subject: subject.isEmpty ? null : subject,
            body: _body.text,
          ),
          fresh: fresh,
        );
    if (!mounted) return;
    switch (r) {
      case MailOk(:final message):
        ScaffoldMessenger.maybeOf(
          context,
        )?.showSnackBar(SnackBar(content: Text('Sent message #${message.id}')));
        unawaited(ref.read(mailboxFeedProvider.notifier).refresh());
        if (context.canPop()) context.pop();
        setState(() => _busy = false);
      case MailFailed(:final error, :final outcomeUnknown):
        setState(() {
          _busy = false;
          _unknown = outcomeUnknown;
          _error = outcomeUnknown
              ? '$error Send retries the same request (the host ignores a '
                    'duplicate); "Send again (new message)" sends a second '
                    'copy.'
              : error;
        });
    }
  }

  @override
  Widget build(BuildContext context) {
    // Keeps the idempotency keys alive for the life of this form only.
    ref.watch(mailboxActionsProvider);
    final hosts = ref.watch(hostsProvider).value ?? const <Host>[];
    if (_hostId == null && hosts.isNotEmpty) _hostId = hosts.first.id;
    final hostId = _hostId;
    final sessions = hostId == null
        ? const AsyncData<List<String>>([])
        : ref.watch(recipientsProvider(hostId));
    final info = hostId == null ? null : ref.watch(hostInfoProvider(hostId));
    return Scaffold(
      appBar: AppBar(title: const Text('New message')),
      body: Form(
        key: _form,
        child: ListView(
          padding: const EdgeInsets.all(16),
          children: [
            if (hosts.length > 1)
              DropdownButtonFormField<String>(
                key: const ValueKey('host'),
                initialValue: hostId,
                decoration: const InputDecoration(labelText: 'Host'),
                items: [
                  for (final h in hosts)
                    DropdownMenuItem(value: h.id, child: Text(h.label)),
                ],
                onChanged: (v) => setState(() => _hostId = v),
              ),
            TextFormField(
              key: const ValueKey('to'),
              controller: _to,
              decoration: const InputDecoration(
                labelText: 'To',
                hintText: 'session name or human:<name>',
              ),
              autocorrect: false,
              validator: (v) {
                final t = (v ?? '').trim();
                if (t.isEmpty) return 'Pick or type a recipient';
                if (!validRecipient(t)) {
                  return 'Use a session name or human:<name>';
                }
                return null;
              },
            ),
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              children: [
                for (final s in sessions.value ?? const <String>[])
                  ActionChip(
                    key: ValueKey('to-$s'),
                    label: Text(s),
                    onPressed: () => setState(() => _to.text = s),
                  ),
                if (info?.value != null && info!.value!.identity.isNotEmpty)
                  ActionChip(
                    key: const ValueKey('to-me'),
                    label: Text(info.value!.identity),
                    onPressed: () =>
                        setState(() => _to.text = info.value!.identity),
                  ),
              ],
            ),
            TextFormField(
              key: const ValueKey('subject'),
              controller: _subject,
              decoration: const InputDecoration(
                labelText: 'Subject (optional)',
              ),
            ),
            const SizedBox(height: 8),
            TextFormField(
              key: const ValueKey('compose-body'),
              controller: _body,
              minLines: 4,
              maxLines: 12,
              decoration: const InputDecoration(
                labelText: 'Message',
                border: OutlineInputBorder(),
              ),
              validator: (v) =>
                  (v ?? '').trim().isEmpty ? 'Write a message' : null,
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
            const SizedBox(height: 12),
            if (_unknown)
              Align(
                alignment: Alignment.centerRight,
                child: OutlinedButton(
                  key: const ValueKey('send-again-new'),
                  onPressed: _busy || hostId == null
                      ? null
                      : () => _send(fresh: true),
                  child: const Text('Send again (new message)'),
                ),
              ),
            Align(
              alignment: Alignment.centerRight,
              child: FilledButton.icon(
                key: const ValueKey('send'),
                icon: const Icon(Icons.send),
                label: const Text('Send'),
                onPressed: _busy || hostId == null ? null : _send,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
