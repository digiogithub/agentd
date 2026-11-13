/// App root: theme, routing and deep-link handling.
library;

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../repositories/host.dart';
import '../state/deep_link_router.dart';
import '../state/notifications.dart';
import '../state/providers.dart';
import 'agents/agents_screen.dart';
import 'hosts_screen.dart';
import 'notifications_host.dart';
import 'launch/launch_screen.dart';
import 'mailbox/compose_screen.dart';
import 'mailbox/mailbox_screen.dart';
import 'mailbox/message_screen.dart';
import 'pairing_screen.dart';
import 'session/session_screen.dart';
import 'settings/settings_screen.dart';

final rootMessengerKey = GlobalKey<ScaffoldMessengerState>();

GoRouter buildRouter({String initialLocation = '/'}) => GoRouter(
  initialLocation: initialLocation,
  routes: [
    GoRoute(path: '/', builder: (c, s) => const AgentsScreen()),
    GoRoute(path: '/hosts', builder: (c, s) => const HostsScreen()),
    GoRoute(path: '/settings', builder: (c, s) => const SettingsScreen()),
    GoRoute(
      path: '/pair',
      builder: (c, s) =>
          PairingScreen(rehostId: s.uri.queryParameters['rehost']),
    ),
    GoRoute(
      path: '/hosts/:id/launch',
      builder: (c, s) => LaunchScreen(hostId: s.pathParameters['id']!),
    ),
    GoRoute(path: '/mailbox', builder: (c, s) => const MailboxScreen()),
    GoRoute(
      path: '/mailbox/compose',
      builder: (c, s) => ComposeScreen(
        hostId: s.uri.queryParameters['host'],
        to: s.uri.queryParameters['to'],
      ),
    ),
    GoRoute(
      path: '/hosts/:id/mailbox/:msgId',
      builder: (c, s) => MessageScreen(
        hostId: s.pathParameters['id']!,
        msgId: int.tryParse(s.pathParameters['msgId']!) ?? 0,
      ),
    ),
    GoRoute(
      path: '/hosts/:id/sessions/:name',
      builder: (c, s) => SessionScreen(
        hostId: s.pathParameters['id']!,
        session: s.pathParameters['name']!,
      ),
    ),
  ],
);

final routerProvider = Provider<GoRouter>((ref) {
  final r = buildRouter();
  ref.onDispose(r.dispose);
  return r;
});

ThemeData _theme(Brightness b) => ThemeData(
  colorScheme: ColorScheme.fromSeed(seedColor: Colors.teal, brightness: b),
  useMaterial3: true,
);

class AgentdApp extends ConsumerWidget {
  const AgentdApp({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final router = ref.watch(routerProvider);
    return MaterialApp.router(
      title: 'agentd',
      scaffoldMessengerKey: rootMessengerKey,
      theme: _theme(Brightness.light),
      darkTheme: _theme(Brightness.dark),
      routerConfig: router,
      builder: (context, child) => NotificationsHost(
        router: router,
        child: DeepLinkListener(
          router: router,
          child: child ?? const SizedBox(),
        ),
      ),
    );
  }
}

/// Feeds `agentd://` links (cold start and while running) to the router.
class DeepLinkListener extends ConsumerStatefulWidget {
  const DeepLinkListener({
    required this.router,
    required this.child,
    super.key,
  });

  final GoRouter router;
  final Widget child;

  @override
  ConsumerState<DeepLinkListener> createState() => _DeepLinkListenerState();
}

class _DeepLinkListenerState extends ConsumerState<DeepLinkListener> {
  StreamSubscription<Uri>? _sub;
  StreamSubscription<Uri>? _tapSub;
  final _dedupe = DeepLinkDeduper();

  @override
  void initState() {
    super.initState();
    final src = ref.read(deepLinkSourceProvider);
    _sub = src.links.listen(handle);
    _tapSub = ref.read(notificationTapsProvider).listen(handle);
    unawaited(
      src.initial().then((u) {
        if (u != null) unawaited(handle(u));
      }),
    );
  }

  Future<void> handle(Uri uri) async {
    if (!_dedupe.accept(uri)) return;
    final List<Host> hosts;
    try {
      hosts = await ref.read(hostsProvider.future);
    } on Object catch (e) {
      rootMessengerKey.currentState?.showSnackBar(
        SnackBar(content: Text('Cannot read the paired hosts: $e')),
      );
      return;
    }
    if (!mounted) return;
    switch (routeForDeepLink(uri, hosts)) {
      case DeepLinkRoute(:final location, :final pairLink):
        if (pairLink != null) {
          ref.read(pendingPairLinkProvider).set(pairLink);
        }
        unawaited(widget.router.push(location));
      case DeepLinkChoose(:final hosts, :final session):
        final ctx = widget.router.routerDelegate.navigatorKey.currentContext;
        if (ctx == null) return;
        final picked = await showDialog<Host>(
          // The context was read just above, after the mounted check.
          // ignore: use_build_context_synchronously
          context: ctx,
          builder: (c) => SimpleDialog(
            title: Text('Open "$session" on which host?'),
            children: [
              for (final h in hosts)
                SimpleDialogOption(
                  key: Key('pick-host-${h.id}'),
                  onPressed: () => Navigator.pop(c, h),
                  child: ListTile(
                    title: Text(h.label),
                    subtitle: Text(h.baseUrl.toString()),
                  ),
                ),
            ],
          ),
        );
        if (picked != null && mounted) {
          unawaited(widget.router.push(sessionLocation(picked, session)));
        }
      case DeepLinkIgnored(:final message):
        rootMessengerKey.currentState?.showSnackBar(
          SnackBar(content: Text(message)),
        );
    }
  }

  @override
  void dispose() {
    unawaited(_sub?.cancel());
    unawaited(_tapSub?.cancel());
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => widget.child;
}
