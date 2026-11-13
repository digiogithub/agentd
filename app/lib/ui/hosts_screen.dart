/// Hosts screen: paired gateways with live status (frontend-app.md 1.2).
library;

import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../repositories/host.dart';
import '../state/deep_link_router.dart';
import '../state/providers.dart';

class HostsScreen extends ConsumerWidget {
  const HostsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final hosts = ref.watch(hostsProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('Hosts')),
      floatingActionButton: FloatingActionButton.extended(
        tooltip: 'Pair a new host',
        icon: const Icon(Icons.add_link),
        label: const Text('Pair host'),
        onPressed: () => context.push('/pair'),
      ),
      body: hosts.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: Text('Cannot read hosts: $e')),
        data: (list) => list.isEmpty
            ? const Center(child: Text('No hosts paired'))
            : ListView.builder(
                padding: const EdgeInsets.only(bottom: 88),
                itemCount: list.length,
                itemBuilder: (c, i) => HostTile(host: list[i]),
              ),
      ),
    );
  }
}

String statusLabel(HostStatus s) => switch (s) {
  HostStatus.checking => 'Checking',
  HostStatus.online => 'Online',
  HostStatus.offline => 'Offline',
  HostStatus.authError => 'Auth error: re-pair',
  HostStatus.pinMismatch => 'Certificate changed',
};

class HostTile extends ConsumerWidget {
  const HostTile({required this.host, super.key});

  final Host host;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final status =
        ref.watch(hostStatusProvider(host.id)).value ?? HostStatus.checking;
    final scheme = Theme.of(context).colorScheme;
    final color = switch (status) {
      HostStatus.online => Colors.green,
      HostStatus.checking => scheme.outline,
      HostStatus.offline => scheme.outline,
      _ => scheme.error,
    };
    final label = statusLabel(status);
    return Semantics(
      container: true,
      label: '${host.label}, $label',
      child: ListTile(
        leading: Icon(
          Icons.circle,
          size: 14,
          color: color,
          semanticLabel: label,
        ),
        title: Text(host.label),
        subtitle: Text('${host.baseUrl}  -  $label  -  ${host.scope}'),
        trailing: PopupMenuButton<String>(
          tooltip: 'Host actions for ${host.label}',
          onSelected: (v) => switch (v) {
            'rename' => _rename(context, ref),
            'launch' => context.push('/hosts/${host.id}/launch'),
            'repair' => context.push(pairLocation(rehost: host.id)),
            _ => _remove(context, ref),
          },
          itemBuilder: (c) => const [
            PopupMenuItem(value: 'launch', child: Text('Launch agent')),
            PopupMenuItem(value: 'rename', child: Text('Rename')),
            PopupMenuItem(value: 'repair', child: Text('Re-pair')),
            PopupMenuItem(value: 'remove', child: Text('Remove')),
          ],
        ),
      ),
    );
  }

  Future<void> _rename(BuildContext context, WidgetRef ref) async {
    final name = await showDialog<String>(
      context: context,
      builder: (c) => _RenameDialog(initial: host.label),
    );
    if (name != null) {
      await ref.read(hostsProvider.notifier).rename(host.id, name);
    }
  }

  Future<void> _remove(BuildContext context, WidgetRef ref) async {
    final cmd = 'agentd serve devices revoke ${host.deviceId}';
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: Text('Remove ${host.label}?'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'This forgets the token on this device only. The host still '
              'trusts this device until you revoke it there:',
            ),
            const SizedBox(height: 8),
            SelectableText(
              cmd,
              style: const TextStyle(fontFamily: 'monospace'),
            ),
            const SizedBox(height: 4),
            Text('Device id: ${host.deviceId}'),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Clipboard.setData(ClipboardData(text: cmd)),
            child: const Text('Copy command'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Remove'),
          ),
        ],
      ),
    );
    if (ok ?? false) await ref.read(hostsProvider.notifier).remove(host.id);
  }
}

class _RenameDialog extends StatefulWidget {
  const _RenameDialog({required this.initial});
  final String initial;

  @override
  State<_RenameDialog> createState() => _RenameDialogState();
}

class _RenameDialogState extends State<_RenameDialog> {
  late final _ctl = TextEditingController(text: widget.initial);

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: const Text('Rename host'),
    content: TextField(
      controller: _ctl,
      autofocus: true,
      decoration: const InputDecoration(labelText: 'Label'),
      onSubmitted: (v) => Navigator.pop(context, v),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('Cancel'),
      ),
      FilledButton(
        onPressed: () => Navigator.pop(context, _ctl.text),
        child: const Text('Save'),
      ),
    ],
  );
}
