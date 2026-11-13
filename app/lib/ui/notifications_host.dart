/// Keeps notification state in step with the UI: open session, foreground,
/// and starts the controller (AGED-US-0032).
library;

import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../state/notifications.dart';
import '../state/notifications_model.dart';

class NotificationsHost extends ConsumerStatefulWidget {
  const NotificationsHost({
    required this.router,
    required this.child,
    super.key,
  });

  final GoRouter router;
  final Widget child;

  @override
  ConsumerState<NotificationsHost> createState() => _NotificationsHostState();
}

class _NotificationsHostState extends ConsumerState<NotificationsHost> {
  late final AppLifecycleListener _life;

  @override
  void initState() {
    super.initState();
    widget.router.routerDelegate.addListener(_onRoute);
    _life = AppLifecycleListener(
      onStateChange: (s) {
        ref
            .read(appVisibleProvider.notifier)
            .set(s == AppLifecycleState.resumed);
      },
    );
    // Providers cannot be modified while the tree builds.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _onRoute();
    });
  }

  void _onRoute() {
    final loc = widget.router.routerDelegate.currentConfiguration.uri
        .toString();
    final key = sessionKeyOfLocation(loc);
    if (ref.read(openSessionProvider) != key) {
      ref.read(openSessionProvider.notifier).set(key);
    }
  }

  @override
  void dispose() {
    widget.router.routerDelegate.removeListener(_onRoute);
    _life.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    ref.watch(notificationControllerProvider);
    return widget.child;
  }
}
