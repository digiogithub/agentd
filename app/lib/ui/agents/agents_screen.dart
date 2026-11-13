/// The home screen: agents of every paired host (AGED-US-0029).
library;

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../../repositories/host.dart';
import '../../state/agents.dart';
import '../../state/mailbox.dart';
import '../../state/providers.dart';
import 'agent_row.dart';

/// Clock seam for relative times.
final agentsClockProvider = Provider<DateTime Function()>(
  (ref) => DateTime.now,
);

class AgentsScreen extends ConsumerStatefulWidget {
  const AgentsScreen({super.key});

  @override
  ConsumerState<AgentsScreen> createState() => _AgentsScreenState();
}

class _AgentsScreenState extends ConsumerState<AgentsScreen> {
  String? _host;
  String? _project;
  Timer? _tick;

  @override
  void initState() {
    super.initState();
    // Keeps "5m ago" fresh without any data change.
    _tick = Timer.periodic(const Duration(seconds: 30), (_) {
      if (mounted) setState(() {});
    });
    // The mailbox badge needs the feed loaded even before it is opened.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final f = ref.read(mailboxFeedProvider);
      if (!f.loaded && !f.loading) {
        unawaited(ref.read(mailboxFeedProvider.notifier).refresh());
      }
    });
  }

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  void _open(AgentEntry e) {
    ref.read(agentsProvider.notifier).markSeen(e.hostId, e.name);
    unawaited(
      context.push(
        '/hosts/${e.hostId}/sessions/${Uri.encodeComponent(e.name)}',
      ),
    );
  }

  Future<void> _launch(List<Host> hosts) async {
    String? id;
    if (hosts.length == 1) {
      id = hosts.first.id;
    } else {
      id = await showModalBottomSheet<String>(
        context: context,
        builder: (c) => SafeArea(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const ListTile(title: Text('Launch on which host?')),
              for (final h in hosts)
                ListTile(
                  leading: const Icon(Icons.dns_outlined),
                  title: Text(h.label),
                  onTap: () => Navigator.of(c).pop(h.id),
                ),
            ],
          ),
        ),
      );
    }
    if (id != null && mounted) unawaited(context.push('/hosts/$id/launch'));
  }

  @override
  Widget build(BuildContext context) {
    final hostsAsync = ref.watch(hostsProvider);
    final agents = ref.watch(agentsProvider);
    final hosts = hostsAsync.value ?? const <Host>[];
    final now = ref.watch(agentsClockProvider)();

    final hostIds = {for (final h in hosts) h.id};
    final host = hostIds.contains(_host) ? _host : null;
    final scoped = [
      for (final e in agents.entries)
        if (host == null || e.hostId == host) e,
    ];
    final projects = ({for (final e in scoped) e.project}..remove('')).toList()
      ..sort();
    final project = projects.contains(_project) ? _project : null;
    final shown = [
      for (final e in scoped)
        if (project == null || e.project == project) e,
    ];
    final offlineHosts = [
      for (final h in hosts)
        if (agents.hosts[h.id] != null && !agents.hosts[h.id]!.online) h,
    ];

    Widget body;
    if (hostsAsync.isLoading && hosts.isEmpty) {
      body = const Center(child: CircularProgressIndicator());
    } else if (hosts.isEmpty) {
      body = _Empty(
        icon: Icons.link_off,
        title: 'No hosts paired',
        action: FilledButton.icon(
          icon: const Icon(Icons.add_link),
          label: const Text('Pair a host'),
          onPressed: () => context.push('/pair'),
        ),
      );
    } else if (agents.loading) {
      body = const Center(child: CircularProgressIndicator());
    } else if (agents.entries.isEmpty) {
      body = RefreshIndicator(
        onRefresh: ref.read(agentsProvider.notifier).refresh,
        child: LayoutBuilder(
          builder: (c, box) => ListView(
            physics: const AlwaysScrollableScrollPhysics(),
            children: [
              SizedBox(
                height: box.maxHeight,
                child: _Empty(
                  icon: Icons.smart_toy_outlined,
                  title: 'No agents running',
                  action: FilledButton.icon(
                    icon: const Icon(Icons.play_arrow),
                    label: const Text('Launch an agent'),
                    onPressed: () => _launch(hosts),
                  ),
                ),
              ),
            ],
          ),
        ),
      );
    } else {
      body = Column(
        children: [
          for (final h in offlineHosts)
            _OfflineBanner(host: h, info: agents.hosts[h.id]!, now: now),
          if (hosts.length > 1)
            _Chips(
              label: 'Host',
              options: {for (final h in hosts) h.id: h.label},
              selected: host,
              onSelected: (v) => setState(() => _host = v),
            ),
          if (projects.length > 1)
            _Chips(
              label: 'Project',
              options: {for (final p in projects) p: p},
              selected: project,
              onSelected: (v) => setState(() => _project = v),
            ),
          Expanded(
            child: RefreshIndicator(
              onRefresh: ref.read(agentsProvider.notifier).refresh,
              child: shown.isEmpty
                  ? ListView(
                      physics: const AlwaysScrollableScrollPhysics(),
                      children: const [
                        Padding(
                          padding: EdgeInsets.all(32),
                          child: Center(child: Text('No agents match')),
                        ),
                      ],
                    )
                  : ListView.separated(
                      physics: const AlwaysScrollableScrollPhysics(),
                      itemCount: shown.length,
                      separatorBuilder: (_, _) => const Divider(height: 1),
                      itemBuilder: (c, i) => AgentRow(
                        key: ValueKey(shown[i].key),
                        entry: shown[i],
                        now: now,
                        onTap: () => _open(shown[i]),
                      ),
                    ),
            ),
          ),
        ],
      );
    }

    return Scaffold(
      appBar: AppBar(
        title: const Text('agentd'),
        actions: [
          Consumer(
            builder: (c, ref, _) {
              final unread = ref.watch(mailboxUnreadProvider);
              return IconButton(
                tooltip: 'Mailbox',
                icon: Badge(
                  key: const Key('mailbox-badge'),
                  isLabelVisible: unread > 0,
                  label: Text('$unread'),
                  child: const Icon(Icons.mail_outline),
                ),
                onPressed: () => context.push('/mailbox'),
              );
            },
          ),
          IconButton(
            tooltip: 'Hosts',
            icon: const Icon(Icons.dns_outlined),
            onPressed: () => context.push('/hosts'),
          ),
          PopupMenuButton<String>(
            key: const Key('agents-overflow'),
            tooltip: 'More',
            onSelected: (v) {
              if (v == 'settings') context.push('/settings');
            },
            itemBuilder: (c) => const [
              PopupMenuItem(
                key: Key('menu-settings'),
                value: 'settings',
                child: Text('Settings'),
              ),
            ],
          ),
        ],
      ),
      floatingActionButton: hosts.isEmpty
          ? null
          : FloatingActionButton.extended(
              key: const Key('launch-fab'),
              tooltip: 'Launch an agent',
              icon: const Icon(Icons.play_arrow),
              label: const Text('Launch'),
              onPressed: () => _launch(hosts),
            ),
      body: body,
    );
  }
}

