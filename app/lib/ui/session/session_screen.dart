/// Session screen: header, transcript, dialog card, composer, quick keys.
library;

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart' hide View;

import '../../api/agentd_api.dart';
import '../../state/providers.dart';
import '../../state/session_view.dart';
import '../launch/session_actions_menu.dart';
import 'composer.dart';
import 'raw_screen_dialog.dart';
import 'dialog_card.dart';
import 'session_status.dart';
import 'transcript.dart';

class SessionScreen extends ConsumerStatefulWidget {
  const SessionScreen({required this.hostId, required this.session, super.key});
  final String hostId;
  final String session;

  @override
  ConsumerState<SessionScreen> createState() => _SessionScreenState();
}

class _SessionScreenState extends ConsumerState<SessionScreen>
    with WidgetsBindingObserver {
  SessionKey get _key => sessionKey(widget.hostId, widget.session);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      ref.read(sessionViewProvider(_key).notifier).setVisible(true);
    });
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState s) {
    final n = ref.read(sessionViewProvider(_key).notifier);
    if (s == AppLifecycleState.resumed) {
      n.setVisible(true);
      n.reconnect();
    } else {
      n.setVisible(false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final s = ref.watch(sessionViewProvider(_key));
    final n = ref.read(sessionViewProvider(_key).notifier);
    final hosts = ref.watch(hostsProvider).value ?? const [];
    final host = hosts.where((h) => h.id == widget.hostId).firstOrNull;
    final hostLabel = host?.label;
    final v = s.view;
    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // The host is shown prominently: the same session name may exist
            // on several hosts (and a deep link may have brought us here).
            Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  Icons.dns_outlined,
                  size: 14,
                  color: Theme.of(context).colorScheme.primary,
                ),
                const SizedBox(width: 4),
                Flexible(
                  child: Text(
                    hostLabel ?? widget.hostId,
                    key: const Key('host-label'),
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.titleSmall?.copyWith(
                      color: Theme.of(context).colorScheme.primary,
                      fontWeight: FontWeight.bold,
                    ),
                  ),
                ),
              ],
            ),
            Text(
              widget.session,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodyMedium,
            ),
          ],
        ),
        actions: [
          if (v != null) ...[
            if (v.attention.set)
              Icon(
                Icons.circle,
                key: const Key('attention-dot'),
                size: 12,
                color: Theme.of(context).colorScheme.error,
              ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 4),
              child: Center(child: StatusBadge(v)),
            ),
          ],
          IconButton(
            key: const Key('raw-screen-button'),
            tooltip: 'Raw screen',
            icon: const Icon(Icons.terminal),
            onPressed: () => showRawScreen(
              context,
              session: _key,
              hostLabel: hostLabel ?? widget.hostId,
            ),
          ),
          IconButton(
            key: const Key('mail-session-button'),
            tooltip: 'Send mail to this session',
            icon: const Icon(Icons.mail_outline),
            onPressed: () => unawaited(
              GoRouter.of(context).push(
                Uri(
                  path: '/mailbox/compose',
                  queryParameters: {
                    'host': widget.hostId,
                    'to': widget.session,
                  },
                ).toString(),
              ),
            ),
          ),
          SessionActionsMenu(
            hostId: widget.hostId,
            hostLabel: hostLabel ?? widget.hostId,
            session: widget.session,
            running: v != null && v.status != Status.exited,
            canWrite: host == null || host.scope != 'read',
            onRemoved: () => Navigator.of(context).maybePop(),
          ),
        ],
      ),
      body: _body(context, s, n),
    );
  }

  Widget _body(
    BuildContext context,
    SessionViewState s,
    SessionViewNotifier n,
  ) {
    final v = s.view;
    if (v == null) {
      if (s.error != null) {
        return Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(s.error!, key: const Key('session-error')),
                const SizedBox(height: 12),
                OutlinedButton(
                  onPressed: n.reconnect,
                  child: const Text('Retry'),
                ),
              ],
            ),
          ),
        );
      }
      return const Center(child: CircularProgressIndicator());
    }
    final exited = v.status == Status.exited;
    final avail = composerAvailability(s);
    final showDialog =
        !exited &&
        v.status != Status.working &&
        v.status != Status.starting &&
        (v.dialog != null || v.options.isNotEmpty);
    return Column(
      children: [
        if (s.reconnecting)
          const LinearProgressIndicator(key: Key('reconnecting-bar')),
        if (s.error != null) _Banner(s.error!, FeedbackKind.error),
        if (exited)
          _Banner(
            'Session exited${v.exitCode == null ? '' : ' with code ${v.exitCode}'}'
            ': read-only.',
            FeedbackKind.info,
            key: const Key('exited-banner'),
          ),
        Expanded(child: Transcript(view: v)),
        if (showDialog)
          DialogCard(
            view: v,
            enabled:
                !s.acting &&
                (v.status == Status.waiting || v.status == Status.idle) &&
                v.input.state != InputState.draft,
            reviewAgain: s.reviewAgain,
            onChoose: (o, shown) => n.choose(o, shown: shown),
            onDismiss: (shown) => n.cancelMenu(shown: shown),
          ),
        if (s.feedback != null)
          _Banner(
            s.feedback!.message,
            s.feedback!.kind,
            key: const Key('act-feedback'),
            onRetry: s.canRetry ? n.retry : null,
            onClose: n.dismissFeedback,
          ),
        if (!exited) ...[
          QuickKeys(
            state: s,
            onKeys: (k) => n.keys(k),
            onInterrupt: (shown) => n.interrupt(shown: shown),
          ),
          Composer(
            state: s,
            onSend: n.prompt,
            onSendAgain: n.sendAgain,
            onDismissUnknown: n.dismissUnknown,
          ),
        ] else if (!avail.enabled)
          const SizedBox(height: 8),
      ],
    );
  }
}

class _Banner extends StatelessWidget {
  const _Banner(
    this.message,
    this.kind, {
    this.onRetry,
    this.onClose,
    super.key,
  });
  final String message;
  final FeedbackKind kind;
  final VoidCallback? onRetry;
  final VoidCallback? onClose;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final bg = switch (kind) {
      FeedbackKind.error => scheme.errorContainer,
      FeedbackKind.warning || FeedbackKind.review => scheme.tertiaryContainer,
      FeedbackKind.info => scheme.surfaceContainerHighest,
    };
    return Container(
      width: double.infinity,
      color: bg,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      child: Row(
        children: [
          Expanded(child: Text(message)),
          if (onRetry != null)
            TextButton(
              key: const Key('feedback-retry'),
              onPressed: onRetry,
              child: const Text('Retry'),
            ),
          if (onClose != null)
            IconButton(
              key: const Key('feedback-close'),
              icon: const Icon(Icons.close, size: 18),
              onPressed: onClose,
            ),
        ],
      ),
    );
  }
}
