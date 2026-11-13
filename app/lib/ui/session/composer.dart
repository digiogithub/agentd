/// Prompt composer, quick keys and the availability rules behind them.
library;

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart' hide View;

import '../../api/agentd_api.dart';
import '../../state/session_view.dart';

/// Whether text can be typed into the session right now, and if not why.
class ComposerAvailability {
  const ComposerAvailability(this.enabled, [this.reason]);
  final bool enabled;
  final String? reason;
}

ComposerAvailability composerAvailability(SessionViewState s) {
  final v = s.view;
  if (v == null) return const ComposerAvailability(false, 'Loading...');
  if (v.status == Status.exited) {
    return const ComposerAvailability(false, 'Session exited: read-only.');
  }
  if (v.input.state == InputState.draft) {
    return const ComposerAvailability(false, 'Someone is typing on the host.');
  }
  switch (v.status) {
    case Status.starting:
      return const ComposerAvailability(false, 'Session is starting...');
    case Status.working:
      return const ComposerAvailability(
        false,
        'The agent is working. Interrupt it to type.',
      );
    case Status.waiting || Status.idle:
      if (v.options.isNotEmpty) {
        return const ComposerAvailability(false, 'Answer the question above.');
      }
      return const ComposerAvailability(true);
    default:
      return const ComposerAvailability(false, 'Unknown session state.');
  }
}

class Composer extends StatefulWidget {
  const Composer({
    required this.state,
    required this.onSend,
    this.onSendAgain,
    this.onDismissUnknown,
    this.initialText = '',
    super.key,
  });

  final SessionViewState state;

  /// Returns true when the prompt was accepted (the field is then cleared).
  final Future<bool> Function(String text) onSend;
  final String initialText;

  /// Sends [SessionViewState.unknownText] again as a new request.
  final Future<bool> Function()? onSendAgain;
  final VoidCallback? onDismissUnknown;

  @override
  State<Composer> createState() => _ComposerState();
}

class _ComposerState extends State<Composer> {
  late final _ctl = TextEditingController(text: widget.initialText);

  @override
  void initState() {
    super.initState();
    _ctl.addListener(() => setState(() {}));
  }

  @override
  void didUpdateWidget(Composer old) {
    super.didUpdateWidget(old);
    // A prompt accepted through the banner's Retry: never leave a primed Send
    // holding the same text.
    final s = widget.state;
    if (s.sentSeq != old.state.sentSeq && s.sentText == _ctl.text) {
      _ctl.clear();
    }
  }

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  Future<void> _send() async {
    final text = _ctl.text;
    if (text.trim().isEmpty) return;
    if (await widget.onSend(text) && mounted) _ctl.clear();
  }