class _Empty extends StatelessWidget {
  const _Empty({required this.icon, required this.title, required this.action});
  final IconData icon;
  final String title;
  final Widget action;

  @override
  Widget build(BuildContext context) => Center(
    child: Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(icon, size: 48, color: Theme.of(context).disabledColor),
        const SizedBox(height: 12),
        Text(title, style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 12),
        action,
      ],
    ),
  );
}

class _Chips extends StatelessWidget {
  const _Chips({
    required this.label,
    required this.options,
    required this.selected,
    required this.onSelected,
  });

  final String label;
  final Map<String, String> options;
  final String? selected;
  final ValueChanged<String?> onSelected;

  @override
  Widget build(BuildContext context) => SizedBox(
    height: 48,
    child: ListView(
      scrollDirection: Axis.horizontal,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      children: [
        Center(child: Text('$label:')),
        const SizedBox(width: 8),
        ChoiceChip(
          label: const Text('All'),
          selected: selected == null,
          onSelected: (_) => onSelected(null),
        ),
        for (final o in options.entries) ...[
          const SizedBox(width: 8),
          ChoiceChip(
            key: Key('chip-$label-${o.key}'),
            label: Text(o.value),
            selected: selected == o.key,
            onSelected: (_) => onSelected(o.key),
          ),
        ],
      ],
    ),
  );
}

class _OfflineBanner extends StatelessWidget {
  const _OfflineBanner({
    required this.host,
    required this.info,
    required this.now,
  });
  final Host host;
  final HostFeedInfo info;
  final DateTime now;

  @override
  Widget build(BuildContext context) {
    final seen = relativeTime(info.lastSeen, now);
    final text = info.authError
        ? '${host.label}: access denied, re-pair this host'
        : '${host.label} offline${seen.isEmpty ? '' : ', last seen $seen'}'
              '${info.error == null ? '' : ' (${info.error})'}';
    return Material(
      key: Key('offline-${host.id}'),
      color: Theme.of(context).colorScheme.errorContainer,
      child: ListTile(
        dense: true,
        leading: const Icon(Icons.cloud_off),
        title: Text(text),
        trailing: info.authError
            ? TextButton(
                onPressed: () => context.push('/hosts'),
                child: const Text('Hosts'),
              )
            : null,
      ),
    );
  }
}
