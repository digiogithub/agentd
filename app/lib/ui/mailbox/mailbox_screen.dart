/// Unified human inbox across hosts, newest first, with an unread badge.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../../repositories/host.dart';
import '../../state/mailbox.dart';
import '../../state/providers.dart';
import 'format.dart';
import 'peek_screen.dart';

class MailboxScreen extends ConsumerStatefulWidget {
  const MailboxScreen({super.key});

  @override
  ConsumerState<MailboxScreen> createState() => _MailboxScreenState();
}

class _MailboxScreenState extends ConsumerState<MailboxScreen> {
  @override
  void initState() {
    super.initState();
    Future.microtask(() => ref.read(mailboxFeedProvider.notifier).refresh());
  }

  Future<void> _peek(List<Host> hosts) async {
    final r = await showPeekDialog(
      context,
      hosts.where((h) => h.scope == 'admin').toList(),
    );
    if (r == null || !mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => PeekScreen(hostId: r.hostId, mailbox: r.mailbox),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final feed = ref.watch(mailboxFeedProvider);
    final hosts = ref.watch(hostsProvider).value ?? const <Host>[];
    final labels = {for (final h in hosts) h.id: h.label};
    final unread = feed.unreadCount;
    return Scaffold(
      appBar: AppBar(
        title: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Text('Mailbox'),
            if (unread > 0) ...[
              const SizedBox(width: 8),
              Badge(
                key: const ValueKey('unread-badge'),
                label: Text('$unread'),
              ),
            ],
          ],
        ),
        actions: [
          IconButton(
            tooltip: 'Refresh',
            icon: const Icon(Icons.refresh),
            onPressed: () => ref.read(mailboxFeedProvider.notifier).refresh(),
          ),
          if (hosts.any((h) => h.scope == 'admin'))
            IconButton(
              tooltip: 'Peek at another mailbox',
              icon: const Icon(Icons.visibility_outlined),
              onPressed: () => _peek(hosts),
            ),
        ],
      ),
      floatingActionButton: hosts.isEmpty
          ? null
          : FloatingActionButton.extended(
              icon: const Icon(Icons.edit_outlined),
              label: const Text('Compose'),
              onPressed: () => context.push('/mailbox/compose'),
            ),
      body: _body(context, feed, labels),
    );
  }

  Widget _body(
    BuildContext context,
    MailboxFeedState feed,
    Map<String, String> labels,
  ) {
    if (!feed.loaded && feed.loading) {
      return const Center(child: CircularProgressIndicator());
    }
    return RefreshIndicator(
      onRefresh: () => ref.read(mailboxFeedProvider.notifier).refresh(),
      child: ListView(
        physics: const AlwaysScrollableScrollPhysics(),
        children: [
          for (final e in feed.errors.entries)
            ListTile(
              key: ValueKey('error-${e.key}'),
              leading: const Icon(Icons.cloud_off_outlined),
              title: Text('${labels[e.key] ?? e.key}: ${e.value}'),
            ),
          if (feed.loaded && feed.items.isEmpty && feed.errors.isEmpty)
            const Padding(
              padding: EdgeInsets.all(32),
              child: Center(child: Text('No messages')),
            ),
          for (final i in feed.items)
            ListTile(
              key: ValueKey('msg-${i.hostId}-${i.message.id}'),
              leading: Icon(
                i.unread ? Icons.mark_email_unread : Icons.drafts_outlined,
                color: i.unread ? Theme.of(context).colorScheme.primary : null,
              ),
              title: Text(
                messageTitle(i.message),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: i.unread
                    ? const TextStyle(fontWeight: FontWeight.bold)
                    : null,
              ),
              subtitle: Text(
                'from ${i.message.sender}  -  '
                '${labels[i.hostId] ?? i.hostId}  -  ${i.message.stateRaw}',
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
              trailing: Text(ago(i.message.createdAt)),
              onTap: () =>
                  context.push('/hosts/${i.hostId}/mailbox/${i.message.id}'),
            ),
          if (feed.hasMore)
            Padding(
              padding: const EdgeInsets.all(12),
              child: Center(
                child: feed.loadingMore
                    ? const CircularProgressIndicator()
                    : OutlinedButton(
                        key: const ValueKey('load-more'),
                        onPressed: () =>
                            ref.read(mailboxFeedProvider.notifier).loadMore(),
                        child: const Text('Load more'),
                      ),
              ),
            ),
          const SizedBox(height: 80),
        ],
      ),
    );
  }
}