  @override
  Widget build(BuildContext context) {
    final s = widget.state;
    final v = s.view;
    final avail = composerAvailability(s);
    final ghost = v?.input.ghost ?? '';
    final draft = v != null && v.input.state == InputState.draft;
    final canSend = avail.enabled && !s.acting && _ctl.text.trim().isNotEmpty;
    return Padding(
      padding: const EdgeInsets.fromLTRB(8, 4, 8, 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          if (s.unknownText != null)
            Container(
              key: const Key('unknown-note'),
              margin: const EdgeInsets.only(bottom: 4),
              padding: const EdgeInsets.all(8),
              decoration: BoxDecoration(
                color: Theme.of(context).colorScheme.tertiaryContainer,
                borderRadius: BorderRadius.circular(8),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Sent? result unknown'),
                  Text(
                    s.unknownText!,
                    maxLines: 3,
                    overflow: TextOverflow.ellipsis,
                    style: const TextStyle(fontFamily: 'monospace'),
                  ),
                  const Text('Check the screen above before sending again.'),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.end,
                    children: [
                      TextButton(
                        key: const Key('unknown-dismiss'),
                        onPressed: widget.onDismissUnknown,
                        child: const Text('Dismiss'),
                      ),
                      FilledButton.tonal(
                        key: const Key('unknown-send-again'),
                        onPressed: avail.enabled && !s.acting
                            ? widget.onSendAgain
                            : null,
                        child: const Text('Send again'),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          if (!avail.enabled && avail.reason != null)
            Padding(
              padding: const EdgeInsets.only(bottom: 4),
              child: Text(
                avail.reason!,
                key: const Key('composer-reason'),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          if (draft && v.input.text.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(bottom: 4),
              child: Text(
                'On the host: ${v.input.text}',
                key: const Key('host-draft'),
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(
                  fontFamily: 'monospace',
                  fontStyle: FontStyle.italic,
                ),
              ),
            ),
          Row(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Expanded(
                child: CallbackShortcuts(
                  bindings: {
                    const SingleActivator(
                      LogicalKeyboardKey.enter,
                      control: true,
                    ): () {
                      if (canSend) _send();
                    },
                  },
                  child: TextField(
                    key: const Key('composer-field'),
                    controller: _ctl,
                    enabled: avail.enabled,
                    minLines: 1,
                    maxLines: 6,
                    keyboardType: TextInputType.multiline,
                    textInputAction: TextInputAction.newline,
                    decoration: InputDecoration(
                      isDense: true,
                      border: const OutlineInputBorder(),
                      // The ghost is only a hint: it is never sent implicitly.
                      hintText: ghost.isNotEmpty ? ghost : 'Message the agent',
                    ),
                  ),
                ),
              ),
              const SizedBox(width: 8),
              IconButton.filled(
                key: const Key('composer-send'),
                tooltip: 'Send',
                onPressed: canSend ? _send : null,
                icon: const Icon(Icons.send),
              ),
            ],
          ),
          if (ghost.isNotEmpty && avail.enabled)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                key: const Key('use-suggestion'),
                onPressed: () {
                  _ctl.text = ghost;
                  _ctl.selection = TextSelection.collapsed(
                    offset: ghost.length,
                  );
                },
                icon: const Icon(Icons.auto_awesome, size: 16),
                label: const Text('Use suggestion'),
              ),
            ),
        ],
      ),
    );
  }
}

class QuickKeys extends StatelessWidget {
  const QuickKeys({
    required this.state,
    required this.onKeys,
    required this.onInterrupt,
    super.key,
  });

  final SessionViewState state;
  final void Function(List<String> keys) onKeys;
  final void Function(View shown) onInterrupt;

  Future<void> _confirmInterrupt(BuildContext context) async {
    // The view the user saw when tapping: if it moves on while the dialog is
    // open the notifier refuses to send.
    final shown = state.view;
    if (shown == null) return;
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text('Interrupt the agent?'),
        content: const Text('This stops what the agent is doing.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: const Key('confirm-interrupt'),
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Interrupt'),
          ),
        ],
      ),
    );
    if (ok == true) onInterrupt(shown);
  }

  @override
  Widget build(BuildContext context) {
    final v = state.view;
    final running =
        v != null &&
        (v.status == Status.working ||
            v.status == Status.idle ||
            v.status == Status.waiting);
    final draft = v != null && v.input.state == InputState.draft;
    final keysOk = running && !draft && !state.acting;
    final canInterrupt =
        v != null &&
        (v.status == Status.working || v.status == Status.waiting) &&
        !state.acting;

    Widget key(String id, String label, String name) => OutlinedButton(
      key: Key('key-$id'),
      onPressed: keysOk ? () => onKeys([name]) : null,
      child: Text(label),
    );

    return Padding(
      padding: const EdgeInsets.fromLTRB(8, 0, 8, 4),
      child: Wrap(
        spacing: 6,
        runSpacing: 4,
        children: [
          key('esc', 'Esc', 'esc'),
          FilledButton.tonal(
            key: const Key('key-interrupt'),
            onPressed: canInterrupt ? () => _confirmInterrupt(context) : null,
            child: const Text('Interrupt'),
          ),
          key('up', '↑', 'up'),
          key('down', '↓', 'down'),
          key('enter', 'Enter', 'enter'),
          key('tab', 'Tab', 'tab'),
        ],
      ),
    );
  }
}
