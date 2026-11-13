/// Settings: notifications, tray, Android permission and ntfy help
/// (frontend-app.md section 6, AGED-US-0032).
library;

import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:material_ui/material_ui.dart';

import '../../repositories/host.dart';
import '../../state/notifications.dart';
import '../../state/providers.dart';

/// `[api.notify]` snippet to put in the gateway's `config.toml`.
String ntfySnippet() => '''[api.notify]
enabled = true
ntfy_url = "https://ntfy.example.net/agentd-<random-topic>"
ntfy_token_env = "AGENTD_NTFY_TOKEN"   # optional; read from the gateway env
on = ["waiting", "turn_done", "exited"]''';

class SettingsScreen extends ConsumerWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final s = ref.watch(notifySettingsProvider);
    final n = ref.read(notifySettingsProvider.notifier);
    final desktop = ref.watch(isDesktopProvider);
    final android = ref.watch(isAndroidProvider);
    final hosts = ref.watch(hostsProvider).value ?? const <Host>[];
    return Scaffold(
      appBar: AppBar(title: const Text('Settings')),
      body: ListView(
        children: [
          const _Header('Notifications'),
          SwitchListTile(
            key: const Key('set-mute'),
            title: const Text('Mute all notifications'),
            subtitle: const Text('No alerts while the app is running.'),
            value: s.muted,
            onChanged: n.setMuted,
          ),
          SwitchListTile(
            key: const Key('set-preview'),
            title: const Text('Include preview'),
            subtitle: const Text(
              'Adds the session\'s one-line preview to the alert. Off by '
              'default: it is terminal text that other apps on this device '
              'could read.',
            ),
            value: s.includePreview,
            onChanged: n.setIncludePreview,
          ),
          if (hosts.isNotEmpty) const _Header('Per host'),
          for (final h in hosts)
            SwitchListTile(
              key: Key('set-host-${h.id}'),
              title: Text('Notify for ${h.label}'),
              value: !s.mutedHosts.contains(h.id),
              onChanged: (v) => n.setHostMuted(h.id, !v),
            ),
          if (desktop) ...[
            const _Header('Desktop'),
            SwitchListTile(
              key: const Key('set-tray'),
              title: const Text('Keep running in the tray'),
              subtitle: const Text(
                'Closing the window hides it; use the tray menu to quit. '
                'Needs a tray (GNOME needs the AppIndicator extension).',
              ),
              value: s.keepInTray,
              onChanged: n.setKeepInTray,
            ),
          ],
          if (android) ...[
            const _Header('Android'),
            const _PermissionTile(),
            SwitchListTile(
              key: const Key('set-fgs'),
              title: const Text('Keep connected (foreground service)'),
              subtitle: const Text(
                'Coming later. Use ntfy below for background alerts.',
              ),
              value: false,
              onChanged: null,
            ),
          ],
          const _Header('Background alerts with ntfy'),
          const _NtfyHelp(),
          const SizedBox(height: 24),
        ],
      ),
    );
  }
}

class _Header extends StatelessWidget {
  const _Header(this.text);
  final String text;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(16, 20, 16, 4),
    child: Text(
      text,
      style: Theme.of(context).textTheme.titleSmall
          ?.copyWith(color: Theme.of(context).colorScheme.primary),
    ),
  );
}

class _PermissionTile extends ConsumerWidget {
  const _PermissionTile();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final p = ref.watch(notifyPermissionProvider);
    return ListTile(
      key: const Key('set-permission'),
      leading: Icon(
        p == NotifyPermission.granted
            ? Icons.notifications_active_outlined
            : Icons.notifications_off_outlined,
      ),
      title: Text(switch (p) {
        NotifyPermission.granted => 'Notifications allowed',
        NotifyPermission.denied => 'Notifications not allowed',
        NotifyPermission.unknown => 'Checking notification permission',
      }),
      trailing: p == NotifyPermission.denied
          ? FilledButton(
              key: const Key('set-allow'),
              onPressed: () => requestWithRationale(context, ref),
              child: const Text('Allow'),
            )
          : null,
    );
  }
}

/// Android 13+: explain first, then ask the system.
Future<void> requestWithRationale(BuildContext context, WidgetRef ref) async {
  final ok = await showDialog<bool>(
    context: context,
    builder: (c) => AlertDialog(
      title: const Text('Allow notifications?'),
      content: const Text(
        'agentd alerts you when an agent is waiting for you, finished its '
        'turn or exited. Alerts name the session and host only, never the '
        'terminal text.',
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(c, false),
          child: const Text('Not now'),
        ),
        FilledButton(
          key: const Key('rationale-continue'),
          onPressed: () => Navigator.pop(c, true),
          child: const Text('Continue'),
        ),
      ],
    ),
  );
  if (ok == true) await ref.read(notifyPermissionProvider.notifier).request();
}

class _NtfyHelp extends ConsumerWidget {
  const _NtfyHelp();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final hosts = ref.watch(hostsProvider).value ?? const <Host>[];
    final snippet = ntfySnippet();
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text(
            'When the app is closed or in the background, agentd can push '
            'alerts through ntfy. 1. Install the ntfy app and subscribe to a '
            'random, hard to guess topic. 2. On each host, add this to '
            'config.toml (agentd serve) and restart it. Tapping an alert '
            'opens the session here. No screen text is sent unless the host '
            'sets include_preview.',
          ),
          const SizedBox(height: 8),
          if (hosts.isEmpty)
            const Text('Pair a host to see its setup.')
          else
            for (final h in hosts)
              ExpansionTile(
                key: Key('ntfy-host-${h.id}'),
                tilePadding: EdgeInsets.zero,
                title: Text(h.label),
                subtitle: Text('ntfy click link: agentd://open?h=${h.label}'),
                children: [
                  Container(
                    width: double.infinity,
                    padding: const EdgeInsets.all(8),
                    color: Theme.of(context)
                        .colorScheme
                        .surfaceContainerHighest,
                    child: SelectableText(
                      snippet,
                      style: const TextStyle(fontFamily: 'monospace'),
                    ),
                  ),
                  Align(
                    alignment: Alignment.centerRight,
                    child: TextButton.icon(
                      key: Key('ntfy-copy-${h.id}'),
                      icon: const Icon(Icons.copy),
                      label: const Text('Copy snippet'),
                      onPressed: () =>
                          Clipboard.setData(ClipboardData(text: snippet)),
                    ),
                  ),
                ],
              ),
          Align(
            alignment: Alignment.centerLeft,
            child: OutlinedButton.icon(
              key: const Key('open-ntfy'),
              icon: const Icon(Icons.open_in_new),
              label: const Text('Open ntfy app'),
              onPressed: () async {
                final ok = await ref
                    .read(linkOpenerProvider)
                    .open(Uri.parse('ntfy://subscribe'));
                if (!ok && context.mounted) {
                  ScaffoldMessenger.of(context).showSnackBar(
                    const SnackBar(
                      content: Text('ntfy app not found. Install it first.'),
                    ),
                  );
                }
              },
            ),
          ),
        ],
      ),
    );
  }
}
